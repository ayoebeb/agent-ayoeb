import { useNavigate } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CircleStop, ExternalLink, RotateCw, SquareTerminal } from "@/components/app-icons";
import { useHostConnection } from "../../hooks/useHostConnection";
import { liveServerLabel, useLiveServerActions, useLiveServers, type LiveServer } from "../../hooks/useLiveServers";
import { useOpenShellTerminal } from "../../hooks/useShellTerminals";
import { useSessionBrowserLink } from "../../hooks/useSessionBrowserLink";
import { compactAge } from "../../lib/crew";
import { sessionUiKey } from "../../lib/hosts";
import { useShell } from "../../lib/shell-context";
import { sessionNavigateTarget } from "../../lib/navigate-to-session";
import { initialTerminalFontSize } from "../../lib/terminal-font-size";
import { createTerminalMux, muxUrlFromApiBase } from "../../lib/terminal-mux";
import { cn } from "../../lib/utils";
import { useResolvedTheme, useUiStore } from "../../stores/ui-store";
import type { WorkspaceSession } from "../../types/workspace";
import { TerminalPane } from "../TerminalPane";
import { Button } from "../ui/button";
import { Pressable } from "../ui/pressable";
import { LiveHealthDot } from "./LiveServerChips";

/**
 * The orchestrator's Live tab: every dev server running for the project, one
 * selected at a time, with what it is, who started it, its output, a terminal
 * you can type in (for servers AO started), and Open / Restart / Stop.
 */
export function LiveRail({ active, orchestrator }: { active: boolean; orchestrator: WorkspaceSession }) {
	const { t } = useTranslation();
	const { servers, isError, isLoading } = useLiveServers(orchestrator, active);
	const uiKey = sessionUiKey(orchestrator.id, orchestrator.hostId);
	const selectedKey = useUiStore((state) => state.inspectorSessions[uiKey]?.liveServerKey);
	const setLiveServerKey = useUiStore((state) => state.setLiveServerKey);
	const selected = servers.find((server) => server.key === selectedKey) ?? servers[0];

	if (!selected) {
		return (
			<div className="flex h-full items-center justify-center p-4 text-center text-xs text-muted-foreground" data-testid="live-rail-empty">
				{isError ? t("live.error") : isLoading ? t("live.loading") : t("live.empty")}
			</div>
		);
	}
	return (
		<div className="flex h-full min-h-0 flex-col" data-testid="live-rail">
			{servers.length > 1 ? (
				<div aria-label={t("live.list.aria")} className="flex shrink-0 flex-wrap gap-1 border-b border-border px-3 py-2" role="tablist">
					{servers.map((server) => {
						const isSelected = server.key === selected.key;
						return (
							<Pressable
								aria-selected={isSelected}
								className={cn(
									"inline-flex h-6 items-center gap-1.5 rounded-md px-2 font-mono text-caption text-muted-foreground outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring",
									isSelected && "bg-accent text-foreground",
								)}
								data-active={isSelected ? "true" : undefined}
								key={server.key}
								onClick={() => setLiveServerKey(uiKey, server.key)}
								role="tab"
							>
								<LiveHealthDot health={server.health} />
								{liveServerLabel(server)}
							</Pressable>
						);
					})}
				</div>
			) : null}
			<LiveServerDetail key={selected.key} orchestrator={orchestrator} server={selected} />
		</div>
	);
}

function LiveServerDetail({ orchestrator, server }: { orchestrator: WorkspaceSession; server: LiveServer }) {
	const { t } = useTranslation();
	const openInBrowser = useSessionBrowserLink(orchestrator);
	const { stop, restart } = useLiveServerActions(orchestrator);
	const openShell = useOpenShellTerminal(orchestrator.hostId);
	const navigate = useNavigate();
	const busy = stop.isPending || restart.isPending;
	const failed = stop.isError || restart.isError;
	const age = server.startedAt ? compactAge(Date.now() - Date.parse(server.startedAt)) : undefined;

	const openTerminalHere = () => {
		openShell.open({ projectId: orchestrator.workspaceId, sessionId: server.sessionId });
		void navigate(sessionNavigateTarget(orchestrator.workspaceId, server.sessionId, orchestrator.hostId));
	};

	return (
		<div className="flex min-h-0 flex-1 flex-col gap-3 p-3" data-testid="live-server-detail">
			<div className="flex shrink-0 flex-wrap items-center gap-2">
				<LiveHealthDot health={server.health} className="size-2" />
				<span className="font-mono text-sm text-foreground">{liveServerLabel(server)}</span>
				<span className="text-caption text-muted-foreground" data-testid="live-health-text">
					{t(server.health === "up" ? "live.health.up" : "live.health.down")}
					{server.healthNote ? ` · ${server.healthNote}` : ""}
				</span>
				<div className="ml-auto flex items-center gap-1">
					<Button onClick={() => openInBrowser(server.url)} size="sm" variant="accent">
						<ExternalLink aria-hidden="true" className="size-icon-md" />
						{t("live.open")}
					</Button>
					{server.canRestart ? (
						<Button disabled={busy} onClick={() => restart.mutate(server.key)} size="sm" variant="secondary">
							<RotateCw aria-hidden="true" className="size-icon-md" />
							{t("live.restart")}
						</Button>
					) : null}
					<Button disabled={busy} onClick={() => stop.mutate(server.key)} size="sm" variant="destructive-ghost">
						<CircleStop aria-hidden="true" className="size-icon-md" />
						{t("live.stop")}
					</Button>
				</div>
			</div>
			{failed ? <p className="shrink-0 text-xs text-destructive" role="alert">{t("live.actionFailed")}</p> : null}
			{server.error ? <p className="shrink-0 text-xs text-destructive">{server.error}</p> : null}

			<dl className="grid shrink-0 grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs select-text" data-testid="live-server-facts">
				<Fact label={t("live.fact.command")}>{server.command || t("live.fact.unknown")}</Fact>
				<Fact label={t("live.fact.port")}>{server.port}</Fact>
				<Fact label={t("live.fact.url")}>{server.url}</Fact>
				<Fact label={t("live.fact.session")}>{server.sessionName || server.sessionId}</Fact>
				{age ? <Fact label={t("live.fact.started")}>{t("live.since", { age: t(`crew.age.${age.unit}`, { count: age.count }) })}</Fact> : null}
			</dl>

			{server.outputAvailable && server.terminalId ? (
				<LiveTerminal orchestrator={orchestrator} server={server} />
			) : server.outputAvailable ? (
				<pre className="board-scrollbar min-h-0 flex-1 overflow-auto rounded-md bg-muted p-2 font-mono text-caption select-text" data-testid="live-server-logs">
					{server.logs.join("\n")}
				</pre>
			) : (
				<div className="flex min-h-0 flex-1 flex-col items-start gap-2 text-xs text-muted-foreground" data-testid="live-server-detected">
					<p>{t("live.detected.note")}</p>
					<Button onClick={openTerminalHere} size="sm" variant="secondary">
						<SquareTerminal aria-hidden="true" className="size-icon-md" />
						{t("live.openTerminalHere")}
					</Button>
				</div>
			)}
		</div>
	);
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
	return (
		<>
			<dt className="text-muted-foreground">{label}</dt>
			<dd className="min-w-0 truncate font-mono text-foreground">{children}</dd>
		</>
	);
}

/** The server's own PTY, so the person sees its output and can type into it. */
function LiveTerminal({ orchestrator, server }: { orchestrator: WorkspaceSession; server: LiveServer }) {
	const theme = useResolvedTheme();
	const { daemonStatus } = useShell();
	const hostId = orchestrator.hostId;
	const { baseUrl: remoteBase } = useHostConnection(hostId);
	const createMux = useMemo(
		() => (remoteBase ? () => createTerminalMux(muxUrlFromApiBase(remoteBase)) : undefined),
		[remoteBase],
	);
	const [fontSize] = useState(initialTerminalFontSize);
	const handleId = server.terminalId ?? "";
	if (hostId && !remoteBase) return null;
	return (
		<div className="min-h-40 flex-1 overflow-hidden rounded-md border border-border" data-testid="live-server-terminal">
			<TerminalPane
				createMux={hostId ? createMux : undefined}
				daemonReady={hostId ? Boolean(remoteBase) : daemonStatus.state === "ready"}
				fontSize={fontSize}
				key={`${hostId ?? "local"}:${handleId}`}
				session={orchestrator}
				terminalTarget={{ kind: "shell", generation: handleId, handleId, sessionId: orchestrator.id, title: liveServerLabel(server) }}
				theme={theme}
			/>
		</div>
	);
}
