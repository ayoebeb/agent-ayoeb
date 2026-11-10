import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { leafKeys } from "../lib/pane-split";
import { useSessionSplitStore } from "../stores/session-split-store";
import { useSidebarSelectionStore } from "../stores/sidebar-selection-store";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { paneOnScreen, useSessionSplitActions } from "./useSessionSplitActions";

const navigate = vi.fn();
let routedSessionId: string | undefined;
vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	useNavigate: () => navigate,
	useParams: ({ select }: { select: (params: Record<string, string | undefined>) => unknown }) => select({ sessionId: routedSessionId }),
}));

function session(id: string, workspaceId: string, overrides: Partial<WorkspaceSession> = {}): WorkspaceSession {
	return { id, workspaceId, title: id, kind: "worker", updatedAt: "2026-11-10T10:00:00Z", ...overrides } as WorkspaceSession;
}

function workspace(id: string, sessions: WorkspaceSession[]): WorkspaceSummary {
	return { id, name: id, kind: "local", path: `/code/${id}`, type: "main", sessions } as unknown as WorkspaceSummary;
}

const workspaces = [
	workspace("yoobi", [
		session("yoobi-orc-1", "yoobi", { kind: "orchestrator", updatedAt: "2026-11-10T08:00:00Z" }),
		session("yoobi-orc-2", "yoobi", { kind: "orchestrator", updatedAt: "2026-11-10T11:00:00Z" }),
		session("yia-88", "yoobi"),
	]),
	workspace("balip", [session("balip-orc", "balip", { kind: "orchestrator" })]),
	workspace("empty", [session("w-1", "empty")]),
];

function actions() {
	return renderHook(() => useSessionSplitActions(() => workspaces)).result.current;
}

describe("useSessionSplitActions", () => {
	const toast = vi.fn();
	beforeEach(() => {
		navigate.mockReset();
		toast.mockReset();
		routedSessionId = undefined;
		window.localStorage.clear();
		useSessionSplitStore.setState({ split: null });
		useUiStore.setState({ showGlobalToast: toast } as never);
	});

	it("opens project orchestrators side by side, each its most recently active one", () => {
		useSidebarSelectionStore.getState().select({ kind: "project", key: "orchestrator:yoobi", ref: { kind: "orchestrator", projectId: "yoobi" } });
		const { open } = actions();
		act(() => {
			expect(open([{ kind: "orchestrator", projectId: "yoobi" }, { kind: "orchestrator", projectId: "balip" }, { kind: "orchestrator", projectId: "empty" }])).toBe(true);
		});
		const split = useSessionSplitStore.getState().split!;
		expect(leafKeys(split.root)).toEqual(["orchestrator:yoobi", "orchestrator:balip", "orchestrator:empty"]);
		expect(split.focus).toBe("orchestrator:yoobi");
		expect(navigate).toHaveBeenCalledWith({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "yoobi", sessionId: "yoobi-orc-2" } });
		// Opening uses up the selection.
		expect(useSidebarSelectionStore.getState().selection).toBeNull();
	});

	it("keeps focus on the session already on screen", () => {
		routedSessionId = "balip-orc";
		const { open } = actions();
		act(() => {
			open([{ kind: "orchestrator", projectId: "yoobi" }, { kind: "orchestrator", projectId: "balip" }]);
		});
		expect(useSessionSplitStore.getState().split?.focus).toBe("orchestrator:balip");
	});

	it("says so instead of opening when nothing selected is running, or too much is selected", () => {
		const { open } = actions();
		act(() => {
			expect(open([{ kind: "orchestrator", projectId: "empty" }, { kind: "orchestrator", projectId: "gone" }])).toBe(false);
		});
		expect(toast).toHaveBeenCalledWith("None of these projects has an orchestrator running");
		expect(useSessionSplitStore.getState().split).toBeNull();

		const five = ["a", "b", "c", "d", "e"].map((id) => ({ kind: "session" as const, sessionId: id, projectId: "yoobi" }));
		act(() => {
			expect(open(five)).toBe(false);
		});
		expect(toast).toHaveBeenLastCalledWith("Up to 4 can be shown side by side");
	});

	it("adds a session beside the one on screen and refuses a fifth pane", () => {
		const { addBeside } = actions();
		const onScreen = paneOnScreen(null, workspaces, "yia-88")!;
		expect(onScreen).toEqual({ kind: "session", sessionId: "yia-88", projectId: "yoobi" });
		act(() => {
			expect(addBeside(onScreen, { kind: "orchestrator", projectId: "balip" }, "right")).toBe(true);
		});
		expect(leafKeys(useSessionSplitStore.getState().split!.root)).toEqual(["session:yia-88", "orchestrator:balip"]);
		expect(navigate).toHaveBeenLastCalledWith({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "balip", sessionId: "balip-orc" } });
		// With the split on screen, the focused pane is the anchor.
		expect(paneOnScreen(useSessionSplitStore.getState().split, workspaces, "balip-orc")).toEqual({ kind: "orchestrator", projectId: "balip" });

		act(() => {
			addBeside(onScreen, { kind: "orchestrator", projectId: "yoobi" }, "bottom");
			addBeside(onScreen, { kind: "orchestrator", projectId: "empty" }, "bottom");
			expect(addBeside(onScreen, { kind: "session", sessionId: "w-1", projectId: "empty" }, "left")).toBe(false);
		});
		expect(toast).toHaveBeenLastCalledWith("Up to 4 can be shown side by side");
		expect(leafKeys(useSessionSplitStore.getState().split!.root)).toHaveLength(4);
	});
});
