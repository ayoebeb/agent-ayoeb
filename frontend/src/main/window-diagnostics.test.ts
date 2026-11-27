// @vitest-environment node
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DIAGNOSTICS_LOG_NAME, createDiagnosticsLog, diagnosticsLogDir } from "./window-diagnostics";

let dir: string;
beforeEach(async () => {
	dir = await mkdtemp(path.join(os.tmpdir(), "ao-window-diagnostics-"));
});
afterEach(async () => {
	await rm(dir, { recursive: true, force: true });
});

describe("window diagnostics log", () => {
	it("prefers the daily logs folder over the data dir", () => {
		expect(diagnosticsLogDir({ AO_DAILY_DIR: "/daily" }, "/data")).toBe(path.join("/daily", "logs"));
		expect(diagnosticsLogDir({}, "/data")).toBe(path.join("/data", "logs"));
	});

	it("writes timestamped lines and keeps one rotated file", async () => {
		const log = createDiagnosticsLog({ dir: path.join(dir, "logs"), maxBytes: 120, now: () => new Date("2026-01-02T03:04:05Z") });
		log("render-process-gone", { reason: "crashed", exitCode: 5 });
		log("reload", { cause: "render-process-gone" });
		log("did-finish-load");
		const file = path.join(dir, "logs", DIAGNOSTICS_LOG_NAME);
		expect(await readFile(`${file}.1`, "utf8")).toContain('2026-01-02T03:04:05.000Z render-process-gone {"reason":"crashed","exitCode":5}\n');
		expect(await readFile(file, "utf8")).toContain("did-finish-load\n");
	});

	it("never throws when the log cannot be written", async () => {
		const log = createDiagnosticsLog({ dir: path.join(dir, "bad\0dir") });
		expect(() => log("reload")).not.toThrow();
	});
});
