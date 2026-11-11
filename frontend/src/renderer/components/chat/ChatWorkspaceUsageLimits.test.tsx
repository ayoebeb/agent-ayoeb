import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { agentAccountsQueryKey, type AgentAccount } from "../../hooks/useAgentAccounts";
import { chatFixtureEmpty } from "../../lib/chat-fixture";
import type { ConversationSnapshot } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";
import { TooltipProvider } from "../ui/tooltip";
import { ChatWorkspace } from "./ChatWorkspace";

vi.mock("../NotificationCenter", () => ({ NotificationCenter: () => null }));
vi.mock("../../hooks/useAgentAccounts", async (original) => ({
	...await original<typeof import("../../hooks/useAgentAccounts")>(),
	useSessionAgentAccounts: (sessionId?: string) => ({ data: sessionId ? { claude: "acct-work" } : undefined, isPending: false }),
}));

const ACCOUNT: AgentAccount = {
	id: "acct-work",
	provider: "claude",
	email: "work@example.com",
	default: true,
	device: false,
	loggedIn: true,
	signedIn: true,
	limits: {
		fetchedAt: new Date().toISOString(),
		windows: [
			{ id: "five_hour", label: "Session (5h)", usedPercent: 38, durationMinutes: 300, resetsAt: new Date(Date.now() + 3_600_000).toISOString() },
			{ id: "seven_day", label: "Weekly", usedPercent: 12, durationMinutes: 10_080, resetsAt: new Date(Date.now() + 86_400_000).toISOString() },
		],
	},
};

const orchestrator = {
	id: chatFixtureEmpty.sessionId,
	workspaceId: "project-1",
	workspaceName: "agent-orchestrator",
	title: "Orchestrator",
	provider: "claude-code",
	kind: "orchestrator",
	mode: "chat",
	status: "working",
	updatedAt: "2026-08-15T00:00:00Z",
	activity: { state: "active", lastActivityAt: "2026-08-15T00:00:00Z" },
	prs: [],
} satisfies WorkspaceSession;

const claudeSnapshot: ConversationSnapshot = { ...chatFixtureEmpty, harness: "claude-code", controller: { state: "ready" } };

function renderComposer(snapshot: ConversationSnapshot, session: WorkspaceSession, remoteHostId?: string) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
	queryClient.setQueryData(agentAccountsQueryKey, [ACCOUNT]);
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<ChatWorkspace
					snapshot={snapshot}
					session={session}
					sessionRole={session.kind}
					remoteHostId={remoteHostId}
					models={[{ id: "opus", displayName: "Opus 5.5", default: true }]}
					onSend={vi.fn()}
					onChooseSettings={vi.fn()}
				/>
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

describe("ChatWorkspace composer usage limits", () => {
	it("shows the session account's limits in an orchestrator chat's turn settings", () => {
		renderComposer(claudeSnapshot, orchestrator);

		const limits = screen.getByRole("group", { name: "Claude Code limits" });
		expect(screen.getByRole("group", { name: "Turn settings" })).toContainElement(limits);
		expect(screen.getByRole("progressbar", { name: "Session (5h): 62% left" })).toBeInTheDocument();
		expect(screen.getByRole("progressbar", { name: "Weekly: 88% left" })).toBeInTheDocument();
	});

	it("leaves them out for another agent, a cloud session, and a session on another computer", () => {
		const { unmount } = renderComposer({ ...claudeSnapshot, harness: "codex" }, { ...orchestrator, provider: "codex" });
		expect(screen.queryByRole("group", { name: "Claude Code limits" })).not.toBeInTheDocument();
		unmount();

		const cloud = renderComposer(claudeSnapshot, { ...orchestrator, cloud: { orgId: "org-1" } });
		expect(screen.queryByRole("group", { name: "Claude Code limits" })).not.toBeInTheDocument();
		cloud.unmount();

		renderComposer(claudeSnapshot, orchestrator, "host-1");
		expect(screen.queryByRole("group", { name: "Claude Code limits" })).not.toBeInTheDocument();
	});
});
