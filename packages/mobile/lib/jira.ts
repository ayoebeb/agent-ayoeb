import { ApiError, apiRequest } from "./api";
import type { ServerConfig } from "./config";
import { UnreachableError } from "./connectionError";

/**
 * A project's Jira tickets, read through the paired machine. The machine holds
 * the Jira credentials: the phone only ever asks it for the board and tells it
 * which ticket to start, so no Jira token is stored here.
 */
export type JiraTicket = {
	key: string;
	summary: string;
	url: string;
	status: { id: string; name: string; category: string };
	assignee?: { accountId: string; displayName: string };
	issueType?: string;
	priority?: string;
	/** Sessions the ticket was sent to, oldest first. */
	sessions?: { sessionId: string; startedAt: string }[];
};

export type JiraBoardColumn = { name: string; other?: boolean; tickets: JiraTicket[] };

export type JiraBoard = {
	siteUrl: string;
	projectKey?: string;
	boardName?: string;
	/** The connected Jira account, so "mine" can be told apart. */
	accountId?: string;
	total: number;
	truncated?: boolean;
	columns: JiraBoardColumn[];
	fetchedAt: string;
};

export type JiraStartResult = { issueKey: string; orchestratorId: string; spawned?: boolean; duplicate?: boolean };

const jiraPath = (projectId: string) => `/api/v1/projects/${encodeURIComponent(projectId)}/jira`;

export async function getJiraBoard(cfg: ServerConfig, projectId: string, init?: Pick<RequestInit, "signal">): Promise<JiraBoard> {
	const res = await apiRequest(cfg, `${jiraPath(projectId)}/board`, init);
	return (await res.json()) as JiraBoard;
}

/** Sends the ticket to the project's orchestrator. The machine reads the ticket from Jira itself. */
export async function startJiraTicket(cfg: ServerConfig, projectId: string, issueKey: string): Promise<JiraStartResult> {
	const res = await apiRequest(cfg, `${jiraPath(projectId)}/tickets/${encodeURIComponent(issueKey)}/start`, { method: "POST" });
	return (await res.json()) as JiraStartResult;
}

/** What the Jira page should tell the person when a request fails. */
export type JiraFailure = "not_connected" | "auth" | "offline" | "bad_query" | "other";

export function jiraFailure(error: unknown): JiraFailure {
	if (error instanceof UnreachableError) return "offline";
	if (error instanceof ApiError) {
		switch (error.code) {
			case "JIRA_NOT_CONNECTED":
				return "not_connected";
			case "JIRA_AUTH_FAILED":
				return "auth";
			case "JIRA_UNREACHABLE":
				return "offline";
			case "JIRA_BAD_QUERY":
				return "bad_query";
			default:
				return "other";
		}
	}
	return "other";
}

/** The sections of the page: one per board column that has tickets, optionally only the ones assigned to the account. */
export function jiraSections(board: JiraBoard, mineOnly: boolean): { key: string; title: string; data: JiraTicket[] }[] {
	return board.columns
		.map((column) => ({
			key: `${column.other ? "other" : "col"}:${column.name}`,
			title: column.other ? "Other" : column.name,
			data: mineOnly && board.accountId ? column.tickets.filter((ticket) => ticket.assignee?.accountId === board.accountId) : column.tickets,
		}))
		.filter((section) => section.data.length > 0);
}
