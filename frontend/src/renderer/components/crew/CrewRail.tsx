import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	AlertTriangle,
	ChevronRight,
	Clock,
	FileDiff,
	GitCommitHorizontal,
	MessageSquare,
	SquareTerminal,
} from "@/components/app-icons";
import { useControlRoom } from "../../hooks/useControlRoom";
import { useConversationCommands } from "../../hooks/useConversation";
import { crewConversationQueryKey, useCrew, useCrewConversation } from "../../hooks/useCrew";
import { clientForSessionHost } from "../../lib/host-clients";
import {
	compactAge,
	lastSignalAt,
	planProgress,
	quietNudgeMessage,
	type CrewGroupId,
	type CrewRow,
	type FileOverlap,
	type CrewModel,
} from "../../lib/crew";
import { sessionNavigateTarget } from "../../lib/navigate-to-session";
import { getSessionStatusDotView, getSessionStatusView } from "../../lib/session-presentation";
import { cn } from "../../lib/utils";
import type { ConversationActivity } from "../../types/conversation";
import { primaryPR, type WorkspaceSession } from "../../types/workspace";
import { AgentAvatar } from "../AgentAvatar";
import { Button } from "../ui/button";
import { Pressable } from "../ui/pressable";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

const GROUP_LABEL_KEYS = {
	needsYou: "crew.group.needsYou",
	working: "crew.group.working",
	inReview: "crew.group.inReview",
	done: "crew.group.done",
} as const satisfies Record<CrewGroupId, string>;

const OVERLAPS_SHOWN = 3;

/**
 * The branch without the namespace every row would repeat: `ao/<task>/root`
 * reads as `<task>`. Any other branch is shown whole.
 */
export function shortBranch(branch: string | undefined): string | undefined {
	if (!branch) return undefined;
	const match = /^ao\/(.+?)(?:\/root)?$/.exec(branch);
	return match?.[1] ?? branch;
}

/**
 * The orchestrator's crew: every worker of the project in one list, grouped by
 * what the person has to do about it. A shared list with dividers, not a card per
 * agent. Rows answer "who is doing what, and does it need me" without opening a session.
 */
export function CrewRail({ active = true, orchestrator }: { active?: boolean; orchestrator: WorkspaceSession }) {
	const { t } = useTranslation();
	const { model, now } = useCrew(orchestrator, true);
	const room = useControlRoom(orchestrator);
	const [doneOpen, setDoneOpen] = useState(false);
	const counts = (["needsYou", "working", "inReview"] as const)
		.map((id) => ({ id, count: model.groups.find((group) => group.id === id)?.rows.length ?? 0 }))
		.filter((entry) => entry.count > 0)
		.map((entry) => t(`crew.counts.${entry.id}`, { count: entry.count }));

	return (
		<section aria-label={t("crew.aria")} className="flex h-full min-h-0 flex-col" data-testid="crew-rail">
			<header className="flex shrink-0 items-baseline gap-2 px-3 pb-1.5 pt-2.5">
				<h2 className="text-base font-semibold text-foreground">{t("crew.title")}</h2>
				<p className="min-w-0 truncate text-caption text-passive" data-testid="crew-counts">
					{counts.join(" · ")}
				</p>
			</header>
			<div className="board-scrollbar min-h-0 flex-1 overflow-y-auto pb-3">
				{model.rows.length === 0 ? (
					<p className="px-3 py-2 text-sm text-passive" data-testid="crew-empty">{t("crew.empty")}</p>
				) : (
					model.groups
						.filter((group) => group.rows.length > 0)
						.map((group) => (
							<CrewGroup
								collapsible={group.id === "done"}
								count={group.rows.length}
								key={group.id}
								label={t(GROUP_LABEL_KEYS[group.id])}
								onOpenChange={setDoneOpen}
								open={group.id !== "done" || doneOpen}
							>
								<ul className="flex flex-col divide-y divide-border">
									{group.rows.map((row) => (
										<CrewRowView active={active} key={row.session.id} now={now.getTime()} orchestrator={orchestrator} room={room} row={row} />
									))}
								</ul>
							</CrewGroup>
						))
				)}
				<OverlapNotice model={model} onReview={() => room.show("changes")} />
			</div>
		</section>
	);
}

function CrewGroup({
	children,
	collapsible,
	count,
	label,
	onOpenChange,
	open,
}: {
	children: ReactNode;
	collapsible: boolean;
	count: number;
	label: string;
	onOpenChange: (open: boolean) => void;
	open: boolean;
}) {
	const heading = (
		<>
			<span className="min-w-0 truncate">{label}</span>
			<span className="tabular-nums text-passive">{count}</span>
		</>
	);
	return (
		<div className="pt-1.5" data-testid="crew-group">
			{collapsible ? (
				<Pressable
					aria-expanded={open}
					className="flex w-full items-center gap-1.5 px-3 py-1 text-caption text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
					onClick={() => onOpenChange(!open)}
				>
					<ChevronRight aria-hidden="true" className={cn("size-icon-2xs shrink-0 transition-transform duration-(--duration-fast)", open && "rotate-90")} />
					{heading}
				</Pressable>
			) : (
				<h3 className="flex items-center gap-1.5 px-3 py-1 text-caption text-muted-foreground">{heading}</h3>
			)}
			{open ? children : null}
		</div>
	);
}

type CrewRowProps = {
	/** The rail is on screen. A hidden rail stays mounted, so it must not keep polling workers. */
	active: boolean;
	now: number;
	orchestrator: WorkspaceSession;
	room: ReturnType<typeof useControlRoom>;
	row: CrewRow;
};

function CrewRowView({ active, now, orchestrator, room, row }: CrewRowProps) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const { session } = row;
	const watched = active && (row.group === "needsYou" || row.group === "working");
	const conversation = useCrewConversation(session, watched);
	const progress = planProgress(conversation?.plan);
	const step = progress && !progress.allDone ? conversation?.plan?.steps[progress.activeIndex] : undefined;
	const dot = getSessionStatusDotView(session);
	const statusLabel = session.displayStatus ?? getSessionStatusView(session.status, t).label;
	const pending = conversation?.approval ?? conversation?.question;
	const line = pending
		? pending.summary
		: step && progress
			? t("crew.row.step", { current: progress.activeIndex + 1, total: progress.total, text: step.text })
			: statusLabel;
	const last = lastSignalAt(session);
	const age = last === undefined ? undefined : compactAge(now - last);
	const pr = primaryPR(session);
	const branch = shortBranch(session.branch);
	const selected = room.scope === session.id;
	const openChat = () => void navigate(sessionNavigateTarget(session.workspaceId, session.id, orchestrator.hostId));

	return (
		<li
			className={cn("group/crew relative px-3 py-2", selected && "bg-interactive-active")}
			data-session-id={session.id}
			data-testid="crew-row"
		>
			<div className="flex min-w-0 items-center gap-2">
				<span
					aria-hidden="true"
					className={cn("size-2 shrink-0 rounded-full", dot.className, dot.breathe && "animate-status-pulse")}
					data-session-status={session.status}
				/>
				<AgentAvatar className="size-4 shrink-0" decorative provider={session.provider} />
				<Pressable
					className="min-w-0 flex-1 truncate text-control font-medium text-foreground focus-visible:outline-2 focus-visible:outline-ring"
					onClick={() => room.setScope(selected ? undefined : session.id)}
					title={session.title}
				>
					{session.title}
				</Pressable>
				{age ? (
					<span className="shrink-0 text-caption tabular-nums text-passive transition-opacity duration-(--duration-fast) group-focus-within/crew:opacity-0 group-hover/crew:opacity-0">
						{t(`crew.age.${age.unit}`, { count: age.count })}
					</span>
				) : null}
			</div>
			{/* Lines up under the title: status dot, gap, avatar, gap. */}
			<div className="pl-10">
				<p className="line-clamp-2 text-sm text-muted-foreground" data-testid="crew-line" title={line}>
					{line}
				</p>
				<p className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-caption text-passive" data-testid="crew-meta">
					{branch ? <span className="min-w-0 max-w-full truncate font-mono" title={session.branch}>{branch}</span> : null}
					<DiffStat row={row} />
					{pr ? <PrSummary session={session} /> : null}
				</p>
				{row.quietMs !== undefined ? <QuietNudge orchestrator={orchestrator} row={row} /> : null}
				{conversation?.approval ? (
					<ApprovalPrompt approval={conversation.approval} onOpenChat={openChat} session={session} />
				) : conversation?.question ? (
					<QuestionPrompt onOpenChat={openChat} question={conversation.question} />
				) : null}
				{row.group === "needsYou" && !pending && session.mode !== "chat" ? (
					<div className="mt-1.5">
						<Button onClick={() => room.show("terminal", session.id)} size="xs" variant="secondary">
							{t("crew.openTerminal")}
						</Button>
					</div>
				) : null}
			</div>
			<div className="absolute right-2 top-1.5 flex items-center opacity-0 transition-opacity duration-(--duration-fast) group-focus-within/crew:opacity-100 group-hover/crew:opacity-100">
				<RowAction icon={<FileDiff />} label={t("crew.action.changes", { name: session.title })} onClick={() => room.show("changes", session.id)} />
				<RowAction icon={<GitCommitHorizontal />} label={t("crew.action.commits", { name: session.title })} onClick={() => room.show("commits", session.id)} />
				<RowAction icon={<SquareTerminal />} label={t("crew.action.terminal", { name: session.title })} onClick={() => room.show("terminal", session.id)} />
				<RowAction icon={<MessageSquare />} label={t("crew.action.chat", { name: session.title })} onClick={openChat} />
			</div>
		</li>
	);
}

function RowAction({ icon, label, onClick }: { icon: ReactNode; label: string; onClick: () => void }) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Button aria-label={label} onClick={onClick} size="icon-xs" variant="quiet">
					{icon}
				</Button>
			</TooltipTrigger>
			<TooltipContent side="bottom">{label}</TooltipContent>
		</Tooltip>
	);
}

function DiffStat({ row }: { row: CrewRow }) {
	const { t } = useTranslation();
	const { diff } = row;
	if (!diff) return null;
	if (diff.files.length === 0) return <span>{t("crew.row.noChanges")}</span>;
	return (
		<span className="inline-flex items-center gap-1.5 tabular-nums" data-testid="crew-diff">
			<span className="font-mono text-success">+{diff.additions}</span>
			<span className="font-mono text-destructive">−{diff.deletions}</span>
			<span>{t("crew.row.files", { count: diff.files.length })}</span>
		</span>
	);
}

function PrSummary({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	const pr = primaryPR(session);
	if (!pr) return null;
	const stateLabel = t(`pr.state.${pr.state}`, { defaultValue: pr.state });
	const ci = pr.ci && pr.ci !== "unknown" ? ` · CI ${pr.ci}` : "";
	return (
		<span className="min-w-0 truncate" data-testid="crew-pr">
			{t("pr.short")} #{pr.number} · {stateLabel}
			{ci}
			{session.prs.length > 1 ? ` +${session.prs.length - 1}` : ""}
		</span>
	);
}

/**
 * The fact that an agent went quiet, and the one step the person can take. It
 * says how long, never "stalled": a long build is quiet too. The step asks the
 * orchestrator to ask the worker for a report; the orchestrator keeps control.
 */
function QuietNudge({ orchestrator, row }: { orchestrator: WorkspaceSession; row: CrewRow }) {
	const { t } = useTranslation();
	const [state, setState] = useState<"idle" | "sending" | "sent" | "failed">("idle");
	const quiet = compactAge(row.quietMs ?? 0);
	const send = async () => {
		setState("sending");
		const { error } = await clientForSessionHost(orchestrator.hostId).POST("/api/v1/sessions/{sessionId}/send", {
			params: { path: { sessionId: orchestrator.id } },
			body: { message: quietNudgeMessage(row.session, row.quietMs ?? 0) },
		});
		setState(error ? "failed" : "sent");
	};
	return (
		<div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1" data-testid="crew-quiet">
			<span className="inline-flex items-center gap-1 text-caption text-status-in-review">
				<Clock aria-hidden="true" className="size-icon-xs" />
				{t("crew.quiet.label", { duration: t(`crew.age.${quiet.unit}`, { count: quiet.count }) })}
			</span>
			{state === "sent" ? (
				<span className="text-caption text-passive" role="status">{t("crew.quiet.sent")}</span>
			) : (
				<Button disabled={state === "sending"} onClick={() => void send()} size="xs" variant="secondary">
					{t("crew.quiet.nudge")}
				</Button>
			)}
			{state === "failed" ? <span className="text-caption text-destructive" role="alert">{t("crew.quiet.failed")}</span> : null}
		</div>
	);
}

/**
 * A command or edit a chat worker waits on, answered where it is read. Only what
 * the provider offers is shown, by its meaning (allow once, deny) in our own words
 * and sentence case, sending the provider's own decision id. "Always allow" stays
 * in the chat: it is a standing permission, and the rail answers one request at a time.
 */
function ApprovalPrompt({ approval, onOpenChat, session }: { approval: ConversationActivity; onOpenChat: () => void; session: WorkspaceSession }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const commands = useConversationCommands(session.id, session.hostId);
	const [answered, setAnswered] = useState<string>();
	const decisions = approval.decisions ?? [];
	const allow = decisions.find((decision) => decision.kind === "allow_once");
	const deny = decisions.find((decision) => decision.kind === "reject_once");
	const requestId = approval.requestId;
	const sending = answered !== undefined && answered === requestId;
	const answer = (decisionId: string) => {
		if (!requestId) return;
		setAnswered(requestId);
		commands.resolve(requestId, decisionId);
		window.setTimeout(() => void queryClient.invalidateQueries({ queryKey: crewConversationQueryKey(session.id, session.hostId) }), 800);
	};
	return (
		<div className="mt-1.5 flex flex-col gap-1.5 rounded-md bg-interactive-hover px-2 py-1.5" data-testid="crew-approval">
			<div className="flex flex-wrap items-center gap-1.5">
				{requestId && allow ? (
					<Button disabled={sending} onClick={() => answer(allow.id)} size="xs" variant="secondary">
						{t("crew.approval.allowOnce")}
					</Button>
				) : null}
				{requestId && deny ? (
					<Button disabled={sending} onClick={() => answer(deny.id)} size="xs" variant="destructive-ghost">
						{t("crew.approval.deny")}
					</Button>
				) : null}
				<Button onClick={onOpenChat} size="xs" variant="quiet">
					{t("crew.approval.openChat")}
				</Button>
				{sending ? <span className="text-caption text-passive" role="status">{t("crew.approval.sending")}</span> : null}
			</div>
		</div>
	);
}

/** A structured question is answered in the worker's chat, where its form lives. */
function QuestionPrompt({ onOpenChat }: { onOpenChat: () => void; question: ConversationActivity }) {
	const { t } = useTranslation();
	return (
		<div className="mt-1.5" data-testid="crew-question">
			<Button onClick={onOpenChat} size="xs" variant="secondary">
				{t("crew.approval.answerInChat")}
			</Button>
		</div>
	);
}

/** Files two or more agents changed: say so before the merge does. */
export function OverlapNotice({ model, onReview }: { model: CrewModel; onReview: () => void }) {
	const { t } = useTranslation();
	if (model.overlaps.length === 0) return null;
	const nameOf = (id: string) => model.rows.find((row) => row.session.id === id)?.session.title ?? id;
	const shown = model.overlaps.slice(0, OVERLAPS_SHOWN);
	const rest = model.overlaps.length - shown.length;
	return (
		<section aria-label={t("crew.overlap.title")} className="mx-3 mt-3 flex flex-col gap-1.5 rounded-lg bg-interactive-hover px-3 py-2" data-testid="crew-overlap">
			<h3 className="flex items-center gap-1.5 text-sm font-medium text-foreground">
				<AlertTriangle aria-hidden="true" className="size-icon-sm shrink-0 text-status-in-review" />
				{t("crew.overlap.title")}
			</h3>
			<ul className="flex flex-col gap-1.5">
				{shown.map((overlap) => (
					<OverlapItem key={overlap.path} nameOf={nameOf} overlap={overlap} />
				))}
			</ul>
			{rest > 0 ? <p className="text-caption text-passive">{t("crew.overlap.more", { count: rest })}</p> : null}
			<div>
				<Button onClick={onReview} size="xs" variant="secondary">
					{t("crew.overlap.review")}
				</Button>
			</div>
		</section>
	);
}

function OverlapItem({ nameOf, overlap }: { nameOf: (id: string) => string; overlap: FileOverlap }) {
	const { t } = useTranslation();
	return (
		<li className="text-sm text-muted-foreground" data-testid="crew-overlap-item">
			<span className="break-all font-mono text-foreground">{overlap.path}</span>
			<span> · {overlap.sessionIds.map(nameOf).join(", ")}</span>
			<p className="text-caption text-passive">{t("crew.overlap.mergeFirst", { name: nameOf(overlap.sessionIds[0]!) })}</p>
		</li>
	);
}
