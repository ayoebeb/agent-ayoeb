import { useCallback, useRef } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { sessionNavigateTarget } from "../lib/navigate-to-session";
import type { SplitSide } from "../lib/pane-split";
import {
	MAX_SPLIT_PANES,
	paneKeyForSession,
	resolvePane,
	sessionPaneKey,
	type SessionPaneRef,
	type SessionSplit,
} from "../lib/session-split";
import { useSessionSplitStore } from "../stores/session-split-store";
import { useSidebarSelectionStore } from "../stores/sidebar-selection-store";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSummary } from "../types/workspace";

/** The local session a route names, with its project. */
export function findLocalSession(workspaces: readonly WorkspaceSummary[] | undefined, sessionId: string | undefined) {
	if (!sessionId) return undefined;
	for (const workspace of workspaces ?? []) {
		if (workspace.hostId) continue;
		const session = workspace.sessions.find((candidate) => candidate.id === sessionId);
		if (session) return { session, workspace };
	}
	return undefined;
}

/**
 * The pane a new one goes beside: the focused pane while the split is on
 * screen, else the session the route shows.
 */
export function paneOnScreen(
	split: SessionSplit | null,
	workspaces: readonly WorkspaceSummary[] | undefined,
	routedSessionId: string | undefined,
): SessionPaneRef | undefined {
	const routed = findLocalSession(workspaces, routedSessionId);
	if (!routed) return undefined;
	const member = paneKeyForSession(split, routed.session);
	if (split && member) return split.panes[split.focus ?? member] ?? split.panes[member];
	return { kind: "session", sessionId: routed.session.id, projectId: routed.workspace.id };
}

/**
 * Opening sessions side by side, shared by the sidebar's selection, its row
 * menus and a row dropped on the center. The route follows the focused pane,
 * which is what keeps the split on screen.
 */
export function useSessionSplitActions(getWorkspaces: () => readonly WorkspaceSummary[] | undefined) {
	const navigate = useNavigate();
	const { t } = useTranslation();
	const getWorkspacesRef = useRef(getWorkspaces);
	getWorkspacesRef.current = getWorkspaces;
	const readWorkspaces = useCallback(() => getWorkspacesRef.current(), []);
	const routedSessionId = useParams({
		strict: false,
		select: (params) => ((params as { hostId?: string }).hostId ? undefined : (params as { sessionId?: string }).sessionId),
	});
	const routedSessionIdRef = useRef(routedSessionId);
	routedSessionIdRef.current = routedSessionId;

	const showPane = useCallback(
		(ref: SessionPaneRef) => {
			const pane = resolvePane(ref, readWorkspaces(), routedSessionIdRef.current);
			if (pane.kind === "session" || pane.kind === "ended") void navigate(sessionNavigateTarget(pane.workspace.id, pane.session.id));
		},
		[navigate, readWorkspaces],
	);

	const tooMany = useCallback(
		() => useUiStore.getState().showGlobalToast(t("sessionSplit.tooMany", { count: MAX_SPLIT_PANES })),
		[t],
	);

	/**
	 * Shows these panes side by side, focused on the session already on screen
	 * when it is one of them, else on the first one that has a session.
	 */
	const open = useCallback(
		(refs: readonly SessionPaneRef[]) => {
			if (refs.length > MAX_SPLIT_PANES) {
				tooMany();
				return false;
			}
			const live = refs.filter((ref) => resolvePane(ref, readWorkspaces(), routedSessionIdRef.current).kind === "session");
			const onScreen = live.find((ref) => {
				const pane = resolvePane(ref, readWorkspaces(), routedSessionIdRef.current);
				return pane.kind === "session" && pane.session.id === routedSessionIdRef.current;
			});
			const first = onScreen ?? live[0];
			if (!first) {
				useUiStore.getState().showGlobalToast(
					t(refs.every((ref) => ref.kind === "orchestrator") ? "sessionSplit.noOrchestrators" : "sessionSplit.nothingToShow"),
				);
				return false;
			}
			if (!useSessionSplitStore.getState().open(refs, sessionPaneKey(first))) return false;
			useSidebarSelectionStore.getState().clear();
			showPane(first);
			return true;
		},
		[readWorkspaces, showPane, t, tooMany],
	);

	/** Shows `ref` on `side` of `anchor`: the session on screen or a pane of the split. */
	const addBeside = useCallback(
		(anchor: SessionPaneRef, ref: SessionPaneRef, side: SplitSide) => {
			const sessionOf = (candidate: SessionPaneRef) => {
				const pane = resolvePane(candidate, readWorkspaces(), routedSessionIdRef.current);
				return pane.kind === "session" || pane.kind === "ended" ? pane.session.id : undefined;
			};
			const outcome = useSessionSplitStore.getState().splitBeside(anchor, ref, side, sessionOf);
			if (outcome === "full") tooMany();
			if (outcome !== "split") return false;
			useSidebarSelectionStore.getState().clear();
			showPane(ref);
			return true;
		},
		[readWorkspaces, showPane, tooMany],
	);

	return { open, addBeside, tooMany };
}
