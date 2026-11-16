import type { QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiErrorException } from "./api-error-exception";
import { apiClient } from "./api-client";
import { clientForHost } from "./host-clients";
import { LOCAL_HOST, type HostId } from "./hosts";

/** One project orchestrator that is waiting for the user, on this computer or a remote host. */
export type OrchestratorWaiting = components["schemas"]["OrchestratorWaitingResponse"] & { hostId: HostId };

export const NOTIFICATION_TYPE_ORCHESTRATOR_WAITING = "orchestrator_waiting";

export const orchestratorWaitingQueryKey = (hostId: HostId) => ["orchestrator-waiting", hostId] as const;
const allOrchestratorWaitingQueryKey = ["orchestrator-waiting"] as const;

/**
 * Every open wait on one host. The daemon lists waits whose notification the
 * user already cleared: clearing the notification panel acknowledges the alert,
 * not the question, so the sidebar marker must outlive it.
 *
 * A host running a build from before this existed has no such route. That is not
 * a failure the user can act on, so it reads as nothing waiting.
 */
export async function fetchOrchestratorWaiting(hostId: HostId): Promise<OrchestratorWaiting[]> {
	const client = hostId === LOCAL_HOST ? apiClient : clientForHost(hostId);
	const { data, error, response } = await client.GET("/api/v1/orchestrator-waiting");
	if (response?.status === 404 || response?.status === 501) return [];
	if (error || !data) throw apiErrorException(error, "Could not load the orchestrators waiting for you");
	return data.waiting.map((item) => ({ ...item, hostId }));
}

/**
 * Re-reads the waits that are on screen: a wait was created, answered or
 * reminded about, or a stream (re)connected. Only mounted readers refetch, which
 * is the sidebar and badge; nothing else needs the answer.
 */
export function refreshOrchestratorWaiting(queryClient: QueryClient, hostId?: HostId): void {
	void queryClient.refetchQueries({
		queryKey: hostId === undefined ? allOrchestratorWaitingQueryKey : orchestratorWaitingQueryKey(hostId),
		type: "active",
	});
}

/**
 * How many waits the app badge must add to the unread count. The unread count
 * already includes a wait whose notification is still unread; once the user
 * opens or clears the notification panel that notification stops counting while
 * the orchestrator is still waiting, and the badge is what keeps saying so.
 */
export function waitsBeyondUnread(
	waiting: readonly OrchestratorWaiting[],
	unreadNotificationIds: ReadonlySet<string>,
): number {
	return waiting.filter((item) => !unreadNotificationIds.has(`${item.hostId}:${item.notificationId}`)).length;
}

/** The wait on a project's orchestrator, if the project's orchestrator is waiting. */
export function waitingForProject(
	waiting: readonly OrchestratorWaiting[],
	hostId: HostId,
	projectId: string,
): OrchestratorWaiting | undefined {
	return waiting.find((item) => item.hostId === hostId && item.projectId === projectId);
}

/** The wait on one orchestrator session. */
export function waitingForSession(
	waiting: readonly OrchestratorWaiting[],
	hostId: HostId,
	sessionId: string,
): OrchestratorWaiting | undefined {
	return waiting.find((item) => item.hostId === hostId && item.sessionId === sessionId);
}
