import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { agentAccountProviderNames, type AgentAccountProvider } from "../hooks/useAgentAccounts";
import { useHostUsageLimitPrompts, useUsageLimitPromptActions, useUsageLimitPrompts, type HostUsageLimitPrompt, type UsageLimitPrompt, type UsageLimitPromptOption } from "../hooks/useUsageLimitPrompts";
import { Button } from "./ui/button";
import { CloseButton } from "./ui/close-button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogOverlay,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { Pressable } from "./ui/pressable";

const MAX_LISTED_SESSIONS = 5;

function formatReset(value: string | null | undefined, locale: string): string | null {
	if (!value) return null;
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return null;
	const now = new Date();
	const sameDay = date.getFullYear() === now.getFullYear() && date.getMonth() === now.getMonth() && date.getDate() === now.getDate();
	return new Intl.DateTimeFormat(locale, sameDay ? { hour: "2-digit", minute: "2-digit" } : { weekday: "short", hour: "2-digit", minute: "2-digit" }).format(date);
}

function optionLabel(option: UsageLimitPromptOption, fallback: string): string {
	return option.email || fallback;
}

/** The question AO asks when sessions stop on an account's usage limit: switch
 * the stopped sessions (orchestrator and workers) to another signed-in account,
 * which continues each interrupted turn, or wait for the reset. One question per
 * limit event. Closing it only hides it here; it stays open until answered. */
export function UsageLimitPromptDialog() {
	const prompts = useUsageLimitPrompts();
	const hostPrompts = useHostUsageLimitPrompts();
	const [dismissed, setDismissed] = useState<ReadonlySet<string>>(new Set());
	// This computer's questions first, then each linked computer's. One dialog at a
	// time, so one alert per question however many computers raise one. A question
	// answered on either PC drops out of its computer's list and so out of this one.
	const queue: HostUsageLimitPrompt[] = [
		...(prompts.data ?? []).map((prompt) => ({ key: prompt.id, prompt })),
		...hostPrompts,
	];
	const open = queue.find((candidate) => !dismissed.has(candidate.key));
	if (!open) return null;
	return <PromptDialog
		key={open.key}
		prompt={open.prompt}
		hostId={open.hostId}
		hostLabel={open.hostLabel}
		onLater={() => setDismissed((previous) => new Set(previous).add(open.key))}
	/>;
}

function PromptDialog({ prompt, hostId, hostLabel, onLater }: { prompt: UsageLimitPrompt; hostId?: string; hostLabel?: string; onLater: () => void }) {
	const { t, i18n } = useTranslation();
	const { switchTo, wait } = useUsageLimitPromptActions(hostId);
	const firstUsable = useMemo(() => prompt.options.find((option) => !option.full)?.accountId ?? "", [prompt.options]);
	const [chosen, setChosen] = useState(firstUsable);
	const confirmRef = useRef<HTMLButtonElement>(null);
	useEffect(() => {
		// The options refresh while the question is open: keep the choice valid.
		if (!prompt.options.some((option) => option.accountId === chosen && !option.full)) setChosen(firstUsable);
	}, [prompt.options, chosen, firstUsable]);

	const provider = agentAccountProviderNames[prompt.provider as AgentAccountProvider] ?? prompt.provider;
	const unknownAccount = t("settings.agentAccounts.oauthToken");
	const limited = prompt.accountEmail || unknownAccount;
	const reset = formatReset(prompt.resetsAt, i18n.language);
	const busy = switchTo.isPending || wait.isPending;
	const chosenOption = prompt.options.find((option) => option.accountId === chosen);
	const error = (switchTo.error ?? wait.error)?.message ?? null;
	const listed = prompt.sessions.slice(0, MAX_LISTED_SESSIONS);
	const percent = (value: number | null | undefined) => (value == null ? "–" : `${Math.round(value)}%`);
	const title = prompt.windowLabel ? t("usageLimitPrompt.titleWindow", { provider, window: prompt.windowLabel }) : t("usageLimitPrompt.title", { provider });

	return (
		<Dialog open onOpenChange={(open) => { if (!open && !busy) onLater(); }}>
			<DialogContent
				showCloseButton={false}
				className={cn(settingsDialogContentClass, "w-[min(480px,calc(100vw-24px))]")}
				data-testid="usage-limit-prompt"
				onOpenAutoFocus={(event) => { event.preventDefault(); confirmRef.current?.focus(); }}
				overlay={<DialogOverlay />}
			>
				<DialogClose asChild>
					<CloseButton floating disabled={busy} tabIndex={-1} aria-label={t("usageLimitPrompt.later")} title={t("usageLimitPrompt.later")} />
				</DialogClose>
				<div className={cn(settingsDialogHeaderClass, "border-b-0 p-5 pr-12")}>
					<DialogTitle className="settings-dialog-title text-base">
						{hostLabel ? `${hostLabel}: ${title}` : title}
					</DialogTitle>
					<DialogDescription asChild>
						<div className="text-control leading-5 text-settings-muted">
							{reset ? t("usageLimitPrompt.descriptionReset", { account: limited, time: reset }) : t("usageLimitPrompt.descriptionNoReset", { account: limited })}
						</div>
					</DialogDescription>
				</div>

				<div className={cn(settingsDialogBodyClass, "px-5 pt-0 pb-3")}>
					<p className="text-caption text-muted-foreground">{t("usageLimitPrompt.sessions", { count: prompt.sessions.length })}</p>
					<ul className="mt-1 text-control leading-5" data-testid="usage-limit-prompt-sessions">
						{listed.map((session) => <li key={`${session.sessionId}:${session.reviewId ?? ""}`} className="truncate">
							{session.displayName || session.sessionId}
							{session.kind === "orchestrator" ? <span className="text-muted-foreground"> · {t("usageLimitPrompt.orchestrator")}</span> : null}
						</li>)}
						{prompt.sessions.length > listed.length
							? <li className="text-muted-foreground">{t("usageLimitPrompt.moreSessions", { count: prompt.sessions.length - listed.length })}</li>
							: null}
					</ul>

					<p className="mt-3 text-caption text-muted-foreground" id="usage-limit-prompt-accounts">{t("usageLimitPrompt.chooseAccount")}</p>
					<div role="radiogroup" aria-labelledby="usage-limit-prompt-accounts" className="mt-1 flex flex-col gap-1">
						{prompt.options.map((option) => {
							const selected = option.accountId === chosen;
							return <Pressable
								key={option.accountId}
								role="radio"
								aria-checked={selected}
								disabled={option.full || busy}
								onClick={() => setChosen(option.accountId)}
								className={cn(
									"flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-control",
									selected ? "border-(--color-accent) bg-(--color-interactive-active)" : "border-(--color-border) hover:bg-(--color-interactive-active)",
									option.full && "opacity-60",
								)}
							>
								<span className="min-w-0 truncate">{optionLabel(option, unknownAccount)}</span>
								<span className="shrink-0 tabular-nums text-muted-foreground">
									{option.full
										? t("usageLimitPrompt.optionFull")
										: t("usageLimitPrompt.optionUsage", { fiveHour: percent(option.fiveHourUsedPercent), weekly: percent(option.weeklyUsedPercent) })}
								</span>
							</Pressable>;
						})}
					</div>
					{error ? <p role="alert" className="mt-2 text-caption leading-4 text-error">{error}</p> : null}
				</div>

				<div className={cn(settingsDialogFooterClass, "gap-2 border-t-0 p-4 pt-1")}>
					<Button type="button" variant="secondary" disabled={busy} onClick={() => wait.mutate(prompt.id)}>
						{t("usageLimitPrompt.wait")}
					</Button>
					<Button
						ref={confirmRef}
						type="button"
						variant="primary"
						disabled={busy || !chosenOption}
						onClick={() => chosenOption && switchTo.mutate({ promptId: prompt.id, accountId: chosenOption.accountId })}
					>
						{chosenOption ? t("usageLimitPrompt.switchTo", { account: optionLabel(chosenOption, unknownAccount) }) : t("usageLimitPrompt.switch")}
					</Button>
				</div>
			</DialogContent>
		</Dialog>
	);
}
