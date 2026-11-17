import type { QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiErrorException } from "./api-error-exception";
import { apiClient } from "./api-client";
import { clientForHost } from "./host-clients";
import { LOCAL_HOST, type HostId } from "./hosts";

export type AutopilotRun = components["schemas"]["AutopilotRunResponse"];
export type AutopilotEvent = components["schemas"]["AutopilotEventResponse"];
export type AutopilotView = components["schemas"]["AutopilotViewResponse"];
export type AutopilotReport = components["schemas"]["AutopilotReportResponse"];
export type AutopilotRunOnHost = AutopilotRun & { hostId: HostId };

/** A run that still owns its session: working, paused on the usage limit, or parked for the user. */
export type AutopilotState = AutopilotRun["state"];

const OPEN_STATES: ReadonlySet<string> = new Set(["running", "paused_usage_limit", "parked"]);

export function isOpenAutopilotRun(run: Pick<AutopilotRun, "state"> | undefined): boolean {
	return run !== undefined && OPEN_STATES.has(run.state);
}

/**
 * What the state means for the user, as a tone the badge colours with the status
 * tokens: working is observed progress, attention is "needs you" (parked), paused
 * is caution, and a finished run is plain.
 */
export function autopilotTone(state: AutopilotState): "working" | "attention" | "paused" | "done" | "stopped" {
	switch (state) {
		case "running":
			return "working";
		case "parked":
			return "attention";
		case "paused_usage_limit":
			return "paused";
		case "done":
			return "done";
		default:
			return "stopped";
	}
}

export const autopilotRunsQueryKey = (hostId: HostId) => ["autopilot", "runs", hostId] as const;
export const autopilotViewQueryKey = (hostId: HostId, sessionId: string) => ["autopilot", "view", hostId, sessionId] as const;
const allAutopilotQueryKey = ["autopilot"] as const;

function clientFor(hostId: HostId) {
	return hostId === LOCAL_HOST ? apiClient : clientForHost(hostId);
}

/** A daemon from before Autopilot has no such route; that reads as no runs, not as a failure. */
const routeMissing = (status: number | undefined) => status === 404 || status === 501;

/** Every open run on one host, for the sidebar markers. */
export async function fetchAutopilotRuns(hostId: HostId): Promise<AutopilotRunOnHost[]> {
	const { data, error, response } = await clientFor(hostId).GET("/api/v1/autopilot");
	if (routeMissing(response?.status)) return [];
	if (error || !data) throw apiErrorException(error, "Could not load the Autopilot runs");
	return data.runs.map((run) => ({ ...run, hostId }));
}

const EMPTY_VIEW: AutopilotView = { events: [], parked: [], warnings: [] };

/** One session's run, log and return report. A session that never ran reads as an empty view. */
export async function fetchAutopilotView(hostId: HostId, sessionId: string): Promise<AutopilotView> {
	const { data, error, response } = await clientFor(hostId).GET("/api/v1/sessions/{sessionId}/autopilot", {
		params: { path: { sessionId } },
	});
	// A daemon from before Autopilot, or a session it does not know, has nothing to show.
	if (routeMissing(response?.status)) return EMPTY_VIEW;
	if (error || !data) throw apiErrorException(error, "Could not load Autopilot");
	return data;
}

/** Re-reads everything Autopilot shows: the markers and every open session view. */
export function refreshAutopilot(queryClient: QueryClient): Promise<void> {
	return queryClient.invalidateQueries({ queryKey: allAutopilotQueryKey });
}

/** The open run on one session, if any. */
export function runForSession(
	runs: readonly AutopilotRunOnHost[],
	hostId: HostId,
	sessionId: string,
): AutopilotRunOnHost | undefined {
	return runs.find((run) => run.hostId === hostId && run.sessionId === sessionId);
}

/** The most pressing open run in a project: one that needs the user, then one that works. */
export function runForProject(
	runs: readonly AutopilotRunOnHost[],
	hostId: HostId,
	projectId: string,
): AutopilotRunOnHost | undefined {
	const inProject = runs.filter((run) => run.hostId === hostId && run.projectId === projectId);
	const rank = (run: AutopilotRun) => (run.state === "parked" ? 0 : run.state === "running" ? 1 : 2);
	return [...inProject].sort((a, b) => rank(a) - rank(b))[0];
}

const REPORT_SEEN_KEY = "ao.autopilot.reportSeen";

function readSeen(): Record<string, string> {
	try {
		const parsed: unknown = JSON.parse(globalThis.localStorage?.getItem(REPORT_SEEN_KEY) ?? "{}");
		return parsed && typeof parsed === "object" ? (parsed as Record<string, string>) : {};
	} catch {
		return {};
	}
}

/**
 * Whether a run that ended while the user was elsewhere still has a return report
 * they have not opened. Held in this app only: opening the report is the user's
 * own acknowledgement, and no other client needs it.
 */
export function hasUnseenReport(hostId: HostId, view: Pick<AutopilotView, "run"> | undefined): boolean {
	const run = view?.run;
	if (!run || isOpenAutopilotRun(run)) return false;
	return readSeen()[`${hostId}:${run.sessionId}`] !== run.id;
}

export function markReportSeen(hostId: HostId, view: Pick<AutopilotView, "run"> | undefined): void {
	const run = view?.run;
	if (!run || isOpenAutopilotRun(run)) return;
	try {
		globalThis.localStorage?.setItem(
			REPORT_SEEN_KEY,
			JSON.stringify({ ...readSeen(), [`${hostId}:${run.sessionId}`]: run.id }),
		);
	} catch {
		// A full or blocked store only means the dot shows again; nothing is lost.
	}
}

/** The notification copy the daemon writes for a parked item, which the desktop shows in the user's language. */
export const NOTIFICATION_TYPE_AUTOPILOT_PARKED = "autopilot_parked";
export const AUTOPILOT_PARKED_TITLE_SUFFIX = ": Autopilot parked an item for you";
