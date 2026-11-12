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
import type { BrowserProfileCopyLedger, BrowserProfileCopySource } from "./browser-profile-copy-ledger";
import { LinkedMachinesSignedOutError, type LinkedMachine } from "./browser-profile-directory";
import { isAntiBotCookie } from "./browser-profile-import";
import { secureOrigin } from "./browser-profile-receiver";
import { isShareableCookieHost } from "./browser-profile-share";
import { createOneTimeKey, openPayload, sealContext, type OneTimeKey } from "./browser-profile-share-crypto";
import { BrowserProfileStoreError, type BrowserProfileStore } from "./browser-profile-store";

export { LinkedMachinesSignedOutError, type LinkedMachine };

/**
 * The receiving machine: lists the browser profiles its account's other
 * machines share and copies one into a local AO profile on an explicit click.
 * Requests go straight from this main process to the other machine's remote
 * listener at the https address AO Cloud lists for it, with the account
 * credential and a ticket this machine's daemon signs with its host key; the
 * other machine seals each answer to a one-time key only this process holds.
 * No cloud or relay server sees the data, and the renderer never receives a
 * cookie value. Everything received is validated and bounded before anything
 * is written.
 */

const HOST_ID_PATTERN = /^[A-Za-z0-9_.:-]{1,128}$/;
/** "<export snapshot>:<cookie offset>:<history offset>", as the source gives it. */
const CURSOR_PATTERN = /^[0-9a-f]{32}:[0-9]{1,6}:[0-9]{1,6}$/;
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
	/** The signed-in account's other machines, from its AO Cloud directory. */
	linkedMachines: () => Promise<LinkedMachine[]>;
	/** The unauthenticated identity probe: the only request made before the credential is sent. */
	identity: (url: string, signal: AbortSignal) => Promise<string>;
	localHostId: () => Promise<string | null>;
	/** This machine's daemon signs a ticket for reading sourceHostId's profiles sealed to key. */
	issueTicket: (sourceHostId: string, key: string) => Promise<{ ticket: string; signature: string }>;
	/** Every profile copied here, kept apart from the registry. */
	ledger: Pick<BrowserProfileCopyLedger, "load" | "record" | "remove">;
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
	insecure: { code: "INSECURE", message: "The other machine has no secure address, so its profiles cannot be copied." },
	unverified: { code: "UNVERIFIED", message: "The other machine could not confirm this machine." },
	"sharing-off": { code: "SHARING_OFF", message: "Browser profile sharing is turned off on the other machine." },
	unauthorized: { code: "UNAUTHORIZED", message: "Pair the other machine again in Remote hosts." },
	outdated: { code: "OUTDATED", message: "Update Ayo on the other machine to copy its browser profiles." },
	"desktop-closed": { code: "DESKTOP_CLOSED", message: "Open the Ayo desktop app on the other machine." },
	failed: { code: "FAILED", message: "The other machine could not send its browser profiles." },
};

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function boundedCount(value: unknown, max: number): number | null {
	return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 && value <= max ? value : null;
}

/**
 * The other machine's https origin. Cookies and history never travel over
 * plain http, even sealed: the credential would, and an http address cannot
 * prove which machine answers.
 */
function remoteBase(url: string): string {
	const origin = secureOrigin(url);
	if (!origin) throw new CopyError("INSECURE", STATE_ERRORS.insecure.message);
	return origin;
}

function stateForResponse(status: number, code: string | undefined): OtherMachineState {
	if (status === 403 && code === "RECEIVER_NOT_VERIFIED") return "unverified";
	if (status === 401 || status === 421 || (status === 403 && code === "REMOTE_LINK_REQUIRED")) return "unauthorized";
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
	// A cookie for localhost, an IP address, or a single-label host would
	// apply to this machine's own local apps, not the site it came from.
	if (!isShareableCookieHost(hostname)) return { skip: "invalid" };
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

	/**
	 * Confirms the address still answers as the listed machine before the
	 * credential is sent, and returns this machine's own host id.
	 */
	private async verifyIdentity(machine: LinkedMachine): Promise<string> {
		const base = remoteBase(machine.url);
		const local = await this.options.localHostId();
		if (!local) throw new CopyError("FAILED", "This machine's Ayo is not ready. Try again in a moment.");
		let actual: string;
		try {
			actual = await this.options.identity(base, AbortSignal.timeout(IDENTITY_TIMEOUT_MS));
		} catch (error) {
			if (error instanceof CopyError) throw error;
			throw new CopyError("OFFLINE", STATE_ERRORS.offline.message);
		}
		if (actual !== machine.hostId || actual === local) {
			throw new CopyError("UNAUTHORIZED", STATE_ERRORS.unauthorized.message);
		}
		return local;
	}

	/** Copies here, by local profile id: the registry's provenance, else the ledger's. */
	private async copySources(): Promise<Map<string, BrowserProfileCopySource>> {
		await this.options.store.load();
		let ledger: Map<string, BrowserProfileCopySource>;
		try {
			ledger = new Map((await this.options.ledger.load()).sources);
		} catch {
			ledger = new Map();
		}
		const sources = new Map<string, BrowserProfileCopySource>();
		for (const profile of this.options.store.profiles) {
			const source = profile.source?.kind === "copy" ? profile.source : ledger.get(profile.id);
			if (source) sources.set(profile.id, source);
		}
		return sources;
	}

	/** A ticket signed by this machine's own daemon; one per request, all naming the operation's key. */
	private async ticket(machine: LinkedMachine, key: OneTimeKey): Promise<{ ticket: string; signature: string }> {
		try {
			const signed = await this.options.issueTicket(machine.hostId, key.publicKey);
			if (typeof signed?.ticket !== "string" || typeof signed.signature !== "string") throw new Error("no ticket");
			return signed;
		} catch {
			throw new CopyError("FAILED", "This machine could not sign its request. Restart Ayo, then try again.");
		}
	}

	/**
	 * One request to the other machine, answered sealed to key. The address is
	 * checked again before each request, so a credential never follows an
	 * address that another machine took over between pages.
	 */
	private async request(
		machine: LinkedMachine,
		key: OneTimeKey,
		request: { path: string; action: "list" | "export"; profileId?: string; cursor?: string },
		timeoutMs: number,
		maxBytes: number,
	): Promise<unknown> {
		const base = remoteBase(machine.url);
		const local = await this.verifyIdentity(machine);
		const signed = await this.ticket(machine, key);
		let response: Response;
		try {
			response = await this.fetchImpl(`${base}${request.path}`, {
				method: "GET",
				redirect: "error",
				headers: {
					Accept: "application/json",
					Authorization: `Bearer ${machine.credential}`,
					"X-AO-Expected-Host-ID": machine.hostId,
					"X-AO-Receiver-Ticket": signed.ticket,
					"X-AO-Receiver-Signature": signed.signature,
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
			try {
				return openPayload(body, key, sealContext({
					sourceHostId: machine.hostId,
					receiverHostId: local,
					action: request.action,
					profileId: request.profileId,
					cursor: request.cursor,
				}));
			} catch {
				throw new CopyError("INVALID_DATA", "The other machine sent data this machine cannot read.");
			}
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
		const copies = [...(await this.copySources()).entries()];
		const listed = await Promise.all(machines.map(async (machine): Promise<OtherMachine> => {
			const name = normalizeBrowserProfileLabel(machine.name, BROWSER_PROFILE_MACHINE_NAME_MAX_LENGTH) ?? machine.hostId;
			try {
				const body = await this.request(
					machine,
					createOneTimeKey(),
					{ path: "/api/v1/shared-browser-profiles", action: "list" },
					LIST_TIMEOUT_MS,
					MAX_LIST_BYTES,
				);
				const raw = isRecord(body) && Array.isArray(body.profiles) ? body.profiles.slice(0, BROWSER_PROFILE_MAX_COUNT) : [];
				const profiles = raw.flatMap((entry) => {
					const summary = parseSharedProfileSummary(entry);
					if (!summary) return [];
					const copy = copies.find(([, source]) => source.hostId === machine.hostId && source.remoteProfileId === summary.id);
					return [{ ...summary, ...(copy ? { localCopy: { profileId: copy[0], copiedAt: copy[1].copiedAt } } : {}) }];
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
			throw new CopyError("NOT_LINKED", "Sign in to Ayo Cloud to copy from your other machines.");
		}
		const machine = machines.find((candidate) => candidate.hostId === hostId);
		if (!machine) throw new CopyError("NOT_LINKED", "That machine is no longer linked to this one.");
		return machine;
	}

	/** Reads every page, validating and bounding each, before anything is written. */
	private async receive(machine: LinkedMachine, remoteProfileId: string): Promise<ReceivedProfile> {
		// One key for the whole copy; its private half never leaves this process.
		const key = createOneTimeKey();
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
				key,
				{
					path: `/api/v1/shared-browser-profiles/${encodeURIComponent(remoteProfileId)}/export${query}`,
					action: "export",
					profileId: remoteProfileId,
					cursor: cursor ?? "",
				},
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
				const sources = await this.copySources();
				const profiles = this.options.store.profiles;
				if ([...sources.values()].some((source) => source.hostId === hostId && source.remoteProfileId === remoteId)) {
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
				// Recorded before any cookie is written, so the copy is never shared on.
				await this.options.ledger.record(profile.id, source, this.options.store.profiles.map((candidate) => candidate.id));
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
				const source = (await this.copySources()).get(profileId);
				const profile = this.options.store.getProfile(profileId);
				if (!profile || !source) {
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
				const refreshed: BrowserProfileCopySource = {
					...source,
					...(data.summary.sourceBrowser ? { browser: data.summary.sourceBrowser } : {}),
					copiedAt: this.now().toISOString(),
				};
				// Also restores provenance an older build stripped from the registry.
				const updated = await this.options.store.setProfileSource(profileId, refreshed);
				await this.options.ledger.record(profileId, refreshed, this.options.store.profiles.map((candidate) => candidate.id)).catch(() => undefined);
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
		// The ledger entry goes only once the profile is gone: until then it withholds it.
		const deleted = await this.options.store.deleteProfile(profileId).then(() => true, () => false);
		if (deleted) await this.options.ledger.remove(profileId).catch(() => undefined);
	}
}
