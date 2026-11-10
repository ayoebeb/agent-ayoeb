import type { CookiesSetDetails } from "electron";
import {
	BROWSER_IMPORT_MAX_COOKIES,
	BROWSER_IMPORT_MAX_HISTORY_ENTRIES,
} from "../shared/browser-profile-import";
import type {
	BrowserProfileCopyErrorCode,
	BrowserProfileCopyResult,
	OtherMachine,
	OtherMachineState,
	OtherMachinesResult,
	SharedBrowserProfileSummary,
} from "../shared/browser-profile-share";
import {
	BROWSER_PROFILE_MACHINE_NAME_MAX_LENGTH,
	BROWSER_PROFILE_MAX_COUNT,
	BROWSER_PROFILE_NAME_MAX_LENGTH,
	BROWSER_PROFILE_SOURCE_BROWSER_MAX_LENGTH,
	browserProfilePartition,
	normalizeBrowserProfileId,
	normalizeBrowserProfileLabel,
	normalizeBrowserProfileName,
	type BrowserProfile,
	type BrowserProfileSource,
} from "../shared/browser-profiles";
import { normalizeBrowserHistoryEntry, type BrowserHistoryEntry, type BrowserHistoryStore } from "./browser-history-store";
import { isAntiBotCookie } from "./browser-profile-import";
import { BrowserProfileStoreError, type BrowserProfileStore } from "./browser-profile-store";

/**
 * The receiving machine: lists the browser profiles its linked machines share
 * and copies one into a local AO profile on an explicit click. Requests go
 * straight from this main process to the other machine's authenticated
 * remote-host listener with the saved credential; no cloud or relay server
 * sees the data, and the renderer never receives a cookie value. Everything
 * received is validated and bounded before anything is written.
 */

/** A paired machine of the signed-in account, as main reads it from remotes.json. */
export type LinkedMachine = { hostId: string; name: string; url: string; credential: string };

/** Linked machines belong to an AO account; signed out there are none to read. */
export class LinkedMachinesSignedOutError extends Error {
	constructor() {
		super("Sign in to Orchestrator Cloud to see your other machines.");
		this.name = "LinkedMachinesSignedOutError";
	}
}

const HOST_ID_PATTERN = /^[A-Za-z0-9_.:-]{1,128}$/;
const CURSOR_PATTERN = /^[0-9]{1,6}:[0-9]{1,6}$/;
const IDENTITY_TIMEOUT_MS = 5_000;
const LIST_TIMEOUT_MS = 25_000;
const PAGE_TIMEOUT_MS = 70_000;
const MAX_LIST_BYTES = 1024 * 1024;
const MAX_PAGE_BYTES = 16 * 1024 * 1024;
const MAX_EXPORT_BYTES = 96 * 1024 * 1024;
const MAX_PAGES = 64;
const COOKIE_NAME_VALUE_MAX_BYTES = 4_096;
const COOKIE_PATH_MAX_LENGTH = 1_024;
const COOKIE_DOMAIN_MAX_LENGTH = 255;
const COOKIE_TEXT = /^[^\u0000-\u001f\u007f;]*$/u;
const SAME_SITE = new Set(["unspecified", "no_restriction", "lax", "strict"]);

type FetchImpl = typeof fetch;

export type BrowserProfileCopyOptions = {
	store: BrowserProfileStore;
	history: Pick<BrowserHistoryStore, "clear" | "mergeImportedEntries">;
	/** The signed-in account's paired machines, excluding this one. */
	linkedMachines: () => Promise<LinkedMachine[]>;
	/** The unauthenticated identity probe: the only request made before the credential is sent. */
	identity: (url: string, signal: AbortSignal) => Promise<string>;
	localHostId: () => Promise<string | null>;
	setCookie: (partition: string, details: CookiesSetDetails) => Promise<void>;
	flushCookies?: (partition: string) => Promise<void>;
	/** Clears a profile's whole Electron storage and cache. */
	clearProfileData: (profileId: string) => Promise<void>;
	isProfileLive: (profileId: string) => boolean;
	fetch?: FetchImpl;
	now?: () => Date;
};

class CopyError extends Error {
	constructor(readonly code: BrowserProfileCopyErrorCode, message: string) {
		super(message);
		this.name = "BrowserProfileCopyError";
	}
}

const STATE_ERRORS: Record<Exclude<OtherMachineState, "online">, { code: BrowserProfileCopyErrorCode; message: string }> = {
	offline: { code: "OFFLINE", message: "The other machine is offline." },
	"sharing-off": { code: "SHARING_OFF", message: "Browser profile sharing is turned off on the other machine." },
	unauthorized: { code: "UNAUTHORIZED", message: "Pair the other machine again in Remote hosts." },
	outdated: { code: "OUTDATED", message: "Update Orchestrator on the other machine to copy its browser profiles." },
	"desktop-closed": { code: "DESKTOP_CLOSED", message: "Open the Orchestrator desktop app on the other machine." },
	failed: { code: "FAILED", message: "The other machine could not send its browser profiles." },
};

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function boundedCount(value: unknown, max: number): number | null {
	return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 && value <= max ? value : null;
}

function remoteBase(url: string): string {
	const base = (url.includes("://") ? url : `http://${url}`).replace(/\/+$/, "");
	const parsed = new URL(base);
	if (!["http:", "https:"].includes(parsed.protocol) || parsed.username || parsed.password) {
		throw new CopyError("UNAUTHORIZED", "The saved address for the other machine is not valid.");
	}
	return base;
}

function stateForResponse(status: number, code: string | undefined): OtherMachineState {
	if (status === 401 || status === 421) return "unauthorized";
	if (status === 403 && code === "BROWSER_PROFILE_SHARING_OFF") return "sharing-off";
	if (status === 501 || (status === 404 && code === "ROUTE_NOT_FOUND")) return "outdated";
	if (status === 503 && code === "DESKTOP_UNAVAILABLE") return "desktop-closed";
	return "failed";
}

async function readCapped(response: Response, maxBytes: number): Promise<string> {
	const reader = response.body?.getReader();
	if (!reader) return "";
	const chunks: Uint8Array[] = [];
	let total = 0;
	for (;;) {
		const { done, value } = await reader.read();
		if (done) break;
		total += value.byteLength;
		if (total > maxBytes) {
			await reader.cancel().catch(() => undefined);
			throw new CopyError("INVALID_DATA", "The other machine sent more data than allowed.");
		}
		chunks.push(value);
	}
	return Buffer.concat(chunks).toString("utf8");
}

/** A received summary, or null when any field is out of bounds. */
export function parseSharedProfileSummary(value: unknown): SharedBrowserProfileSummary | null {
	if (!isRecord(value)) return null;
	const id = normalizeBrowserProfileId(value.id);
	const name = normalizeBrowserProfileName(value.name);
	const sourceBrowser = value.sourceBrowser === undefined || value.sourceBrowser === ""
		? undefined
		: normalizeBrowserProfileLabel(value.sourceBrowser, BROWSER_PROFILE_SOURCE_BROWSER_MAX_LENGTH);
	const cookieCount = boundedCount(value.cookieCount, BROWSER_IMPORT_MAX_COOKIES);
	const historyCount = boundedCount(value.historyCount, BROWSER_IMPORT_MAX_HISTORY_ENTRIES);
	if (
		!id || !name || sourceBrowser === null || cookieCount === null || historyCount === null ||
		typeof value.updatedAt !== "string" || !Number.isFinite(Date.parse(value.updatedAt))
	) {
		return null;
	}
	return { id, name, ...(sourceBrowser ? { sourceBrowser } : {}), cookieCount, historyCount, updatedAt: value.updatedAt };
}

type CookieOutcome = { details: CookiesSetDetails } | { skip: "expired" | "invalid" | "anti-bot" };

/**
 * One received cookie as Electron cookie details. Mirrors how imports build
 * them: the URL comes from the host and path, and `domain` is passed only for
 * a domain cookie, because passing it turns a host-only cookie into one.
 */
export function receivedCookieDetails(value: unknown, nowSeconds: number): CookieOutcome {
	if (!isRecord(value)) return { skip: "invalid" };
	const { name, value: cookieValue, domain, path, hostOnly, secure, httpOnly, sameSite, expirationDate } = value;
	if (
		typeof name !== "string" || name.length === 0 || name.includes("=") || !COOKIE_TEXT.test(name) ||
		typeof cookieValue !== "string" || !COOKIE_TEXT.test(cookieValue) ||
		Buffer.byteLength(name) + Buffer.byteLength(cookieValue) > COOKIE_NAME_VALUE_MAX_BYTES ||
		typeof domain !== "string" || domain.length === 0 || domain.length > COOKIE_DOMAIN_MAX_LENGTH ||
		typeof path !== "string" || !path.startsWith("/") || path.length > COOKIE_PATH_MAX_LENGTH || !COOKIE_TEXT.test(path) ||
		typeof hostOnly !== "boolean" || typeof secure !== "boolean" || typeof httpOnly !== "boolean" ||
		(sameSite !== undefined && (typeof sameSite !== "string" || !SAME_SITE.has(sameSite))) ||
		(expirationDate !== undefined && (typeof expirationDate !== "number" || !Number.isFinite(expirationDate) || expirationDate <= 0))
	) {
		return { skip: "invalid" };
	}
	if (isAntiBotCookie(name)) return { skip: "anti-bot" };
	if (expirationDate !== undefined && expirationDate <= nowSeconds) return { skip: "expired" };
	const normalizedDomain = domain.toLowerCase();
	const hostname = normalizedDomain.replace(/^\.+/, "");
	let url: string;
	try {
		const parsed = new URL(`${secure ? "https" : "http"}://${hostname}`);
		// The URL parser accepts and rewrites odd hosts; only an exact host is a cookie domain.
		if (parsed.hostname !== hostname || parsed.port || parsed.username) return { skip: "invalid" };
		url = new URL(path, parsed).href;
	} catch {
		return { skip: "invalid" };
	}
	return {
		details: {
			url,
			name,
			value: cookieValue,
			...(hostOnly ? {} : { domain: normalizedDomain.startsWith(".") ? normalizedDomain : `.${normalizedDomain}` }),
			path,
			secure,
			httpOnly,
			...(sameSite ? { sameSite: sameSite as CookiesSetDetails["sameSite"] } : {}),
			...(expirationDate !== undefined ? { expirationDate } : {}),
		},
	};
}

/**
 * The renderer's localized name when it is a valid, free profile name;
 * otherwise "<profile> (from <machine>)", shortened to the name limit and
 * made unique.
 */
export function copiedProfileName(remoteName: string, machineName: string, taken: Iterable<string>, preferred?: unknown): string {
	const used = new Set([...taken].map((name) => name.toLowerCase()));
	const wanted = normalizeBrowserProfileName(preferred);
	if (wanted && !used.has(wanted.toLowerCase())) return wanted;
	const machine = machineName.length > 32 ? `${machineName.slice(0, 31).trimEnd()}…` : machineName;
	for (let attempt = 1; attempt < 100; attempt += 1) {
		const suffix = ` (from ${machine})${attempt > 1 ? ` ${attempt}` : ""}`;
		const room = BROWSER_PROFILE_NAME_MAX_LENGTH - suffix.length;
		const base = remoteName.length > room ? `${remoteName.slice(0, room - 1).trimEnd()}…` : remoteName;
		const candidate = `${base}${suffix}`;
		if (!used.has(candidate.toLowerCase())) return candidate;
	}
	throw new CopyError("FAILED", "Choose a different name for one of your browser profiles, then copy again.");
}

type ReceivedProfile = {
	summary: SharedBrowserProfileSummary;
	cookies: CookiesSetDetails[];
	skippedCookies: number;
	history: BrowserHistoryEntry[];
};

export class BrowserProfileCopyService {
	private readonly fetchImpl: FetchImpl;
	private readonly now: () => Date;
	private queue: Promise<void> = Promise.resolve();

	constructor(private readonly options: BrowserProfileCopyOptions) {
		this.fetchImpl = options.fetch ?? fetch;
		this.now = options.now ?? (() => new Date());
	}

	/** Copies and updates run one at a time: each holds a whole profile in memory. */
	private serialize<T>(operation: () => Promise<T>): Promise<T> {
		const result = this.queue.then(operation, operation);
		this.queue = result.then(() => undefined, () => undefined);
		return result;
	}

	/** Confirms the address still answers as the paired machine before the credential is sent. */
	private async verifyIdentity(machine: LinkedMachine): Promise<void> {
		let actual: string;
		try {
			actual = await this.options.identity(remoteBase(machine.url), AbortSignal.timeout(IDENTITY_TIMEOUT_MS));
		} catch (error) {
			if (error instanceof CopyError) throw error;
			throw new CopyError("OFFLINE", STATE_ERRORS.offline.message);
		}
		const local = await this.options.localHostId();
		if (actual !== machine.hostId || (local && actual === local)) {
			throw new CopyError("UNAUTHORIZED", STATE_ERRORS.unauthorized.message);
		}
	}

	private async request(machine: LinkedMachine, path: string, timeoutMs: number, maxBytes: number): Promise<unknown> {
		const url = new URL(`${remoteBase(machine.url)}${path}`);
		let response: Response;
		try {
			response = await this.fetchImpl(url.href, {
				method: "GET",
				redirect: "error",
				headers: {
					Accept: "application/json",
					Authorization: `Bearer ${machine.credential}`,
					"X-AO-Expected-Host-ID": machine.hostId,
				},
				signal: AbortSignal.timeout(timeoutMs),
			});
		} catch {
			throw new CopyError("OFFLINE", STATE_ERRORS.offline.message);
		}
		let text: string;
		try {
			text = await readCapped(response, maxBytes);
		} catch (error) {
			if (error instanceof CopyError) throw error;
			throw new CopyError("OFFLINE", STATE_ERRORS.offline.message);
		}
		let body: unknown;
		try {
			body = text ? JSON.parse(text) : undefined;
		} catch {
			body = undefined;
		}
		if (response.ok) {
			if (body === undefined) throw new CopyError("INVALID_DATA", "The other machine sent data this machine cannot read.");
			return body;
		}
		const code = isRecord(body) && typeof body.code === "string" ? body.code : undefined;
		if (response.status === 404 && code === "SHARED_BROWSER_PROFILE_NOT_FOUND") {
			throw new CopyError("NOT_SHARED", "The other machine no longer shares this browser profile.");
		}
		const state = stateForResponse(response.status, code);
		const failure = STATE_ERRORS[state === "online" ? "failed" : state];
		throw new CopyError(failure.code, failure.message);
	}

	private machineState(error: unknown): OtherMachineState {
		if (!(error instanceof CopyError)) return "failed";
		const entry = Object.entries(STATE_ERRORS).find(([, value]) => value.code === error.code);
		return (entry?.[0] as OtherMachineState | undefined) ?? "failed";
	}

	async listMachines(): Promise<OtherMachinesResult> {
		let machines: LinkedMachine[];
		try {
			machines = await this.options.linkedMachines();
		} catch (error) {
			if (error instanceof LinkedMachinesSignedOutError) return { signedIn: false, machines: [] };
			return { signedIn: true, machines: [], error: "Could not read your linked machines." };
		}
		await this.options.store.load();
		const copies = this.options.store.profiles.flatMap((profile) =>
			profile.source?.kind === "copy" ? [{ profile, source: profile.source }] : []);
		const listed = await Promise.all(machines.map(async (machine): Promise<OtherMachine> => {
			const name = normalizeBrowserProfileLabel(machine.name, BROWSER_PROFILE_MACHINE_NAME_MAX_LENGTH) ?? machine.hostId;
			try {
				await this.verifyIdentity(machine);
				const body = await this.request(machine, "/api/v1/shared-browser-profiles", LIST_TIMEOUT_MS, MAX_LIST_BYTES);
				const raw = isRecord(body) && Array.isArray(body.profiles) ? body.profiles.slice(0, BROWSER_PROFILE_MAX_COUNT) : [];
				const profiles = raw.flatMap((entry) => {
					const summary = parseSharedProfileSummary(entry);
					if (!summary) return [];
					const copy = copies.find(({ source }) => source.hostId === machine.hostId && source.remoteProfileId === summary.id);
					return [{ ...summary, ...(copy ? { localCopy: { profileId: copy.profile.id, copiedAt: copy.source.copiedAt } } : {}) }];
				});
				return { hostId: machine.hostId, name, state: "online", profiles };
			} catch (error) {
				return { hostId: machine.hostId, name, state: this.machineState(error), profiles: [] };
			}
		}));
		return { signedIn: true, machines: listed };
	}

	private async findMachine(hostId: string): Promise<LinkedMachine> {
		let machines: LinkedMachine[];
		try {
			machines = await this.options.linkedMachines();
		} catch {
			throw new CopyError("NOT_LINKED", "Sign in to Orchestrator Cloud to copy from your other machines.");
		}
		const machine = machines.find((candidate) => candidate.hostId === hostId);
		if (!machine) throw new CopyError("NOT_LINKED", "That machine is no longer linked to this one.");
		return machine;
	}

	/** Reads every page, validating and bounding each, before anything is written. */
	private async receive(machine: LinkedMachine, remoteProfileId: string): Promise<ReceivedProfile> {
		await this.verifyIdentity(machine);
		const nowSeconds = this.now().getTime() / 1_000;
		const cookies = new Map<string, CookiesSetDetails>();
		const history: BrowserHistoryEntry[] = [];
		let skippedCookies = 0;
		let receivedCookies = 0;
		let receivedHistory = 0;
		let summary: SharedBrowserProfileSummary | null = null;
		let cursor: string | undefined;
		const seenCursors = new Set<string>();
		let totalBytes = 0;
		for (let page = 0; ; page += 1) {
			if (page >= MAX_PAGES) throw new CopyError("INVALID_DATA", "The other machine sent more data than allowed.");
			const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : "";
			const body = await this.request(
				machine,
				`/api/v1/shared-browser-profiles/${encodeURIComponent(remoteProfileId)}/export${query}`,
				PAGE_TIMEOUT_MS,
				MAX_PAGE_BYTES,
			);
			totalBytes += Buffer.byteLength(JSON.stringify(body));
			if (totalBytes > MAX_EXPORT_BYTES) throw new CopyError("INVALID_DATA", "The other machine sent more data than allowed.");
			if (!isRecord(body) || !Array.isArray(body.cookies) || !Array.isArray(body.history)) {
				throw new CopyError("INVALID_DATA", "The other machine sent data this machine cannot read.");
			}
			const pageSummary = parseSharedProfileSummary(body.profile);
			if (!pageSummary || pageSummary.id !== remoteProfileId) {
				throw new CopyError("INVALID_DATA", "The other machine sent data this machine cannot read.");
			}
			summary ??= pageSummary;
			receivedCookies += body.cookies.length;
			receivedHistory += body.history.length;
			if (receivedCookies > BROWSER_IMPORT_MAX_COOKIES || receivedHistory > BROWSER_IMPORT_MAX_HISTORY_ENTRIES) {
				throw new CopyError("INVALID_DATA", "The other machine sent more cookies or history than allowed.");
			}
			for (const raw of body.cookies) {
				const outcome = receivedCookieDetails(raw, nowSeconds);
				if ("skip" in outcome) {
					skippedCookies += 1;
					continue;
				}
				const key = `${outcome.details.name}\0${outcome.details.domain ?? outcome.details.url}\0${outcome.details.path}`;
				if (cookies.has(key)) skippedCookies += 1;
				cookies.set(key, outcome.details);
			}
			for (const raw of body.history) {
				const entry = normalizeBrowserHistoryEntry(raw);
				if (entry) history.push(entry);
			}
			const next = body.nextCursor;
			if (next === undefined || next === null || next === "") break;
			if (typeof next !== "string" || !CURSOR_PATTERN.test(next) || seenCursors.has(next)) {
				throw new CopyError("INVALID_DATA", "The other machine sent data this machine cannot read.");
			}
			seenCursors.add(next);
			cursor = next;
		}
		return { summary: summary!, cookies: [...cookies.values()], skippedCookies, history };
	}

	private async writeContents(profileId: string, data: ReceivedProfile): Promise<{ imported: number; skipped: number; history: number }> {
		const partition = browserProfilePartition(profileId);
		let imported = 0;
		let skipped = data.skippedCookies;
		for (const details of data.cookies) {
			try {
				await this.options.setCookie(partition, details);
				imported += 1;
			} catch {
				// Electron's reason may quote the cookie; it is counted, never kept.
				skipped += 1;
			}
		}
		await this.options.flushCookies?.(partition).catch(() => undefined);
		const outcome = await this.options.history.mergeImportedEntries(profileId, data.history);
		return { imported, skipped, history: outcome.imported };
	}

	private failure(error: unknown): BrowserProfileCopyResult {
		if (error instanceof CopyError) return { error: error.message, code: error.code };
		if (error instanceof BrowserProfileStoreError) {
			if (error.code === "BROWSER_PROFILE_LIMIT") return { error: "The browser profile limit has been reached.", code: "PROFILE_LIMIT" };
			if (error.code === "BROWSER_PROFILE_ACTIVE" || error.code === "BROWSER_PROFILE_OPERATION_IN_PROGRESS") {
				return { error: "A browser is using this profile. Switch it to another profile, then try again.", code: "PROFILE_ACTIVE" };
			}
		}
		// Anything else may carry storage or Electron detail; it stays in main.
		return { error: "The browser profile could not be copied.", code: "FAILED" };
	}

	copy(hostId: unknown, remoteProfileId: unknown, preferredName?: unknown): Promise<BrowserProfileCopyResult> {
		return this.serialize(async () => {
			const remoteId = normalizeBrowserProfileId(remoteProfileId);
			if (typeof hostId !== "string" || !HOST_ID_PATTERN.test(hostId) || !remoteId) {
				return { error: "That browser profile is not valid.", code: "INVALID_DATA" };
			}
			let created: BrowserProfile | null = null;
			try {
				const machine = await this.findMachine(hostId);
				await this.options.store.load();
				const profiles = this.options.store.profiles;
				if (profiles.some((profile) => profile.source?.kind === "copy" && profile.source.hostId === hostId && profile.source.remoteProfileId === remoteId)) {
					throw new CopyError("ALREADY_COPIED", "This profile is already on this machine. Update it instead.");
				}
				if (profiles.length >= BROWSER_PROFILE_MAX_COUNT) {
					throw new CopyError("PROFILE_LIMIT", "The browser profile limit has been reached.");
				}
				const data = await this.receive(machine, remoteId);
				const machineName = normalizeBrowserProfileLabel(machine.name, BROWSER_PROFILE_MACHINE_NAME_MAX_LENGTH) ?? machine.hostId;
				const source: BrowserProfileSource = {
					kind: "copy",
					...(data.summary.sourceBrowser ? { browser: data.summary.sourceBrowser } : {}),
					hostId,
					machineName,
					remoteProfileId: remoteId,
					copiedAt: this.now().toISOString(),
				};
				const name = copiedProfileName(
					data.summary.name,
					machineName,
					this.options.store.profiles.map((profile) => profile.name),
					preferredName,
				);
				const profile = await this.options.store.createProfile(name, { claimDefault: false, source });
				created = profile;
				const outcome = await this.options.store.runProfileOperation(
					profile.id,
					() => this.options.isProfileLive(profile.id),
					() => this.writeContents(profile.id, data),
				);
				// Like an import, only a committed copy may become the default.
				await this.options.store.claimDefaultIfFirst(profile.id);
				return {
					profile: this.options.store.getProfile(profile.id) ?? profile,
					importedCookies: outcome.imported,
					skippedCookies: outcome.skipped,
					importedHistoryEntries: outcome.history,
				};
			} catch (error) {
				if (created) await this.discard(created.id);
				return this.failure(error);
			}
		});
	}

	update(localProfileId: unknown): Promise<BrowserProfileCopyResult> {
		return this.serialize(async () => {
			const profileId = normalizeBrowserProfileId(localProfileId);
			if (!profileId) return { error: "That browser profile is not valid.", code: "INVALID_DATA" };
			try {
				await this.options.store.load();
				const profile = this.options.store.getProfile(profileId);
				const source = profile?.source;
				if (!profile || source?.kind !== "copy") {
					throw new CopyError("NOT_SHARED", "Only a profile copied from another machine can be updated.");
				}
				const machine = await this.findMachine(source.hostId);
				const data = await this.receive(machine, source.remoteProfileId);
				const outcome = await this.options.store.runProfileOperation(
					profileId,
					() => this.options.isProfileLive(profileId),
					async () => {
						// Replace, not merge: the copy mirrors the other machine's profile.
						await this.options.clearProfileData(profileId);
						await this.options.history.clear(profileId);
						return this.writeContents(profileId, data);
					},
				);
				const updated = await this.options.store.setProfileSource(profileId, {
					...source,
					...(data.summary.sourceBrowser ? { browser: data.summary.sourceBrowser } : {}),
					copiedAt: this.now().toISOString(),
				});
				return {
					profile: updated,
					importedCookies: outcome.imported,
					skippedCookies: outcome.skipped,
					importedHistoryEntries: outcome.history,
				};
			} catch (error) {
				return this.failure(error);
			}
		});
	}

	/** Removes a copy that did not finish; best effort, like a failed import's cleanup. */
	private async discard(profileId: string): Promise<void> {
		await Promise.allSettled([this.options.clearProfileData(profileId), this.options.history.clear(profileId)]);
		await this.options.store.deleteProfile(profileId).catch(() => undefined);
	}
}
