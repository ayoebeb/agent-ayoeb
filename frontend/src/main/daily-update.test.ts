import path from "node:path";
import { describe, expect, it, vi } from "vitest";
import { createDailyUpdateController, NOTICE_SHOWN_FILE, PENDING_FILE } from "./daily-update";

const dailyDir = path.join("C:", "ao", "daily");
const pendingPath = path.join(dailyDir, PENDING_FILE);
const shownPath = path.join(dailyDir, NOTICE_SHOWN_FILE);
const pending = (extra: Record<string, unknown> = {}) =>
	JSON.stringify({ target: "abc123", commits: ["a one", "b two", "c three"], ...extra });

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
		const script = path.join(dailyDir, "scripts", "update-daily-ao.ps1");
		const cfg = path.join(dailyDir, "config.json");
		const { controller, spawn, child } = setup({
			[pendingPath]: pending({ scriptPath: script, pwsh: "C:\\pwsh\\pwsh.exe", configFile: cfg }),
		});
		expect(controller.apply()).toEqual({ state: "restarting", changes: 3, target: "abc123" });
		expect(spawn).toHaveBeenCalledWith(
			"C:\\pwsh\\pwsh.exe",
			["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Yes", "-ConfigFile", cfg],
			{ detached: true, stdio: "ignore", windowsHide: true },
		);
		expect(child.unref).toHaveBeenCalled();
		// A second click while it runs does not start a second update.
		controller.apply();
		expect(spawn).toHaveBeenCalledTimes(1);
		expect(controller.getState().state).toBe("restarting");
	});

	it("falls back to the installed script when an older watcher wrote no paths", () => {
		const { controller, spawn } = setup({ [pendingPath]: pending() });
		controller.apply();
		const call = spawn.mock.calls[0] as unknown as [string, string[]];
		expect(call[0]).toBe("pwsh");
		expect(call[1]).toContain(path.join(dailyDir, "scripts", "update-daily-ao.ps1"));
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

	it("ignores a pwsh that is not PowerShell and a config outside the daily folder", () => {
		const { controller, spawn } = setup({ [pendingPath]: pending({ pwsh: "C:\\evil\\calc.exe", configFile: "C:\\evil\\cfg.json" }) });
		controller.apply();
		const call = spawn.mock.calls[0] as unknown as [string, string[]];
		expect(call[0]).toBe("pwsh");
		expect(call[1]).not.toContain("-ConfigFile");
	});

	it("keeps a PowerShell path and a config inside the daily folder", () => {
		const cfg = path.join(dailyDir, "config.json");
		const pwsh = "C:\\Program Files\\PowerShell\\7\\pwsh.exe";
		const { controller, spawn } = setup({ [pendingPath]: pending({ pwsh, configFile: cfg }) });
		controller.apply();
		const call = spawn.mock.calls[0] as unknown as [string, string[]];
		expect(call[0]).toBe(pwsh);
		expect(call[1]).toContain(cfg);
	});

	it("refuses a script outside the daily folder", () => {
		const { controller, spawn } = setup({ [pendingPath]: pending({ scriptPath: "C:\\evil\\x.ps1" }) });
		expect(controller.apply().state).toBe("failed");
		expect(spawn).not.toHaveBeenCalled();
	});

	it("goes hidden once the update succeeds and the script removes the pending file", () => {
		const { controller, child, files } = setup({ [pendingPath]: pending() });
		controller.apply();
		child.emit("exit", 0);
		delete files[pendingPath];
		expect(controller.getState()).toEqual({ state: "hidden" });
	});
});
