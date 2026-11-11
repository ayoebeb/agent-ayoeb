import { useCallback, useEffect, useMemo, useRef, type CSSProperties, type ReactNode } from "react";
import { CatchBoundary, useNavigate } from "@tanstack/react-router";
import { Suspense } from "react";
import { useTranslation } from "react-i18next";
import { X } from "@/components/app-icons";
import { useProjectOrchestratorAction } from "../hooks/useProjectOrchestratorAction";
import { findLocalSession, paneOnScreen, useSessionSplitActions } from "../hooks/useSessionSplitActions";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { projectNavigateTarget, sessionNavigateTarget } from "../lib/navigate-to-session";
import { registerSessionDropTarget } from "../lib/pane-drag";
import { containsPoint, edgeAt, halfOf, layoutBoxes, layoutCss, leafKeys, toClientRect, type ClientRect } from "../lib/pane-split";
import {
	closeSplitPane,
	MAX_SPLIT_PANES,
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

export type SessionPaneViewProps = { sessionId: string; projectId?: string; cloudOrgId?: string };

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
	routedPaneKey,
	cloudOrgId,
	renderSession,
}: {
	split: SessionSplit;
	routedSessionId: string;
	routedPaneKey: string;
	cloudOrgId?: string;
	renderSession: (props: SessionPaneViewProps) => ReactNode;
}) {
	const navigate = useNavigate();
	const workspaceQuery = useWorkspaceQuery();
	const workspaces = workspaceQuery.data;
	const workspacesRef = useRef(workspaces);
	workspacesRef.current = workspaces;
	const screenRef = useRef<HTMLDivElement | null>(null);
	const layout = useMemo(() => layoutCss(split.root), [split.root]);
	const boxes = useMemo(() => layoutBoxes(split.root).leaves, [split.root]);
	const keys = useMemo(() => leafKeys(split.root), [split.root]);
	const focusKey = split.focus && split.panes[split.focus] ? split.focus : routedPaneKey;
	const routedRef = useRef({ sessionId: routedSessionId, paneKey: routedPaneKey });
	routedRef.current = { sessionId: routedSessionId, paneKey: routedPaneKey };

	// The route names the focused pane; following a link or the sidebar to
	// another pane's session focuses that pane.
	useEffect(() => {
		useSessionSplitStore.getState().focus(routedPaneKey);
	}, [routedPaneKey]);

	const showPaneRoute = useCallback(
		(pane: ResolvedPane, replace: boolean) => {
			if (pane.kind !== "session" && pane.kind !== "ended") return;
			if (pane.session.id === routedRef.current.sessionId) return;
			void navigate({ ...sessionNavigateTarget(pane.workspace.id, pane.session.id), replace });
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
			const pane = resolvePane(ref, workspacesRef.current, routedRef.current.sessionId);
			if (pane.kind === "session") showPaneRoute(pane, true);
		},
		[showPaneRoute],
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
			const nextKey = remaining?.focus ?? leafKeys(current.root).find((candidate) => candidate !== key);
			// The route may already be on its way to the focused pane.
			const wasShown = routedRef.current.paneKey === key || current.focus === key;
			state.close(key);
			if (!wasShown || !nextKey) return;
			const ref = current.panes[nextKey];
			if (!ref) return;
			const pane = resolvePane(ref, workspacesRef.current);
			if (pane.kind === "session" || pane.kind === "ended") showPaneRoute(pane, true);
			else void navigate({ ...projectNavigateTarget(ref.projectId), replace: true });
		},
		[navigate, showPaneRoute],
	);

	const keepsSplit = useCallback((sessionId: string | undefined) => {
		const current = useSessionSplitStore.getState().split;
		return Boolean(paneKeyForSession(current, findLocalSession(workspacesRef.current, sessionId)?.session));
	}, []);

	const resize = useCallback((path: string, ratio: number) => useSessionSplitStore.getState().resize(path, ratio), []);
	// Panes stay in the order they were added, so moving one only moves its box.
	const paneOrder = Object.keys(split.panes).filter((key) => keys.includes(key));

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
						resolved={resolvePane(ref, workspaces, routedSessionId)}
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
	edges: { top: boolean; left: boolean; right: boolean };
	style?: CSSProperties;
	cloudOrgId?: string;
	keepsSplit: (sessionId: string | undefined) => boolean;
	onClose: (key: string) => void;
	renderSession: (props: SessionPaneViewProps) => ReactNode;
}) {
	const { t } = useTranslation();
	const rootRef = useRef<HTMLDivElement | null>(null);
	const pane = useMemo<SessionPaneValue>(() => ({ paneKey, focused, rootRef, keepsSplit }), [focused, keepsSplit, paneKey]);
	const session = resolved.kind === "session" || resolved.kind === "ended" ? resolved.session : undefined;
	const workspace = resolved.kind === "loading" || resolved.kind === "missing" ? undefined : resolved.workspace;
	const projectName = workspace ? (workspace.id === STANDALONE_WORKSPACE_ID ? t("standalone.workspaceName") : workspace.name) : undefined;
	const paneName = session?.title ?? projectName ?? t("sessionSplit.pane");
	const close = useCallback(() => onClose(paneKey), [onClose, paneKey]);
	// The session view subscribes to its own data; a streamed list update must
	// not rebuild it from here.
	const sessionId = resolved.kind === "session" ? resolved.session.id : undefined;
	const sessionProjectId = resolved.kind === "session" ? routeProjectId(resolved.workspace) : undefined;
	const sessionView = useMemo(
		() => (sessionId ? renderSession({ sessionId, projectId: sessionProjectId, cloudOrgId }) : null),
		[cloudOrgId, renderSession, sessionId, sessionProjectId],
	);
	const chrome = useMemo(
		() => ({
			leading: <PaneLead projectName={projectName} session={resolved.kind === "session" ? session : undefined} />,
			trailing: <ClosePaneButton label={t("sessionSplit.closePane", { name: paneName })} onClose={close} />,
		}),
		[close, paneName, projectName, resolved.kind, session, t],
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
						{resolved.kind === "session" ? (
							<CatchBoundary errorComponent={LazyChunkErrorPanel} getResetKey={() => sessionId ?? ""}>
								<Suspense fallback={null}>{sessionView}</Suspense>
							</CatchBoundary>
						) : resolved.kind === "loading" ? null : (
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
function PaneLead({ session, projectName }: { session?: WorkspaceSession; projectName?: string }) {
	return (
		<span className="session-pane-lead flex min-w-0 max-w-40 shrink-0 items-center gap-1 pl-2 pr-1.5 text-caption text-passive" data-testid="session-pane-lead">
			{session ? <SessionRowStatusGlyph session={session} /> : null}
			{projectName ? <span className="truncate">{projectName}</span> : null}
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
function EmptyPane({ paneRef, resolved }: { paneRef: SessionPaneRef; resolved: Exclude<ResolvedPane, { kind: "session" } | { kind: "loading" }> }) {
	const { t } = useTranslation();
	const title = resolved.kind === "ended" ? resolved.session.title : paneRef.kind === "orchestrator" ? t("shell.orchestrator") : "";
	return (
		<div className="flex h-full min-h-0 flex-col" data-pane-state={resolved.kind} data-testid="session-pane-empty">
			<SessionTabsTopbar tabs={<span className="flex min-w-0 items-center truncate px-1 text-control text-muted-foreground">{title}</span>} />
			<div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
				{resolved.kind === "no-orchestrator" ? (
					<NoOrchestrator workspace={resolved.workspace} />
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
	const canStart = !workspace.shared;
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
export function useSessionRowDropTarget(routedSessionId: string | undefined, enabled: boolean): void {
	const workspaceQuery = useWorkspaceQuery();
	const workspacesRef = useRef(workspaceQuery.data);
	workspacesRef.current = workspaceQuery.data;
	const { addBeside } = useSessionSplitActions(useCallback(() => workspacesRef.current, []));
	const stateRef = useRef({ workspaces: workspaceQuery.data, routedSessionId, addBeside });
	stateRef.current = { workspaces: workspaceQuery.data, routedSessionId, addBeside };
	useEffect(() => {
		if (!enabled) return;
		return registerSessionDropTarget({
			canDrag: (key) => {
				const { workspaces, routedSessionId: routed } = stateRef.current;
				return Boolean(paneRefFromDragKey(key) && paneOnScreen(useSessionSplitStore.getState().split, workspaces, routed));
			},
			resolve: (key, x, y) => {
				const { workspaces, routedSessionId: routed } = stateRef.current;
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
