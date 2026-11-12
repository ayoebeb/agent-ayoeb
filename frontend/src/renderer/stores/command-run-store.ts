/**
 * Chat command runs, keyed by the code block that started them.
 *
 * Runs live here rather than in the code block so a block that scrolls out of
 * the chat's history window, or a chat the user switches away from, keeps its
 * run and output. They are local UI state only: never persisted (a reload
 * starts clean and never re-runs anything) and never sent to the agent.
 */

import { create } from "zustand";
import { apiErrorMessage } from "../lib/api-client";
import {
	CommandRunUnavailableError,
	stopCommandRun,
	streamCommandRun,
	type CommandRunTarget,
} from "../lib/command-runs";

/** Matches the daemon's own cap; a run's output past it is dropped there. */
export const MAX_STORED_OUTPUT = 1 << 20;

export type CommandRunStatus = "starting" | "running" | "succeeded" | "failed" | "stopped" | "timedOut" | "error";

export type CommandRun = {
	target: CommandRunTarget;
	command: string;
	shell: string;
	status: CommandRunStatus;
	output: string;
	truncated: boolean;
	startedAt: number;
	runId?: string;
	shellName?: string;
	workingDir?: string;
	exitCode?: number;
	durationMs?: number;
	/** Why the run could not start or lost its connection. */
	error?: string;
	/** The error is that local Run needs the desktop app window. */
	desktopOnly?: boolean;
	stopping?: boolean;
	collapsed?: boolean;
};

type CommandRunStore = {
	runs: Record<string, CommandRun>;
	/** The shell the reader picked for a block, overriding its language's default. */
	shells: Record<string, string>;
	start: (key: string, target: CommandRunTarget, command: string, shell: string) => void;
	stop: (key: string) => Promise<void>;
	dismiss: (key: string) => void;
	toggle: (key: string) => void;
	setShell: (key: string, shell: string) => void;
};

const controllers = new Map<string, AbortController>();

export function isCommandRunActive(run: CommandRun | undefined): boolean {
	return run?.status === "starting" || run?.status === "running";
}

export const useCommandRunStore = create<CommandRunStore>((set, get) => {
	const update = (key: string, controller: AbortController, patch: (run: CommandRun) => Partial<CommandRun>) => {
		// A later run of the same block replaced this one: drop late events.
		if (controllers.get(key) !== controller) return;
		set((state) => {
			const run = state.runs[key];
			return run ? { runs: { ...state.runs, [key]: { ...run, ...patch(run) } } } : state;
		});
	};

	return {
		runs: {},
		shells: {},
		start: (key, target, command, shell) => {
			if (isCommandRunActive(get().runs[key])) return;
			const controller = new AbortController();
			controllers.set(key, controller);
			set((state) => ({
				runs: {
					...state.runs,
					[key]: { target, command, shell, status: "starting", output: "", truncated: false, startedAt: Date.now() },
				},
			}));
			void (async () => {
				let ended = false;
				try {
					await streamCommandRun(target, { command, shell }, controller.signal, (event) => {
						if (event.started) {
							const started = event.started;
							update(key, controller, () => ({
								status: "running",
								runId: started.runId,
								shellName: started.shell,
								workingDir: started.workingDir,
							}));
						} else if (typeof event.output === "string") {
							const text = event.output;
							update(key, controller, (run) => {
								const room = MAX_STORED_OUTPUT - run.output.length;
								if (room <= 0) return { truncated: true };
								return text.length > room
									? { output: run.output + text.slice(0, room), truncated: true }
									: { output: run.output + text };
							});
						} else if (event.exit) {
							ended = true;
							const exit = event.exit;
							update(key, controller, (run) => ({
								status: exit.stopped ? "stopped" : exit.timedOut ? "timedOut" : exit.exitCode === 0 ? "succeeded" : "failed",
								exitCode: exit.exitCode,
								durationMs: exit.durationMs,
								truncated: run.truncated || Boolean(exit.truncated),
								stopping: false,
							}));
						} else if (event.error) {
							ended = true;
							const message = apiErrorMessage(event.error);
							update(key, controller, () => ({ status: "error", error: message, stopping: false }));
						}
					});
					if (!ended) {
						update(key, controller, (run) => ({
							status: run.stopping ? "stopped" : "error",
							error: run.stopping ? undefined : "connection",
							durationMs: Date.now() - run.startedAt,
							stopping: false,
						}));
					}
				} catch (error) {
					if (controller.signal.aborted) {
						update(key, controller, (run) => ({ status: "stopped", durationMs: Date.now() - run.startedAt, stopping: false }));
						return;
					}
					const desktopOnly = error instanceof CommandRunUnavailableError;
					update(key, controller, () => ({
						status: "error",
						error: desktopOnly ? undefined : apiErrorMessage(error),
						desktopOnly,
						stopping: false,
					}));
				} finally {
					if (controllers.get(key) === controller) controllers.delete(key);
				}
			})();
		},
		stop: async (key) => {
			const run = get().runs[key];
			if (!run || !isCommandRunActive(run)) return;
			const controller = controllers.get(key);
			if (!controller) return;
			update(key, controller, () => ({ stopping: true }));
			// Ask the daemon to stop it, so the stream ends with the real exit.
			// Before the run has an id, or when that request fails, closing the
			// stream stops it too (the daemon stops a command its client left).
			if (!run.runId) {
				controller.abort();
				return;
			}
			try {
				await stopCommandRun(run.target, run.runId);
			} catch {
				controller.abort();
			}
		},
		dismiss: (key) => {
			const controller = controllers.get(key);
			if (controller) {
				controllers.delete(key);
				controller.abort();
			}
			set((state) => {
				const { [key]: _removed, ...runs } = state.runs;
				return { runs };
			});
		},
		toggle: (key) => {
			set((state) => {
				const run = state.runs[key];
				return run ? { runs: { ...state.runs, [key]: { ...run, collapsed: !run.collapsed } } } : state;
			});
		},
		setShell: (key, shell) => set((state) => ({ shells: { ...state.shells, [key]: shell } })),
	};
});
