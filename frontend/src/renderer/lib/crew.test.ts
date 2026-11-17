import { describe, expect, it } from "vitest";
import type { ConversationPlan } from "../types/conversation";
import type { WorkspaceSession } from "../types/workspace";
import {
	buildCrew,
	compactAge,
	crewDiffFromManifest,
	quietNudgeMessage,
	crewGroupOf,
	currentPlan,
	DONE_WINDOW_MS,
	fileOverlaps,
	planProgress,
	QUIET_AFTER_MS,
	quietForMs,
	scopedRow,
	type CrewDiff,
	type CrewRow,
} from "./crew";

const NOW = Date.parse("2026-11-16T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const MIN = 60_000;

function session(over: Partial<WorkspaceSession> & { id: string }): WorkspaceSession {
	return {
		workspaceId: "p",
		workspaceName: "p",
		title: over.id,
		provider: "claude-code",
		status: "working",
		kanbanColumn: "building",
		activity: { state: "active", lastActivityAt: ago(MIN) },
		createdAt: ago(60 * MIN),
		updatedAt: ago(MIN),
		prs: [],
		...over,
	} as WorkspaceSession;
}

const diff = (...paths: string[]): CrewDiff => ({
	files: paths.map((path) => ({ path, additions: 2, deletions: 1 })),
	additions: paths.length * 2,
	deletions: paths.length,
});

describe("crewGroupOf", () => {
	it("puts a worker that waits for input in Needs you", () => {
		expect(crewGroupOf(session({ id: "a", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } }))).toBe("needsYou");
	});

	it("puts a PR the person must act on (CI failing) in Needs you", () => {
		expect(crewGroupOf(session({ id: "a", status: "ci_failed", displayStatus: "CI failing", kanbanColumn: "validating" }))).toBe("needsYou");
	});

	it("puts a human-review column in Needs you, validating and ready in In review", () => {
		expect(crewGroupOf(session({ id: "a", status: "review_pending", displayStatus: "Needs review", kanbanColumn: "needs_review" }))).toBe("needsYou");
		expect(crewGroupOf(session({ id: "b", status: "pr_open", displayStatus: "Reviewing", kanbanColumn: "validating" }))).toBe("inReview");
		expect(crewGroupOf(session({ id: "c", status: "mergeable", displayStatus: "Mergeable", kanbanColumn: "ready" }))).toBe("inReview");
	});

	it("keeps a plain active worker in Working", () => {
		expect(crewGroupOf(session({ id: "a" }))).toBe("working");
	});

	it("puts merged and terminated sessions in Done", () => {
		expect(crewGroupOf(session({ id: "a", status: "merged", displayStatus: "Merged", kanbanColumn: "ready" }))).toBe("done");
		expect(crewGroupOf(session({ id: "b", isTerminated: true, status: "terminated" }))).toBe("done");
	});
});

describe("quietForMs", () => {
	it("reports a duration once an active agent has been silent past the threshold", () => {
		const quiet = session({ id: "a", lastEventAt: ago(9 * MIN), activity: { state: "active", lastActivityAt: ago(7 * MIN) } });
		expect(quietForMs(quiet, NOW)).toBe(7 * MIN);
	});

	it("uses the newest of the event time and the activity time", () => {
		const s = session({ id: "a", lastEventAt: ago(2 * MIN), activity: { state: "active", lastActivityAt: ago(30 * MIN) } });
		expect(quietForMs(s, NOW)).toBeUndefined();
	});

	it("is not quiet below the threshold, and the boundary counts as quiet", () => {
		expect(quietForMs(session({ id: "a", lastEventAt: ago(QUIET_AFTER_MS - 1) }), NOW)).toBeUndefined();
		expect(quietForMs(session({ id: "b", lastEventAt: ago(QUIET_AFTER_MS), activity: { state: "active", lastActivityAt: ago(QUIET_AFTER_MS) } }), NOW)).toBe(QUIET_AFTER_MS);
	});

	it("never marks an idle, waiting, exited or finished agent quiet", () => {
		for (const state of ["idle", "waiting_input", "blocked", "exited", "unknown"] as const) {
			expect(quietForMs(session({ id: state, activity: { state, lastActivityAt: ago(60 * MIN) }, lastEventAt: ago(60 * MIN) }), NOW)).toBeUndefined();
		}
		expect(quietForMs(session({ id: "t", isTerminated: true, status: "terminated", lastEventAt: ago(60 * MIN) }), NOW)).toBeUndefined();
	});

	it("says nothing when the daemon reported no time at all", () => {
		expect(quietForMs({ ...session({ id: "a" }), lastEventAt: undefined, activity: { state: "active", lastActivityAt: "" } }, NOW)).toBeUndefined();
	});
});

describe("fileOverlaps", () => {
	const row = (id: string, files: string[], over: Partial<WorkspaceSession> = {}): CrewRow => {
		const s = session({ id, ...over });
		return { session: s, group: crewGroupOf(s), diff: diff(...files), overlapPaths: [] };
	};

	it("lists a file two workers both changed, with the worker furthest along first", () => {
		const polish = row("polish", ["ChatWorkspace.tsx", "a.ts"], { status: "mergeable", kanbanColumn: "ready", displayStatus: "Mergeable" });
		const anchors = row("anchors", ["ChatWorkspace.tsx"]);
		expect(fileOverlaps([anchors, polish])).toEqual([{ path: "ChatWorkspace.tsx", sessionIds: ["polish", "anchors"] }]);
	});

	it("ignores files only one worker changed, and workers without a diff yet", () => {
		const a = row("a", ["x.ts"]);
		const b = row("b", ["y.ts"]);
		const c: CrewRow = { ...row("c", []), diff: undefined };
		expect(fileOverlaps([a, b, c])).toEqual([]);
	});

	it("does not count a finished session as competing for the file", () => {
		const done = row("done", ["x.ts"], { status: "merged", displayStatus: "Merged", kanbanColumn: "ready" });
		expect(fileOverlaps([done, row("live", ["x.ts"])])).toEqual([]);
	});

	it("breaks a tie by who started first", () => {
		const older = row("older", ["x.ts"], { createdAt: ago(90 * MIN) });
		const newer = row("newer", ["x.ts"], { createdAt: ago(10 * MIN) });
		expect(fileOverlaps([newer, older])[0]?.sessionIds).toEqual(["older", "newer"]);
	});
});

describe("buildCrew", () => {
	const orchestrator = session({ id: "p-orchestrator", kind: "orchestrator" });

	it("leaves the orchestrator out and groups the workers in order", () => {
		const model = buildCrew({
			now: NOW,
			diffs: new Map(),
			sessions: [
				orchestrator,
				session({ id: "w1" }),
				session({ id: "w2", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } }),
				session({ id: "w3", status: "pr_open", kanbanColumn: "validating", displayStatus: "Reviewing" }),
			],
		});
		expect(model.groups.map((group) => [group.id, group.rows.map((row) => row.session.id)])).toEqual([
			["needsYou", ["w2"]],
			["working", ["w1"]],
			["inReview", ["w3"]],
			["done", []],
		]);
		expect(model.needsYouCount).toBe(1);
	});

	it("sums the diff of live workers only and counts their worktrees", () => {
		const model = buildCrew({
			now: NOW,
			diffs: new Map([
				["w1", diff("a.ts", "b.ts")],
				["w2", diff("c.ts")],
				["gone", diff("z.ts", "y.ts", "x.ts")],
			]),
			sessions: [
				session({ id: "w1" }),
				session({ id: "w2" }),
				session({ id: "w3" }),
				session({ id: "gone", isTerminated: true, status: "terminated", lastEventAt: ago(MIN) }),
			],
		});
		expect(model.totals).toEqual({ additions: 6, deletions: 3, worktrees: 2 });
	});

	it("marks each overlapping worker with the shared paths and flags quiet agents", () => {
		const model = buildCrew({
			now: NOW,
			diffs: new Map([
				["w1", diff("shared.ts", "own1.ts")],
				["w2", diff("shared.ts")],
			]),
			sessions: [
				session({ id: "w1", lastEventAt: ago(20 * MIN), activity: { state: "active", lastActivityAt: ago(20 * MIN) } }),
				session({ id: "w2" }),
			],
		});
		expect(model.overlaps.map((overlap) => overlap.path)).toEqual(["shared.ts"]);
		expect(model.rows.find((row) => row.session.id === "w1")?.overlapPaths).toEqual(["shared.ts"]);
		expect(model.rows.find((row) => row.session.id === "w1")?.quietMs).toBe(20 * MIN);
		expect(model.rows.find((row) => row.session.id === "w2")?.quietMs).toBeUndefined();
	});

	it("drops a finished session after a day but keeps a recent one", () => {
		const model = buildCrew({
			now: NOW,
			diffs: new Map(),
			sessions: [
				session({ id: "recent", isTerminated: true, status: "terminated", lastEventAt: ago(2 * 60 * MIN), activity: { state: "exited", lastActivityAt: ago(2 * 60 * MIN) } }),
				session({ id: "old", isTerminated: true, status: "terminated", lastEventAt: ago(DONE_WINDOW_MS + MIN), activity: { state: "exited", lastActivityAt: ago(DONE_WINDOW_MS + MIN) } }),
			],
		});
		expect(model.rows.map((row) => row.session.id)).toEqual(["recent"]);
	});

	it("orders a group by creation, newest first, so rows do not jump while agents work", () => {
		const model = buildCrew({
			now: NOW,
			diffs: new Map(),
			sessions: [
				session({ id: "old", createdAt: ago(300 * MIN) }),
				session({ id: "new", createdAt: ago(5 * MIN) }),
				session({ id: "mid", createdAt: ago(60 * MIN) }),
			],
		});
		expect(model.rows.map((row) => row.session.id)).toEqual(["new", "mid", "old"]);
	});

	it("finds the scoped worker and treats an unknown scope as all agents", () => {
		const model = buildCrew({ now: NOW, diffs: new Map(), sessions: [session({ id: "w1" })] });
		expect(scopedRow(model, "w1")?.session.id).toBe("w1");
		expect(scopedRow(model, "nope")).toBeUndefined();
		expect(scopedRow(model, undefined)).toBeUndefined();
	});
});

describe("compactAge and the nudge", () => {
	it("rounds down to the coarsest unit that fits", () => {
		expect(compactAge(8_000)).toEqual({ unit: "seconds", count: 8 });
		expect(compactAge(6 * MIN + 12_000)).toEqual({ unit: "minutes", count: 6 });
		expect(compactAge(2 * 60 * MIN + 5 * MIN)).toEqual({ unit: "hours", count: 2 });
		expect(compactAge(3 * 24 * 60 * MIN)).toEqual({ unit: "days", count: 3 });
		expect(compactAge(-5)).toEqual({ unit: "seconds", count: 0 });
	});

	it("tells the orchestrator the fact and asks for a report, without calling the worker stuck", () => {
		const message = quietNudgeMessage({ id: "w-7", title: "Remote hosts spike" }, 6 * MIN + 20_000);
		expect(message).toContain('"Remote hosts spike" (w-7)');
		expect(message).toContain("6 minutes");
		expect(message).toContain("ao send");
		expect(message.toLowerCase()).not.toContain("stuck");
		expect(message.toLowerCase()).not.toContain("stalled");
	});
});

describe("crewDiffFromManifest", () => {
	it("keeps changed files and totals their lines", () => {
		expect(
			crewDiffFromManifest({
				files: [
					{ path: "a.ts", status: "modified", additions: 4, deletions: 1 },
					{ path: "b.ts", status: "unmodified", additions: 0, deletions: 0 },
					{ path: "c.ts", status: "untracked" },
				],
			}),
		).toEqual({
			files: [
				{ path: "a.ts", additions: 4, deletions: 1 },
				{ path: "c.ts", additions: 0, deletions: 0 },
			],
			additions: 4,
			deletions: 1,
		});
	});
});

describe("plan progress", () => {
	const plan = (...statuses: ("pending" | "in_progress" | "completed")[]): ConversationPlan => ({
		steps: statuses.map((status, index) => ({ text: `step ${index + 1}`, status })),
	});

	it("counts done steps and points at the one in progress", () => {
		expect(planProgress(plan("completed", "completed", "in_progress", "pending"))).toEqual({ done: 2, total: 4, activeIndex: 2, allDone: false });
	});

	it("points at the next pending step when none is in progress, and at the last when all are done", () => {
		expect(planProgress(plan("completed", "pending"))?.activeIndex).toBe(1);
		expect(planProgress(plan("completed", "completed"))).toEqual({ done: 2, total: 2, activeIndex: 1, allDone: true });
	});

	it("has no progress for no plan or an empty one", () => {
		expect(planProgress(undefined)).toBeUndefined();
		expect(planProgress({ steps: [] })).toBeUndefined();
	});

	it("reads the newest turn's plan that no later turn replaced", () => {
		const turns = [
			{ id: "t1", plan: { ...plan("completed"), superseded: true } },
			{ id: "t2", plan: plan("completed", "in_progress") },
			{ id: "t3" },
		];
		expect(currentPlan({ turns } as never)?.steps).toHaveLength(2);
		expect(currentPlan({ turns: [{ id: "x" }] } as never)).toBeUndefined();
		expect(currentPlan(undefined)).toBeUndefined();
	});
});
