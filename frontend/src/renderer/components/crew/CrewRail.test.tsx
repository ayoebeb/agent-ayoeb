import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { buildCrew, type CrewDiff } from "../../lib/crew";
import type { ConversationActivity, ConversationPlan } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";

const NOW = Date.parse("2026-11-16T12:00:00Z");
const MIN = 60_000;
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const { navigate, post, resolve, room, crewState, conversations, watching, hostClient } = vi.hoisted(() => ({
	navigate: vi.fn(),
	hostClient: vi.fn(),
	post: vi.fn(),
	resolve: vi.fn(),
	room: {
		tab: "session" as string,
		scope: undefined as string | undefined,
		setTab: vi.fn(),
		setScope: vi.fn(),
		show: vi.fn(),
	},
	crewState: { current: undefined as unknown },
	conversations: {} as Record<string, { plan?: ConversationPlan; approval?: ConversationActivity; question?: ConversationActivity } | undefined>,
	watching: {} as Record<string, boolean>,
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("../../hooks/useControlRoom", () => ({ useControlRoom: () => room }));
vi.mock("../../hooks/useConversation", () => ({ useConversationCommands: () => ({ resolveAsync: resolve }) }));
vi.mock("../../hooks/useCrew", () => ({
	useCrew: () => crewState.current,
	useCrewConversation: (session: WorkspaceSession, enabled: boolean) => {
		watching[session.id] = enabled;
		return conversations[session.id];
	},
	crewConversationQueryKey: (id: string) => ["crew-conversation", id],
}));
vi.mock("../../lib/host-clients", () => ({ clientForSessionHost: (hostId?: string) => hostClient(hostId) }));

import { CrewRail } from "./CrewRail";
import { TooltipProvider } from "../ui/tooltip";

const orchestrator = { id: "p-orchestrator", workspaceId: "p", kind: "orchestrator", title: "Orchestrator" } as WorkspaceSession;

function worker(over: Partial<WorkspaceSession> & { id: string }): WorkspaceSession {
	return {
		workspaceId: "p",
		workspaceName: "p",
		title: over.id,
		provider: "claude-code",
		mode: "chat",
		status: "working",
		kanbanColumn: "building",
		activity: { state: "active", lastActivityAt: ago(30_000) },
		lastEventAt: ago(30_000),
		createdAt: ago(60 * MIN),
		updatedAt: ago(30_000),
		prs: [],
		branch: "ao/session/feature",
		...over,
	} as WorkspaceSession;
}

const diff = (additions: number, deletions: number, ...paths: string[]): CrewDiff => ({
	files: paths.map((path) => ({ path, additions: 1, deletions: 0 })),
	additions,
	deletions,
});

function setCrew(sessions: WorkspaceSession[], diffs: Record<string, CrewDiff> = {}) {
	crewState.current = {
		model: buildCrew({ sessions, diffs: new Map(Object.entries(diffs)), now: NOW }),
		sessions,
		now: new Date(NOW),
	};
}

function renderRail(): ReturnType<typeof render> {
	const client = new QueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>
			<TooltipProvider>{children}</TooltipProvider>
		</QueryClientProvider>
	);
	return render(<CrewRail orchestrator={orchestrator} />, { wrapper });
}

const row = (id: string): HTMLElement => {
	const found = screen.getAllByTestId("crew-row").find((element) => element.dataset.sessionId === id);
	if (!found) throw new Error(`no crew row for ${id}`);
	return found;
};

beforeEach(() => {
	vi.clearAllMocks();
	for (const key of Object.keys(conversations)) delete conversations[key];
	for (const key of Object.keys(watching)) delete watching[key];
	room.scope = undefined;
	post.mockReset();
	post.mockResolvedValue({ error: undefined });
	resolve.mockReset();
	resolve.mockResolvedValue(undefined);
	hostClient.mockReset();
	hostClient.mockImplementation(() => ({ POST: post }));
});

describe("CrewRail", () => {
	it("says there is nobody yet when the project has no workers", () => {
		setCrew([]);
		renderRail();
		expect(screen.getByTestId("crew-empty")).toHaveTextContent("No workers yet. Ask the orchestrator to start one.");
	});

	it("groups workers by what the person must do and counts them in the header", () => {
		setCrew([
			worker({ id: "asks", title: "Anchor links", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } }),
			worker({ id: "builds", title: "Jira toggle" }),
			worker({ id: "reviewed", title: "Chat polish", status: "pr_open", kanbanColumn: "validating", displayStatus: "Reviewing" }),
		]);
		renderRail();
		expect(screen.getByTestId("crew-counts")).toHaveTextContent("1 need you · 1 working · 1 in review");
		const groups = screen.getAllByTestId("crew-group").map((group) => group.querySelector("h3")?.textContent);
		expect(groups).toEqual(["Needs you1", "Working1", "In review1"]);
	});

	it("shows the step a chat worker is on and falls back to its derived status", () => {
		setCrew([worker({ id: "planner", title: "Planner" }), worker({ id: "plain", title: "Plain", displayStatus: "Working" })]);
		conversations.planner = {
			plan: { steps: [{ text: "Read the code", status: "completed" }, { text: "Wire the toggle", status: "in_progress" }, { text: "Test", status: "pending" }] },
		};
		renderRail();
		expect(within(row("planner")).getByTestId("crew-line")).toHaveTextContent("Step 2 of 3 · Wire the toggle");
		expect(within(row("plain")).getByTestId("crew-line")).toHaveTextContent("Working");
	});

	it("watches only the workers that can need the person, and none while the rail is hidden", () => {
		const sessions = [
			worker({ id: "asks", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } }),
			worker({ id: "builds" }),
			worker({ id: "reviewed", status: "pr_open", kanbanColumn: "validating", displayStatus: "Reviewing" }),
		];
		setCrew(sessions);
		const { rerender } = renderRail();
		expect(watching).toEqual({ asks: true, builds: true, reviewed: false });

		rerender(<CrewRail active={false} orchestrator={orchestrator} />);
		expect(watching).toEqual({ asks: false, builds: false, reviewed: false });
	});

	it("shows branch, lines and PR state for a worker", () => {
		const pr = { url: "https://github.com/o/r/pull/31", number: 31, state: "draft" as const, ci: "running", review: "none", mergeability: "unknown", reviewComments: false, updatedAt: ago(MIN) };
		setCrew([worker({ id: "w", prs: [pr] })], { w: diff(184, 22, "a.ts", "b.ts") });
		renderRail();
		const meta = within(row("w")).getByTestId("crew-meta");
		expect(meta).toHaveTextContent("session/feature");
		expect(meta).not.toHaveTextContent("ao/");
		expect(within(row("w")).getByTestId("crew-diff")).toHaveTextContent("+184−222 files");
		expect(within(row("w")).getByTestId("crew-pr")).toHaveTextContent("PR #31 · draft · CI running");
	});

	it("marks a quiet agent with the fact, and one click asks the orchestrator for a report", async () => {
		setCrew([worker({ id: "slow", title: "Remote hosts spike", lastEventAt: ago(7 * MIN), activity: { state: "active", lastActivityAt: ago(7 * MIN) } })]);
		renderRail();
		const quiet = within(row("slow")).getByTestId("crew-quiet");
		expect(quiet).toHaveTextContent("No update for 7m");
		expect(quiet).not.toHaveTextContent(/stalled|stuck/i);

		await userEvent.click(within(quiet).getByRole("button", { name: "Ask for a report" }));

		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		const [path, options] = post.mock.calls[0]!;
		expect(path).toBe("/api/v1/sessions/{sessionId}/send");
		expect(options.params.path.sessionId).toBe("p-orchestrator");
		expect(options.body.message).toContain('"Remote hosts spike" (slow)');
		expect(await within(quiet).findByRole("status")).toHaveTextContent("Asked the orchestrator");
		expect(within(quiet).queryByRole("button")).toBeNull();
	});

	it("says when the orchestrator could not be reached and lets the person try again", async () => {
		post.mockResolvedValue({ error: { message: "down" } });
		setCrew([worker({ id: "slow", lastEventAt: ago(9 * MIN), activity: { state: "active", lastActivityAt: ago(9 * MIN) } })]);
		renderRail();
		await userEvent.click(screen.getByRole("button", { name: "Ask for a report" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("Could not reach the orchestrator");
		expect(screen.getByRole("button", { name: "Ask for a report" })).toBeEnabled();
	});

	it("recovers when the request itself rejects (network failure), and lets the person retry", async () => {
		post.mockRejectedValueOnce(new Error("network down"));
		setCrew([worker({ id: "slow", lastEventAt: ago(9 * MIN), activity: { state: "active", lastActivityAt: ago(9 * MIN) } })]);
		renderRail();
		await userEvent.click(screen.getByRole("button", { name: "Ask for a report" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("Could not reach the orchestrator");
		expect(screen.getByRole("button", { name: "Ask for a report" })).toBeEnabled();

		await userEvent.click(screen.getByRole("button", { name: "Ask for a report" }));
		expect(await screen.findByRole("status")).toHaveTextContent("Asked the orchestrator");
		expect(post).toHaveBeenCalledTimes(2);
	});

	it("recovers when no client can be built for the orchestrator's host (a remote host that went away)", async () => {
		hostClient.mockImplementationOnce(() => {
			throw new Error("host disconnected");
		});
		setCrew([worker({ id: "slow", lastEventAt: ago(9 * MIN), activity: { state: "active", lastActivityAt: ago(9 * MIN) } })]);
		renderRail();
		await userEvent.click(screen.getByRole("button", { name: "Ask for a report" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("Could not reach the orchestrator");
		expect(screen.getByRole("button", { name: "Ask for a report" })).toBeEnabled();
		expect(post).not.toHaveBeenCalled();
	});

	it("does not mark a worker that is only a minute silent", () => {
		setCrew([worker({ id: "fine", lastEventAt: ago(MIN), activity: { state: "active", lastActivityAt: ago(MIN) } })]);
		renderRail();
		expect(screen.queryByTestId("crew-quiet")).toBeNull();
	});

	it("answers an approval in place with only what the provider offers, never Always allow", async () => {
		setCrew([worker({ id: "asks", title: "Permission fix", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } })]);
		conversations.asks = {
			approval: {
				kind: "activity", id: "a1", sequence: 5, revision: 1, activityKind: "approval", status: "pending", createdAt: ago(MIN),
				summary: "Run npm test -- permissions", requestId: "acp-request:h:2",
				decisions: [
					{ id: "allow", label: "Allow once", kind: "allow_once" },
					{ id: "allow_always", label: "Always allow", kind: "allow_always" },
					{ id: "reject", label: "Deny", kind: "reject_once" },
				],
			} as ConversationActivity,
		};
		renderRail();
		const prompt = within(row("asks")).getByTestId("crew-approval");
		expect(within(row("asks")).getByTestId("crew-line")).toHaveTextContent("Run npm test -- permissions");
		expect(within(prompt).queryByRole("button", { name: "Always allow" })).toBeNull();

		await userEvent.click(within(prompt).getByRole("button", { name: "Allow once" }));

		expect(resolve).toHaveBeenCalledWith("acp-request:h:2", "allow");
		expect(within(prompt).getByRole("status")).toHaveTextContent("Sending…");
		expect(within(prompt).getByRole("button", { name: "Allow once" })).toBeDisabled();
	});

	const approvalFor = (requestId: string): ConversationActivity => ({
		kind: "activity", id: `a-${requestId}`, sequence: 5, revision: 1, activityKind: "approval", status: "pending", createdAt: ago(MIN),
		summary: "Run npm test", requestId,
		decisions: [{ id: "allow", label: "Allow once", kind: "allow_once" }, { id: "reject", label: "Deny", kind: "reject_once" }],
	} as ConversationActivity);

	it("shows a failed answer and re-enables the same request for another try", async () => {
		resolve.mockRejectedValueOnce(new Error("503"));
		setCrew([worker({ id: "asks", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } })]);
		conversations.asks = { approval: approvalFor("r1") };
		renderRail();
		const prompt = within(row("asks")).getByTestId("crew-approval");

		await userEvent.click(within(prompt).getByRole("button", { name: "Allow once" }));
		expect(await within(prompt).findByRole("alert")).toHaveTextContent("Could not send the answer. Try again.");
		expect(within(prompt).queryByRole("status")).toBeNull();
		expect(within(prompt).getByRole("button", { name: "Allow once" })).toBeEnabled();
		expect(within(prompt).getByRole("button", { name: "Deny" })).toBeEnabled();

		await userEvent.click(within(prompt).getByRole("button", { name: "Allow once" }));
		expect(resolve).toHaveBeenCalledTimes(2);
		expect(resolve).toHaveBeenLastCalledWith("r1", "allow");
		expect(await within(prompt).findByRole("status")).toHaveTextContent("Sending…");
		expect(within(prompt).queryByRole("alert")).toBeNull();
	});

	it("does not carry a failed or pending answer over to the next request", async () => {
		resolve.mockRejectedValueOnce(new Error("503"));
		setCrew([worker({ id: "asks", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } })]);
		conversations.asks = { approval: approvalFor("r1") };
		const { rerender } = renderRail();
		await userEvent.click(screen.getByRole("button", { name: "Allow once" }));
		expect(await screen.findByRole("alert")).toBeInTheDocument();

		conversations.asks = { approval: approvalFor("r2") };
		rerender(<CrewRail orchestrator={orchestrator} />);
		expect(screen.queryByRole("alert")).toBeNull();
		expect(screen.getByRole("button", { name: "Allow once" })).toBeEnabled();
	});

	it("labels the buttons by meaning in sentence case, whatever the provider calls them", async () => {
		setCrew([worker({ id: "asks", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } })]);
		conversations.asks = {
			approval: {
				kind: "activity", id: "a1", sequence: 5, revision: 1, activityKind: "approval", status: "pending", createdAt: ago(MIN),
				summary: "Edit file", requestId: "r2",
				decisions: [{ id: "accept", label: "Allow Once", kind: "allow_once" }, { id: "decline", label: "REJECT", kind: "reject_once" }],
			} as ConversationActivity,
		};
		renderRail();
		expect(screen.getByRole("button", { name: "Allow once" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Deny" })).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Deny" }));
		expect(resolve).toHaveBeenCalledWith("r2", "decline");
	});

	it("sends a refusal with the provider's own decision id", async () => {
		setCrew([worker({ id: "asks", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } })]);
		conversations.asks = {
			approval: {
				kind: "activity", id: "a1", sequence: 5, revision: 1, activityKind: "approval", status: "pending", createdAt: ago(MIN),
				summary: "Edit file", requestId: "r9", decisions: [{ id: "decline", label: "Decline", kind: "reject_once" }],
			} as ConversationActivity,
		};
		renderRail();
		await userEvent.click(screen.getByRole("button", { name: "Deny" }));
		expect(resolve).toHaveBeenCalledWith("r9", "decline");
		expect(screen.queryByRole("button", { name: "Allow once" })).toBeNull();
	});

	it("sends a question and an approval with no usable decision to the worker's chat", async () => {
		setCrew([worker({ id: "q", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } })]);
		conversations.q = {
			question: { kind: "activity", id: "q1", sequence: 3, revision: 1, activityKind: "user_input", status: "pending", createdAt: ago(MIN), summary: "Should anchors also work in summaries?", requestId: "r1" } as ConversationActivity,
		};
		renderRail();
		expect(within(row("q")).getByTestId("crew-line")).toHaveTextContent("Should anchors also work in summaries?");
		await userEvent.click(screen.getByRole("button", { name: "Answer in chat" }));
		expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ params: expect.objectContaining({ projectId: "p", sessionId: "q" }) }));
		expect(resolve).not.toHaveBeenCalled();
	});

	it("warns about a file two agents changed and says who should merge first", async () => {
		setCrew(
			[
				worker({ id: "polish", title: "Chat polish", status: "mergeable", kanbanColumn: "ready", displayStatus: "Mergeable" }),
				worker({ id: "anchors", title: "Anchor links" }),
			],
			{ polish: diff(3, 1, "ChatWorkspace.tsx"), anchors: diff(2, 0, "ChatWorkspace.tsx", "anchor-links.ts") },
		);
		renderRail();
		const notice = screen.getByTestId("crew-overlap");
		expect(notice).toHaveTextContent("Same file, more than one agent");
		expect(within(notice).getByTestId("crew-overlap-item")).toHaveTextContent("ChatWorkspace.tsx · Chat polish, Anchor links");
		expect(notice).toHaveTextContent("Merge Chat polish first: it is further along.");

		await userEvent.click(within(notice).getByRole("button", { name: "Review in Changes" }));
		expect(room.show).toHaveBeenCalledWith("changes");
	});

	it("shows no warning when each file has one owner", () => {
		setCrew([worker({ id: "a" }), worker({ id: "b" })], { a: diff(1, 0, "a.ts"), b: diff(1, 0, "b.ts") });
		renderRail();
		expect(screen.queryByTestId("crew-overlap")).toBeNull();
	});

	it("jumps to a worker's changes, commits, terminal or chat from its row", async () => {
		setCrew([worker({ id: "w", title: "Jira toggle" })]);
		renderRail();
		await userEvent.click(screen.getByRole("button", { name: "Changes of Jira toggle" }));
		await userEvent.click(screen.getByRole("button", { name: "Commits of Jira toggle" }));
		await userEvent.click(screen.getByRole("button", { name: "Terminal of Jira toggle" }));
		expect(room.show.mock.calls).toEqual([["changes", "w"], ["commits", "w"], ["terminal", "w"]]);

		await userEvent.click(screen.getByRole("button", { name: "Open Jira toggle" }));
		expect(navigate).toHaveBeenCalledTimes(1);
	});

	it("scopes the tabs to a worker when its title is picked, and back to all when picked again", async () => {
		setCrew([worker({ id: "w", title: "Jira toggle" })]);
		const { rerender } = renderRail();
		await userEvent.click(screen.getByRole("button", { name: "Jira toggle" }));
		expect(room.setScope).toHaveBeenLastCalledWith("w");

		room.scope = "w";
		rerender(<CrewRail orchestrator={orchestrator} />);
		await userEvent.click(screen.getByRole("button", { name: "Jira toggle" }));
		expect(room.setScope).toHaveBeenLastCalledWith(undefined);
	});

	it("keeps finished work folded under Done today until it is opened", async () => {
		setCrew([worker({ id: "old", title: "Finished", isTerminated: true, status: "terminated", lastEventAt: ago(3 * 60 * MIN), activity: { state: "exited", lastActivityAt: ago(3 * 60 * MIN) } })]);
		renderRail();
		expect(screen.queryByTestId("crew-row")).toBeNull();
		await userEvent.click(screen.getByRole("button", { name: /Done today/ }));
		expect(screen.getByTestId("crew-row")).toHaveTextContent("Finished");
	});
});
