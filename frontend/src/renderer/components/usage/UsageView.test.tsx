import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RemoteHost } from "../../hooks/useRemoteHosts";
import { ShellProvider, type ShellContextValue } from "../../lib/shell-context";
import { TooltipProvider } from "../ui/tooltip";
import { UsageView } from "./UsageView";

// The Usage page reads this computer's daemon, or a linked computer's own
// daemon through the host connection, depending on the computer chosen.
const mocks = vi.hoisted(() => ({
	localGet: vi.fn(),
	hostGet: vi.fn(),
	connection: { base: "http://studio:3011", label: "Studio PC" } as { base: string; label: string } | undefined,
}));
vi.mock("../../lib/api-client", async (original) => ({
	...await original<typeof import("../../lib/api-client")>(),
	apiClient: { GET: mocks.localGet },
}));
vi.mock("../../lib/host-clients", () => ({
	clientForHost: () => ({ GET: mocks.hostGet }),
	clientForSessionHost: (hostId?: string) => (hostId ? { GET: mocks.hostGet } : { GET: mocks.localGet }),
	connectedHost: () => mocks.connection,
	connectedHosts: () => [],
	subscribeConnectedHosts: () => () => {},
}));
vi.mock("../../hooks/useWorkspaceQuery", () => ({ useWorkspaceQuery: () => ({ data: [] }) }));

const REPORT = { since: "2026-11-13", until: "2026-11-20", rows: [] };

function host(status: RemoteHost["status"], over: Partial<RemoteHost> = {}): RemoteHost {
	return { hostId: "studio", label: "Studio PC", url: "http://studio:3011", status, ...over };
}

function renderUsage(remoteHosts: RemoteHost[]) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const shell = { remoteHosts } as ShellContextValue;
	return render(
		<QueryClientProvider client={client}>
			<TooltipProvider>
				<ShellProvider value={shell}>
					<UsageView />
				</ShellProvider>
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

async function chooseStudio(user: ReturnType<typeof userEvent.setup>) {
	await user.click(screen.getByRole("button", { name: "Computer" }));
	await user.click(await screen.findByRole("menuitem", { name: "Studio PC" }));
}

beforeEach(() => {
	mocks.localGet.mockReset().mockResolvedValue({ data: REPORT });
	mocks.hostGet.mockReset().mockImplementation(async (path: string) => {
		if (path === "/api/v1/usage/account-limits") {
			return {
				data: {
					accounts: [{
						id: "studio-claude", provider: "claude", email: "studio@example.com", default: true, device: true, loggedIn: true, signedIn: true,
						limits: {
							fetchedAt: new Date().toISOString(),
							windows: [{ id: "five_hour", label: "Session (5h)", usedPercent: 37, durationMinutes: 300, resetsAt: new Date(Date.now() + 3_600_000).toISOString() }],
						},
					}],
				},
			};
		}
		return { data: REPORT };
	});
	mocks.connection = { base: "http://studio:3011", label: "Studio PC" };
});

describe("Usage page computer choice", () => {
	it("offers no choice when no other computer is linked", async () => {
		renderUsage([]);

		await waitFor(() => expect(mocks.localGet).toHaveBeenCalledWith("/api/v1/usage/report", expect.anything()));
		expect(screen.queryByRole("button", { name: "Computer" })).not.toBeInTheDocument();
	});

	it("lists this computer and each linked computer by name, starting on this one", async () => {
		const user = userEvent.setup();
		renderUsage([host("connected")]);

		const trigger = screen.getByRole("button", { name: "Computer" });
		expect(trigger).toHaveTextContent("This computer");
		await user.click(trigger);
		expect(await screen.findByRole("menuitem", { name: "This computer" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Studio PC" })).toBeInTheDocument();
		expect(mocks.hostGet).not.toHaveBeenCalled();
	});

	it("reads the chosen computer's report from its own daemon", async () => {
		const user = userEvent.setup();
		renderUsage([host("connected")]);
		await waitFor(() => expect(mocks.localGet).toHaveBeenCalledTimes(1));

		await chooseStudio(user);

		await waitFor(() => expect(mocks.hostGet).toHaveBeenCalledWith("/api/v1/usage/report", expect.anything()));
		expect(screen.getByRole("button", { name: "Computer" })).toHaveTextContent("Studio PC");
		expect(mocks.localGet).toHaveBeenCalledTimes(1);
	});

	it("shows the chosen computer's limits on the Limits tab, not this computer's accounts", async () => {
		const user = userEvent.setup();
		renderUsage([host("connected")]);
		await chooseStudio(user);
		await user.click(screen.getByRole("tab", { name: "Limits" }));

		await waitFor(() => expect(mocks.hostGet).toHaveBeenCalledWith("/api/v1/usage/account-limits", { params: { query: {} } }));
		expect(await screen.findByText("studio@example.com")).toBeInTheDocument();
		expect(screen.getByText(/Studio PC's login/)).toBeInTheDocument();
		expect(mocks.localGet).not.toHaveBeenCalledWith("/api/v1/agents/accounts", expect.anything());
	});

	it("keeps the offline state for a computer that is not reachable and reads nothing from it", async () => {
		const user = userEvent.setup();
		renderUsage([host("offline")]);

		await chooseStudio(user);

		expect(await screen.findByText("Studio PC is not reachable")).toBeInTheDocument();
		expect(mocks.hostGet).not.toHaveBeenCalled();
	});

	it("does not show another computer's numbers once its read fails", async () => {
		const user = userEvent.setup();
		mocks.hostGet.mockResolvedValue({ error: { message: "Studio exploded" } });
		renderUsage([host("connected")]);

		await chooseStudio(user);

		// The hook retries a failed read once before giving up.
		expect(await screen.findByRole("alert", {}, { timeout: 5000 })).toHaveTextContent("Studio exploded");
		expect(screen.queryByText(/Studio PC is not reachable/)).not.toBeInTheDocument();
	});
});
