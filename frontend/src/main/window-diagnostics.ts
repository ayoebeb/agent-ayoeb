import { appendFileSync, mkdirSync, renameSync, statSync } from "node:fs";
import path from "node:path";

/** Why the window went blank or reloaded: one small, rotated, local-only file. */
export const DIAGNOSTICS_LOG_NAME = "window-diagnostics.log";

const MAX_LOG_BYTES = 512 * 1024;
const MAX_FIELD_CHARS = 2_000;

export type DiagnosticsLog = (event: string, fields?: Record<string, unknown>) => void;

/** The daily build keeps its logs together; every other run uses the data dir. */
export function diagnosticsLogDir(env: NodeJS.ProcessEnv, dataDir: string): string {
	return path.join(env.AO_DAILY_DIR?.trim() || dataDir, "logs");
}

/**
 * Appends `<ISO time> <event> <json fields>` lines and keeps one previous file
 * (`.1`). Writes are synchronous so the line survives a process that is about to
 * die, and never throw: diagnostics must not become a second failure.
 */
export function createDiagnosticsLog(options: { dir: string; maxBytes?: number; now?: () => Date }): DiagnosticsLog {
	const file = path.join(options.dir, DIAGNOSTICS_LOG_NAME);
	const maxBytes = options.maxBytes ?? MAX_LOG_BYTES;
	const now = options.now ?? (() => new Date());
	return (event, fields) => {
		try {
			const detail = fields
				? ` ${JSON.stringify(fields, (_key, value: unknown) => (typeof value === "string" ? value.slice(0, MAX_FIELD_CHARS) : value))}`
				: "";
			const line = `${now().toISOString()} ${event}${detail}\n`;
			mkdirSync(options.dir, { recursive: true });
			let size = 0;
			try {
				size = statSync(file).size;
			} catch {
				// No log yet.
			}
			if (size > 0 && size + Buffer.byteLength(line) > maxBytes) renameSync(file, `${file}.1`);
			appendFileSync(file, line, { mode: 0o600 });
		} catch (error) {
			console.warn("AO: could not write window diagnostics:", error);
		}
	};
}
