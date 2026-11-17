/**
 * The orchestrator's crew: every worker of the project, grouped by what the
 * person has to do about it. Pure functions only. The rail, the info row and the
 * tabs all read the model built here, so a number never differs between them.
 *
 * Nothing here stores a status. Groups are read off the daemon's derived facts
 * (status, kanban column, activity), the same facts the board uses.
 */

import type { ConversationPlan, ConversationSnapshot } from "../types/conversation";
import {
	isOrchestratorSession,
	sessionIsActive,
	toKanbanColumn,
	type WorkspaceSession,
} from "../types/workspace";
import { sessionNeedsInput, sessionNeedsYou } from "./session-presentation";

/** How long an active agent may go without anything a person would notice before the rail says so. */
export const QUIET_AFTER_MS = 6 * 60_000;
/** Finished sessions stay in the rail for a day, then belong to the archive. */
export const DONE_WINDOW_MS = 24 * 60 * 60_000;

export type CrewGroupId = "needsYou" | "working" | "inReview" | "done";
export const CREW_GROUP_ORDER: readonly CrewGroupId[] = ["needsYou", "working", "inReview", "done"];

/** One changed file of one worker's worktree, as far as the rail needs it. */
export type CrewFile = { path: string; additions: number; deletions: number };

/** The change a worker's worktree carries against its base. */
export type CrewDiff = { files: CrewFile[]; additions: number; deletions: number };

export type CrewRow = {
	session: WorkspaceSession;
	group: CrewGroupId;
	/** Absent until the worktree answered (or when it has none to read). */
	diff?: CrewDiff;
	/** How long the agent has been silent, only when that is past the threshold. */
	quietMs?: number;
	/** Paths this worker shares with at least one other worker. */
	overlapPaths: string[];
};

export type FileOverlap = {
	path: string;
	/** Two or more workers, ordered so the one to merge first leads. */
	sessionIds: string[];
};

export type CrewModel = {
	rows: CrewRow[];
	groups: { id: CrewGroupId; rows: CrewRow[] }[];
	overlaps: FileOverlap[];
	totals: { additions: number; deletions: number; worktrees: number };
	/** Workers that ask something of the person right now. */
	needsYouCount: number;
};

function time(value?: string): number | undefined {
	if (!value) return undefined;
	const parsed = Date.parse(value);
	return Number.isNaN(parsed) ? undefined : parsed;
}

/** The latest moment the daemon reported for the session: an event, or agent activity. */
export function lastSignalAt(session: Pick<WorkspaceSession, "lastEventAt" | "activity" | "updatedAt">): number | undefined {
	const candidates = [time(session.lastEventAt), time(session.activity?.lastActivityAt)].filter(
		(value): value is number => value !== undefined,
	);
	return candidates.length > 0 ? Math.max(...candidates) : undefined;
}

export function isMerged(session: Pick<WorkspaceSession, "displayStatus" | "status" | "scmStatus">): boolean {
	return session.displayStatus ? session.displayStatus === "Merged" : (session.scmStatus ?? session.status) === "merged";
}

export function crewGroupOf(session: WorkspaceSession): CrewGroupId {
	if (!sessionIsActive(session) || isMerged(session)) return "done";
	// The board's own checks: a question, a failing CI run, requested changes.
	if (sessionNeedsInput(session) || sessionNeedsYou(session)) return "needsYou";
	const column = toKanbanColumn(session.kanbanColumn, session.scmStatus ?? session.status);
	if (column === "needs_review") return "needsYou";
	if (column === "validating" || column === "ready") return "inReview";
	return "working";
}

/**
 * How long an agent that should be working has been silent, or undefined.
 * It states a duration, never a verdict: a long build is silent too.
 * Only an active agent can be quiet. An idle one has nothing to do, a waiting
 * one is already in "Needs you", and an exited one says so itself.
 */
export function quietForMs(
	session: Pick<WorkspaceSession, "lastEventAt" | "activity" | "updatedAt" | "isTerminated" | "status">,
	now: number,
	thresholdMs = QUIET_AFTER_MS,
): number | undefined {
	if (session.isTerminated || session.status === "terminated") return undefined;
	if (session.activity?.state !== "active") return undefined;
	const last = lastSignalAt(session);
	if (last === undefined) return undefined;
	const silent = now - last;
	return silent >= thresholdMs ? silent : undefined;
}

/** How far along a worker is, for ordering a merge: further along merges first. */
function progressRank(row: Pick<CrewRow, "group" | "session">): number {
	if (row.session.kanbanColumn === "ready") return 3;
	if (row.group === "inReview") return 2;
	if (row.group === "needsYou") return 1;
	return 0;
}

function orderForMerge(rows: CrewRow[]): CrewRow[] {
	return [...rows].sort(
		(a, b) =>
			progressRank(b) - progressRank(a) ||
			(time(a.session.createdAt) ?? 0) - (time(b.session.createdAt) ?? 0) ||
			a.session.id.localeCompare(b.session.id),
	);
}

/** Files changed by two or more workers. Finished and exited sessions no longer compete for the file. */
export function fileOverlaps(rows: CrewRow[]): FileOverlap[] {
	const byPath = new Map<string, CrewRow[]>();
	for (const row of rows) {
		if (row.group === "done" || !row.diff) continue;
		for (const file of row.diff.files) byPath.set(file.path, [...(byPath.get(file.path) ?? []), row]);
	}
	const overlaps: FileOverlap[] = [];
	for (const [path, owners] of byPath) {
		if (owners.length < 2) continue;
		overlaps.push({ path, sessionIds: orderForMerge(owners).map((row) => row.session.id) });
	}
	return overlaps.sort((a, b) => a.path.localeCompare(b.path));
}

function sortRows(rows: CrewRow[]): CrewRow[] {
	// Creation order, newest first: stable while agents work, so a row never jumps under the pointer.
	return [...rows].sort(
		(a, b) =>
			(time(b.session.createdAt) ?? 0) - (time(a.session.createdAt) ?? 0) || a.session.id.localeCompare(b.session.id),
	);
}

export function buildCrew({
	sessions,
	diffs,
	now,
	quietAfterMs = QUIET_AFTER_MS,
}: {
	sessions: WorkspaceSession[];
	/** Diff per worker session id; a missing entry means not read yet. */
	diffs: ReadonlyMap<string, CrewDiff>;
	now: number;
	quietAfterMs?: number;
}): CrewModel {
	const workers = sessions.filter((session) => !isOrchestratorSession(session));
	const base: CrewRow[] = workers
		.map((session): CrewRow => ({
			session,
			group: crewGroupOf(session),
			diff: diffs.get(session.id),
			quietMs: quietForMs(session, now, quietAfterMs),
			overlapPaths: [],
		}))
		// A finished session that left the window belongs to the archive, not the rail.
		.filter((row) => {
			if (row.group !== "done") return true;
			const last = lastSignalAt(row.session);
			return last === undefined || now - last <= DONE_WINDOW_MS;
		});
	const overlaps = fileOverlaps(base);
	const rows = sortRows(
		base.map((row) => ({
			...row,
			overlapPaths: overlaps.filter((overlap) => overlap.sessionIds.includes(row.session.id)).map((overlap) => overlap.path),
		})),
	);
	const live = rows.filter((row) => row.group !== "done" && row.diff);
	return {
		rows,
		groups: CREW_GROUP_ORDER.map((id) => ({ id, rows: rows.filter((row) => row.group === id) })),
		overlaps,
		totals: {
			additions: live.reduce((sum, row) => sum + (row.diff?.additions ?? 0), 0),
			deletions: live.reduce((sum, row) => sum + (row.diff?.deletions ?? 0), 0),
			worktrees: live.length,
		},
		needsYouCount: rows.filter((row) => row.group === "needsYou").length,
	};
}

/** What the workspace manifest says about a worktree, reduced to the rail's needs. */
export function crewDiffFromManifest(manifest: {
	files: ReadonlyArray<{ path: string; status: string; additions?: number; deletions?: number }>;
}): CrewDiff {
	const files = manifest.files
		.filter((file) => file.status !== "unmodified")
		.map((file) => ({ path: file.path, additions: file.additions ?? 0, deletions: file.deletions ?? 0 }));
	return {
		files,
		additions: files.reduce((sum, file) => sum + file.additions, 0),
		deletions: files.reduce((sum, file) => sum + file.deletions, 0),
	};
}

/** The scoped worker of a control room, or undefined for all agents / a worker that left the list. */
export function scopedRow(model: CrewModel, scope: string | undefined): CrewRow | undefined {
	return scope ? model.rows.find((row) => row.session.id === scope) : undefined;
}

export type CompactAge = { unit: "seconds" | "minutes" | "hours" | "days"; count: number };

/** A coarse "how long ago" for a tight column: 8s, 6m, 2h, 3d. */
export function compactAge(ms: number): CompactAge {
	const seconds = Math.max(0, Math.floor(ms / 1000));
	if (seconds < 60) return { unit: "seconds", count: seconds };
	if (seconds < 3600) return { unit: "minutes", count: Math.floor(seconds / 60) };
	if (seconds < 86_400) return { unit: "hours", count: Math.floor(seconds / 3600) };
	return { unit: "days", count: Math.floor(seconds / 86_400) };
}

/**
 * What the one-click nudge tells the orchestrator. It states the fact and asks
 * for a report; it does not say the worker is stuck, because it may not be.
 */
export function quietNudgeMessage(worker: Pick<WorkspaceSession, "id" | "title">, quietMs: number): string {
	const minutes = Math.max(1, Math.round(quietMs / 60_000));
	return [
		`Worker "${worker.title}" (${worker.id}) has had no update for ${minutes} minutes.`,
		"Ask it for a short status report with `ao send`, then tell me what it says.",
		"It may just be running a long command, so do not interrupt it.",
	].join(" ");
}

export type PlanProgress ={ done: number; total: number; activeIndex: number; allDone: boolean };

/**
 * The plan the orchestrator is working through: the newest turn's plan that no
 * later turn has replaced. Current state, not history (see ConversationPlan).
 */
export function currentPlan(snapshot: Pick<ConversationSnapshot, "turns"> | undefined): ConversationPlan | undefined {
	const turns = snapshot?.turns ?? [];
	for (let index = turns.length - 1; index >= 0; index -= 1) {
		const plan = turns[index]?.plan;
		if (plan && plan.steps.length > 0 && !plan.superseded) return plan;
	}
	return undefined;
}

export function planProgress(plan: ConversationPlan | undefined): PlanProgress | undefined {
	if (!plan || plan.steps.length === 0) return undefined;
	const done = plan.steps.filter((step) => step.status === "completed").length;
	const inProgress = plan.steps.findIndex((step) => step.status === "in_progress");
	const next = plan.steps.findIndex((step) => step.status === "pending");
	const activeIndex = inProgress >= 0 ? inProgress : next >= 0 ? next : plan.steps.length - 1;
	return { done, total: plan.steps.length, activeIndex, allDone: done === plan.steps.length };
}
