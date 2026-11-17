import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ProjectSettingsSection } from "@aoagents/product-ui";
import { useEffect, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { useJiraConnection } from "../hooks/useJira";
import { connectJira, disconnectJira, jiraQueryKeys, sameJiraSite, testJira, type JiraConnectInput, type JiraTestResult } from "../lib/jira";
import { useUiStore } from "../stores/ui-store";
import { ConfirmDialog } from "./ConfirmDialog";
import { SettingsRow } from "./settings/SettingsRow";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

type Draft = {
	siteUrl: string;
	email: string;
	/** Write-only: empty keeps the token already stored. */
	apiToken: string;
	projectKey: string;
	boardId: string;
	jql: string;
};

const EMPTY: Draft = { siteUrl: "", email: "", apiToken: "", projectKey: "", boardId: "", jql: "" };

/**
 * Connect a project to Jira Cloud: site, account email, API token and which
 * tickets to show. The token goes to the daemon and is never read back; the
 * field stays empty once saved. Works against the machine that owns the
 * project, so a project on another host connects there.
 */
export function JiraConnectionSettings({ projectId, hostId }: { projectId: string; hostId?: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const connection = useJiraConnection(projectId, hostId);
	const saved = connection.data;
	const [draft, setDraft] = useState<Draft>(EMPTY);
	const [loadedFor, setLoadedFor] = useState<string>();
	const [tested, setTested] = useState<JiraTestResult>();
	const [error, setError] = useState<string>();
	const [confirmDisconnect, setConfirmDisconnect] = useState(false);

	// Fill the form from the saved connection once per project, never the token.
	useEffect(() => {
		if (!saved || loadedFor === `${hostId ?? ""}:${projectId}`) return;
		setLoadedFor(`${hostId ?? ""}:${projectId}`);
		if (saved.connected) {
			setDraft({ siteUrl: saved.siteUrl ?? "", email: saved.email ?? "", apiToken: "", projectKey: saved.projectKey ?? "", boardId: saved.boardId ? String(saved.boardId) : "", jql: saved.jql ?? "" });
		}
	}, [saved, loadedFor, hostId, projectId]);

	const patch = (values: Partial<Draft>) => {
		setDraft((current) => ({ ...current, ...values }));
		setTested(undefined);
		setError(undefined);
	};
	const connected = saved?.connected === true;
	const input = (): JiraConnectInput => ({
		siteUrl: draft.siteUrl.trim(),
		email: draft.email.trim(),
		...(draft.apiToken.trim() ? { apiToken: draft.apiToken.trim() } : {}),
		...(draft.projectKey.trim() ? { projectKey: draft.projectKey.trim() } : {}),
		...(/^\d+$/.test(draft.boardId.trim()) ? { boardId: Number(draft.boardId.trim()) } : {}),
		...(draft.jql.trim() ? { jql: draft.jql.trim() } : {}),
	});
	// The saved token belongs to the site and account it was saved for: it is kept
	// only while both are unchanged, so another site or email needs its own token.
	const keepsSavedToken = connected && sameJiraSite(draft.siteUrl, saved?.siteUrl ?? "") && draft.email.trim().toLowerCase() === (saved?.email ?? "").trim().toLowerCase();
	const complete = draft.siteUrl.trim() !== "" && draft.email.trim() !== "" && (draft.apiToken.trim() !== "" || keepsSavedToken) && (draft.projectKey.trim() !== "" || draft.jql.trim() !== "");
	const refresh = () => {
		void queryClient.invalidateQueries({ queryKey: jiraQueryKeys.connection(projectId, hostId) });
		void queryClient.invalidateQueries({ queryKey: jiraQueryKeys.board(projectId, hostId) });
	};

	const test = useMutation({
		mutationFn: () => testJira(projectId, input(), hostId),
		onMutate: () => { setError(undefined); setTested(undefined); },
		onSuccess: setTested,
		onError: (cause) => setError(cause instanceof Error ? cause.message : t("jira.settings.failed")),
	});
	const save = useMutation({
		mutationFn: () => connectJira(projectId, input(), hostId),
		onMutate: () => { setError(undefined); setTested(undefined); },
		onSuccess: () => {
			setDraft((current) => ({ ...current, apiToken: "" }));
			refresh();
			useUiStore.getState().showGlobalToast(t("jira.settings.saved"), undefined, "success");
		},
		onError: (cause) => setError(cause instanceof Error ? cause.message : t("jira.settings.failed")),
	});
	const disconnect = useMutation({
		mutationFn: () => disconnectJira(projectId, hostId),
		onSuccess: () => {
			setConfirmDisconnect(false);
			setDraft(EMPTY);
			setTested(undefined);
			refresh();
			useUiStore.getState().showGlobalToast(t("jira.settings.disconnected"), undefined, "info");
		},
		onError: (cause) => {
			setConfirmDisconnect(false);
			setError(cause instanceof Error ? cause.message : t("jira.settings.failed"));
		},
	});
	const busy = test.isPending || save.isPending || disconnect.isPending;
	// Enter in a field must not submit the surrounding project form.
	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		if (event.key !== "Enter") return;
		event.preventDefault();
		if (complete && !busy) save.mutate();
	};
	const field = (id: keyof Draft, label: string, props: { placeholder?: string; type?: string; autoComplete?: string; description?: string; mono?: boolean } = {}) => (
		<SettingsRow label={<label htmlFor={`jira-${id}`}>{label}</label>} description={props.description}>
			<Input
				id={`jira-${id}`}
				data-testid={`jira-field-${id}`}
				className={props.mono ? "w-80 max-w-full font-mono text-xs" : "w-80 max-w-full"}
				type={props.type}
				value={draft[id]}
				placeholder={props.placeholder}
				autoComplete={props.autoComplete ?? "off"}
				spellCheck={false}
				disabled={busy}
				onKeyDown={onKeyDown}
				onChange={(event) => patch({ [id]: event.target.value })}
			/>
		</SettingsRow>
	);

	return (
		<ProjectSettingsSection title={t("jira.settings.title")} grouped>
			<SettingsRow label={t("jira.settings.status")}>
				<span className="text-sm text-settings-muted" role="status" data-testid="jira-connection-status">
					{connection.isPending ? t("jira.loading") : connected ? t("jira.settings.connectedAs", { name: saved?.accountName ?? saved?.email ?? "" }) : t("jira.settings.notConnected")}
				</span>
			</SettingsRow>
			{field("siteUrl", t("jira.settings.siteUrl"), { placeholder: t("jira.settings.siteUrlPlaceholder") })}
			{field("email", t("jira.settings.email"), { type: "email", autoComplete: "username" })}
			{field("apiToken", t("jira.settings.apiToken"), {
				type: "password",
				autoComplete: "new-password",
				placeholder: keepsSavedToken ? t("jira.settings.apiTokenKeep") : connected ? t("jira.settings.apiTokenNew") : undefined,
				description: t("jira.settings.apiTokenHelp"),
			})}
			{field("projectKey", t("jira.settings.projectKey"), { placeholder: t("jira.settings.projectKeyPlaceholder"), mono: true })}
			{field("boardId", t("jira.settings.boardId"), { placeholder: t("jira.settings.boardIdPlaceholder"), mono: true })}
			{field("jql", t("jira.settings.jql"), { placeholder: t("jira.settings.jqlPlaceholder"), mono: true, description: t("jira.settings.jqlHelp") })}
			<div className="settings-row-bar flex-wrap justify-end gap-2">
				<div className="mr-auto min-w-0 text-control" aria-live="polite">
					{error ? <p role="alert" className="text-error" data-testid="jira-settings-error">{error}</p> : null}
					{tested ? (
						<p role="status" className="text-settings-muted" data-testid="jira-test-result">
							{t("jira.settings.testOk", { name: tested.accountName, project: (tested.projectName ?? draft.projectKey.trim().toUpperCase()) || "–", tickets: tested.ticketCount })}
						</p>
					) : null}
				</div>
				{connected ? (
					<Button type="button" variant="destructive-ghost" disabled={busy} onClick={() => setConfirmDisconnect(true)}>{t("jira.settings.disconnect")}</Button>
				) : null}
				<Button type="button" variant="secondary" disabled={busy || !complete} aria-busy={test.isPending} onClick={() => test.mutate()}>
					{test.isPending ? t("jira.settings.testing") : t("jira.settings.test")}
				</Button>
				<Button type="button" variant="primary" disabled={busy || !complete} aria-busy={save.isPending} onClick={() => save.mutate()}>
					{save.isPending ? t("jira.settings.saving") : connected ? t("jira.settings.save") : t("jira.settings.connect")}
				</Button>
			</div>
			<ConfirmDialog
				open={confirmDisconnect}
				title={t("jira.settings.disconnectTitle")}
				description={t("jira.settings.disconnectBody")}
				confirmLabel={t("jira.settings.disconnect")}
				destructive
				busy={disconnect.isPending}
				onConfirm={() => disconnect.mutate()}
				onOpenChange={setConfirmDisconnect}
			/>
		</ProjectSettingsSection>
	);
}
