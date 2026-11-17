import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { buildCrew } from "../../lib/crew";
import { sessionUiKey } from "../../lib/hosts";
import { useUiStore } from "../../stores/ui-store";
import type { WorkspaceSession } from "../../types/workspace";

const NOW = Date.parse("2026-11-16T12:00:00Z");
const MIN = 60_000;
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const { navigate, room, crewState, history } = vi.hoisted(() => ({
	navigate: vi.fn(),
	room: { tab: "changes" as string, scope: undefined as string | undefined, setTab: vi.fn(), setScope: vi.fn(), show: vi.fn() },
	crewState: { current: undefined as unknown },
	history: { commits: {} as Record<string, { sha: string; subject: string; author: string; timestamp: string; files: never[] }[]> },
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("../../hooks/useControlRoom", () => ({ useControlRoom: () => room }));
vi.mock("../../hooks/useCrew", () => ({ useCrew: () => crewState.current }));
vi.mock("../../hooks/useSessionWorkspaceFiles", () => ({
	sessionWorkspaceHistoryQueryOptions: (sessionId: string) => ({
		queryKey: ["history", sessionId],
		queryFn: async () => ({ commits: history.commits[sessionId] ?? [] }),
	}),
}));
vi.mock("../../lib/shell-context", () => ({ useShell: () => ({ daemonStatus: { state: "ready" } }) }));
vi.mock("../SessionFileExplorer", () => ({
	SessionFileExplorer: (props: { sessionId: string; isMaximized?: boolean }) => <div data-maximized={String(props.isMaximized)} data-session={props.sessionId} data-testid="explorer" />,
}));
vi.mock("../TerminalPane", () => ({
	TerminalPane: (props: { session?: { id: string }; inputDisabled?: boolean }) => (
		<div data-input-disabled={String(props.inputDisabled)} data-session={props.session?.id} data-testid="terminal-pane" />
	),
}));

import { ControlRoomBody } from "./ControlRoomBody";

const orchestrator = { id: "p-orchestrator", workspaceId: "p", kind: "orchestrator" } as WorkspaceSession;

function worker(over: Partial<WorkspaceSession> & { id: string }): WorkspaceSession {
	return {
		workspaceId: "p", workspaceName: "p", title: over.id, provider: "claude-code", mode: "tui", status: "working", kanbanColumn: "building",
		activity: { state: "active", lastActivityAt: ago(MIN) }, lastEventAt: ago(MIN), createdAt: ago(60 * MIN), updatedAt: ago(MIN), prs: [],
		branch: "ao/p/feature", ...over,
	} as WorkspaceSession;
}

type Diffs = Record<string, { paths: string[]; additions?: number; deletions?: number }>;

function setCrew(sessions: WorkspaceSession[], diffs: Diffs = {}) {
	const map = new Map(
		Object.entries(diffs).map(([id, d]) => [id, { additions: d.additions ?? d.paths.length, deletions: d.deletions ?? 0, files: d.paths.map((path) => ({ path, additions: 1, deletions: 0 })) }]),
	);
	crewState.current = { model: buildCrew({ sessions, diffs: map, now: NOW }), sessions, now: new Date(NOW) };
}

function renderBody() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
	return render(<ControlRoomBody orchestrator={orchestrator} projectKind="single_repo" />, { wrapper });
}

beforeEach(() => {
	vi.clearAllMocks();
	room.tab = "changes";
	room.scope = undefined;
	history.commits = {};
	useUiStore.setState({ inspectorSessions: {}, controlRooms: {} });
	setCrew([]);
});

describe("ControlRoomBody scope bar", () => {
	it("offers all agents and each live worker, with the lines each changed", async () => {
		setCrew([worker({ id: "a", title: "Jira toggle" }), worker({ id: "done", title: "Old", isTerminated: true, status: "terminated" })], { a: { paths: ["x.ts", "y.ts"], additions: 184, deletions: 22 } });
		renderBody();
		const bar = screen.getByTestId("control-scope-bar");
		const chips = within(bar).getAllByRole("button");
		expect(chips.map((chip) => chip.textContent)).toEqual(["All agents+184−22", "Jira toggle+184−22"]);
		expect(chips[0]).toHaveAttribute("aria-pressed", "true");

		await userEvent.click(chips[1]!);
		expect(room.setScope).toHaveBeenCalledWith("a");
	});

	it("marks the scoped worker and returns to all agents from the first chip", async () => {
		setCrew([worker({ id: "a", title: "Jira toggle" })], { a: { paths: ["x.ts"] } });
		room.scope = "a";
		renderBody();
		const bar = screen.getByTestId("control-scope-bar");
		expect(within(bar).getByRole("button", { name: /Jira toggle/ })).toHaveAttribute("aria-pressed", "true");
		expect(within(bar).getByRole("button", { name: /All agents/ })).toHaveAttribute("aria-pressed", "false");
		await userEvent.click(within(bar).getByRole("button", { name: /All agents/ }));
		expect(room.setScope).toHaveBeenCalledWith(undefined);
	});

	it("treats a scope whose worker left the list as all agents", () => {
		setCrew([worker({ id: "a" })], { a: { paths: ["x.ts"] } });
		room.scope = "gone";
		renderBody();
		expect(within(screen.getByTestId("control-scope-bar")).getByRole("button", { name: /All agents/ })).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByTestId("worktree-overview")).toBeInTheDocument();
	});
});

describe("ControlRoomBody changes", () => {
	it("groups every agent's files under its own heading and marks a file another agent also changed", async () => {
		setCrew(
			[worker({ id: "polish", title: "Chat polish" }), worker({ id: "anchors", title: "Anchor links" })],
			{ polish: { paths: ["ChatWorkspace.tsx", "styles.css"] }, anchors: { paths: ["ChatWorkspace.tsx", "anchor-links.ts"] } },
		);
		renderBody();
		const overview = screen.getByTestId("worktree-overview");
		const files = within(overview).getAllByTestId("worktree-file");
		expect(files).toHaveLength(4);
		const marked = within(overview).getAllByTestId("worktree-file-also").map((node) => node.textContent);
		// Workers started together list by id: Anchor links first, so its shared file names Chat polish.
		expect(marked).toEqual(["also Chat polish", "also Anchor links"]);

		await userEvent.click(within(files[2]!).getByRole("button"));
		expect(room.setScope).toHaveBeenCalledWith("polish");
	});

	it("says so when no agent has changed anything", () => {
		setCrew([worker({ id: "a" })], { a: { paths: [] } });
		renderBody();
		expect(screen.getByTestId("control-room-empty")).toHaveTextContent("No agent has changed files yet.");
	});

	it("shows one agent's changes in the explorer, changed files only", () => {
		setCrew([worker({ id: "a", title: "Jira toggle" })], { a: { paths: ["x.ts"] } });
		room.scope = "a";
		renderBody();
		expect(screen.getByTestId("explorer")).toHaveAttribute("data-session", "a");
		// Unset reads as "changes only": that is the explorer's own default.
		expect(useUiStore.getState().inspectorSessions[sessionUiKey("a")]?.filesChangedOnly ?? true).toBe(true);
	});

	it("shows the whole tree on the Files tab", () => {
		setCrew([worker({ id: "a" })], { a: { paths: ["x.ts"] } });
		room.tab = "files";
		room.scope = "a";
		renderBody();
		expect(screen.getByTestId("explorer")).toHaveAttribute("data-session", "a");
		expect(useUiStore.getState().inspectorSessions[sessionUiKey("a")]?.filesChangedOnly).toBe(false);
	});

	it("explains a worker that has no worktree yet instead of an empty explorer", () => {
		setCrew([worker({ id: "a", title: "Starting", workspacePublished: false })], { a: { paths: [] } });
		room.scope = "a";
		renderBody();
		expect(screen.queryByTestId("explorer")).toBeNull();
		expect(screen.getByTestId("control-room-empty")).toHaveTextContent("Starting has no worktree yet.");
	});
});

describe("ControlRoomBody commits", () => {
	it("lists each branch's commits and marks the newest ones that were not pushed", async () => {
		room.tab = "commits";
		setCrew([worker({ id: "a", title: "Jira toggle", branchState: { commits: 3, unpushed: 2 } })]);
		history.commits.a = [
			{ sha: "aaaaaaa1111", subject: "wire the toggle", author: "me", timestamp: ago(2 * MIN), files: [] },
			{ sha: "bbbbbbb2222", subject: "add the store key", author: "me", timestamp: ago(30 * MIN), files: [] },
			{ sha: "ccccccc3333", subject: "scaffold", author: "me", timestamp: ago(3 * 60 * MIN), files: [] },
		];
		renderBody();
		const rows = await screen.findAllByTestId("commit-row");
		expect(rows).toHaveLength(3);
		expect(rows[0]).toHaveTextContent("aaaaaaawire the toggleNot pushed");
		expect(rows[1]).toHaveTextContent("bbbbbbbadd the store keyNot pushed");
		expect(rows[2]).toHaveTextContent("cccccccscaffold");
		expect(rows[2]).not.toHaveTextContent(/not pushed/i);
		expect(screen.getByTestId("commits-tab")).toHaveTextContent("2 not pushed");
	});

	it("shows only the scoped worker's commits", async () => {
		room.tab = "commits";
		room.scope = "b";
		setCrew([worker({ id: "a", title: "First" }), worker({ id: "b", title: "Second" })]);
		history.commits.b = [{ sha: "ddddddd4444", subject: "only here", author: "me", timestamp: ago(MIN), files: [] }];
		renderBody();
		await waitFor(() => expect(screen.getAllByTestId("commit-row")).toHaveLength(1));
		expect(screen.getByTestId("commits-tab")).not.toHaveTextContent("First");
	});

	it("says a branch has no commits yet", async () => {
		room.tab = "commits";
		setCrew([worker({ id: "a" })]);
		renderBody();
		expect(await screen.findByText("No commits yet.")).toBeInTheDocument();
	});
});

describe("ControlRoomBody terminal", () => {
	it("asks whose terminal to watch when no agent is picked", async () => {
		room.tab = "terminal";
		setCrew([worker({ id: "a", title: "Jira toggle" }), worker({ id: "b", title: "Anchors", mode: "chat" })]);
		renderBody();
		const list = screen.getByTestId("choose-agent");
		expect(within(list).getAllByRole("button").map((button) => button.textContent)).toEqual(["Jira toggleTerminal", "AnchorsChat"]);
		await userEvent.click(within(list).getByRole("button", { name: /Anchors/ }));
		expect(room.setScope).toHaveBeenCalledWith("b");
	});

	it("watches a terminal agent with typing off until the person takes over", async () => {
		room.tab = "terminal";
		room.scope = "a";
		setCrew([worker({ id: "a", title: "Jira toggle" })]);
		renderBody();
		expect(screen.getByTestId("terminal-pane")).toHaveAttribute("data-session", "a");
		expect(screen.getByTestId("terminal-pane")).toHaveAttribute("data-input-disabled", "true");
		expect(screen.getByTestId("terminal-mode")).toHaveTextContent("Watching");

		await userEvent.click(screen.getByRole("button", { name: "Take over" }));
		expect(screen.getByTestId("terminal-pane")).toHaveAttribute("data-input-disabled", "false");
		expect(screen.getByTestId("terminal-mode")).toHaveTextContent("You are typing");

		await userEvent.click(screen.getByRole("button", { name: "Stop typing" }));
		expect(screen.getByTestId("terminal-pane")).toHaveAttribute("data-input-disabled", "true");
	});

	it("says a chat agent has no terminal and offers its session, instead of hiding the tab", async () => {
		room.tab = "terminal";
		room.scope = "b";
		setCrew([worker({ id: "b", title: "Anchors", mode: "chat" })]);
		renderBody();
		expect(screen.queryByTestId("terminal-pane")).toBeNull();
		expect(screen.getByTestId("control-room-empty")).toHaveTextContent("Anchors works in chat, so it has no terminal of its own.");
		await userEvent.click(screen.getByRole("button", { name: "Open session" }));
		expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ params: expect.objectContaining({ projectId: "p", sessionId: "b" }) }));
	});
});
