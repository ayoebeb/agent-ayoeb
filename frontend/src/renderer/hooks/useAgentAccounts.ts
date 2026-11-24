import { apiErrorException } from "../lib/api-error-exception";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { clientForSessionHost } from "../lib/host-clients";
import { apiClient } from "../lib/api-client";
import type { AgentReadinessSnapshot } from "./useAgentReadinessQuery";
import { useHostConnection } from "./useHostConnection";
import { sessionInterfaceTransitionQueryKey } from "./useSessionInterfaceTransition";

export type AgentAccount = components["schemas"]["AgentAccountResponse"];
export type AgentAccountProvider = AgentAccount["provider"];
export type AgentAccountLimitWindow = components["schemas"]["AgentAccountLimitWindow"];
export type AgentAccountLogin = components["schemas"]["StartAgentAccountLoginResponse"];
export type AgentAccountUsageRow = components["schemas"]["AgentAccountUsageRow"];

export const agentAccountsQueryKey = ["agent-accounts"] as const;
const usageQueryKey = ["agent-accounts", "usage"] as const;
// A computer's accounts are its own: the local keys stay as they were and
// another computer's rows live under their own host segment.
const hostAccountsKey = (hostId: string | undefined) => hostId ? [...agentAccountsQueryKey, "host", hostId] as const : agentAccountsQueryKey;
const sessionAccountsKey = (sessionId: string, hostId?: string) => hostId
	? ["agent-accounts", "host", hostId, "session", sessionId] as const
	: ["agent-accounts", "session", sessionId] as const;

/** Whether the computer the accounts belong to can be asked: this one always,
 * another one only while its connection is up. */
function useAccountsHostReady(hostId: string | undefined): boolean {
	const connection = useHostConnection(hostId);
	return !hostId || Boolean(connection.baseUrl);
}

/** The account a harness signs in with, or null when it has none. */
export function agentAccountProviderFor(harness: string | undefined): AgentAccountProvider | null {
	if (harness === "claude-code") return "claude";
	if (harness === "codex") return "codex";
	return null;
}

/** New sessions start on a saved account, so readiness is that account's
 * sign-in; the CLI's own login is read from its row in the accounts list. */
export function followsSavedAccount(agent: AgentReadinessSnapshot | undefined): boolean {
	return Boolean(agent?.authentication.diagnostics?.some((step) => step.code === "saved_default_account"));
}

function unwrap<T>({ data, error }: { data?: T; error?: unknown }): T {
	if (error) throw apiErrorException(error);
	return data as T;
}

export async function fetchAgentAccounts(refresh = false, hostId?: string) {
	return unwrap(await clientForSessionHost(hostId).GET("/api/v1/agents/accounts", { params: { query: refresh ? { refresh: true } : {} } }))?.accounts ?? [];
}

/** Every account with its limits. The daemon reuses a reading for five minutes.
 * With a hostId these are another computer's accounts, as that computer shares
 * them: the email is masked. */
export function useAgentAccounts(enabled = true, hostId?: string) {
	const ready = useAccountsHostReady(hostId);
	return useQuery({ queryKey: hostAccountsKey(hostId), queryFn: () => fetchAgentAccounts(false, hostId), refetchInterval: 60_000, enabled: enabled && ready });
}

export const agentAccountProviderNames: Record<AgentAccountProvider, string> = { claude: "Claude Code", codex: "Codex" };

type AccountNameT = (key: "settings.agentAccounts.cliLoginOf" | "settings.agentAccounts.oauthToken", options?: { provider: string }) => string;

/** An account's name: its email. A Claude login without one is a
 * CLAUDE_CODE_OAUTH_TOKEN login, which reports no identity. */
export function agentAccountName(account: Pick<AgentAccount, "email" | "provider">, t: AccountNameT): string {
	if (account.email) return account.email;
	return account.provider === "claude"
		? t("settings.agentAccounts.oauthToken")
		: t("settings.agentAccounts.cliLoginOf", { provider: agentAccountProviderNames[account.provider] });
}

/** Names an account by id in status text, such as "Moved from … to …". */
export function useAgentAccountLabel(enabled = true, hostId?: string) {
	const { t } = useTranslation();
	const accounts = useAgentAccounts(enabled, hostId);
	return (id: string | undefined) => {
		const account = accounts.data?.find((candidate) => candidate.id === id);
		return account ? agentAccountName(account, t) : t("settings.agentAccounts.removedAccount");
	};
}

export function useAgentAccountUsage(days = 30) {
	return useQuery({
		queryKey: [...usageQueryKey, days],
		queryFn: async () => unwrap(await apiClient.GET("/api/v1/agents/accounts/usage", { params: { query: { days } } }))?.usage ?? [],
		refetchInterval: 60_000,
	});
}

export function useSessionAgentAccounts(sessionId: string | undefined, hostId?: string) {
	const ready = useAccountsHostReady(hostId);
	return useQuery({
		queryKey: sessionAccountsKey(sessionId ?? "", hostId),
		enabled: Boolean(sessionId) && ready,
		queryFn: async () => unwrap(await clientForSessionHost(hostId).GET("/api/v1/agents/accounts/sessions/{sessionId}", { params: { path: { sessionId: sessionId ?? "" } } })) ?? {},
	});
}

export function useAgentAccountActions(hostId?: string) {
	const queryClient = useQueryClient();
	const setAccounts = (accounts: AgentAccount[]) => queryClient.setQueryData(hostAccountsKey(hostId), accounts);
	return {
		refresh: async () => setAccounts(await fetchAgentAccounts(true)),
		startLogin: async (provider: AgentAccountProvider) =>
			unwrap(await apiClient.POST("/api/v1/agents/accounts/login", { body: { provider } })),
		/** Signs a saved account in again in its own folder; verify and cancel are the same as a new sign-in. */
		signInAgain: async (accountId: string) =>
			unwrap(await apiClient.POST("/api/v1/agents/accounts/{accountId}/login", { params: { path: { accountId } } })),
		verifyLogin: async (operationId: string) => {
			const result = unwrap(await apiClient.POST("/api/v1/agents/accounts/login/{operationId}/verify", { params: { path: { operationId } } }));
			if (result.status === "complete") await queryClient.invalidateQueries({ queryKey: agentAccountsQueryKey });
			return result;
		},
		cancelLogin: async (operationId: string) => {
			const { error } = await apiClient.POST("/api/v1/agents/accounts/login/{operationId}/cancel", { params: { path: { operationId } } });
			if (error) throw apiErrorException(error);
		},
		makeDefault: async (accountId: string) =>
			setAccounts(unwrap(await apiClient.POST("/api/v1/agents/accounts/{accountId}/default", { params: { path: { accountId } } })).accounts),
		remove: async (accountId: string) =>
			setAccounts(unwrap(await apiClient.DELETE("/api/v1/agents/accounts/{accountId}", { params: { path: { accountId } } })).accounts),
		useReset: async (accountId: string) => {
			const result = unwrap(await apiClient.POST("/api/v1/agents/accounts/{accountId}/reset-credit", {
				params: { path: { accountId } }, body: { idempotencyKey: crypto.randomUUID() },
			}));
			await queryClient.invalidateQueries({ queryKey: agentAccountsQueryKey });
			return result.outcome;
		},
		/** Starts the durable move (on the computer the hostId names, when given); the shared interface-transition status shows its
		 * progress. An orchestrator takes its project's live workers along; the
		 * result says how many moved. */
		switchSession: async (sessionId: string, accountId: string, stopTurn = false) => {
			const result = unwrap(await clientForSessionHost(hostId).PUT("/api/v1/agents/accounts/sessions/{sessionId}", {
				params: { path: { sessionId } }, body: { accountId, ...(stopTurn ? { stopTurn: true } : {}) },
			}));
			await queryClient.invalidateQueries({ queryKey: sessionInterfaceTransitionQueryKey(sessionId, hostId) });
			for (const worker of result.workers) {
				if (worker.status === "moving") await queryClient.invalidateQueries({ queryKey: sessionInterfaceTransitionQueryKey(worker.sessionId, hostId) });
			}
			return { transition: result.transition, movedWorkers: result.movedWorkers, workers: result.workers };
		},
		refreshSession: (sessionId: string) => queryClient.invalidateQueries({ queryKey: sessionAccountsKey(sessionId, hostId) }),
	};
}
