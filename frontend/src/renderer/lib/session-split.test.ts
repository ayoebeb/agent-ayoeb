import { describe, expect, it } from "vitest";
import { leafKeys } from "./pane-split";
import {
	closeSplitPane,
	MAX_SPLIT_PANES,
	mostRecentlyActiveOrchestrator,
	openSplit,
	paneDragKey,
	paneKeyForSession,
	paneRefFromDragKey,
	parseStoredSplit,
	resizeSplit,
	resolvePane,
	selectRange,
	sessionPaneKey,
	splitLayout,
	splitPaneBeside,
	toggleSelection,
	type SessionPaneRef,
} from "./session-split";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";

const a: SessionPaneRef = { kind: "session", sessionId: "a", projectId: "p1" };
const b: SessionPaneRef = { kind: "session", sessionId: "b", projectId: "p1" };
const c: SessionPaneRef = { kind: "session", sessionId: "c", projectId: "p2" };
const d: SessionPaneRef = { kind: "session", sessionId: "d", projectId: "p2" };
const e: SessionPaneRef = { kind: "session", sessionId: "e", projectId: "p2" };
const yoobi: SessionPaneRef = { kind: "orchestrator", projectId: "yoobi" };

function session(overrides: Partial<WorkspaceSession> & { id: string }): WorkspaceSession {
	return { workspaceId: "p1", title: overrides.id, updatedAt: "2026-11-10T10:00:00Z", kind: "worker", ...overrides } as WorkspaceSession;
}

function workspace(id: string, sessions: WorkspaceSession[]): WorkspaceSummary {
	return { id, name: id, kind: "local", path: `/code/${id}`, type: "main", sessions } as unknown as WorkspaceSummary;
}

describe("split layouts", () => {
	it("puts two and three panes in a row and four in a grid", () => {
		expect(splitLayout(["x"])).toBeNull();
		expect(splitLayout(["x", "y"])).toMatchObject({ dir: "row", ratio: 0.5 });
		const three = splitLayout(["x", "y", "z"])!;
		expect(leafKeys(three)).toEqual(["x", "y", "z"]);
		expect(three).toMatchObject({ dir: "row", ratio: 1 / 3, b: { dir: "row" } });
		const four = splitLayout(["w", "x", "y", "z"])!;
		expect(leafKeys(four)).toEqual(["w", "x", "y", "z"]);
		expect(four).toMatchObject({ dir: "column", a: { dir: "row" }, b: { dir: "row" } });
		expect(splitLayout(["v", "w", "x", "y", "z"])).toBeNull();
	});
});

describe("opening and closing panes", () => {
	it("opens two to four distinct panes, focused on the one asked for", () => {
		expect(openSplit([a])).toBeNull();
		expect(openSplit([a, a])).toBeNull();
		expect(openSplit([a, b, c, d, e])).toBeNull();
		const split = openSplit([a, b, yoobi], sessionPaneKey(b))!;
		expect(leafKeys(split.root)).toEqual(["session:a", "session:b", "orchestrator:yoobi"]);
		expect(split.focus).toBe("session:b");
		expect(openSplit([a, b], "session:zzz")!.focus).toBe("session:a");
	});

	it("closing down to one pane leaves no split", () => {
		const split = openSplit([a, b, c], "session:b")!;
		const two = closeSplitPane(split, "session:b")!;
		expect(leafKeys(two.root)).toEqual(["session:a", "session:c"]);
		expect(two.panes["session:b"]).toBeUndefined();
		expect(two.focus).toBe("session:a");
		expect(closeSplitPane(two, "session:a")).toBeNull();
		expect(closeSplitPane(two, "session:nope")).toBe(two);
	});

	it("splits beside the session on screen, moves a member and refuses a fifth pane", () => {
		const first = splitPaneBeside(null, a, b, "right");
		expect(first.outcome).toBe("split");
		expect(leafKeys(first.split!.root)).toEqual(["session:a", "session:b"]);
		expect(first.split!.focus).toBe("session:b");

		const below = splitPaneBeside(first.split, b, c, "bottom").split!;
		expect(below.root).toMatchObject({ dir: "row", b: { dir: "column" } });

		// b moves to the left of a; nothing is duplicated.
		const moved = splitPaneBeside(below, a, b, "left").split!;
		expect(leafKeys(moved.root).sort()).toEqual(["session:a", "session:b", "session:c"]);
		expect(leafKeys(moved.root)[0]).toBe("session:b");

		const full = splitPaneBeside(below, a, d, "right").split!;
		expect(splitPaneBeside(full, a, e, "right")).toEqual({ split: full, outcome: "full" });
		expect(splitPaneBeside(full, a, a, "right").outcome).toBe("unchanged");
		// A session not in the split starts a new one with the anchor.
		expect(leafKeys(splitPaneBeside(full, e, a, "left").split!.root)).toEqual(["session:a", "session:e"]);
	});

	it("never shows one session in two panes: an orchestrator's own row moves its project's pane", () => {
		const orchestratorRow: SessionPaneRef = { kind: "session", sessionId: "orch-1", projectId: "yoobi" };
		const sessionOf = (ref: SessionPaneRef) => (ref.kind === "orchestrator" ? "orch-1" : ref.sessionId);

		// Beside the project's orchestrator pane, which already shows it: nothing to add.
		const split = openSplit([a, yoobi], sessionPaneKey(a))!;
		expect(splitPaneBeside(split, yoobi, orchestratorRow, "right", sessionOf).outcome).toBe("unchanged");

		// Beside another pane, the orchestrator pane moves there instead of a copy opening.
		const moved = splitPaneBeside(split, a, orchestratorRow, "left", sessionOf);
		expect(moved.outcome).toBe("split");
		expect(leafKeys(moved.split!.root)).toEqual(["orchestrator:yoobi", "session:a"]);
		expect(Object.keys(moved.split!.panes).sort()).toEqual(["orchestrator:yoobi", "session:a"]);
		expect(moved.split!.focus).toBe("orchestrator:yoobi");

		// Without a split yet, the session on screen is the anchor it is compared with.
		expect(splitPaneBeside(null, orchestratorRow, yoobi, "right", sessionOf).outcome).toBe("unchanged");
		// Without a resolver, panes are compared by what they name, as before.
		expect(Object.keys(splitPaneBeside(split, a, orchestratorRow, "left").split!.panes)).toHaveLength(3);
	});

	it("resizes one split and leaves the rest alone", () => {
		const split = openSplit([a, b])!;
		const resized = resizeSplit(split, "r", 0.3);
		expect(resized.root).toMatchObject({ ratio: 0.3 });
		expect(resizeSplit(resized, "r", 0.3)).toBe(resized);
	});
});

describe("which pane a route shows", () => {
	it("matches a session pane by id and an orchestrator pane by project", () => {
		const split = openSplit([a, yoobi])!;
		expect(paneKeyForSession(split, session({ id: "a" }))).toBe("session:a");
		expect(paneKeyForSession(split, session({ id: "orc-2", kind: "orchestrator", workspaceId: "yoobi" }))).toBe("orchestrator:yoobi");
		expect(paneKeyForSession(split, session({ id: "w-1", workspaceId: "yoobi" }))).toBeUndefined();
		expect(paneKeyForSession(null, session({ id: "a" }))).toBeUndefined();
	});

	it("resolves an orchestrator pane to the running orchestrator that was active last", () => {
		const older = session({ id: "orc-1", kind: "orchestrator", workspaceId: "yoobi", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-10T12:00:00Z" });
		const newer = session({ id: "orc-2", kind: "orchestrator", workspaceId: "yoobi", createdAt: "2026-11-09T00:00:00Z", updatedAt: "2026-11-10T09:00:00Z" });
		const dead = session({ id: "orc-3", kind: "orchestrator", workspaceId: "yoobi", isTerminated: true, updatedAt: "2026-11-10T13:00:00Z" });
		const worker = session({ id: "w-1", workspaceId: "yoobi", updatedAt: "2026-11-10T14:00:00Z" });
		expect(mostRecentlyActiveOrchestrator([newer, older, dead, worker])?.id).toBe("orc-1");
		// The agent's own activity counts, not only the row's update time.
		const busy = { ...newer, activity: { state: "active", lastActivityAt: "2026-11-10T12:30:00Z" } } as WorkspaceSession;
		expect(mostRecentlyActiveOrchestrator([older, busy])?.id).toBe("orc-2");
		// A tie goes to the newer one.
		expect(mostRecentlyActiveOrchestrator([{ ...older, updatedAt: newer.updatedAt }, newer])?.id).toBe("orc-2");
		expect(mostRecentlyActiveOrchestrator([dead, worker])).toBeUndefined();

		const workspaces = [workspace("yoobi", [older, newer])];
		expect(resolvePane(yoobi, workspaces)).toMatchObject({ kind: "session", session: { id: "orc-1" } });
		// The orchestrator the route points at stays, while it runs.
		expect(resolvePane(yoobi, workspaces, "orc-2")).toMatchObject({ kind: "session", session: { id: "orc-2" } });
	});

	it("shows an empty state when there is nothing running to show", () => {
		const workspaces = [
			workspace("yoobi", [session({ id: "w-1", workspaceId: "yoobi" })]),
			workspace("p1", [session({ id: "a" }), session({ id: "b", isTerminated: true })]),
		];
		expect(resolvePane(yoobi, undefined)).toEqual({ kind: "loading" });
		expect(resolvePane(yoobi, workspaces)).toMatchObject({ kind: "no-orchestrator", workspace: { id: "yoobi" } });
		expect(resolvePane(a, workspaces)).toMatchObject({ kind: "session", session: { id: "a" } });
		expect(resolvePane(b, workspaces)).toMatchObject({ kind: "ended", session: { id: "b" } });
		expect(resolvePane(c, workspaces)).toEqual({ kind: "missing" });
		expect(resolvePane({ kind: "orchestrator", projectId: "gone" }, workspaces)).toEqual({ kind: "missing" });
	});
});

describe("remembered splits", () => {
	it("round-trips a split and drops what does not parse", () => {
		const split = resizeSplit(openSplit([a, yoobi, c], "orchestrator:yoobi")!, "r", 0.4);
		expect(parseStoredSplit(JSON.stringify(split))).toEqual(split);
		expect(parseStoredSplit(null)).toBeNull();
		expect(parseStoredSplit("{nope")).toBeNull();
		expect(parseStoredSplit(JSON.stringify({ root: { type: "leaf", key: "session:a" }, panes: { "session:a": a } }))).toBeNull();
	});

	it("keeps only panes it can name, and needs two of them", () => {
		const split = openSplit([a, b, c])!;
		const broken = { ...split, panes: { ...split.panes, "session:b": { kind: "session", projectId: "p1" } }, focus: "session:b" };
		const parsed = parseStoredSplit(JSON.stringify(broken))!;
		expect(leafKeys(parsed.root)).toEqual(["session:a", "session:c"]);
		expect(parsed.focus).toBe("session:a");
		const mismatched = { ...openSplit([a, b])!, panes: { "session:a": a, "session:b": c } };
		expect(parseStoredSplit(JSON.stringify(mismatched))).toBeNull();
	});

	it("carries a dragged row's pane in its drag key", () => {
		expect(paneRefFromDragKey(paneDragKey(yoobi))).toEqual(yoobi);
		expect(paneRefFromDragKey(paneDragKey(a))).toEqual(a);
		expect(paneRefFromDragKey("session")).toBeNull();
		expect(paneRefFromDragKey("pane:{bad")).toBeNull();
	});
});

describe("sidebar selection", () => {
	it("Ctrl-click adds the open row and the clicked one, then toggles", () => {
		const first = toggleSelection(null, "session", "session:b", "session:a");
		expect(first).toEqual({ selection: { kind: "session", keys: ["session:a", "session:b"], anchor: "session:b" }, overflow: false });
		const off = toggleSelection(first.selection, "session", "session:a");
		expect(off.selection?.keys).toEqual(["session:b"]);
		expect(toggleSelection(off.selection, "session", "session:b").selection).toBeNull();
		// Without an open row, the selection starts with the clicked one alone.
		expect(toggleSelection(null, "session", "session:b").selection?.keys).toEqual(["session:b"]);
	});

	it("keeps sessions and projects apart: starting one clears the other", () => {
		const sessions = toggleSelection(null, "session", "session:b", "session:a").selection;
		const projects = toggleSelection(sessions, "project", "orchestrator:yoobi");
		expect(projects.selection).toEqual({ kind: "project", keys: ["orchestrator:yoobi"], anchor: "orchestrator:yoobi" });
		const range = selectRange(projects.selection, "session", ["session:a", "session:b"], "session:b");
		expect(range.selection?.keys).toEqual(["session:b"]);
	});

	it("refuses a fifth row instead of dropping one", () => {
		let selection = null as ReturnType<typeof toggleSelection>["selection"];
		for (const key of ["s1", "s2", "s3", "s4"]) selection = toggleSelection(selection, "session", key).selection;
		expect(selection?.keys).toHaveLength(MAX_SPLIT_PANES);
		const fifth = toggleSelection(selection, "session", "s5");
		expect(fifth).toEqual({ selection, overflow: true });
	});

	it("Shift-click selects the rows between the anchor and the clicked row as listed", () => {
		const ordered = ["s1", "s2", "s3", "s4", "s5", "s6"];
		const anchored = toggleSelection(null, "session", "s2").selection;
		expect(selectRange(anchored, "session", ordered, "s4").selection).toEqual({ kind: "session", keys: ["s2", "s3", "s4"], anchor: "s2" });
		// Upwards from the anchor, too; the anchor stays for the next Shift-click.
		const up = selectRange(anchored, "session", ordered, "s1").selection;
		expect(up).toEqual({ kind: "session", keys: ["s1", "s2"], anchor: "s2" });
		// The open row anchors a first Shift-click.
		expect(selectRange(null, "session", ordered, "s3", "s1").selection?.keys).toEqual(["s1", "s2", "s3"]);
		expect(selectRange(anchored, "session", ordered, "s6")).toEqual({ selection: anchored, overflow: true });
		// A row not on screen is toggled instead.
		expect(selectRange(anchored, "session", ordered, "hidden").selection?.keys).toEqual(["s2", "hidden"]);
	});
});
