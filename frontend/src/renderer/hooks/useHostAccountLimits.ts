import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiErrorException } from "../lib/api-error-exception";
import { clientForHost } from "../lib/host-clients";
import { useHostConnection } from "./useHostConnection";

export type HostAccountLimits = components["schemas"]["AccountLimitsResponse"];

const hostAccountLimitsKey = (hostId: string, sessionId: string | undefined) => ["agent-account-limits", hostId, sessionId ?? ""] as const;

/**
 * The accounts' limits of another computer, read from that computer's own
 * daemon. The accounts routes under /agents/accounts are blocked for remote
 * desktops, so this reads the read-only /usage/account-limits instead; it also
 * names the account the session runs under. The limits belong to the Claude
 * account that computer uses, not this one.
 *
 * A host that is not connected, or a read that failed, leaves `data`
 * undefined: an old reading of a computer that went away is not shown as
 * current.
 */
export function useHostAccountLimits(hostId: string | undefined, sessionId?: string, enabled = true) {
	const connection = useHostConnection(hostId);
	const connected = Boolean(connection.baseUrl);
	const query = useQuery({
		queryKey: hostAccountLimitsKey(hostId ?? "", sessionId),
		enabled: enabled && Boolean(hostId) && connected,
		retry: 1,
		refetchInterval: 60_000,
		queryFn: async (): Promise<HostAccountLimits> => {
			const { data, error } = await clientForHost(hostId ?? "").GET("/api/v1/usage/account-limits", {
				params: { query: sessionId ? { sessionId } : {} },
			});
			if (error) throw apiErrorException(error);
			return data as HostAccountLimits;
		},
	});
	const usable = connected && !query.isError;
	return {
		connected,
		isPending: connected && query.isPending,
		isFetching: query.isFetching,
		error: connected ? query.error : null,
		data: usable ? query.data : undefined,
		refetch: query.refetch,
	};
}
