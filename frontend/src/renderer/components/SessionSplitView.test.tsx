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
let remoteWorkspaces: WorkspaceSummary[] = [];

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	useNavigate: () => navigate,
}));
vi.mock("../hooks/useWorkspaceQuery", async (importOriginal) => ({
	...(await importOriginal<typeof import("../hooks/useWorkspaceQuery")>()),
	useWorkspaceQuery: () => ({ data: workspaces }),
	useRemoteWorkspaces: () => ({ data: remoteWorkspaces }),
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
function FakeSession({ sessionId, hostId }: SessionPaneViewProps) {
	const pane = useSessionPane();
	useSessionShortcut("toggle-terminal", () => shortcutSpy(sessionId));
	return (
		<div data-focused-pane={pane?.focused ? "yes" : "no"} data-host={hostId} data-testid={`view-${sessionId}`}>
			<SessionTabsTopbar tabs={<span>{sessionId}</span>} />
		</div>
	);
}
const renderSession = (props: SessionPaneViewProps) => <FakeSession {...props} />;

const a: SessionPaneRef = { kind: "session", sessionId: "a", projectId: "p1" };
const b: SessionPaneRef = { kind: "session", sessionId: "b", projectId: "p2" };
const empty: SessionPaneRef = { kind: "orchestrator", projectId: "p3" };

/** Subscribes to the split as LocalSessionHost does. */
function Host({ routedSessionId, routedPaneKey, routedHostId }: { routedSessionId: string; routedPaneKey: string; routedHostId?: string }) {
	const split = useSessionSplitStore((state) => state.split);
	return split ? <SessionSplitView renderSession={renderSession} routedHostId={routedHostId} routedPaneKey={routedPaneKey} routedSessionId={routedSessionId} split={split} /> : null;
}

function renderSplit(routedSessionId: string, routedPaneKey: string, routedHostId?: string) {
	return render(
		<TooltipProvider>
			<Host routedHostId={routedHostId} routedPaneKey={routedPaneKey} routedSessionId={routedSessionId} />
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
		remoteWorkspaces = [];
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

	it("closing the focused pane routes to a remaining pane with a session, not past an empty first pane", () => {
		// The orchestrator pane with none running comes first, where focus falls by default.
		useSessionSplitStore.setState({ split: openSplit([empty, a, b], "session:a") });
		const first = renderSplit("a", "session:a");
		fireEvent.click(within(pane("session:a")).getByTestId("session-pane-close"));
		expect(navigate).toHaveBeenCalledTimes(1);
		expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ params: { projectId: "p2", sessionId: "b" }, replace: true }));
		expect(Object.keys(useSessionSplitStore.getState().split!.panes)).toEqual(["orchestrator:p3", "session:b"]);
		expect(useSessionSplitStore.getState().split?.focus).toBe("session:b");
		first.unmount();

		// With no pane left that has a session, the route goes to that pane's project.
		navigate.mockReset();
		useSessionSplitStore.setState({ split: openSplit([empty, a], "session:a") });
		renderSplit("a", "session:a");
		fireEvent.click(within(pane("session:a")).getByTestId("session-pane-close"));
		expect(useSessionSplitStore.getState().split).toBeNull();
		expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ params: { projectId: "p3" }, replace: true }));
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

	describe("with panes from different computers", () => {
		const remoteA: SessionPaneRef = { kind: "session", sessionId: "a", projectId: "p1", hostId: "host-2" };
		const remoteOrchestrator: SessionPaneRef = { kind: "orchestrator", projectId: "p9", hostId: "host-2" };
		const remoteKey = "session:remote:host-2:a";

		beforeEach(() => {
			remoteWorkspaces = [
				{ ...workspace("p1", [session("a", "p1", { hostId: "host-2" })]), hostId: "host-2" } as WorkspaceSummary,
			];
		});

		it("gives each pane its own computer, even with the same session id, and names the computer", () => {
			useSessionSplitStore.setState({ split: openSplit([a, remoteA], "session:a") });
			renderSplit("a", "session:a");
			const views = screen.getAllByTestId("view-a");
			expect(views.map((view) => view.getAttribute("data-host"))).toEqual([null, "host-2"]);
			expect(within(pane("session:a")).getByTestId("session-pane-host")).toHaveTextContent("This computer");
			expect(within(pane(remoteKey)).getByTestId("session-pane-host")).toHaveTextContent("host-2");
		});

		it("names no computer while every pane is on the same one", () => {
			renderSplit("a", "session:a");
			expect(screen.queryByTestId("session-pane-host")).toBeNull();
		});

		it("focuses a remote pane by routing to its host", () => {
			useSessionSplitStore.setState({ split: openSplit([a, remoteA], "session:a") });
			renderSplit("a", "session:a");
			fireEvent.pointerDown(within(pane(remoteKey)).getByTestId("view-a"));
			expect(navigate).toHaveBeenCalledWith({
				to: "/host/$hostId/project/$projectId/session/$sessionId",
				params: { hostId: "host-2", projectId: "p1", sessionId: "a" },
				replace: true,
			});
			expect(useSessionSplitStore.getState().split?.focus).toBe(remoteKey);
		});

		it("focuses a local pane from a remote route by routing to the local session", () => {
			useSessionSplitStore.setState({ split: openSplit([a, remoteA], remoteKey) });
			renderSplit("a", remoteKey, "host-2");
			fireEvent.pointerDown(within(pane("session:a")).getByTestId("view-a"));
			expect(navigate).toHaveBeenCalledWith({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId: "p1", sessionId: "a" },
				replace: true,
			});
		});

		it("keeps the other panes working when a computer is offline", () => {
			remoteWorkspaces = [];
			useSessionSplitStore.setState({ split: openSplit([a, remoteA, remoteOrchestrator], "session:a") });
			renderSplit("a", "session:a");
			// The remote session pane still renders its session view, which shows the offline state itself.
			expect(within(pane(remoteKey)).getByTestId("view-a")).toHaveAttribute("data-host", "host-2");
			// A remote orchestrator cannot resolve without its project list.
			expect(within(pane("orchestrator:remote:host-2:p9")).getByRole("alert")).toHaveTextContent("Host is offline");
			// The local pane is untouched.
			expect(within(pane("session:a")).getByTestId("view-a")).toHaveAttribute("data-focused-pane", "yes");
		});

		it("closes a remote pane and returns to the local one", () => {
			useSessionSplitStore.setState({ split: openSplit([a, remoteA], remoteKey) });
			renderSplit("a", remoteKey, "host-2");
			fireEvent.click(within(pane(remoteKey)).getByTestId("session-pane-close"));
			expect(useSessionSplitStore.getState().split).toBeNull();
			expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: "/projects/$projectId/sessions/$sessionId", replace: true }));
		});
	});
});
