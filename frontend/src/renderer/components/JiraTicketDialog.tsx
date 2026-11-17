import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { ExternalLink } from "@/components/app-icons";
import { useRestoreFocusOnClose } from "../hooks/useRestoreFocusOnClose";
import { openLinkInSystemBrowser } from "../lib/external-link-policy";
import { latestSentSessionId, type JiraTicket } from "../lib/jira";
import { cn } from "../lib/utils";
import type { WorkspaceSession } from "../types/workspace";
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

/**
 * A ticket in full: description, acceptance criteria, who is on it, and the
 * same one-tap "Start with orchestrator" the card has. Open while `ticket` is set.
 */
export function JiraTicketDialog({ ticket, sessions, starting, onOpenChange, onStart, onStartWorker, onOpenSession, onOpenOrchestrator }: {
	ticket?: JiraTicket;
	/** Worker sessions already working on the ticket. */
	sessions: WorkspaceSession[];
	starting: boolean;
	onOpenChange: (open: boolean) => void;
	onStart: (ticket: JiraTicket) => void;
	onStartWorker: (ticket: JiraTicket) => void;
	onOpenSession: (session: WorkspaceSession) => void;
	onOpenOrchestrator: (sessionId: string) => void;
}) {
	const { t } = useTranslation();
	const restoreFocusOnClose = useRestoreFocusOnClose(Boolean(ticket));
	const startRef = useRef<HTMLButtonElement>(null);
	const sentTo = ticket ? latestSentSessionId(ticket) : undefined;
	const meta = ticket ? [ticket.status.name, ticket.issueType, ticket.priority].filter(Boolean).join(" · ") : "";

	return (
		<Dialog open={Boolean(ticket)} onOpenChange={onOpenChange}>
			<DialogContent
				showCloseButton={false}
				className={cn(settingsDialogContentClass, "w-[min(640px,calc(100vw-24px))]")}
				onClick={(event) => event.stopPropagation()}
				onOpenAutoFocus={(event) => {
					// The primary action is the one thing the person opened this for.
					if (!startRef.current) return;
					event.preventDefault();
					startRef.current.focus();
				}}
				onCloseAutoFocus={restoreFocusOnClose}
				overlay={<DialogOverlay onClick={(event) => event.stopPropagation()} />}
			>
				<DialogClose asChild>
					<CloseButton floating aria-label={t("jira.detail.close")} />
				</DialogClose>
				{ticket ? (
					<>
						<div className={cn(settingsDialogHeaderClass, "border-b-0 p-5 pr-12")}>
							<span className="font-mono text-2xs text-muted-foreground">{ticket.key}</span>
							<DialogTitle className="settings-dialog-title text-base leading-snug">{ticket.summary}</DialogTitle>
							<DialogDescription className="text-control text-settings-muted">{meta}</DialogDescription>
						</div>
						<div className={cn(settingsDialogBodyClass, "px-5 pb-4 pt-0")} data-testid="jira-ticket-body">
							<dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-control">
								<dt className="text-settings-muted">{t("jira.detail.assignee")}</dt>
								<dd>{ticket.assignee?.displayName ?? t("jira.unassigned")}</dd>
								{ticket.labels && ticket.labels.length > 0 ? (
									<>
										<dt className="text-settings-muted">{t("jira.detail.labels")}</dt>
										<dd className="min-w-0 break-words">{ticket.labels.join(", ")}</dd>
									</>
								) : null}
							</dl>
							<TicketText title={t("jira.detail.description")} text={ticket.description} empty={t("jira.detail.empty")} />
							{ticket.acceptanceCriteria ? <TicketText title={t("jira.detail.acceptance")} text={ticket.acceptanceCriteria} /> : null}
							{sentTo || sessions.length > 0 ? (
								<section className="flex flex-col gap-1.5">
									<h3 className="text-xs font-medium text-foreground">{t("jira.detail.sessions")}</h3>
									<div className="flex flex-col items-start gap-1">
										{sentTo ? (
											<Button type="button" variant="ghost" size="xs" onClick={() => onOpenOrchestrator(sentTo)}>
												{t("jira.sentToOrchestrator")}
											</Button>
										) : null}
										{sessions.map((session) => (
											<Button key={session.id} type="button" variant="ghost" size="xs" onClick={() => onOpenSession(session)}>{session.title}</Button>
										))}
									</div>
								</section>
							) : null}
						</div>
						<div className={cn(settingsDialogFooterClass, "gap-2 border-t-0 p-4 pt-1")}>
							<Button type="button" variant="quiet" className="mr-auto" onClick={() => void openLinkInSystemBrowser(ticket.url)}>
								<ExternalLink className="size-icon-md" aria-hidden="true" />
								{t("jira.openInJira")}
							</Button>
							<Button type="button" variant="secondary" onClick={() => onStartWorker(ticket)}>{t("jira.startWorker")}</Button>
							<Button ref={startRef} type="button" variant="primary" disabled={starting} aria-busy={starting} onClick={() => onStart(ticket)}>
								{starting ? t("jira.starting") : t("jira.start")}
							</Button>
						</div>
					</>
				) : null}
			</DialogContent>
		</Dialog>
	);
}

function TicketText({ title, text, empty }: { title: string; text?: string; empty?: string }) {
	const body = text?.trim();
	return (
		<section className="flex flex-col gap-1.5">
			<h3 className="text-xs font-medium text-foreground">{title}</h3>
			{body ? (
				<p className="whitespace-pre-wrap break-words text-control leading-5 text-settings-label">{body}</p>
			) : (
				<p className="text-control text-settings-muted">{empty}</p>
			)}
		</section>
	);
}
