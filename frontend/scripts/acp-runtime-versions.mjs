import { closeSync, mkdirSync, openSync, readdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { isAbsolute, join, relative, resolve } from "node:path";

// Versioned ACP runtime directories for Windows dev/daily builds.
//
// Why: on Windows a running chat session keeps node.exe and its modules open
// inside the runtime directory, and sessions outlive daemon restarts. Rebuilding
// resources/acp-runtime in place then fails with EPERM. This mirrors the daemon's
// frontend/daemon/dev-<time>-<pid>/ao.exe folders and dev-daemon.json manifest
// (scripts/build-daemon.mjs): every build gets a fresh directory, a manifest
// names the newest one, and old directories are pruned only when nothing holds
// them open. Packaged builds keep resources/acp-runtime untouched.

export const RUNTIME_BUILDS_DIRNAME = "acp-runtime-dev";
export const RUNTIME_MANIFEST_NAME = "current.json";
export const RUNTIME_DIR_PREFIX = "rt-";
export const KEEP_NEWEST_RUNTIMES = 5;

export function runtimeBuildsRoot(frontendRoot) {
	return join(frontendRoot, "resources", RUNTIME_BUILDS_DIRNAME);
}

export function newRuntimeDir(buildsRoot, now = Date.now(), pid = process.pid) {
	return join(buildsRoot, `${RUNTIME_DIR_PREFIX}${now}-${pid}`);
}

// The directory the newest successful build wrote to the manifest, or null when
// there is no manifest, it is malformed, or it points outside the builds root.
export function readCurrentRuntimeDir(buildsRoot) {
	try {
		const parsed = JSON.parse(readFileSync(join(buildsRoot, RUNTIME_MANIFEST_NAME), "utf8"));
		if (typeof parsed?.path !== "string") return null;
		const dir = resolve(parsed.path.trim());
		const rel = relative(resolve(buildsRoot), dir);
		if (!rel || rel.startsWith("..") || isAbsolute(rel)) return null;
		return dir;
	} catch {
		return null;
	}
}

// Written only after a build fully succeeded, via rename so a reader never sees
// a half-written manifest.
export function writeCurrentRuntimeDir(buildsRoot, dir) {
	mkdirSync(buildsRoot, { recursive: true });
	const temp = join(buildsRoot, `${RUNTIME_MANIFEST_NAME}.${process.pid}.tmp`);
	writeFileSync(temp, `${JSON.stringify({ path: dir }, null, 2)}\n`);
	renameSync(temp, join(buildsRoot, RUNTIME_MANIFEST_NAME));
}

// True while a process runs from `dir`. On Windows a running node.exe cannot be
// opened for writing (EBUSY), and probing that way changes nothing. Renaming or
// deleting is NOT a safe probe: Windows renames a directory under a running exe
// without complaint, and a recursive delete removes unlocked files first, either
// of which would break the live session that still runs from it.
export function runtimeInUse(dir, platform = process.platform) {
	if (platform !== "win32") return false;
	try {
		closeSync(openSync(join(dir, "node", "node.exe"), "r+"));
		return false;
	} catch (error) {
		return error?.code === "EBUSY" || error?.code === "EPERM" || error?.code === "EACCES";
	}
}

// Deletes old runtime directories beyond the newest `keep`, never `activeDir`,
// and skips any directory still in use. `inUse` and `remove` are injectable.
export function pruneRuntimeDirs(
	buildsRoot,
	{ activeDir, keep = KEEP_NEWEST_RUNTIMES, inUse = runtimeInUse, remove = rmSync } = {},
) {
	const result = { removed: [], skipped: [] };
	let entries;
	try {
		entries = readdirSync(buildsRoot, { withFileTypes: true })
			.filter((entry) => entry.isDirectory() && entry.name.startsWith(RUNTIME_DIR_PREFIX))
			.map((entry) => {
				const dir = join(buildsRoot, entry.name);
				return { dir, mtimeMs: statSync(dir).mtimeMs };
			})
			.sort((a, b) => b.mtimeMs - a.mtimeMs || (a.dir < b.dir ? 1 : -1));
	} catch {
		return result;
	}
	const active = activeDir ? resolve(activeDir) : "";
	for (const entry of entries.slice(keep)) {
		if (resolve(entry.dir) === active) continue;
		if (inUse(entry.dir)) {
			result.skipped.push(entry.dir);
			continue;
		}
		try {
			remove(entry.dir, { recursive: true, force: true });
			result.removed.push(entry.dir);
		} catch {
			result.skipped.push(entry.dir);
		}
	}
	return result;
}
