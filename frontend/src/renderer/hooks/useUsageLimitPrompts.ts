import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { apiErrorException } from "../lib/api-error-exception";
import { clientForHost, labelForHost } from "../lib/host-clients";
import { LOCAL_HOST, type HostId } from "../lib/hosts";
import { sessionInterfaceTransitionQueryKey } from "./useSessionInterfaceTransition";
import { useConnectedHosts } from "./useHostConnection";

export type UsageLimitPrompt = components["schemas"]["UsageLimitPrompt"];
export type UsageLimitPromptOption = components["schemas"]["UsageLimitPromptOption"];

/** An open question as this desktop shows it: from this computer, or from a
 * linked one (`hostId` set, `hostLabel` its name). */
export type HostUsageLimitPrompt = {
	/** Unique across computers: prompt ids only are unique per computer. */
	key: string;
	hostId?: HostId;
	hostLabel?: string;
	prompt: UsageLimitPrompt;
};

export const usageLimitPromptsQueryKey = ["agent-accounts", "limit-prompts"] as const;
export const hostUsageLimitPromptsQueryKey = (hostId: string) => ["agent-accounts", "host-limit-prompts", hostId] as const;

const LOCAL_POLL_MS = 15_000;
// Another computer is asked less often: its own desktop shows the prompt at once.
const HOST_POLL_MS = 30_000;

function unwrap<T>({ data, error }: { data?: T; error?: unknown }): T {
	if (error) throw apiErrorException(error);
	return data as T;
}

/** The switch-or-wait questions for sessions stopped on an account's usage limit.
 * One question per limit event, however many sessions stopped. */
export function useUsageLimitPrompts() {
	return useQuery({
		queryKey: usageLimitPromptsQueryKey,
		queryFn: async () => unwrap(await apiClient.GET("/api/v1/agents/accounts/limit-prompts"))?.prompts ?? [],
		refetchInterval: LOCAL_POLL_MS,
	});
}

/**
 * The open questions of every linked computer that is online, read from that
 * computer's own daemon (its addresses come masked). A computer that is not
 * connected is not asked, and one whose read failed shows nothing: an old
 * question of a computer that went away is not offered as current.
 */
export function useHostUsageLimitPrompts(): HostUsageLimitPrompt[] {
	const hosts = useConnectedHosts();
	const queries = useQueries({
		queries: hosts.map((hostId) => ({
			queryKey: hostUsageLimitPromptsQueryKey(hostId),
			retry: 1,
			refetchInterval: HOST_POLL_MS,
			queryFn: async (): Promise<UsageLimitPrompt[]> =>
				unwrap(await clientForHost(hostId).GET("/api/v1/agents/accounts/limit-prompts"))?.prompts ?? [],
		})),
	});
	return hosts.flatMap((hostId, index) => {
		const query = queries[index];
		if (!query || query.isError) return [];
		const hostLabel = labelForHost(hostId) ?? hostId;
		return (query.data ?? []).map((prompt) => ({ key: `${hostId}:${prompt.id}`, hostId, hostLabel, prompt }));
	});
}

/** Switch or wait on a question of this computer, or of the linked computer
 * `hostId`: the answer goes to the computer that owns the question. */
export function useUsageLimitPromptActions(hostId?: HostId) {
	const queryClient = useQueryClient();
	const remote = hostId && hostId !== LOCAL_HOST ? hostId : undefined;
	const client = (): typeof apiClient => (remote ? clientForHost(remote) : apiClient);
	const refresh = () => queryClient.invalidateQueries({ queryKey: remote ? hostUsageLimitPromptsQueryKey(remote) : usageLimitPromptsQueryKey });
	const switchTo = useMutation({
		mutationFn: async ({ promptId, accountId }: { promptId: string; accountId: string }) =>
			unwrap(await client().POST("/api/v1/agents/accounts/limit-prompts/{promptId}/switch", { params: { path: { promptId } }, body: { accountId } })),
		onSuccess: async (result) => {
			// The moved sessions belong to the computer that answered, not to this one.
			if (!remote) for (const sessionId of result.moved) await queryClient.invalidateQueries({ queryKey: sessionInterfaceTransitionQueryKey(sessionId) });
			await refresh();
		},
		// A prompt answered elsewhere answers 409; the list shows what is left.
		onError: () => void refresh(),
	});
	const wait = useMutation({
		mutationFn: async (promptId: string) =>
			unwrap(await client().POST("/api/v1/agents/accounts/limit-prompts/{promptId}/wait", { params: { path: { promptId } } })),
		onSettled: () => void refresh(),
	});
	return { switchTo, wait };
}
