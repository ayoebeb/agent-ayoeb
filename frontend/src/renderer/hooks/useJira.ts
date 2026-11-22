import { useQuery } from "@tanstack/react-query";
import {
	getJiraBoard,
	getJiraConnection,
	JIRA_BOARD_REFRESH_MS,
	jiraFailureKind,
	jiraQueryKeys,
} from "../lib/jira";

/** Whether the project is connected to Jira. Never carries the API token. */
export function useJiraConnection(projectId: string | undefined, hostId?: string, enabled = true) {
	return useQuery({
		queryKey: jiraQueryKeys.connection(projectId ?? "", hostId),
		queryFn: () => getJiraConnection(projectId ?? "", hostId),
		enabled: enabled && Boolean(projectId),
		staleTime: 30_000,
		retry: false,
	});
}

/**
 * The project's Jira tickets in board columns, re-read every minute while the
 * Jira view is open. A failure the person has to fix (wrong token, bad query,
 * not connected) stops the polling until they change something; an offline
 * Jira keeps being retried.
 */
export function useJiraBoard(projectId: string | undefined, hostId: string | undefined, enabled: boolean) {
	return useQuery({
		queryKey: jiraQueryKeys.board(projectId ?? "", hostId),
		queryFn: () => getJiraBoard(projectId ?? "", hostId),
		enabled: enabled && Boolean(projectId),
		staleTime: 15_000,
		retry: false,
		refetchInterval: (query) => {
			const kind = query.state.error ? jiraFailureKind(query.state.error) : undefined;
			return kind === "auth" || kind === "not_connected" || kind === "bad_query" ? false : JIRA_BOARD_REFRESH_MS;
		},
		refetchIntervalInBackground: false,
	});
}
