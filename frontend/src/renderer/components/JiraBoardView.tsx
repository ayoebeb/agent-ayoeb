import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { BoardCardView, BoardColumnFrame, BoardColumnsGrid } from "@aoagents/product-ui";
import { AlertTriangle, ExternalLink, LoaderCircle, Plug, RefreshCw, Send } from "@/components/app-icons";
import { useJiraBoard, useJiraConnection } from "../hooks/useJira";
import {
	columnTone,
	initialsOf,
	jiraFailureKind,
	jiraQueryKeys,
	latestSentSessionId,
	onlyAssignedTo,
	ticketCategoryTextClass,
	ticketMeta,
	hasOtherAssignees,
	sessionsForTicket,
	startJiraTicket,
	type JiraBoard,
	type JiraFailureKind,
	type JiraTicket,
} from "../lib/jira";
import { openLinkInSystemBrowser } from "../lib/external-link-policy";
import { formatTimeCompact } from "../lib/format-time";
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

const TONE_TITLE = {
	todo: "text-muted-foreground",
	progress: "text-status-working",
	done: "text-status-ready",
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
	const othersShown = useMemo(() => (data ? hasOtherAssignees(data) : false), [data]);
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
	// Without a Jira board or project key the board is simply what is assigned to the person.
	const scope = data.boardName || data.projectKey || t("jira.assignedToMe");

	return (
		<div className="flex h-full min-h-0 flex-col" data-testid="jira-board" data-project-id={projectId}>
			<div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
				{othersShown ? (
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
				) : null}
				<span className="min-w-0 flex-1 truncate text-2xs text-passive" title={scope}>
					{[scope, String(shown.total)].filter(Boolean).join(" · ")}
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
						accountId={data.accountId}
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

function JiraColumns({ board, sessions, accountId, startingKey, onOpen, onStart, onOpenSession, onOpenOrchestrator }: {
	board: JiraBoard;
	sessions: readonly WorkspaceSession[];
	/** The connected Jira account; a ticket of anyone else shows its assignee. */
	accountId?: string;
	startingKey?: string;
	onOpen: (ticket: JiraTicket) => void;
	onStart: (ticket: JiraTicket) => void;
	onOpenSession: (session: WorkspaceSession) => void;
	onOpenOrchestrator: (sessionId: string) => void;
}) {
	const { t } = useTranslation();
	return (
		<BoardColumnsGrid columnCount={board.columns.length} scrollTestId="jira-board-scroll">
			{board.columns.map((column) => {
				const name = column.other ? t("jira.column.other") : column.name;
				const tone = columnTone(column);
				return (
					<BoardColumnFrame
						key={`${column.other ? "other" : "col"}:${column.name}`}
						ariaLabel={t("jira.column.aria", { name })}
						count={column.tickets.length}
						dot={TONE_DOT[tone]}
						label={name}
						labelClassName={TONE_TITLE[tone]}
						testId="jira-column"
					>
						{column.tickets.map((ticket) => (
							<JiraTicketCard
								key={ticket.key}
								ticket={ticket}
								showAssignee={Boolean(accountId) && Boolean(ticket.assignee) && ticket.assignee?.accountId !== accountId}
								linked={sessionsForTicket(ticket.key, sessions)}
								orchestratorLinked={latestSentSessionId(ticket)}
								starting={startingKey === ticket.key}
								onOpen={() => onOpen(ticket)}
								onStart={() => onStart(ticket)}
								onOpenSession={onOpenSession}
								onOpenOrchestrator={onOpenOrchestrator}
							/>
						))}
					</BoardColumnFrame>
				);
			})}
		</BoardColumnsGrid>
	);
}

/**
 * A ticket in the Agents card's shell (see BoardCardView): the summary as the
 * title, "KEY · type · priority" as the meta line, sessions working on it as
 * sub-rows, and a footer with the Jira status and how long ago it changed.
 */
function JiraTicketCard({ ticket, showAssignee, linked, orchestratorLinked, starting, onOpen, onStart, onOpenSession, onOpenOrchestrator }: {
	ticket: JiraTicket;
	/** The ticket is someone else's (a custom JQL), so say whose. */
	showAssignee: boolean;
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
	const meta = ticketMeta(ticket);
	const assignee = ticket.assignee?.displayName;
	const hoverReveal = "pointer-events-none opacity-0 group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100";
	const rowClass = "inline-flex min-w-0 max-w-full items-center gap-1.5 py-0.5 text-2xs text-muted-foreground hover:underline";
	return (
		<BoardCardView
			testId="jira-card"
			dataKey={ticket.key}
			title={ticket.summary}
			openLabel={t("jira.card.openDetails", { key: ticket.key })}
			onOpen={onOpen}
			meta={
				<span className="min-w-0 truncate" title={meta.join(" · ")}>
					<span className="font-mono">{meta[0]}</span>
					{meta.slice(1).map((part) => <span key={part}> · {part}</span>)}
				</span>
			}
			action={
				<Tooltip>
					<TooltipTrigger asChild>
						<Button
							type="button"
							variant="quiet"
							size="icon-sm"
							className={cn("rounded-sm", hoverReveal)}
							aria-label={t("jira.openInJira")}
							onClick={(event) => { event.stopPropagation(); void openLinkInSystemBrowser(ticket.url); }}
						>
							<ExternalLink className="size-icon-sm" aria-hidden="true" />
						</Button>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("jira.openInJira")}</TooltipContent>
				</Tooltip>
			}
			statusLabel={ticket.status.name}
			statusClassName={ticketCategoryTextClass(ticket.status.category)}
			time={ticket.updatedAt ? formatTimeCompact(ticket.updatedAt) : undefined}
			timeTitle={ticket.updatedAt ? new Date(ticket.updatedAt).toLocaleString() : undefined}
			footerAction={
				<>
					{showAssignee && assignee ? (
						<span
							className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-interactive-active text-[10px] font-medium text-foreground"
							title={t("jira.assignedTo", { name: assignee })}
							role="img"
							aria-label={t("jira.assignedTo", { name: assignee })}
						>
							{initialsOf(assignee)}
						</span>
					) : null}
					<Tooltip>
						<TooltipTrigger asChild>
							<Button
								type="button"
								variant="quiet"
								size="icon-xs"
								disabled={starting}
								aria-busy={starting}
								aria-label={t("jira.startAria", { key: ticket.key })}
								onClick={(event) => { event.stopPropagation(); onStart(); }}
							>
								{starting ? <LoaderCircle className="size-icon-sm animate-spin motion-reduce:animate-none" aria-hidden="true" /> : <Send className="size-icon-sm" aria-hidden="true" />}
							</Button>
						</TooltipTrigger>
						<TooltipContent side="bottom">{starting ? t("jira.starting") : t("jira.start")}</TooltipContent>
					</Tooltip>
				</>
			}
		>
			{orchestratorLinked || linked.length > 0 ? (
				<>
					{orchestratorLinked ? (
						<Pressable className={rowClass} onClick={(event) => { event.stopPropagation(); onOpenOrchestrator(orchestratorLinked); }}>
							<span className="size-1.5 shrink-0 rounded-full" style={{ backgroundColor: "var(--color-status-working)" }} aria-hidden="true" />
							<span className="truncate">{t("jira.sentToOrchestrator")}</span>
						</Pressable>
					) : null}
					{linked.slice(0, 2).map((session) => (
						<Pressable key={session.id} className={rowClass} onClick={(event) => { event.stopPropagation(); onOpenSession(session); }}>
							<span className="size-1.5 shrink-0 rounded-full" style={{ backgroundColor: "var(--color-status-working)" }} aria-hidden="true" />
							<span className="truncate">{session.title}</span>
						</Pressable>
					))}
					{linked.length > 2 ? <span className="text-2xs text-passive">{t("jira.moreSessions", { more: linked.length - 2 })}</span> : null}
				</>
			) : null}
		</BoardCardView>
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
