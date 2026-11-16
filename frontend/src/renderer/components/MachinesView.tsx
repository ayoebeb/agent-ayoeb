import { useQueries, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { Fragment, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Laptop, RefreshCw, Server } from "@/components/app-icons";
import { machineVersionQueryKey, machineVersionQueryOptions } from "../hooks/useMachineVersion";
import type { RemoteHost } from "../hooks/useRemoteHosts";
import { getApiBaseUrl } from "../lib/api-client";
import { baseUrlForHost } from "../lib/host-clients";
import { connectionRoute, type ConnectionRoute } from "../lib/machine-route";
import { versionRelation, type VersionRelation } from "../lib/machine-version";
import { useShellMaybe } from "../lib/shell-context";
import { cn } from "../lib/utils";
import { useMachineAccountsStore, type ClaudeAccountChoice } from "../stores/machine-accounts-store";
import { useUiStore } from "../stores/ui-store";
import { PageTopbar, PageTopbarTitle } from "./PageTopbar";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { SettingsRow } from "./settings/SettingsRow";
import { SettingsSection } from "./settings/SettingsSection";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type AccountOption = "unset" | ClaudeAccountChoice;

/** What a computer's saved connection says, in the words the list uses. */
function statusKey(
	host: RemoteHost,
): "machines.status.online" | "machines.status.connecting" | "remote.passwordRejected" | "machines.status.incompatible" | "machines.status.offline" {
	if (host.status === "connected") return "machines.status.online";
	if (host.status === "connecting") return "machines.status.connecting";
	if (host.failureReason === "unauthorized") return "remote.passwordRejected";
	if (host.failureReason === "incompatible") return "machines.status.incompatible";
	return "machines.status.offline";
}

function joinParts(parts: ReactNode[]): ReactNode {
	return parts.map((part, index) => (
		<Fragment key={index}>
			{index > 0 ? " · " : null}
			{part}
		</Fragment>
	));
}

/**
 * What the latest version check of one computer came back with. A check that
 * failed is its own answer, not "no version": a timeout or a 503 says nothing
 * about the release, and must never read as "update it" or as a match.
 */
type VersionRead =
	| { kind: "checking" }
	| { kind: "version"; version: string }
	| { kind: "unreported" }
	/** The latest check failed. `lastKnown` is what an earlier check saw, shown only as such. */
	| { kind: "failed"; lastKnown: string | null };

function toVersionRead(query: Pick<UseQueryResult<string | null>, "isError" | "data"> | undefined): VersionRead {
	if (!query) return { kind: "checking" };
	// A failed refresh keeps the earlier data in the query; the error still wins.
	if (query.isError) return { kind: "failed", lastKnown: typeof query.data === "string" ? query.data : null };
	if (typeof query.data === "string") return { kind: "version", version: query.data };
	if (query.data === null) return { kind: "unreported" };
	return { kind: "checking" };
}

/** The release to compare or name: only a check that just succeeded counts. */
const versionOf = (read: VersionRead): string | undefined => (read.kind === "version" ? read.version : undefined);

function VersionPart({ read, relation }: { read: VersionRead; relation: VersionRelation }) {
	const { t } = useTranslation();
	switch (read.kind) {
		case "version":
			return (
				<span className={cn(relation === "behind" || relation === "ahead" ? "text-warning" : undefined)}>
					<span className="tabular-nums">{t("machines.version", { version: read.version })}</span>
					{relation === "unknown" ? null : ` · ${t(`machines.version.${relation}`)}`}
				</span>
			);
		case "checking":
			return <span>{t("machines.version.checking")}</span>;
		case "failed":
			return (
				<span>
					{t("machines.version.failed")}
					{read.lastKnown ? <span className="tabular-nums">{` · ${t("machines.version.lastKnown", { version: read.lastKnown })}`}</span> : null}
				</span>
			);
		case "unreported":
			return (
				<Tooltip>
					<TooltipTrigger asChild>
						<span>{t("machines.version.unknown")}</span>
					</TooltipTrigger>
					<TooltipContent>{t("machines.version.unknownHint")}</TooltipContent>
				</Tooltip>
			);
	}
}

function RoutePart({ route }: { route: ConnectionRoute }) {
	const { t } = useTranslation();
	if (route !== "tunnel") return <span>{t(`machines.route.${route}`)}</span>;
	// A tunnel is the one route through a third party; say so where it is shown.
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span>{t("machines.route.tunnel")}</span>
			</TooltipTrigger>
			<TooltipContent>{t("machines.route.tunnelHint")}</TooltipContent>
		</Tooltip>
	);
}

/**
 * Every computer this desktop works with, one row each: whether it answers, how
 * it is reached, which Ayo release it runs next to this computer's, and which
 * Claude account it uses. It lists and compares; it opens no new access to any
 * computer. The version is read through the connection the desktop already has.
 */
export function MachinesView() {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const shell = useShellMaybe();
	const hosts = shell?.remoteHosts ?? [];
	const accounts = useMachineAccountsStore((state) => state.byHost);
	const setChoice = useMachineAccountsStore((state) => state.setChoice);

	const reads = useQueries({
		queries: [
			machineVersionQueryOptions("local", getApiBaseUrl()),
			...hosts.map((host) => machineVersionQueryOptions(host.hostId, host.status === "connected" ? baseUrlForHost(host.hostId) : undefined)),
		],
	});
	const local = toVersionRead(reads[0]);
	const rows = hosts.map((host, index) => {
		const read = toVersionRead(reads[index + 1]);
		// Without a fresh answer from both sides there is nothing to compare.
		return { host, read, relation: versionRelation(versionOf(read), versionOf(local)) };
	});
	const mismatched = rows.filter((row) => row.relation === "behind" || row.relation === "ahead");
	const checking = reads.some((read) => read.isFetching);

	const accountOptions = [
		{ value: "unset" as const, label: t("machines.claude.unset") },
		{ value: "same" as const, label: t("machines.claude.same") },
		{ value: "different" as const, label: t("machines.claude.different") },
	];

	return (
		<div className="flex min-h-0 flex-1 flex-col bg-background">
			<PageTopbar
				lead={<PageTopbarTitle as="h1" icon={<Server className="size-icon-md" aria-hidden="true" />}>{t("machines.title")}</PageTopbarTitle>}
				actions={
					<Button
						variant="quiet"
						size="icon-sm"
						aria-label={t("machines.refresh")}
						title={t("machines.refresh")}
						disabled={checking}
						onClick={() => {
							void shell?.refreshRemoteHosts?.();
							void queryClient.invalidateQueries({ queryKey: machineVersionQueryKey });
						}}
					>
						<RefreshCw aria-hidden="true" className={cn("size-icon-sm", checking && "animate-spin")} />
					</Button>
				}
			/>
			<main className="min-h-0 flex-1 overflow-y-auto">
				<div className="mx-auto flex w-full max-w-3xl flex-col gap-4 px-6 py-6">
					{mismatched.length > 0 ? (
						<div role="status" className="flex items-start gap-2.5 px-1 text-sm text-foreground" data-testid="machines-version-warning">
							<AlertTriangle aria-hidden="true" className="mt-0.5 size-icon-md shrink-0 text-warning" />
							<div className="flex flex-col gap-1.5">
								{mismatched.map(({ host, read, relation }) => (
									<p key={host.hostId}>
										{t(relation === "behind" ? "machines.warning.behind" : "machines.warning.ahead", {
											name: host.label,
											host: versionOf(read),
											local: versionOf(local),
										})}
									</p>
								))}
							</div>
						</div>
					) : null}
					<SettingsSection title={t("machines.title")} sectionId="machines" titleHidden grouped>
						<SettingsRow
							icon={Laptop}
							label={t("machines.thisComputer")}
							data-testid="machine-row"
							description={joinParts([
								<span key="status">{t("machines.status.online")}</span>,
								<VersionPart key="version" read={local} relation="unknown" />,
							])}
						>
							<span className="text-control text-settings-muted">{t("machines.claude.thisComputer")}</span>
						</SettingsRow>
						{rows.map(({ host, read, relation }) => (
							<SettingsRow
								key={host.hostId}
								icon={Server}
								label={host.label}
								data-testid="machine-row"
								data-host-id={host.hostId}
								description={joinParts([
									<span key="status">{t(statusKey(host))}</span>,
									<RoutePart key="route" route={connectionRoute(host.url)} />,
									host.status === "connected" ? <VersionPart key="version" read={read} relation={relation} /> : null,
								].filter((part) => part !== null))}
							>
								<SettingsOptionMenu<AccountOption>
									aria-label={t("machines.claude.aria", { name: host.label })}
									value={accounts[host.hostId] ?? "unset"}
									options={accountOptions}
									onChange={(value) => setChoice(host.hostId, value === "unset" ? null : value)}
									menuAlign="end"
								/>
							</SettingsRow>
						))}
					</SettingsSection>
					{hosts.length === 0 ? (
						<div className="flex items-center gap-3 px-1 text-sm text-muted-foreground">
							<span>{t("machines.empty")}</span>
							<Button variant="quiet" size="sm" onClick={() => useUiStore.getState().openGlobalSettings("remoteHosts")}>
								{t("machines.emptyAction")}
							</Button>
						</div>
					) : null}
				</div>
			</main>
		</div>
	);
}
