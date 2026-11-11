import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { normalizeBrowserProfileId, normalizeBrowserProfileSource, type BrowserProfile, type BrowserProfileSource } from "../shared/browser-profiles";

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

/** The ledger exists but cannot be read, or is gone after one was kept: nothing may be shared. */
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

/**
 * Where it is recorded that this machine keeps a ledger: in the sharing
 * setting, a separate file, so a deleted ledger reads as lost, not as empty.
 */
export type BrowserProfileCopyLedgerMarker = {
	expectsCopyLedger(): Promise<boolean>;
	markCopyLedger(): Promise<void>;
};

export class BrowserProfileCopyLedger {
	private readonly file: string;
	private queue: Promise<void> = Promise.resolve();

	constructor(stateDir: string, private readonly marker?: BrowserProfileCopyLedgerMarker) {
		this.file = path.join(stateDir, BROWSER_PROFILE_COPIES_FILE_NAME);
	}

	private async read(): Promise<{ ids: Set<string>; sources: Map<string, BrowserProfileCopySource>; missing: boolean }> {
		let text: string;
		try {
			text = await readFile(this.file, "utf8");
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code === "ENOENT") return { ids: new Set(), sources: new Map(), missing: true };
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
		return { ids, sources, missing: false };
	}

	/**
	 * The recorded copies, for a copy's provenance. A missing file is none;
	 * any other unreadable file throws. Sharing reads the ledger through ensure.
	 */
	async load(): Promise<BrowserProfileCopies> {
		const { ids, sources } = await this.read();
		return { ids, sources };
	}

	/**
	 * The copies sharing must withhold, after recording any the registry knows
	 * that the ledger does not: copies an earlier build made before it kept a
	 * ledger, and profiles whose provenance cannot be read. A ledger that is
	 * gone after this machine started keeping one throws: what it held can no
	 * longer be told apart from local profiles.
	 */
	ensure(profiles: readonly BrowserProfile[]): Promise<BrowserProfileCopies> {
		return this.serialized(async () => {
			const { ids, sources, missing } = await this.read();
			if (missing && await this.marker?.expectsCopyLedger()) throw new BrowserProfileCopyLedgerError();
			let added = false;
			for (const profile of profiles) {
				if ((profile.source?.kind !== "copy" && profile.source?.kind !== "unknown") || ids.has(profile.id)) continue;
				ids.add(profile.id);
				if (profile.source.kind === "copy") sources.set(profile.id, profile.source);
				added = true;
			}
			if (missing || added) await this.persist(ids, sources);
			await this.mark();
			return { ids, sources };
		});
	}

	/** Records profileId as a copy of source, dropping entries for profiles no longer in keep. */
	record(profileId: string, source: BrowserProfileCopySource, keep: Iterable<string>): Promise<void> {
		return this.change((ids, sources) => {
			const kept = new Set([...keep, profileId]);
			for (const id of [...ids]) {
				if (kept.has(id)) continue;
				ids.delete(id);
				sources.delete(id);
			}
			ids.add(profileId);
			sources.set(profileId, source);
		});
	}

	/** Forgets a copy that was never finished. */
	remove(profileId: string): Promise<void> {
		return this.change((ids, sources) => {
			ids.delete(profileId);
			sources.delete(profileId);
		});
	}

	private change(apply: (ids: Set<string>, sources: Map<string, BrowserProfileCopySource>) => void): Promise<void> {
		return this.serialized(async () => {
			// An unreadable ledger is replaced only by a write that still knows
			// every id it held; failing here keeps those profiles withheld.
			const { ids, sources } = await this.read();
			apply(ids, sources);
			await this.persist(ids, sources);
			await this.mark();
		});
	}

	private serialized<T>(operation: () => Promise<T>): Promise<T> {
		const result = this.queue.then(operation, operation);
		this.queue = result.then(() => undefined, () => undefined);
		return result;
	}

	/** Best effort: the ledger itself is already written. */
	private async mark(): Promise<void> {
		await this.marker?.markCopyLedger().catch(() => undefined);
	}

	private async persist(ids: ReadonlySet<string>, sources: ReadonlyMap<string, BrowserProfileCopySource>): Promise<void> {
		const copies: Record<string, unknown> = {};
		for (const id of ids) copies[id] = sources.get(id) ?? { kind: "unknown" };
		await mkdir(path.dirname(this.file), { recursive: true, mode: 0o750 });
		const temporary = path.join(path.dirname(this.file), `.${BROWSER_PROFILE_COPIES_FILE_NAME}.${process.pid}.${randomUUID()}.tmp`);
		try {
			await writeFile(temporary, `${JSON.stringify({ version: 1, copies }, null, 2)}\n`, { mode: 0o600 });
			await rename(temporary, this.file);
		} catch (error) {
			await rm(temporary, { force: true }).catch(() => undefined);
			throw error;
		}
	}
}
