import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { BROWSER_IMPORT_MAX_COOKIES } from "../shared/browser-profile-import";
import type { SharedBrowserProfileSummary } from "../shared/browser-profile-share";
import { normalizeBrowserProfileId, type BrowserProfile } from "../shared/browser-profiles";
import type { BrowserHistoryEntry, BrowserHistoryStore } from "./browser-history-store";
import type { BrowserProfileCopyLedger } from "./browser-profile-copy-ledger";
import { isAntiBotCookie } from "./browser-profile-import";
import type { BrowserProfileReceiverVerifier, VerifiedReceiver } from "./browser-profile-receiver";
import { sealContext, sealPayload, type SealedPayload } from "./browser-profile-share-crypto";
import type { BrowserProfileStore } from "./browser-profile-store";

/**
 * The machine that owns a browser profile: it answers its own daemon's
 * `__browser-profile-share-*` actions, which the daemon serves only to a
 * request that carries the account credential on the remote listener.
 * Nothing is shared until this machine's owner turns sharing on; every
 * request must carry a ticket signed by another machine of the account (see
 * browser-profile-receiver.ts), and every answer is sealed to that machine.
 * Only this machine's own profiles are shared: a profile copied from another
 * machine, or one whose provenance cannot be read, never is.
 */

export const BROWSER_PROFILE_SHARING_FILE_NAME = "browser-profile-sharing.json";
/** Keeps one export page well under the 8 MiB desktop bridge frame. */
export const BROWSER_PROFILE_SHARE_PAGE_BYTES = 3 * 1024 * 1024;
const CURSOR_PATTERN = /^([0-9]{1,6}):([0-9]{1,6})$/;

/** The cookie shape that crosses the remote connection (Electron's own Cookie fields). */
export type SharedBrowserCookie = {
	name: string;
	value: string;
	domain: string;
	hostOnly: boolean;
	path: string;
	secure: boolean;
	httpOnly: boolean;
	sameSite?: "unspecified" | "no_restriction" | "lax" | "strict";
	expirationDate?: number;
};

export type SharedBrowserProfilePage = {
	profile: SharedBrowserProfileSummary;
	cookies: SharedBrowserCookie[];
	history: BrowserHistoryEntry[];
	nextCursor?: string;
};

/** A failure the daemon maps by code. Messages are fixed: no profile data in them. */
export class BrowserProfileShareError extends Error {
	constructor(
		readonly code:
			| "BROWSER_PROFILE_SHARING_OFF"
			| "BROWSER_PROFILE_NOT_FOUND"
			| "BROWSER_PROFILE_NOT_SHARED"
			| "BROWSER_PROFILE_RECEIVER_UNVERIFIED"
			| "INVALID_CURSOR"
			| "BROWSER_PROFILE_SHARE_FAILED",
		message: string,
	) {
		super(message);
		this.name = "BrowserProfileShareError";
	}
}

/**
 * "Share my browser profiles with my linked machines". Off by default, and off
 * again whenever the file is missing, unreadable, or not exactly this shape.
 */
export class BrowserProfileSharingSetting {
	private readonly file: string;
	private enabled = false;
	/** Whether this machine has started keeping a copy ledger; see expectsCopyLedger. */
	private copyLedger = false;
	private turnedOff = 0;
	private loaded: Promise<void> | null = null;
	private queue: Promise<void> = Promise.resolve();

	constructor(stateDir: string) {
		this.file = path.join(stateDir, BROWSER_PROFILE_SHARING_FILE_NAME);
	}

	private load(): Promise<void> {
		this.loaded ??= (async () => {
			try {
				const parsed = JSON.parse(await readFile(this.file, "utf8")) as unknown;
				const valid = typeof parsed === "object" && parsed !== null && (parsed as Record<string, unknown>).version === 1;
				this.enabled = valid && (parsed as Record<string, unknown>).shareWithLinkedMachines === true;
				this.copyLedger = valid && (parsed as Record<string, unknown>).copyLedger === true;
			} catch {
				this.enabled = false;
			}
		})();
		return this.loaded;
	}

	/**
	 * Whether a copy ledger should exist: one was written here. A ledger that
	 * is gone after that is not "no copies" but lost, and nothing is shared
	 * until the owner turns sharing on again.
	 */
	async expectsCopyLedger(): Promise<boolean> {
		await this.load();
		return this.copyLedger;
	}

	/** Records that this machine keeps a copy ledger. */
	markCopyLedger(): Promise<void> {
		return this.enqueue(async () => {
			if (!this.copyLedger) await this.persist(this.enabled, true);
		});
	}

	private enqueue(change: () => Promise<void>): Promise<void> {
		const write = this.queue.then(async () => {
			await this.load();
			await change();
		});
		this.queue = write.catch(() => undefined);
		return write;
	}

	private async persist(enabled: boolean, copyLedger: boolean): Promise<void> {
		await mkdir(path.dirname(this.file), { recursive: true, mode: 0o750 });
		const temporary = path.join(path.dirname(this.file), `.${BROWSER_PROFILE_SHARING_FILE_NAME}.${process.pid}.${randomUUID()}.tmp`);
		try {
			const value = { version: 1, shareWithLinkedMachines: enabled, ...(copyLedger ? { copyLedger: true } : {}) };
			await writeFile(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
			await rename(temporary, this.file);
		} catch (error) {
			await rm(temporary, { force: true }).catch(() => undefined);
			throw error;
		}
		this.enabled = enabled;
		this.copyLedger = copyLedger;
	}

	async isEnabled(): Promise<boolean> {
		await this.load();
		return this.enabled;
	}

	/**
	 * Counts the times sharing was turned off. A read that started before the
	 * owner turned it off must not be answered, even if it is back on by then.
	 */
	generation(): number {
		return this.turnedOff;
	}

	async setEnabled(enabled: boolean): Promise<boolean> {
		// Counted at once, before the write: reads already running stop now.
		if (!enabled) this.turnedOff += 1;
		await this.enqueue(async () => {
			// Turning sharing on, which the owner confirms in a native dialog, is
			// the one way past a lost copy ledger: the next answer rebuilds it from
			// the profile registry. "On" while already on changes nothing.
			const copyLedger = enabled && !this.enabled ? false : this.copyLedger;
			await this.persist(enabled, copyLedger);
		});
		return this.enabled;
	}
}

/** The slice of Electron's Cookie this module reads. */
type ElectronCookie = {
	name: string;
	value: string;
	domain?: string;
	hostOnly?: boolean;
	path?: string;
	secure?: boolean;
	httpOnly?: boolean;
	session?: boolean;
	expirationDate?: number;
	sameSite?: string;
};

export type BrowserProfileShareSourceOptions = {
	store: BrowserProfileStore;
	history: Pick<BrowserHistoryStore, "entries">;
	sharing: Pick<BrowserProfileSharingSetting, "isEnabled" | "generation">;
	/** Every profile copied from another machine, kept apart from the registry. */
	copies: Pick<BrowserProfileCopyLedger, "ensure">;
	receivers: Pick<BrowserProfileReceiverVerifier, "verify">;
	/** Every cookie in the profile's Electron partition. */
	cookies: (profileId: string) => Promise<ElectronCookie[]>;
	now?: () => Date;
};

const SAME_SITE = new Set(["unspecified", "no_restriction", "lax", "strict"]);

/**
 * Whether a cookie's host is a site another machine may receive. A host that
 * names a machine rather than a site, such as localhost, an IP address, or a
 * single label like "intranet", would mean something else there: a copied
 * session cookie for it could sign in to the receiver's own local apps.
 */
export function isShareableCookieHost(domain: string): boolean {
	const host = domain.toLowerCase().replace(/^\.+/, "").replace(/\.$/, "");
	if (!host.includes(".") || host.includes(":") || host.startsWith("[")) return false;
	if (host === "localhost" || host.endsWith(".localhost")) return false;
	return !/^[0-9.]+$/.test(host);
}

function cookieKey(cookie: SharedBrowserCookie): string {
	return `${cookie.domain}\0${cookie.path}\0${cookie.name}`;
}

function byteLength(value: unknown): number {
	return Buffer.byteLength(JSON.stringify(value), "utf8") + 1;
}

export class BrowserProfileShareSource {
	private readonly now: () => Date;

	constructor(private readonly options: BrowserProfileShareSourceOptions) {
		this.now = options.now ?? (() => new Date());
	}

	/** Throws unless sharing is on; returns the setting's generation for requireSharingStill. */
	private async requireSharing(): Promise<number> {
		const generation = this.options.sharing.generation();
		if (!(await this.options.sharing.isEnabled())) throw this.sharingOff();
		return generation;
	}

	/** Re-checked just before answering: turning sharing off stops a read already running. */
	private async requireSharingStill(generation: number): Promise<void> {
		if (this.options.sharing.generation() !== generation || !(await this.options.sharing.isEnabled())) throw this.sharingOff();
	}

	private sharingOff(): BrowserProfileShareError {
		return new BrowserProfileShareError("BROWSER_PROFILE_SHARING_OFF", "Browser profile sharing is turned off on this machine.");
	}

	private async verifiedReceiver(args: unknown): Promise<VerifiedReceiver> {
		const record = typeof args === "object" && args !== null ? args as Record<string, unknown> : {};
		try {
			return await this.options.receivers.verify(record.ticket, record.signature);
		} catch {
			throw new BrowserProfileShareError("BROWSER_PROFILE_RECEIVER_UNVERIFIED", "The machine asking could not be verified.");
		}
	}

	/**
	 * Whether this machine may share a profile: only its own, with a positive
	 * record of where it came from (made here, or imported here) and not in
	 * the copy ledger. A copy, a provenance this build cannot read, and a copy
	 * an older build stripped of its provenance are all withheld.
	 */
	private isOwnProfile(profile: BrowserProfile, copies: ReadonlySet<string>): boolean {
		if (copies.has(profile.id)) return false;
		return profile.source === undefined || profile.source.kind === "import";
	}

	private async copiedIds(): Promise<ReadonlySet<string>> {
		try {
			// Also records copies the registry knows that the ledger does not.
			return (await this.options.copies.ensure(this.options.store.profiles)).ids;
		} catch {
			// Without the ledger a stripped copy cannot be told apart: share nothing.
			throw new BrowserProfileShareError("BROWSER_PROFILE_SHARE_FAILED", "Browser profiles could not be read.");
		}
	}

	/**
	 * Cookies a linked machine can use: not expired, not anti-bot tokens that are
	 * bound to this device, capped at the import limit (longest-lived first, so
	 * session sign-ins survive), and in a stable order so pages line up.
	 */
	private async shareableCookies(profileId: string): Promise<SharedBrowserCookie[]> {
		const nowSeconds = this.now().getTime() / 1_000;
		const cookies: SharedBrowserCookie[] = [];
		for (const cookie of await this.options.cookies(profileId)) {
			if (typeof cookie.name !== "string" || !cookie.name || typeof cookie.value !== "string") continue;
			if (typeof cookie.domain !== "string" || !cookie.domain || isAntiBotCookie(cookie.name)) continue;
			if (!isShareableCookieHost(cookie.domain)) continue;
			const expirationDate = cookie.session === true
				? undefined
				: typeof cookie.expirationDate === "number" && Number.isFinite(cookie.expirationDate)
					? cookie.expirationDate
					: undefined;
			if (expirationDate !== undefined && expirationDate <= nowSeconds) continue;
			cookies.push({
				name: cookie.name,
				value: cookie.value,
				domain: cookie.domain,
				hostOnly: cookie.hostOnly === true,
				path: typeof cookie.path === "string" && cookie.path.startsWith("/") ? cookie.path : "/",
				secure: cookie.secure === true,
				httpOnly: cookie.httpOnly === true,
				...(typeof cookie.sameSite === "string" && SAME_SITE.has(cookie.sameSite)
					? { sameSite: cookie.sameSite as SharedBrowserCookie["sameSite"] }
					: {}),
				...(expirationDate !== undefined ? { expirationDate } : {}),
			});
		}
		const kept = cookies.length > BROWSER_IMPORT_MAX_COOKIES
			? [...cookies]
				.sort((a, b) => (b.expirationDate ?? Infinity) - (a.expirationDate ?? Infinity) || cookieKey(a).localeCompare(cookieKey(b)))
				.slice(0, BROWSER_IMPORT_MAX_COOKIES)
			: cookies;
		return kept.sort((a, b) => cookieKey(a).localeCompare(cookieKey(b)));
	}

	private summary(profile: BrowserProfile, cookieCount: number, history: BrowserHistoryEntry[]): SharedBrowserProfileSummary {
		// The newest visit says when the profile was last used; the record's own
		// timestamp covers a profile nobody has browsed with since its import.
		const lastVisited = history[0]?.lastVisited;
		const updatedAt = lastVisited && Date.parse(lastVisited) > Date.parse(profile.updatedAt) ? lastVisited : profile.updatedAt;
		return {
			id: profile.id,
			name: profile.name,
			...(profile.source?.kind === "import" ? { sourceBrowser: profile.source.browser } : {}),
			cookieCount,
			historyCount: history.length,
			updatedAt,
		};
	}

	/** The profiles this machine shares, sealed to the verified receiver. */
	async list(args: unknown): Promise<SealedPayload> {
		const generation = await this.requireSharing();
		const receiver = await this.verifiedReceiver(args);
		const profiles = await this.listProfiles();
		await this.requireSharingStill(generation);
		return sealPayload({ profiles }, receiver.key, sealContext({ ...receiver, action: "list" }));
	}

	/** One page of a profile, sealed to the verified receiver; see exportPlainPage. */
	async exportPage(args: unknown): Promise<SealedPayload> {
		const generation = await this.requireSharing();
		const receiver = await this.verifiedReceiver(args);
		const record = typeof args === "object" && args !== null ? args as Record<string, unknown> : {};
		const page = await this.exportPlainPage(record.profileId, record.cursor);
		await this.requireSharingStill(generation);
		return sealPayload(page, receiver.key, sealContext({
			...receiver,
			action: "export",
			profileId: page.profile.id,
			cursor: typeof record.cursor === "string" ? record.cursor : "",
		}));
	}

	private async listProfiles(): Promise<SharedBrowserProfileSummary[]> {
		await this.options.store.load();
		const copies = await this.copiedIds();
		try {
			const profiles: SharedBrowserProfileSummary[] = [];
			for (const profile of this.options.store.profiles.filter((candidate) => this.isOwnProfile(candidate, copies))) {
				const [cookies, history] = await Promise.all([
					this.shareableCookies(profile.id),
					this.options.history.entries(profile.id),
				]);
				profiles.push(this.summary(profile, cookies.length, history));
			}
			return profiles;
		} catch {
			throw new BrowserProfileShareError("BROWSER_PROFILE_SHARE_FAILED", "Browser profiles could not be read.");
		}
	}

	/**
	 * One page of a profile's cookies, then its history, up to the page budget.
	 * The cursor is "<cookie offset>:<history offset>".
	 */
	private async exportPlainPage(rawProfileId: unknown, rawCursorValue: unknown): Promise<SharedBrowserProfilePage> {
		const profileId = normalizeBrowserProfileId(rawProfileId);
		if (!profileId) throw new BrowserProfileShareError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
		const rawCursor = rawCursorValue === undefined ? "0:0" : rawCursorValue;
		const cursor = typeof rawCursor === "string" ? CURSOR_PATTERN.exec(rawCursor) : null;
		if (!cursor) throw new BrowserProfileShareError("INVALID_CURSOR", "Export cursor is invalid.");
		await this.options.store.load();
		const profile = this.options.store.getProfile(profileId);
		if (!profile) throw new BrowserProfileShareError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
		if (!this.isOwnProfile(profile, await this.copiedIds())) {
			throw new BrowserProfileShareError("BROWSER_PROFILE_NOT_SHARED", "Profiles copied from another machine are not shared.");
		}
		let cookies: SharedBrowserCookie[];
		let history: BrowserHistoryEntry[];
		try {
			[cookies, history] = await Promise.all([
				this.shareableCookies(profileId),
				this.options.history.entries(profileId),
			]);
		} catch {
			throw new BrowserProfileShareError("BROWSER_PROFILE_SHARE_FAILED", "Browser profile could not be read.");
		}
		let cookieOffset = Number(cursor[1]);
		let historyOffset = Number(cursor[2]);
		if (cookieOffset > cookies.length || historyOffset > history.length) {
			throw new BrowserProfileShareError("INVALID_CURSOR", "Export cursor is invalid.");
		}
		const summary = this.summary(profile, cookies.length, history);
		let budget = BROWSER_PROFILE_SHARE_PAGE_BYTES - byteLength(summary);
		const pageCookies: SharedBrowserCookie[] = [];
		const pageHistory: BrowserHistoryEntry[] = [];
		// Every page carries at least one item, so an export always progresses.
		while (cookieOffset < cookies.length) {
			const size = byteLength(cookies[cookieOffset]);
			if (size > budget && (pageCookies.length > 0)) break;
			pageCookies.push(cookies[cookieOffset]!);
			budget -= size;
			cookieOffset += 1;
		}
		if (cookieOffset === cookies.length) {
			while (historyOffset < history.length) {
				const size = byteLength(history[historyOffset]);
				if (size > budget && (pageCookies.length > 0 || pageHistory.length > 0)) break;
				pageHistory.push(history[historyOffset]!);
				budget -= size;
				historyOffset += 1;
			}
		}
		const done = cookieOffset === cookies.length && historyOffset === history.length;
		return {
			profile: summary,
			cookies: pageCookies,
			history: pageHistory,
			...(done ? {} : { nextCursor: `${cookieOffset}:${historyOffset}` }),
		};
	}
}
