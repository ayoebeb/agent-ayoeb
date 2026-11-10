import { beforeEach, describe, expect, it } from "vitest";
import type { SessionPaneRef } from "../lib/session-split";
import { selectedPaneRefs, useSidebarSelectionStore } from "./sidebar-selection-store";

const ref = (sessionId: string): SessionPaneRef => ({ kind: "session", sessionId, projectId: "p1" });
const project = (projectId: string): SessionPaneRef => ({ kind: "orchestrator", projectId });
const key = (pane: SessionPaneRef) => (pane.kind === "session" ? `session:${pane.sessionId}` : `orchestrator:${pane.projectId}`);

describe("sidebar selection store", () => {
	beforeEach(() => useSidebarSelectionStore.setState({ selection: null, refs: {} }));

	it("Ctrl-click selects the open session and the clicked one, in order", () => {
		const store = useSidebarSelectionStore.getState();
		expect(store.select({ kind: "session", key: "session:b", ref: ref("b"), seed: { key: "session:a", ref: ref("a") } })).toBe(true);
		expect(selectedPaneRefs(useSidebarSelectionStore.getState())).toEqual([ref("a"), ref("b")]);
		store.select({ kind: "session", key: "session:a", ref: ref("a") });
		expect(selectedPaneRefs(useSidebarSelectionStore.getState())).toEqual([ref("b")]);
	});

	it("Shift-click takes the panes of every row in the range", () => {
		const rows = ["a", "b", "c", "d"].map(ref);
		const range = { ordered: rows.map(key), refs: Object.fromEntries(rows.map((row) => [key(row), row])) };
		const store = useSidebarSelectionStore.getState();
		store.select({ kind: "session", key: "session:a", ref: ref("a") });
		store.select({ kind: "session", key: "session:c", ref: ref("c"), range });
		expect(selectedPaneRefs(useSidebarSelectionStore.getState())).toEqual([ref("a"), ref("b"), ref("c")]);
	});

	it("starting a project selection clears the sessions", () => {
		const store = useSidebarSelectionStore.getState();
		store.select({ kind: "session", key: "session:b", ref: ref("b"), seed: { key: "session:a", ref: ref("a") } });
		store.select({ kind: "project", key: "orchestrator:yoobi", ref: project("yoobi"), seed: { key: "orchestrator:balip", ref: project("balip") } });
		const state = useSidebarSelectionStore.getState();
		expect(state.selection?.kind).toBe("project");
		expect(selectedPaneRefs(state)).toEqual([project("balip"), project("yoobi")]);
	});

	it("refuses a fifth row and keeps the four already selected", () => {
		const store = useSidebarSelectionStore.getState();
		for (const id of ["p1", "p2", "p3", "p4"]) expect(store.select({ kind: "project", key: `orchestrator:${id}`, ref: project(id) })).toBe(true);
		expect(store.select({ kind: "project", key: "orchestrator:p5", ref: project("p5") })).toBe(false);
		expect(selectedPaneRefs(useSidebarSelectionStore.getState()).map((pane) => pane.projectId)).toEqual(["p1", "p2", "p3", "p4"]);
		store.clear();
		expect(useSidebarSelectionStore.getState().selection).toBeNull();
	});
});
