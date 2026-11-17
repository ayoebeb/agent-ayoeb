import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useOrchestratorWaiting } from "../hooks/useOrchestratorWaiting";
import { cn } from "../lib/utils";
import { NOTIFICATION_TYPE_ORCHESTRATOR_WAITING, localizedNotificationCopy } from "../lib/orchestrator-waiting";
import { sessionNavigateTarget } from "../lib/navigate-to-session";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "./NavRowHighlight";
import { Pressable } from "./ui/pressable";

/**
 * The one line Home shows when something is waiting for the user, and nothing
 * otherwise. One waiting orchestrator is named and a click opens its chat; more
 * than one is counted and a click opens the Projects page.
 */
export function HomeWaitingLine() {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const { waiting } = useOrchestratorWaiting();
	if (waiting.length === 0) return null;
	const only = waiting.length === 1 ? waiting[0] : undefined;
	// The daemon's own title, in the user's language: "<project>: orchestrator is waiting for you".
	const text = only
		? localizedNotificationCopy({ type: NOTIFICATION_TYPE_ORCHESTRATOR_WAITING, title: only.title, body: only.question }).title
		: t("home.orchestratorsWaiting", { count: waiting.length });
	return (
		<Pressable
			className={cn("flex w-full items-center gap-3 rounded-lg px-4 py-1 text-foreground", NAV_ROW_HIGHLIGHT_HOST_CLASS)}
			data-testid="home-waiting-line"
			onClick={() => void navigate(only ? sessionNavigateTarget(only.projectId, only.sessionId, only.hostId) : { to: "/projects" })}
		>
			<NavRowHighlight className="bg-card" />
			<span aria-hidden="true" className="relative z-[1] grid size-8 shrink-0 place-items-center">
				<span className="size-2 rounded-full bg-status-needs-you" />
			</span>
			<span className="relative z-[1] min-w-0 truncate text-sm leading-5">{text}</span>
		</Pressable>
	);
}
