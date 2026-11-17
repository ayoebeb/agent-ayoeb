import { useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, CircleStop, GitBranch, Monitor, Pin, PinOff, Server } from "@/components/app-icons";
import { useConversation, useConversationCommands } from "../../hooks/useConversation";
import { useControlRoom } from "../../hooks/useControlRoom";
import { useCrew } from "../../hooks/useCrew";
import { usePinSession, useUnpinSession } from "../../hooks/usePinSession";
import { currentPlan, planProgress, type CrewModel, type PlanProgress } from "../../lib/crew";
import { labelForHost } from "../../lib/host-clients";
import { formatTokens } from "../../lib/usage-format";
import { cn } from "../../lib/utils";
import type { ConversationPlan, ConversationUsage } from "../../types/conversation";
import { activeTurn } from "../../types/conversation";
import type { ControlRoomTab } from "../../stores/ui-store";
import type { WorkspaceSession } from "../../types/workspace";
import { Button } from "../ui/button";
import { Pressable } from "../ui/pressable";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

export const CONTROL_ROOM_TABS: readonly ControlRoomTab[] = ["session", "changes", "commits", "files", "terminal"];

const TAB_LABEL_KEYS = {
	session: "controlRoom.tab.session",
	changes: "controlRoom.tab.changes",
	commits: "controlRoom.tab.commits",
	files: "controlRoom.tab.files",
	terminal: "controlRoom.tab.terminal",
} as const satisfies Record<ControlRoomTab, string>;

/** What each tab counts, where it has a number worth showing. */
export function controlRoomTabCounts(model: CrewModel): Partial<Record<ControlRoomTab, number>> {
	const live = model.rows.filter((row) => row.group !== "done");
	const files = new Set(live.flatMap((row) => (row.diff?.files ?? []).map((file) => `${row.session.id}:${file.path}`)));
	const commits = live.reduce((sum, row) => sum + (row.session.branchState?.commits ?? 0), 0);
	return { changes: files.size || undefined, commits: commits || undefined };
}

/**
 * The orchestrator's control room, above its session: the tabs that scope to one
 * agent or all of them, the actions that belong to the orchestrator itself, the
 * numbers that describe the whole project, and how far its plan has got.
 * The tab strip keeps the app's 36px; the info row and plan bar can fold away.
 */
export function OrchestratorControlBar({ orchestrator }: { orchestrator: WorkspaceSession }) {
	const { t } = useTranslation();
	const room = useControlRoom(orchestrator);
	// The one caller that keeps the worktrees fresh; the rail and tabs read the same cache.
	const { model } = useCrew(orchestrator, true, true);
	const { snapshot } = useConversation(orchestrator.mode === "chat" ? orchestrator.id : undefined, orchestrator.hostId);
	const [detailsOpen, setDetailsOpen] = useState(true);
	const counts = controlRoomTabCounts(model);
	const plan = currentPlan(snapshot);
	const progress = planProgress(plan);

	const onTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
		const last = CONTROL_ROOM_TABS.length - 1;
		const next = event.key === "ArrowRight" ? (index >= last ? 0 : index + 1)
			: event.key === "ArrowLeft" ? (index <= 0 ? last : index - 1)
			: event.key === "Home" ? 0
			: event.key === "End" ? last
			: undefined;
		if (next === undefined) return;
		event.preventDefault();
		room.setTab(CONTROL_ROOM_TABS[next]!);
		event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]').item(next).focus();
	};

	return (
		<div className="shrink-0 border-b border-border-strong bg-background" data-testid="orchestrator-control-bar">
			<div className="flex h-toolbar items-stretch gap-3 pl-2 pr-2">
				<div aria-label={t("controlRoom.tabs.aria")} className="flex min-w-0 items-stretch" role="tablist">
					{CONTROL_ROOM_TABS.map((tab, index) => {
						const active = room.tab === tab;
						const count = counts[tab];
						return (
							<Pressable
								aria-selected={active}
								className={cn(
									"relative inline-flex shrink-0 items-center gap-1.5 px-2.5 text-control font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
									"after:absolute after:inset-x-2.5 after:bottom-0 after:h-0.5 after:rounded-full after:bg-transparent",
									active && "text-foreground after:bg-foreground",
								)}
								data-active={active ? "true" : undefined}
								data-testid={`control-tab-${tab}`}
								key={tab}
								onClick={() => room.setTab(tab)}
								onKeyDown={(event) => onTabKeyDown(event, index)}
								role="tab"
								tabIndex={active ? 0 : -1}
							>
								{t(TAB_LABEL_KEYS[tab])}
								{count ? <span className="tabular-nums text-caption text-passive">{count}</span> : null}
							</Pressable>
						);
					})}
				</div>
				<div className="ml-auto flex shrink-0 items-center gap-0.5">
					<PinButton orchestrator={orchestrator} />
					<StopTurnButton orchestrator={orchestrator} running={snapshot ? activeTurn(snapshot)?.state === "running" : false} />
					<Tooltip>
						<TooltipTrigger asChild>
							<Button
								aria-expanded={detailsOpen}
								aria-label={detailsOpen ? t("controlRoom.collapse") : t("controlRoom.expand")}
								onClick={() => setDetailsOpen((open) => !open)}
								size="icon-sm"
								variant="quiet"
							>
								<ChevronDown aria-hidden="true" className={cn("size-icon-md transition-transform duration-(--duration-fast)", !detailsOpen && "-rotate-90")} />
							</Button>
						</TooltipTrigger>
						<TooltipContent side="bottom">{detailsOpen ? t("controlRoom.collapse") : t("controlRoom.expand")}</TooltipContent>
					</Tooltip>
				</div>
			</div>
			{detailsOpen ? (
				<>
					<InfoRow model={model} orchestrator={orchestrator} usage={snapshot?.usage} fallbackModel={snapshot?.settings.model} />
					{progress && plan ? <PlanBar needsYou={model.needsYouCount} plan={plan} progress={progress} /> : null}
				</>
			) : null}
		</div>
	);
}

function PinButton({ orchestrator }: { orchestrator: WorkspaceSession }) {
	const { t } = useTranslation();
	const pin = usePinSession();
	const unpin = useUnpinSession();
	const pinned = Boolean(orchestrator.isPinned);
	const label = pinned ? t("controlRoom.unpin") : t("controlRoom.pin");
	return (
		<Button
			aria-pressed={pinned}
			disabled={pin.isPending || unpin.isPending}
			onClick={() => (pinned ? unpin.mutate(orchestrator) : pin.mutate(orchestrator))}
			size="sm"
			variant="quiet"
		>
			{pinned ? <PinOff aria-hidden="true" className="size-icon-md" /> : <Pin aria-hidden="true" className="size-icon-md" />}
			{label}
		</Button>
	);
}

/** Visible only while a turn runs: there is nothing to stop otherwise. */
function StopTurnButton({ orchestrator, running }: { orchestrator: WorkspaceSession; running: boolean }) {
	const { t } = useTranslation();
	const commands = useConversationCommands(orchestrator.id, orchestrator.hostId, orchestrator.isTerminated);
	if (!running) return null;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Button onClick={() => commands.interrupt()} size="sm" variant="destructive-ghost">
					<CircleStop aria-hidden="true" className="size-icon-md" />
					{t("controlRoom.stopTurn")}
				</Button>
			</TooltipTrigger>
			<TooltipContent side="bottom">{t("controlRoom.stopTurnHint")}</TooltipContent>
		</Tooltip>
	);
}

function InfoRow({
	fallbackModel,
	model,
	orchestrator,
	usage,
}: {
	fallbackModel?: string;
	model: CrewModel;
	orchestrator: WorkspaceSession;
	usage?: ConversationUsage;
}) {
	const { t, i18n } = useTranslation();
	const locale = i18n.language;
	const modelName = orchestrator.model ?? fallbackModel;
	const remoteLabel = orchestrator.hostId ? labelForHost(orchestrator.hostId) : undefined;
	const { additions, deletions, worktrees } = model.totals;
	return (
		<div className="flex min-h-8 flex-wrap items-center gap-x-3 gap-y-1 px-3 pb-1.5 text-caption text-muted-foreground" data-testid="control-info-row">
			{orchestrator.branch ? (
				<span className="inline-flex min-w-0 items-center gap-1" title={t("controlRoom.info.branch")}>
					<GitBranch aria-hidden="true" className="size-icon-xs shrink-0" />
					<span className="truncate font-mono text-foreground">{orchestrator.branch}</span>
				</span>
			) : null}
			{worktrees > 0 ? (
				<span className="inline-flex items-center gap-1.5 tabular-nums" data-testid="control-info-diff">
					<span className="font-mono text-success">+{additions}</span>
					<span className="font-mono text-destructive">−{deletions}</span>
					<span>{t("controlRoom.info.worktrees", { count: worktrees })}</span>
				</span>
			) : null}
			<UsageChunk locale={locale} usage={usage} />
			{modelName ? <span className="font-mono text-foreground" title={t("controlRoom.info.model")}>{modelName}</span> : null}
			<span className="ml-auto inline-flex items-center gap-1" data-testid="control-info-machine" title={t("controlRoom.info.machine")}>
				{orchestrator.hostId ? <Server aria-hidden="true" className="size-icon-xs" /> : <Monitor aria-hidden="true" className="size-icon-xs" />}
				{orchestrator.hostId ? remoteLabel ?? orchestrator.hostId : t("remote.thisComputer")}
			</span>
		</div>
	);
}

/** The provider's cumulative money, in the currency it reported (major units). */
export function formatCost(amount: number, currency: string | undefined, locale: string): string {
	try {
		return new Intl.NumberFormat(locale, { style: "currency", currency: currency || "USD" }).format(amount);
	} catch {
		// An unknown currency code must not break the bar.
		return `${amount.toFixed(2)} ${currency ?? ""}`.trim();
	}
}

/**
 * The orchestrator's own tokens and, when the provider reports it, cost. A roll-up
 * across workers is not available yet, so the chunk says whose numbers these are
 * instead of passing them off as the project's. Cost the account does not report
 * is a dash, never zero.
 */
function UsageChunk({ locale, usage }: { locale: string; usage?: ConversationUsage }) {
	const { t } = useTranslation();
	const cost = usage && typeof usage.cost === "number" ? formatCost(usage.cost, usage.currency, locale) : undefined;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span className="inline-flex items-center gap-1.5 tabular-nums" data-testid="control-info-usage">
					{usage ? (
						<>
							<span>{t("controlRoom.info.tokensIn", { value: formatTokens(usage.inputTokens, locale) })}</span>
							<span>{t("controlRoom.info.tokensOut", { value: formatTokens(usage.outputTokens, locale) })}</span>
							<span>{cost ?? t("controlRoom.info.noCost")}</span>
						</>
					) : (
						<span>{t("controlRoom.info.noUsage")}</span>
					)}
					<span className="text-passive">{t("controlRoom.info.usageScope")}</span>
				</span>
			</TooltipTrigger>
			<TooltipContent side="bottom">{t("controlRoom.info.usageHint")}</TooltipContent>
		</Tooltip>
	);
}

/** One segment per step of the orchestrator's plan, in the board's status colors. */
function PlanBar({ needsYou, plan, progress }: { needsYou: number; plan: ConversationPlan; progress: PlanProgress }) {
	const { t } = useTranslation();
	const current = progress.allDone ? undefined : plan.steps[progress.activeIndex]?.text;
	return (
		<div className="flex items-center gap-3 px-3 pb-2" data-testid="control-plan-bar">
			<span className="shrink-0 text-control font-medium text-foreground">{t("controlRoom.plan.title")}</span>
			<ol aria-label={t("controlRoom.plan.aria")} className="flex min-w-0 flex-1 items-center gap-1">
				{plan.steps.map((step, index) => (
					<li
						aria-label={`${step.text} — ${t(`controlRoom.plan.status.${step.status}`)}`}
						className={cn(
							"h-1 min-w-2 flex-1 rounded-full",
							step.status === "completed" && "bg-status-ready",
							step.status === "in_progress" && "bg-status-working",
							step.status === "pending" && "bg-border-strong",
						)}
						data-step-status={step.status}
						key={`${index}-${step.text}`}
					/>
				))}
			</ol>
			<span className="shrink-0 text-caption tabular-nums text-muted-foreground" data-testid="control-plan-count">
				{t("controlRoom.plan.done", { done: progress.done, total: progress.total })}
				{needsYou > 0 ? ` · ${t("crew.counts.needsYou", { count: needsYou })}` : ""}
			</span>
			{current ? <span className="hidden min-w-0 max-w-[30%] truncate text-caption text-passive @3xl:inline" title={current}>{current}</span> : null}
		</div>
	);
}
