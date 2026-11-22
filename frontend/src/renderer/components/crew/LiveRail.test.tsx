import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../../stores/ui-store";
import type { WorkspaceSession } from "../../types/workspace";

const { live, stop, restart, openLink, openShell, navigate, terminalProps } = vi.hoisted(() => ({
	live: { servers: [] as Record<string, unknown>[], isError: false, isLoading: false },
	stop: { mutate: vi.fn(), isPending: false, isError: false },
	restart: { mutate: vi.fn(), isPending: false, isError: false },
	openLink: vi.fn(),
	openShell: vi.fn(),
	navigate: vi.fn(),
	terminalProps: { current: undefined as unknown },
}));

vi.mock("../../hooks/useLiveServers", () => ({
	useLiveServers: () => live,
	useLiveServerActions: () => ({ stop, restart }),
	liveServerLabel: (server: { name: string; port: number }) => `${server.name} :${server.port}`,
}));
vi.mock("../../hooks/useSessionBrowserLink", () => ({ useSessionBrowserLink: () => openLink }));
vi.mock("../../hooks/useShellTerminals", () => ({ useOpenShellTerminal: () => ({ open: openShell }) }));
vi.mock("../../hooks/useHostConnection", () => ({ useHostConnection: () => ({ baseUrl: undefined }) }));
vi.mock("../../lib/shell-context", () => ({ useShell: () => ({ daemonStatus: { state: "ready" } }) }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("../TerminalPane", () => ({
	TerminalPane: (props: unknown) => {
		terminalProps.current = props;
		return <div data-testid="terminal-pane" />;
	},
}));

import { TooltipProvider } from "../ui/tooltip";
import { LiveRail } from "./LiveRail";

const orchestrator = { id: "p-orchestrator", workspaceId: "p", kind: "orchestrator", mode: "chat" } as WorkspaceSession;

const managed = {
	key: "managed:w1", source: "managed", name: "vite", port: 5173, url: "http://127.0.0.1:5173/", command: "npm run dev",
	sessionId: "w1", sessionName: "Fix login", state: "ready", health: "up", healthMs: 12, terminalId: "term-1",
	startedAt: new Date(Date.now() - 5 * 60_000).toISOString(), logs: ["ready"], outputAvailable: true, canRestart: true,
};
const detected = {
	key: "detected:3000", source: "detected", name: "node", port: 3000, url: "http://127.0.0.1:3000/", command: "node.exe",
	sessionId: "w2", sessionName: "Docs", state: "ready", health: "down", healthMs: 3000, healthNote: "no response within the time limit",
	workingDir: "C:/w2", logs: [], outputAvailable: false, canRestart: false,
};

function renderRail() {
	const wrapper = ({ children }: { children: ReactNode }) => <TooltipProvider>{children}</TooltipProvider>;
	return render(<LiveRail active orchestrator={orchestrator} />, { wrapper });
}

beforeEach(() => {
	vi.clearAllMocks();
	live.servers = [];
	live.isError = false;
	live.isLoading = false;
	stop.isError = false;
	useUiStore.setState({ inspectorSessions: {} });
});

describe("LiveRail", () => {
	it("says so when nothing is running", () => {
		renderRail();
		expect(screen.getByTestId("live-rail-empty")).toHaveTextContent("No dev servers are running for this project.");
	});

	it("shows an AO-started server with its facts, a typable terminal, and Open / Restart / Stop", async () => {
		live.servers = [managed];
		renderRail();
		expect(screen.getByTestId("live-health-text")).toHaveTextContent("Answering");
		const facts = screen.getByTestId("live-server-facts");
		expect(facts).toHaveTextContent("npm run dev");
		expect(facts).toHaveTextContent("5173");
		expect(facts).toHaveTextContent("http://127.0.0.1:5173/");
		expect(facts).toHaveTextContent("Fix login");
		expect(facts).toHaveTextContent("5m ago");
		expect(screen.getByTestId("terminal-pane")).toBeInTheDocument();
		expect(terminalProps.current).toMatchObject({
			terminalTarget: { kind: "shell", handleId: "term-1", sessionId: "p-orchestrator" },
			daemonReady: true,
		});

		await userEvent.click(screen.getByRole("button", { name: "Open" }));
		expect(openLink).toHaveBeenCalledWith("http://127.0.0.1:5173/");
		await userEvent.click(screen.getByRole("button", { name: "Restart" }));
		expect(restart.mutate).toHaveBeenCalledWith("managed:w1");
		await userEvent.click(screen.getByRole("button", { name: "Stop" }));
		expect(stop.mutate).toHaveBeenCalledWith("managed:w1");
	});

	it("shows a server a worker started itself without output or Restart, and opens a terminal in its folder", async () => {
		live.servers = [detected];
		renderRail();
		expect(screen.getByTestId("live-health-text")).toHaveTextContent("Not answering · no response within the time limit");
		expect(screen.queryByTestId("terminal-pane")).toBeNull();
		expect(screen.queryByRole("button", { name: "Restart" })).toBeNull();
		expect(screen.getByRole("button", { name: "Stop" })).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Open terminal in this folder" }));
		expect(openShell).toHaveBeenCalledWith({ projectId: "p", sessionId: "w2" });
		expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ params: expect.objectContaining({ sessionId: "w2" }) }));
	});

	it("follows the server picked in the store and switches on tab click", async () => {
		live.servers = [managed, detected];
		useUiStore.setState({ inspectorSessions: { "p-orchestrator": { isOpen: true, view: "live", liveServerKey: "detected:3000" } } });
		renderRail();
		expect(screen.getByTestId("live-server-detected")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("tab", { name: /vite :5173/ }));
		expect(useUiStore.getState().inspectorSessions["p-orchestrator"]?.liveServerKey).toBe("managed:w1");
	});

	it("tells the person when stop or restart failed", () => {
		live.servers = [managed];
		stop.isError = true;
		renderRail();
		expect(screen.getByRole("alert")).toHaveTextContent("That did not work");
	});
});
