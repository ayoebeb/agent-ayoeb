import { useQueries } from "@tanstack/react-query";
import { useMemo } from "react";
import { LOCAL_HOST } from "../lib/hosts";
import {
	fetchOrchestratorWaiting,
	orchestratorWaitingQueryKey,
	waitingForProject,
	type OrchestratorWaiting,
} from "../lib/orchestrator-waiting";
import type { HostId } from "../lib/hosts";
import { useConnectedHosts } from "./useHostConnection";

/**
 * Every orchestrator waiting for the user, on this computer and on each
 * connected remote host. It is kept live by notification events (the local
 * stream and each remote stream refetch it); the slow poll only covers a stream
 * that is down, so a marker cannot stay up for an answered question for long.
 */
export function useOrchestratorWaiting(): { waiting: OrchestratorWaiting[] } {
	const connected = useConnectedHosts();
	const hosts = useMemo(() => [LOCAL_HOST, ...connected], [connected]);
	const queries = useQueries({
		queries: hosts.map((hostId) => ({
			queryKey: orchestratorWaitingQueryKey(hostId),
			queryFn: () => fetchOrchestratorWaiting(hostId),
			retry: 1,
			refetchInterval: 30_000,
		})),
	});
	// A failed read keeps the last answer rather than dropping a marker the user
	// may still need; React Query holds the previous data through the error.
	const waiting = useMemo(
		() => queries.flatMap((query) => query.data ?? []),
		// eslint-disable-next-line react-hooks/exhaustive-deps -- the data identities are the dependencies
		[queries.map((query) => query.data)],
	);
	return { waiting };
}

/** The wait on one project's orchestrator, or undefined when it is not waiting. */
export function useProjectOrchestratorWaiting(hostId: HostId, projectId: string): OrchestratorWaiting | undefined {
	const { waiting } = useOrchestratorWaiting();
	return useMemo(() => waitingForProject(waiting, hostId, projectId), [hostId, projectId, waiting]);
}
