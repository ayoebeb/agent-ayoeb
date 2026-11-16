import { useNavigate } from "@tanstack/react-router";
import { useQueries } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { GitBranch, Users } from "@/components/app-icons";
import { useControlRoom, type ControlRoom } from "../../hooks/useControlRoom";
import { useCrew } from "../../hooks/useCrew";
import { sessionWorkspaceHistoryQueryOptions } from "../../hooks/useSessionWorkspaceFiles";
import { sessionUiKey } from "../../lib/hosts";
import { useShell } from "../../lib/shell-context";
import { initialTerminalFontSize } from "../../lib/terminal-font-size";
import { scopedRow, type CrewModel, type CrewRow } from "../../lib/crew";
import { sessionNavigateTarget } from "../../lib/navigate-to-session";
import { getSessionStatusDotView } from "../../lib/session-presentation";
import { formatRelativeTime } from "../../lib/usage-format";
import { cn } from "../../lib/utils";
import { useResolvedTheme, useUiStore } from "../../stores/ui-store";
import type { ProjectKind, WorkspaceSession } from "../../types/workspace";
import { AgentAvatar } from "../AgentAvatar";
import { SessionFileExplorer } from "../SessionFileExplorer";
import { TerminalPane } from "../TerminalPane";
import { Button } from "../ui/button";
import { Pressable } from "../ui/pressable";
import { shortBranch } from "./CrewRail";

/**
 * The control room's tabs other than Session. Each is scoped to all agents or to
 * one, and the scope survives a switch of tab. It reads the same crew model as the
 * rail, so a number here is the number there.
 */
export function ControlRoomBody({ orchestrator, projectKind }: { orchestrator: WorkspaceSession; projectKind?: ProjectKind }) {
	const { t } = useTranslation();
	const room = useControlRoom(orchestrator);
	const { model, now } = useCrew(orchestrator, true);
	const live = model.rows.filter((row) => row.group !== "done");
	const scoped = scopedRow(model, room.scope);
	return (
		<div className="absolute inset-0 z-10 flex min-h-0 flex-col bg-background" data-testid="control-room-body" role="tabpanel">
			<ScopeBar model={model} room={room} rows={live} />
			<div className="min-h-0 flex-1" data-testid={`control-room-${room.tab}`}>
				{room.tab === "changes" || room.tab === "files" ? (
					scoped ? (
						<ScopedExplorer key={`${room.tab}:${scoped.session.id}`} mode={room.tab} orchestrator={orchestrator} projectKind={projectKind} row={scoped} />
					) : (
						<WorktreeOverview model={model} onScope={(id) => room.setScope(id)} rows={live} />
					)
				) : room.tab === "commits" ? (
					<CommitsTab now={now.getTime()} orchestrator={orchestrator} rows={scoped ? [scoped] : live} />
				) : room.tab === "terminal" ? (
					scoped ? (
						<ScopedTerminal key={scoped.session.id} orchestrator={orchestrator} row={scoped} />
					) : (
						<ChooseAgent onScope={(id) => room.setScope(id)} rows={live} title={t("controlRoom.terminal.choose")} />
					)
				) : null}
			</div>
		</div>
	);
}

/** All agents, or one. The choice stays when the tab changes. */
function ScopeBar({ model, room, rows }: { model: CrewModel; room: ControlRoom; rows: CrewRow[] }) {
	const { t } = useTranslation();
	const all = room.scope === undefined || !rows.some((row) => row.session.id === room.scope);
	return (
		// Wraps instead of scrolling: a chip cut off at the edge hides who is in scope,
		// and a project rarely has more than two rows of workers.
		<div className="board-scrollbar flex max-h-24 shrink-0 flex-wrap items-center gap-1 overflow-y-auto border-b border-border px-2 py-1.5" data-testid="control-scope-bar" role="group" aria-label={t("controlRoom.scope.aria")}>
			<ScopeChip active={all} onClick={() => room.setScope(undefined)}>
				<Users aria-hidden="true" className="size-icon-sm shrink-0" />
				<span>{t("controlRoom.scope.all")}</span>
				{model.totals.worktrees > 0 ? <Diff additions={model.totals.additions} deletions={model.totals.deletions} /> : null}
			</ScopeChip>
			{rows.map((row) => {
				const dot = getSessionStatusDotView(row.session);
				return (
					<ScopeChip active={!all && room.scope === row.session.id} key={row.session.id} onClick={() => room.setScope(row.session.id)}>
						<span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full", dot.className, dot.breathe && "animate-status-pulse")} />
						<span className="max-w-40 truncate">{row.session.title}</span>
						{row.diff && row.diff.files.length > 0 ? <Diff additions={row.diff.additions} deletions={row.diff.deletions} /> : null}
					</ScopeChip>
				);
			})}
		</div>
	);
}

function ScopeChip({ active, children, onClick }: { active: boolean; children: ReactNode; onClick: () => void }) {
	return (
		<Pressable
			aria-pressed={active}
			className={cn(
				"inline-flex h-control-md shrink-0 items-center gap-1.5 rounded-md px-2 text-control text-muted-foreground hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring",
				active && "bg-interactive-active text-foreground",
			)}
			data-active={active ? "true" : undefined}
			onClick={onClick}
		>
			{children}
		</Pressable>
	);
}

function Diff({ additions, deletions }: { additions: number; deletions: number }) {
	return (
		<span className="inline-flex items-center gap-1 font-mono text-caption tabular-nums">
			<span className="text-success">+{additions}</span>
			<span className="text-destructive">−{deletions}</span>
		</span>
	);
}

/**
 * All agents: the files each worktree changed, grouped under the agent that
 * changed them, with a marker where another agent changed the same file. Picking
 * a file scopes the tabs to its agent, where the diff opens.
 */
function WorktreeOverview({ model, onScope, rows }: { model: CrewModel; onScope: (sessionId: string) => void; rows: CrewRow[] }) {
	const { t } = useTranslation();
	const withFiles = rows.filter((row) => (row.diff?.files.length ?? 0) > 0);
	if (withFiles.length === 0) return <EmptyState>{t("controlRoom.changes.none")}</EmptyState>;
	const titleOf = (id: string) => model.rows.find((row) => row.session.id === id)?.session.title ?? id;
	return (
		<div className="board-scrollbar h-full overflow-y-auto pb-4" data-testid="worktree-overview">
			{withFiles.map((row) => (
				<section className="pt-2" key={row.session.id}>
					<Pressable
						className="flex w-full items-center gap-2 px-3 py-1.5 text-control font-medium text-foreground hover:bg-interactive-hover focus-visible:outline-2 focus-visible:outline-ring"
						onClick={() => onScope(row.session.id)}
					>
						<GitBranch aria-hidden="true" className="size-icon-sm shrink-0 text-muted-foreground" />
						<span className="min-w-0 truncate">{row.session.title}</span>
						<span className="ml-auto"><Diff additions={row.diff!.additions} deletions={row.diff!.deletions} /></span>
					</Pressable>
					<ul className="flex flex-col">
						{row.diff!.files.map((file) => {
							const others = model.overlaps.find((overlap) => overlap.path === file.path)?.sessionIds.filter((id) => id !== row.session.id) ?? [];
							return (
								<li data-testid="worktree-file" key={file.path}>
									<Pressable
										className="flex w-full items-center gap-2 py-1 pl-8 pr-3 text-control text-muted-foreground hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
										onClick={() => onScope(row.session.id)}
									>
										<span className="min-w-0 flex-1 truncate font-mono" title={file.path}>{file.path}</span>
										{others.length > 0 ? (
											<span className="shrink-0 text-caption text-status-in-review" data-testid="worktree-file-also">
												{t("controlRoom.changes.also", { names: others.map(titleOf).join(", ") })}
											</span>
										) : null}
										<Diff additions={file.additions} deletions={file.deletions} />
									</Pressable>
								</li>
							);
						})}
					</ul>
				</section>
			))}
		</div>
	);
}

/**
 * One agent's changes or files, in the explorer the Files view already is. The
 * explorer remembers "changes only" per session, so this tab sets it for its own
 * purpose and leaves the choice to the explorer's own toggle afterwards.
 */
function ScopedExplorer({ mode, orchestrator, projectKind, row }: { mode: "changes" | "files"; orchestrator: WorkspaceSession; projectKind?: ProjectKind; row: CrewRow }) {
	const { t } = useTranslation();
	const setFilesChangedOnly = useUiStore((state) => state.setFilesChangedOnly);
	const hostId = orchestrator.hostId;
	const uiKey = sessionUiKey(row.session.id, hostId);
	useEffect(() => {
		setFilesChangedOnly(uiKey, mode === "changes");
	}, [mode, setFilesChangedOnly, uiKey]);
	if (!row.session.workspacePublished && row.session.workspacePublished !== undefined) {
		return <EmptyState>{t("controlRoom.workspacePending", { name: row.session.title })}</EmptyState>;
	}
	return (
		<div className="h-full min-h-0" data-testid="scoped-explorer">
			<SessionFileExplorer
				checkedOutBranch={row.session.branch}
				hostId={hostId}
				isMaximized
				projectKind={projectKind}
				sessionId={row.session.id}
			/>
		</div>
	);
}

/**
 * Commits of each agent's branch, newest first, with the ones that never left
 * this machine marked. Reads the same history the Files review lists.
 */
function CommitsTab({ now, orchestrator, rows }: { now: number; orchestrator: WorkspaceSession; rows: CrewRow[] }) {
	const { t, i18n } = useTranslation();
	const hostId = orchestrator.hostId;
	const targets = rows.filter((row) => !row.session.cloud && !row.session.needsResume && row.session.workspacePublished !== false);
	const histories = useQueries({
		queries: targets.map((row) => ({
			...sessionWorkspaceHistoryQueryOptions(row.session.id, undefined, hostId),
			// New commits arrive without a file event; read on a gentle clock.
			staleTime: 30_000,
			refetchInterval: 30_000,
		})),
	});
	if (targets.length === 0) return <EmptyState>{t("controlRoom.commits.none")}</EmptyState>;
	return (
		<div className="board-scrollbar h-full overflow-y-auto pb-4" data-testid="commits-tab">
			{targets.map((row, index) => {
				const history = histories[index];
				const commits = history?.data?.commits ?? [];
				const unpushed = Math.min(row.session.branchState?.unpushed ?? 0, commits.length);
				return (
					<section className="pt-2" key={row.session.id}>
						<header className="flex items-center gap-2 px-3 py-1.5 text-control">
							<span className="min-w-0 truncate font-medium text-foreground">{row.session.title}</span>
							<span className="min-w-0 truncate font-mono text-caption text-passive">{shortBranch(row.session.branch)}</span>
							{unpushed > 0 ? <span className="ml-auto shrink-0 text-caption text-status-in-review">{t("controlRoom.commits.notPushed", { count: unpushed })}</span> : null}
						</header>
						{history?.isLoading ? (
							<p className="px-3 py-1 text-sm text-passive">{t("controlRoom.commits.loading")}</p>
						) : history?.isError ? (
							<p className="px-3 py-1 text-sm text-passive" role="status">{t("controlRoom.commits.unavailable")}</p>
						) : commits.length === 0 ? (
							<p className="px-3 py-1 text-sm text-passive">{t("controlRoom.commits.empty")}</p>
						) : (
							<ul className="flex flex-col">
								{commits.map((commit, position) => (
									<li className="flex items-baseline gap-2 px-3 py-1 pl-8 text-control" data-testid="commit-row" key={commit.sha}>
										<span className="shrink-0 font-mono text-caption text-passive">{commit.sha.slice(0, 7)}</span>
										<span className="min-w-0 flex-1 truncate text-foreground" title={commit.subject}>{commit.subject}</span>
										{position < unpushed ? <span className="shrink-0 text-caption text-status-in-review">{t("controlRoom.commits.notPushedShort")}</span> : null}
										<span className="shrink-0 text-caption text-passive">{formatRelativeTime(commit.timestamp, new Date(now), i18n.language)}</span>
									</li>
								))}
							</ul>
						)}
					</section>
				);
			})}
		</div>
	);
}

/**
 * The agent's own terminal, watched. Typing is off until the person takes over, so
 * a glance at a worker can never type into it by accident. A chat agent has no
 * terminal of its own; it says so and offers its chat. A shell in the worker's
 * worktree is not wired up yet.
 */
function ScopedTerminal({ orchestrator, row }: { orchestrator: WorkspaceSession; row: CrewRow }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const { daemonStatus } = useShell();
	const theme = useResolvedTheme();
	const [takenOver, setTakenOver] = useState(false);
	const { session } = row;
	const openSession = () => void navigate(sessionNavigateTarget(session.workspaceId, session.id, orchestrator.hostId));
	if (session.mode === "chat" || orchestrator.hostId) {
		return (
			<EmptyState>
				<p>{t(session.mode === "chat" ? "controlRoom.terminal.chatAgent" : "controlRoom.terminal.remote", { name: session.title })}</p>
				<Button onClick={openSession} size="sm" variant="secondary">{t("controlRoom.terminal.openSession")}</Button>
			</EmptyState>
		);
	}
	return (
		<div className="flex h-full min-h-0 flex-col" data-testid="scoped-terminal">
			<div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1.5">
				<AgentAvatar className="size-4 shrink-0" decorative provider={session.provider} />
				<span className="min-w-0 truncate text-control font-medium text-foreground">{session.title}</span>
				<span className="text-caption text-passive" data-testid="terminal-mode">
					{takenOver ? t("controlRoom.terminal.typing") : t("controlRoom.terminal.watching")}
				</span>
				<Button
					aria-pressed={takenOver}
					className="ml-auto"
					onClick={() => setTakenOver((current) => !current)}
					size="xs"
					variant={takenOver ? "secondary" : "primary"}
				>
					{takenOver ? t("controlRoom.terminal.stopTakeOver") : t("controlRoom.terminal.takeOver")}
				</Button>
			</div>
			<div className="min-h-0 flex-1">
				<TerminalPane
					daemonReady={daemonStatus.state === "ready"}
					focusRequested={takenOver}
					fontSize={initialTerminalFontSize()}
					inputDisabled={!takenOver}
					session={session}
					terminalGeneration={session.terminalGeneration}
					terminalTarget={{ kind: "worker" }}
					theme={theme}
				/>
			</div>
		</div>
	);
}

/** The terminal tab with no agent picked: choose whose to watch. */
function ChooseAgent({ onScope, rows, title }: { onScope: (sessionId: string) => void; rows: CrewRow[]; title: string }) {
	const { t } = useTranslation();
	if (rows.length === 0) return <EmptyState>{t("crew.empty")}</EmptyState>;
	return (
		<div className="board-scrollbar h-full overflow-y-auto pb-4" data-testid="choose-agent">
			<h2 className="px-3 pb-1 pt-3 text-caption text-muted-foreground">{title}</h2>
			<ul className="flex flex-col divide-y divide-border">
				{rows.map((row) => (
					<li key={row.session.id}>
						<Pressable
							className="flex w-full items-center gap-2 px-3 py-2 text-control text-foreground hover:bg-interactive-hover focus-visible:outline-2 focus-visible:outline-ring"
							onClick={() => onScope(row.session.id)}
						>
							<AgentAvatar className="size-4 shrink-0" decorative provider={row.session.provider} />
							<span className="min-w-0 flex-1 truncate">{row.session.title}</span>
							<span className="shrink-0 text-caption text-passive">
								{row.session.mode === "chat" ? t("controlRoom.terminal.kindChat") : t("controlRoom.terminal.kindTerminal")}
							</span>
						</Pressable>
					</li>
				))}
			</ul>
		</div>
	);
}

function EmptyState({ children }: { children: ReactNode }) {
	return (
		<div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-passive" data-testid="control-room-empty">
			{children}
		</div>
	);
}
