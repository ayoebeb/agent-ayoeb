import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { clientForHost } from "../lib/host-clients";
import { LOCAL_HOST, type HostId } from "../lib/hosts";
import {
	autopilotRunsQueryKey,
	autopilotViewQueryKey,
	fetchAutopilotRuns,
	fetchAutopilotView,
	isOpenAutopilotRun,
	refreshAutopilot,
	runForProject,
	type AutopilotRunOnHost,
	type AutopilotView,
} from "../lib/autopilot";
import { useConnectedHosts } from "./useHostConnection";

/**
 * Every open Autopilot run on this computer and each connected host, for the
 * sidebar markers. The daemon is the source of truth and Autopilot changes state
 * on its own (it parks, finishes, pauses), so the markers poll; a mutation made
 * here refreshes them at once.
 */
export function useAutopilotRuns(): { runs: AutopilotRunOnHost[] } {
	const connected = useConnectedHosts();
	const hosts = useMemo(() => [LOCAL_HOST, ...connected], [connected]);
	const queries = useQueries({
		queries: hosts.map((hostId) => ({
			queryKey: autopilotRunsQueryKey(hostId),
			queryFn: () => fetchAutopilotRuns(hostId),
			retry: 1,
			refetchInterval: 10_000,
		})),
	});
	// A failed read keeps the last answer rather than dropping a marker the user may still need.
	return { runs: queries.flatMap((query) => query.data ?? []) };
}

/** The open run in one project, for the sidebar row. */
export function useProjectAutopilot(hostId: HostId, projectId: string): AutopilotRunOnHost | undefined {
	const { runs } = useAutopilotRuns();
	return runForProject(runs, hostId, projectId);
}

/** One session's run, log and return report. */
export function useAutopilotView(hostId: HostId, sessionId: string | undefined, enabled = true) {
	return useQuery({
		queryKey: autopilotViewQueryKey(hostId, sessionId ?? ""),
		queryFn: () => fetchAutopilotView(hostId, sessionId as string),
		enabled: enabled && Boolean(sessionId),
		retry: 1,
		// A session that has no open run cannot change on its own, and every session
		// switch mounts a fresh read: it is fetched once and kept for a while, and
		// only a session with an open run (which parks, pauses and finishes by
		// itself) is polled.
		staleTime: 30_000,
		refetchInterval: (query: { state: { data?: AutopilotView } }) => (isOpenAutopilotRun(query.state.data?.run) ? 8_000 : false),
	});
}

function clientFor(hostId: HostId) {
	return hostId === LOCAL_HOST ? apiClient : clientForHost(hostId);
}

/**
 * The user's three Autopilot actions on one session, plus marking a parked item
 * handled. Each refreshes every Autopilot read on success, so the badge and the
 * log never claim a change the daemon did not make.
 */
export function useAutopilotActions(hostId: HostId, sessionId: string) {
	const queryClient = useQueryClient();
	const sessionPath = { params: { path: { sessionId } } };
	const done = async () => {
		await refreshAutopilot(queryClient);
	};

	const start = useMutation({
		mutationFn: async (input: { task: string; maxIterations?: number }): Promise<AutopilotView> => {
			const { data, error } = await clientFor(hostId).POST("/api/v1/sessions/{sessionId}/autopilot", {
				...sessionPath,
				body: { task: input.task, ...(input.maxIterations ? { maxIterations: input.maxIterations } : {}) },
			});
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: done,
	});
	const stop = useMutation({
		mutationFn: async () => {
			const { data, error } = await clientFor(hostId).POST("/api/v1/sessions/{sessionId}/autopilot/stop", sessionPath);
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: done,
	});
	const resume = useMutation({
		mutationFn: async () => {
			const { data, error } = await clientFor(hostId).POST("/api/v1/sessions/{sessionId}/autopilot/resume", sessionPath);
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: done,
	});
	const resolveItem = useMutation({
		mutationFn: async (eventId: string) => {
			const { data, error } = await clientFor(hostId).POST("/api/v1/autopilot/items/{eventId}/resolve", {
				params: { path: { eventId } },
			});
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: done,
	});
	const undoDecision = useMutation({
		mutationFn: async (eventId: string) => {
			const { data, error } = await clientFor(hostId).POST("/api/v1/autopilot/items/{eventId}/undo", {
				params: { path: { eventId } },
			});
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: done,
	});

	return {
		start,
		stop,
		resume,
		resolveItem,
		undoDecision,
		error: [start, stop, resume, resolveItem, undoDecision].map((m) => m.error).find(Boolean)?.message,
	};
}
