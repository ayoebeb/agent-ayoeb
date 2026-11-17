import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { OrchestratorWaiting } from "../lib/orchestrator-waiting";
import type { PullRequestFacts, WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { ProjectsView } from "./ProjectsView";

const mocks = vi.hoisted(() => ({
	navigate: vi.fn(),
	workspaces: [] as unknown[],
	remote: [] as unknown[],
	waiting: [] as unknown[],
	query: { isError: false, isSuccess: true },
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	useNavigate: () => mocks.navigate,
}));
vi.mock("../hooks/useWorkspaceQuery", async (importOriginal) => ({
	...(await importOriginal<typeof import("../hooks/useWorkspaceQuery")>()),
	useWorkspaceQuery: () => ({ data: mocks.workspaces, ...mocks.query }),
	useRemoteWorkspaces: () => ({ data: mocks.remote, loadedProjectHostIds: [], loadedSessionHostIds: [], failedHostIds: [], refetch: vi.fn() }),
}));
vi.mock("../hooks/useOrchestratorWaiting", async (importOriginal) => ({
	...(await importOriginal<typeof import("../hooks/useOrchestratorWaiting")>()),
	useOrchestratorWaiting: () => ({ waiting: mocks.waiting }),
}));
vi.mock("../lib/host-clients", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/host-clients")>()),
	labelForHost: (hostId: string) => (hostId === "box" ? "Studio PC" : undefined),
}));

const session = (over: Partial<WorkspaceSession> & { id: string }): WorkspaceSession =>
	({ workspaceId: "p", workspaceName: "P", title: over.id, provider: "claude-code", status: "working", updatedAt: "2026-11-17T09:00:00Z", prs: [], ...over }) as WorkspaceSession;
const orchestrator = (id: string, state: "active" | "idle" | "waiting_input" | "exited") =>
	session({ id, kind: "orchestrator", activity: { state, lastActivityAt: "2026-11-17T10:00:00Z" } });
const project = (id: string, sessions: WorkspaceSession[], extra: Partial<WorkspaceSummary> = {}) =>
	({ id, name: id, path: `/${id}`, sessions, ...extra }) as WorkspaceSummary;
const pr = (over: Partial<PullRequestFacts>): PullRequestFacts => ({
	url: "https://example.test/pr/1", number: 1, state: "open", ci: "unknown", review: "none", mergeability: "mergeable", reviewComments: false, updatedAt: "2026-11-17T09:00:00Z", ...over,
});
const wait = (projectId: string, sessionId: string, since: string, hostId = "local"): OrchestratorWaiting => ({
	notificationId: `n-${projectId}`, projectId, sessionId, question: "Merge now?", reminded: false, since, title: `${projectId}: orchestrator is waiting for you`, hostId,
});
const names = () => screen.getAllByTestId("project-card").map((card) => card.querySelector("span.truncate")?.textContent);

describe("ProjectsView", () => {
	beforeEach(() => {
		mocks.navigate.mockClear();
		mocks.workspaces = [];
		mocks.remote = [];
		mocks.waiting = [];
		mocks.query = { isError: false, isSuccess: true };
	});

	it("lists the projects waiting for the user first, then by what the orchestrator is doing", () => {
		mocks.workspaces = [
			project("alpha", [orchestrator("o-a", "idle")]),
			project("beta", [orchestrator("o-b", "active")]),
			project("gamma", [orchestrator("o-g", "idle")]),
			project("delta", []),
		];
		mocks.remote = [project("remote-one", [orchestrator("o-r", "idle")], { hostId: "box" })];
		mocks.waiting = [wait("gamma", "o-g", "2026-11-17T10:00:00Z"), wait("remote-one", "o-r", "2026-11-17T09:00:00Z", "box")];
		render(<ProjectsView />);
		expect(names()).toEqual(["remote-one", "gamma", "beta", "alpha", "delta"]);
		const [first, second] = screen.getAllByTestId("project-card");
		expect(first).toHaveAttribute("data-state", "waiting");
		expect(first).toHaveTextContent("Waiting for you");
		expect(second).toHaveAttribute("data-state", "waiting");
	});

	it("shows the machine, workers and pull requests of each project", () => {
		mocks.workspaces = [
			project("beta", [
				orchestrator("o-b", "active"),
				session({ id: "w1" }),
				session({ id: "w2", prs: [pr({ ci: "failing", review: "approved" })] }),
			]),
			project("quiet", [orchestrator("o-q", "idle")]),
		];
		mocks.remote = [project("far", [orchestrator("o-f", "idle")], { hostId: "box" }), project("lost", [orchestrator("o-l", "idle")], { hostId: "gone" })];
		render(<ProjectsView />);
		const card = (name: string) => screen.getAllByTestId("project-card").find((c) => c.textContent?.includes(name)) as HTMLElement;
		expect(card("beta")).toHaveTextContent("This computer");
		expect(card("beta")).toHaveTextContent("Working");
		expect(card("beta")).toHaveTextContent("Workers: 2 active");
		expect(card("beta")).toHaveTextContent("PRs: 1 open · 1 approved · CI failing");
		expect(card("quiet")).toHaveTextContent("Idle");
		expect(card("quiet")).toHaveTextContent("No active workers");
		expect(card("quiet")).toHaveTextContent("No open PRs");
		expect(card("far")).toHaveTextContent("Studio PC");
		expect(card("lost")).toHaveTextContent("Another computer");
	});

	it("says each state in words", () => {
		mocks.workspaces = [
			project("needs", [orchestrator("o1", "waiting_input")]),
			project("stopped", [orchestrator("o2", "exited")]),
			project("bare", []),
		];
		render(<ProjectsView />);
		const states = Object.fromEntries(screen.getAllByTestId("project-card").map((c) => [c.getAttribute("data-state"), c.textContent]));
		expect(states["needs-input"]).toContain("Needs input");
		expect(states.stopped).toContain("Stopped");
		expect(states.none).toContain("No orchestrator yet");
	});

	it("opens the orchestrator chat, on its own computer, and the project when there is none", () => {
		mocks.workspaces = [project("gamma", [orchestrator("o-g", "idle")]), project("bare", [])];
		mocks.remote = [project("far", [orchestrator("o-f", "idle")], { hostId: "box" })];
		render(<ProjectsView />);
		fireEvent.click(screen.getByRole("button", { name: "Open the orchestrator chat for gamma on This computer" }));
		expect(mocks.navigate).toHaveBeenLastCalledWith({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "gamma", sessionId: "o-g" } });
		fireEvent.click(screen.getByRole("button", { name: "Open the orchestrator chat for far on Studio PC" }));
		expect(mocks.navigate).toHaveBeenLastCalledWith({
			to: "/host/$hostId/project/$projectId/session/$sessionId",
			params: { hostId: "box", projectId: "far", sessionId: "o-f" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Open bare on This computer" }));
		expect(mocks.navigate).toHaveBeenLastCalledWith({ to: "/projects/$projectId", params: { projectId: "bare" } });
	});

	it("sends a click on a waiting project to the orchestrator that waits", () => {
		mocks.workspaces = [project("gamma", [])];
		mocks.waiting = [wait("gamma", "o-wait", "2026-11-17T10:00:00Z")];
		render(<ProjectsView />);
		fireEvent.click(within(screen.getByTestId("project-cards")).getByRole("button"));
		expect(mocks.navigate).toHaveBeenLastCalledWith({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "gamma", sessionId: "o-wait" } });
	});

	it("points to Home when there are no projects, and says so when they could not load", () => {
		render(<ProjectsView />);
		expect(screen.getByText("No projects yet.")).toBeVisible();
		fireEvent.click(screen.getByRole("button", { name: "Add a project" }));
		expect(mocks.navigate).toHaveBeenLastCalledWith({ to: "/" });
	});

	it("says when the projects could not be loaded", () => {
		mocks.query = { isError: true, isSuccess: false };
		render(<ProjectsView />);
		expect(screen.getByRole("alert")).toBeVisible();
		expect(screen.queryByTestId("project-card")).toBeNull();
	});
});
