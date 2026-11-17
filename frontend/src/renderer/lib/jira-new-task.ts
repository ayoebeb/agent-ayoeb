import { useUiStore } from "../stores/ui-store";
import { writeChatComposerText } from "./chat-drafts";
import { ticketTaskBrief, type JiraTicket } from "./jira";

/**
 * Opens the existing New Task flow with the ticket already in the composer, so
 * the person picks the agent and model and sends it as a worker of its own.
 * The New Task page keeps one draft per host, under this id.
 */
export function startWorkerFromTicket(ticket: JiraTicket, projectId: string, hostId?: string): void {
	const draftId = `new-chat:${hostId ?? "local"}`;
	writeChatComposerText({ sessionId: draftId, incarnation: draftId }, ticketTaskBrief(ticket));
	useUiStore.getState().requestNewTask(projectId, hostId);
}
