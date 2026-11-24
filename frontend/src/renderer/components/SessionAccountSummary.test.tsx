import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import { SessionAccountSummary } from "./SessionAccountSummary";

const { getMock, putMock, hostCalls, hostState } = vi.hoisted(() => ({
	getMock: vi.fn(),
	putMock: vi.fn(),
	hostCalls: [] as Array<string | undefined>,
	hostState: { connected: true },
}));

vi.mock("../lib/preview-mode", () => ({ usesPreviewWorkspaceData: false }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: vi.fn() },
	getApiBaseUrl: () => "http://127.0.0.1:3001",
	// Trusted, so a local session's move status is read; the cloud one must not be.
	hasTrustedApiBaseUrl: () => true,
	subscribeApiBaseUrl: () => () => {},
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "Request failed",
}));

vi.mock("../lib/host-clients", async (importOriginal) => ({
	...await importOriginal<typeof import("../lib/host-clients")>(),
	clientForSessionHost: (hostId?: string) => {
		hostCalls.push(hostId);
		return { GET: getMock, PUT: putMock };
	},
}));

vi.mock("../hooks/useHostConnection", () => ({
	useHostConnection: (hostId?: string) => ({ hostId, isRemote: Boolean(hostId), baseUrl: hostId && hostState.connected ? "http://10.0.0.5:3002" : undefined }),
}));

vi.mock("../hooks/useCloudCp", async (importOriginal) => ({
	...await importOriginal<typeof import("../hooks/useCloudCp")>(),
	useCloudCp: () => ({ client: {}, ready: false, baseUrl: "" }),
}));

const session = (id: string, overrides: Partial<WorkspaceSession> = {}) =>
	({ id, provider: "claude-code", isTerminated: false, ...overrides }) as WorkspaceSession;

const sessionIdOf = (options: unknown) => (options as { params?: { path?: { sessionId?: string } } } | undefined)?.params?.path?.sessionId;

// A cloud session has no row in the local daemon: its summary is hidden and
// asks the local daemon for neither its account nor a move.
it("asks the local daemon about a local session's account, never a cloud session's", async () => {
	getMock.mockImplementation((path: string) => Promise.resolve({ data: path === "/api/v1/agents/accounts" ? { accounts: [] } : {} }));
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(<QueryClientProvider client={queryClient}>
		<SessionAccountSummary session={session("s-cloud", { cloud: { orgId: "org-1" } })} />
		<SessionAccountSummary session={session("s-local")} />
	</QueryClientProvider>);

	await waitFor(() => {
		const paths = getMock.mock.calls.filter(([, options]) => sessionIdOf(options) === "s-local").map(([path]) => path);
		expect(paths).toEqual(expect.arrayContaining(["/api/v1/agents/accounts/sessions/{sessionId}", "/api/v1/sessions/{sessionId}/interface-transition"]));
	});
	expect(getMock.mock.calls.filter(([, options]) => sessionIdOf(options) === "s-cloud")).toEqual([]);
});

const remoteAccounts = [
	{ id: "acct-1", provider: "claude", device: false, email: "o***@example.com", default: true, signedIn: true, loggedIn: true },
	{ id: "acct-2", provider: "claude", device: false, email: "w***@example.com", default: false, signedIn: true, loggedIn: true },
];

// The remote computer shares its accounts (masked) and takes the move itself:
// every account call of a remote session names that computer, never this one.
describe("a session on another computer", () => {
	beforeEach(() => {
		getMock.mockReset();
		putMock.mockReset();
		hostCalls.length = 0;
		hostState.connected = true;
		getMock.mockImplementation((path: string) => Promise.resolve({
			data: path === "/api/v1/agents/accounts" ? { accounts: remoteAccounts }
				: path === "/api/v1/agents/accounts/sessions/{sessionId}" ? { claude: "acct-1" } : {},
		}));
	});

	const renderRemote = () => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
		<SessionAccountSummary hostId="host-b" session={session("s-remote")} />
	</QueryClientProvider>);

	it("shows the host's account and switches it on that host, including the moved workers", async () => {
		putMock.mockResolvedValue({ data: { ok: true, sessionId: "s-remote", movedWorkers: 2, workers: [{ sessionId: "w-1", status: "moving" }, { sessionId: "w-2", status: "moving" }] } });
		renderRemote();

		const picker = await screen.findByRole("combobox", { name: "Account" });
		await waitFor(() => expect(picker).toHaveTextContent("o***@example.com"));
		await userEvent.click(picker);
		await userEvent.click(await screen.findByRole("option", { name: "w***@example.com" }));

		await waitFor(() => expect(putMock).toHaveBeenCalledWith("/api/v1/agents/accounts/sessions/{sessionId}", expect.objectContaining({
			params: { path: { sessionId: "s-remote" } }, body: { accountId: "acct-2" },
		})));
		expect(await screen.findByTestId("session-account-moved-workers")).toHaveTextContent("Also moved 2 workers");
		expect(hostCalls.length).toBeGreaterThan(0);
		expect(hostCalls.every((hostId) => hostId === "host-b")).toBe(true);
	});

	it("shows nothing while the computer is not connected, and when it does not share its accounts", async () => {
		hostState.connected = false;
		const { container, unmount } = renderRemote();
		expect(container).toBeEmptyDOMElement();
		expect(getMock).not.toHaveBeenCalled();
		unmount();

		hostState.connected = true;
		getMock.mockImplementation(() => Promise.resolve({ error: { code: "NOT_FOUND", message: "not found" } }));
		const old = renderRemote();
		await waitFor(() => expect(getMock).toHaveBeenCalled());
		expect(old.container).toBeEmptyDOMElement();
	});
});
