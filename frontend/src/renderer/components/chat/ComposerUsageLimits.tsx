/**
 * The Claude subscription's 5-hour and weekly limits, beside the model in the
 * chat composer.
 *
 * It is the Usage page's Limits tab in miniature and reads the same query, so
 * the two never disagree: the share left, a bar in Claude's tone that turns
 * amber at a quarter left and red when spent, and the pace marker. The tooltip
 * carries the reset countdowns and the pace line in the Limits tab's words.
 *
 * It follows the account the session runs under, else the default login. A
 * failed refresh keeps the last numbers and says how old they are, as the
 * Limits tab does. The wrapper only takes the room left in the row, so a narrow
 * composer drops the bars and then the whole readout before the model or the
 * send button move.
 */

import { Fragment } from "react";
import { TriangleAlert } from "@/components/app-icons";
import { useTranslation } from "react-i18next";
import { agentAccountName, useAgentAccounts, useSessionAgentAccounts, type AgentAccount, type AgentAccountLimitWindow } from "../../hooks/useAgentAccounts";
import { useNow } from "../../hooks/useNow";
import { agentTone } from "../../lib/agent-tone";
import { formatRelativeTime } from "../../lib/usage-format";
import { formatCountdown, groupAccounts, limitWindowPace, PROVIDER_HARNESS, remainingPercent, remainingTone } from "../../lib/usage-limits";
import { cn } from "../../lib/utils";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../ui/tooltip";

const CLAUDE_HARNESS = PROVIDER_HARNESS.claude;

/** The two windows shown, by the daemon's window id, with their short inline names. */
const WINDOWS = [
	{ id: "five_hour", short: "usage.composer.fiveHour" },
	{ id: "seven_day", short: "usage.composer.weekly" },
] as const;

// Matches the context meter beside it: a border separates the tooltip without
// the layered shadow's hover halos.
const TOOLTIP_SURFACE = "border border-(--color-border-overlay) shadow-none";
const TRIGGER_CLASS =
	"inline-flex h-7 min-w-0 shrink-0 cursor-default items-center gap-4 rounded-lg px-2 text-xs text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring @max-[8.5rem]/usage:hidden";
const BAR_CLASS = "relative h-1 w-6 shrink-0 overflow-hidden rounded-full bg-muted @max-[12.5rem]/usage:hidden";

/** The account a Claude session runs under: its bound account, else the default login. */
export function sessionClaudeAccount(accounts: readonly AgentAccount[], boundId: string | undefined): AgentAccount | undefined {
	const claude = groupAccounts(accounts).find((entry) => entry.provider === "claude")?.accounts ?? [];
	return claude.find((account) => account.id === boundId) ?? claude[0];
}

export function ComposerUsageLimits({ harness, sessionId }: { harness?: string; sessionId?: string }) {
	const shown = harness === CLAUDE_HARNESS;
	const accounts = useAgentAccounts(shown);
	const boundSessionId = shown ? sessionId : undefined;
	const binding = useSessionAgentAccounts(boundSessionId);
	if (!shown) return null;

	// Wait for the session's own account rather than flash the default one's numbers.
	const loading = (!accounts.data && accounts.isPending) || (Boolean(boundSessionId) && binding.isPending);
	const account = accounts.data ? sessionClaudeAccount(accounts.data, binding.data?.claude) : undefined;
	const windows = account?.limits?.windows ?? [];
	const shownWindows = WINDOWS.flatMap((entry) => {
		const window = windows.find((candidate) => candidate.id === entry.id);
		return window ? [{ ...entry, window }] : [];
	});
	if (!loading && (!account || shownWindows.length === 0)) return null;

	return (
		<div className="@container/usage flex h-7 min-w-0 flex-1 items-center" data-composer-usage="">
			{loading || !account ? (
				<UsagePlaceholder />
			) : (
				<UsageReadout
					account={account}
					windows={shownWindows}
					// The query keeps its last data when a refetch fails; say the numbers are old.
					refreshFailed={Boolean(accounts.error)}
					namesAccount={(accounts.data ?? []).filter((candidate) => candidate.provider === "claude").length > 1}
				/>
			)}
		</div>
	);
}

type ShownWindow = (typeof WINDOWS)[number] & { window: AgentAccountLimitWindow };

function UsageReadout({ account, windows, refreshFailed, namesAccount }: {
	account: AgentAccount;
	windows: ShownWindow[];
	refreshFailed: boolean;
	namesAccount: boolean;
}) {
	const { t, i18n } = useTranslation();
	const now = useNow(30_000);
	const percentFormat = new Intl.NumberFormat(i18n.language, { style: "percent", maximumFractionDigits: 0 });
	const fetchedAt = account.limits?.fetchedAt;
	const readings = windows.map(({ id, short, window }) => {
		const remaining = Math.round(remainingPercent(window.usedPercent));
		const pace = fetchedAt ? limitWindowPace(window, fetchedAt) : null;
		const leftAtReset = pace ? Math.max(0, 100 - pace.atReset) : null;
		const runsOutIn = pace?.runsOutAt ? formatCountdown(pace.runsOutAt.toISOString(), now) : null;
		const countdown = formatCountdown(window.resetsAt, now);
		return {
			id,
			short: t(short),
			label: window.label,
			remaining,
			percent: percentFormat.format(remaining / 100),
			tone: remainingTone(remaining),
			leftAtReset,
			runsOut: Boolean(pace?.runsOutAt),
			resets: countdown ? t("usage.limits.resetsIn", { value: countdown }) : null,
			pace: pace
				? pace.runsOutAt
					? runsOutIn ? t("usage.limits.paceRunsOut", { value: runsOutIn }) : t("usage.limits.paceRanOut")
					: t("usage.limits.paceLeft", { value: percentFormat.format((leftAtReset ?? 0) / 100) })
				: null,
		};
	});
	// The same wording as the Limits tab's account line when a reading is old.
	const issue = account.problem || (account.signedIn ? null : t("usage.limits.signedOut")) || (refreshFailed ? t("usage.limits.refreshFailed") : null);
	const staleFrom = issue && fetchedAt ? t("usage.limits.staleFrom", { value: formatRelativeTime(fetchedAt, now, i18n.language) }) : null;
	const stale = issue ? (staleFrom ? `${issue} · ${staleFrom}` : issue) : null;

	return (
		<TooltipProvider>
			<Tooltip>
				<TooltipTrigger asChild>
					<div role="group" tabIndex={0} aria-label={t("usage.composer.label")} className={TRIGGER_CLASS}>
						{stale ? <TriangleAlert aria-hidden="true" className="size-3 shrink-0 text-warning" /> : null}
						{readings.map((reading) => (
							<span
								key={reading.id}
								role="progressbar"
								aria-label={t("usage.limits.barLabel", { window: reading.label, value: reading.percent })}
								aria-valuemin={0}
								aria-valuemax={100}
								aria-valuenow={reading.remaining}
								className="inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap"
							>
								<span>{reading.short}</span>
								<span aria-hidden="true" className={BAR_CLASS}>
									<span
										className={cn("block h-full rounded-full", reading.tone)}
										style={{ width: `${reading.remaining}%`, backgroundColor: reading.tone ? undefined : agentTone(CLAUDE_HARNESS) }}
									/>
									{/* At 0% the marker would sit on the bar's rounded end; the tooltip's pace line says it runs out. */}
									{reading.leftAtReset !== null && reading.leftAtReset > 0 && reading.leftAtReset < reading.remaining ? (
										<span className="absolute inset-y-0 w-px bg-foreground/70" style={{ insetInlineStart: `${reading.leftAtReset}%` }} />
									) : null}
								</span>
								<span className="tabular-nums">{reading.percent}</span>
							</span>
						))}
					</div>
				</TooltipTrigger>
				<TooltipContent side="top" className={cn(TOOLTIP_SURFACE, "max-w-80")}>
					{namesAccount ? <p className="mb-1.5 truncate text-muted-foreground">{agentAccountName(account, t)}</p> : null}
					<div className="grid grid-cols-[auto_auto_auto] items-baseline gap-x-3 gap-y-1">
						{readings.map((reading) => (
							<Fragment key={reading.id}>
								<span className="font-medium">{reading.label}</span>
								<span className="whitespace-nowrap text-end tabular-nums">
									{reading.percent}{" "}
									<span className="text-muted-foreground">{t("usage.limits.left")}</span>
								</span>
								<span className="whitespace-nowrap text-end tabular-nums text-muted-foreground">{reading.resets ?? ""}</span>
								{reading.pace ? (
									<p className={cn("col-span-2 col-start-2 inline-flex items-center gap-1 tabular-nums", reading.runsOut ? "text-warning" : "text-muted-foreground")}>
										{reading.runsOut ? <TriangleAlert aria-hidden="true" className="size-3 shrink-0" /> : null}
										{reading.pace}
									</p>
								) : null}
							</Fragment>
						))}
					</div>
					{stale ? (
						<p className="mt-1.5 inline-flex items-center gap-1 text-muted-foreground">
							<TriangleAlert aria-hidden="true" className="size-3 shrink-0 text-warning" />
							{stale}
						</p>
					) : null}
				</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}

/** Holds the readout's place while the first reading loads, so the numbers arrive without moving anything. */
function UsagePlaceholder() {
	const { t } = useTranslation();
	return (
		<TooltipProvider>
			<Tooltip>
				<TooltipTrigger asChild>
					<div role="group" tabIndex={0} aria-busy="true" aria-label={t("usage.composer.loading")} className={cn(TRIGGER_CLASS, "text-passive")}>
						{WINDOWS.map((entry) => (
							<span key={entry.id} aria-hidden="true" className="inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap">
								<span>{t(entry.short)}</span>
								<span className={cn(BAR_CLASS, "animate-pulse")} />
								<span className="tabular-nums">–</span>
							</span>
						))}
					</div>
				</TooltipTrigger>
				<TooltipContent side="top" className={TOOLTIP_SURFACE}>{t("usage.composer.loading")}</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}
