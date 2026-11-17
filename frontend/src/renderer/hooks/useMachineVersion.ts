import { queryOptions } from "@tanstack/react-query";
import { parseDaemonProbe } from "../../shared/daemon-attach";

export const machineVersionQueryKey = ["machine-version"] as const;

/**
 * Reads a daemon's release from its health probe. A linked computer is read
 * through the connection this desktop already holds to it, which attaches the
 * saved credential, so this asks nothing a desktop could not ask before.
 * Resolves null when the daemon answers but does not report a version (an older
 * Ayo, or a development build).
 */
export async function readMachineVersion(base: string): Promise<string | null> {
	const response = await fetch(`${base.replace(/\/+$/, "")}/healthz`, { signal: AbortSignal.timeout(5_000) });
	if (!response.ok) throw new Error(`health probe returned ${response.status}`);
	return parseDaemonProbe("healthz", await response.json())?.version ?? null;
}

/** `key` names the computer ("local" or a host ID); no `base` leaves the query idle. */
export function machineVersionQueryOptions(key: string, base: string | undefined) {
	return queryOptions({
		queryKey: [...machineVersionQueryKey, key, base ?? ""],
		queryFn: () => readMachineVersion(base ?? ""),
		enabled: Boolean(base),
		staleTime: 60_000,
		retry: false,
		refetchOnWindowFocus: false,
	});
}
