import { useCallback, useEffect, useMemo, useRef, type CSSProperties, type ReactNode } from "react";
import { CatchBoundary, useNavigate } from "@tanstack/react-router";
import { Suspense } from "react";
import { useTranslation } from "react-i18next";
import { X } from "@/components/app-icons";
import { useHostConnection } from "../hooks/useHostConnection";
import { useProjectOrchestratorAction } from "../hooks/useProjectOrchestratorAction";
import { findRoutedSession, paneNavigateTarget, paneOnScreen, useSessionSplitActions } from "../hooks/useSessionSplitActions";
import { useSplitWorkspaces } from "../hooks/useSplitWorkspaces";
import { LOCAL_HOST, isLocal, type HostId } from "../lib/hosts";
import { projectNavigateTarget } from "../lib/navigate-to-session";
import { registerSessionDropTarget } from "../lib/pane-drag";
import { containsPoint, edgeAt, halfOf, layoutBoxes, layoutCss, leafKeys, toClientRect, type ClientRect } from "../lib/pane-split";
import {
	closeSplitPane,
	MAX_SPLIT_PANES,
	paneHost,
	paneKeyForSession,
	paneRefFromDragKey,
	resolvePane,
	sessionPaneKey,
	type ResolvedPane,
	type SessionPaneRef,
	type SessionSplit,
} from "../lib/session-split";
import { useSessionSplitStore } from "../stores/session-split-store";
import { STANDALONE_WORKSPACE_ID, type WorkspaceSession, type WorkspaceSummary } from "../types/workspace";
import { LazyChunkErrorPanel } from "./LazyChunkBoundary";
import { SessionTabsTopbar } from "./PageTopbar";
import { SessionPaneChromeContext, SessionPaneContext, type SessionPaneValue } from "./session-pane-context";
import { SessionRowStatusGlyph } from "./Sidebar";
import { SessionTopbarProvider } from "./SessionTopbarPortal";
import { SplitDivider } from "./SplitDivider";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

export type SessionPaneViewProps = { sessionId: string; projectId?: string; cloudOrgId?: string; hostId?: HostId };

const hostOf = (hostId: string | undefined) => (hostId && !isLocal(hostId) ? hostId : undefined);

/** The route a pane's session opens at: the scratchpad's sessions have no project in theirs. */
function routeProjectId(workspace: WorkspaceSummary): string | undefined {
	return workspace.id === STANDALONE_WORKSPACE_ID ? undefined : workspace.id;
}

/**
 * Sessions side by side. Each pane is the whole session as it renders on its
 * own (chat or terminal), with its top bar as the pane header. The focused
 * pane is the routed session: clicking a pane focuses it and moves the route
 * there, so the sidebar, ⌘K and every shortcut follow it, and only that pane
 * takes keyboard input. Dividers and the layout are the in-session split's.
 */
export function SessionSplitView({
	split,
	routedSessionId,
	routedHostId,
	routedPaneKey,
	cloudOrgId,
	renderSession,
}: {
	split: SessionSplit;
	routedSessionId: string;
	routedHostId?: HostId;
	routedPaneKey: string;
	cloudOrgId?: string;
	renderSession: (props: SessionPaneViewProps) => ReactNode;
}) {
	const navigate = useNavigate();
	const workspaces = useSplitWorkspaces();
	const workspacesRef = useRef(workspaces);
	workspacesRef.current = workspaces;
	const screenRef = useRef<HTMLDivElement | null>(null);
	const layout = useMemo(() => layoutCss(split.root), [split.root]);
	const boxes = useMemo(() => layoutBoxes(split.root).leaves, [split.root]);
	const keys = useMemo(() => leafKeys(split.root), [split.root]);
	const focusKey = split.focus && split.panes[split.focus] ? split.focus : routedPaneKey;
	const routedRef = useRef({ sessionId: routedSessionId, hostId: hostOf(routedHostId), paneKey: routedPaneKey });
	routedRef.current = { sessionId: routedSessionId, hostId: hostOf(routedHostId), paneKey: routedPaneKey };
	/** The routed session id, only for a pane on the routed session's computer. */
	const routedFor = useCallback((ref: SessionPaneRef) => (paneHost(ref) === routedRef.current.hostId ? routedRef.current.sessionId : undefined), []);

	// The route names the focused pane; following a link or the sidebar to
	// another pane's session focuses that pane.
	useEffect(() => {
		useSessionSplitStore.getState().focus(routedPaneKey);
	}, [routedPaneKey]);

	const showPaneRoute = useCallback(
		(pane: ResolvedPane, replace: boolean) => {
			const target =
				pane.kind === "session" || pane.kind === "ended"
					? { project: pane.workspace.id, id: pane.session.id, host: hostOf(pane.session.hostId) }
					: pane.kind === "remote"
						? { project: pane.projectId, id: pane.sessionId, host: pane.hostId }
						: undefined;
			if (!target) return;
			if (target.id === routedRef.current.sessionId && target.host === routedRef.current.hostId) return;
			void navigate({ ...paneNavigateTarget(target.project, target.id, target.host), replace });
		},
		[navigate],
	);

	const focusPane = useCallback(
		(key: string) => {
			const state = useSessionSplitStore.getState();
			const ref = state.split?.panes[key];
			if (!ref) return;
			state.focus(key);
			// Focus moves between panes in place, so it replaces the history entry.
			// A pane with no live session takes focus where the route already is.
			const pane = resolvePane(ref, workspacesRef.current, routedFor(ref));
			if (pane.kind === "session" || pane.kind === "remote") showPaneRoute(pane, true);
		},
		[routedFor, showPaneRoute],
	);

	// A press or keyboard focus inside a pane focuses it. Native listeners:
	// terminals are portaled in from the shared cache, so React would deliver
	// their events elsewhere. A pane's close button closes it without focusing it.
	useEffect(() => {
		const paneOf = (target: EventTarget | null) => {
			if (target instanceof Element && target.closest("[data-pane-close]")) return undefined;
			const pane = target instanceof Element ? target.closest<HTMLElement>("[data-session-pane]") : null;
			return pane && screenRef.current?.contains(pane) ? pane.dataset.sessionPane : undefined;
		};
		const onFocus = (event: Event) => {
			const key = paneOf(event.target);
			if (key) focusPane(key);
		};
		document.addEventListener("pointerdown", onFocus, true);
		document.addEventListener("focusin", onFocus, true);
		return () => {
			document.removeEventListener("pointerdown", onFocus, true);
			document.removeEventListener("focusin", onFocus, true);
		};
	}, [focusPane]);

	// Keys go to the focused pane only: a caret left in another pane lets go,
	// and the focused pane's composer or terminal takes it (its autofocus).
	useEffect(() => {
		const active = document.activeElement;
		const pane = active instanceof HTMLElement ? active.closest<HTMLElement>("[data-session-pane]") : null;
		if (pane && pane.dataset.sessionPane !== focusKey && screenRef.current?.contains(pane)) (active as HTMLElement).blur();
	}, [focusKey]);

	const closePane = useCallback(
		(key: string) => {
			const state = useSessionSplitStore.getState();
			const current = state.split;
			if (!current) return;
			const remaining = closeSplitPane(current, key);
			// The route may already be on its way to the focused pane.
			const wasShown = routedRef.current.paneKey === key || current.focus === key;
			state.close(key);
			if (!wasShown) return;
			// Only a pane with a session can hold the route, and the route is what
			// keeps the split on screen. So the route moves to the first remaining
			// pane that has one, the focused pane first; a project board only when none does.
			const others = leafKeys(current.root).filter((candidate) => candidate !== key);
			const order = remaining?.focus ? [remaining.focus, ...others.filter((candidate) => candidate !== remaining.focus)] : others;
			for (const candidate of order) {
				const ref = current.panes[candidate];
				const pane = ref ? resolvePane(ref, workspacesRef.current) : undefined;
				if (pane?.kind !== "session" && pane?.kind !== "ended" && pane?.kind !== "remote") continue;
				if (remaining) useSessionSplitStore.getState().focus(candidate);
				showPaneRoute(pane, true);
				return;
			}
			const fallback = order[0] ? current.panes[order[0]] : undefined;
			if (fallback) void navigate({ ...projectNavigateTarget(fallback.projectId, paneHost(fallback) ?? LOCAL_HOST), replace: true });
		},
		[navigate, showPaneRoute],
	);

	const keepsSplit = useCallback((sessionId: string | undefined, hostId?: HostId) => {
		const current = useSessionSplitStore.getState().split;
		const host = hostOf(hostId);
		// A pane on a computer whose list is not loaded still keeps the split on screen.
		if (current && sessionId && host && current.panes[sessionPaneKey({ kind: "session", sessionId, projectId: "", hostId: host })]) return true;
		return Boolean(paneKeyForSession(current, findRoutedSession(workspacesRef.current, { sessionId, hostId: host })?.session));
	}, []);

	const resize = useCallback((path: string, ratio: number) => useSessionSplitStore.getState().resize(path, ratio), []);
	// Panes stay in the order they were added, so moving one only moves its box.
	const paneOrder = Object.keys(split.panes).filter((key) => keys.includes(key));
	// The computer's name shows on a pane's tab once the panes come from different computers.
	const mixedHosts = new Set(paneOrder.map((key) => paneHost(split.panes[key]!) ?? LOCAL_HOST)).size > 1;

	return (
		<div
			className="session-split-view relative min-h-0 flex-1 overflow-hidden bg-background"
			data-session-split-view=""
			data-testid="session-split-view"
			ref={screenRef}
			style={layout.vars as CSSProperties}
		>
			{paneOrder.map((key) => {
				const box = boxes.get(key);
				const css = layout.leaves.get(key);
				const ref = split.panes[key]!;
				return (
					<SplitPane
						cloudOrgId={cloudOrgId}
						edges={{
							top: (box?.y ?? 0) < 1e-6,
							left: (box?.x ?? 0) < 1e-6,
							right: Math.abs((box?.x ?? 0) + (box?.w ?? 1) - 1) < 1e-6,
						}}
						focused={key === focusKey}
						key={key}
						keepsSplit={keepsSplit}
						onClose={closePane}
						paneKey={key}
						paneRef={ref}
						renderSession={renderSession}
						resolved={resolvePane(ref, workspaces, routedFor(ref))}
							showHost={mixedHosts}
						showFocusRing={keys.length > 1}
						style={css ? { position: "absolute", ...css } : undefined}
					/>
				);
			})}
			{layout.dividers.map((divider) => (
				<SplitDivider divider={divider} key={divider.path} onResize={resize} screenRef={screenRef} />
			))}
		</div>
	);
}

function SplitPane({
	paneKey,
	paneRef,
	resolved,
	focused,
	showFocusRing,
	showHost,
	edges,
	style,
	cloudOrgId,
	keepsSplit,
	onClose,
	renderSession,
}: {
	paneKey: string;
	paneRef: SessionPaneRef;
	resolved: ResolvedPane;
	focused: boolean;
	showFocusRing: boolean;
	showHost: boolean;
	edges: { top: boolean; left: boolean; right: boolean };
	style?: CSSProperties;
	cloudOrgId?: string;
	keepsSplit: (sessionId: string | undefined, hostId?: HostId) => boolean;
	onClose: (key: string) => void;
	renderSession: (props: SessionPaneViewProps) => ReactNode;
}) {
	const { t } = useTranslation();
	const rootRef = useRef<HTMLDivElement | null>(null);
	const pane = useMemo<SessionPaneValue>(() => ({ paneKey, focused, rootRef, keepsSplit }), [focused, keepsSplit, paneKey]);
	const session = resolved.kind === "session" || resolved.kind === "ended" ? resolved.session : undefined;
	const workspace = "workspace" in resolved ? resolved.workspace : undefined;
	const paneHostId = paneHost(paneRef);
	const hostLabel = useHostConnection(paneHostId).label ?? paneHostId;
	const projectName = workspace ? (workspace.id === STANDALONE_WORKSPACE_ID ? t("standalone.workspaceName") : workspace.name) : undefined;
	const paneName = session?.title ?? projectName ?? t("sessionSplit.pane");
	const close = useCallback(() => onClose(paneKey), [onClose, paneKey]);
	// The session view subscribes to its own data; a streamed list update must
	// not rebuild it from here.
	// A session on another computer renders even before its list loads: the
	// session view resolves it itself and shows that computer's offline state.
	const sessionId = resolved.kind === "session" ? resolved.session.id : resolved.kind === "remote" ? resolved.sessionId : undefined;
	const sessionProjectId = resolved.kind === "session" ? routeProjectId(resolved.workspace) : resolved.kind === "remote" ? resolved.projectId : undefined;
	const sessionView = useMemo(
		() => (sessionId ? renderSession({ sessionId, projectId: sessionProjectId, cloudOrgId, hostId: paneHostId }) : null),
		[cloudOrgId, paneHostId, renderSession, sessionId, sessionProjectId],
	);
	const chrome = useMemo(
		() => ({
			leading: <PaneLead hostLabel={showHost ? hostLabel ?? t("remote.thisComputer") : undefined} projectName={projectName} session={resolved.kind === "session" ? session : undefined} />,
			trailing: <ClosePaneButton label={t("sessionSplit.closePane", { name: paneName })} onClose={close} />,
		}),
		[close, hostLabel, paneName, projectName, resolved.kind, session, showHost, t],
	);
	return (
		<div
			aria-label={paneName}
			className="min-h-0 min-w-0 overflow-hidden bg-background"
			data-focused={focused || undefined}
			data-pane-left={edges.left || undefined}
			data-pane-right={edges.right || undefined}
			data-pane-top={edges.top || undefined}
			data-session-pane={paneKey}
			ref={rootRef}
			role="region"
			style={style}
		>
			<SessionPaneContext.Provider value={pane}>
				<SessionPaneChromeContext.Provider value={chrome}>
					<SessionTopbarProvider>
						{sessionId ? (
							<CatchBoundary errorComponent={LazyChunkErrorPanel} getResetKey={() => sessionId ?? ""}>
								<Suspense fallback={null}>{sessionView}</Suspense>
							</CatchBoundary>
						) : resolved.kind === "loading" || resolved.kind === "session" || resolved.kind === "remote" ? null : (
							<EmptyPane paneRef={paneRef} resolved={resolved} />
						)}
					</SessionTopbarProvider>
				</SessionPaneChromeContext.Provider>
			</SessionPaneContext.Provider>
			{focused && showFocusRing ? <div aria-hidden="true" className="session-pane-focus-ring" data-testid="session-pane-focus-ring" /> : null}
		</div>
	);
}

/** The pane header's lead: the session's status as its sidebar row shows it, and its project. */
function PaneLead({ session, projectName, hostLabel }: { session?: WorkspaceSession; projectName?: string; hostLabel?: string }) {
	return (
		<span className="session-pane-lead flex min-w-0 max-w-40 shrink-0 items-center gap-1 pl-2 pr-1.5 text-caption text-passive" data-testid="session-pane-lead">
			{session ? <SessionRowStatusGlyph session={session} /> : null}
			{projectName ? <span className="truncate">{projectName}</span> : null}
			{hostLabel ? <span className="truncate text-passive/70" data-testid="session-pane-host">{projectName ? `· ${hostLabel}` : hostLabel}</span> : null}
		</span>
	);
}

function ClosePaneButton({ label, onClose }: { label: string; onClose: () => void }) {
	const { t } = useTranslation();
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Button aria-label={label} data-pane-close="" data-testid="session-pane-close" onClick={onClose} size="icon-sm" type="button" variant="quiet">
					<X aria-hidden="true" className="size-icon-sm" />
				</Button>
			</TooltipTrigger>
			<TooltipContent side="bottom">{t("sessionSplit.close")}</TooltipContent>
		</Tooltip>
	);
}

/** A pane with no session to show: an orchestrator not running, or a session that ended or is gone. */
function EmptyPane({ paneRef, resolved }: { paneRef: SessionPaneRef; resolved: Exclude<ResolvedPane, { kind: "session" } | { kind: "remote" } | { kind: "loading" }> }) {
	const { t } = useTranslation();
	const title = resolved.kind === "ended" ? resolved.session.title : paneRef.kind === "orchestrator" ? t("shell.orchestrator") : "";
	return (
		<div className="flex h-full min-h-0 flex-col" data-pane-state={resolved.kind} data-testid="session-pane-empty">
			<SessionTabsTopbar tabs={<span className="flex min-w-0 items-center truncate px-1 text-control text-muted-foreground">{title}</span>} />
			<div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
				{resolved.kind === "no-orchestrator" ? (
					<NoOrchestrator workspace={resolved.workspace} />
				) : resolved.kind === "host-unavailable" ? (
					<p className="text-sm text-muted-foreground" role="alert">{t("remote.hostOffline")}</p>
				) : (
					<p className="text-sm text-muted-foreground">
						{resolved.kind === "ended" ? t("sessionSplit.ended") : t("sessionSplit.missing")}
					</p>
				)}
			</div>
		</div>
	);
}

function NoOrchestrator({ workspace }: { workspace: WorkspaceSummary }) {
	const { t } = useTranslation();
	const action = useProjectOrchestratorAction({ projectId: workspace.id, project: workspace, source: "sidebar" });
	// A guest cannot start an orchestrator in someone else's project.
	const canStart = !workspace.shared && !workspace.hostId;
	return (
		<>
			<p className="text-sm text-muted-foreground">{t("sessionSplit.noOrchestrator")}</p>
			{canStart ? (
				<Button
					disabled={action.isSpawning || action.isProjectRestarting || action.isProvisioning}
					onClick={() => action.openOrchestrator()}
					size="sm"
					type="button"
					variant="secondary"
				>
					{t("shell.spawnOrchestratorLower")}
				</Button>
			) : null}
			{action.spawnError ? <p className="max-w-80 text-xs text-error" role="alert">{action.spawnError}</p> : null}
		</>
	);
}

/**
 * Lets a session row dragged out of the sidebar land on the center: on an
 * edge of the session on screen, or of a pane of the split, it goes there.
 * Mounted wherever a local session is on screen.
 */
export function useSessionRowDropTarget(routedSessionId: string | undefined, enabled: boolean, routedHostId?: HostId): void {
	const workspaces = useSplitWorkspaces();
	const workspacesRef = useRef(workspaces);
	workspacesRef.current = workspaces;
	const { addBeside } = useSessionSplitActions(useCallback(() => workspacesRef.current, []));
	const stateRef = useRef({ workspaces, routed: { sessionId: routedSessionId, hostId: routedHostId }, addBeside });
	stateRef.current = { workspaces, routed: { sessionId: routedSessionId, hostId: routedHostId }, addBeside };
	useEffect(() => {
		if (!enabled) return;
		return registerSessionDropTarget({
			canDrag: (key) => {
				const { workspaces, routed } = stateRef.current;
				return Boolean(paneRefFromDragKey(key) && paneOnScreen(useSessionSplitStore.getState().split, workspaces, routed));
			},
			resolve: (key, x, y) => {
				const { workspaces, routed } = stateRef.current;
				const ref = paneRefFromDragKey(key);
				const split = useSessionSplitStore.getState().split;
				const anchor = paneOnScreen(split, workspaces, routed);
				if (!ref || !anchor) return null;
				const draggedKey = sessionPaneKey(ref);
				const splitScreen = document.querySelector<HTMLElement>("[data-session-split-view]");
				const shown = split && splitScreen && split.panes[sessionPaneKey(anchor)] ? split : null;
				const screen = shown ? splitScreen : document.getElementById("terminal");
				if (!screen) return null;
				const bounds = screen.getBoundingClientRect();
				const rect: ClientRect = { left: bounds.left, top: bounds.top, width: bounds.width, height: bounds.height };
				if (!containsPoint(rect, x, y)) return null;
				const panes = shown ? [...layoutBoxes(shown.root).leaves].map(([paneKey, box]) => ({ paneKey, rect: toClientRect(rect, box) })) : [{ paneKey: sessionPaneKey(anchor), rect }];
				const target = panes.find((candidate) => containsPoint(candidate.rect, x, y));
				if (!target || target.paneKey === draggedKey) return null;
				const targetRef = shown ? shown.panes[target.paneKey] : anchor;
				// A fifth pane has nowhere to go; the preview does not offer it.
				if (!targetRef || (shown && !shown.panes[draggedKey] && leafKeys(shown.root).length >= MAX_SPLIT_PANES)) return null;
				const edge = edgeAt(target.rect, x, y);
				if (!edge) return null;
				return { rect: halfOf(target.rect, edge), commit: () => stateRef.current.addBeside(targetRef, ref, edge) };
			},
		});
	}, [enabled]);
}
