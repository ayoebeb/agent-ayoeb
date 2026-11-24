import type { TFunction } from "i18next";
import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { useAutopilotActions, useAutopilotView } from "../hooks/useAutopilot";
import { useRestoreFocusOnClose } from "../hooks/useRestoreFocusOnClose";
import { useSettings } from "../hooks/useSettings";
import {
	autopilotTone,
	isOpenAutopilotRun,
	markReportSeen,
	type AutopilotEvent,
	type AutopilotRun,
	type AutopilotView,
} from "../lib/autopilot";
import type { HostId } from "../lib/hosts";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** The status dot's colour: the same status tokens the sidebar and board use. */
const TONE_DOT: Record<ReturnType<typeof autopilotTone>, string> = {
	working: "bg-status-working",
	attention: "bg-status-needs-you",
	paused: "bg-status-in-review",
	done: "bg-status-ready",
	stopped: "bg-muted-foreground",
};

/**
 * Autopilot for one session: starts a run, and shows where a run stands, what it
 * decided for the user, what it parked, and its log. Closing it never stops a
 * run; the Stop button does, and says so.
 */
export function AutopilotDialog({
	open,
	onOpenChange,
	hostId,
	sessionId,
	sessionTerminated = false,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	hostId: HostId;
	sessionId: string;
	sessionTerminated?: boolean;
}) {
	const { t } = useTranslation();
	const restoreFocusOnClose = useRestoreFocusOnClose(open);
	const { data: view } = useAutopilotView(hostId, sessionId, open);
	const actions = useAutopilotActions(hostId, sessionId);
	const run = view?.run;
	const isOpenRun = isOpenAutopilotRun(run);

	// Opening the report is the user's acknowledgement of it.
	useEffect(() => {
		if (open) markReportSeen(hostId, view);
	}, [open, hostId, view]);

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent
				showCloseButton={false}
				className={cn(settingsDialogContentClass, "w-[min(560px,calc(100vw-24px))]")}
				onCloseAutoFocus={restoreFocusOnClose}
			>
				<div className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title text-base">{t("autopilot.dialog.title")}</DialogTitle>
					<DialogDescription className="text-control leading-5 text-settings-muted">
						{isOpenRun ? t("autopilot.dialog.descriptionOpen") : t("autopilot.start.description")}
					</DialogDescription>
				</div>

				<div className={settingsDialogBodyClass} data-testid="autopilot-dialog-body">
					{view?.warnings.map((warning) => (
						<p key={warning} role="note" className="text-xs leading-5 text-status-in-review">{warning}</p>
					))}
					{run ? <RunSummary run={run} heading={isOpenRun ? undefined : t("autopilot.panel.lastRun")} /> : null}
					{view && run ? (
						<RunDetails
							view={view}
							onHandled={(eventId) => actions.resolveItem.mutate(eventId)}
							handling={actions.resolveItem.isPending}
							onUndo={(eventId) => actions.undoDecision.mutate(eventId)}
							undoing={actions.undoDecision.isPending}
						/>
					) : null}
					{!isOpenRun ? (
						<StartForm
							disabled={sessionTerminated}
							starting={actions.start.isPending}
							onStart={(input) =>
								actions.start.mutate(input, { onSuccess: () => onOpenChange(false) })}
						/>
					) : null}
					{actions.error ? (
						<p role="alert" className="text-xs leading-5 text-error">{t("autopilot.error.generic", { message: actions.error })}</p>
					) : null}
				</div>

				<div className={cn(settingsDialogFooterClass, "gap-1.5")}>
					{isOpenRun ? (
						<>
							<Button
								type="button"
								variant="destructive-ghost"
								disabled={actions.stop.isPending}
								onClick={() => actions.stop.mutate()}
							>
								{t("autopilot.stop.label")}
							</Button>
							{run?.state === "parked" ? (
								<Button
									type="button"
									variant="primary"
									disabled={actions.resume.isPending}
									onClick={() => actions.resume.mutate()}
								>
									{t("autopilot.panel.resume")}
								</Button>
							) : null}
						</>
					) : null}
					<DialogClose asChild>
						<Button type="button" variant="ghost">{t("autopilot.dialog.close")}</Button>
					</DialogClose>
				</div>
			</DialogContent>
		</Dialog>
	);
}

function StartForm({
	disabled,
	starting,
	onStart,
}: {
	disabled: boolean;
	starting: boolean;
	onStart: (input: { task: string; maxIterations?: number }) => void;
}) {
	const { t } = useTranslation();
	const { settings } = useSettings();
	const defaultMax = settings?.autopilotMaxIterations ?? 30;
	const [task, setTask] = useState("");
	const [max, setMax] = useState("");
	const parsedMax = max.trim() === "" ? undefined : Number(max);
	const maxInvalid = parsedMax !== undefined && (!Number.isInteger(parsedMax) || parsedMax < 1 || parsedMax > 500);
	const canStart = task.trim() !== "" && !maxInvalid && !disabled && !starting;

	return (
		<form
			className="flex flex-col gap-3"
			onSubmit={(event) => {
				event.preventDefault();
				if (canStart) onStart({ task: task.trim(), maxIterations: parsedMax });
			}}
		>
			<div className="flex flex-col gap-1.5">
				<Label htmlFor="autopilot-task">{t("autopilot.start.task.label")}</Label>
				<textarea
					id="autopilot-task"
					value={task}
					maxLength={8000}
					rows={4}
					placeholder={t("autopilot.start.task.placeholder")}
					onChange={(event) => setTask(event.target.value)}
					className="min-h-24 w-full resize-y rounded-md border border-transparent bg-input/50 px-3 py-2 text-[13px] leading-5 outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30"
				/>
			</div>
			<div className="flex flex-col gap-1.5">
				<Label htmlFor="autopilot-max">{t("autopilot.start.max.label")}</Label>
				<Input
					id="autopilot-max"
					type="number"
					min={1}
					max={500}
					inputMode="numeric"
					value={max}
					placeholder={String(defaultMax)}
					aria-invalid={maxInvalid || undefined}
					onChange={(event) => setMax(event.target.value)}
					className="w-28 tabular-nums"
				/>
				<p className="text-xs leading-5 text-settings-muted">{t("autopilot.start.max.hint", { default: defaultMax })}</p>
			</div>
			<p className="text-xs leading-5 text-settings-muted">{t("autopilot.start.limits")}</p>
			<div className="flex justify-end">
				<Button type="submit" variant="primary" disabled={!canStart}>
					{starting ? t("autopilot.start.starting") : t("autopilot.start.submit")}
				</Button>
			</div>
		</form>
	);
}

type TFn = TFunction;

// Literal keys, not a template string: the typed catalog cannot resolve a built key.
function reasonText(reason: AutopilotRun["endReason"], t: TFn): string {
	switch (reason) {
		case "max_iterations": return t("autopilot.reason.max_iterations");
		case "stuck": return t("autopilot.reason.stuck");
		case "needs_input": return t("autopilot.reason.needs_input");
		case "emergency_stop": return t("autopilot.reason.emergency_stop");
		case "user": return t("autopilot.reason.user");
		case "session_ended": return t("autopilot.reason.session_ended");
		case "goal_done": return t("autopilot.reason.goal_done");
		default: return "";
	}
}

function eventKindText(kind: AutopilotEvent["kind"], t: TFn): string {
	switch (kind) {
		case "started": return t("autopilot.event.started");
		case "iteration": return t("autopilot.event.iteration");
		case "decision": return t("autopilot.event.decision");
		case "parked": return t("autopilot.event.parked");
		case "gate_failed": return t("autopilot.event.gate_failed");
		case "done": return t("autopilot.event.done");
		case "stopped": return t("autopilot.event.stopped");
		case "run_parked": return t("autopilot.event.run_parked");
		case "paused": return t("autopilot.event.paused");
		case "resumed": return t("autopilot.event.resumed");
		case "permission_denied": return t("autopilot.event.permission_denied");
		case "wait_diverted": return t("autopilot.event.wait_diverted");
	}
}

function stateText(run: AutopilotRun, t: TFn): string {
	const reason = reasonText(run.endReason, t);
	switch (run.state) {
		case "running":
			return t("autopilot.state.running", { current: run.iteration + 1, max: run.maxIterations });
		case "paused_usage_limit":
			return t("autopilot.state.paused");
		case "parked":
			return `${t("autopilot.state.parked")} ${reason}`.trim();
		case "done":
			return t("autopilot.state.done");
		default:
			return `${t("autopilot.state.stopped")} ${reason}`.trim();
	}
}

function RunSummary({ run, heading }: { run: AutopilotRun; heading?: string }) {
	const { t } = useTranslation();
	return (
		<section className="flex flex-col gap-1.5" aria-label={heading ?? t("autopilot.dialog.title")}>
			{heading ? <h3 className="text-caption font-medium text-settings-muted">{heading}</h3> : null}
			<p className="flex items-start gap-2 text-sm leading-5 text-foreground">
				<span
					aria-hidden="true"
					className={cn("mt-1.5 size-2 shrink-0 rounded-full", TONE_DOT[autopilotTone(run.state)])}
				/>
				<span data-testid="autopilot-state">{stateText(run, t)}</span>
			</p>
			<Field label={t("autopilot.panel.task")}>{run.task}</Field>
			<Field label={t("autopilot.panel.criteria")}>
				{run.criteria.trim() ? run.criteria : <span className="text-settings-muted">{t("autopilot.panel.criteriaPending")}</span>}
			</Field>
			{run.lastGap.trim() && isOpenAutopilotRun(run) ? <Field label={t("autopilot.panel.stillOpen")}>{run.lastGap}</Field> : null}
			{run.permissionsAllowed + run.permissionsDenied > 0 ? (
				<p className="text-xs leading-5 text-settings-muted tabular-nums">
					{t("autopilot.panel.permissions", { allowed: run.permissionsAllowed, denied: run.permissionsDenied })}
				</p>
			) : null}
		</section>
	);
}

function Field({ label, children }: { label: string; children: ReactNode }) {
	return (
		<div className="flex flex-col gap-0.5">
			<span className="text-caption font-medium text-settings-muted">{label}</span>
			<div className="whitespace-pre-wrap break-words text-[13px] leading-5 text-foreground">{children}</div>
		</div>
	);
}

function RunDetails({
	view,
	onHandled,
	handling,
	onUndo,
	undoing,
}: {
	view: AutopilotView;
	onHandled: (eventId: string) => void;
	handling: boolean;
	onUndo: (eventId: string) => void;
	undoing: boolean;
}) {
	const { t } = useTranslation();
	const report = view.report;
	const decisions = report?.decisions ?? [];
	const merged = report?.mergedPrs ?? [];
	const gaps = report?.gaps ?? [];
	// The report's parked list includes handled items; the unhandled ones are the
	// user's to act on and lead the panel.
	const parked = report?.parked ?? view.parked;
	const hasReport = merged.length + decisions.length + gaps.length > 0;

	return (
		<>
			{parked.length > 0 ? (
				<section className="flex flex-col gap-1.5" aria-label={t("autopilot.panel.parkedTitle")}>
					<h3 className="text-caption font-medium text-settings-muted">{t("autopilot.panel.parkedTitle")}</h3>
					<ul className="flex flex-col gap-2">
						{parked.map((item) => (
							<li key={item.id} className="flex items-start justify-between gap-3" data-testid="autopilot-parked-item">
								<div className="min-w-0">
									<p className="break-words text-[13px] leading-5 text-foreground">{item.summary}</p>
									<p className="break-words text-xs leading-5 text-settings-muted">{item.detail}</p>
								</div>
								{item.resolvedAt ? (
									<span className="shrink-0 text-xs leading-5 text-settings-muted">{t("autopilot.panel.handled")}</span>
								) : (
									<Button type="button" size="xs" variant="secondary" disabled={handling} onClick={() => onHandled(item.id)}>
										{t("autopilot.panel.markHandled")}
									</Button>
								)}
							</li>
						))}
					</ul>
				</section>
			) : null}

			{hasReport ? (
				<section className="flex flex-col gap-2" aria-label={t("autopilot.panel.reportTitle")}>
					<h3 className="text-caption font-medium text-settings-muted">{t("autopilot.panel.reportTitle")}</h3>
					{merged.length > 0 ? (
						<Field label={t("autopilot.panel.merged")}>
							<ul className="flex flex-col gap-0.5">
								{merged.map((pr) => <li key={pr.url}>{pr.title || pr.url}</li>)}
							</ul>
						</Field>
					) : null}
					{decisions.length > 0 ? (
						<Field label={t("autopilot.panel.decisions")}>
							<ul className="flex flex-col gap-1.5" data-testid="autopilot-decisions">
								{decisions.map((decision) => (
									<li key={decision.id} className="flex items-start justify-between gap-3" data-testid="autopilot-decision">
										<div className="min-w-0">
											<span>{decision.summary}</span>
											{decision.detail ? <span className="block text-xs text-settings-muted">{decision.detail}</span> : null}
											{decision.basedOn?.length ? (
												<span className="block text-xs text-settings-muted" data-testid="autopilot-decision-basis">
													{t("autopilot.panel.basedOn")} {decision.basedOn.join(" · ")}
												</span>
											) : null}
											{decision.confidence === "low" ? (
												<span className="block text-xs text-status-in-review" data-testid="autopilot-decision-low-confidence">
													{t("autopilot.panel.lowConfidence")}
												</span>
											) : null}
										</div>
										{decision.resolvedAt ? (
											<span className="shrink-0 text-xs leading-5 text-settings-muted">{t("autopilot.panel.undone")}</span>
										) : (
											<Button type="button" size="xs" variant="secondary" disabled={undoing} onClick={() => onUndo(decision.id)}>
												{t("autopilot.panel.undo")}
											</Button>
										)}
									</li>
								))}
							</ul>
						</Field>
					) : null}
					{gaps.length > 0 ? (
						<Field label={t("autopilot.panel.gaps")}>
							<ul className="flex flex-col gap-0.5">{gaps.map((gap) => <li key={gap}>{gap}</li>)}</ul>
						</Field>
					) : null}
				</section>
			) : null}

			<section className="flex flex-col gap-1.5" aria-label={t("autopilot.panel.logTitle")}>
				<h3 className="text-caption font-medium text-settings-muted">{t("autopilot.panel.logTitle")}</h3>
				<ul className="flex flex-col gap-1" data-testid="autopilot-log">
					{view.events.slice(0, 30).map((event) => <LogLine key={event.id} event={event} />)}
				</ul>
				{view.events.length === 0 ? <p className="text-xs text-settings-muted">{t("autopilot.panel.nothing")}</p> : null}
			</section>
		</>
	);
}

function LogLine({ event }: { event: AutopilotEvent }) {
	const { t } = useTranslation();
	return (
		<li className="flex items-baseline gap-2 text-xs leading-5">
			<time className="shrink-0 tabular-nums text-settings-muted" dateTime={event.createdAt}>
				{new Date(event.createdAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
			</time>
			<span className="shrink-0 font-medium text-foreground">{eventKindText(event.kind, t)}</span>
			<span className="min-w-0 break-words text-settings-muted">{event.summary}</span>
		</li>
	);
}
