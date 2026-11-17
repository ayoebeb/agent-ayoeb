import { describe, expect, it, vi } from "vitest";
import { createDailyUpdateController, NOTICE_SHOWN_FILE, PENDING_FILE } from "./daily-update";

const dailyDir = "C:\\ao\\daily";
const pendingPath = `${dailyDir}\\${PENDING_FILE}`;
const shownPath = `${dailyDir}\\${NOTICE_SHOWN_FILE}`;
const script = `${dailyDir}\\scripts\\update-daily-ao.ps1`;
const cfg = `${dailyDir}\\config.json`;
const toolsPwsh = "C:\\ao\\tools\\pwsh\\pwsh.exe";
const env = { ProgramFiles: "C:\\Program Files", SystemRoot: "C:\\Windows", LOCALAPPDATA: "C:\\Users\\u\\AppData\\Local" };
const existing = new Set([script, cfg, toolsPwsh, "C:\\Program Files\\PowerShell\\7\\pwsh.exe", "C:\\Users\\u\\AppData\\Local\\Microsoft\\WindowsApps\\pwsh.exe"]);

/** A pending file as the watcher writes it, with the fields a test wants to tamper with. */
const pending = (extra: Record<string, unknown> = {}) =>
	JSON.stringify({ target: "abc123", commits: ["a one", "b two", "c three"], scriptPath: script, pwsh: toolsPwsh, ...extra });

function fakeChild() {
	const handlers: Record<string, (arg?: unknown) => void> = {};
	return {
		on: vi.fn((event: string, handler: (arg?: unknown) => void) => {
			handlers[event] = handler;
		}),
		unref: vi.fn(),
		emit: (event: string, arg?: unknown) => handlers[event]?.(arg),
	};
}

function setup(files: Record<string, string>, options: { platform?: NodeJS.Platform; dir?: string | undefined } = {}) {
	const child = fakeChild();
	const spawn = vi.fn(() => child);
	const writes: Record<string, string> = {};
	let clock = 1_000_000;
	const controller = createDailyUpdateController({
		dailyDir: "dir" in options ? options.dir : dailyDir,
		platform: options.platform ?? "win32",
		readFile: (file) => {
			if (!(file in files)) throw new Error("ENOENT");
			return files[file];
		},
		writeFile: (file, text) => {
			writes[file] = text;
		},
		spawn,
		now: () => clock,
		isFile: (file) => existing.has(file),
		env,
	});
	return { controller, child, spawn, writes, files, tick: (ms: number) => (clock += ms) };
}

describe("daily update notice state", () => {
	it("is hidden without AO_DAILY_DIR, off Windows, without a file, or with a broken file", () => {
		expect(setup({ [pendingPath]: pending() }, { dir: undefined }).controller.getState()).toEqual({ state: "hidden" });
		expect(setup({ [pendingPath]: pending() }, { platform: "linux" }).controller.getState()).toEqual({ state: "hidden" });
		expect(setup({}).controller.getState()).toEqual({ state: "hidden" });
		expect(setup({ [pendingPath]: "{nope" }).controller.getState()).toEqual({ state: "hidden" });
	});

	it("is ready with the change count, and tells the watcher the window shows it", () => {
		const { controller, writes, tick } = setup({ [pendingPath]: pending() });
		expect(controller.getState()).toEqual({ state: "ready", changes: 3, target: "abc123" });
		expect(JSON.parse(writes[shownPath]).target).toBe("abc123");
		// Refreshed on a slower beat than the poll, not on every read.
		const before = writes[shownPath];
		tick(1_000);
		controller.getState();
		expect(writes[shownPath]).toBe(before);
		tick(20_000);
		controller.getState();
		expect(writes[shownPath]).not.toBe(before);
	});

	it("never starts the update by itself", () => {
		const { controller, spawn } = setup({ [pendingPath]: pending() });
		controller.getState();
		controller.getState();
		expect(spawn).not.toHaveBeenCalled();
	});

	it("restarts only on apply, running the script from the pending file with -Yes", () => {
		const { controller, spawn, child } = setup({ [pendingPath]: pending({ configFile: cfg }) });
		expect(controller.apply()).toEqual({ state: "restarting", changes: 3, target: "abc123" });
		expect(spawn).toHaveBeenCalledWith(
			toolsPwsh,
			["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Yes", "-ConfigFile", cfg],
			{ detached: true, stdio: "ignore", windowsHide: true },
		);
		expect(child.unref).toHaveBeenCalled();
		// A second click while it runs does not start a second update.
		controller.apply();
		expect(spawn).toHaveBeenCalledTimes(1);
		expect(controller.getState().state).toBe("restarting");
	});

	it("uses the installed script when an older watcher wrote no script path", () => {
		const { controller, spawn } = setup({ [pendingPath]: pending({ scriptPath: undefined }) });
		controller.apply();
		const call = spawn.mock.calls[0] as unknown as [string, string[]];
		expect(call[1]).toContain(script);
	});

	it.each([
		["the AO tools folder", toolsPwsh],
		["Program Files", "C:\\Program Files\\PowerShell\\7\\pwsh.exe"],
		["the Store launcher folder", "C:\\Users\\u\\AppData\\Local\\Microsoft\\WindowsApps\\pwsh.exe"],
	])("accepts a PowerShell under %s", (_where, pwsh) => {
		const { controller, spawn } = setup({ [pendingPath]: pending({ pwsh }) });
		expect(controller.apply().state).toBe("restarting");
		expect(spawn.mock.calls[0]).toBeDefined();
	});

	it("is failed when the script exits with an error, and Retry runs it again", () => {
		const { controller, spawn, child } = setup({ [pendingPath]: pending() });
		controller.apply();
		child.emit("exit", 1);
		const failed = controller.getState();
		expect(failed).toMatchObject({ state: "failed", changes: 3 });
		expect(failed.state === "failed" && failed.message).toContain("exit code 1");
		controller.apply();
		expect(spawn).toHaveBeenCalledTimes(2);
		expect(controller.getState().state).toBe("restarting");
	});

	it("is failed when the script cannot start", () => {
		const { controller, child } = setup({ [pendingPath]: pending() });
		controller.apply();
		child.emit("error", new Error("spawn pwsh ENOENT"));
		expect(controller.getState()).toMatchObject({ state: "failed", message: "spawn pwsh ENOENT" });
	});

	it("goes hidden once the update succeeds and the script removes the pending file", () => {
		const { controller, child, files } = setup({ [pendingPath]: pending() });
		controller.apply();
		child.emit("exit", 0);
		delete files[pendingPath];
		expect(controller.getState()).toEqual({ state: "hidden" });
	});
});

describe("a tampered pending-restart.json", () => {
	// Each case must end in the existing failed state and must never spawn.
	it.each([
		["a script outside the daily folder", { scriptPath: "C:\\evil\\x.ps1" }, "update script"],
		["a script that climbs out with ..", { scriptPath: `${dailyDir}\\..\\evil\\x.ps1` }, "update script"],
		["a script that does not exist", { scriptPath: `${dailyDir}\\scripts\\gone.ps1` }, "update script"],
		["a relative script path", { scriptPath: "scripts\\update-daily-ao.ps1" }, "update script"],
		["a PowerShell outside the expected places", { pwsh: "C:\\evil\\pwsh.exe" }, "PowerShell"],
		["an executable that is not PowerShell", { pwsh: "C:\\Program Files\\PowerShell\\7\\calc.exe" }, "PowerShell"],
		["a PowerShell that does not exist", { pwsh: "C:\\Program Files\\PowerShell\\6\\pwsh.exe" }, "PowerShell"],
		["a bare command name", { pwsh: "pwsh" }, "PowerShell"],
		["no PowerShell path at all", { pwsh: undefined }, "PowerShell"],
		["a PowerShell path with an injected argument", { pwsh: `${toolsPwsh}" -Command calc` }, "PowerShell"],
		["a config outside the daily folder", { configFile: "C:\\evil\\cfg.json" }, "config"],
		["a config that does not exist", { configFile: `${dailyDir}\\missing.json` }, "config"],
		["a config that looks like an option", { configFile: "-Command calc" }, "config"],
		["a config that is not a string", { configFile: ["a"] }, "config"],
	])("refuses %s", (_name, extra, mentions) => {
		const { controller, spawn } = setup({ [pendingPath]: pending(extra) });
		const state = controller.apply();
		expect(state.state).toBe("failed");
		expect(state.state === "failed" && state.message).toContain(mentions);
		expect(spawn).not.toHaveBeenCalled();
	});

	it("still offers Retry after a refusal and recovers once the file is fixed", () => {
		const { controller, spawn, files } = setup({ [pendingPath]: pending({ pwsh: "C:\\evil\\pwsh.exe" }) });
		expect(controller.apply().state).toBe("failed");
		files[pendingPath] = pending({ target: "next999" });
		expect(controller.getState().state).toBe("ready");
		expect(controller.apply().state).toBe("restarting");
		expect(spawn).toHaveBeenCalledTimes(1);
	});
});
