import { useNavigate } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { LayoutDashboard } from "@/components/app-icons";
import { useOrchestratorWaiting } from "../hooks/useOrchestratorWaiting";
import { useRemoteWorkspaces, useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { formatTimeCompact } from "../lib/format-time";
import { labelForHost } from "../lib/host-clients";
import { LOCAL_HOST } from "../lib/hosts";
import { projectNavigateTarget, sessionNavigateTarget } from "../lib/navigate-to-session";
import { buildProjectCards, sortProjectCards, type ProjectCard, type ProjectOrchestratorState } from "../lib/project-overview";
import { cn } from "../lib/utils";
import { PageTopbar, PageTopbarTitle } from "./PageTopbar";
import { Button } from "./ui/button";
import { Pressable } from "./ui/pressable";

const STATE_KEYS = {
	waiting: "projects.state.waiting",
	"needs-input": "projects.state.needsInput",
	working: "projects.state.working",
	idle: "projects.state.idle",
	stopped: "projects.state.stopped",
	none: "projects.state.none",
} as const satisfies Record<ProjectOrchestratorState, string>;

// One status dot per card, as everywhere else in the app; the state is always
// also said in words, so color is never the only cue.
const DOT_CLASS: Record<ProjectOrchestratorState, string> = {
	waiting: "bg-status-needs-you",
	"needs-input": "bg-status-needs-you",
	working: "bg-status-working",
	idle: "bg-muted-foreground/50",
	stopped: "bg-status-exited",
	none: "bg-border-strong",
};

const attention = (state: ProjectOrchestratorState) => state === "waiting" || state === "needs-input";

/**
 * One card per project: where it runs, what its orchestrator is doing, how many
 * workers are live, and the state of its open pull requests. A click opens the
 * orchestrator's chat (or the project's board when it has none yet).
 *
 * Room is left between the state line and the facts for the orchestrator's last
 * message, which is a later change; nothing is drawn there until it exists.
 */
function ProjectCardView({ card, machine, onOpen }: { card: ProjectCard; machine: string; onOpen: () => void }) {
	const { t } = useTranslation();
	const state = t(STATE_KEYS[card.state]);
	const stateLine = card.state === "waiting" && card.waitingSince ? `${state} · ${formatTimeCompact(card.waitingSince)}` : state;
	const prDetails = [
		t("projects.prs.open", { n: card.prs.open }),
		card.prs.approved > 0 ? t("projects.prs.approved", { n: card.prs.approved }) : null,
		card.prs.ci === "none" ? null : t(`projects.prs.ci.${card.prs.ci}`),
	].filter((part): part is string => part !== null);
	return (
		<Pressable
			aria-label={t(card.orchestratorId ? "projects.openChatAria" : "projects.openBoardAria", { name: card.name, machine })}
			className="flex w-full min-w-0 flex-col gap-1.5 rounded-lg bg-card px-4 py-3 text-left transition-[background-color] duration-fast ease-out hover:bg-accent"
			data-host-id={card.hostId}
			data-state={card.state}
			data-testid="project-card"
			onClick={onOpen}
		>
			<span className="flex min-w-0 items-center gap-2.5">
				<span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full", DOT_CLASS[card.state])} />
				<span className="min-w-0 flex-1 truncate text-sm font-medium text-foreground">{card.name}</span>
				<span className="max-w-[40%] shrink-0 truncate text-caption text-muted-foreground">{machine}</span>
			</span>
			<span className={cn("pl-[18px] text-xs", attention(card.state) ? "text-status-needs-you" : "text-muted-foreground")}>{stateLine}</span>
			<span className="flex flex-wrap gap-x-3 gap-y-0.5 pl-[18px] text-xs tabular-nums text-muted-foreground">
				<span>{card.activeWorkers > 0 ? t("projects.workers", { n: card.activeWorkers }) : t("projects.noWorkers")}</span>
				<span>{card.prs.open > 0 ? t("projects.prs", { details: prDetails.join(" · ") }) : t("projects.noPrs")}</span>
			</span>
		</Pressable>
	);
}

/**
 * Every project this Ayo knows, on this computer and on each connected linked
 * computer, the ones waiting for the user first. Cloud projects and the
 * scratchpad are not listed.
 */
export function ProjectsView() {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const workspaceQuery = useWorkspaceQuery();
	// Shares the sidebar's cached queries, so this adds no requests.
	const remote = useRemoteWorkspaces();
	const { waiting } = useOrchestratorWaiting();
	const local = workspaceQuery.data;
	const cards = useMemo(
		() => sortProjectCards(buildProjectCards([...(local ?? []).filter((workspace) => !workspace.hostId), ...remote.data], waiting)),
		[local, remote.data, waiting],
	);
	const machineOf = (hostId: string) => (hostId === LOCAL_HOST ? t("projects.thisComputer") : (labelForHost(hostId) ?? t("projects.otherComputer")));

	return (
		<div className="flex min-h-0 flex-1 flex-col bg-background">
			<PageTopbar
				lead={<PageTopbarTitle as="h1" icon={<LayoutDashboard className="size-icon-md" aria-hidden="true" />}>{t("projects.title")}</PageTopbarTitle>}
			/>
			<main className="min-h-0 flex-1 overflow-y-auto">
				<div className="mx-auto w-full max-w-4xl px-6 py-6">
					{workspaceQuery.isError ? (
						<p role="alert" className="py-16 text-center text-sm text-destructive">{t("shell.couldNotLoadProjects")}</p>
					) : cards.length === 0 ? (
						workspaceQuery.isSuccess ? (
							<div className="flex items-center gap-3 px-1 text-sm text-muted-foreground">
								<span>{t("projects.empty")}</span>
								<Button variant="quiet" size="sm" onClick={() => void navigate({ to: "/" })}>
									{t("projects.emptyAction")}
								</Button>
							</div>
						) : null
					) : (
						<ul className="grid gap-3 sm:grid-cols-2" data-testid="project-cards">
							{cards.map((card) => (
								<li key={card.key} className="min-w-0">
									<ProjectCardView
										card={card}
										machine={machineOf(card.hostId)}
										onOpen={() =>
											void navigate(
												card.orchestratorId
													? sessionNavigateTarget(card.projectId, card.orchestratorId, card.hostId)
													: projectNavigateTarget(card.projectId, card.hostId),
											)
										}
									/>
								</li>
							))}
						</ul>
					)}
				</div>
			</main>
		</div>
	);
}
