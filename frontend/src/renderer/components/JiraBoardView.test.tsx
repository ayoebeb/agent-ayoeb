import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "../i18n";
import type { JiraBoard, JiraTicket } from "../lib/jira";
import type { WorkspaceSession } from "../types/workspace";

type Reply = { data?: unknown; error?: unknown; response: { status: number } };

// A fake daemon: the Jira view only ever talks to these routes.
const { daemon, openLinkMock } = vi.hoisted(() => ({
	daemon: { calls: [] as { method: string; path: string; init?: { params?: { path?: Record<string, string> } } }[], replies: {} as Record<string, () => Reply | Promise<Reply>> },
	openLinkMock: vi.fn(),
}));

vi.mock("../lib/host-clients", () => {
	const send = (method: string) => async (path: string, init?: { params?: { path?: Record<string, string> } }) => {
		daemon.calls.push({ method, path, init });
		const reply = daemon.replies[`${method} ${path}`];
		if (!reply) throw new Error(`unexpected ${method} ${path}`);
		return reply();
	};
	return { clientForSessionHost: () => ({ GET: send("GET"), POST: send("POST"), PUT: send("PUT"), DELETE: send("DELETE") }) };
});
vi.mock("../lib/external-link-policy", () => ({ openLinkInSystemBrowser: openLinkMock }));

import { useUiStore } from "../stores/ui-store";
import { JiraBoardView } from "./JiraBoardView";
import { TooltipProvider } from "./ui/tooltip";

const ada = { accountId: "acc-1", displayName: "Ada Example" };

function ticket(key: string, summary: string, overrides: Partial<JiraTicket> = {}): JiraTicket {
	return {
		key, summary, url: `https://yoobi.atlassian.net/browse/${key}`, status: { id: "1", name: "To Do", category: "new" },
		updatedAt: "2026-11-10T08:30:00Z", ...overrides,
	};
}

const board: JiraBoard = {
	siteUrl: "https://yoobi.atlassian.net", projectKey: "YIA", boardName: "YIA board", accountId: "acc-1",
	jql: 'project = "YIA" ORDER BY updated DESC', total: 3, fetchedAt: "2026-11-16T12:00:00Z",
	columns: [
		{
			name: "To Do",
			tickets: [
				ticket("YIA-10", "Add CSV export", { assignee: ada, issueType: "Story", priority: "High", description: "Users want CSV export.", acceptanceCriteria: "- exports all rows" }),
				ticket("YIA-11", "Fix login redirect", { assignee: { accountId: "acc-2", displayName: "Bo Builder" }, issueType: "Bug", priority: "Medium" }),
			],
		},
		{ name: "In Progress", tickets: [ticket("YIA-12", "Upgrade React", { status: { id: "3", name: "In Progress", category: "indeterminate" } })] },
		{ name: "Done", tickets: [] },
	],
};

const connected = { connected: true, siteUrl: "https://yoobi.atlassian.net", email: "ada@example.com", projectKey: "YIA" };
const ok = (data: unknown, status = 200): Reply => ({ data, response: { status } });
const fail = (code: string, message: string, status: number): Reply => ({ error: { code, message }, response: { status } });

const handlers = {
	onOpenSettings: vi.fn(),
	onOpenSession: vi.fn(),
	onOpenOrchestrator: vi.fn(),
	onStartWorker: vi.fn(),
};

function renderBoard(sessions: WorkspaceSession[] = []) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<JiraBoardView projectId="yoobi-app" sessions={sessions} {...handlers} />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

function serveBoard(reply: () => Reply | Promise<Reply> = () => ok(board)) {
	daemon.replies["GET /api/v1/projects/{id}/jira"] = () => ok(connected);
	daemon.replies["GET /api/v1/projects/{id}/jira/board"] = reply;
}

beforeEach(() => {
	daemon.calls.length = 0;
	daemon.replies = {};
	Object.values(handlers).forEach((mock) => mock.mockReset());
	openLinkMock.mockReset();
	useUiStore.getState().clearGlobalToast();
});
afterEach(cleanup);

describe("JiraBoardView", () => {
	it("shows the project's tickets in the Jira board's columns, with who, what kind, and a link out", async () => {
		serveBoard();
		renderBoard();

		const columns = await screen.findAllByTestId("jira-column");
		expect(columns.map((column) => within(column).getAllByText(/To Do|In Progress|Done/)[0].textContent)).toEqual(["To Do", "In Progress", "Done"]);
		const todo = within(columns[0]);
		const card = todo.getByText("Add CSV export").closest("[data-testid=jira-card]") as HTMLElement;
		// The Agents card's shell: title, a "KEY · type · priority" meta line, and a footer
		// with the coloured Jira status on the left and how long ago it changed on the right.
		expect(within(card).getByText("YIA-10")).toBeInTheDocument();
		expect(card.textContent).toContain("YIA-10 · Story · High");
		expect(within(card).getByTestId("board-card-status")).toHaveTextContent("To Do");
		expect(within(card).getByTestId("board-card-status").className).toContain("text-muted-foreground");
		expect(within(columns[1]).getByTestId("board-card-status")).toHaveTextContent("In Progress");
		expect(within(columns[1]).getByTestId("board-card-status").className).toContain("text-status-working");
		expect(card.querySelector("[title]:not([aria-label])")).not.toBeNull();
		// Ada's own ticket carries no avatar; Bo's does.
		expect(within(card).queryByRole("img")).not.toBeInTheDocument();
		const bo = within(columns[0]).getByText("Fix login redirect").closest("[data-testid=jira-card]") as HTMLElement;
		expect(within(bo).getByRole("img", { name: "Assigned to Bo Builder" })).toHaveTextContent("BB");

		await userEvent.click(within(card).getByRole("button", { name: "Open in Jira" }));
		expect(openLinkMock).toHaveBeenCalledWith("https://yoobi.atlassian.net/browse/YIA-10");
	});

	it("shows no assignee avatars and no filter when everything is assigned to me", async () => {
		const mine: JiraBoard = {
			...board, projectKey: undefined, boardName: undefined, total: 2,
			columns: [{ name: "To Do", tickets: [ticket("WEB-3", "Own ticket", { assignee: ada }), ticket("API-9", "Another own", { assignee: ada, status: { id: "3", name: "In Review", category: "indeterminate" } })] }],
		};
		serveBoard(() => ok(mine));
		renderBoard();

		const card = (await screen.findByText("Own ticket")).closest("[data-testid=jira-card]") as HTMLElement;
		// The ticket's Jira project is its key's prefix, shown in the meta line.
		expect(card.textContent).toContain("WEB-3");
		expect(screen.queryByRole("img")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Assigned to me" })).not.toBeInTheDocument();
		// With no board or project key, the toolbar says what the board is.
		expect(screen.getByTestId("jira-board")).toHaveTextContent("Assigned to me · 2");
	});

	it("starting a ticket from its card does not also open its details", async () => {
		serveBoard();
		daemon.replies["POST /api/v1/projects/{id}/jira/tickets/{issueKey}/start"] = () => ok({ issueKey: "YIA-10", orchestratorId: "orch-1" }, 202);
		renderBoard();
		await userEvent.click(await screen.findByRole("button", { name: "Start YIA-10 with the orchestrator" }));
		await waitFor(() => expect(daemon.calls.some((call) => call.method === "POST")).toBe(true));
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("filters to the tickets assigned to the connected account", async () => {
		serveBoard();
		renderBoard();
		await screen.findByText("Fix login redirect");

		await userEvent.click(screen.getByRole("button", { name: "Assigned to me" }));
		expect(screen.queryByText("Fix login redirect")).not.toBeInTheDocument();
		expect(screen.getByText("Add CSV export")).toBeInTheDocument();
		expect(screen.queryByText("Upgrade React")).not.toBeInTheDocument();
	});

	it("says what to do when Jira is not connected, and opens project settings", async () => {
		daemon.replies["GET /api/v1/projects/{id}/jira"] = () => ok({ connected: false });
		renderBoard();

		await userEvent.click(await screen.findByRole("button", { name: "Connect Jira" }));
		expect(handlers.onOpenSettings).toHaveBeenCalledTimes(1);
		expect(daemon.calls.some((call) => call.path.endsWith("/jira/board"))).toBe(false);
	});

	it("shows a loading state first", async () => {
		serveBoard(() => new Promise(() => undefined));
		renderBoard();
		expect(await screen.findByRole("status")).toHaveTextContent("Loading tickets…");
	});

	it("reports a rejected sign-in and offers the fix in project settings", async () => {
		serveBoard(() => fail("JIRA_AUTH_FAILED", "Jira rejected the email or API token.", 422));
		renderBoard();

		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Jira rejected the sign-in");
		await userEvent.click(within(alert).getByRole("button", { name: "Open project settings" }));
		expect(handlers.onOpenSettings).toHaveBeenCalledTimes(1);
	});

	it("reports an unreachable Jira and retries on request", async () => {
		let attempts = 0;
		serveBoard(() => (++attempts === 1 ? fail("JIRA_UNREACHABLE", "Could not reach Jira.", 503) : ok(board)));
		renderBoard();

		expect(await screen.findByRole("alert")).toHaveTextContent("Can't reach Jira");
		await userEvent.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("Add CSV export")).toBeInTheDocument();
	});

	it("shows the query when no ticket matches", async () => {
		serveBoard(() => ok({ ...board, total: 0, columns: board.columns.map((column) => ({ ...column, tickets: [] })) }));
		renderBoard();

		expect(await screen.findByText("No tickets to show")).toBeInTheDocument();
		expect(screen.getByText(board.jql)).toBeInTheDocument();
	});

	it("keeps the tickets it has when a refresh fails, and says so", async () => {
		let attempts = 0;
		serveBoard(() => (++attempts === 1 ? ok(board) : fail("JIRA_UNREACHABLE", "Could not reach Jira.", 503)));
		renderBoard();
		await screen.findByText("Add CSV export");

		await userEvent.click(screen.getByRole("button", { name: "Refresh tickets" }));
		expect(await screen.findByText(/Couldn't refresh/)).toBeInTheDocument();
		expect(screen.getByText("Add CSV export")).toBeInTheDocument();
	});

	it("starts a ticket with the orchestrator from its card in one tap, then confirms", async () => {
		serveBoard();
		daemon.replies["POST /api/v1/projects/{id}/jira/tickets/{issueKey}/start"] = () => ok({ issueKey: "YIA-10", orchestratorId: "orch-1" }, 202);
		renderBoard();
		await screen.findByText("Add CSV export");

		await userEvent.click(screen.getByRole("button", { name: "Start YIA-10 with the orchestrator" }));

		await waitFor(() => expect(daemon.calls.filter((call) => call.method === "POST")).toHaveLength(1));
		expect(daemon.calls.find((call) => call.method === "POST")).toMatchObject({
			path: "/api/v1/projects/{id}/jira/tickets/{issueKey}/start",
			init: { params: { path: { id: "yoobi-app", issueKey: "YIA-10" } } },
		});
		await waitFor(() => expect(useUiStore.getState().globalToast).toMatchObject({ title: "Sent YIA-10 to the orchestrator", tone: "success" }));
		useUiStore.getState().globalToast?.action?.run();
		expect(handlers.onOpenOrchestrator).toHaveBeenCalledWith("orch-1");
	});

	it("reports a ticket that could not be sent", async () => {
		serveBoard();
		daemon.replies["POST /api/v1/projects/{id}/jira/tickets/{issueKey}/start"] = () => fail("JIRA_UNREACHABLE", "Could not reach Jira.", 503);
		renderBoard();
		await screen.findByText("Add CSV export");

		await userEvent.click(screen.getByRole("button", { name: "Start YIA-10 with the orchestrator" }));
		await waitFor(() => expect(useUiStore.getState().globalToast).toMatchObject({ title: "Could not send YIA-10", tone: "error" }));
	});

	it("opens a ticket's detail from its title, with the same Start button and a direct-worker choice", async () => {
		serveBoard();
		daemon.replies["POST /api/v1/projects/{id}/jira/tickets/{issueKey}/start"] = () => ok({ issueKey: "YIA-10", orchestratorId: "orch-1" }, 202);
		renderBoard();

		await userEvent.click(await screen.findByRole("button", { name: "Open YIA-10 details" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Add CSV export")).toBeInTheDocument();
		expect(within(dialog).getByText("Users want CSV export.")).toBeInTheDocument();
		expect(within(dialog).getByText("Acceptance criteria")).toBeInTheDocument();
		expect(within(dialog).getByText("- exports all rows")).toBeInTheDocument();

		await userEvent.click(within(dialog).getByRole("button", { name: "Start with orchestrator" }));
		await waitFor(() => expect(daemon.calls.some((call) => call.method === "POST")).toBe(true));

		await userEvent.click(within(dialog).getByRole("button", { name: "Start a worker directly" }));
		expect(handlers.onStartWorker).toHaveBeenCalledWith(expect.objectContaining({ key: "YIA-10", summary: "Add CSV export" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});

	it("shows which agents are on a ticket and opens them", async () => {
		const withOrchestratorLink: JiraBoard = {
			...board,
			columns: [{ name: "To Do", tickets: [{ ...board.columns[0].tickets[0], sessions: [{ sessionId: "orch-1", startedAt: "2026-11-16T12:00:00Z" }] }] }],
			total: 1,
		};
		serveBoard(() => ok(withOrchestratorLink));
		const worker = { id: "w1", workspaceId: "yoobi-app", workspaceName: "P", title: "YIA-10 csv export", provider: "claude-code", status: "working", issueId: "jira:YIA-10" } as WorkspaceSession;
		renderBoard([worker]);
		await screen.findByText("Add CSV export");

		await userEvent.click(screen.getByRole("button", { name: "Sent to the orchestrator" }));
		expect(handlers.onOpenOrchestrator).toHaveBeenCalledWith("orch-1");
		await userEvent.click(screen.getByRole("button", { name: "YIA-10 csv export" }));
		expect(handlers.onOpenSession).toHaveBeenCalledWith(worker);
	});
});
