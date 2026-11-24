import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { autopilotLearningQueryKey, fetchAutopilotLearning } from "../lib/autopilot-learning";

/**
 * What Autopilot learned about how the owner decides: the profile rules and the
 * stored decisions. It re-reads while a profile update runs, which can take minutes.
 */
export function useAutopilotLearning(enabled = true) {
	return useQuery({
		queryKey: autopilotLearningQueryKey,
		queryFn: fetchAutopilotLearning,
		enabled,
		retry: 1,
		refetchInterval: (query) => (query.state.data?.learning ? 3_000 : 15_000),
	});
}

/** The owner's edits and the two feeds (history import, profile update). Each re-reads the view on success. */
export function useAutopilotLearningActions() {
	const queryClient = useQueryClient();
	const refresh = () => queryClient.invalidateQueries({ queryKey: autopilotLearningQueryKey });

	const importHistory = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/autopilot/learning/import");
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: refresh,
	});
	const relearn = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/autopilot/learning/relearn");
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: refresh,
	});
	const addRule = useMutation({
		mutationFn: async (input: { scope: string; text: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/autopilot/learning/rules", { body: input });
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: refresh,
	});
	const editRule = useMutation({
		mutationFn: async (input: { id: string; text: string }) => {
			const { data, error } = await apiClient.PATCH("/api/v1/autopilot/learning/rules/{ruleId}", {
				params: { path: { ruleId: input.id } },
				body: { text: input.text },
			});
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: refresh,
	});
	const deleteRule = useMutation({
		mutationFn: async (id: string) => {
			const { error } = await apiClient.DELETE("/api/v1/autopilot/learning/rules/{ruleId}", { params: { path: { ruleId: id } } });
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: refresh,
	});
	const deletePair = useMutation({
		mutationFn: async (id: string) => {
			const { error } = await apiClient.DELETE("/api/v1/autopilot/learning/pairs/{pairId}", { params: { path: { pairId: id } } });
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: refresh,
	});

	return {
		importHistory,
		relearn,
		addRule,
		editRule,
		deleteRule,
		deletePair,
		error: [importHistory, relearn, addRule, editRule, deleteRule, deletePair].map((m) => m.error).find(Boolean)?.message,
	};
}
