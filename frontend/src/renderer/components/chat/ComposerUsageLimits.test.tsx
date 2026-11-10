import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentAccount, AgentAccountLimitWindow } from "../../hooks/useAgentAccounts";
import { ComposerUsageLimits, sessionClaudeAccount } from "./ComposerUsageLimits";

// The readout's data comes from the shared accounts query and the session's
// account binding; each test sets what those hooks return.
const hooks = vi.hoisted(() => ({
	accounts: { data: undefined, isPending: false, error: null } as { data?: AgentAccount[]; isPending: boolean; error: Error | null },
	binding: { data: undefined, isPending: false } as { data?: { claude?: string }; isPending: boolean },
	accountsEnabled: [] as boolean[],
	boundSessions: [] as (string | undefined)[],
}));
vi.mock("../../hooks/useAgentAccounts", async (original) => ({
	...await original<typeof import("../../hooks/useAgentAccounts")>(),
	useAgentAccounts: (enabled = true) => {
		hooks.accountsEnabled.push(enabled);
		return hooks.accounts;
	},
	// A disabled query stays pending, as react-query's does.
	useSessionAgentAccounts: (sessionId?: string) => {
		hooks.boundSessions.push(sessionId);
		return sessionId ? hooks.binding : { data: undefined, isPending: true };
	},
}));

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

/** A window that resets `resetsIn` from now; half a minute short so the countdown rounds to whole minutes. */
function limitWindow(id: string, label: string, usedPercent: number, durationMinutes: number, resetsIn: number): AgentAccountLimitWindow {
	return { id, label, usedPercent, durationMinutes, resetsAt: new Date(Date.now() + resetsIn - MINUTE / 2).toISOString() };
}

function account(id: string, over: Partial<AgentAccount> = {}, used: [number, number] = [60, 19]): AgentAccount {
	return {
		id,
		provider: "claude",
		email: `${id}@example.com`,
		default: false,
		device: false,
		loggedIn: true,
		signedIn: true,
		limits: {
			// Read a minute ago, as the daemon reuses a reading.
			fetchedAt: new Date(Date.now() - MINUTE).toISOString(),
			windows: [
				limitWindow("five_hour", "Session (5h)", used[0], 5 * 60, 2 * HOUR + 14 * MINUTE),
				limitWindow("seven_day", "Weekly", used[1], 7 * 24 * 60, 76 * HOUR),
				limitWindow("seven_day_opus", "Weekly · Opus", 50, 7 * 24 * 60, 76 * HOUR),
			],
		},
		...over,
	};
}

function readout() {
	return screen.getByRole("group", { name: "Claude Code limits" });
}

beforeEach(() => {
	hooks.accounts = { data: [account("a", { default: true }, [10, 10]), account("b")], isPending: false, error: null };
	hooks.binding = { data: { claude: "b" }, isPending: false };
	hooks.accountsEnabled = [];
	hooks.boundSessions = [];
});

describe("ComposerUsageLimits", () => {
	it("shows the share left in the session account's 5-hour and weekly windows for Claude Code", () => {
		render(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);

		expect(hooks.boundSessions).toContain("s1");
		const group = readout();
		expect(group).toHaveTextContent("5h");
		expect(group).toHaveTextContent("Weekly");
		expect(screen.getByRole("progressbar", { name: "Session (5h): 40% left" })).toHaveAttribute("aria-valuenow", "40");
		expect(screen.getByRole("progressbar", { name: "Weekly: 81% left" })).toHaveAttribute("aria-valuenow", "81");
		// Only the two headline windows, not the per-model weekly ones.
		expect(screen.getAllByRole("progressbar")).toHaveLength(2);
	});

	it("opens a tooltip with each window's reset countdown and pace, in the Limits tab's words", async () => {
		const user = userEvent.setup();
		render(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);

		await user.hover(readout());
		const tooltip = await screen.findByRole("tooltip");
		// Two Claude accounts, so it names the one it reads.
		expect(tooltip).toHaveTextContent("b@example.com");
		expect(tooltip).toHaveTextContent("Session (5h)40% leftResets in 2h 14m");
		// 60% used 2h 46m into five hours runs out before the reset.
		expect(tooltip).toHaveTextContent(/At this pace it runs out in 1h 5\dm/);
		expect(tooltip).toHaveTextContent("Weekly81% leftResets in 3d 4h");
		// 19% used 92h into the week projects to about 35% used at the reset.
		expect(tooltip).toHaveTextContent("At this pace: about 65% left at the reset");
		expect(tooltip).not.toHaveTextContent("Showing numbers from");
	});

	it("colours each bar as the Limits tab does: amber at a quarter left, red when spent", () => {
		hooks.accounts = { data: [account("b", {}, [80, 100])], isPending: false, error: null };
		render(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);

		const fill = (name: string) => screen.getByRole("progressbar", { name }).querySelector("[aria-hidden] > span");
		expect(fill("Session (5h): 20% left")).toHaveClass("bg-warning");
		expect(fill("Weekly: 0% left")).toHaveClass("bg-error");
	});

	it("is hidden for other agents and does not read their accounts", () => {
		const { container } = render(<ComposerUsageLimits harness="codex" sessionId="s1" />);

		expect(container).toBeEmptyDOMElement();
		expect(hooks.accountsEnabled.every((enabled) => !enabled)).toBe(true);
		expect(hooks.boundSessions.every((sessionId) => sessionId === undefined)).toBe(true);
	});

	it("follows the default login when the session names no account", () => {
		hooks.binding = { data: {}, isPending: false };
		render(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);

		expect(screen.getByRole("progressbar", { name: "Session (5h): 90% left" })).toBeInTheDocument();
	});

	it("holds its place while the accounts or the session's account load", () => {
		hooks.accounts = { data: undefined, isPending: true, error: null };
		const { rerender } = render(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);
		expect(screen.getByRole("group", { name: "Checking limits…" })).toHaveAttribute("aria-busy", "true");
		expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();

		// Not the default account's numbers while the session's own is unknown.
		hooks.accounts = { data: [account("a", { default: true }, [10, 10]), account("b")], isPending: false, error: null };
		hooks.binding = { data: undefined, isPending: true };
		rerender(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);
		expect(screen.getByRole("group", { name: "Checking limits…" })).toBeInTheDocument();
	});

	it("renders nothing without a Claude account or a limit window", () => {
		hooks.accounts = { data: [], isPending: false, error: null };
		const { container, rerender } = render(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);
		expect(container).toBeEmptyDOMElement();

		hooks.accounts = { data: [account("b", { limits: { fetchedAt: new Date().toISOString(), windows: [] } })], isPending: false, error: null };
		rerender(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);
		expect(container).toBeEmptyDOMElement();

		// A first read that failed has no numbers to show.
		hooks.accounts = { data: undefined, isPending: false, error: new Error("daemon unreachable") };
		rerender(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);
		expect(container).toBeEmptyDOMElement();
	});

	it("keeps the last numbers when a refresh fails and says how old they are", async () => {
		const user = userEvent.setup();
		hooks.accounts = { ...hooks.accounts, error: new Error("daemon unreachable") };
		render(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);

		expect(screen.getByRole("progressbar", { name: "Session (5h): 40% left" })).toBeInTheDocument();
		await user.hover(readout());
		expect(await screen.findByRole("tooltip")).toHaveTextContent("Couldn't refresh limits · Showing numbers from 1 minute ago");
	});

	it("keeps the daemon's last reading for an account it could not read, with the account's problem", async () => {
		const user = userEvent.setup();
		hooks.accounts = { data: [account("b", { problem: "Claude rate limited the usage read" })], isPending: false, error: null };
		render(<ComposerUsageLimits harness="claude-code" sessionId="s1" />);

		expect(screen.getByRole("progressbar", { name: "Weekly: 81% left" })).toBeInTheDocument();
		await user.hover(readout());
		const tooltip = await screen.findByRole("tooltip");
		expect(tooltip).toHaveTextContent("Claude rate limited the usage read · Showing numbers from 1 minute ago");
		// One account: no need to name it.
		expect(tooltip).not.toHaveTextContent("b@example.com");
	});
});

describe("sessionClaudeAccount", () => {
	const accounts = [
		account("codex-1", { provider: "codex", default: true }),
		account("a"),
		account("b", { default: true }),
	];

	it("picks the session's bound Claude account", () => {
		expect(sessionClaudeAccount(accounts, "a")?.id).toBe("a");
	});

	it("falls back to the default Claude login, never another provider's", () => {
		expect(sessionClaudeAccount(accounts, undefined)?.id).toBe("b");
		expect(sessionClaudeAccount(accounts, "codex-1")?.id).toBe("b");
		expect(sessionClaudeAccount([accounts[0]!], undefined)).toBeUndefined();
	});
});
