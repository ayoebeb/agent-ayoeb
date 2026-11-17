import { spawn as nodeSpawn, type SpawnOptions } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { HIDDEN_DAILY_UPDATE, type DailyUpdateState } from "../shared/daily-update";

/** What the watcher writes (see Invoke-UpdatePass); the last three are optional so an older watcher still works. */
type PendingFile = { target?: unknown; commits?: unknown; scriptPath?: unknown; pwsh?: unknown; configFile?: unknown };

type Pending = { target: string; changes: number; scriptPath: string; pwsh: string; configFile?: string };

type ChildLike = {
	// eslint-disable-next-line @typescript-eslint/no-explicit-any -- 'error' and 'exit' carry different arguments.
	on(event: "error" | "exit", listener: (arg: any) => void): unknown;
	unref(): void;
};
type SpawnLike = (command: string, args: string[], options: SpawnOptions) => ChildLike;

export type DailyUpdateDeps = {
	/** AO_DAILY_DIR: where the watcher keeps pending-restart.json. Unset means this is not a daily run. */
	dailyDir: string | undefined;
	platform?: NodeJS.Platform;
	readFile?: (file: string) => string;
	writeFile?: (file: string, text: string) => void;
	spawn?: SpawnLike;
	now?: () => number;
};

export const PENDING_FILE = "pending-restart.json";
/** The watcher skips its Windows toast while this is fresh and names the pending target. */
export const NOTICE_SHOWN_FILE = "update-notice-shown.json";
const HEARTBEAT_EVERY_MS = 15_000;

function parsePending(text: string, dailyDir: string): Pending | undefined {
	let raw: PendingFile;
	try {
		raw = JSON.parse(text) as PendingFile;
	} catch {
		return undefined;
	}
	if (typeof raw.target !== "string" || !raw.target) return undefined;
	const commits = Array.isArray(raw.commits) ? raw.commits.length : 0;
	const fallbackScript = path.join(dailyDir, "scripts", "update-daily-ao.ps1");
	return {
		target: raw.target,
		changes: Math.max(1, commits),
		scriptPath: typeof raw.scriptPath === "string" && raw.scriptPath ? raw.scriptPath : fallbackScript,
		pwsh: typeof raw.pwsh === "string" && raw.pwsh ? raw.pwsh : "pwsh",
		...(typeof raw.configFile === "string" && raw.configFile ? { configFile: raw.configFile } : {}),
	};
}

/** The script is only ever run from inside the daily dir, whatever the file says. */
function insideDir(file: string, dir: string): boolean {
	const relative = path.relative(path.resolve(dir), path.resolve(file));
	return relative !== "" && !relative.startsWith("..") && !path.isAbsolute(relative);
}

export function createDailyUpdateController(deps: DailyUpdateDeps) {
	const dailyDir = deps.dailyDir?.trim() || undefined;
	const platform = deps.platform ?? process.platform;
	const readFile = deps.readFile ?? ((file: string) => readFileSync(file, "utf8"));
	const writeFile = deps.writeFile ?? ((file: string, text: string) => writeFileSync(file, text, "utf8"));
	const spawn: SpawnLike = deps.spawn ?? (nodeSpawn as SpawnLike);
	const now = deps.now ?? Date.now;

	// The script's own outcome, for this app run. The file alone cannot say
	// "restarting" or "failed": it just stays until the update succeeds.
	let run: { phase: "restarting" | "failed"; target: string; message?: string } | undefined;
	let lastHeartbeat = 0;

	const read = (): Pending | undefined => {
		if (!dailyDir || platform !== "win32") return undefined;
		try {
			return parsePending(readFile(path.join(dailyDir, PENDING_FILE)), dailyDir);
		} catch {
			return undefined;
		}
	};

	const beat = (pending: Pending) => {
		if (!dailyDir || now() - lastHeartbeat < HEARTBEAT_EVERY_MS) return;
		lastHeartbeat = now();
		try {
			writeFile(path.join(dailyDir, NOTICE_SHOWN_FILE), JSON.stringify({ target: pending.target, at: new Date(now()).toISOString() }));
		} catch {
			// The watcher then falls back to its toast; the notice itself still works.
		}
	};

	const getState = (): DailyUpdateState => {
		const pending = read();
		if (!pending) {
			run = undefined;
			return HIDDEN_DAILY_UPDATE;
		}
		// A newer pending target replaces an old failure: it is a different update.
		if (run && run.target !== pending.target) run = undefined;
		const base = { changes: pending.changes, target: pending.target };
		if (run?.phase === "restarting") return { state: "restarting", ...base };
		beat(pending);
		if (run?.phase === "failed") return { state: "failed", ...base, message: run.message ?? "" };
		return { state: "ready", ...base };
	};

	/** The user's click. Runs the same update the script runs by hand: update-daily-ao.ps1 -Yes. */
	const apply = (): DailyUpdateState => {
		const pending = read();
		if (!dailyDir || !pending || run?.phase === "restarting") return getState();
		if (!insideDir(pending.scriptPath, dailyDir)) {
			run = { phase: "failed", target: pending.target, message: "The update script is not in the daily folder." };
			return getState();
		}
		const args = ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", pending.scriptPath, "-Yes"];
		if (pending.configFile) args.push("-ConfigFile", pending.configFile);
		const target = pending.target;
		run = { phase: "restarting", target };
		try {
			// Detached: the script stops this Electron and starts a new one; it must outlive us.
			const child = spawn(pending.pwsh, args, { detached: true, stdio: "ignore", windowsHide: true });
			child.on("error", (error: Error) => {
				run = { phase: "failed", target, message: error.message };
			});
			child.on("exit", (code: number | null) => {
				// Success removes the pending file, which hides the notice.
				if (code !== 0) run = { phase: "failed", target, message: `The update stopped (exit code ${code ?? "?"}).` };
				else run = undefined;
			});
			child.unref();
		} catch (error) {
			run = { phase: "failed", target, message: error instanceof Error ? error.message : String(error) };
		}
		return getState();
	};

	return { getState, apply };
}

export type DailyUpdateController = ReturnType<typeof createDailyUpdateController>;
