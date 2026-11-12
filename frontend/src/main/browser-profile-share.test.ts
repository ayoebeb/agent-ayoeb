import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { BROWSER_IMPORT_MAX_COOKIES } from "../shared/browser-profile-import";
import type { SharedBrowserProfileSummary } from "../shared/browser-profile-share";
import { BrowserHistoryStore } from "./browser-history-store";
import { BROWSER_PROFILE_COPIES_FILE_NAME, BrowserProfileCopyLedger } from "./browser-profile-copy-ledger";
import { BrowserProfileReceiverUnverifiedError, type VerifiedReceiver } from "./browser-profile-receiver";
import {
	BROWSER_PROFILE_EXPORT_IDLE_MS,
	BROWSER_PROFILE_SHARE_PAGE_BYTES,
	BROWSER_PROFILE_SHARING_FILE_NAME,
	BrowserProfileShareSource,
	BrowserProfileSharingSetting,
	isShareableCookieHost,
	type SharedBrowserCookie,
	type SharedBrowserProfilePage,
} from "./browser-profile-share";
import { createOneTimeKey, openPayload, sealContext, type SealedPayload } from "./browser-profile-share-crypto";
import { BrowserProfileStore } from "./browser-profile-store";

const NOW = new Date("2026-11-10T12:00:00.000Z");
const FUTURE = NOW.getTime() / 1_000 + 86_400;
const REMOTE_ID = "7c9e6679-7425-40de-944b-e07fc1f90ae7";
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

/** The receiving machine's one-time key, and a verifier that accepts only its ticket. */
function receiverFixture() {
	const key = createOneTimeKey();
	const verified: VerifiedReceiver = { sourceHostId: "h_source", receiverHostId: "h_receiver", key: key.publicKey };
	const verifications: unknown[] = [];
	const receivers = {
		verify: async (ticket: unknown, signature: unknown) => {
			verifications.push(ticket);
			if (ticket !== "good-ticket" || signature !== "good-signature") throw new BrowserProfileReceiverUnverifiedError();
			return verified;
		},
	};
	const openList = (payload: SealedPayload) =>
		(openPayload(payload, key, sealContext({ ...verified, action: "list" })) as { profiles: SharedBrowserProfileSummary[] }).profiles;
	const openPage = (payload: SealedPayload, profileId: string, cursor = "") =>
		openPayload(payload, key, sealContext({ ...verified, action: "export", profileId, cursor })) as SharedBrowserProfilePage;
	return { key, verified, receivers, verifications, openList, openPage };
}

const TICKET = { ticket: "good-ticket", signature: "good-signature" };

async function setup(options: { cookies?: (profileId: string) => Cookie[]; sharing?: boolean; dir?: string } = {}) {
	const dir = options.dir ?? await stateDir();
	const store = new BrowserProfileStore({ stateDir: dir, now: () => NOW });
	const history = new BrowserHistoryStore({ stateDir: dir, now: () => NOW });
	const setting = new BrowserProfileSharingSetting(dir);
	if (options.sharing !== false) await setting.setEnabled(true);
	const copies = new BrowserProfileCopyLedger(dir, setting);
	const receiver = receiverFixture();
	const reads: string[] = [];
	const hooks: { onRead?: () => Promise<void> } = {};
	const clock = { now: NOW };
	const source = new BrowserProfileShareSource({
		store,
		history,
		sharing: setting,
		copies,
		receivers: receiver.receivers,
		cookies: async (profileId) => {
			reads.push(profileId);
			await hooks.onRead?.();
			return (options.cookies?.(profileId) ?? []) as never;
		},
		now: () => clock.now,
	});
	return { dir, store, history, setting, copies, source, reads, receiver, hooks, clock };
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

const COPY_SOURCE = {
	kind: "copy" as const,
	browser: "Google Chrome",
	hostId: "h_laptop",
	machineName: "Laptop",
	remoteProfileId: REMOTE_ID,
	copiedAt: NOW.toISOString(),
};

describe("BrowserProfileSharingSetting", () => {
	it("is off by default and when the file is unreadable", async () => {
		const dir = await stateDir();
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(false);
		await writeFile(path.join(dir, BROWSER_PROFILE_SHARING_FILE_NAME), "{ not json");
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(false);
		await writeFile(path.join(dir, BROWSER_PROFILE_SHARING_FILE_NAME), JSON.stringify({ version: 1, shareWithLinkedMachines: "yes" }));
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(false);
	});

	it("persists the owner's choice and counts every time it is turned off", async () => {
		const dir = await stateDir();
		const setting = new BrowserProfileSharingSetting(dir);
		await setting.setEnabled(true);
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(true);
		expect(JSON.parse(await readFile(path.join(dir, BROWSER_PROFILE_SHARING_FILE_NAME), "utf8"))).toEqual({
			version: 1,
			shareWithLinkedMachines: true,
		});
		const before = setting.generation();
		await setting.setEnabled(false);
		expect(setting.generation()).toBe(before + 1);
		expect(await new BrowserProfileSharingSetting(dir).isEnabled()).toBe(false);
	});
});

describe("isShareableCookieHost", () => {
	it("refuses hosts that name a machine rather than a site", () => {
		for (const host of ["localhost", ".localhost", "app.localhost", "127.0.0.1", "192.168.1.1", "10.0.0.8", "[::1]", "::1", "intranet", ".intranet", "router."]) {
			expect(isShareableCookieHost(host), host).toBe(false);
		}
		for (const host of ["example.test", ".example.com", "mail.google.com", "sub.domain.co.uk"]) {
			expect(isShareableCookieHost(host), host).toBe(true);
		}
	});
});

describe("BrowserProfileShareSource", () => {
	it("refuses to list or export while sharing is off, without checking the receiver or reading any profile", async () => {
		const { store, source, reads, receiver } = await setup({ sharing: false });
		const profile = await store.createProfile("Brave");
		await expect(source.list(TICKET)).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARING_OFF" });
		await expect(source.exportPage({ ...TICKET, profileId: profile.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARING_OFF" });
		expect(reads).toEqual([]);
		expect(receiver.verifications).toEqual([]);
	});

	// A local process, a phone, or anything holding only a connection
	// credential has no ticket the receiver's host key signed.
	it("refuses a receiver it cannot verify, without reading any profile", async () => {
		const { store, source, reads } = await setup({ cookies: () => [cookie("sid")] });
		const profile = await store.createProfile("Brave");
		for (const args of [{}, { ticket: "forged", signature: "good-signature" }, { ticket: "good-ticket" }]) {
			await expect(source.list(args)).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
			await expect(source.exportPage({ ...args, profileId: profile.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
		}
		expect(reads).toEqual([]);
	});

	it("seals every answer so only the receiver's one-time key opens it, for that request only", async () => {
		const { store, source, receiver } = await setup({ cookies: () => [cookie("sid")] });
		const profile = await store.createProfile("Brave");
		const page = await source.exportPage({ ...TICKET, profileId: profile.id });
		expect(JSON.stringify(page)).not.toContain("dummy-sid");
		expect(Object.keys(page).sort()).toEqual(["data", "iv", "key", "version"]);
		expect(receiver.openPage(page, profile.id).cookies.map((entry) => entry.value)).toEqual(["dummy-sid"]);
		expect(() => openPayload(page, createOneTimeKey(), sealContext({ ...receiver.verified, action: "export", profileId: profile.id, cursor: "" })))
			.toThrow();
		// Bound to the request: the same page does not open as a list, or as another page.
		expect(() => receiver.openList(page)).toThrow();
		expect(() => receiver.openPage(page, profile.id, "1:0")).toThrow();
	});

	it("lists only this machine's own profiles", async () => {
		const { store, history, source, receiver, copies } = await setup({
			cookies: () => [
				cookie("sid"),
				cookie("expired", { expirationDate: NOW.getTime() / 1_000 - 1 }),
				cookie("cf_clearance"),
				cookie("session-login", { session: true, expirationDate: undefined }),
				cookie("dev-session", { domain: "localhost" }),
				cookie("router", { domain: "192.168.1.1" }),
				cookie("wiki", { domain: ".intranet", hostOnly: false }),
			],
		});
		const brave = await store.createProfile("Brave", { source: { kind: "import", browser: "Brave" } });
		const work = await store.createProfile("Work");
		const copied = await store.createProfile("Chrome (from Laptop)", { source: COPY_SOURCE });
		await copies.record(copied.id, COPY_SOURCE, store.profiles.map((profile) => profile.id));
		await history.mergeImportedEntries(brave.id, [
			{ url: "https://example.test/a", lastVisited: "2026-11-10T12:30:00.000Z", visitCount: 1 },
			{ url: "https://example.test/b", lastVisited: "2026-11-09T11:00:00.000Z", visitCount: 3 },
		]);
		const listed = receiver.openList(await source.list(TICKET));
		expect(listed.map((profile) => profile.id).sort()).toEqual([brave.id, work.id].sort());
		expect(listed.find((profile) => profile.id === brave.id)).toEqual({
			id: brave.id,
			name: "Brave",
			sourceBrowser: "Brave",
			// sid and the session sign-in; expired, anti-bot and machine-host cookies are left out.
			cookieCount: 2,
			historyCount: 2,
			// The newest visit is later than the record itself.
			updatedAt: "2026-11-10T12:30:00.000Z",
		});
	});

	// Probe P2 of the security review: an older build rewrites the registry
	// without the provenance it does not know. The copy must stay withheld.
	it("withholds a copy whose provenance an older build stripped from the registry", async () => {
		const dir = await stateDir();
		const first = await setup({ dir, cookies: () => [cookie("sid")] });
		const copied = await first.store.createProfile("Chrome (from Laptop)", { source: COPY_SOURCE });
		await first.copies.record(copied.id, COPY_SOURCE, first.store.profiles.map((profile) => profile.id));
		const registryPath = path.join(dir, "browser-profiles.json");
		const registry = JSON.parse(await readFile(registryPath, "utf8")) as { profiles: Record<string, unknown>[] };
		registry.profiles = registry.profiles.map(({ source: _source, ...rest }) => rest);
		await writeFile(registryPath, JSON.stringify(registry));

		const reloaded = await setup({ dir, cookies: () => [cookie("sid")] });
		await reloaded.store.load();
		expect(reloaded.store.getProfile(copied.id)?.source).toBeUndefined();
		expect(reloaded.receiver.openList(await reloaded.source.list(TICKET))).toEqual([]);
		await expect(reloaded.source.exportPage({ ...TICKET, profileId: copied.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_SHARED" });
	});

	it("withholds a profile whose provenance this build cannot read", async () => {
		const dir = await stateDir();
		const first = await setup({ dir });
		const profile = await first.store.createProfile("Mystery");
		const registryPath = path.join(dir, "browser-profiles.json");
		const registry = JSON.parse(await readFile(registryPath, "utf8")) as { profiles: Record<string, unknown>[] };
		registry.profiles[0]!.source = { kind: "synced-from-the-future", hostId: 7 };
		await writeFile(registryPath, JSON.stringify(registry));

		const reloaded = await setup({ dir });
		await reloaded.store.load();
		expect(reloaded.store.getProfile(profile.id)?.source).toEqual({ kind: "unknown" });
		expect(reloaded.receiver.openList(await reloaded.source.list(TICKET))).toEqual([]);
		await expect(reloaded.source.exportPage({ ...TICKET, profileId: profile.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_SHARED" });
	});

	// Review finding N2: a ledger that is deleted after this machine started
	// keeping one is lost, not empty, and withholds everything until the owner
	// turns sharing on again, which rebuilds it from the registry.
	it("shares nothing once a kept copy ledger is gone, until sharing is turned on again", async () => {
		const { dir, store, source, copies, setting, receiver } = await setup({ cookies: () => [cookie("sid")] });
		const copied = await store.createProfile("Chrome (from Laptop)", { source: COPY_SOURCE });
		const own = await store.createProfile("Work");
		await copies.record(copied.id, COPY_SOURCE, store.profiles.map((profile) => profile.id));
		expect(JSON.parse(await readFile(path.join(dir, BROWSER_PROFILE_SHARING_FILE_NAME), "utf8"))).toMatchObject({ copyLedger: true });
		await rm(path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME));
		await expect(source.list(TICKET)).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARE_FAILED" });
		await expect(source.exportPage({ ...TICKET, profileId: own.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARE_FAILED" });

		// "On" while already on is not the owner's choice again: still closed.
		await setting.setEnabled(true);
		await expect(source.list(TICKET)).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARE_FAILED" });

		await setting.setEnabled(false);
		await setting.setEnabled(true);
		expect(receiver.openList(await source.list(TICKET)).map((profile) => profile.id)).toEqual([own.id]);
		const rebuilt = JSON.parse(await readFile(path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME), "utf8")) as { copies: Record<string, unknown> };
		expect(Object.keys(rebuilt.copies)).toEqual([copied.id]);
	});

	it("records copies an earlier build made before the ledger, so stripping them later shares nothing", async () => {
		const dir = await stateDir();
		const first = await setup({ dir, cookies: () => [cookie("sid")] });
		// A copy made by a build that kept no ledger: provenance in the registry only.
		const copied = await first.store.createProfile("Chrome (from Laptop)", { source: COPY_SOURCE });
		await expect(readFile(path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME), "utf8")).rejects.toMatchObject({ code: "ENOENT" });
		expect(first.receiver.openList(await first.source.list(TICKET))).toEqual([]);
		expect([...(await first.copies.load()).ids]).toEqual([copied.id]);

		const registryPath = path.join(dir, "browser-profiles.json");
		const registry = JSON.parse(await readFile(registryPath, "utf8")) as { profiles: Record<string, unknown>[] };
		registry.profiles = registry.profiles.map(({ source: _source, ...rest }) => rest);
		await writeFile(registryPath, JSON.stringify(registry));
		const reloaded = await setup({ dir, cookies: () => [cookie("sid")] });
		expect(reloaded.receiver.openList(await reloaded.source.list(TICKET))).toEqual([]);
		await expect(reloaded.source.exportPage({ ...TICKET, profileId: copied.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_SHARED" });
	});

	it("shares nothing while the copy ledger cannot be read", async () => {
		const { dir, store, source } = await setup();
		const profile = await store.createProfile("Work");
		await writeFile(path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME), "{ damaged");
		await expect(source.list(TICKET)).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARE_FAILED" });
		await expect(source.exportPage({ ...TICKET, profileId: profile.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARE_FAILED" });
	});

	// Probe P3 of the security review: the owner turns sharing off while a
	// page is being read. That page must not be answered either.
	it("does not answer a read that was running when sharing was turned off", async () => {
		const { store, source, setting, hooks } = await setup({ cookies: () => [cookie("sid")] });
		const profile = await store.createProfile("Brave");
		hooks.onRead = async () => {
			hooks.onRead = undefined;
			await setting.setEnabled(false);
			await setting.setEnabled(true);
		};
		await expect(source.exportPage({ ...TICKET, profileId: profile.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARING_OFF" });
		hooks.onRead = async () => {
			hooks.onRead = undefined;
			await setting.setEnabled(false);
		};
		await expect(source.list(TICKET)).rejects.toMatchObject({ code: "BROWSER_PROFILE_SHARING_OFF" });
	});

	it("never exports a copied profile and validates the request", async () => {
		const { store, source } = await setup();
		const copied = await store.createProfile("Chrome (from Laptop)", { source: COPY_SOURCE });
		const own = await store.createProfile("Work");
		await expect(source.exportPage({ ...TICKET, profileId: copied.id })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_SHARED" });
		await expect(source.exportPage({ ...TICKET, profileId: "0f8fad5b-d9cb-469f-a165-70867728950e" })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_FOUND" });
		await expect(source.exportPage({ ...TICKET, profileId: "../etc" })).rejects.toMatchObject({ code: "BROWSER_PROFILE_NOT_FOUND" });
		await expect(source.exportPage({ ...TICKET, profileId: own.id, cursor: "../../x" })).rejects.toMatchObject({ code: "INVALID_CURSOR" });
		await expect(source.exportPage({ ...TICKET, profileId: own.id, cursor: "5:0" })).rejects.toMatchObject({ code: "INVALID_CURSOR" });
	});

	it("pages a large profile under the bridge budget and covers every cookie and visit once", async () => {
		const big = "v".repeat(3_900);
		const all = Array.from({ length: 1_500 }, (_, index) => cookie(`c${index.toString().padStart(4, "0")}`, { value: big }));
		const { store, history, source, receiver } = await setup({ cookies: () => all });
		const profile = await store.createProfile("Brave");
		await history.mergeImportedEntries(profile.id, Array.from({ length: 50 }, (_, index) => ({
			url: `https://example.test/page-${index}`,
			lastVisited: NOW.toISOString(),
			visitCount: 1,
		})));
		const cookies: SharedBrowserCookie[] = [];
		const visits: string[] = [];
		let cursor: string | undefined;
		let pages = 0;
		do {
			const sealed = await source.exportPage({ ...TICKET, profileId: profile.id, ...(cursor ? { cursor } : {}) });
			// Sealed, a page still fits the desktop bridge frame.
			expect(Buffer.byteLength(JSON.stringify(sealed))).toBeLessThanOrEqual(Math.ceil(BROWSER_PROFILE_SHARE_PAGE_BYTES * 4 / 3) + 256);
			const page = receiver.openPage(sealed, profile.id, cursor ?? "");
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

	// Review follow-up: pages walk one snapshot read at the first page, so the
	// owner browsing mid-export cannot shift a page past a cookie.
	it("walks one snapshot per export, so a cookie cleared mid-export does not skip another", async () => {
		const big = "v".repeat(3_900);
		let jar = Array.from({ length: 1_500 }, (_, index) => cookie(`c${index.toString().padStart(4, "0")}`, { value: big }));
		const { store, source, receiver, reads } = await setup({ cookies: () => jar });
		const profile = await store.createProfile("Brave");
		const first = receiver.openPage(await source.exportPage({ ...TICKET, profileId: profile.id }), profile.id);
		expect(first.nextCursor).toMatch(/^[0-9a-f]{32}:[0-9]+:0$/);
		// The owner browses here: a cookie already sent is cleared.
		jar = jar.slice(1);
		const names = first.cookies.map((entry) => entry.name);
		let cursor = first.nextCursor;
		while (cursor) {
			const page = receiver.openPage(await source.exportPage({ ...TICKET, profileId: profile.id, cursor }), profile.id, cursor);
			names.push(...page.cookies.map((entry) => entry.name));
			cursor = page.nextCursor;
		}
		expect(names).toHaveLength(1_500);
		expect(new Set(names).size).toBe(1_500);
		expect(reads).toEqual([profile.id]);
		// Finished, its snapshot is gone.
		await expect(source.exportPage({ ...TICKET, profileId: profile.id, cursor: first.nextCursor })).rejects.toMatchObject({ code: "INVALID_CURSOR" });
	});

	it("takes a cursor only for its machine, profile and sharing, and not after it idles", async () => {
		const big = "v".repeat(3_900);
		const jar = Array.from({ length: 1_500 }, (_, index) => cookie(`c${index}`, { value: big }));
		const { store, source, receiver, setting, clock } = await setup({ cookies: () => jar });
		const profile = await store.createProfile("Brave");
		const other = await store.createProfile("Work");
		const start = async () => receiver.openPage(await source.exportPage({ ...TICKET, profileId: profile.id }), profile.id).nextCursor!;
		const next = (cursor: string, profileId = profile.id) => source.exportPage({ ...TICKET, profileId, cursor });

		const cursor = await start();
		await expect(next(cursor, other.id)).rejects.toMatchObject({ code: "INVALID_CURSOR" });
		await expect(next(`${"f".repeat(32)}:1:0`)).rejects.toMatchObject({ code: "INVALID_CURSOR" });
		receiver.verified.receiverHostId = "h_another_machine";
		await expect(next(cursor)).rejects.toMatchObject({ code: "INVALID_CURSOR" });
		receiver.verified.receiverHostId = "h_receiver";
		await expect(next(cursor)).resolves.toBeDefined();

		// Turning sharing off ends every export, even once it is back on.
		const beforeOff = await start();
		await setting.setEnabled(false);
		await setting.setEnabled(true);
		await expect(next(beforeOff)).rejects.toMatchObject({ code: "INVALID_CURSOR" });

		const idle = await start();
		clock.now = new Date(NOW.getTime() + BROWSER_PROFILE_EXPORT_IDLE_MS + 1);
		await expect(next(idle)).rejects.toMatchObject({ code: "INVALID_CURSOR" });
	});

	it("caps a profile at the cookie import limit", async () => {
		const many = Array.from({ length: BROWSER_IMPORT_MAX_COOKIES + 5 }, (_, index) => cookie(`c${index}`));
		const { store, source, receiver } = await setup({ cookies: () => many });
		const profile = await store.createProfile("Brave");
		expect(receiver.openList(await source.list(TICKET))[0]?.cookieCount).toBe(BROWSER_IMPORT_MAX_COOKIES);
		const first = receiver.openPage(await source.exportPage({ ...TICKET, profileId: profile.id }), profile.id);
		expect(first.profile.cookieCount).toBe(BROWSER_IMPORT_MAX_COOKIES);
	});
});
