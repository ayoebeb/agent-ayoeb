import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import type { CookiesSetDetails } from "electron";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BROWSER_IMPORT_MAX_COOKIES } from "../shared/browser-profile-import";
import { BROWSER_PROFILE_MAX_COUNT, browserProfilePartition } from "../shared/browser-profiles";
import { BrowserHistoryStore } from "./browser-history-store";
import {
	BrowserProfileCopyService,
	LinkedMachinesSignedOutError,
	copiedProfileName,
	receivedCookieDetails,
	type LinkedMachine,
} from "./browser-profile-copy";
import { BrowserProfileStore } from "./browser-profile-store";

const NOW = new Date("2026-11-10T12:00:00.000Z");
const FUTURE = NOW.getTime() / 1_000 + 86_400;
const REMOTE_PROFILE = "0f8fad5b-d9cb-469f-a165-70867728950e";
const SECRET = "dummy-secret-cookie-value-7f3a";
const MACHINE: LinkedMachine = { hostId: "h_brave_box", name: "Brave box", url: "http://192.0.2.10:3003", credential: "pairing-password" };
const tempDirectories: string[] = [];

afterEach(async () => {
	vi.restoreAllMocks();
	await Promise.all(tempDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

function summary(overrides: Record<string, unknown> = {}) {
	return { id: REMOTE_PROFILE, name: "Brave", sourceBrowser: "Brave", cookieCount: 2, historyCount: 1, updatedAt: "2026-11-10T10:00:00.000Z", ...overrides };
}

function wireCookie(name: string, overrides: Record<string, unknown> = {}) {
	return { name, value: SECRET, domain: "example.test", hostOnly: true, path: "/", secure: true, httpOnly: true, sameSite: "lax", expirationDate: FUTURE, ...overrides };
}

type Route = (url: URL, init: RequestInit) => { status: number; body: unknown } | Promise<{ status: number; body: unknown }>;

function jsonResponse(status: number, body: unknown): Response {
	return new Response(body === undefined ? "" : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

async function setup(options: {
	route?: Route;
	identity?: (url: string) => Promise<string>;
	machines?: () => Promise<LinkedMachine[]>;
	live?: (profileId: string) => boolean;
	setCookie?: (partition: string, details: CookiesSetDetails) => Promise<void>;
} = {}) {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-browser-copy-"));
	tempDirectories.push(dir);
	const store = new BrowserProfileStore({ stateDir: dir, now: () => NOW });
	const history = new BrowserHistoryStore({ stateDir: dir, now: () => NOW });
	const written: Array<{ partition: string; details: CookiesSetDetails }> = [];
	const cleared: string[] = [];
	const requests: Array<{ url: string; headers: Record<string, string> }> = [];
	const fetchImpl = vi.fn(async (input: string | URL | Request, init: RequestInit = {}) => {
		const url = new URL(String(input));
		requests.push({ url: url.href, headers: Object.fromEntries(new Headers(init.headers).entries()) });
		const answer = await (options.route ?? defaultRoute)(url, init);
		return jsonResponse(answer.status, answer.body);
	});
	const identity = vi.fn(options.identity ?? (async () => MACHINE.hostId));
	const service = new BrowserProfileCopyService({
		store,
		history,
		linkedMachines: options.machines ?? (async () => [MACHINE]),
		identity: (url) => identity(url),
		localHostId: async () => "h_this_machine",
		setCookie: options.setCookie ?? (async (partition, details) => { written.push({ partition, details }); }),
		clearProfileData: async (profileId) => { cleared.push(profileId); },
		isProfileLive: options.live ?? (() => false),
		fetch: fetchImpl as unknown as typeof fetch,
		now: () => NOW,
	});
	return { store, history, service, written, cleared, requests, fetchImpl, identity };
}

const defaultRoute: Route = (url) => {
	if (url.pathname === "/api/v1/shared-browser-profiles") return { status: 200, body: { profiles: [summary()] } };
	if (url.pathname === `/api/v1/shared-browser-profiles/${REMOTE_PROFILE}/export`) {
		return {
			status: 200,
			body: {
				profile: summary(),
				cookies: [wireCookie("sid"), wireCookie("pref", { hostOnly: false, domain: ".example.test" })],
				history: [{ url: "https://example.test/inbox", title: "Inbox", lastVisited: "2026-11-10T09:00:00.000Z", visitCount: 4 }],
			},
		};
	}
	return { status: 404, body: { code: "ROUTE_NOT_FOUND" } };
};

describe("receivedCookieDetails", () => {
	const now = NOW.getTime() / 1_000;
	it("keeps host-only cookies host-only and domain cookies on their domain", () => {
		expect(receivedCookieDetails(wireCookie("sid"), now)).toEqual({ details: expect.objectContaining({ url: "https://example.test/", name: "sid", path: "/" }) });
		expect("domain" in (receivedCookieDetails(wireCookie("sid"), now) as { details: CookiesSetDetails }).details).toBe(false);
		expect(receivedCookieDetails(wireCookie("pref", { hostOnly: false, domain: "Example.TEST" }), now)).toEqual({
			details: expect.objectContaining({ domain: ".example.test", url: "https://example.test/" }),
		});
	});

	it("skips what a linked machine must not plant", () => {
		for (const bad of [
			wireCookie("a", { domain: "evil.test/../x" }),
			wireCookie("a", { domain: "user@evil.test" }),
			wireCookie("a", { domain: "evil.test:8080" }),
			wireCookie("a=b"),
			wireCookie("a", { value: "x;y" }),
			wireCookie("a", { value: "x".repeat(5_000) }),
			wireCookie("a", { path: "nope" }),
			wireCookie("a", { sameSite: "sometimes" }),
			wireCookie("a", { secure: "yes" }),
			"not a cookie",
		]) {
			expect(receivedCookieDetails(bad, now)).toEqual({ skip: "invalid" });
		}
		expect(receivedCookieDetails(wireCookie("a", { expirationDate: now - 5 }), now)).toEqual({ skip: "expired" });
		expect(receivedCookieDetails(wireCookie("cf_clearance"), now)).toEqual({ skip: "anti-bot" });
	});
});

describe("copiedProfileName", () => {
	it("uses the localized name when it is free, and stays within the name limit otherwise", () => {
		expect(copiedProfileName("Brave", "Laptop", [], "Brave (von Laptop)")).toBe("Brave (von Laptop)");
		expect(copiedProfileName("Brave", "Laptop", ["brave (from laptop)"])).toBe("Brave (from Laptop) 2");
		const long = copiedProfileName("P".repeat(64), "M".repeat(100), []);
		expect(long.length).toBeLessThanOrEqual(64);
		expect(long).toContain("(from ");
	});
});

describe("BrowserProfileCopyService.listMachines", () => {
	it("reports a signed-out account without contacting anything", async () => {
		const { service, fetchImpl, identity } = await setup({ machines: async () => { throw new LinkedMachinesSignedOutError(); } });
		expect(await service.listMachines()).toEqual({ signedIn: false, machines: [] });
		expect(fetchImpl).not.toHaveBeenCalled();
		expect(identity).not.toHaveBeenCalled();
	});

	it("never sends the credential to an address that does not answer as the paired machine", async () => {
		const offline = await setup({ identity: async () => { throw new Error("ECONNREFUSED"); } });
		expect((await offline.service.listMachines()).machines[0]).toMatchObject({ name: "Brave box", state: "offline", profiles: [] });
		expect(offline.fetchImpl).not.toHaveBeenCalled();

		const reassigned = await setup({ identity: async () => "h_someone_else" });
		expect((await reassigned.service.listMachines()).machines[0]?.state).toBe("unauthorized");
		expect(reassigned.fetchImpl).not.toHaveBeenCalled();

		const itself = await setup({ identity: async () => "h_this_machine" });
		expect((await itself.service.listMachines()).machines[0]?.state).toBe("unauthorized");
		expect(itself.fetchImpl).not.toHaveBeenCalled();
	});

	it("maps the other machine's refusals to states the UI explains", async () => {
		for (const [status, code, state] of [
			[403, "BROWSER_PROFILE_SHARING_OFF", "sharing-off"],
			[401, "BAD_PASSWORD", "unauthorized"],
			[421, "HOST_ID_MISMATCH", "unauthorized"],
			[404, "ROUTE_NOT_FOUND", "outdated"],
			[503, "DESKTOP_UNAVAILABLE", "desktop-closed"],
			[502, "SHARED_BROWSER_PROFILES_FAILED", "failed"],
		] as const) {
			const { service } = await setup({ route: () => ({ status, body: { code } }) });
			expect((await service.listMachines()).machines[0]?.state, code).toBe(state);
		}
	});

	it("lists valid profiles with the credential and links this machine's copies", async () => {
		const { service, requests, store } = await setup({
			route: () => ({ status: 200, body: { profiles: [summary(), summary({ id: "nope" }), summary({ id: "7c9e6679-7425-40de-944b-e07fc1f90ae7", cookieCount: -1 })] } }),
		});
		const copy = await store.createProfile("Brave (from Brave box)", {
			source: { kind: "copy", hostId: MACHINE.hostId, machineName: MACHINE.name, remoteProfileId: REMOTE_PROFILE, copiedAt: NOW.toISOString() },
		});
		const result = await service.listMachines();
		expect(result.machines).toEqual([{
			hostId: MACHINE.hostId,
			name: "Brave box",
			state: "online",
			profiles: [{ ...summary(), localCopy: { profileId: copy.id, copiedAt: NOW.toISOString() } }],
		}]);
		expect(requests[0]?.headers).toMatchObject({ authorization: "Bearer pairing-password", "x-ao-expected-host-id": MACHINE.hostId });
	});
});

describe("BrowserProfileCopyService.copy", () => {
	it("creates a normal local profile with the cookies, history and its origin", async () => {
		const { service, store, history, written } = await setup();
		const result = await service.copy(MACHINE.hostId, REMOTE_PROFILE, "Brave (from Brave box)");
		expect(result).toMatchObject({ importedCookies: 2, skippedCookies: 0, importedHistoryEntries: 1 });
		const profile = result.profile!;
		expect(store.getProfile(profile.id)).toMatchObject({
			name: "Brave (from Brave box)",
			source: { kind: "copy", browser: "Brave", hostId: MACHINE.hostId, machineName: "Brave box", remoteProfileId: REMOTE_PROFILE },
		});
		// The first profile becomes the default, as after an import.
		expect(store.getDefaultProfileId()).toBe(profile.id);
		expect(written.every(({ partition }) => partition === browserProfilePartition(profile.id))).toBe(true);
		expect(written.map(({ details }) => details.name).sort()).toEqual(["pref", "sid"]);
		expect((await history.entries(profile.id)).map((entry) => entry.url)).toEqual(["https://example.test/inbox"]);
	});

	it("follows the export cursor and rejects one that does not advance", async () => {
		let pages = 0;
		const paged = await setup({
			route: (url) => {
				if (!url.pathname.endsWith("/export")) return { status: 404, body: {} };
				pages += 1;
				const cursor = url.searchParams.get("cursor");
				return cursor === null
					? { status: 200, body: { profile: summary(), cookies: [wireCookie("a")], history: [], nextCursor: "1:0" } }
					: { status: 200, body: { profile: summary(), cookies: [wireCookie("b")], history: [] } };
			},
		});
		expect(await paged.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ importedCookies: 2 });
		expect(pages).toBe(2);

		const looping = await setup({
			route: () => ({ status: 200, body: { profile: summary(), cookies: [wireCookie("a")], history: [], nextCursor: "1:0" } }),
		});
		expect(await looping.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "INVALID_DATA" });
		expect(looping.store.profiles).toEqual([]);
	});

	it("validates everything before writing: over-limit or mismatched data writes nothing", async () => {
		const tooMany = Array.from({ length: BROWSER_IMPORT_MAX_COOKIES + 1 }, (_, index) => wireCookie(`c${index}`));
		for (const body of [
			{ profile: summary(), cookies: tooMany, history: [] },
			{ profile: summary({ id: "7c9e6679-7425-40de-944b-e07fc1f90ae7" }), cookies: [wireCookie("a")], history: [] },
			{ profile: summary(), cookies: "nope", history: [] },
		]) {
			const { service, store, written } = await setup({ route: () => ({ status: 200, body }) });
			expect(await service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "INVALID_DATA" });
			expect(store.profiles).toEqual([]);
			expect(written).toEqual([]);
		}
	});

	it("explains a machine whose sharing is off, and refuses duplicates and a full profile list", async () => {
		const off = await setup({ route: () => ({ status: 403, body: { code: "BROWSER_PROFILE_SHARING_OFF" } }) });
		expect(await off.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "SHARING_OFF" });

		const twice = await setup();
		await twice.service.copy(MACHINE.hostId, REMOTE_PROFILE);
		expect(await twice.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "ALREADY_COPIED" });

		const full = await setup();
		for (let index = 0; index < BROWSER_PROFILE_MAX_COUNT; index += 1) await full.store.createProfile(`P${index}`);
		expect(await full.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "PROFILE_LIMIT" });
		expect(full.fetchImpl).not.toHaveBeenCalled();

		const unlinked = await setup();
		expect(await unlinked.service.copy("h_unknown", REMOTE_PROFILE)).toMatchObject({ code: "NOT_LINKED" });
	});

	it("keeps cookie values out of logs and errors even when Electron rejects a cookie", async () => {
		const consoleCalls: unknown[][] = [];
		for (const method of ["log", "info", "warn", "error", "debug"] as const) {
			vi.spyOn(console, method).mockImplementation((...args: unknown[]) => { consoleCalls.push(args); });
		}
		const { service } = await setup({
			setCookie: async (_partition, details) => {
				if (details.name === "sid") throw new Error(`Failed to set cookie ${details.name}=${details.value}`);
			},
		});
		const result = await service.copy(MACHINE.hostId, REMOTE_PROFILE);
		expect(result).toMatchObject({ importedCookies: 1, skippedCookies: 1 });
		const failing = await setup({ route: () => ({ status: 200, body: { profile: summary(), cookies: [wireCookie("a")], history: "x" } }) });
		const failed = await failing.service.copy(MACHINE.hostId, REMOTE_PROFILE);
		expect(JSON.stringify([result, failed, consoleCalls])).not.toContain(SECRET);
	});
});

describe("BrowserProfileCopyService.update", () => {
	it("replaces the copy's contents and records when it was refreshed", async () => {
		let round = 0;
		const { service, store, history, cleared, written } = await setup({
			route: (url) => {
				if (!url.pathname.endsWith("/export")) return { status: 404, body: {} };
				round += 1;
				return {
					status: 200,
					body: {
						profile: summary(),
						cookies: [wireCookie(round === 1 ? "old" : "new")],
						history: [{ url: `https://example.test/round-${round}`, lastVisited: NOW.toISOString(), visitCount: 1 }],
					},
				};
			},
		});
		const created = (await service.copy(MACHINE.hostId, REMOTE_PROFILE)).profile!;
		const updated = await service.update(created.id);
		expect(updated.error).toBeUndefined();
		expect(cleared).toEqual([created.id]);
		expect(written.map(({ details }) => details.name)).toEqual(["old", "new"]);
		expect((await history.entries(created.id)).map((entry) => entry.url)).toEqual(["https://example.test/round-2"]);
		expect(store.getProfile(created.id)?.source).toMatchObject({ kind: "copy", remoteProfileId: REMOTE_PROFILE });
	});

	it("refuses to replace a profile a browser is using, and only updates copies", async () => {
		let live = false;
		const { service, store, cleared } = await setup({ live: () => live });
		const created = (await service.copy(MACHINE.hostId, REMOTE_PROFILE)).profile!;
		live = true;
		expect(await service.update(created.id)).toMatchObject({ code: "PROFILE_ACTIVE" });
		expect(cleared).toEqual([]);
		const own = await store.createProfile("Mine");
		expect(await service.update(own.id)).toMatchObject({ code: "NOT_SHARED" });
	});
});
