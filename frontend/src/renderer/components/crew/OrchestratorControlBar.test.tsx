import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { buildCrew } from "../../lib/crew";
import type { ConversationPlan, ConversationUsage } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";

const NOW = Date.parse("2026-11-16T12:00:00Z");
const MIN = 60_000;
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const { room, crewState, conversation, interrupt, pin, unpin } = vi.hoisted(() => ({
	room: { tab: "session" as string, scope: undefined as string | undefined, setTab: vi.fn(), setScope: vi.fn(), show: vi.fn() },
	crewState: { current: undefined as unknown },
	conversation: { snapshot: undefined as unknown },
	interrupt: vi.fn(),
	pin: { mutate: vi.fn(), isPending: false },
	unpin: { mutate: vi.fn(), isPending: false },
}));

vi.mock("../../hooks/useControlRoom", () => ({ useControlRoom: () => room }));
vi.mock("../../hooks/useCrew", () => ({ useCrew: () => crewState.current }));
vi.mock("../../hooks/useConversation", () => ({
	useConversation: () => ({ snapshot: conversation.snapshot }),
	useConversationCommands: () => ({ interrupt }),
}));
vi.mock("../../hooks/usePinSession", () => ({ usePinSession: () => pin, useUnpinSession: () => unpin }));
vi.mock("../../lib/host-clients", () => ({ labelForHost: (id: string) => (id === "host-1" ? "NLSTATION78" : undefined) }));

import { OrchestratorControlBar, controlRoomTabCounts } from "./OrchestratorControlBar";
import { TooltipProvider } from "../ui/tooltip";

const orchestrator = {
	id: "p-orchestrator",
	workspaceId: "p",
	kind: "orchestrator",
	mode: "chat",
	branch: "main",
	model: "claude-opus-5-5",
	isPinned: false,
} as WorkspaceSession;

function worker(over: Partial<WorkspaceSession> & { id: string }): WorkspaceSession {
	return {
		workspaceId: "p", workspaceName: "p", title: over.id, provider: "claude-code", status: "working", kanbanColumn: "building",
		activity: { state: "active", lastActivityAt: ago(MIN) }, lastEventAt: ago(MIN), createdAt: ago(60 * MIN), updatedAt: ago(MIN), prs: [], ...over,
	} as WorkspaceSession;
}

function setCrew(sessions: WorkspaceSession[], diffs: Record<string, { additions: number; deletions: number; paths: string[] }> = {}) {
	const map = new Map(Object.entries(diffs).map(([id, d]) => [id, { additions: d.additions, deletions: d.deletions, files: d.paths.map((path) => ({ path, additions: 1, deletions: 0 })) }]));
	crewState.current = { model: buildCrew({ sessions, diffs: map, now: NOW }), sessions, now: new Date(NOW) };
}

const plan = (...statuses: ("pending" | "in_progress" | "completed")[]): ConversationPlan => ({
	steps: statuses.map((status, index) => ({ text: `Step ${index + 1}`, status })),
});

function snapshotWith(extra: { plan?: ConversationPlan; running?: boolean; usage?: ConversationUsage; model?: string } = {}) {
	return {
		turns: [{ id: "t1", state: extra.running ? "running" : "completed", requestedAt: ago(MIN), plan: extra.plan }],
		items: [],
		settings: { model: extra.model },
		usage: extra.usage,
	};
}

function renderBar(over: Partial<WorkspaceSession> = {}) {
	const wrapper = ({ children }: { children: ReactNode }) => <TooltipProvider>{children}</TooltipProvider>;
	return render(<OrchestratorControlBar orchestrator={{ ...orchestrator, ...over }} />, { wrapper });
}

beforeEach(() => {
	vi.clearAllMocks();
	room.tab = "session";
	room.scope = undefined;
	conversation.snapshot = snapshotWith();
	setCrew([]);
});

describe("OrchestratorControlBar tabs", () => {
	it("lists Session, Changes, Commits, Files and Terminal with the current one selected", () => {
		room.tab = "changes";
		renderBar();
		const tabs = within(screen.getByRole("tablist", { name: "Orchestrator views" })).getAllByRole("tab");
		expect(tabs.map((tab) => tab.textContent)).toEqual(["Session", "Changes", "Commits", "Files", "Terminal"]);
		expect(tabs.map((tab) => tab.getAttribute("aria-selected"))).toEqual(["false", "true", "false", "false", "false"]);
	});

	it("counts changed files across live worktrees and commits ahead of the base", () => {
		setCrew(
			[worker({ id: "a", branchState: { commits: 3, unpushed: 1 } }), worker({ id: "b", branchState: { commits: 2, unpushed: 0 } }), worker({ id: "gone", isTerminated: true, status: "terminated" })],
			{ a: { additions: 4, deletions: 1, paths: ["x.ts", "y.ts"] }, b: { additions: 2, deletions: 0, paths: ["x.ts"] } },
		);
		renderBar();
		expect(screen.getByRole("tab", { name: /Changes/ })).toHaveTextContent("Changes3");
		expect(screen.getByRole("tab", { name: /Commits/ })).toHaveTextContent("Commits5");
		expect(screen.getByRole("tab", { name: "Files" })).toHaveTextContent(/^Files$/);
	});

	it("counts a file twice when two worktrees both changed it, because each is a separate change to review", () => {
		setCrew([worker({ id: "a" }), worker({ id: "b" })], { a: { additions: 1, deletions: 0, paths: ["same.ts"] }, b: { additions: 1, deletions: 0, paths: ["same.ts"] } });
		expect(controlRoomTabCounts((crewState.current as { model: Parameters<typeof controlRoomTabCounts>[0] }).model).changes).toBe(2);
	});

	it("switches tab on click and with the arrow keys, wrapping at the ends", async () => {
		renderBar();
		await userEvent.click(screen.getByRole("tab", { name: /Commits/ }));
		expect(room.setTab).toHaveBeenLastCalledWith("commits");

		screen.getByRole("tab", { name: "Session" }).focus();
		await userEvent.keyboard("{ArrowLeft}");
		expect(room.setTab).toHaveBeenLastCalledWith("terminal");
		await userEvent.keyboard("{End}");
		expect(room.setTab).toHaveBeenLastCalledWith("terminal");
	});
});

describe("OrchestratorControlBar info row", () => {
	it("shows the branch, the lines across worktrees, the model and the machine", () => {
		setCrew([worker({ id: "a" }), worker({ id: "b" })], { a: { additions: 633, deletions: 98, paths: ["a.ts"] }, b: { additions: 1, deletions: 2, paths: ["b.ts"] } });
		renderBar();
		const row = screen.getByTestId("control-info-row");
		expect(row).toHaveTextContent("main");
		expect(screen.getByTestId("control-info-diff")).toHaveTextContent("+634−100across 2 worktrees");
		expect(row).toHaveTextContent("claude-opus-5-5");
		expect(screen.getByTestId("control-info-machine")).toHaveTextContent("This computer");
	});

	it("names the other computer for a remote project and falls back to the model of the chat", () => {
		conversation.snapshot = snapshotWith({ model: "gpt-5" });
		renderBar({ hostId: "host-1", model: undefined });
		expect(screen.getByTestId("control-info-machine")).toHaveTextContent("NLSTATION78");
		expect(screen.getByTestId("control-info-row")).toHaveTextContent("gpt-5");
	});

	it("shows no line total until a worktree has answered", () => {
		renderBar();
		expect(screen.queryByTestId("control-info-diff")).toBeNull();
	});

	it("shows the orchestrator's own tokens and cost and says whose they are", () => {
		conversation.snapshot = snapshotWith({ usage: { contextUsed: 1, contextWindow: 0, inputTokens: 2_100_000, outputTokens: 148_000, cachedTokens: 0, totalTokens: 2_248_000, cost: 11.3, currency: "USD" } });
		renderBar();
		const usage = screen.getByTestId("control-info-usage");
		expect(usage).toHaveTextContent("In 2.1MOut 148K$11.30orchestrator only");
	});

	it("shows a dash, never zero, when the account reports no cost", () => {
		conversation.snapshot = snapshotWith({ usage: { contextUsed: 1, contextWindow: 0, inputTokens: 10, outputTokens: 5, cachedTokens: 0, totalTokens: 15, cost: null } });
		renderBar();
		const usage = screen.getByTestId("control-info-usage");
		expect(usage).toHaveTextContent("–");
		expect(usage).not.toHaveTextContent("$0");
	});

	it("leaves a clear placeholder when no usage was reported", () => {
		renderBar();
		expect(screen.getByTestId("control-info-usage")).toHaveTextContent("Tokens and cost: not reported");
	});

	it("folds the info row and plan bar away and back", async () => {
		conversation.snapshot = snapshotWith({ plan: plan("completed", "in_progress") });
		renderBar();
		expect(screen.getByTestId("control-info-row")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Hide project details" }));
		expect(screen.queryByTestId("control-info-row")).toBeNull();
		expect(screen.queryByTestId("control-plan-bar")).toBeNull();
		await userEvent.click(screen.getByRole("button", { name: "Show project details" }));
		expect(screen.getByTestId("control-plan-bar")).toBeInTheDocument();
	});
});

describe("OrchestratorControlBar plan bar", () => {
	it("draws one segment per step in its state and counts the done ones with who needs you", () => {
		conversation.snapshot = snapshotWith({ plan: plan("completed", "completed", "in_progress", "pending") });
		setCrew([worker({ id: "asks", status: "needs_input", activity: { state: "waiting_input", lastActivityAt: ago(MIN) } })]);
		renderBar();
		const segments = within(screen.getByRole("list", { name: "Orchestrator plan" })).getAllByRole("listitem");
		expect(segments.map((segment) => segment.getAttribute("data-step-status"))).toEqual(["completed", "completed", "in_progress", "pending"]);
		expect(screen.getByTestId("control-plan-count")).toHaveTextContent("2 of 4 done · 1 need you");
	});

	it("draws nothing when the orchestrator made no plan, rather than a fake bar", () => {
		renderBar();
		expect(screen.queryByTestId("control-plan-bar")).toBeNull();
	});

	it("omits the need-you count when nobody waits", () => {
		conversation.snapshot = snapshotWith({ plan: plan("completed") });
		renderBar();
		expect(screen.getByTestId("control-plan-count")).toHaveTextContent(/^1 of 1 done$/);
	});
});

describe("OrchestratorControlBar actions", () => {
	it("offers Stop turn only while the orchestrator is mid-turn, and stops it", async () => {
		const { rerender } = renderBar();
		expect(screen.queryByRole("button", { name: "Stop turn" })).toBeNull();

		conversation.snapshot = snapshotWith({ running: true });
		rerender(<OrchestratorControlBar orchestrator={orchestrator} />);
		await userEvent.click(screen.getByRole("button", { name: "Stop turn" }));
		expect(interrupt).toHaveBeenCalledTimes(1);
	});

	it("pins an unpinned orchestrator and unpins a pinned one", async () => {
		const { rerender } = renderBar();
		await userEvent.click(screen.getByRole("button", { name: "Pin" }));
		expect(pin.mutate).toHaveBeenCalledWith(expect.objectContaining({ id: "p-orchestrator" }));

		rerender(<OrchestratorControlBar orchestrator={{ ...orchestrator, isPinned: true }} />);
		const unpinButton = screen.getByRole("button", { name: "Unpin" });
		expect(unpinButton).toHaveAttribute("aria-pressed", "true");
		await userEvent.click(unpinButton);
		expect(unpin.mutate).toHaveBeenCalledWith(expect.objectContaining({ id: "p-orchestrator" }));
	});
});
