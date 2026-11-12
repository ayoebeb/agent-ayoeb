import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CommandRunStreamEvent } from "../lib/command-runs";

type Emit = (event: CommandRunStreamEvent) => void;
const daemon = vi.hoisted(() => ({
	runs: [] as Array<{ emit: Emit; finish: () => void; fail: (error: unknown) => void; signal: AbortSignal; request: unknown }>,
	stop: vi.fn(async () => undefined),
}));
vi.mock("../lib/command-runs", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/command-runs")>()),
	streamCommandRun: (_target: unknown, request: unknown, signal: AbortSignal, onEvent: Emit) =>
		new Promise<void>((resolve, reject) => {
			signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
			daemon.runs.push({ emit: onEvent, finish: resolve, fail: reject, signal, request });
		}),
	stopCommandRun: daemon.stop,
}));

import { CommandRunUnavailableError } from "../lib/command-runs";
import { MAX_STORED_OUTPUT, useCommandRunStore } from "./command-run-store";

const target = { sessionId: "s1" };
const run = (key = "block") => useCommandRunStore.getState().runs[key];
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
	daemon.runs = [];
	daemon.stop.mockClear();
	useCommandRunStore.setState({ runs: {}, shells: {} });
});

describe("command run store", () => {
	it("streams output and records the exit code", async () => {
		useCommandRunStore.getState().start("block", target, "Get-Date", "pwsh");
		expect(run()).toMatchObject({ status: "starting", command: "Get-Date", shell: "pwsh", output: "" });
		const live = daemon.runs[0];
		expect(live.request).toEqual({ command: "Get-Date", shell: "pwsh" });
		live.emit({ started: { runId: "cmdrun-1", shell: "PowerShell", shellPath: "pwsh.exe", workingDir: "C:\\repo", startedAt: "2026-11-12T10:00:00Z" } });
		expect(run()).toMatchObject({ status: "running", runId: "cmdrun-1", shellName: "PowerShell", workingDir: "C:\\repo" });
		live.emit({ output: "Wednesday\n" });
		live.emit({ output: "done\n" });
		live.emit({ exit: { exitCode: 3, durationMs: 120 } });
		live.finish();
		await flush();
		expect(run()).toMatchObject({ status: "failed", exitCode: 3, durationMs: 120, output: "Wednesday\ndone\n" });
	});

	it("does not start a second run of a block that is still running", () => {
		const { start } = useCommandRunStore.getState();
		start("block", target, "npm test", "auto");
		start("block", target, "npm test", "auto");
		expect(daemon.runs).toHaveLength(1);
	});

	it("stops through the daemon and shows the stopped exit", async () => {
		const { start, stop } = useCommandRunStore.getState();
		start("block", target, "npm run dev", "auto");
		const live = daemon.runs[0];
		live.emit({ started: { runId: "cmdrun-9", shell: "bash", shellPath: "/bin/bash", workingDir: "/repo", startedAt: "2026-11-12T10:00:00Z" } });
		await stop("block");
		expect(daemon.stop).toHaveBeenCalledWith(target, "cmdrun-9");
		expect(run()?.stopping).toBe(true);
		live.emit({ exit: { exitCode: -1, durationMs: 900, stopped: true } });
		live.finish();
		await flush();
		expect(run()).toMatchObject({ status: "stopped", stopping: false });
	});

	it("closes the stream when the daemon cannot stop the run", async () => {
		daemon.stop.mockRejectedValueOnce(new Error("offline"));
		const { start, stop } = useCommandRunStore.getState();
		start("block", target, "sleep 100", "auto");
		daemon.runs[0].emit({ started: { runId: "cmdrun-2", shell: "bash", shellPath: "/bin/bash", workingDir: "/repo", startedAt: "2026-11-12T10:00:00Z" } });
		await stop("block");
		expect(daemon.runs[0].signal.aborted).toBe(true);
		await flush();
		expect(run()?.status).toBe("stopped");
	});

	it("shows why a run could not start", async () => {
		useCommandRunStore.getState().start("block", target, "echo", "auto");
		daemon.runs[0].fail({ code: "COMMAND_RUN_USER_CLIENT_REQUIRED", message: "Commands can only be run from the Orchestrator app" });
		await flush();
		expect(run()).toMatchObject({ status: "error", error: "Commands can only be run from the Orchestrator app" });

		useCommandRunStore.getState().start("other", target, "echo", "auto");
		daemon.runs[1].fail(new CommandRunUnavailableError());
		await flush();
		expect(run("other")).toMatchObject({ status: "error", desktopOnly: true });
	});

	it("reports a stream that ended without an exit as a lost connection", async () => {
		useCommandRunStore.getState().start("block", target, "echo", "auto");
		daemon.runs[0].emit({ started: { runId: "cmdrun-3", shell: "bash", shellPath: "/bin/bash", workingDir: "/repo", startedAt: "2026-11-12T10:00:00Z" } });
		daemon.runs[0].finish();
		await flush();
		expect(run()).toMatchObject({ status: "error", error: "connection" });
	});

	it("caps stored output", async () => {
		useCommandRunStore.getState().start("block", target, "yes", "auto");
		const live = daemon.runs[0];
		live.emit({ output: "x".repeat(MAX_STORED_OUTPUT - 2) });
		live.emit({ output: "yyyy" });
		live.emit({ output: "zzzz" });
		expect(run()?.output).toHaveLength(MAX_STORED_OUTPUT);
		expect(run()?.output.endsWith("yy")).toBe(true);
		expect(run()?.truncated).toBe(true);
		live.emit({ exit: { exitCode: 0, durationMs: 5, truncated: true, droppedBytes: 10 } });
		live.finish();
		await flush();
		expect(run()).toMatchObject({ status: "succeeded", truncated: true });
	});

	it("dismissing a running block stops it and forgets its output", async () => {
		const { start, dismiss } = useCommandRunStore.getState();
		start("block", target, "sleep 100", "auto");
		dismiss("block");
		expect(daemon.runs[0].signal.aborted).toBe(true);
		await flush();
		expect(run()).toBeUndefined();
	});
});
