/**
 * AO-owned browser profile data. The renderer may use profile IDs, but it must
 * never receive filesystem paths or raw Electron partition names.
 */

export const BROWSER_PROFILE_REGISTRY_VERSION = 1 as const;
export const BROWSER_PROFILE_NAME_MAX_LENGTH = 64;
export const BROWSER_PROFILE_MAX_COUNT = 32;
export const BROWSER_PROFILE_MAX_BINDINGS = 1_024;
export const BROWSER_PROFILE_MAX_SESSION_ID_LENGTH = 160;
export const BROWSER_PROFILE_REGISTRY_FILE_NAME = "browser-profiles.json";

const UUID_PATTERN =
	/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const UNSAFE_RECORD_KEYS = new Set(["__proto__", "constructor", "prototype"]);

export type BrowserProfileId = string;

export const BROWSER_PROFILE_SOURCE_BROWSER_MAX_LENGTH = 64;
export const BROWSER_PROFILE_MACHINE_NAME_MAX_LENGTH = 128;
const HOST_ID_PATTERN = /^[A-Za-z0-9_.:-]{1,128}$/;

/**
 * Where a profile's data came from. An import records the browser; a copy
 * records the linked machine and the profile there, so it can be updated and
 * so it is never shared onward (no re-sharing chains).
 */
export type BrowserProfileSource =
	| { kind: "import"; browser: string }
	| {
		kind: "copy";
		browser?: string;
		hostId: string;
		machineName: string;
		remoteProfileId: BrowserProfileId;
		copiedAt: string;
	};

export type BrowserProfile = {
	id: BrowserProfileId;
	name: string;
	createdAt: string;
	updatedAt: string;
	source?: BrowserProfileSource;
};

export type BrowserProfileBinding = {
	/** `null` is an explicit, durable choice of Temporary — distinct from no binding at all. */
	profileId: BrowserProfileId | null;
	updatedAt: string;
};

export type BrowserProfileRegistry = {
	version: typeof BROWSER_PROFILE_REGISTRY_VERSION;
	profiles: BrowserProfile[];
	bindings: Record<string, BrowserProfileBinding>;
	/** Profile new sessions fall back to when they have no session-specific binding. */
	defaultProfileId: BrowserProfileId | null;
	/**
	 * Set once the oldest profile has been offered as the default. Registries
	 * saved without it get that default on load, so a later unset stays unset.
	 */
	defaultProfileSeeded: true;
};

export type BrowserProfileStoreError = {
	code: "BROWSER_PROFILE_STORE_CORRUPT" | "BROWSER_PROFILE_STORE_UNAVAILABLE";
	message: string;
};

export type BrowserProfileListState = {
	profiles: BrowserProfile[];
	defaultProfileId: BrowserProfileId | null;
	error?: BrowserProfileStoreError;
};

export type BrowserProfileViewState = {
	viewId: string;
	profileId: BrowserProfileId | null;
	profileName?: string;
	/** True when the selection is the per-worker temporary profile. */
	temporary: boolean;
};

/** Live browsers affected by deleting a named profile; no paths or partitions. */
export type BrowserProfileUsage = {
	viewId: string;
	sessionId: string;
	blocker: "agent-active" | "switching" | null;
};

export type BrowserProfileUsageResult = { sessions: BrowserProfileUsage[]; error?: string };
export type BrowserProfileDeleteResult = { error?: string };

export type BrowserMenuBounds = {
	x: number;
	y: number;
	width: number;
	height: number;
};

export type BrowserProfileMenuLabels = {
	temporary: string;
	manage: string;
	switchTitle: string;
	switchMessage: string;
	switchDetail: string;
	cancel: string;
	confirm: string;
};

export type BrowserProfileMenuInput = {
	viewId: string;
	bounds: BrowserMenuBounds;
	labels: BrowserProfileMenuLabels;
};

export type BrowserProfileSelectInput = {
	viewId: string;
	profileId: BrowserProfileId | null;
	labels: BrowserProfileMenuLabels;
};

export function isBrowserProfileId(value: unknown): value is BrowserProfileId {
	return typeof value === "string" && UUID_PATTERN.test(value);
}

export function normalizeBrowserProfileId(value: unknown): BrowserProfileId | null {
	return isBrowserProfileId(value) ? value.toLowerCase() : null;
}

/**
 * Names are display-only. They are deliberately not used in a path or an
 * Electron partition, but control characters are still rejected so a profile
 * cannot forge terminal/UI output.
 */
export function normalizeBrowserProfileName(value: unknown): string | null {
	if (typeof value !== "string") return null;
	const name = value.trim();
	if (
		name.length === 0 ||
		name.length > BROWSER_PROFILE_NAME_MAX_LENGTH ||
		/[\u0000-\u001f\u007f]/u.test(name)
	) {
		return null;
	}
	return name;
}

/** Display text from another machine or browser: trimmed, bounded, no control characters. */
export function normalizeBrowserProfileLabel(value: unknown, maxLength: number): string | null {
	if (typeof value !== "string") return null;
	const label = value.trim();
	if (label.length === 0 || label.length > maxLength || /[\u0000-\u001f\u007f]/u.test(label)) return null;
	return label;
}

/** A stored or received source record, or null when it is not one. */
export function normalizeBrowserProfileSource(value: unknown): BrowserProfileSource | null {
	if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
	const record = value as Record<string, unknown>;
	if (record.kind === "import") {
		const browser = normalizeBrowserProfileLabel(record.browser, BROWSER_PROFILE_SOURCE_BROWSER_MAX_LENGTH);
		return browser ? { kind: "import", browser } : null;
	}
	if (record.kind !== "copy") return null;
	const machineName = normalizeBrowserProfileLabel(record.machineName, BROWSER_PROFILE_MACHINE_NAME_MAX_LENGTH);
	const remoteProfileId = normalizeBrowserProfileId(record.remoteProfileId);
	const browser = record.browser === undefined
		? undefined
		: normalizeBrowserProfileLabel(record.browser, BROWSER_PROFILE_SOURCE_BROWSER_MAX_LENGTH);
	if (
		typeof record.hostId !== "string" ||
		!HOST_ID_PATTERN.test(record.hostId) ||
		!machineName ||
		!remoteProfileId ||
		browser === null ||
		typeof record.copiedAt !== "string" ||
		!Number.isFinite(Date.parse(record.copiedAt))
	) {
		return null;
	}
	return {
		kind: "copy",
		...(browser ? { browser } : {}),
		hostId: record.hostId,
		machineName,
		remoteProfileId,
		copiedAt: record.copiedAt,
	};
}

export function isValidBrowserProfileSessionId(value: unknown): value is string {
	return (
		typeof value === "string" &&
		value.length > 0 &&
		value.length <= BROWSER_PROFILE_MAX_SESSION_ID_LENGTH &&
		!UNSAFE_RECORD_KEYS.has(value) &&
		!Object.hasOwn(Object.prototype, value) &&
		!/[\u0000-\u001f\u007f]/u.test(value)
	);
}

/** The only partition constructor used by AO-owned named profiles. */
export function browserProfilePartition(id: BrowserProfileId): string {
	const normalized = normalizeBrowserProfileId(id);
	if (!normalized) throw new Error("Invalid browser profile ID");
	return `persist:ao-browser-profile-${normalized}`;
}
