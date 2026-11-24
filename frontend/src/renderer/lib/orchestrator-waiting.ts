import type { QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { appI18n } from "../i18n";
import { apiErrorException } from "./api-error-exception";
import { apiClient } from "./api-client";
import { AUTOPILOT_PARKED_TITLE_SUFFIX, NOTIFICATION_TYPE_AUTOPILOT_PARKED } from "./autopilot";
import { clientForHost } from "./host-clients";
import { LOCAL_HOST, type HostId } from "./hosts";

/** One project orchestrator that is waiting for the user, on this computer or a remote host. */
export type OrchestratorWaiting = components["schemas"]["OrchestratorWaitingResponse"] & { hostId: HostId };

export const NOTIFICATION_TYPE_ORCHESTRATOR_WAITING = "orchestrator_waiting";

/**
 * The daemon has no UI language, so it stores this notification in English like
 * every other one (phone push, CLI, API): "<project>: orchestrator is waiting for
 * you", with the one-line question as the body, or the fallback below when the
 * wait is an approval with no declared question. The desktop shows it in the
 * user's own language instead. These two constants are copies of the daemon's
 * (notify.OrchestratorWaitingTitleSuffix and notify.OrchestratorWaitingFallbackBody);
 * a daemon test and a renderer test fail if they drift.
 */
export const ORCHESTRATOR_WAITING_TITLE_SUFFIX = ": orchestrator is waiting for you";
export const ORCHESTRATOR_WAITING_FALLBACK_BODY = "The orchestrator is waiting for your answer or approval.";

/**
 * What the user reads for a notification: an orchestrator wait in their own
 * language, anything else exactly as the daemon wrote it. A title that does not
 * end with the daemon's suffix (a daemon from another build) is shown unchanged
 * rather than guessed at. The question the orchestrator wrote is never altered.
 */
export function localizedNotificationCopy(notification: { type: string; title: string; body: string }): {
	title: string;
	body: string;
} {
	if (notification.type === NOTIFICATION_TYPE_AUTOPILOT_PARKED) {
		// "<session>: Autopilot parked an item for you". The item text after it is
		// the agent's own and is never altered.
		const session = notification.title.endsWith(AUTOPILOT_PARKED_TITLE_SUFFIX)
			? notification.title.slice(0, notification.title.length - AUTOPILOT_PARKED_TITLE_SUFFIX.length)
			: undefined;
		return {
			title: session === undefined ? notification.title : appI18n.t("notify.autopilotParked.title", { session }),
			body: notification.body,
		};
	}
	if (notification.type !== NOTIFICATION_TYPE_ORCHESTRATOR_WAITING) {
		return { title: notification.title, body: notification.body };
	}
	const { title, body } = notification;
	const project = title.endsWith(ORCHESTRATOR_WAITING_TITLE_SUFFIX)
		? title.slice(0, title.length - ORCHESTRATOR_WAITING_TITLE_SUFFIX.length)
		: undefined;
	return {
		title: project === undefined ? title : appI18n.t("notify.orchestratorWaiting.title", { project }),
		body: body.trim() === "" || body === ORCHESTRATOR_WAITING_FALLBACK_BODY ? appI18n.t("notify.orchestratorWaiting.body") : body,
	};
}

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
