import type { components } from "../../api/schema";
import type { WorkspaceSession } from "../types/workspace";
import { apiErrorCode, apiErrorMessage } from "./api-client";
import { clientForSessionHost } from "./host-clients";
import { markApiError } from "./reported-errors";

export type JiraConnection = components["schemas"]["JiraConnection"];
export type JiraConnectInput = components["schemas"]["JiraConnectRequest"];
export type JiraTestResult = components["schemas"]["JiraTestResult"];
export type JiraBoard = components["schemas"]["JiraBoard"];
export type JiraBoardColumn = components["schemas"]["JiraBoardColumn"];
export type JiraTicket = components["schemas"]["JiraTicket"];
export type JiraStartResult = components["schemas"]["JiraStartResult"];

/** How often an open Jira view re-reads the board. */
export const JIRA_BOARD_REFRESH_MS = 60_000;

export const jiraQueryKeys = {
	connection: (projectId: string, hostId?: string) => ["jira", "connection", hostId ?? "local", projectId] as const,
	board: (projectId: string, hostId?: string) => ["jira", "board", hostId ?? "local", projectId] as const,
};

/** What the Jira view should tell the person when a request fails. */
export type JiraFailureKind = "not_connected" | "auth" | "offline" | "bad_query" | "other";

/** A failed Jira request, with the daemon's stable code. It never carries the API token. */
export class JiraRequestError extends Error {
	constructor(
		message: string,
		readonly status: number,
		readonly code?: string,
	) {
		super(message);
		this.name = "JiraRequestError";
		markApiError(this);
	}
}

export function jiraFailureKind(error: unknown): JiraFailureKind {
	if (error instanceof JiraRequestError) {
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
				// 0 is "no answer at all": the daemon, or a remote host, is not reachable.
				return error.status === 0 ? "offline" : "other";
		}
	}
	// fetch() rejects with a TypeError when the daemon cannot be reached.
	return error instanceof TypeError ? "offline" : "other";
}

function toError(error: unknown, status: number): JiraRequestError {
	return new JiraRequestError(apiErrorMessage(error, `Jira request failed (${status})`), status, apiErrorCode(error));
}

type Result<T> = { data?: T; error?: unknown; response: { status: number } };

function unwrap<T>({ data, error, response }: Result<T>): T {
	if (error !== undefined || data === undefined) throw toError(error, response.status);
	return data;
}

export async function getJiraConnection(projectId: string, hostId?: string): Promise<JiraConnection> {
	return unwrap(await clientForSessionHost(hostId).GET("/api/v1/projects/{id}/jira", { params: { path: { id: projectId } } }));
}

export async function connectJira(projectId: string, input: JiraConnectInput, hostId?: string): Promise<JiraConnection> {
	return unwrap(await clientForSessionHost(hostId).PUT("/api/v1/projects/{id}/jira", { params: { path: { id: projectId } }, body: input }));
}

export async function testJira(projectId: string, input: JiraConnectInput, hostId?: string): Promise<JiraTestResult> {
	return unwrap(await clientForSessionHost(hostId).POST("/api/v1/projects/{id}/jira/test", { params: { path: { id: projectId } }, body: input }));
}

export async function disconnectJira(projectId: string, hostId?: string): Promise<void> {
	unwrap(await clientForSessionHost(hostId).DELETE("/api/v1/projects/{id}/jira", { params: { path: { id: projectId } } }));
}

export async function getJiraBoard(projectId: string, hostId?: string): Promise<JiraBoard> {
	return unwrap(await clientForSessionHost(hostId).GET("/api/v1/projects/{id}/jira/board", { params: { path: { id: projectId } } }));
}

export async function startJiraTicket(projectId: string, issueKey: string, hostId?: string): Promise<JiraStartResult> {
	return unwrap(await clientForSessionHost(hostId).POST("/api/v1/projects/{id}/jira/tickets/{issueKey}/start", {
		params: { path: { id: projectId, issueKey } },
	}));
}

/** The session a ticket was last sent to (an orchestrator), if it was sent at all. */
export function latestSentSessionId(ticket: JiraTicket): string | undefined {
	const sent = ticket.sessions ?? [];
	return sent.length > 0 ? sent[sent.length - 1].sessionId : undefined;
}

/**
 * Whether two Jira site addresses name the same site, the way the daemon reads
 * them: scheme and host, case-insensitive, any path ignored. The daemon only reuses
 * a saved API token for the site and email it was saved for.
 */
export function sameJiraSite(a: string, b: string): boolean {
	const origin = (raw: string): string => {
		const text = raw.trim();
		try {
			return new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(text) ? text : `https://${text}`).origin.toLowerCase();
		} catch {
			return text.toLowerCase();
		}
	};
	return origin(a) === origin(b);
}

/** The ids a session started for a ticket carries (`ao spawn --issue jira:KEY`). */
export function jiraIssueRef(key: string): string {
	return `jira:${key}`;
}

function escapeRegExp(text: string): string {
	return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Worker sessions that are working on a ticket: the ones the orchestrator
 * spawned with the ticket's issue id, plus any session titled with the key
 * (a task started from the ticket's brief, which opens with it).
 */
export function sessionsForTicket(ticketKey: string, sessions: readonly WorkspaceSession[]): WorkspaceSession[] {
	const ref = jiraIssueRef(ticketKey);
	const inTitle = new RegExp(`(^|[^A-Za-z0-9])${escapeRegExp(ticketKey)}($|[^0-9])`, "i");
	return sessions.filter((session) => session.kind !== "orchestrator" && (session.issueId === ref || inTitle.test(session.title)));
}

/** The board with only the tickets assigned to the connected Jira account. */
export function onlyAssignedTo(board: JiraBoard, accountId: string | undefined): JiraBoard {
	if (!accountId) return board;
	const columns = board.columns.map((column) => ({ ...column, tickets: column.tickets.filter((ticket) => ticket.assignee?.accountId === accountId) }));
	return { ...board, columns, total: columns.reduce((sum, column) => sum + column.tickets.length, 0) };
}

/** Up to two letters for an avatar: "Ada Example" is AE. */
export function initialsOf(name: string): string {
	const parts = name.trim().split(/\s+/).filter(Boolean);
	if (parts.length === 0) return "";
	const first = Array.from(parts[0])[0] ?? "";
	const last = parts.length > 1 ? Array.from(parts[parts.length - 1])[0] ?? "" : "";
	return (first + last).toUpperCase();
}

/**
 * Dot tone for a column header, from the Jira workflow category of its tickets
 * (to do, in progress or done). An empty column reads as to do.
 */
export function columnTone(column: JiraBoardColumn): "todo" | "progress" | "done" {
	const category = column.tickets[0]?.status.category;
	if (category === "done") return "done";
	if (category === "indeterminate") return "progress";
	return "todo";
}

/**
 * The text a New Task opens with when a ticket goes straight to a worker. It
 * leads with the key so the worker's session is found under the ticket.
 */
export function ticketTaskBrief(ticket: JiraTicket): string {
	const parts = [`${ticket.key}: ${ticket.summary}`, `Link: ${ticket.url}`];
	if (ticket.description?.trim()) parts.push(`Description:\n${ticket.description.trim()}`);
	if (ticket.acceptanceCriteria?.trim()) parts.push(`Acceptance criteria:\n${ticket.acceptanceCriteria.trim()}`);
	return parts.join("\n\n");
}

/** The card's small meta line as parts: the key first, then type and priority when Jira gave them. */
export function ticketMeta(ticket: JiraTicket): string[] {
	return [ticket.key, ticket.issueType, ticket.priority].filter((part): part is string => Boolean(part));
}

/** Text colour for a ticket's status label, from its Jira workflow category. */
export function ticketCategoryTextClass(category: string | undefined): string {
	if (category === "done") return "text-status-ready";
	if (category === "indeterminate") return "text-status-working";
	return "text-muted-foreground";
}

/** Whether any ticket on the board is assigned to someone other than the connected account. */
export function hasOtherAssignees(board: JiraBoard): boolean {
	if (!board.accountId) return false;
	return board.columns.some((column) => column.tickets.some((ticket) => ticket.assignee && ticket.assignee.accountId !== board.accountId));
}
