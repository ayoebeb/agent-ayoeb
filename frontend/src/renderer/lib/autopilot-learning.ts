import type { components } from "../../api/schema";
import { apiClient } from "./api-client";
import { apiErrorException } from "./api-error-exception";

export type AutopilotLearning = components["schemas"]["AutopilotLearningResponse"];
export type AutopilotLearningRule = components["schemas"]["AutopilotLearningRuleResponse"];
export type AutopilotLearningPair = components["schemas"]["AutopilotLearningPairResponse"];
export type AutopilotLearningImport = components["schemas"]["AutopilotLearningImportResponse"];

export const autopilotLearningQueryKey = ["autopilot", "learning"] as const;

/** What Autopilot learned about how the owner decides. The learned data lives on this computer only. */
export async function fetchAutopilotLearning(): Promise<AutopilotLearning> {
	const { data, error } = await apiClient.GET("/api/v1/autopilot/learning");
	if (error || !data) throw apiErrorException(error, "Could not load what Autopilot learned");
	return data;
}

/** The scope label of a rule: "All projects" for the global scope, the project id otherwise. */
export function isGlobalScope(scope: string): boolean {
	return scope === "global";
}

/** Rules grouped for display: all-project rules first, then each project in name order. */
export function groupRules(rules: AutopilotLearningRule[]): { scope: string; rules: AutopilotLearningRule[] }[] {
	const byScope = new Map<string, AutopilotLearningRule[]>();
	for (const rule of rules) byScope.set(rule.scope, [...(byScope.get(rule.scope) ?? []), rule]);
	return [...byScope.entries()]
		.sort(([a], [b]) => (isGlobalScope(a) ? -1 : isGlobalScope(b) ? 1 : a.localeCompare(b)))
		.map(([scope, grouped]) => ({ scope, rules: grouped }));
}
