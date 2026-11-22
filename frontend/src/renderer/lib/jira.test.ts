import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";

const { getMock, postMock, putMock, deleteMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	putMock: vi.fn(),
	deleteMock: vi.fn(),
}));

vi.mock("./host-clients", () => ({
	clientForSessionHost: () => ({ GET: getMock, POST: postMock, PUT: putMock, DELETE: deleteMock }),
}));

import {
	columnTone,
	connectJira,
	disconnectJira,
	getJiraBoard,
	getJiraConnection,
	initialsOf,
	jiraFailureKind,
	JiraRequestError,
	latestSentSessionId,
	onlyAssignedTo,
	sameJiraSite,
	sessionsForTicket,
	startJiraTicket,
	testJira,
	ticketTaskBrief,
	type JiraBoard,
	type JiraTicket,
} from "./jira";

function ticket(key: string, overrides: Partial<JiraTicket> = {}): JiraTicket {
	return {
		key, summary: `Summary of ${key}`, url: `https://yoobi.atlassian.net/browse/${key}`,
		status: { id: "1", name: "To Do", category: "new" }, updatedAt: "2026-11-10T08:30:00Z", ...overrides,
	};
}

function session(overrides: Partial<WorkspaceSession>): WorkspaceSession {
	return { id: "s1", workspaceId: "p1", workspaceName: "P", title: "Some task", provider: "claude-code", status: "working", ...overrides } as WorkspaceSession;
}

beforeEach(() => {
	for (const mock of [getMock, postMock, putMock, deleteMock]) mock.mockReset();
});

describe("Jira API calls", () => {
	it("reads the connection and board for a project through the typed client", async () => {
		getMock.mockResolvedValueOnce({ data: { connected: true }, response: { status: 200 } });
		await expect(getJiraConnection("yoobi-app")).resolves.toEqual({ connected: true });
		expect(getMock).toHaveBeenCalledWith("/api/v1/projects/{id}/jira", { params: { path: { id: "yoobi-app" } } });

		getMock.mockResolvedValueOnce({ data: { columns: [] }, response: { status: 200 } });
		await getJiraBoard("yoobi-app", "host-2");
		expect(getMock).toHaveBeenLastCalledWith("/api/v1/projects/{id}/jira/board", { params: { path: { id: "yoobi-app" } } });
	});

	it("sends the connection form, test and disconnect to the project's routes", async () => {
		const input = { siteUrl: "yoobi.atlassian.net", email: "ada@example.com", apiToken: "secret-token", projectKey: "YIA" };
		putMock.mockResolvedValueOnce({ data: { connected: true }, response: { status: 200 } });
		await connectJira("yoobi-app", input);
		expect(putMock).toHaveBeenCalledWith("/api/v1/projects/{id}/jira", { params: { path: { id: "yoobi-app" } }, body: input });

		postMock.mockResolvedValueOnce({ data: { accountName: "Ada", jql: "x", ticketCount: 3 }, response: { status: 200 } });
		await testJira("yoobi-app", input);
		expect(postMock).toHaveBeenCalledWith("/api/v1/projects/{id}/jira/test", { params: { path: { id: "yoobi-app" } }, body: input });

		deleteMock.mockResolvedValueOnce({ data: { status: "ok" }, response: { status: 200 } });
		await disconnectJira("yoobi-app");
		expect(deleteMock).toHaveBeenCalledWith("/api/v1/projects/{id}/jira", { params: { path: { id: "yoobi-app" } } });
	});

	it("starts a ticket by project and key, with no ticket text from the client", async () => {
		postMock.mockResolvedValueOnce({ data: { issueKey: "YIA-10", orchestratorId: "orch-1" }, response: { status: 202 } });
		await expect(startJiraTicket("yoobi-app", "YIA-10")).resolves.toMatchObject({ orchestratorId: "orch-1" });
		expect(postMock).toHaveBeenCalledWith("/api/v1/projects/{id}/jira/tickets/{issueKey}/start", { params: { path: { id: "yoobi-app", issueKey: "YIA-10" } } });
	});

	it("turns an error envelope into a JiraRequestError carrying its code and status", async () => {
		getMock.mockResolvedValueOnce({ error: { code: "JIRA_AUTH_FAILED", message: "Jira rejected the email or API token." }, response: { status: 422 } });
		const error = await getJiraBoard("yoobi-app").catch((cause: unknown) => cause);
		expect(error).toBeInstanceOf(JiraRequestError);
		expect(error).toMatchObject({ code: "JIRA_AUTH_FAILED", status: 422, message: "Jira rejected the email or API token." });
	});
});

describe("jiraFailureKind", () => {
	it("tells the person what to fix", () => {
		expect(jiraFailureKind(new JiraRequestError("x", 409, "JIRA_NOT_CONNECTED"))).toBe("not_connected");
		expect(jiraFailureKind(new JiraRequestError("x", 422, "JIRA_AUTH_FAILED"))).toBe("auth");
		expect(jiraFailureKind(new JiraRequestError("x", 503, "JIRA_UNREACHABLE"))).toBe("offline");
		expect(jiraFailureKind(new JiraRequestError("x", 400, "JIRA_BAD_QUERY"))).toBe("bad_query");
		expect(jiraFailureKind(new JiraRequestError("x", 502, "JIRA_UPSTREAM_ERROR"))).toBe("other");
		expect(jiraFailureKind(new JiraRequestError("x", 0))).toBe("offline");
		expect(jiraFailureKind(new TypeError("Failed to fetch"))).toBe("offline");
		expect(jiraFailureKind(new Error("boom"))).toBe("other");
	});
});

describe("sessionsForTicket", () => {
	const sessions = [
		session({ id: "a", title: "Spawned for the ticket", issueId: "jira:YIA-10" }),
		session({ id: "b", title: "YIA-10: Add CSV export" }),
		session({ id: "c", title: "Branch fix/yia-10-export" }),
		session({ id: "d", title: "YIA-100 is another ticket" }),
		session({ id: "e", title: "Unrelated", issueId: "github:acme/app#10" }),
		session({ id: "orch", title: "YIA-10 coordination", kind: "orchestrator" }),
	];

	it("finds workers by issue id or by the key in the title, and nothing else", () => {
		expect(sessionsForTicket("YIA-10", sessions).map((s) => s.id)).toEqual(["a", "b", "c"]);
		expect(sessionsForTicket("YIA-11", sessions)).toEqual([]);
	});
});

describe("board helpers", () => {
	const ada = { accountId: "acc-1", displayName: "Ada Example" };
	const board: JiraBoard = {
		siteUrl: "https://yoobi.atlassian.net", jql: "project = YIA", total: 3, fetchedAt: "2026-11-16T12:00:00Z", accountId: "acc-1",
		columns: [
			{ name: "To Do", tickets: [ticket("YIA-1", { assignee: ada }), ticket("YIA-2", { assignee: { accountId: "acc-2", displayName: "Bo" } })] },
			{ name: "Done", tickets: [ticket("YIA-3", { status: { id: "5", name: "Done", category: "done" } })] },
		],
	};

	it("keeps only the connected account's tickets, per column, and recounts", () => {
		const mine = onlyAssignedTo(board, "acc-1");
		expect(mine.columns.map((c) => c.tickets.map((t) => t.key))).toEqual([["YIA-1"], []]);
		expect(mine.total).toBe(1);
		expect(onlyAssignedTo(board, undefined)).toBe(board);
	});

	it("reads column tone from the ticket workflow category", () => {
		expect(columnTone(board.columns[0])).toBe("todo");
		expect(columnTone(board.columns[1])).toBe("done");
		expect(columnTone({ name: "Doing", tickets: [ticket("YIA-4", { status: { id: "3", name: "Doing", category: "indeterminate" } })] })).toBe("progress");
		expect(columnTone({ name: "Empty", tickets: [] })).toBe("todo");
	});

	it("makes avatar initials from one or two names", () => {
		expect(initialsOf("Ada Example")).toBe("AE");
		expect(initialsOf("ada")).toBe("A");
		expect(initialsOf("  Jan van der Berg ")).toBe("JB");
		expect(initialsOf("")).toBe("");
	});

	it("names the session a ticket was last sent to", () => {
		expect(latestSentSessionId(ticket("YIA-1"))).toBeUndefined();
		expect(latestSentSessionId(ticket("YIA-1", { sessions: [{ sessionId: "orch-1", startedAt: "a" }, { sessionId: "orch-2", startedAt: "b" }] }))).toBe("orch-2");
	});
});

describe("ticketTaskBrief", () => {
	it("leads with the key and carries the link, description and acceptance criteria", () => {
		const brief = ticketTaskBrief(ticket("YIA-10", { summary: "Add CSV export", description: "Users want CSV.", acceptanceCriteria: "- exports all rows" }));
		expect(brief).toBe("YIA-10: Add CSV export\n\nLink: https://yoobi.atlassian.net/browse/YIA-10\n\nDescription:\nUsers want CSV.\n\nAcceptance criteria:\n- exports all rows");
		expect(ticketTaskBrief(ticket("YIA-11"))).toBe("YIA-11: Summary of YIA-11\n\nLink: https://yoobi.atlassian.net/browse/YIA-11");
	});
});

describe("sameJiraSite", () => {
	it("compares sites the way the daemon does, so a saved token is kept only for the same site", () => {
		expect(sameJiraSite("https://yoobi.atlassian.net", "yoobi.atlassian.net")).toBe(true);
		expect(sameJiraSite("https://yoobi.atlassian.net/", "HTTPS://YOOBI.Atlassian.net/jira/software/projects/YIA/boards/1")).toBe(true);
		expect(sameJiraSite("https://yoobi.atlassian.net", "https://other.atlassian.net")).toBe(false);
		expect(sameJiraSite("http://127.0.0.1:47810", "http://127.0.0.1:47811")).toBe(false);
		expect(sameJiraSite("http://127.0.0.1:47810", "https://127.0.0.1:47810")).toBe(false);
	});
});
import { hasOtherAssignees, ticketCategoryTextClass, ticketMeta } from "./jira";

describe("card helpers", () => {
	const ticket = (overrides: Partial<JiraTicket> = {}): JiraTicket => ({
		key: "WEB-12", summary: "S", url: "https://x.atlassian.net/browse/WEB-12", status: { id: "1", name: "To Do", category: "new" }, ...overrides,
	});

	it("builds the meta line from the key, type and priority, skipping what Jira left out", () => {
		expect(ticketMeta(ticket({ issueType: "Bug", priority: "High" }))).toEqual(["WEB-12", "Bug", "High"]);
		expect(ticketMeta(ticket({ priority: "High" }))).toEqual(["WEB-12", "High"]);
		expect(ticketMeta(ticket())).toEqual(["WEB-12"]);
	});

	it("colours a status by its workflow category", () => {
		expect(ticketCategoryTextClass("new")).toBe("text-muted-foreground");
		expect(ticketCategoryTextClass(undefined)).toBe("text-muted-foreground");
		expect(ticketCategoryTextClass("indeterminate")).toBe("text-status-working");
		expect(ticketCategoryTextClass("done")).toBe("text-status-ready");
	});

	it("knows when a board holds someone else's tickets", () => {
		const board = (assignees: (string | undefined)[], accountId: string | null = "me"): JiraBoard => ({
			siteUrl: "", jql: "", total: assignees.length, fetchedAt: "", accountId: accountId ?? undefined,
			columns: [{ name: "To Do", tickets: assignees.map((id, i) => ticket({ key: `A-${i}`, assignee: id ? { accountId: id, displayName: id } : undefined })) }],
		});
		expect(hasOtherAssignees(board(["me", "me", undefined]))).toBe(false);
		expect(hasOtherAssignees(board(["me", "bo"]))).toBe(true);
		expect(hasOtherAssignees(board(["bo"], null))).toBe(false);
	});
});
