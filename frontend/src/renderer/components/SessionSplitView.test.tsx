import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { runAppShortcut } from "../hooks/useAppShortcut";
import { openSplit, type SessionPaneRef } from "../lib/session-split";
import { useSessionSplitStore } from "../stores/session-split-store";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { SessionTabsTopbar } from "./PageTopbar";
import { useSessionPane } from "./session-pane-context";
import { SessionSplitView, type SessionPaneViewProps } from "./SessionSplitView";
import { useSessionShortcut } from "../hooks/useAppShortcut";
import { TooltipProvider } from "./ui/tooltip";

const navigate = vi.fn();
const openOrchestrator = vi.fn();
let workspaces: WorkspaceSummary[] = [];

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	useNavigate: () => navigate,
}));
vi.mock("../hooks/useWorkspaceQuery", async (importOriginal) => ({
	...(await importOriginal<typeof import("../hooks/useWorkspaceQuery")>()),
	useWorkspaceQuery: () => ({ data: workspaces }),
}));
vi.mock("../hooks/useProjectOrchestratorAction", () => ({
	useProjectOrchestratorAction: () => ({ openOrchestrator, isSpawning: false, isProjectRestarting: false, isProvisioning: false, spawnError: "" }),
}));
vi.mock("./Sidebar", () => ({ SessionRowStatusGlyph: () => <span data-testid="status-glyph" /> }));

function session(id: string, workspaceId: string, overrides: Partial<WorkspaceSession> = {}): WorkspaceSession {
	return { id, workspaceId, title: id, kind: "worker", updatedAt: "2026-11-10T10:00:00Z", ...overrides } as WorkspaceSession;
}

function workspace(id: string, sessions: WorkspaceSession[]): WorkspaceSummary {
	return { id, name: `Project ${id}`, kind: "local", path: `/code/${id}`, type: "main", sessions } as unknown as WorkspaceSummary;
}

const shortcutSpy = vi.fn();
/** Stands in for SessionView: shows what its pane tells it and takes a session shortcut. */
function FakeSession({ sessionId }: SessionPaneViewProps) {
	const pane = useSessionPane();
	useSessionShortcut("toggle-terminal", () => shortcutSpy(sessionId));
	return (
		<div data-focused-pane={pane?.focused ? "yes" : "no"} data-testid={`view-${sessionId}`}>
			<SessionTabsTopbar tabs={<span>{sessionId}</span>} />
		</div>
	);
}
const renderSession = (props: SessionPaneViewProps) => <FakeSession {...props} />;

const a: SessionPaneRef = { kind: "session", sessionId: "a", projectId: "p1" };
const b: SessionPaneRef = { kind: "session", sessionId: "b", projectId: "p2" };
const empty: SessionPaneRef = { kind: "orchestrator", projectId: "p3" };

/** Subscribes to the split as LocalSessionHost does. */
function Host({ routedSessionId, routedPaneKey }: { routedSessionId: string; routedPaneKey: string }) {
	const split = useSessionSplitStore((state) => state.split);
	return split ? <SessionSplitView renderSession={renderSession} routedPaneKey={routedPaneKey} routedSessionId={routedSessionId} split={split} /> : null;
}

function renderSplit(routedSessionId: string, routedPaneKey: string) {
	return render(
		<TooltipProvider>
			<Host routedPaneKey={routedPaneKey} routedSessionId={routedSessionId} />
		</TooltipProvider>,
	);
}

function pane(key: string): HTMLElement {
	return document.querySelector<HTMLElement>(`[data-session-pane="${key}"]`)!;
}

describe("SessionSplitView", () => {
	beforeEach(() => {
		window.localStorage.clear();
		navigate.mockReset();
		shortcutSpy.mockReset();
		workspaces = [
			workspace("p1", [session("a", "p1")]),
			workspace("p2", [session("b", "p2")]),
			workspace("p3", [session("w-1", "p3")]),
		];
		useSessionSplitStore.setState({ split: openSplit([a, b, empty], "session:a") });
	});
	afterEach(() => useSessionSplitStore.setState({ split: null }));

	it("shows every pane's session, and an orchestrator pane with none running as an empty state", () => {
		renderSplit("a", "session:a");
		expect(screen.getByTestId("view-a")).toBeInTheDocument();
		expect(screen.getByTestId("view-b")).toBeInTheDocument();
		const orchestratorPane = within(pane("orchestrator:p3"));
		expect(orchestratorPane.getByText("No orchestrator running")).toBeInTheDocument();
		fireEvent.click(orchestratorPane.getByRole("button", { name: "Spawn orchestrator" }));
		expect(openOrchestrator).toHaveBeenCalled();
		// The session's own top bar is the pane header: status and project lead it, close ends it.
		expect(within(pane("session:b")).getByText("Project p2")).toBeInTheDocument();
		expect(within(pane("session:b")).getByTestId("status-glyph")).toBeInTheDocument();
		expect(within(pane("orchestrator:p3")).getByText("Project p3")).toBeInTheDocument();
		expect(screen.getAllByTestId("session-pane-close")).toHaveLength(3);
		expect(within(pane("session:b")).getByRole("button", { name: "Close b" })).toBeInTheDocument();
	});

	it("routes keys to the focused pane only and moves focus where the pointer goes", () => {
		renderSplit("a", "session:a");
		expect(screen.getByTestId("view-a")).toHaveAttribute("data-focused-pane", "yes");
		expect(screen.getByTestId("view-b")).toHaveAttribute("data-focused-pane", "no");
		expect(pane("session:a").querySelector("[data-testid='session-pane-focus-ring']")).not.toBeNull();
		expect(pane("session:b").querySelector("[data-testid='session-pane-focus-ring']")).toBeNull();

		act(() => runAppShortcut({ id: "toggle-terminal" }));
		expect(shortcutSpy.mock.calls).toEqual([["a"]]);

		fireEvent.pointerDown(screen.getByTestId("view-b"));
		expect(useSessionSplitStore.getState().split?.focus).toBe("session:b");
		expect(screen.getByTestId("view-b")).toHaveAttribute("data-focused-pane", "yes");
		// The route follows the focused pane, replacing the entry.
		expect(navigate).toHaveBeenCalledWith(expect.objectContaining({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "p2", sessionId: "b" },
			replace: true,
		}));
		shortcutSpy.mockReset();
		act(() => runAppShortcut({ id: "toggle-terminal" }));
		expect(shortcutSpy.mock.calls).toEqual([["b"]]);

		// An empty pane takes focus without a route to move to.
		navigate.mockReset();
		fireEvent.pointerDown(within(pane("orchestrator:p3")).getByText("No orchestrator running"));
		expect(useSessionSplitStore.getState().split?.focus).toBe("orchestrator:p3");
		expect(navigate).not.toHaveBeenCalled();
	});

	it("lets go of a caret left in a pane that lost focus", () => {
		renderSplit("a", "session:a");
		const input = document.createElement("input");
		pane("session:a").appendChild(input);
		input.focus();
		expect(document.activeElement).toBe(input);
		act(() => useSessionSplitStore.getState().focus("session:b"));
		expect(document.activeElement).not.toBe(input);
	});

	it("closing panes down to one returns to that session on its own", () => {
		const first = renderSplit("a", "session:a");
		fireEvent.click(within(pane("orchestrator:p3")).getByTestId("session-pane-close"));
		expect(Object.keys(useSessionSplitStore.getState().split!.panes)).toEqual(["session:a", "session:b"]);
		expect(navigate).not.toHaveBeenCalled();
		first.unmount();

		// Closing the routed pane moves the route to the one that stays.
		renderSplit("a", "session:a");
		fireEvent.click(within(pane("session:a")).getByTestId("session-pane-close"));
		expect(useSessionSplitStore.getState().split).toBeNull();
		expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ params: { projectId: "p2", sessionId: "b" }, replace: true }));
	});

	it("closes a pane from its close button without focusing or routing to it first", () => {
		workspaces = [workspace("p1", [session("a", "p1")]), workspace("p2", [session("b", "p2", { isTerminated: true })]), workspace("p3", [])];
		renderSplit("a", "session:a");
		const close = within(pane("session:b")).getByTestId("session-pane-close");
		fireEvent.pointerDown(close);
		fireEvent.focusIn(close);
		fireEvent.click(close);
		expect(navigate).not.toHaveBeenCalled();
		expect(useSessionSplitStore.getState().split?.focus).toBe("session:a");
		expect(Object.keys(useSessionSplitStore.getState().split!.panes)).toEqual(["session:a", "orchestrator:p3"]);
		// An ended pane takes focus without moving the route to a session that is gone.
		act(() => useSessionSplitStore.setState({ split: openSplit([a, b], "session:a") }));
		fireEvent.pointerDown(within(pane("session:b")).getByText("This session has ended."));
		expect(useSessionSplitStore.getState().split?.focus).toBe("session:b");
		expect(navigate).not.toHaveBeenCalled();
	});

	it("shows a clean ended state for a session that was killed", () => {
		workspaces = [workspace("p1", [session("a", "p1")]), workspace("p2", [session("b", "p2", { isTerminated: true })]), workspace("p3", [])];
		renderSplit("a", "session:a");
		expect(screen.queryByTestId("view-b")).toBeNull();
		expect(within(pane("session:b")).getByText("This session has ended.")).toBeInTheDocument();
		expect(within(pane("session:b")).getByTestId("session-pane-empty")).toHaveAttribute("data-pane-state", "ended");
	});
});
