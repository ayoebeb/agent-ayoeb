import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentAccount } from "../../hooks/useAgentAccounts";
import { ComposerUsageLimits } from "./ComposerUsageLimits";

// A session on another computer reads that computer's daemon through the host
// connection, never this computer's accounts.
const mocks = vi.hoisted(() => ({
	hostGet: vi.fn(),
	localGet: vi.fn(),
	connection: { base: "http://other-pc:3011", label: "Studio PC" } as { base: string; label: string } | undefined,
}));
vi.mock("../../lib/host-clients", () => ({
	clientForHost: () => ({ GET: mocks.hostGet }),
	clientForSessionHost: () => ({ GET: mocks.hostGet }),
	connectedHost: () => mocks.connection,
	connectedHosts: () => [],
	subscribeConnectedHosts: () => () => {},
}));
vi.mock("../../lib/api-client", async (original) => ({
	...await original<typeof import("../../lib/api-client")>(),
	apiClient: { GET: mocks.localGet },
}));

const MINUTE = 60_000;

function remoteAccount(): AgentAccount {
	return {
		id: "studio-claude",
		provider: "claude",
		email: "studio@example.com",
		default: true,
		device: true,
		loggedIn: true,
		signedIn: true,
		limits: {
			fetchedAt: new Date(Date.now() - MINUTE).toISOString(),
			windows: [
				{ id: "five_hour", label: "Session (5h)", usedPercent: 37, durationMinutes: 300, resetsAt: new Date(Date.now() + 3_600_000).toISOString() },
				{ id: "seven_day", label: "Weekly", usedPercent: 38, durationMinutes: 10_080, resetsAt: new Date(Date.now() + 86_400_000).toISOString() },
			],
		},
	};
}

function renderComposer() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<ComposerUsageLimits harness="claude-code" sessionId="s-remote" hostId="studio" />
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	mocks.hostGet.mockReset().mockResolvedValue({ data: { accounts: [remoteAccount()], session: { claude: "studio-claude" } } });
	mocks.localGet.mockReset();
	mocks.connection = { base: "http://other-pc:3011", label: "Studio PC" };
});

describe("ComposerUsageLimits on a remote session", () => {
	it("shows the limits of the computer the session runs on, read from that computer's daemon", async () => {
		const user = userEvent.setup();
		renderComposer();

		expect(await screen.findByRole("progressbar", { name: "Session (5h): 63% left" })).toHaveAttribute("aria-valuenow", "63");
		expect(screen.getByRole("progressbar", { name: "Weekly: 62% left" })).toBeInTheDocument();
		expect(mocks.hostGet).toHaveBeenCalledWith("/api/v1/usage/account-limits", { params: { query: { sessionId: "s-remote" } } });
		// Nothing was read from this computer's own accounts.
		expect(mocks.localGet).not.toHaveBeenCalled();

		await user.hover(screen.getByRole("group", { name: "Claude Code limits" }));
		expect(await screen.findByRole("tooltip")).toHaveTextContent("On Studio PC");
	});

	it("shows nothing while the computer is not connected", async () => {
		mocks.connection = undefined;
		const { container } = renderComposer();

		await Promise.resolve();
		expect(container).toBeEmptyDOMElement();
		expect(mocks.hostGet).not.toHaveBeenCalled();
	});

	it("shows no numbers when the computer's read fails, instead of an old reading", async () => {
		mocks.hostGet.mockResolvedValue({ error: { message: "boom" } });
		const { container } = renderComposer();

		await waitFor(() => expect(mocks.hostGet).toHaveBeenCalled());
		// The hook retries a failed read once before giving up.
		await waitFor(() => expect(container).toBeEmptyDOMElement(), { timeout: 5000 });
		expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
	});

	it("does not ask a computer for limits when the session is not Claude Code", () => {
		const client = new QueryClient();
		const { container } = render(
			<QueryClientProvider client={client}>
				<ComposerUsageLimits harness="codex" sessionId="s-remote" hostId="studio" />
			</QueryClientProvider>,
		);

		expect(container).toBeEmptyDOMElement();
		expect(mocks.hostGet).not.toHaveBeenCalled();
	});
});
