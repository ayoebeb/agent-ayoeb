import { LOCAL_HOST, type HostId } from "./hosts";
import { waitingForProject, type OrchestratorWaiting } from "./orchestrator-waiting";
import {
	CLOUD_PROJECT_KIND,
	STANDALONE_PROJECT_KIND,
	STANDALONE_WORKSPACE_ID,
	isOrchestratorSession,
	newestActiveOrchestrator,
	sessionAgentExited,
	sessionIsActive,
	type PullRequestFacts,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "../types/workspace";

/**
 * What one project's orchestrator is doing, in the order the Projects page
 * lists it. "waiting" is the daemon's own wait (an open `orchestrator_waiting`
 * row, see lib/orchestrator-waiting.ts); nothing here guesses it from text.
 * "needs-input" is the raw lifecycle state with no wait row behind it, which is
 * what a project reads as when the user switched orchestrator alerts off.
 */
export type ProjectOrchestratorState = "waiting" | "needs-input" | "working" | "idle" | "stopped" | "none";

const STATE_RANK: Record<ProjectOrchestratorState, number> = {
	waiting: 0,
	"needs-input": 1,
	working: 2,
	idle: 3,
	stopped: 4,
	none: 5,
};

export type PullRequestSummary = {
	/** Open or draft pull requests, one per URL. */
	open: number;
	approved: number;
	changesRequested: number;
	/** Worst CI result among the open ones; "none" when nothing open reports one. */
	ci: "failing" | "pending" | "passing" | "none";
};

export type ProjectCard = {
	/** `host:project`, unique across computers. */
	key: string;
	projectId: string;
	hostId: HostId;
	name: string;
	state: ProjectOrchestratorState;
	/** When the orchestrator started waiting; only for a waiting project. */
	waitingSince?: string;
	/** The orchestrator session a click opens; absent when the project has none. */
	orchestratorId?: string;
	/** Live workers (not archived, not merged). */
	activeWorkers: number;
	prs: PullRequestSummary;
	lastActivityAt?: string;
};

/** Projects the page lists: this computer's and linked computers', not the scratchpad or cloud projects. */
export function isOverviewProject(workspace: WorkspaceSummary): boolean {
	return (
		workspace.id !== STANDALONE_WORKSPACE_ID &&
		workspace.kind !== STANDALONE_PROJECT_KIND &&
		workspace.kind !== CLOUD_PROJECT_KIND
	);
}

export function orchestratorStateOf(
	orchestrator: WorkspaceSession | undefined,
	wait: OrchestratorWaiting | undefined,
): ProjectOrchestratorState {
	if (wait) return "waiting";
	if (!orchestrator) return "none";
	if (sessionAgentExited(orchestrator)) return "stopped";
	switch (orchestrator.activity?.state) {
		case "active":
			return "working";
		case "waiting_input":
		case "blocked":
			return "needs-input";
		case "exited":
			return "stopped";
		default:
			return "idle";
	}
}

function isLiveWorker(session: WorkspaceSession): boolean {
	return !isOrchestratorSession(session) && sessionIsActive(session) && session.status !== "merged";
}

export function summarizePullRequests(sessions: readonly WorkspaceSession[]): PullRequestSummary {
	// One entry per pull request: two sessions can point at the same one.
	const byUrl = new Map<string, PullRequestFacts>();
	for (const pr of sessions.flatMap((session) => session.prs ?? [])) {
		if (pr.state !== "open" && pr.state !== "draft") continue;
		const id = pr.url || String(pr.number);
		if (!byUrl.has(id)) byUrl.set(id, pr);
	}
	const open = [...byUrl.values()];
	const ci = open.some((pr) => pr.ci === "failing")
		? "failing"
		: open.some((pr) => pr.ci === "pending")
			? "pending"
			: open.length > 0 && open.every((pr) => pr.ci === "passing")
				? "passing"
				: "none";
	return {
		open: open.length,
		approved: open.filter((pr) => pr.review === "approved").length,
		changesRequested: open.filter((pr) => pr.review === "changes_requested").length,
		ci,
	};
}

/** One card per project, from this computer's projects and every linked computer's. */
export function buildProjectCards(workspaces: readonly WorkspaceSummary[], waiting: readonly OrchestratorWaiting[]): ProjectCard[] {
	return workspaces.filter(isOverviewProject).map((workspace) => {
		const hostId = workspace.hostId ?? LOCAL_HOST;
		const orchestrator = newestActiveOrchestrator(workspace.sessions);
		const wait = waitingForProject(waiting, hostId, workspace.id);
		return {
			key: `${hostId}:${workspace.id}`,
			projectId: workspace.id,
			hostId,
			name: workspace.name,
			state: orchestratorStateOf(orchestrator, wait),
			waitingSince: wait?.since,
			orchestratorId: wait?.sessionId ?? orchestrator?.id,
			activeWorkers: workspace.sessions.filter(isLiveWorker).length,
			prs: summarizePullRequests(workspace.sessions),
			lastActivityAt: orchestrator?.activity?.lastActivityAt ?? orchestrator?.updatedAt,
		};
	});
}

const time = (value: string | undefined): number => {
	const parsed = Date.parse(value ?? "");
	return Number.isFinite(parsed) ? parsed : 0;
};

/**
 * Projects that wait for the user first, the longest wait on top. After that,
 * by what the orchestrator is doing, newest activity first, then by name, so the
 * order stays put while the user reads it.
 */
export function sortProjectCards(cards: readonly ProjectCard[]): ProjectCard[] {
	return [...cards].sort((a, b) => {
		if (a.state !== b.state) return STATE_RANK[a.state] - STATE_RANK[b.state];
		if (a.state === "waiting") {
			const waited = time(a.waitingSince) - time(b.waitingSince);
			if (waited !== 0) return waited;
		} else {
			const recent = time(b.lastActivityAt) - time(a.lastActivityAt);
			if (recent !== 0) return recent;
		}
		return a.name.localeCompare(b.name) || a.key.localeCompare(b.key);
	});
}
