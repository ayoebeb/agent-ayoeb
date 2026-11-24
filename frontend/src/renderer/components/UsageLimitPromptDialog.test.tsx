import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { UsageLimitPromptDialog } from "./UsageLimitPromptDialog";

const { getMock, postMock, hostGetMock, hostPostMock, hostState } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	hostGetMock: vi.fn(),
	hostPostMock: vi.fn(),
	hostState: { hosts: [] as string[] },
}));

// A linked computer: its own client, name and connection.
vi.mock("../lib/host-clients", () => ({
	clientForHost: () => ({ GET: hostGetMock, POST: hostPostMock }),
	labelForHost: (hostId: string) => (hostId === "pc2" ? "NLSTATION78" : undefined),
}));
vi.mock("../hooks/useHostConnection", () => ({ useConnectedHosts: () => hostState.hosts }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	getApiBaseUrl: () => "http://127.0.0.1:3001",
	hasTrustedApiBaseUrl: () => true,
	subscribeApiBaseUrl: () => () => {},
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "Request failed",
}));

const prompt = {
	id: "p1",
	provider: "claude",
	windowLabel: "5-hour",
	accountId: "acct-1",
	accountEmail: "limited@example.com",
	resetsAt: "2026-11-23T18:00:00Z",
	createdAt: "2026-11-23T15:00:00Z",
	sessions: [
		{ sessionId: "orch", displayName: "Main orchestrator", kind: "orchestrator", projectId: "proj" },
		{ sessionId: "w1", displayName: "Fix login", kind: "worker", projectId: "proj" },
	],
	options: [
		{ accountId: "acct-light", email: "light@example.com", device: false, full: false, fiveHourUsedPercent: 10, weeklyUsedPercent: 20 },
		{ accountId: "acct-heavy", email: "heavy@example.com", device: false, full: false, fiveHourUsedPercent: 80, weeklyUsedPercent: 70 },
		{ accountId: "acct-full", email: "full@example.com", device: false, full: true, fiveHourUsedPercent: 100, weeklyUsedPercent: 100 },
	],
};

function renderDialog() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<QueryClientProvider client={queryClient}><UsageLimitPromptDialog /></QueryClientProvider>);
}

beforeEach(() => {
	getMock.mockReset();
	postMock.mockReset();
	hostGetMock.mockReset();
	hostPostMock.mockReset();
	hostState.hosts = [];
	getMock.mockResolvedValue({ data: { prompts: [prompt] } });
});

// One question for the whole limit event: the limited account, every stopped
// session, and the other accounts with their 5h and weekly usage.
it("asks once, naming the sessions and the accounts with their usage", async () => {
	renderDialog();
	await screen.findByTestId("usage-limit-prompt");
	const sessions = screen.getByTestId("usage-limit-prompt-sessions");
	expect(sessions.textContent).toContain("Main orchestrator");
	expect(sessions.textContent).toContain("Fix login");
	const radios = screen.getAllByRole("radio");
	expect(radios.map((radio) => radio.textContent)).toEqual(expect.arrayContaining([
		expect.stringContaining("light@example.com"),
		expect.stringContaining("heavy@example.com"),
		expect.stringContaining("full@example.com"),
	]));
	// The account with the most room is picked; one at its limit cannot be.
	expect(radios[0].getAttribute("aria-checked")).toBe("true");
	expect((radios[2] as HTMLButtonElement).disabled).toBe(true);
	expect(getMock).toHaveBeenCalledWith("/api/v1/agents/accounts/limit-prompts");
});

it("Switch sends the chosen account for the whole prompt", async () => {
	postMock.mockResolvedValue({ data: { ok: true, moved: ["orch", "w1"], failed: [] } });
	renderDialog();
	await screen.findByTestId("usage-limit-prompt");
	fireEvent.click(screen.getAllByRole("radio")[1]);
	fireEvent.click(screen.getByRole("button", { name: "Switch to heavy@example.com" }));

	await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
	expect(postMock).toHaveBeenCalledWith("/api/v1/agents/accounts/limit-prompts/{promptId}/switch", {
		params: { path: { promptId: "p1" } }, body: { accountId: "acct-heavy" },
	});
});

it("Wait answers the prompt without moving anything", async () => {
	postMock.mockResolvedValue({ data: { ok: true } });
	renderDialog();
	await screen.findByTestId("usage-limit-prompt");
	fireEvent.click(screen.getByRole("button", { name: "Wait for the reset" }));

	await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
	expect(postMock).toHaveBeenCalledWith("/api/v1/agents/accounts/limit-prompts/{promptId}/wait", { params: { path: { promptId: "p1" } } });
});

// Another computer's limit question is answered from this PC: the dialog names
// that computer, and Switch and Wait go to its daemon, never to this one.
const remotePrompt = { ...prompt, id: "r1", accountEmail: "o***@example.com", options: [{ ...prompt.options[0], accountId: "acct-remote", email: "l***@example.com" }] };

it("shows another computer's question with its name and answers it on that computer", async () => {
	hostState.hosts = ["pc2"];
	getMock.mockResolvedValue({ data: { prompts: [] } });
	hostGetMock.mockResolvedValue({ data: { prompts: [remotePrompt] } });
	hostPostMock.mockResolvedValue({ data: { ok: true, moved: ["orch"], failed: [] } });
	renderDialog();
	const dialog = await screen.findByTestId("usage-limit-prompt");
	expect(dialog.textContent).toContain("NLSTATION78: Claude Code 5-hour limit reached");
	expect(hostGetMock).toHaveBeenCalledWith("/api/v1/agents/accounts/limit-prompts");

	fireEvent.click(screen.getByRole("button", { name: "Switch to l***@example.com" }));
	await waitFor(() => expect(hostPostMock).toHaveBeenCalledTimes(1));
	expect(hostPostMock).toHaveBeenCalledWith("/api/v1/agents/accounts/limit-prompts/{promptId}/switch", {
		params: { path: { promptId: "r1" } }, body: { accountId: "acct-remote" },
	});
	expect(postMock).not.toHaveBeenCalled();
});

it("Wait on another computer's question goes to that computer", async () => {
	hostState.hosts = ["pc2"];
	getMock.mockResolvedValue({ data: { prompts: [] } });
	hostGetMock.mockResolvedValue({ data: { prompts: [remotePrompt] } });
	hostPostMock.mockResolvedValue({ data: { ok: true } });
	renderDialog();
	await screen.findByTestId("usage-limit-prompt");
	fireEvent.click(screen.getByRole("button", { name: "Wait for the reset" }));
	await waitFor(() => expect(hostPostMock).toHaveBeenCalledTimes(1));
	expect(hostPostMock).toHaveBeenCalledWith("/api/v1/agents/accounts/limit-prompts/{promptId}/wait", { params: { path: { promptId: "r1" } } });
	expect(postMock).not.toHaveBeenCalled();
});

// Prompt ids are only unique per computer: the same id on both PCs is two
// questions, shown one after the other, each answered by its own computer.
it("keeps this computer's and another's question apart even with the same id", async () => {
	hostState.hosts = ["pc2"];
	getMock.mockResolvedValue({ data: { prompts: [prompt] } });
	hostGetMock.mockResolvedValue({ data: { prompts: [{ ...remotePrompt, id: "p1" }] } });
	renderDialog();
	const first = await screen.findByTestId("usage-limit-prompt");
	// This computer's own question comes first, without a computer name.
	expect(first.textContent).not.toContain("NLSTATION78");
	fireEvent.click(screen.getByRole("button", { name: "Decide later" }));
	await waitFor(() => expect(screen.getByTestId("usage-limit-prompt").textContent).toContain("NLSTATION78"));
	expect(screen.getAllByTestId("usage-limit-prompt")).toHaveLength(1);
});

// One alert per question: a question answered on the other PC drops out of that
// computer's list on the next read, and the dialog goes with it.
it("closes when the question is answered on the other PC", async () => {
	hostState.hosts = ["pc2"];
	getMock.mockResolvedValue({ data: { prompts: [] } });
	hostGetMock.mockResolvedValue({ data: { prompts: [remotePrompt] } });
	hostPostMock.mockResolvedValue({ error: { status: 409, error: { code: "USAGE_LIMIT_PROMPT_ANSWERED", message: "This question was already answered" } } });
	renderDialog();
	await screen.findByTestId("usage-limit-prompt");
	hostGetMock.mockResolvedValue({ data: { prompts: [] } });
	fireEvent.click(screen.getByRole("button", { name: "Wait for the reset" }));
	await waitFor(() => expect(screen.queryByTestId("usage-limit-prompt")).toBeNull());
});

it("shows nothing from a computer whose read failed", async () => {
	hostState.hosts = ["pc2"];
	getMock.mockResolvedValue({ data: { prompts: [] } });
	hostGetMock.mockResolvedValue({ error: { message: "offline" } });
	renderDialog();
	await waitFor(() => expect(hostGetMock).toHaveBeenCalled());
	expect(screen.queryByTestId("usage-limit-prompt")).toBeNull();
});

it("shows nothing when no question is open", async () => {
	getMock.mockResolvedValue({ data: { prompts: [] } });
	renderDialog();
	await waitFor(() => expect(getMock).toHaveBeenCalled());
	expect(screen.queryByTestId("usage-limit-prompt")).toBeNull();
});
