import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { normalizeBrowserProfileId, normalizeBrowserProfileSource, type BrowserProfileSource } from "../shared/browser-profiles";

/**
 * Every local profile that was copied from another machine, kept apart from
 * the profile registry. The registry also records a copy's provenance, but an
 * older Orchestrator build that rewrites the registry drops fields it does not
 * know, which would make a copy look like a local profile and share it onward.
 * No other build writes this file, so a copy stays withheld from sharing, and
 * keeps its link to the machine it came from, whatever happens to the
 * registry.
 */

export const BROWSER_PROFILE_COPIES_FILE_NAME = "browser-profile-copies.json";

export type BrowserProfileCopySource = Extract<BrowserProfileSource, { kind: "copy" }>;

/** The ledger exists but cannot be read: nothing may be shared until it can. */
export class BrowserProfileCopyLedgerError extends Error {
	constructor() {
		super("The list of copied browser profiles could not be read.");
		this.name = "BrowserProfileCopyLedgerError";
	}
}

export type BrowserProfileCopies = {
	/** Every recorded copy's profile id, including any whose provenance is unreadable. */
	ids: ReadonlySet<string>;
	sources: ReadonlyMap<string, BrowserProfileCopySource>;
};

export class BrowserProfileCopyLedger {
	private readonly file: string;
	private queue: Promise<void> = Promise.resolve();

	constructor(stateDir: string) {
		this.file = path.join(stateDir, BROWSER_PROFILE_COPIES_FILE_NAME);
	}

	/** The recorded copies. A missing file is none; any other unreadable file throws. */
	async load(): Promise<BrowserProfileCopies> {
		let text: string;
		try {
			text = await readFile(this.file, "utf8");
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code === "ENOENT") return { ids: new Set(), sources: new Map() };
			throw new BrowserProfileCopyLedgerError();
		}
		let parsed: unknown;
		try {
			parsed = JSON.parse(text);
		} catch {
			throw new BrowserProfileCopyLedgerError();
		}
		const copies = typeof parsed === "object" && parsed !== null && (parsed as Record<string, unknown>).version === 1
			? (parsed as Record<string, unknown>).copies
			: undefined;
		if (typeof copies !== "object" || copies === null || Array.isArray(copies)) throw new BrowserProfileCopyLedgerError();
		const ids = new Set<string>();
		const sources = new Map<string, BrowserProfileCopySource>();
		for (const [key, value] of Object.entries(copies)) {
			const id = normalizeBrowserProfileId(key);
			if (!id) continue;
			// An entry whose provenance cannot be read still withholds its profile.
			ids.add(id);
			const source = normalizeBrowserProfileSource(value);
			if (source?.kind === "copy") sources.set(id, source);
		}
		return { ids, sources };
	}

	/** Records profileId as a copy of source, dropping entries for profiles no longer in keep. */
	record(profileId: string, source: BrowserProfileCopySource, keep: Iterable<string>): Promise<void> {
		return this.write((copies) => {
			const kept = new Set([...keep, profileId]);
			for (const id of Object.keys(copies)) if (!kept.has(id)) delete copies[id];
			copies[profileId] = source;
		});
	}

	/** Forgets a copy that was never finished. */
	remove(profileId: string): Promise<void> {
		return this.write((copies) => {
			delete copies[profileId];
		});
	}

	private write(change: (copies: Record<string, unknown>) => void): Promise<void> {
		const write = this.queue.then(async () => {
			// An unreadable ledger is replaced only by a write that still knows
			// every id it held; failing here keeps those profiles withheld.
			const current = await this.load();
			const copies: Record<string, unknown> = {};
			for (const id of current.ids) copies[id] = current.sources.get(id) ?? { kind: "unknown" };
			change(copies);
			await mkdir(path.dirname(this.file), { recursive: true, mode: 0o750 });
			const temporary = path.join(path.dirname(this.file), `.${BROWSER_PROFILE_COPIES_FILE_NAME}.${process.pid}.${randomUUID()}.tmp`);
			try {
				await writeFile(temporary, `${JSON.stringify({ version: 1, copies }, null, 2)}\n`, { mode: 0o600 });
				await rename(temporary, this.file);
			} catch (error) {
				await rm(temporary, { force: true }).catch(() => undefined);
				throw error;
			}
		});
		this.queue = write.catch(() => undefined);
		return write;
	}
}
