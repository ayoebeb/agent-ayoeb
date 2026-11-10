import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { BROWSER_IMPORT_MAX_COOKIES } from "../shared/browser-profile-import";
import type { SharedBrowserProfileSummary } from "../shared/browser-profile-share";
import { normalizeBrowserProfileId, type BrowserProfile } from "../shared/browser-profiles";
import type { BrowserHistoryEntry, BrowserHistoryStore } from "./browser-history-store";
import { isAntiBotCookie } from "./browser-profile-import";
import type { BrowserProfileStore } from "./browser-profile-store";

/**
 * The machine that owns a browser profile: it answers its own daemon's
 * `__browser-profile-share-*` actions, which the daemon serves only to a
 * linked machine over the authenticated remote-host connection. Nothing is
 * shared until this machine's owner turns sharing on, and a profile that was
 * itself copied from another machine is never shared onward.
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
		readonly code: "BROWSER_PROFILE_SHARING_OFF" | "BROWSER_PROFILE_NOT_FOUND" | "BROWSER_PROFILE_NOT_SHARED" | "INVALID_CURSOR" | "BROWSER_PROFILE_SHARE_FAILED",
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
	private loaded: Promise<void> | null = null;
	private queue: Promise<void> = Promise.resolve();

	constructor(stateDir: string) {
		this.file = path.join(stateDir, BROWSER_PROFILE_SHARING_FILE_NAME);
	}

	private load(): Promise<void> {
		this.loaded ??= (async () => {
			try {
				const parsed = JSON.parse(await readFile(this.file, "utf8")) as unknown;
				this.enabled = typeof parsed === "object" && parsed !== null
					&& (parsed as Record<string, unknown>).version === 1
					&& (parsed as Record<string, unknown>).shareWithLinkedMachines === true;
			} catch {
				this.enabled = false;
			}
		})();
		return this.loaded;
	}

	async isEnabled(): Promise<boolean> {
		await this.load();
		return this.enabled;
	}

	async setEnabled(enabled: boolean): Promise<boolean> {
		const write = this.queue.then(async () => {
			await this.load();
			await mkdir(path.dirname(this.file), { recursive: true, mode: 0o750 });
			const temporary = path.join(path.dirname(this.file), `.${BROWSER_PROFILE_SHARING_FILE_NAME}.${process.pid}.${randomUUID()}.tmp`);
			try {
				await writeFile(temporary, `${JSON.stringify({ version: 1, shareWithLinkedMachines: enabled }, null, 2)}\n`, { mode: 0o600 });
				await rename(temporary, this.file);
			} catch (error) {
				await rm(temporary, { force: true }).catch(() => undefined);
				throw error;
			}
			this.enabled = enabled;
		});
		this.queue = write.catch(() => undefined);
		await write;
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
	sharing: Pick<BrowserProfileSharingSetting, "isEnabled">;
	/** Every cookie in the profile's Electron partition. */
	cookies: (profileId: string) => Promise<ElectronCookie[]>;
	now?: () => Date;
};

const SAME_SITE = new Set(["unspecified", "no_restriction", "lax", "strict"]);

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

	private async requireSharing(): Promise<void> {
		if (!(await this.options.sharing.isEnabled())) {
			throw new BrowserProfileShareError(
				"BROWSER_PROFILE_SHARING_OFF",
				"Browser profile sharing is turned off on this machine.",
			);
		}
	}

	/** Profiles this machine may share: every profile not copied from another machine. */
	private sharedProfiles(): BrowserProfile[] {
		return this.options.store.profiles.filter((profile) => profile.source?.kind !== "copy");
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

	async list(): Promise<{ profiles: SharedBrowserProfileSummary[] }> {
		await this.requireSharing();
		await this.options.store.load();
		try {
			const profiles: SharedBrowserProfileSummary[] = [];
			for (const profile of this.sharedProfiles()) {
				const [cookies, history] = await Promise.all([
					this.shareableCookies(profile.id),
					this.options.history.entries(profile.id),
				]);
				profiles.push(this.summary(profile, cookies.length, history));
			}
			return { profiles };
		} catch {
			throw new BrowserProfileShareError("BROWSER_PROFILE_SHARE_FAILED", "Browser profiles could not be read.");
		}
	}

	/**
	 * One page of a profile's cookies, then its history, up to the page budget.
	 * The cursor is "<cookie offset>:<history offset>".
	 */
	async exportPage(args: unknown): Promise<SharedBrowserProfilePage> {
		await this.requireSharing();
		const record = typeof args === "object" && args !== null ? args as Record<string, unknown> : {};
		const profileId = normalizeBrowserProfileId(record.profileId);
		if (!profileId) throw new BrowserProfileShareError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
		const rawCursor = record.cursor === undefined ? "0:0" : record.cursor;
		const cursor = typeof rawCursor === "string" ? CURSOR_PATTERN.exec(rawCursor) : null;
		if (!cursor) throw new BrowserProfileShareError("INVALID_CURSOR", "Export cursor is invalid.");
		await this.options.store.load();
		const profile = this.options.store.getProfile(profileId);
		if (!profile) throw new BrowserProfileShareError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
		if (profile.source?.kind === "copy") {
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
