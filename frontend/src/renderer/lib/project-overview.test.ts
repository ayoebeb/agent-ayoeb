import { describe, expect, it } from "vitest";
import type { OrchestratorWaiting } from "./orchestrator-waiting";
import {
	buildProjectCards,
	isOverviewProject,
	orchestratorStateOf,
	sortProjectCards,
	summarizePullRequests,
	type ProjectCard,
} from "./project-overview";
import type { PullRequestFacts, WorkspaceSession, WorkspaceSummary } from "../types/workspace";

const session = (over: Partial<WorkspaceSession> & { id: string }): WorkspaceSession =>
	({
		workspaceId: "p",
		workspaceName: "P",
		title: over.id,
		provider: "claude-code",
		status: "working",
		updatedAt: "2026-11-17T09:00:00Z",
		prs: [],
		...over,
	}) as WorkspaceSession;
const orchestrator = (id: string, state: "active" | "idle" | "waiting_input" | "blocked" | "exited" | "unknown", at = "2026-11-17T10:00:00Z") =>
	session({ id, kind: "orchestrator", activity: { state, lastActivityAt: at } });
const project = (id: string, sessions: WorkspaceSession[], extra: Partial<WorkspaceSummary> = {}): WorkspaceSummary =>
	({ id, name: id, path: `/${id}`, sessions, ...extra }) as WorkspaceSummary;
let prNumber = 0;
const pr = (over: Partial<PullRequestFacts> = {}): PullRequestFacts => {
	prNumber += 1;
	return { url: `https://example.test/pr/${prNumber}`, number: prNumber, state: "open", ci: "unknown", review: "none", mergeability: "mergeable", reviewComments: false, updatedAt: "2026-11-17T09:00:00Z", ...over };
};
const wait = (projectId: string, sessionId: string, since: string, hostId = "local"): OrchestratorWaiting => ({
	notificationId: `n-${projectId}`,
	projectId,
	sessionId,
	question: "Merge now?",
	reminded: false,
	since,
	title: `${projectId}: orchestrator is waiting for you`,
	hostId,
});

describe("orchestratorStateOf", () => {
	it("is waiting whenever the daemon has an open wait, whatever the session says", () => {
		const w = wait("p", "o1", "2026-11-17T10:00:00Z");
		expect(orchestratorStateOf(orchestrator("o1", "active"), w)).toBe("waiting");
		expect(orchestratorStateOf(undefined, w)).toBe("waiting");
	});

	it("reads the rest from the orchestrator's activity", () => {
		expect(orchestratorStateOf(undefined, undefined)).toBe("none");
		expect(orchestratorStateOf(orchestrator("o", "active"), undefined)).toBe("working");
		expect(orchestratorStateOf(orchestrator("o", "idle"), undefined)).toBe("idle");
		expect(orchestratorStateOf(orchestrator("o", "unknown"), undefined)).toBe("idle");
		expect(orchestratorStateOf(orchestrator("o", "waiting_input"), undefined)).toBe("needs-input");
		expect(orchestratorStateOf(orchestrator("o", "blocked"), undefined)).toBe("needs-input");
		expect(orchestratorStateOf(orchestrator("o", "exited"), undefined)).toBe("stopped");
	});
});

describe("summarizePullRequests", () => {
	it("counts open and draft pull requests once, and the worst CI result among them", () => {
		const shared = pr({ ci: "passing", review: "approved" });
		const summary = summarizePullRequests([
			session({ id: "w1", prs: [shared, pr({ state: "draft", ci: "pending" })] }),
			session({ id: "w2", prs: [shared, pr({ state: "merged", ci: "failing" }), pr({ state: "closed", ci: "failing" })] }),
		]);
		expect(summary).toEqual({ open: 2, approved: 1, changesRequested: 0, ci: "pending" });
	});

	it("ranks failing above pending above passing, and says none when nothing reports", () => {
		expect(summarizePullRequests([session({ id: "a", prs: [pr({ ci: "passing" }), pr({ ci: "failing", review: "changes_requested" })] })])).toMatchObject({ ci: "failing", changesRequested: 1 });
		expect(summarizePullRequests([session({ id: "a", prs: [pr({ ci: "passing" }), pr({ ci: "passing" })] })]).ci).toBe("passing");
		expect(summarizePullRequests([session({ id: "a", prs: [pr({ ci: "unknown" })] })]).ci).toBe("none");
		expect(summarizePullRequests([])).toEqual({ open: 0, approved: 0, changesRequested: 0, ci: "none" });
	});
});

describe("buildProjectCards", () => {
	it("lists local and linked-computer projects, and leaves out the scratchpad and cloud projects", () => {
		expect(isOverviewProject(project("a", []))).toBe(true);
		expect(isOverviewProject(project("scratch", [], { kind: "standalone" }))).toBe(false);
		expect(isOverviewProject(project("c", [], { kind: "cloud" }))).toBe(false);
		const cards = buildProjectCards(
			[project("a", []), project("a", [], { hostId: "box" }), project("c", [], { kind: "cloud" })],
			[],
		);
		expect(cards.map((card) => card.key)).toEqual(["local:a", "box:a"]);
	});

	it("counts only live workers: not the orchestrator, archived or merged ones", () => {
		const [card] = buildProjectCards(
			[project("a", [
				orchestrator("o", "idle"),
				session({ id: "w1" }),
				session({ id: "w2", status: "needs_input" }),
				session({ id: "w3", isTerminated: true }),
				session({ id: "w4", status: "merged" }),
			])],
			[],
		);
		expect(card.activeWorkers).toBe(2);
	});

	it("opens the orchestrator that waits, even when the project list does not hold it yet", () => {
		const waits = [wait("a", "o-wait", "2026-11-17T10:00:00Z")];
		const [withOrchestrator] = buildProjectCards([project("a", [orchestrator("o-live", "active")])], waits);
		expect(withOrchestrator).toMatchObject({ state: "waiting", orchestratorId: "o-wait", waitingSince: "2026-11-17T10:00:00Z" });
		const [bare] = buildProjectCards([project("a", [])], waits);
		expect(bare).toMatchObject({ state: "waiting", orchestratorId: "o-wait" });
		const [none] = buildProjectCards([project("a", [])], []);
		expect(none).toMatchObject({ state: "none", orchestratorId: undefined });
	});

	it("matches a wait to its own computer", () => {
		const cards = buildProjectCards([project("a", [orchestrator("o", "idle")]), project("a", [orchestrator("o", "idle")], { hostId: "box" })], [wait("a", "o", "2026-11-17T10:00:00Z", "box")]);
		expect(cards.map((card) => [card.key, card.state])).toEqual([["local:a", "idle"], ["box:a", "waiting"]]);
	});
});

describe("sortProjectCards", () => {
	const card = (name: string, state: ProjectCard["state"], over: Partial<ProjectCard> = {}): ProjectCard => ({
		key: `local:${name}`,
		projectId: name,
		hostId: "local",
		name,
		state,
		activeWorkers: 0,
		prs: { open: 0, approved: 0, changesRequested: 0, ci: "none" },
		...over,
	});

	it("puts the projects waiting for the user first, the longest wait on top", () => {
		const sorted = sortProjectCards([
			card("alpha", "idle", { lastActivityAt: "2026-11-17T12:00:00Z" }),
			card("beta", "waiting", { waitingSince: "2026-11-17T11:00:00Z" }),
			card("gamma", "waiting", { waitingSince: "2026-11-17T10:00:00Z" }),
			card("delta", "working", { lastActivityAt: "2026-11-17T09:00:00Z" }),
			card("eps", "none"),
			card("zeta", "needs-input"),
			card("eta", "stopped"),
		]);
		expect(sorted.map((c) => c.name)).toEqual(["gamma", "beta", "zeta", "delta", "alpha", "eta", "eps"]);
	});

	it("orders the rest of a state by newest activity, then name, and does not touch its input", () => {
		const input = [
			card("b", "idle", { lastActivityAt: "2026-11-17T08:00:00Z" }),
			card("c", "idle", { lastActivityAt: "2026-11-17T09:00:00Z" }),
			card("a", "idle", { lastActivityAt: "2026-11-17T08:00:00Z" }),
		];
		const copy = [...input];
		expect(sortProjectCards(input).map((c) => c.name)).toEqual(["c", "a", "b"]);
		expect(input).toEqual(copy);
	});
});
