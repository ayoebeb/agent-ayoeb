import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, ExternalLink, Plug, RefreshCw } from "@/components/app-icons";
import { useJiraBoard, useJiraConnection } from "../hooks/useJira";
import {
	columnTone,
	initialsOf,
	jiraFailureKind,
	jiraQueryKeys,
	latestSentSessionId,
	onlyAssignedTo,
	sessionsForTicket,
	startJiraTicket,
	type JiraBoard,
	type JiraFailureKind,
	type JiraTicket,
} from "../lib/jira";
import { openLinkInSystemBrowser } from "../lib/external-link-policy";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSession } from "../types/workspace";
import { JiraTicketDialog } from "./JiraTicketDialog";
import { Button } from "./ui/button";
import { Pressable } from "./ui/pressable";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

const TONE_DOT = {
	todo: "var(--color-text-passive)",
	progress: "var(--color-status-working)",
	done: "var(--color-status-ready)",
} as const;

type JiraBoardViewProps = {
	projectId: string;
	hostId?: string;
	/** Every session of the project, so tickets can show who is working on them. */
	sessions: readonly WorkspaceSession[];
	onOpenSettings: () => void;
	onOpenSession: (session: WorkspaceSession) => void;
	onOpenOrchestrator: (sessionId: string) => void;
	/** Hands a ticket to the existing New Task flow, prefilled, for a worker of its own. */
	onStartWorker: (ticket: JiraTicket) => void;
};

/**
 * The project's Jira tickets in the columns of its Jira board, each with a
 * one-tap "Start with orchestrator". Reads through the daemon, so it works the
 * same for a local project, a project on another machine, and a paired phone.
 */
export function JiraBoardView({ projectId, hostId, sessions, onOpenSettings, onOpenSession, onOpenOrchestrator, onStartWorker }: JiraBoardViewProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const connection = useJiraConnection(projectId, hostId);
	const connected = connection.data?.connected === true;
	const board = useJiraBoard(projectId, hostId, connected);
	const [mineOnly, setMineOnly] = useState(false);
	const [openKey, setOpenKey] = useState<string>();
	const [startingKey, setStartingKey] = useState<string>();

	const start = useMutation({
		mutationFn: (key: string) => startJiraTicket(projectId, key, hostId),
		onMutate: (key) => setStartingKey(key),
		onSettled: () => setStartingKey(undefined),
		onSuccess: (result, key) => {
			void queryClient.invalidateQueries({ queryKey: jiraQueryKeys.board(projectId, hostId) });
			useUiStore.getState().showGlobalToast(
				t(result.duplicate ? "jira.startedAgain" : result.spawned ? "jira.startedSpawned" : "jira.started", { key }),
				t("jira.startedBody"),
				{ tone: "success", action: { label: t("jira.openOrchestrator"), run: () => onOpenOrchestrator(result.orchestratorId) } },
			);
		},
		onError: (error, key) => {
			useUiStore.getState().showGlobalToast(t("jira.startFailed", { key }), error instanceof Error ? error.message : undefined, "error");
		},
	});

	const data = board.data;
	const view = useMemo(() => (data && mineOnly ? onlyAssignedTo(data, data.accountId) : data), [data, mineOnly]);
	const openTicket = useMemo(() => data?.columns.flatMap((column) => column.tickets).find((ticket) => ticket.key === openKey), [data, openKey]);

	if (connection.isPending) return <JiraNotice kind="loading" title={t("jira.loading")} />;
	if (connection.isError) {
		return <JiraFailure kind={jiraFailureKind(connection.error)} message={connection.error.message} onRetry={() => void connection.refetch()} onOpenSettings={onOpenSettings} />;
	}
	if (!connected) {
		return (
			<JiraNotice
				kind="info"
				title={t("jira.notConnected.title")}
				body={t("jira.notConnected.body")}
				action={<Button type="button" variant="primary" size="sm" onClick={onOpenSettings}><Plug className="size-icon-md" aria-hidden="true" />{t("jira.notConnected.action")}</Button>}
			/>
		);
	}
	if (!data) {
		return board.isError
			? <JiraFailure kind={jiraFailureKind(board.error)} message={board.error.message} onRetry={() => void board.refetch()} onOpenSettings={onOpenSettings} />
			: <JiraNotice kind="loading" title={t("jira.loading")} />;
	}

	const shown = view ?? data;
	const empty = data.total === 0;
	const mineEmpty = !empty && mineOnly && shown.total === 0;
	const updated = formatClock(data.fetchedAt);

	return (
		<div className="flex h-full min-h-0 flex-col" data-testid="jira-board" data-project-id={projectId}>
			<div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
				<Button
					type="button"
					variant="quiet"
					size="xs"
					aria-pressed={mineOnly}
					data-active={mineOnly ? "true" : undefined}
					disabled={!data.accountId}
					onClick={() => setMineOnly((current) => !current)}
				>
					{t("jira.assignedToMe")}
				</Button>
				<span className="min-w-0 flex-1 truncate text-2xs text-passive" title={data.boardName ?? data.projectKey}>
					{[data.boardName ?? data.projectKey, String(shown.total)].filter(Boolean).join(" · ")}
				</span>
				<span className="shrink-0 text-2xs tabular-nums text-passive">{t("jira.updatedAt", { time: updated })}</span>
				<Tooltip>
					<TooltipTrigger asChild>
						<Button type="button" variant="quiet" size="icon-xs" aria-label={t("jira.refresh")} disabled={board.isFetching} onClick={() => void board.refetch()}>
							<RefreshCw className={cn("size-icon-sm", board.isFetching && "animate-spin motion-reduce:animate-none")} aria-hidden="true" />
						</Button>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("jira.refresh")}</TooltipContent>
				</Tooltip>
			</div>
			{board.isError ? (
				<div role="status" className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1.5 text-2xs text-muted-foreground">
					<AlertTriangle className="size-icon-sm shrink-0 text-warning" aria-hidden="true" />
					<span className="min-w-0 flex-1">{t("jira.stale", { time: updated })}</span>
					<Button type="button" variant="ghost" size="xs" onClick={() => void board.refetch()}>{t("jira.error.retry")}</Button>
				</div>
			) : null}
			<div className="min-h-0 flex-1 overflow-hidden">
				{empty ? (
					<JiraNotice
						kind="info"
						title={t("jira.empty.title")}
						body={t("jira.empty.body")}
						detail={data.jql}
						action={<Button type="button" variant="secondary" size="sm" onClick={onOpenSettings}>{t("jira.empty.action")}</Button>}
					/>
				) : mineEmpty ? (
					<JiraNotice
						kind="info"
						title={t("jira.emptyMine.title")}
						action={<Button type="button" variant="secondary" size="sm" onClick={() => setMineOnly(false)}>{t("jira.emptyMine.action")}</Button>}
					/>
				) : (
					<JiraColumns
						board={shown}
						sessions={sessions}
						startingKey={startingKey}
						onOpen={(ticket) => setOpenKey(ticket.key)}
						onStart={(ticket) => start.mutate(ticket.key)}
						onOpenSession={onOpenSession}
						onOpenOrchestrator={onOpenOrchestrator}
					/>
				)}
			</div>
			{data.truncated ? <p className="shrink-0 border-t border-border px-3 py-1.5 text-2xs text-passive">{t("jira.truncated", { shown: data.total })}</p> : null}
			<JiraTicketDialog
				ticket={openTicket}
				sessions={openTicket ? sessionsForTicket(openTicket.key, sessions) : []}
				starting={startingKey === openTicket?.key}
				onOpenChange={(open) => { if (!open) setOpenKey(undefined); }}
				onStart={(ticket) => start.mutate(ticket.key)}
				onStartWorker={(ticket) => { setOpenKey(undefined); onStartWorker(ticket); }}
				onOpenSession={(session) => { setOpenKey(undefined); onOpenSession(session); }}
				onOpenOrchestrator={(id) => { setOpenKey(undefined); onOpenOrchestrator(id); }}
			/>
		</div>
	);
}

function JiraColumns({ board, sessions, startingKey, onOpen, onStart, onOpenSession, onOpenOrchestrator }: {
	board: JiraBoard;
	sessions: readonly WorkspaceSession[];
	startingKey?: string;
	onOpen: (ticket: JiraTicket) => void;
	onStart: (ticket: JiraTicket) => void;
	onOpenSession: (session: WorkspaceSession) => void;
	onOpenOrchestrator: (sessionId: string) => void;
}) {
	const { t } = useTranslation();
	return (
		<div className="board-horizontal-scrollbar h-full overflow-x-auto overflow-y-hidden" data-testid="jira-board-scroll">
			<div
				className="relative grid h-full divide-x divide-border-strong"
				style={{ gridTemplateColumns: `repeat(${Math.max(board.columns.length, 1)}, minmax(16rem, 1fr))`, minWidth: `${board.columns.length * 16}rem` }}
			>
				<div aria-hidden="true" className="pointer-events-none absolute inset-x-0 top-12 z-10 border-t border-border-strong" />
				{board.columns.map((column) => {
					const name = column.other ? t("jira.column.other") : column.name;
					return (
						<section key={`${column.other ? "other" : "col"}:${column.name}`} aria-label={t("jira.column.aria", { name })} className="flex min-w-0 flex-col overflow-hidden" data-testid="jira-column">
							<div className="flex h-12 shrink-0 items-center gap-2.5 px-4">
								<span className="size-[var(--size-swatch)] rounded-full" style={{ backgroundColor: TONE_DOT[columnTone(column)] }} aria-hidden="true" />
								<span className="min-w-0 truncate text-xs font-medium">{name}</span>
								<span className="ml-auto tabular-nums text-xs leading-none text-passive">{column.tickets.length}</span>
							</div>
							<div className="board-scrollbar min-h-0 flex-1 overflow-y-auto pb-3 pl-3 pr-2 pt-3">
								<div className="flex min-h-full flex-col gap-2.5">
									{column.tickets.map((ticket) => (
										<JiraTicketCard
											key={ticket.key}
											ticket={ticket}
											linked={sessionsForTicket(ticket.key, sessions)}
											orchestratorLinked={latestSentSessionId(ticket)}
											starting={startingKey === ticket.key}
											onOpen={() => onOpen(ticket)}
											onStart={() => onStart(ticket)}
											onOpenSession={onOpenSession}
											onOpenOrchestrator={onOpenOrchestrator}
										/>
									))}
								</div>
							</div>
						</section>
					);
				})}
			</div>
		</div>
	);
}

function JiraTicketCard({ ticket, linked, orchestratorLinked, starting, onOpen, onStart, onOpenSession, onOpenOrchestrator }: {
	ticket: JiraTicket;
	linked: WorkspaceSession[];
	/** The orchestrator session this ticket was last sent to, when it was. */
	orchestratorLinked?: string;
	starting: boolean;
	onOpen: () => void;
	onStart: () => void;
	onOpenSession: (session: WorkspaceSession) => void;
	onOpenOrchestrator: (sessionId: string) => void;
}) {
	const { t } = useTranslation();
	const meta = [ticket.issueType, ticket.priority].filter(Boolean).join(" · ");
	const assignee = ticket.assignee?.displayName;
	return (
		<article
			className="group relative flex w-full flex-col gap-2 rounded-lg border border-border bg-surface px-3.5 py-3 text-left transition-colors duration-[120ms] ease-out hover:bg-interactive-hover motion-reduce:transition-none"
			data-testid="jira-card"
			data-ticket-key={ticket.key}
		>
			<div className="flex min-w-0 items-center gap-2">
				<span className="shrink-0 font-mono text-2xs text-muted-foreground">{ticket.key}</span>
				<span className="min-w-0 flex-1 truncate text-2xs text-passive">{meta}</span>
				<Tooltip>
					<TooltipTrigger asChild>
						<Button type="button" variant="quiet" size="icon-xs" className="relative z-10" aria-label={t("jira.openInJira")} onClick={() => void openLinkInSystemBrowser(ticket.url)}>
							<ExternalLink className="size-icon-sm" aria-hidden="true" />
						</Button>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("jira.openInJira")}</TooltipContent>
				</Tooltip>
			</div>
			{/* The title is the card's one primary hit target; it stretches over the card, under the action buttons. */}
			<Pressable
				className="text-sm font-medium leading-snug text-foreground after:absolute after:inset-0 after:rounded-lg after:content-[''] focus-visible:after:outline-2 focus-visible:after:outline-ring"
				aria-label={t("jira.card.openDetails", { key: ticket.key })}
				onClick={onOpen}
			>
				{ticket.summary}
			</Pressable>
			<div className="flex min-w-0 items-center gap-2 text-2xs text-muted-foreground">
				<span
					className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-interactive-active text-[10px] font-medium text-foreground"
					aria-hidden="true"
				>
					{assignee ? initialsOf(assignee) : "–"}
				</span>
				<span className="min-w-0 truncate">{assignee ?? t("jira.unassigned")}</span>
			</div>
			{linked.length > 0 || orchestratorLinked ? (
				<div className="relative z-10 flex min-w-0 flex-col gap-1">
					{orchestratorLinked ? (
						<Button type="button" variant="ghost" size="xs" className="w-fit max-w-full justify-start" onClick={() => onOpenOrchestrator(orchestratorLinked)}>
							<span className="size-1.5 shrink-0 rounded-full" style={{ backgroundColor: "var(--color-status-working)" }} aria-hidden="true" />
							<span className="truncate">{t("jira.sentToOrchestrator")}</span>
						</Button>
					) : null}
					{linked.slice(0, 2).map((session) => (
						<Button key={session.id} type="button" variant="ghost" size="xs" className="w-fit max-w-full justify-start" onClick={() => onOpenSession(session)}>
							<span className="size-1.5 shrink-0 rounded-full" style={{ backgroundColor: "var(--color-status-working)" }} aria-hidden="true" />
							<span className="truncate">{session.title}</span>
						</Button>
					))}
					{linked.length > 2 ? <span className="px-2 text-2xs text-passive">{t("jira.moreSessions", { more: linked.length - 2 })}</span> : null}
				</div>
			) : null}
			<div className="relative z-10 flex items-center pt-0.5">
				<Button type="button" variant="secondary" size="xs" disabled={starting} aria-busy={starting} aria-label={t("jira.startAria", { key: ticket.key })} onClick={onStart}>
					{starting ? t("jira.starting") : t("jira.start")}
				</Button>
			</div>
		</article>
	);
}

function JiraNotice({ kind, title, body, detail, action }: {
	kind: "loading" | "info";
	title: string;
	body?: string;
	/** Code-like text under the body, such as the JQL a board is using. */
	detail?: string;
	action?: React.ReactNode;
}) {
	return (
		<div className="flex h-full flex-col items-center justify-center gap-3 px-6 py-10 text-center" role={kind === "loading" ? "status" : undefined} data-testid="jira-notice" data-kind={kind}>
			<p className="text-sm font-medium text-foreground">{title}</p>
			{body ? <p className="max-w-md text-pretty text-xs leading-5 text-muted-foreground">{body}</p> : null}
			{detail ? <code className="max-w-md break-words rounded-md bg-interactive-hover px-2 py-1 text-left font-mono text-2xs text-muted-foreground">{detail}</code> : null}
			{action}
		</div>
	);
}

function JiraFailure({ kind, message, onRetry, onOpenSettings }: {
	kind: JiraFailureKind;
	message: string;
	onRetry: () => void;
	onOpenSettings: () => void;
}) {
	const { t } = useTranslation();
	const fixInSettings = kind === "auth" || kind === "bad_query" || kind === "not_connected";
	const title = kind === "auth" ? t("jira.error.auth.title")
		: kind === "offline" ? t("jira.error.offline.title")
		: kind === "bad_query" ? t("jira.error.query.title")
		: t("jira.error.other.title");
	const body = kind === "auth" ? t("jira.error.auth.body") : kind === "offline" ? t("jira.error.offline.body") : message;
	return (
		<div className="flex h-full flex-col items-center justify-center gap-3 px-6 py-10 text-center" role="alert" data-testid="jira-notice" data-kind={kind}>
			<AlertTriangle className="size-icon-lg text-warning" aria-hidden="true" />
			<p className="text-sm font-medium text-foreground">{title}</p>
			<p className="max-w-md text-pretty text-xs leading-5 text-muted-foreground">{body}</p>
			<div className="flex items-center gap-2">
				{fixInSettings ? <Button type="button" variant="primary" size="sm" onClick={onOpenSettings}>{t("jira.error.settings")}</Button> : null}
				<Button type="button" variant="secondary" size="sm" onClick={onRetry}>{t("jira.error.retry")}</Button>
			</div>
		</div>
	);
}

/** A short local time for "Updated 14:32". */
function formatClock(iso: string): string {
	const date = new Date(iso);
	return Number.isNaN(date.getTime()) ? "" : date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}
