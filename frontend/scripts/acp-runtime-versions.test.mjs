// @vitest-environment node
import { spawn } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
	KEEP_NEWEST_RUNTIMES,
	newRuntimeDir,
	pruneRuntimeDirs,
	runtimeInUse,
	readCurrentRuntimeDir,
	runtimeBuildsRoot,
	writeCurrentRuntimeDir,
} from "./acp-runtime-versions.mjs";

const cleanup = [];
afterEach(() => {
	while (cleanup.length) {
		try {
			rmSync(cleanup.pop(), { recursive: true, force: true });
		} catch {
			// A held handle in a failed test; the OS temp dir cleaner gets it.
		}
	}
});

function scratchRoot() {
	const root = mkdtempSync(join(tmpdir(), "ao-acp-versions-"));
	cleanup.push(root);
	return runtimeBuildsRoot(root);
}

// Creates rt-<n> with one file, aged so that a higher n is newer.
function makeRuntime(buildsRoot, n) {
	const dir = newRuntimeDir(buildsRoot, 1000 + n, 1);
	mkdirSync(join(dir, "node"), { recursive: true });
	const file = join(dir, "node", "node.exe");
	writeFileSync(file, "x");
	const when = new Date(Date.now() - (100 - n) * 60_000);
	utimesSync(dir, when, when);
	return { dir, file };
}

describe("runtime path selection", () => {
	it("names each build after time and pid, inside the builds root", () => {
		const root = scratchRoot();
		expect(newRuntimeDir(root, 5, 7)).toBe(join(root, "rt-5-7"));
	});

	it("returns null without a manifest", () => {
		expect(readCurrentRuntimeDir(scratchRoot())).toBeNull();
	});

	it("points at the newest manifest entry", () => {
		const root = scratchRoot();
		const a = makeRuntime(root, 1).dir;
		const b = makeRuntime(root, 2).dir;
		writeCurrentRuntimeDir(root, a);
		expect(readCurrentRuntimeDir(root)).toBe(a);
		writeCurrentRuntimeDir(root, b);
		expect(readCurrentRuntimeDir(root)).toBe(b);
	});

	it("rejects a manifest path outside the builds root and malformed manifests", () => {
		const root = scratchRoot();
		mkdirSync(root, { recursive: true });
		writeFileSync(join(root, "current.json"), JSON.stringify({ path: join(root, "..", "elsewhere") }));
		expect(readCurrentRuntimeDir(root)).toBeNull();
		writeFileSync(join(root, "current.json"), JSON.stringify({ path: root }));
		expect(readCurrentRuntimeDir(root)).toBeNull();
		writeFileSync(join(root, "current.json"), "{not json");
		expect(readCurrentRuntimeDir(root)).toBeNull();
	});
});

describe("pruneRuntimeDirs", () => {
	it("keeps the newest few and removes the rest", () => {
		const root = scratchRoot();
		const runtimes = Array.from({ length: KEEP_NEWEST_RUNTIMES + 2 }, (_, i) => makeRuntime(root, i + 1));
		const result = pruneRuntimeDirs(root);
		expect(result.removed.sort()).toEqual([runtimes[0].dir, runtimes[1].dir].sort());
		expect(existsSync(runtimes[0].dir)).toBe(false);
		expect(existsSync(runtimes[2].dir)).toBe(true);
	});

	it("never removes the active directory even when it is old", () => {
		const root = scratchRoot();
		const runtimes = Array.from({ length: 4 }, (_, i) => makeRuntime(root, i + 1));
		pruneRuntimeDirs(root, { activeDir: runtimes[0].dir, keep: 1 });
		expect(existsSync(runtimes[0].dir)).toBe(true);
		expect(existsSync(runtimes[1].dir)).toBe(false);
	});

	it("skips an in-use directory and leaves its files untouched", () => {
		const root = scratchRoot();
		const locked = makeRuntime(root, 1);
		const free = makeRuntime(root, 2);
		makeRuntime(root, 3);
		const result = pruneRuntimeDirs(root, { keep: 1, inUse: (dir) => dir === locked.dir });
		expect(result.skipped).toEqual([locked.dir]);
		expect(result.removed).toEqual([free.dir]);
		expect(existsSync(locked.file)).toBe(true);
	});

	it("skips a directory whose removal fails and retries it next time", () => {
		const root = scratchRoot();
		const old = makeRuntime(root, 1);
		makeRuntime(root, 2);
		const result = pruneRuntimeDirs(root, {
			keep: 1,
			remove: () => {
				throw new Error("EBUSY");
			},
		});
		expect(result.skipped).toEqual([old.dir]);
		expect(pruneRuntimeDirs(root, { keep: 1 }).removed).toEqual([old.dir]);
	});
});

// Real Windows behaviour: a running node.exe (what a chat session is) cannot be
// deleted or renamed, nor can the directory that contains it.
describe.skipIf(process.platform !== "win32")("with a runtime executing from the old directory (Windows)", () => {
	it("builds a new runtime beside a locked one and prunes it only after the process ends", async () => {
		const root = scratchRoot();
		const old = makeRuntime(root, 1);
		copyFileSync(process.execPath, old.file);
		writeCurrentRuntimeDir(root, old.dir);
		const child = spawn(old.file, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore", windowsHide: true });
		const exited = new Promise((resolve) => child.once("exit", resolve));
		try {
			await new Promise((resolve) => setTimeout(resolve, 500));
			const fresh = makeRuntime(root, 2);
			writeCurrentRuntimeDir(root, fresh.dir);
			expect(readCurrentRuntimeDir(root)).toBe(fresh.dir);

			const first = pruneRuntimeDirs(root, { keep: 0, activeDir: fresh.dir });
			expect(runtimeInUse(old.dir)).toBe(true);
			expect(first.skipped).toEqual([old.dir]);
			expect(existsSync(old.file)).toBe(true);

			child.kill();
			await exited;
			const second = pruneRuntimeDirs(root, { keep: 0, activeDir: fresh.dir });
			expect(second.removed).toEqual([old.dir]);
			expect(existsSync(fresh.file)).toBe(true);
		} finally {
			child.kill();
		}
	}, 30_000);
});

function mkdirAndRename(from, to) {
	return renameSync(from, to);
}
