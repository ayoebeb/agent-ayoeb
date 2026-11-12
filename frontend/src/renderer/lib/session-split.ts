import { insertBeside, leafKeys, parseNode, removeLeaf, setRatioAt, type SplitNode, type SplitSide } from "./pane-split";
import { isOrchestratorSession, sessionIsActive, type WorkspaceSession, type WorkspaceSummary } from "../types/workspace";

/**
 * Sessions side by side. Two to four panes share the center, each showing a
 * whole session as it normally renders. A pane names a session, or a
 * project's orchestrator, which resolves to whichever orchestrator that
 * project has running. The layout is the same tree of splits the in-session
 * split uses (lib/pane-split), with one pane key in each leaf.
 */
export type SessionPaneRef =
	| { kind: "session"; sessionId: string; projectId: string }
	| { kind: "orchestrator"; projectId: string };

export type SessionSplit = {
	root: SplitNode;
	panes: Record<string, SessionPaneRef>;
	/** The pane that takes keyboard input. */
	focus?: string;
};

export const MIN_SPLIT_PANES = 2;
export const MAX_SPLIT_PANES = 4;

export function sessionPaneKey(ref: SessionPaneRef): string {
	return ref.kind === "session" ? `session:${ref.sessionId}` : `orchestrator:${ref.projectId}`;
}

/** Side by side for two or three, a two-by-two grid for four. */
export function splitLayout(keys: readonly string[]): SplitNode | null {
	const [a, b, c, d] = keys.map((key): SplitNode => ({ type: "leaf", key }));
	if (keys.length === 2) return { type: "split", dir: "row", ratio: 0.5, a, b };
	if (keys.length === 3) return { type: "split", dir: "row", ratio: 1 / 3, a, b: { type: "split", dir: "row", ratio: 0.5, a: b, b: c } };
	if (keys.length === 4) {
		return {
			type: "split",
			dir: "column",
			ratio: 0.5,
			a: { type: "split", dir: "row", ratio: 0.5, a, b },
			b: { type: "split", dir: "row", ratio: 0.5, a: c, b: d },
		};
	}
	return null;
}

/**
 * A split of the given panes, in order, or null when fewer than two distinct
 * panes remain. Callers cap the count first; more than four are refused.
 */
export function openSplit(refs: readonly SessionPaneRef[], focus?: string): SessionSplit | null {
	const panes: Record<string, SessionPaneRef> = {};
	for (const ref of refs) panes[sessionPaneKey(ref)] ??= ref;
	const keys = Object.keys(panes);
	if (keys.length > MAX_SPLIT_PANES) return null;
	const root = splitLayout(keys);
	if (!root) return null;
	return { root, panes, focus: focus && panes[focus] ? focus : keys[0] };
}

export type SplitBesideResult = { split: SessionSplit | null; outcome: "split" | "full" | "unchanged" };

/**
 * Shows `ref` on `side` of `anchor`, focused. Without a split yet, the anchor
 * is the session on screen and the two form one. A pane already in the split
 * moves; a fifth pane is refused. With `sessionOf`, a pane already showing the
 * session `ref` resolves to counts as `ref`, so one session never fills two
 * panes (an orchestrator's own row beside its project's orchestrator pane).
 */
export function splitPaneBeside(
	split: SessionSplit | null,
	anchor: SessionPaneRef,
	dragged: SessionPaneRef,
	side: SplitSide,
	sessionOf?: (ref: SessionPaneRef) => string | undefined,
): SplitBesideResult {
	const anchorKey = sessionPaneKey(anchor);
	const current = split && split.panes[anchorKey] ? split : null;
	const ref = paneShowingSameSession(current ? Object.values(current.panes) : [anchor], dragged, sessionOf) ?? dragged;
	const key = sessionPaneKey(ref);
	if (anchorKey === key) return { split, outcome: "unchanged" };
	if (!current) {
		return {
			split: { root: insertBeside({ type: "leaf", key: anchorKey }, anchorKey, key, side), panes: { [anchorKey]: anchor, [key]: ref }, focus: key },
			outcome: "split",
		};
	}
	const moving = Boolean(current.panes[key]);
	if (!moving && leafKeys(current.root).length >= MAX_SPLIT_PANES) return { split, outcome: "full" };
	const base = moving ? removeLeaf(current.root, key) ?? current.root : current.root;
	return {
		split: { root: insertBeside(base, anchorKey, key, side), panes: { ...current.panes, [key]: ref }, focus: key },
		outcome: "split",
	};
}

function paneShowingSameSession(
	panes: readonly SessionPaneRef[],
	ref: SessionPaneRef,
	sessionOf: ((ref: SessionPaneRef) => string | undefined) | undefined,
): SessionPaneRef | undefined {
	const sessionId = sessionOf?.(ref);
	if (!sessionId) return undefined;
	const key = sessionPaneKey(ref);
	return panes.find((pane) => sessionPaneKey(pane) !== key && sessionOf?.(pane) === sessionId);
}

/** The split without one pane; one pane left is no split. */
export function closeSplitPane(split: SessionSplit, key: string): SessionSplit | null {
	if (!split.panes[key]) return split;
	const root = removeLeaf(split.root, key);
	if (!root || root.type === "leaf") return null;
	const { [key]: _removed, ...panes } = split.panes;
	const keys = leafKeys(root);
	return { root, panes, focus: split.focus && split.focus !== key ? split.focus : keys[0] };
}

export function resizeSplit(split: SessionSplit, path: string, ratio: number): SessionSplit {
	const root = setRatioAt(split.root, path, ratio);
	return root === split.root ? split : { ...split, root };
}

export function focusSplitPane(split: SessionSplit, key: string): SessionSplit {
	return split.focus === key || !split.panes[key] ? split : { ...split, focus: key };
}

/**
 * The pane a routed session belongs to: its own pane, or for an orchestrator,
 * its project's orchestrator pane. The split shows while the route points
 * into it.
 */
export function paneKeyForSession(
	split: SessionSplit | null,
	session: Pick<WorkspaceSession, "id" | "kind" | "workspaceId"> | undefined,
): string | undefined {
	if (!split || !session) return undefined;
	const own = sessionPaneKey({ kind: "session", sessionId: session.id, projectId: session.workspaceId });
	if (split.panes[own]) return own;
	if (!isOrchestratorSession(session)) return undefined;
	const orchestrator = sessionPaneKey({ kind: "orchestrator", projectId: session.workspaceId });
	return split.panes[orchestrator] ? orchestrator : undefined;
}

function timestamp(value: string | undefined): number {
	const parsed = value ? Date.parse(value) : Number.NaN;
	return Number.isNaN(parsed) ? 0 : parsed;
}

function activityTime(session: WorkspaceSession): number {
	return Math.max(timestamp(session.activity?.lastActivityAt), timestamp(session.lastInteractionAt), timestamp(session.updatedAt));
}

/**
 * The orchestrator a project's pane shows: of the ones still running, the one
 * that did something most recently; the newest one breaks a tie.
 */
export function mostRecentlyActiveOrchestrator(sessions: readonly WorkspaceSession[]): WorkspaceSession | undefined {
	let best: WorkspaceSession | undefined;
	for (const session of sessions) {
		if (!isOrchestratorSession(session) || !sessionIsActive(session)) continue;
		if (!best) {
			best = session;
			continue;
		}
		const delta = activityTime(session) - activityTime(best);
		const created = timestamp(session.createdAt) - timestamp(best.createdAt);
		if (delta > 0 || (delta === 0 && (created > 0 || (created === 0 && session.id > best.id)))) best = session;
	}
	return best;
}

export type ResolvedPane =
	| { kind: "loading" }
	| { kind: "session"; session: WorkspaceSession; workspace: WorkspaceSummary }
	| { kind: "ended"; session: WorkspaceSession; workspace: WorkspaceSummary }
	| { kind: "missing" }
	| { kind: "no-orchestrator"; workspace: WorkspaceSummary };

/**
 * What a pane shows now. A running orchestrator the route points at wins over
 * the project's most recent one, so choosing one orchestrator of several keeps it.
 */
export function resolvePane(
	ref: SessionPaneRef,
	workspaces: readonly WorkspaceSummary[] | undefined,
	routedSessionId?: string,
): ResolvedPane {
	if (!workspaces) return { kind: "loading" };
	const workspace = workspaces.find((candidate) => !candidate.hostId && candidate.id === ref.projectId);
	if (ref.kind === "session") {
		const session = workspace?.sessions.find((candidate) => candidate.id === ref.sessionId);
		if (!workspace || !session) return { kind: "missing" };
		return sessionIsActive(session) ? { kind: "session", session, workspace } : { kind: "ended", session, workspace };
	}
	if (!workspace) return { kind: "missing" };
	const routed = workspace.sessions.find(
		(candidate) => candidate.id === routedSessionId && isOrchestratorSession(candidate) && sessionIsActive(candidate),
	);
	const orchestrator = routed ?? mostRecentlyActiveOrchestrator(workspace.sessions);
	return orchestrator ? { kind: "session", session: orchestrator, workspace } : { kind: "no-orchestrator", workspace };
}

function parseRef(value: unknown): SessionPaneRef | null {
	if (!value || typeof value !== "object") return null;
	const ref = value as Record<string, unknown>;
	if (typeof ref.projectId !== "string" || !ref.projectId) return null;
	if (ref.kind === "orchestrator") return { kind: "orchestrator", projectId: ref.projectId };
	if (ref.kind === "session" && typeof ref.sessionId === "string" && ref.sessionId) {
		return { kind: "session", sessionId: ref.sessionId, projectId: ref.projectId };
	}
	return null;
}

/** A pane as the key of a sidebar row being dragged onto the center. */
export function paneDragKey(ref: SessionPaneRef): string {
	return `pane:${JSON.stringify(ref)}`;
}

export function paneRefFromDragKey(key: string): SessionPaneRef | null {
	if (!key.startsWith("pane:")) return null;
	try {
		return parseRef(JSON.parse(key.slice("pane:".length)));
	} catch {
		return null;
	}
}

/** A remembered split, keeping only panes that are well formed; two or more, or none. */
export function parseStoredSplit(raw: string | null | undefined): SessionSplit | null {
	if (!raw) return null;
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return null;
	}
	if (!parsed || typeof parsed !== "object") return null;
	const stored = parsed as Record<string, unknown>;
	const storedPanes = stored.panes && typeof stored.panes === "object" ? (stored.panes as Record<string, unknown>) : {};
	const panes: Record<string, SessionPaneRef> = {};
	for (const [key, value] of Object.entries(storedPanes)) {
		const ref = parseRef(value);
		if (ref && sessionPaneKey(ref) === key) panes[key] = ref;
	}
	const seen = new Set<string>();
	let root = parseNode(stored.root, (key) => !panes[key]);
	for (const key of root ? leafKeys(root) : []) {
		if (seen.has(key) && root) root = removeLeaf(root, key);
		seen.add(key);
	}
	if (!root || root.type !== "split") return null;
	const keys = leafKeys(root);
	if (keys.length > MAX_SPLIT_PANES) return null;
	const kept = Object.fromEntries(keys.map((key) => [key, panes[key]!]));
	const focus = typeof stored.focus === "string" && kept[stored.focus] ? stored.focus : keys[0];
	return { root, panes: kept, focus };
}

/*
 * Sidebar multi-selection. Ctrl/Cmd-click toggles a row, Shift-click selects
 * the rows between the anchor and the clicked one as shown on screen. Session
 * and project selections never mix: starting one clears the other. The row
 * already open seeds a new selection, so Ctrl-clicking a second session
 * selects both.
 */
export type SelectionKind = "session" | "project";
export type SidebarSelection = { kind: SelectionKind; keys: string[]; anchor: string };
export type SelectionChange = { selection: SidebarSelection | null; overflow: boolean };

function baseKeys(current: SidebarSelection | null, kind: SelectionKind, key: string, seed?: string): string[] {
	if (current?.kind === kind) return current.keys;
	return seed && seed !== key ? [seed] : [];
}

export function toggleSelection(
	current: SidebarSelection | null,
	kind: SelectionKind,
	key: string,
	seed?: string,
): SelectionChange {
	const keys = baseKeys(current, kind, key, seed);
	if (keys.includes(key)) {
		const remaining = keys.filter((candidate) => candidate !== key);
		return { selection: remaining.length ? { kind, keys: remaining, anchor: key } : null, overflow: false };
	}
	if (keys.length >= MAX_SPLIT_PANES) return { selection: current, overflow: true };
	return { selection: { kind, keys: [...keys, key], anchor: key }, overflow: false };
}

export function selectRange(
	current: SidebarSelection | null,
	kind: SelectionKind,
	ordered: readonly string[],
	key: string,
	seed?: string,
): SelectionChange {
	const preferred = current?.kind === kind ? current.anchor : seed;
	const anchor = preferred && ordered.includes(preferred) ? preferred : key;
	const from = ordered.indexOf(anchor);
	const to = ordered.indexOf(key);
	if (to < 0) return toggleSelection(current, kind, key, seed);
	const keys = ordered.slice(Math.min(from, to), Math.max(from, to) + 1);
	if (keys.length > MAX_SPLIT_PANES) return { selection: current, overflow: true };
	return { selection: { kind, keys, anchor }, overflow: false };
}
