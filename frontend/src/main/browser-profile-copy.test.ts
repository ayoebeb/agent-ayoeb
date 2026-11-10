import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import type { CookiesSetDetails } from "electron";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BROWSER_IMPORT_MAX_COOKIES } from "../shared/browser-profile-import";
import { BROWSER_PROFILE_MAX_COUNT, BROWSER_PROFILE_REGISTRY_FILE_NAME, browserProfilePartition } from "../shared/browser-profiles";
import { BrowserHistoryStore } from "./browser-history-store";
import {
	BrowserProfileCopyService,
	LinkedMachinesSignedOutError,
	copiedProfileName,
	receivedCookieDetails,
	type LinkedMachine,
} from "./browser-profile-copy";
import { BrowserProfileCopyLedger } from "./browser-profile-copy-ledger";
import { createOneTimeKey, parseReceiverTicket, sealContext, sealPayload, RECEIVER_TICKET_PURPOSE } from "./browser-profile-share-crypto";
import { BrowserProfileStore } from "./browser-profile-store";
import { resetMainErrorCaptureForTests, setMainErrorSink } from "./main-error-capture";

const NOW = new Date("2026-11-10T12:00:00.000Z");
const FUTURE = NOW.getTime() / 1_000 + 86_400;
const REMOTE_PROFILE = "0f8fad5b-d9cb-469f-a165-70867728950e";
const SECRET = "dummy-secret-cookie-value-7f3a";
const ACCOUNT_TOKEN = "a".repeat(64);
const LOCAL_HOST = "h_this_machine";
const MACHINE: LinkedMachine = { hostId: "h_brave_box", name: "Brave box", url: "https://brave-box.example.test", credential: ACCOUNT_TOKEN };
const tempDirectories: string[] = [];

afterEach(async () => {
	vi.restoreAllMocks();
	resetMainErrorCaptureForTests();
	await Promise.all(tempDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

function summary(overrides: Record<string, unknown> = {}) {
	return { id: REMOTE_PROFILE, name: "Brave", sourceBrowser: "Brave", cookieCount: 2, historyCount: 1, updatedAt: "2026-11-10T10:00:00.000Z", ...overrides };
}

function wireCookie(name: string, overrides: Record<string, unknown> = {}) {
	return { name, value: SECRET, domain: "example.test", hostOnly: true, path: "/", secure: true, httpOnly: true, sameSite: "lax", expirationDate: FUTURE, ...overrides };
}

/** What the other machine answers; `seal: false` answers in plaintext, as an old or hostile build would. */
type Answer = { status: number; body: unknown; seal?: boolean | "other-key" };
type Route = (url: URL, init: RequestInit) => Answer | Promise<Answer>;

function jsonResponse(status: number, body: unknown): Response {
	return new Response(body === undefined ? "" : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/** A ticket as this machine's daemon signs it; the fake remote reads the key from it. */
function fakeTicket(sourceHostId: string, key: string) {
	const payload = { v: 1, purpose: RECEIVER_TICKET_PURPOSE, sourceHostId, receiverHostId: LOCAL_HOST, key, issuedAt: Math.floor(NOW.getTime() / 1_000) };
	return { ticket: Buffer.from(JSON.stringify(payload)).toString("base64url"), signature: "S".repeat(86) };
}

async function setup(options: {
	route?: Route;
	identity?: (url: string) => Promise<string>;
	machines?: () => Promise<LinkedMachine[]>;
	live?: (profileId: string) => boolean;
	setCookie?: (partition: string, details: CookiesSetDetails) => Promise<void>;
	dir?: string;
} = {}) {
	const dir = options.dir ?? await mkdtemp(path.join(os.tmpdir(), "ao-browser-copy-"));
	if (!options.dir) tempDirectories.push(dir);
	const store = new BrowserProfileStore({ stateDir: dir, now: () => NOW });
	const history = new BrowserHistoryStore({ stateDir: dir, now: () => NOW });
	const ledger = new BrowserProfileCopyLedger(dir);
	const written: Array<{ partition: string; details: CookiesSetDetails }> = [];
	const cleared: string[] = [];
	const requests: Array<{ url: string; headers: Record<string, string> }> = [];
	const tickets: Array<{ sourceHostId: string; key: string }> = [];
	const fetchImpl = vi.fn(async (input: string | URL | Request, init: RequestInit = {}) => {
		const url = new URL(String(input));
		const headers = Object.fromEntries(new Headers(init.headers).entries());
		requests.push({ url: url.href, headers });
		const answer = await (options.route ?? defaultRoute)(url, init);
		if (answer.status !== 200 || answer.seal === false) return jsonResponse(answer.status, answer.body);
		const ticket = parseReceiverTicket(headers["x-ao-receiver-ticket"]);
		if (!ticket) return jsonResponse(400, { code: "RECEIVER_TICKET_REQUIRED" });
		const exportMatch = /^\/api\/v1\/shared-browser-profiles\/([^/]+)\/export$/.exec(url.pathname);
		const context = sealContext({
			sourceHostId: MACHINE.hostId,
			receiverHostId: ticket.receiverHostId,
			action: exportMatch ? "export" : "list",
			profileId: exportMatch?.[1],
			cursor: url.searchParams.get("cursor") ?? "",
		});
		const key = answer.seal === "other-key" ? createOneTimeKey().publicKey : ticket.key;
		return jsonResponse(200, sealPayload(answer.body, key, context));
	});
	const identity = vi.fn(options.identity ?? (async () => MACHINE.hostId));
	const service = new BrowserProfileCopyService({
		store,
		history,
		linkedMachines: options.machines ?? (async () => [MACHINE]),
		identity: (url) => identity(url),
		localHostId: async () => LOCAL_HOST,
		issueTicket: async (sourceHostId, key) => {
			tickets.push({ sourceHostId, key });
			return fakeTicket(sourceHostId, key);
		},
		ledger,
		setCookie: options.setCookie ?? (async (partition, details) => { written.push({ partition, details }); }),
		clearProfileData: async (profileId) => { cleared.push(profileId); },
		isProfileLive: options.live ?? (() => false),
		fetch: fetchImpl as unknown as typeof fetch,
		now: () => NOW,
	});
	return { dir, store, history, ledger, service, written, cleared, requests, tickets, fetchImpl, identity };
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

	// Probe P1 of the security review: a cookie for a host that names a
	// machine would sign this machine's own local apps in to someone else.
	it("skips cookies for localhost, IP addresses and single-label hosts", () => {
		for (const domain of ["127.0.0.1", "localhost", ".localhost", "app.localhost", "192.168.1.1", "intranet", ".intranet"]) {
			expect(receivedCookieDetails(wireCookie("sid", { domain, hostOnly: !domain.startsWith(".") }), now), domain).toEqual({ skip: "invalid" });
		}
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

	// Finding H2: the credential and the cookies never travel over plain http.
	it("refuses a machine without an https address, without contacting it", async () => {
		for (const url of ["http://192.0.2.10:3011", "192.0.2.10:3011", "https://user:pw@brave-box.example.test", "ftp://brave-box.example.test"]) {
			const { service, fetchImpl, identity, tickets } = await setup({ machines: async () => [{ ...MACHINE, url }] });
			expect((await service.listMachines()).machines[0], url).toMatchObject({ state: "insecure", profiles: [] });
			expect(await service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "INSECURE" });
			expect(fetchImpl).not.toHaveBeenCalled();
			expect(identity).not.toHaveBeenCalled();
			expect(tickets).toEqual([]);
		}
	});

	it("never sends the credential to an address that does not answer as the listed machine", async () => {
		const offline = await setup({ identity: async () => { throw new Error("ECONNREFUSED"); } });
		expect((await offline.service.listMachines()).machines[0]).toMatchObject({ name: "Brave box", state: "offline", profiles: [] });
		expect(offline.fetchImpl).not.toHaveBeenCalled();

		const reassigned = await setup({ identity: async () => "h_someone_else" });
		expect((await reassigned.service.listMachines()).machines[0]?.state).toBe("unauthorized");
		expect(reassigned.fetchImpl).not.toHaveBeenCalled();

		const itself = await setup({ identity: async () => LOCAL_HOST });
		expect((await itself.service.listMachines()).machines[0]?.state).toBe("unauthorized");
		expect(itself.fetchImpl).not.toHaveBeenCalled();
	});

	it("maps the other machine's refusals to states the UI explains", async () => {
		for (const [status, code, state] of [
			[403, "BROWSER_PROFILE_SHARING_OFF", "sharing-off"],
			[403, "RECEIVER_NOT_VERIFIED", "unverified"],
			[403, "REMOTE_LINK_REQUIRED", "unauthorized"],
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

	it("lists valid profiles with the account credential and a signed ticket, and links this machine's copies", async () => {
		const { service, requests, store, tickets } = await setup({
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
		expect(requests[0]?.url).toBe("https://brave-box.example.test/api/v1/shared-browser-profiles");
		expect(requests[0]?.headers).toMatchObject({
			authorization: `Bearer ${ACCOUNT_TOKEN}`,
			"x-ao-expected-host-id": MACHINE.hostId,
			"x-ao-receiver-signature": "S".repeat(86),
		});
		expect(tickets).toEqual([{ sourceHostId: MACHINE.hostId, key: expect.stringMatching(/^[A-Za-z0-9_-]{43}$/) }]);
	});

	it("refuses an answer that is not sealed to this machine's one-time key", async () => {
		for (const seal of [false, "other-key"] as const) {
			const { service } = await setup({ route: () => ({ status: 200, body: { profiles: [summary()] }, seal }) });
			expect((await service.listMachines()).machines[0], String(seal)).toMatchObject({ state: "failed", profiles: [] });
			const copy = await setup({ route: (url, init) => ({ ...(defaultRoute(url, init) as Answer), seal }) });
			expect(await copy.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "INVALID_DATA" });
			expect(copy.store.profiles).toEqual([]);
			expect(copy.written).toEqual([]);
		}
	});
});

describe("BrowserProfileCopyService.copy", () => {
	it("creates a normal local profile with the cookies, history and its origin", async () => {
		const { service, store, history, written, ledger } = await setup();
		const result = await service.copy(MACHINE.hostId, REMOTE_PROFILE, "Brave (from Brave box)");
		expect(result).toMatchObject({ importedCookies: 2, skippedCookies: 0, importedHistoryEntries: 1 });
		const profile = result.profile!;
		const source = { kind: "copy", browser: "Brave", hostId: MACHINE.hostId, machineName: "Brave box", remoteProfileId: REMOTE_PROFILE };
		expect(store.getProfile(profile.id)).toMatchObject({ name: "Brave (from Brave box)", source });
		// The copy ledger withholds it from sharing whatever happens to the registry.
		expect((await ledger.load()).sources.get(profile.id)).toMatchObject(source);
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
		// One key for the whole copy, a fresh signed ticket for every request.
		expect(paged.tickets).toHaveLength(2);
		expect(new Set(paged.tickets.map((ticket) => ticket.key)).size).toBe(1);

		const looping = await setup({
			route: () => ({ status: 200, body: { profile: summary(), cookies: [wireCookie("a")], history: [], nextCursor: "1:0" } }),
		});
		expect(await looping.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "INVALID_DATA" });
		expect(looping.store.profiles).toEqual([]);
		expect((await looping.ledger.load()).ids.size).toBe(0);
	});

	// Finding L2: the address is checked again before every credentialed page,
	// so a credential never follows an address another machine took over.
	it("checks the machine's identity before every page and stops when it changes", async () => {
		let identityChecks = 0;
		const { service, requests, store } = await setup({
			identity: async () => (++identityChecks <= 1 ? MACHINE.hostId : "h_someone_else"),
			route: (url) => ({ status: 200, body: { profile: summary(), cookies: [wireCookie("a")], history: [], ...(url.searchParams.get("cursor") ? {} : { nextCursor: "1:0" }) } }),
		});
		expect(await service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "UNAUTHORIZED" });
		expect(identityChecks).toBe(2);
		expect(requests).toHaveLength(1);
		expect(store.profiles).toEqual([]);
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

	it("explains a machine whose sharing is off or that cannot verify this one, and refuses duplicates and a full profile list", async () => {
		const off = await setup({ route: () => ({ status: 403, body: { code: "BROWSER_PROFILE_SHARING_OFF" } }) });
		expect(await off.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "SHARING_OFF" });

		const unverified = await setup({ route: () => ({ status: 403, body: { code: "RECEIVER_NOT_VERIFIED" } }) });
		expect(await unverified.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "UNVERIFIED" });

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

	// Review item 6, gap 7: no cookie value reaches the console, an error, or
	// main's error capture, which feeds telemetry.
	it("keeps cookie values out of logs, errors and error capture even when writing fails", async () => {
		const consoleCalls: unknown[][] = [];
		for (const method of ["log", "info", "warn", "error", "debug"] as const) {
			vi.spyOn(console, method).mockImplementation((...args: unknown[]) => { consoleCalls.push(args); });
		}
		const captured: unknown[] = [];
		setMainErrorSink((capture) => { captured.push(capture); });
		const { service } = await setup({
			setCookie: async (_partition, details) => {
				if (details.name === "sid") throw new Error(`Failed to set cookie ${details.name}=${details.value}`);
			},
		});
		const result = await service.copy(MACHINE.hostId, REMOTE_PROFILE);
		expect(result).toMatchObject({ importedCookies: 1, skippedCookies: 1 });
		const failing = await setup({ route: () => ({ status: 200, body: { profile: summary(), cookies: [wireCookie("a")], history: "x" } }) });
		const failed = await failing.service.copy(MACHINE.hostId, REMOTE_PROFILE);
		const brokenHistory = await setup();
		vi.spyOn(brokenHistory.history, "mergeImportedEntries").mockRejectedValue(new Error(`history write failed near ${SECRET}`));
		const historyFailed = await brokenHistory.service.copy(MACHINE.hostId, REMOTE_PROFILE);
		expect(historyFailed).toMatchObject({ code: "FAILED" });
		expect(JSON.stringify([result, failed, historyFailed, consoleCalls, captured, String(historyFailed.error)])).not.toContain(SECRET);
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

	it("keeps a copy linked to its machine after an older build strips its provenance", async () => {
		const first = await setup();
		const created = (await first.service.copy(MACHINE.hostId, REMOTE_PROFILE)).profile!;
		const registryPath = path.join(first.dir, BROWSER_PROFILE_REGISTRY_FILE_NAME);
		const registry = JSON.parse(await readFile(registryPath, "utf8")) as { profiles: Record<string, unknown>[] };
		registry.profiles = registry.profiles.map(({ source: _source, ...rest }) => rest);
		await writeFile(registryPath, JSON.stringify(registry));

		const reloaded = await setup({ dir: first.dir });
		const listed = await reloaded.service.listMachines();
		expect(listed.machines[0]?.profiles[0]?.localCopy).toEqual({ profileId: created.id, copiedAt: NOW.toISOString() });
		expect(await reloaded.service.copy(MACHINE.hostId, REMOTE_PROFILE)).toMatchObject({ code: "ALREADY_COPIED" });
		expect((await reloaded.service.update(created.id)).error).toBeUndefined();
		// Update writes the provenance back into the registry.
		expect(reloaded.store.getProfile(created.id)?.source).toMatchObject({ kind: "copy", hostId: MACHINE.hostId });
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
