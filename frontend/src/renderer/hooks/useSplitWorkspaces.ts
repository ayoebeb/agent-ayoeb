import { useMemo } from "react";
import { paneKeyForSession, sessionPaneKey, type RoutedSession, type SessionSplit } from "../lib/session-split";
import { useSessionSplitStore } from "../stores/session-split-store";
import type { WorkspaceSummary } from "../types/workspace";
import { findRoutedSession } from "./useSessionSplitActions";
import { useRemoteWorkspaces, useWorkspaceQuery } from "./useWorkspaceQuery";

/**
 * Every project the split can show a pane from: this computer's, then each
 * connected computer's. Undefined until this computer's list has loaded. A
 * computer that is offline contributes nothing, so its panes show their offline state.
 */
export function useSplitWorkspaces(): readonly WorkspaceSummary[] | undefined {
	const local = useWorkspaceQuery().data;
	const remote = useRemoteWorkspaces().data;
	return useMemo(() => (local ? (remote.length ? [...local, ...remote] : local) : undefined), [local, remote]);
}

/**
 * The split pane a routed session belongs to, on whichever computer it runs:
 * its own pane, or for an orchestrator, its project's orchestrator pane.
 */
export function routedPaneKeyOf(
	split: SessionSplit | null,
	workspaces: readonly WorkspaceSummary[] | undefined,
	routed: Partial<RoutedSession>,
): string | undefined {
	if (!split || !routed.sessionId) return undefined;
	const own = sessionPaneKey({ kind: "session", sessionId: routed.sessionId, projectId: "", hostId: routed.hostId });
	return split.panes[own] ? own : paneKeyForSession(split, findRoutedSession(workspaces, routed)?.session);
}

/** True while the split is on screen for this route, so the route itself renders nothing. */
export function useRouteInSplit(routed: Partial<RoutedSession>): boolean {
	const split = useSessionSplitStore((state) => state.split);
	const workspaces = useSplitWorkspaces();
	return Boolean(routedPaneKeyOf(split, workspaces, routed));
}
