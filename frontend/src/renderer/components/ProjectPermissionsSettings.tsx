import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ProjectSettingsSection } from "@aoagents/product-ui";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { apiErrorException } from "../lib/api-error-exception";
import { cn } from "../lib/utils";
import { Button } from "./ui/button";

type Rule = components["schemas"]["ControllersProjectPermissionRuleResponse"];

export const projectPermissionRulesQueryKey = (projectId: string) => ["project", projectId, "permission-rules"] as const;

/**
 * The tool permissions people chose "Always allow" for in this project. Every
 * new Claude Code session of the project starts with them; Revoke removes one
 * from future sessions and from the project's live ones.
 */
export function ProjectPermissionsSettings({ projectId }: { projectId: string }) {
	const { t, i18n } = useTranslation();
	const client = useQueryClient();
	const queryKey = projectPermissionRulesQueryKey(projectId);
	const query = useQuery({
		queryKey,
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}/permission-rules", { params: { path: { id: projectId } } });
			if (error) throw apiErrorException(error);
			return data?.rules ?? [];
		},
	});
	const revoke = useMutation({
		mutationFn: async (rule: Rule) => {
			const { error } = await apiClient.DELETE("/api/v1/projects/{id}/permission-rules/{ruleId}", { params: { path: { id: projectId, ruleId: rule.id } } });
			if (error) throw apiErrorException(error);
		},
		// A rule already revoked elsewhere is also gone from the list on refetch.
		onSettled: () => client.invalidateQueries({ queryKey }),
	});

	const kindLabel: Record<Rule["kind"], string> = {
		exact: t("settings.project.permissionsKindExact"),
		command: t("settings.project.permissionsKindCommand"),
		tool: t("settings.project.permissionsKindTool"),
		server: t("settings.project.permissionsKindServer"),
	};
	const date = new Intl.DateTimeFormat(i18n.language, { dateStyle: "medium" });

	if (query.isLoading) return <p className="px-(--settings-row-inset) text-sm text-settings-muted">{t("settings.project.loading")}</p>;
	if (query.isError) {
		return <p role="alert" className="px-(--settings-row-inset) text-sm text-error">{query.error instanceof Error ? query.error.message : t("settings.project.permissionsLoadFailed")}</p>;
	}
	const rules = query.data ?? [];
	return <div className="flex min-h-full flex-col gap-(--size-settings-section-inner-gap)">
		<ProjectSettingsSection title={t("settings.project.permissions")} titleHidden grouped>
			{rules.length === 0
				? <div className="settings-row-bar"><span className="text-sm leading-5 text-settings-label">{t("settings.project.permissionsEmpty")}</span></div>
				: rules.map((rule) => {
					const meta = [kindLabel[rule.kind], date.format(new Date(rule.createdAt)), rule.sourceSessionId].filter(Boolean).join(" · ");
					return <div className="settings-row-bar gap-3" key={rule.id}>
						<div className="flex min-w-0 flex-1 flex-col gap-0.5">
							<span className={cn("truncate text-sm leading-5 text-settings-label", rule.tool === "Bash" && "font-mono")} title={rule.label}>{rule.label}</span>
							<span className="truncate text-xs text-settings-muted" title={meta}>{meta}</span>
						</div>
						<Button
							aria-label={t("settings.project.permissionsRevokeRule", { rule: rule.label })}
							className="shrink-0"
							disabled={revoke.isPending && revoke.variables?.id === rule.id}
							onClick={() => revoke.mutate(rule)}
							size="sm"
							type="button"
							variant="destructive-ghost"
						>{t("settings.project.permissionsRevoke")}</Button>
					</div>;
				})}
		</ProjectSettingsSection>
		{revoke.isError && <p role="alert" className="px-(--settings-row-inset) text-xs text-error">{revoke.error instanceof Error ? revoke.error.message : t("settings.project.permissionsRevokeFailed")}</p>}
	</div>;
}
