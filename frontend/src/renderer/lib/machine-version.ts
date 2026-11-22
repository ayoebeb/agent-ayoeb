/** How a computer's Ayo release compares with this computer's. */
export type VersionRelation = "same" | "behind" | "ahead" | "unknown";

type ParsedVersion = { core: [number, number, number]; pre: string[] };

const VERSION_PATTERN = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z.-]+)?$/;

export function parseVersion(raw: string | null | undefined): ParsedVersion | null {
	const match = VERSION_PATTERN.exec((raw ?? "").trim());
	if (!match) return null;
	return {
		core: [Number(match[1]), Number(match[2]), Number(match[3])],
		pre: match[4] ? match[4].split(".") : [],
	};
}

// Semver precedence for the part after the dash: a release outranks its own
// pre-releases, numeric identifiers rank below words, and more identifiers win a tie.
function comparePrerelease(left: string[], right: string[]): number {
	if (left.length === 0 && right.length === 0) return 0;
	if (left.length === 0) return 1;
	if (right.length === 0) return -1;
	for (let index = 0; index < Math.max(left.length, right.length); index++) {
		const a = left[index];
		const b = right[index];
		if (a === undefined) return -1;
		if (b === undefined) return 1;
		const aNumeric = /^\d+$/.test(a);
		const bNumeric = /^\d+$/.test(b);
		if (aNumeric && bNumeric) {
			if (Number(a) !== Number(b)) return Number(a) < Number(b) ? -1 : 1;
			continue;
		}
		if (aNumeric !== bNumeric) return aNumeric ? -1 : 1;
		if (a !== b) return a < b ? -1 : 1;
	}
	return 0;
}

/** Negative when `left` is older, positive when newer, 0 when equal, null when either is not a version. */
export function compareVersions(left: string | null | undefined, right: string | null | undefined): number | null {
	const a = parseVersion(left);
	const b = parseVersion(right);
	if (!a || !b) return null;
	for (let index = 0; index < 3; index++) {
		if (a.core[index] !== b.core[index]) return a.core[index] < b.core[index] ? -1 : 1;
	}
	return comparePrerelease(a.pre, b.pre);
}

/**
 * Whether a linked computer runs an older or newer Ayo than this one. An unknown
 * answer (an older Ayo that does not report its version, a development build, or
 * a version this cannot read) never counts as a mismatch.
 */
export function versionRelation(host: string | null | undefined, local: string | null | undefined): VersionRelation {
	const order = compareVersions(host, local);
	if (order === null) return "unknown";
	if (order === 0) return "same";
	return order < 0 ? "behind" : "ahead";
}
