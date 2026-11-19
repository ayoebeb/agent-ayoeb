import { spawn as nodeSpawn, type SpawnOptions } from "node:child_process";
import { lstatSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { HIDDEN_DAILY_UPDATE, type DailyUpdateState } from "../shared/daily-update";

/** What the watcher writes (see Invoke-UpdatePass); the last three are optional so an older watcher still works. */
type PendingFile = { target?: unknown; commits?: unknown; scriptPath?: unknown; pwsh?: unknown; configFile?: unknown };

/** Unchecked: these come from a file anyone with write access could change. */
type Pending = { target: string; changes: number; scriptPath: unknown; pwsh: unknown; configFile: unknown };

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
	/** True for an existing regular file. */
	isFile?: (file: string) => boolean;
	env?: NodeJS.ProcessEnv;
};

export const PENDING_FILE = "pending-restart.json";
/** The watcher skips its Windows toast while this is fresh and names the pending target. */
export const NOTICE_SHOWN_FILE = "update-notice-shown.json";
const HEARTBEAT_EVERY_MS = 15_000;

const win = path.win32;
const lower = (value: string) => value.toLowerCase();

/** The daily update is a Windows flow, so every path is judged as a Windows path, case-insensitively. */
function insideDir(file: string, dir: string): boolean {
	const relative = win.relative(lower(win.resolve(dir)), lower(win.resolve(file)));
	return relative !== "" && !relative.startsWith("..") && !win.isAbsolute(relative);
}

/** A plain absolute path: no option-looking value, no control characters, no quotes. */
function plainAbsolutePath(value: unknown): value is string {
	return typeof value === "string" && win.isAbsolute(value) && !/[\x00-\x1f"]/.test(value) && !value.startsWith("-");
}

/** Where a PowerShell may live: the AO tools folder or a known system install. */
function pwshRoots(dailyDir: string, env: NodeJS.ProcessEnv): string[] {
	const roots = [win.join(win.dirname(win.resolve(dailyDir)), "tools")];
	for (const key of ["ProgramFiles", "ProgramFiles(x86)"]) if (env[key]) roots.push(win.join(env[key] as string, "PowerShell"));
	if (env.SystemRoot) roots.push(win.join(env.SystemRoot, "System32", "WindowsPowerShell"));
	// The Microsoft Store pwsh is a launcher alias here.
	if (env.LOCALAPPDATA) roots.push(win.join(env.LOCALAPPDATA, "Microsoft", "WindowsApps"));
	return roots;
}

function parsePending(text: string, dailyDir: string): Pending | undefined {
	let raw: PendingFile;
	try {
		raw = JSON.parse(text) as PendingFile;
	} catch {
		return undefined;
	}
	if (typeof raw.target !== "string" || !raw.target) return undefined;
	const commits = Array.isArray(raw.commits) ? raw.commits.length : 0;
	return {
		target: raw.target,
		changes: Math.max(1, commits),
		scriptPath: raw.scriptPath ?? win.join(dailyDir, "scripts", "update-daily-ao.ps1"),
		pwsh: raw.pwsh,
		configFile: raw.configFile,
	};
}

type Checked = { ok: true; command: string; args: string[] } | { ok: false; message: string };

/** Everything that will be spawned must be an existing file in an expected place. Refuse, never repair. */
function check(pending: Pending, dailyDir: string, env: NodeJS.ProcessEnv, isFile: (file: string) => boolean): Checked {
	const { scriptPath, pwsh, configFile } = pending;
	if (!plainAbsolutePath(scriptPath) || !insideDir(scriptPath, dailyDir) || !isFile(scriptPath)) {
		return { ok: false, message: "The update script is not in the daily folder." };
	}
	const pwshName = typeof pwsh === "string" ? lower(win.basename(pwsh)) : "";
	if (
		!plainAbsolutePath(pwsh) ||
		(pwshName !== "pwsh.exe" && pwshName !== "powershell.exe") ||
		!pwshRoots(dailyDir, env).some((root) => insideDir(pwsh, root)) ||
		!isFile(pwsh)
	) {
		return { ok: false, message: "PowerShell was not found in an expected place." };
	}
	const args = ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath, "-Yes"];
	// Older watchers wrote "configFile": null when no config file is used: that means absent.
	if (configFile !== undefined && configFile !== null && configFile !== "") {
		if (!plainAbsolutePath(configFile) || !insideDir(configFile, dailyDir) || !isFile(configFile)) {
			return { ok: false, message: "The update config is not in the daily folder." };
		}
		args.push("-ConfigFile", configFile);
	}
	return { ok: true, command: pwsh, args };
}

export function createDailyUpdateController(deps: DailyUpdateDeps) {
	const dailyDir = deps.dailyDir?.trim() || undefined;
	const platform = deps.platform ?? process.platform;
	const readFile = deps.readFile ?? ((file: string) => readFileSync(file, "utf8"));
	const writeFile = deps.writeFile ?? ((file: string, text: string) => writeFileSync(file, text, "utf8"));
	const spawn: SpawnLike = deps.spawn ?? (nodeSpawn as SpawnLike);
	const now = deps.now ?? Date.now;
	const env = deps.env ?? process.env;
	const isFile =
		deps.isFile ??
		((file: string) => {
			try {
				return statSync(file).isFile();
			} catch (error) {
				// A Store App Execution Alias (a reparse point) cannot be stat'ed (EACCES/EPERM/UNKNOWN) but is listed by lstat.
				const code = (error as NodeJS.ErrnoException).code;
				if (code !== "EACCES" && code !== "EPERM" && code !== "UNKNOWN") return false;
				try {
					const link = lstatSync(file);
					return link.isFile() || link.isSymbolicLink();
				} catch {
					return false;
				}
			}
		});

	// The script's own outcome, for this app run. The file alone cannot say
	// "restarting" or "failed": it just stays until the update succeeds.
	let run: { phase: "restarting" | "failed"; target: string; message?: string } | undefined;
	let lastHeartbeat = 0;

	const read = (): Pending | undefined => {
		if (!dailyDir || platform !== "win32") return undefined;
		try {
			return parsePending(readFile(win.join(dailyDir, PENDING_FILE)), dailyDir);
		} catch {
			return undefined;
		}
	};

	const beat = (pending: Pending) => {
		if (!dailyDir || now() - lastHeartbeat < HEARTBEAT_EVERY_MS) return;
		lastHeartbeat = now();
		try {
			writeFile(win.join(dailyDir, NOTICE_SHOWN_FILE), JSON.stringify({ target: pending.target, at: new Date(now()).toISOString() }));
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
		const checked = check(pending, dailyDir, env, isFile);
		if (!checked.ok) {
			run = { phase: "failed", target: pending.target, message: checked.message };
			return getState();
		}
		const target = pending.target;
		run = { phase: "restarting", target };
		try {
			// Detached: the script stops this Electron and starts a new one; it must outlive us.
			const child = spawn(checked.command, checked.args, { detached: true, stdio: "ignore", windowsHide: true });
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
