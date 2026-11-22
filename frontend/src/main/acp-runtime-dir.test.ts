import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { devAcpRuntimeDir } from "./acp-runtime-dir";

const dirs: string[] = [];
afterEach(() => {
	for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

function appPath() {
	const dir = mkdtempSync(path.join(tmpdir(), "ao-acp-dir-"));
	dirs.push(dir);
	return dir;
}

function writeManifest(app: string, content: string) {
	const root = path.join(app, "resources", "acp-runtime-dev");
	mkdirSync(root, { recursive: true });
	writeFileSync(path.join(root, "current.json"), content);
	return root;
}

describe("devAcpRuntimeDir", () => {
	it("uses the fixed directory off Windows even with a manifest", () => {
		const app = appPath();
		const root = writeManifest(app, "{}");
		writeManifest(app, JSON.stringify({ path: path.join(root, "rt-1-1") }));
		expect(devAcpRuntimeDir(app, "darwin")).toBe(path.join(app, "resources", "acp-runtime"));
	});

	it("uses the manifest's newest runtime on Windows", () => {
		const app = appPath();
		const root = path.join(app, "resources", "acp-runtime-dev");
		const newest = path.join(root, "rt-2-1");
		writeManifest(app, JSON.stringify({ path: newest }));
		expect(devAcpRuntimeDir(app, "win32")).toBe(newest);
	});

	it("falls back to the fixed directory without, or with a bad, manifest", () => {
		const app = appPath();
		const fixed = path.join(app, "resources", "acp-runtime");
		expect(devAcpRuntimeDir(app, "win32")).toBe(fixed);
		writeManifest(app, "{nope");
		expect(devAcpRuntimeDir(app, "win32")).toBe(fixed);
		writeManifest(app, JSON.stringify({ path: path.join(app, "elsewhere") }));
		expect(devAcpRuntimeDir(app, "win32")).toBe(fixed);
	});
});
