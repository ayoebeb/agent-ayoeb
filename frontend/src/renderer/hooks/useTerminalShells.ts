// Shells installed on a daemon's machine, detected by that daemon for the
// Settings shell picker and the new-terminal menu. The renderer never probes
// the filesystem itself.

import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { clientForSessionHost } from "../lib/host-clients";
import { LOCAL_HOST, type HostId } from "../lib/hosts";

export type TerminalShell = components["schemas"]["TerminalShellResponse"];
export type TerminalShellCatalog = components["schemas"]["TerminalShellsResponse"];

/**
 * The shells of the daemon that owns a session: this computer's by default, or
 * a connected remote host's. The answer also says whether this client may open
 * them as administrator, which a remote host always refuses.
 */
export function useTerminalShells(hostId?: HostId) {
	const remote = Boolean(hostId && hostId !== LOCAL_HOST);
	return useQuery({
		queryKey: remote ? ["terminal-shells", hostId] : ["terminal-shells"],
		queryFn: async (): Promise<TerminalShellCatalog> => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/terminal-shells");
			if (error) throw error;
			if (!data) throw new Error("Daemon returned no shells");
			return data;
		},
		staleTime: 30_000,
	});
}

/** Asks the daemon whether a custom shell path can launch. */
export async function checkTerminalShellPath(path: string): Promise<components["schemas"]["TerminalShellCheckResponse"]> {
	const { data, error } = await apiClient.GET("/api/v1/terminal-shells/check", { params: { query: { path } } });
	if (error || !data) throw error ?? new Error("Daemon returned no shell check");
	return data;
}
