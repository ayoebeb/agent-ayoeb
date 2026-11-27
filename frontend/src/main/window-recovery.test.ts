// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	BLANK_CHECK_DELAY_MS,
	CRASH_RELOAD_DELAY_MS,
	HEALTH_CHECK_INTERVAL_MS,
	UNRESPONSIVE_RELOAD_MS,
	createChildProcessMonitor,
	createReloadBudget,
	installWindowRecovery,
	type RecoverableContents,
} from "./window-recovery";

function setup(options: { url?: string; max?: number; repairShellView?: () => string | null } = {}) {
	const listeners = new Map<string, (...args: unknown[]) => void>();
	const state = { rendered: true, url: options.url ?? "http://localhost:5173/#/projects?token=secret" };
	const contents = {
		on: vi.fn((event: string, listener: (...args: unknown[]) => void) => {
			listeners.set(event, listener);
		}),
		executeJavaScript: vi.fn(async () => state.rendered),
		reload: vi.fn(),
		loadURL: vi.fn(async () => undefined),
		getURL: () => state.url,
		isDestroyed: () => false,
		isLoadingMainFrame: () => false,
		forcefullyCrashRenderer: vi.fn(),
	};
	const log = vi.fn();
	const dispose = installWindowRecovery({
		contents: contents as unknown as RecoverableContents,
		rendererUrl: () => "http://localhost:5173/",
		log,
		budget: createReloadBudget({ max: options.max }),
		repairShellView: options.repairShellView,
	});
	const emit = (event: string, ...args: unknown[]) => listeners.get(event)?.(...args);
	const events = () => log.mock.calls.map(([event]) => event as string);
	return { contents, state, log, emit, events, dispose };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("window recovery", () => {
	it("logs a renderer crash and reloads", async () => {
		const { contents, emit, log } = setup();
		emit("render-process-gone", {}, { reason: "crashed", exitCode: 5 });
		expect(log).toHaveBeenCalledWith("render-process-gone", { reason: "crashed", exitCode: 5 });
		await vi.advanceTimersByTimeAsync(CRASH_RELOAD_DELAY_MS);
		expect(contents.reload).toHaveBeenCalledTimes(1);
	});

	it("does not reload after a clean exit", async () => {
		const { contents, emit } = setup();
		emit("render-process-gone", {}, { reason: "clean-exit", exitCode: 0 });
		await vi.advanceTimersByTimeAsync(CRASH_RELOAD_DELAY_MS);
		expect(contents.reload).not.toHaveBeenCalled();
	});

	it("stops reloading after the hourly limit and starts again an hour later", async () => {
		const { contents, emit, log } = setup({ max: 2 });
		const crash = async () => {
			emit("render-process-gone", {}, { reason: "oom", exitCode: 1 });
			await vi.advanceTimersByTimeAsync(CRASH_RELOAD_DELAY_MS);
		};
		await crash();
		await crash();
		await crash();
		expect(contents.reload).toHaveBeenCalledTimes(2);
		expect(log).toHaveBeenCalledWith("reload-skipped", { cause: "render-process-gone", reason: "hourly-limit" });
		await vi.advanceTimersByTimeAsync(60 * 60_000);
		await crash();
		expect(contents.reload).toHaveBeenCalledTimes(3);
	});

	it("reloads a renderer that stays unresponsive, once", async () => {
		const { contents, emit } = setup();
		emit("unresponsive");
		await vi.advanceTimersByTimeAsync(UNRESPONSIVE_RELOAD_MS);
		expect(contents.forcefullyCrashRenderer).toHaveBeenCalledTimes(1);
		expect(contents.reload).toHaveBeenCalledTimes(1);
		// The crash this caused must not trigger a second reload.
		emit("render-process-gone", {}, { reason: "killed", exitCode: 1 });
		await vi.advanceTimersByTimeAsync(CRASH_RELOAD_DELAY_MS);
		expect(contents.reload).toHaveBeenCalledTimes(1);
	});

	it("leaves a renderer that recovers by itself alone", async () => {
		const { contents, emit, events } = setup();
		emit("unresponsive");
		await vi.advanceTimersByTimeAsync(UNRESPONSIVE_RELOAD_MS / 2);
		emit("responsive");
		await vi.advanceTimersByTimeAsync(UNRESPONSIVE_RELOAD_MS);
		expect(contents.reload).not.toHaveBeenCalled();
		expect(events()).toContain("responsive");
	});

	it("reloads a blank screen once after load and only logs that it stays blank", async () => {
		const { contents, state, emit, events, log } = setup();
		state.rendered = false;
		emit("did-finish-load");
		await vi.advanceTimersByTimeAsync(BLANK_CHECK_DELAY_MS - 1);
		expect(contents.executeJavaScript).not.toHaveBeenCalled();
		await vi.advanceTimersByTimeAsync(1);
		expect(contents.reload).toHaveBeenCalledTimes(1);
		// The query string stays out of the log.
		expect(log).toHaveBeenCalledWith("blank-screen", { trigger: "load", url: "http://localhost:5173/" });
		emit("did-finish-load");
		await vi.advanceTimersByTimeAsync(BLANK_CHECK_DELAY_MS + 2 * HEALTH_CHECK_INTERVAL_MS);
		expect(contents.reload).toHaveBeenCalledTimes(1);
		expect(events().filter((event) => event === "blank-screen-persists")).toHaveLength(1);
	});

	it("does nothing when the app rendered", async () => {
		const { contents, emit, events } = setup();
		emit("did-finish-load");
		await vi.advanceTimersByTimeAsync(BLANK_CHECK_DELAY_MS + HEALTH_CHECK_INTERVAL_MS);
		expect(contents.reload).not.toHaveBeenCalled();
		expect(events()).toEqual(["did-finish-load"]);
	});

	it("notices a screen that goes blank long after loading", async () => {
		const { contents, state, emit, log } = setup();
		emit("did-finish-load");
		await vi.advanceTimersByTimeAsync(BLANK_CHECK_DELAY_MS);
		state.rendered = false;
		await vi.advanceTimersByTimeAsync(HEALTH_CHECK_INTERVAL_MS);
		expect(log).toHaveBeenCalledWith("blank-screen", expect.objectContaining({ trigger: "periodic" }));
		expect(contents.reload).toHaveBeenCalledTimes(1);
	});

	it("loads the app URL again when the first load never committed", async () => {
		const { contents, state, emit, log } = setup({ url: "" });
		state.rendered = false;
		emit("did-fail-load", {}, -102, "ERR_CONNECTION_REFUSED", "http://localhost:5173/?a=1", true);
		expect(log).toHaveBeenCalledWith("did-fail-load", expect.objectContaining({ errorCode: -102, url: "http://localhost:5173/" }));
		await vi.advanceTimersByTimeAsync(BLANK_CHECK_DELAY_MS);
		expect(contents.loadURL).toHaveBeenCalledWith("http://localhost:5173/");
		expect(contents.reload).not.toHaveBeenCalled();
	});

	it("logs later full reloads but not in-page navigation", () => {
		const { emit, events } = setup();
		const navigate = (isSameDocument: boolean) => emit("did-start-navigation", { isMainFrame: true, isSameDocument, url: "http://localhost:5173/" });
		navigate(false);
		emit("did-finish-load");
		navigate(true);
		expect(events()).not.toContain("page-reload");
		navigate(false);
		expect(events()).toContain("page-reload");
	});

	it("logs console errors and Vite messages, capped per minute", () => {
		const { emit, log } = setup();
		const message = (level: string, text: string) => emit("console-message", { level, message: text, sourceId: "app.js?t=1", lineNumber: 3 });
		message("info", "just chatter");
		message("debug", "[vite] hot updated: /a.tsx");
		expect(log).not.toHaveBeenCalled();
		message("debug", "[vite] server connection lost. Polling for restart...");
		expect(log).toHaveBeenLastCalledWith("vite", expect.objectContaining({ source: "app.js" }));
		for (let i = 0; i < 30; i++) message("error", `boom ${i}`);
		expect(log).toHaveBeenCalledTimes(20);
		vi.advanceTimersByTime(60_000);
		message("error", "later");
		expect(log).toHaveBeenCalledWith("console-suppressed", { count: 11 });
	});

	it("logs a repaired shell view", async () => {
		const { log } = setup({ repairShellView: () => "shell 0x0, window 800x600" });
		await vi.advanceTimersByTimeAsync(HEALTH_CHECK_INTERVAL_MS);
		expect(log).toHaveBeenCalledWith("shell-view-repaired", { detail: "shell 0x0, window 800x600" });
	});

	it("stops all timers once disposed", async () => {
		const { contents, state, emit, dispose } = setup();
		state.rendered = false;
		emit("did-finish-load");
		emit("unresponsive");
		dispose();
		await vi.advanceTimersByTimeAsync(UNRESPONSIVE_RELOAD_MS + HEALTH_CHECK_INTERVAL_MS);
		expect(contents.executeJavaScript).not.toHaveBeenCalled();
		expect(contents.reload).not.toHaveBeenCalled();
	});
});

describe("child process monitor", () => {
	const details = (type: string, reason = "crashed") => ({ type, reason, exitCode: 1 }) as unknown as Electron.Details;

	it("offers the GPU switch once, after repeated GPU crashes within an hour", () => {
		let time = 0;
		const log = vi.fn();
		const onRepeatedGpuCrash = vi.fn();
		const monitor = createChildProcessMonitor({ log, onRepeatedGpuCrash, now: () => time });
		monitor(details("GPU"));
		time += 2 * 60 * 60_000;
		monitor(details("GPU"));
		monitor(details("GPU"));
		monitor(details("Utility"));
		expect(onRepeatedGpuCrash).not.toHaveBeenCalled();
		monitor(details("GPU"));
		monitor(details("GPU"));
		expect(onRepeatedGpuCrash).toHaveBeenCalledTimes(1);
		expect(log).toHaveBeenCalledWith("gpu-repeated-crash", { count: 3 });
	});

	it("ignores clean exits", () => {
		const log = vi.fn();
		createChildProcessMonitor({ log, onRepeatedGpuCrash: vi.fn() })(details("Utility", "clean-exit"));
		expect(log).not.toHaveBeenCalled();
	});
});
