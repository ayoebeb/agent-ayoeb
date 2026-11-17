import { useCallback, useRef } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { LOCAL_HOST, isLocal, sessionUiKey } from "../lib/hosts";
import { sessionNavigateTarget } from "../lib/navigate-to-session";
import type { SplitSide } from "../lib/pane-split";
import {
	MAX_SPLIT_PANES,
	paneHost,
	paneKeyForSession,
	resolvePane,
	sessionPaneKey,
	type RoutedSession,
	type SessionPaneRef,
	type SessionSplit,
} from "../lib/session-split";
import { useSessionSplitStore } from "../stores/session-split-store";
import { useSidebarSelectionStore } from "../stores/sidebar-selection-store";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSummary } from "../types/workspace";

/** The session a route names, on this computer or the host it carries, with its project. */
export function findRoutedSession(workspaces: readonly WorkspaceSummary[] | undefined, routed: Partial<RoutedSession> | undefined) {
	if (!routed?.sessionId) return undefined;
	const host = routed.hostId && !isLocal(routed.hostId) ? routed.hostId : undefined;
	for (const workspace of workspaces ?? []) {
		if ((workspace.hostId || undefined) !== host) continue;
		const session = workspace.sessions.find((candidate) => candidate.id === routed.sessionId);
		if (session) return { session, workspace };
	}
	return undefined;
}

/** The route a pane's session opens at, on its own computer. */
export function paneNavigateTarget(projectId: string, sessionId: string, hostId?: string) {
	return sessionNavigateTarget(projectId, sessionId, hostId ?? LOCAL_HOST);
}

/**
 * The pane a new one goes beside: the focused pane while the split is on
 * screen, else the session the route shows.
 */
export function paneOnScreen(
	split: SessionSplit | null,
	workspaces: readonly WorkspaceSummary[] | undefined,
	routedSession: Partial<RoutedSession> | undefined,
): SessionPaneRef | undefined {
	const routed = findRoutedSession(workspaces, routedSession);
	if (!routed) return undefined;
	const member = paneKeyForSession(split, routed.session);
	if (split && member) return split.panes[split.focus ?? member] ?? split.panes[member];
	return { kind: "session", sessionId: routed.session.id, projectId: routed.workspace.id, ...(routed.session.hostId ? { hostId: routed.session.hostId } : {}) };
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
	const routedSessionId = useParams({ strict: false, select: (params) => (params as { sessionId?: string }).sessionId });
	const routedHostId = useParams({ strict: false, select: (params) => (params as { hostId?: string }).hostId });
	const routedSessionIdRef = useRef(routedSessionId);
	routedSessionIdRef.current = routedSessionId;
	const routedHostIdRef = useRef(routedHostId);
	routedHostIdRef.current = routedHostId;

	/** The routed session id, but only for a pane on the routed session's computer. */
	const routedHost = useCallback(() => (routedHostIdRef.current && !isLocal(routedHostIdRef.current) ? routedHostIdRef.current : undefined), []);
	const routedFor = useCallback((ref: SessionPaneRef) => (paneHost(ref) === routedHost() ? routedSessionIdRef.current : undefined), [routedHost]);

	const showPane = useCallback(
		(ref: SessionPaneRef) => {
			const pane = resolvePane(ref, readWorkspaces(), routedFor(ref));
			if (pane.kind === "session" || pane.kind === "ended") void navigate(paneNavigateTarget(pane.workspace.id, pane.session.id, paneHost(ref)));
			else if (pane.kind === "remote") void navigate(paneNavigateTarget(pane.projectId, pane.sessionId, pane.hostId));
		},
		[navigate, readWorkspaces, routedFor],
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
			const live = refs.filter((ref) => {
					const kind = resolvePane(ref, readWorkspaces(), routedFor(ref)).kind;
					return kind === "session" || kind === "remote";
				});
			const onScreen = live.find((ref) => {
				const pane = resolvePane(ref, readWorkspaces(), routedFor(ref));
				const id = pane.kind === "session" ? pane.session.id : pane.kind === "remote" ? pane.sessionId : undefined;
				return id === routedSessionIdRef.current && paneHost(ref) === routedHost();
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
		[readWorkspaces, routedFor, routedHost, showPane, t, tooMany],
	);

	/** Shows `ref` on `side` of `anchor`: the session on screen or a pane of the split. */
	const addBeside = useCallback(
		(anchor: SessionPaneRef, ref: SessionPaneRef, side: SplitSide) => {
			const sessionOf = (candidate: SessionPaneRef) => {
				const pane = resolvePane(candidate, readWorkspaces(), routedFor(candidate));
				// Qualified by computer: the same id on two computers is two sessions.
				const id = pane.kind === "session" || pane.kind === "ended" ? pane.session.id : pane.kind === "remote" ? pane.sessionId : undefined;
				return id ? sessionUiKey(id, paneHost(candidate)) : undefined;
			};
			const outcome = useSessionSplitStore.getState().splitBeside(anchor, ref, side, sessionOf);
			if (outcome === "full") tooMany();
			if (outcome !== "split") return false;
			useSidebarSelectionStore.getState().clear();
			showPane(ref);
			return true;
		},
		[readWorkspaces, routedFor, showPane, tooMany],
	);

	return { open, addBeside, tooMany };
}
