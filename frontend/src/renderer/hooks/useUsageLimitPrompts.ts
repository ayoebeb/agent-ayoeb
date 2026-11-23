import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { apiErrorException } from "../lib/api-error-exception";
import { sessionInterfaceTransitionQueryKey } from "./useSessionInterfaceTransition";

export type UsageLimitPrompt = components["schemas"]["UsageLimitPrompt"];
export type UsageLimitPromptOption = components["schemas"]["UsageLimitPromptOption"];

export const usageLimitPromptsQueryKey = ["agent-accounts", "limit-prompts"] as const;

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
		refetchInterval: 15_000,
	});
}

export function useUsageLimitPromptActions() {
	const queryClient = useQueryClient();
	const refresh = () => queryClient.invalidateQueries({ queryKey: usageLimitPromptsQueryKey });
	const switchTo = useMutation({
		mutationFn: async ({ promptId, accountId }: { promptId: string; accountId: string }) =>
			unwrap(await apiClient.POST("/api/v1/agents/accounts/limit-prompts/{promptId}/switch", { params: { path: { promptId } }, body: { accountId } })),
		onSuccess: async (result) => {
			for (const sessionId of result.moved) await queryClient.invalidateQueries({ queryKey: sessionInterfaceTransitionQueryKey(sessionId) });
			await refresh();
		},
		// A prompt answered elsewhere answers 409; the list shows what is left.
		onError: () => void refresh(),
	});
	const wait = useMutation({
		mutationFn: async (promptId: string) =>
			unwrap(await apiClient.POST("/api/v1/agents/accounts/limit-prompts/{promptId}/wait", { params: { path: { promptId } } })),
		onSettled: () => void refresh(),
	});
	return { switchTo, wait };
}
