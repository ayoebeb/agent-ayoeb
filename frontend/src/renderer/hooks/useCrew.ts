import { useQueries, useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState } from "react";
import { clientForSessionHost } from "../lib/host-clients";
import { buildCrew, crewDiffFromManifest, currentPlan, type CrewDiff, type CrewModel } from "../lib/crew";
import {
	pendingApproval,
	pendingUserInput,
	type ConversationActivity,
	type ConversationPlan,
} from "../types/conversation";
import {
	isOrchestratorSession,
	sessionIsActive,
	sessionWorkspaceReady,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "../types/workspace";
import { toSnapshot } from "./useConversation";
import { sessionWorkspaceFilesQueryOptions, type WorkspaceFilesResponse } from "./useSessionWorkspaceFiles";
import { useRemoteProjectQuery, workspaceQueryOptions } from "./useWorkspaceQuery";

/** Worktrees change without an event, so the crew reads them on a gentle clock. */
const DIFF_POLL_MS = 20_000;
/** Re-evaluate who has gone quiet. Coarse on purpose: the threshold is minutes. */
const QUIET_TICK_MS = 30_000;
/** A page small enough to poll: the newest rows hold the pending request and the turn's plan. */
const CREW_CONVERSATION_LIMIT = 40;
const CREW_CONVERSATION_OUTPUT_LIMIT = 64;
const CONVERSATION_POLL_MS = 15_000;

const EMPTY_SESSIONS: WorkspaceSession[] = [];

/** A worker whose worktree the daemon can read. Cloud workers have none on this machine. */
function hasReadableWorktree(session: WorkspaceSession): boolean {
	return !isOrchestratorSession(session) && sessionIsActive(session) && !session.cloud && !session.needsResume && sessionWorkspaceReady(session);
}

/** The current time, refreshed every `intervalMs` while `running`; frozen at mount otherwise. */
function useClock(running: boolean, intervalMs: number): Date {
	const [now, setNow] = useState(() => new Date());
	useEffect(() => {
		if (!running) return;
		setNow(new Date());
		const id = window.setInterval(() => setNow(new Date()), intervalMs);
		return () => window.clearInterval(id);
	}, [intervalMs, running]);
	return now;
}

export type UseCrewResult = {
	model: CrewModel;
	/** The project's sessions the model was built from, for callers that need the raw rows. */
	sessions: WorkspaceSession[];
	/** Wall clock the model was evaluated at; relative times render from the same instant. */
	now: Date;
};

/**
 * Every worker of the orchestrator's project, with the diff each worktree carries.
 * `enabled` is false while nothing on screen shows the crew. `poll` makes this
 * caller keep the worktrees fresh: every observer with an interval runs its own
 * timer, so exactly one caller (the control bar, always mounted with the crew)
 * polls and the rail and tabs read what it keeps current.
 */
export function useCrew(orchestrator: WorkspaceSession | undefined, enabled = true, poll = false): UseCrewResult {
	const hostId = orchestrator?.hostId;
	const projectId = orchestrator?.workspaceId;
	const selectProjectSessions = useCallback(
		(workspaces: WorkspaceSummary[]) => workspaces.find((workspace) => workspace.id === projectId)?.sessions ?? EMPTY_SESSIONS,
		[projectId],
	);
	const local = useQuery({ ...workspaceQueryOptions, select: selectProjectSessions, enabled: !hostId && Boolean(projectId) });
	const remote = useRemoteProjectQuery(hostId ?? "", projectId ?? "");
	const sessions = (hostId ? remote.data?.sessions : local.data) ?? EMPTY_SESSIONS;
	// Only a crew that is on screen needs a clock; every other session's inspector calls
	// this hook too and must not re-render every 30 seconds for nothing.
	const now = useClock(Boolean(orchestrator) && enabled, QUIET_TICK_MS);
	const readable = useMemo(() => sessions.filter(hasReadableWorktree), [sessions]);
	const diffs = useQueries({
		queries: readable.map((worker) => ({
			...sessionWorkspaceFilesQueryOptions(worker.id, undefined, hostId),
			enabled: enabled && Boolean(orchestrator),
			refetchInterval: poll ? DIFF_POLL_MS : (false as const),
			// Shared with the Files view's cache entry; only this observer reads the reduction.
			select: (data: WorkspaceFilesResponse): CrewDiff => crewDiffFromManifest(data),
		})),
		combine: (results) => results.map((result) => result.data),
	});
	const model = useMemo(() => {
		const byId = new Map<string, CrewDiff>();
		readable.forEach((worker, index) => {
			const diff = diffs[index];
			if (diff) byId.set(worker.id, diff);
		});
		return buildCrew({ sessions, diffs: byId, now: now.getTime() });
	}, [diffs, now, readable, sessions]);
	return { model, sessions, now };
}

export type CrewConversation = {
	plan?: ConversationPlan;
	approval?: ConversationActivity;
	question?: ConversationActivity;
};

/**
 * What a chat worker is doing right now: the plan of its turn and any request
 * waiting on the person. A small page of the conversation on its own cache entry,
 * so watching ten workers never pulls ten full transcripts into the chat cache.
 * A Terminal worker has no conversation: it answers with nothing, and the row
 * falls back to its derived status.
 */
export function useCrewConversation(session: WorkspaceSession, enabled: boolean): CrewConversation | undefined {
	const hostId = session.hostId;
	const watch = enabled && session.mode === "chat" && sessionIsActive(session) && !session.cloud && !session.needsResume;
	const query = useQuery({
		queryKey: crewConversationQueryKey(session.id, hostId),
		enabled: watch,
		staleTime: 10_000,
		refetchInterval: CONVERSATION_POLL_MS,
		retry: false,
		queryFn: async (): Promise<CrewConversation> => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/sessions/{sessionId}/conversation", {
				params: {
					path: { sessionId: session.id },
					query: { limit: CREW_CONVERSATION_LIMIT, outputLimit: CREW_CONVERSATION_OUTPUT_LIMIT },
				},
			});
			if (error || !data) throw error ?? new Error("conversation unavailable");
			const snapshot = toSnapshot(data as Parameters<typeof toSnapshot>[0]);
			return { plan: currentPlan(snapshot), approval: pendingApproval(snapshot), question: pendingUserInput(snapshot) };
		},
	});
	return watch ? query.data : undefined;
}

export const crewConversationQueryKey = (sessionId: string, hostId?: string) =>
	hostId ? (["crew-conversation", hostId, sessionId] as const) : (["crew-conversation", sessionId] as const);
