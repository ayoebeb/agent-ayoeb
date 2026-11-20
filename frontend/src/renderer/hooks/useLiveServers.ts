import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { clientForSessionHost } from "../lib/host-clients";
import type { WorkspaceSession } from "../types/workspace";

export type LiveServer = components["schemas"]["LiveServerResponse"];

/** The daemon health-probes each server; a slower poll than this only makes the dots stale. */
export const LIVE_SERVERS_POLL_MS = 10_000;

const EMPTY: LiveServer[] = [];

export const liveServersQueryKey = (projectId: string | undefined, hostId?: string) =>
	["live-servers", hostId ?? "local", projectId ?? ""] as const;

/**
 * The dev servers running for an orchestrator's project, with their health.
 * Polls only while the window is visible (React Query pauses the interval in a
 * hidden window) and only while `enabled`, so a closed control room costs
 * nothing. A remote orchestrator reads the same list from that computer's
 * daemon through the existing host connection.
 */
export function useLiveServers(orchestrator: WorkspaceSession | undefined, enabled = true) {
	const projectId = orchestrator?.workspaceId;
	const hostId = orchestrator?.hostId;
	const watch = enabled && Boolean(projectId) && !orchestrator?.cloud && !orchestrator?.isTerminated;
	const query = useQuery({
		queryKey: liveServersQueryKey(projectId, hostId),
		enabled: watch,
		staleTime: LIVE_SERVERS_POLL_MS / 2,
		refetchInterval: LIVE_SERVERS_POLL_MS,
		refetchIntervalInBackground: false,
		retry: false,
		queryFn: async (): Promise<LiveServer[]> => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/projects/{projectId}/live-servers", {
				params: { path: { projectId: projectId ?? "" } },
			});
			if (error || !data) throw error ?? new Error("live servers unavailable");
			return data.servers ?? EMPTY;
		},
	});
	return { servers: watch ? (query.data ?? EMPTY) : EMPTY, isError: query.isError, isLoading: watch && query.isLoading };
}

/** Stop and restart for one server; both refresh the list when they settle. */
export function useLiveServerActions(orchestrator: WorkspaceSession | undefined) {
	const queryClient = useQueryClient();
	const projectId = orchestrator?.workspaceId ?? "";
	const hostId = orchestrator?.hostId;
	const refresh = () => queryClient.invalidateQueries({ queryKey: liveServersQueryKey(projectId, hostId) });
	const stop = useMutation({
		mutationFn: async (key: string) => {
			const { error } = await clientForSessionHost(hostId).POST("/api/v1/projects/{projectId}/live-servers/stop", {
				params: { path: { projectId } },
				body: { key },
			});
			if (error) throw error;
		},
		onSettled: refresh,
	});
	const restart = useMutation({
		mutationFn: async (key: string) => {
			const { error } = await clientForSessionHost(hostId).POST("/api/v1/projects/{projectId}/live-servers/restart", {
				params: { path: { projectId } },
				body: { key },
			});
			if (error) throw error;
		},
		onSettled: refresh,
	});
	return { stop, restart };
}

/** Chip text: "vite :5173". */
export function liveServerLabel(server: Pick<LiveServer, "name" | "port">): string {
	return `${server.name} :${server.port}`;
}
