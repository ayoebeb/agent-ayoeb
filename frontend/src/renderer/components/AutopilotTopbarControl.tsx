import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Square, Zap } from "@/components/app-icons";
import { cn } from "@/lib/utils";
import { useAutopilotActions, useAutopilotView } from "../hooks/useAutopilot";
import { autopilotTone, hasUnseenReport, isOpenAutopilotRun } from "../lib/autopilot";
import { LOCAL_HOST, type HostId } from "../lib/hosts";
import { AutopilotDialog } from "./AutopilotDialog";
import { noDragStyle } from "./PageTopbar";
import { TopbarActionError } from "./TopbarActionError";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

const TONE_TEXT = {
	working: "text-status-working",
	attention: "text-status-needs-you",
	paused: "text-status-in-review",
	done: "text-status-ready",
	stopped: "text-muted-foreground",
} as const;

/**
 * The session's Autopilot control in the top bar. With no run it is a quiet
 * "Autopilot" button that opens the start form. While a run is open it is the
 * state badge (on, paused, or parked and needing the user) beside a Stop button
 * that is always visible: stopping must never take a menu or a dialog.
 * A run that ended while the user was elsewhere leaves a small dot until they
 * open its report.
 */
export function AutopilotTopbarControl({
	hostId,
	sessionId,
	sessionTerminated = false,
}: {
	hostId?: HostId;
	sessionId: string;
	sessionTerminated?: boolean;
}) {
	const { t } = useTranslation();
	const host = hostId ?? LOCAL_HOST;
	const { data: view } = useAutopilotView(host, sessionId);
	const actions = useAutopilotActions(host, sessionId);
	const [open, setOpen] = useState(false);
	const run = view?.run;
	const isOpen = isOpenAutopilotRun(run);
	const reportReady = hasUnseenReport(host, view);
	const tone = run && (isOpen || reportReady) ? autopilotTone(run.state) : undefined;

	let label = t("autopilot.button");
	if (run && isOpen) {
		label = run.state === "running"
			? t("autopilot.badge.running", { current: run.iteration + 1, max: run.maxIterations })
			: run.state === "parked" ? t("autopilot.badge.parked") : t("autopilot.badge.paused");
	} else if (reportReady) {
		label = t("autopilot.button.reportReady");
	}
	const tooltip = isOpen ? t("autopilot.badge.openLog") : t("autopilot.button.tooltip");
	// A session that ended has nothing to start a run on, but its last report stays readable.
	if (sessionTerminated && !run) return null;

	return (
		<span className="inline-flex items-center" style={noDragStyle} data-testid="autopilot-control">
			<Tooltip>
				<TooltipTrigger asChild>
					<Button
						type="button"
						size="sm"
						variant="quiet"
						aria-label={label}
						data-autopilot-state={run && isOpen ? run.state : undefined}
						data-priority="secondary"
						onClick={() => setOpen(true)}
					>
						<span className="relative inline-flex">
							<Zap aria-hidden="true" className={cn("size-icon-md", tone ? TONE_TEXT[tone] : undefined)} />
							{reportReady && !isOpen ? (
								<span
									aria-hidden="true"
									className="pointer-events-none absolute -right-1 -top-1 size-2 rounded-full bg-accent ring-2 ring-background"
								/>
							) : null}
						</span>
						<span data-compact-label className="tabular-nums">{label}</span>
					</Button>
				</TooltipTrigger>
				<TooltipContent side="bottom">{tooltip}</TooltipContent>
			</Tooltip>
			{isOpen ? (
				<Tooltip>
					<TooltipTrigger asChild>
						<Button
							type="button"
							size="sm"
							variant="destructive-ghost"
							aria-label={t("autopilot.stop.aria")}
							data-autopilot-stop=""
							disabled={actions.stop.isPending}
							onClick={() => actions.stop.mutate()}
						>
							<Square aria-hidden="true" className="size-icon-sm" />
							<span data-compact-label>{t("autopilot.stop.label")}</span>
						</Button>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("autopilot.stop.tooltip")}</TooltipContent>
				</Tooltip>
			) : null}
			{actions.stop.error ? (
				<TopbarActionError className="max-w-48 truncate" title={actions.stop.error.message}>
					{t("autopilot.error.generic", { message: actions.stop.error.message })}
				</TopbarActionError>
			) : null}
			{open ? (
				<AutopilotDialog
					open={open}
					onOpenChange={setOpen}
					hostId={host}
					sessionId={sessionId}
					sessionTerminated={sessionTerminated}
				/>
			) : null}
		</span>
	);
}
