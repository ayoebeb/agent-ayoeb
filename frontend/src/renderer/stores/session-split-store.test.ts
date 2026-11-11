import { beforeEach, describe, expect, it, vi } from "vitest";
import { leafKeys } from "../lib/pane-split";
import { openSplit, type SessionPaneRef } from "../lib/session-split";
import { SESSION_SPLIT_STORAGE_KEY, useSessionSplitStore } from "./session-split-store";

const a: SessionPaneRef = { kind: "session", sessionId: "a", projectId: "p1" };
const b: SessionPaneRef = { kind: "session", sessionId: "b", projectId: "p1" };
const c: SessionPaneRef = { kind: "session", sessionId: "c", projectId: "p2" };
const yoobi: SessionPaneRef = { kind: "orchestrator", projectId: "yoobi" };

function stored() {
	return JSON.parse(window.localStorage.getItem(SESSION_SPLIT_STORAGE_KEY) ?? "null");
}

describe("session split store", () => {
	beforeEach(() => {
		window.localStorage.clear();
		useSessionSplitStore.setState({ split: null });
	});

	it("opens two to four panes and refuses any other count", () => {
		const store = useSessionSplitStore.getState();
		expect(store.open([a])).toBe(false);
		expect(store.open([a, b, c, yoobi, { kind: "session", sessionId: "e", projectId: "p2" }])).toBe(false);
		expect(useSessionSplitStore.getState().split).toBeNull();
		expect(store.open([a, b, yoobi], "session:b")).toBe(true);
		const split = useSessionSplitStore.getState().split!;
		expect(leafKeys(split.root)).toEqual(["session:a", "session:b", "orchestrator:yoobi"]);
		expect(split.focus).toBe("session:b");
	});

	it("remembers the layout, its sizes and focus across reloads", async () => {
		const store = useSessionSplitStore.getState();
		store.open([a, b]);
		store.resize("r", 0.35);
		store.focus("session:b");
		expect(stored()).toMatchObject({ root: { ratio: 0.35 }, focus: "session:b" });

		vi.resetModules();
		const reloaded = await import("./session-split-store");
		expect(reloaded.useSessionSplitStore.getState().split).toEqual(useSessionSplitStore.getState().split);
	});

	it("goes back to one session at a time when closed down to one pane", () => {
		const store = useSessionSplitStore.getState();
		store.open([a, b, c]);
		store.close("session:b");
		expect(leafKeys(useSessionSplitStore.getState().split!.root)).toEqual(["session:a", "session:c"]);
		store.close("session:a");
		expect(useSessionSplitStore.getState().split).toBeNull();
		expect(window.localStorage.getItem(SESSION_SPLIT_STORAGE_KEY)).toBeNull();
	});

	it("adds a pane beside another and says when the split is full", () => {
		const store = useSessionSplitStore.getState();
		expect(store.splitBeside(a, b, "right")).toBe("split");
		expect(store.splitBeside(b, c, "bottom")).toBe("split");
		expect(store.splitBeside(a, yoobi, "left")).toBe("split");
		const full = useSessionSplitStore.getState().split;
		expect(store.splitBeside(a, { kind: "session", sessionId: "e", projectId: "p2" }, "right")).toBe("full");
		expect(useSessionSplitStore.getState().split).toBe(full);
	});

	it("ignores a stored layout it cannot read", async () => {
		window.localStorage.setItem(SESSION_SPLIT_STORAGE_KEY, "{broken");
		vi.resetModules();
		expect((await import("./session-split-store")).useSessionSplitStore.getState().split).toBeNull();
		window.localStorage.setItem(SESSION_SPLIT_STORAGE_KEY, JSON.stringify(openSplit([a, yoobi])));
		vi.resetModules();
		expect(leafKeys((await import("./session-split-store")).useSessionSplitStore.getState().split!.root)).toEqual([
			"session:a",
			"orchestrator:yoobi",
		]);
	});
});
