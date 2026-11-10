import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { BROWSER_IMPORT_MAX_COOKIES } from "../shared/browser-profile-import";
import { BrowserHistoryStore } from "./browser-history-store";
import {
	BROWSER_PROFILE_SHARE_PAGE_BYTES,
	BROWSER_PROFILE_SHARING_FILE_NAME,
	BrowserProfileShareSource,
	BrowserProfileSharingSetting,
	type SharedBrowserCookie,
} from "./browser-profile-share";
import { BrowserProfileStore } from "./browser-profile-store";

const NOW = new Date("2026-11-10T12:00:00.000Z");
const FUTURE = NOW.getTime() / 1_000 + 86_400;
const tempDirectories: string[] = [];

async function stateDir(): Promise<string> {
	const directory = await mkdtemp(path.join(os.tmpdir(), "ao-browser-share-"));
	tempDirectories.push(directory);
	return directory;
}

afterEach(async () => {
	await Promise.all(tempDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

type Cookie = Record<string, unknown>;

async function setup(cookiesByProfile: Record<string, Cookie[]> = {}, sharing = true) {
	const dir = await stateDir();
	const store = new BrowserProfileStore({ stateDir: dir, now: () => NOW });
	const history = new BrowserHistoryStore({ stateDir: dir, now: () => NOW });
	const setting = new BrowserProfileSharingSetting(dir);
	if (sharing) await setting.setEnabled(true);
	const reads: string[] = [];
	const source = new BrowserProfileShareSource({
		store,
		history,
		sharing: setting,
		cookies: async (profileId) => {
			reads.push(profileId);
			return (cookiesByProfile[profileId] ?? []) as never;
		},
		now: () => NOW,
	});
	return { dir, store, history, setting, source, reads };
}

function cookie(name: string, overrides: Record<string, unknown> = {}): Cookie {
	return {
		name,
		value: `dummy-${name}`,
		domain: "example.test",
		hostOnly: true,
		path: "/",
		secure: true,
		httpOnly: true,
		session: false,
		expirationDate: FUTURE,
		sameSite: "lax",
		...overrides,
	} as Cookie;
}

describe("BrowserProfileSharingSetting", () => {
	it("is off by default and when the file is unreadable", async () => {
		const dir = await stateDir();
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(false);
		await writeFile(path.join(dir, BROWSER_PROFILE_SHARING_FILE_NAME), "{ not json");
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(false);
		await writeFile(path.join(dir, BROWSER_PROFILE_SHARING_FILE_NAME), JSON.stringify({ version: 1, shareWithLinkedMachines: "yes" }));
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(false);
	});

	it("persists the owner's choice", async () => {
		const dir = await stateDir();
		await new BrowserProfileSharingSetting(dir).setEnabled(true);
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(true);
		expect(JSON.parse(await readFile(path.join(dir, BROWSER_PROFILE_SHARING_FILE_NAME), "utf8"))).toEqual({
			version: 1,
			shareWithLinkedMachines: true,
		});
		await new BrowserProfileSharingSetting(dir).setEnabled(false);
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(false);
	});
});

describe("BrowserProfileShareSource", () => {
	it("refuses to list or export while sharing is off, without reading any profile", async () => {
		const { store, source, reads } = await setup({}, false);
		const profile = await store.createProfile("Brave");
		await expect(source.list()).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARING_OFF" });
		await expect(source.exportPage({ profileId: profile.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARING_OFF" });
		expect(reads).toEqual([]);
	});

	it("lists only profiles that were not copied from another machine", async () => {
		const { store, history } = await setup();
		const brave = await store.createProfile("Brave", { source: { kind: "import", browser: "Brave" } });
		await store.createProfile("Chrome (from Laptop)", {
			source: {
				kind: "copy",
				browser: "Google Chrome",
				hostId: "h_laptop",
				machineName: "Laptop",
				remoteProfileId: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
				copiedAt: NOW.toISOString(),
			},
		});
		await history.mergeImportedEntries(brave.id, [
			{ url: "https://example.test/a", lastVisited: "2026-11-10T12:30:00.000Z", visitCount: 1 },
			{ url: "https://example.test/b", lastVisited: "2026-11-09T11:00:00.000Z", visitCount: 3 },
		]);
		const listed = await new BrowserProfileShareSource({
			store,
			history,
			sharing: { isEnabled: async () => true },
			cookies: async () => [
				cookie("sid"),
				cookie("expired", { expirationDate: NOW.getTime() / 1_000 - 1 }),
				cookie("cf_clearance"),
				cookie("session-login", { session: true, expirationDate: undefined }),
			] as never,
			now: () => NOW,
		}).list();
		expect(listed.profiles).toEqual([{
			id: brave.id,
			name: "Brave",
			sourceBrowser: "Brave",
			cookieCount: 2,
			historyCount: 2,
			// The newest visit is later than the record itself.
			updatedAt: "2026-11-10T12:30:00.000Z",
		}]);
	});

	it("never exports a copied profile and validates the request", async () => {
		const { store, source } = await setup();
		const copied = await store.createProfile("Chrome (from Laptop)", {
			source: {
				kind: "copy",
				hostId: "h_laptop",
				machineName: "Laptop",
				remoteProfileId: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
				copiedAt: NOW.toISOString(),
			},
		});
		const own = await store.createProfile("Work");
		await expect(source.exportPage({ profileId: copied.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_SHARED" });
		await expect(source.exportPage({ profileId: "0f8fad5b-d9cb-469f-a165-70867728950e" })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_FOUND" });
		await expect(source.exportPage({ profileId: "../etc" })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_FOUND" });
		await expect(source.exportPage({ profileId: own.id, cursor: "../../x" })).rejects.toMatchObject({ code: "INVALID_CURSOR" });
		await expect(source.exportPage({ profileId: own.id, cursor: "5:0" })).rejects.toMatchObject({ code: "INVALID_CURSOR" });
	});

	it("pages a large profile under the bridge budget and covers every cookie and visit once", async () => {
		const big = "v".repeat(3_900);
		const dir = await stateDir();
		const store = new BrowserProfileStore({ stateDir: dir, now: () => NOW });
		const history = new BrowserHistoryStore({ stateDir: dir, now: () => NOW });
		const profile = await store.createProfile("Brave");
		await history.mergeImportedEntries(profile.id, Array.from({ length: 50 }, (_, index) => ({
			url: `https://example.test/page-${index}`,
			lastVisited: NOW.toISOString(),
			visitCount: 1,
		})));
		const all = Array.from({ length: 1_500 }, (_, index) => cookie(`c${index.toString().padStart(4, "0")}`, { value: big }));
		const source = new BrowserProfileShareSource({
			store,
			history,
			sharing: { isEnabled: async () => true },
			cookies: async () => all as never,
			now: () => NOW,
		});
		const cookies: SharedBrowserCookie[] = [];
		const visits: string[] = [];
		let cursor: string | undefined;
		let pages = 0;
		do {
			const page = await source.exportPage({ profileId: profile.id, ...(cursor ? { cursor } : {}) });
			pages += 1;
			expect(Buffer.byteLength(JSON.stringify(page))).toBeLessThanOrEqual(BROWSER_PROFILE_SHARE_PAGE_BYTES);
			expect(page.profile.cookieCount).toBe(1_500);
			cookies.push(...page.cookies);
			visits.push(...page.history.map((entry) => entry.url));
			cursor = page.nextCursor;
		} while (cursor);
		expect(pages).toBeGreaterThan(1);
		expect(new Set(cookies.map((entry) => entry.name)).size).toBe(1_500);
		expect(cookies).toHaveLength(1_500);
		expect(new Set(visits).size).toBe(50);
	});

	it("caps a profile at the cookie import limit", async () => {
		const many = Array.from({ length: BROWSER_IMPORT_MAX_COOKIES + 5 }, (_, index) => cookie(`c${index}`));
		const dir = await stateDir();
		const store = new BrowserProfileStore({ stateDir: dir, now: () => NOW });
		const profile = await store.createProfile("Brave");
		const source = new BrowserProfileShareSource({
			store,
			history: new BrowserHistoryStore({ stateDir: dir }),
			sharing: { isEnabled: async () => true },
			cookies: async () => many as never,
			now: () => NOW,
		});
		expect((await source.list()).profiles[0]?.cookieCount).toBe(BROWSER_IMPORT_MAX_COOKIES);
		const first = await source.exportPage({ profileId: profile.id });
		expect(first.profile.cookieCount).toBe(BROWSER_IMPORT_MAX_COOKIES);
	});
});
