import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CommandRunStreamEvent, TerminalShellCatalog } from "../../lib/command-runs";

type Emit = (event: CommandRunStreamEvent) => void;
const daemon = vi.hoisted(() => ({
	runs: [] as Array<{ emit: Emit; finish: () => void; request: { command: string; shell: string }; target: unknown }>,
	stop: vi.fn(async () => undefined),
}));
vi.mock("../../lib/command-runs", async (importOriginal) => ({
	...(await importOriginal<typeof import("../../lib/command-runs")>()),
	fetchTerminalShells: async (): Promise<TerminalShellCatalog> => ({
		default: { id: "auto", name: "PowerShell", path: "C:\\pwsh.exe" },
		shells: [
			{ id: "pwsh", name: "PowerShell", path: "C:\\pwsh.exe" },
			{ id: "cmd", name: "Command Prompt", path: "C:\\cmd.exe" },
			{ id: "git-bash", name: "Git Bash", path: "C:\\Git\\bin\\bash.exe" },
		],
	}),
	streamCommandRun: (target: unknown, request: { command: string; shell: string }, signal: AbortSignal, onEvent: Emit) =>
		new Promise<void>((resolve, reject) => {
			signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
			daemon.runs.push({ emit: onEvent, finish: resolve, request, target });
		}),
	stopCommandRun: daemon.stop,
}));

import { TooltipProvider } from "../ui/tooltip";
import { useCommandRunStore } from "../../stores/command-run-store";
import { ChatLinkProvider, ChatMarkdown, UserMarkdown } from "./ChatMarkdown";
import { CodeRunScope } from "./CodeBlockRun";

const POWERSHELL_BLOCK = "Run this:\n\n```powershell\nGet-Date\n```\n";

function renderChat(content: ReactNode, provider: { sessionId?: string; sessionLinkSourceKind?: "cloud"; sessionLinkHostId?: string } = { sessionId: "s1" }) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<TooltipProvider>
				<ChatLinkProvider {...provider}>{content}</ChatLinkProvider>
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	daemon.runs = [];
	daemon.stop.mockClear();
	useCommandRunStore.setState({ runs: {}, shells: {} });
});

describe("Run on chat code blocks", () => {
	it("offers Run on an agent's shell command block", async () => {
		renderChat(<ChatMarkdown text={POWERSHELL_BLOCK} />);
		expect(await screen.findByTestId("command-run-start")).toHaveTextContent("Run");
		await waitFor(() => expect(screen.getByRole("button", { name: "Shell: PowerShell" })).toBeInTheDocument());
	});

	it.each([
		["a JSON block", <ChatMarkdown key="json" text={"```json\n{\"a\": 1}\n```"} />, { sessionId: "s1" }],
		["a TypeScript block", <ChatMarkdown key="ts" text={"```ts\nconst a = 1;\n```"} />, { sessionId: "s1" }],
		["prose in a text block", <ChatMarkdown key="prose" text={"```text\nThe build failed because the cache was stale.\n```"} />, { sessionId: "s1" }],
		["a block still streaming", <ChatMarkdown key="stream" text={"```powershell\nGet-Date"} streaming />, { sessionId: "s1" }],
		["third-party content", <ChatMarkdown key="safe" text={POWERSHELL_BLOCK} safeOrigin />, { sessionId: "s1" }],
		["the person's own message", <UserMarkdown key="user" text={POWERSHELL_BLOCK} />, { sessionId: "s1" }],
		["a chat without a session", <ChatMarkdown key="nosession" text={POWERSHELL_BLOCK} />, {}],
		["a Cloud session", <ChatMarkdown key="cloud" text={POWERSHELL_BLOCK} />, { sessionId: "s1", sessionLinkSourceKind: "cloud" as const }],
	])("does not offer Run for %s", (_name, content, provider) => {
		renderChat(content, provider);
		expect(screen.queryByTestId("command-run-start")).toBeNull();
	});

	it("runs on click and shows the exact command, output and exit code under the block", async () => {
		const user = userEvent.setup();
		renderChat(<CodeRunScope.Provider value="message-1"><ChatMarkdown text={POWERSHELL_BLOCK} /></CodeRunScope.Provider>);
		await waitFor(() => expect(screen.getByRole("button", { name: "Shell: PowerShell" })).toBeInTheDocument());
		expect(daemon.runs).toHaveLength(0);
		await user.click(screen.getByTestId("command-run-start"));
		expect(daemon.runs).toHaveLength(1);
		expect(daemon.runs[0].request).toEqual({ command: "Get-Date", shell: "pwsh" });
		expect(daemon.runs[0].target).toEqual({ sessionId: "s1", hostId: undefined });

		const live = daemon.runs[0];
		act(() => {
			live.emit({ started: { runId: "cmdrun-1", shell: "PowerShell", shellPath: "C:\\pwsh.exe", workingDir: "C:\\repo", startedAt: "2026-11-12T10:00:00Z" } });
			live.emit({ output: "\u001b[32mWednesday, 12 November 2026\u001b[0m\r\n" });
		});
		const panel = screen.getByTestId("command-run-output");
		expect(within(panel).getByText("Running")).toBeInTheDocument();
		expect(screen.getByTestId("command-run-stop")).toBeInTheDocument();
		act(() => {
			live.emit({ exit: { exitCode: 0, durationMs: 840 } });
			live.finish();
		});
		await waitFor(() => expect(within(panel).getByText("Exit 0")).toBeInTheDocument());
		expect(within(panel).getByText("PowerShell in C:\\repo")).toBeInTheDocument();
		expect(screen.getByTestId("command-run-command")).toHaveTextContent("> Get-Date");
		// Colour codes are stripped, the text stays.
		expect(screen.getByTestId("command-run-output-text").textContent).toContain("Wednesday, 12 November 2026");
		expect(screen.getByTestId("command-run-output-text").textContent).not.toContain("\u001b");
		expect(screen.getByTestId("command-run-start")).toBeInTheDocument();
	});

	it("runs with the shell the reader picks", async () => {
		const user = userEvent.setup();
		renderChat(<ChatMarkdown text={POWERSHELL_BLOCK} />);
		const picker = await screen.findByRole("button", { name: "Shell: PowerShell" });
		await user.click(picker);
		await user.click(await screen.findByRole("menuitemradio", { name: "Command Prompt" }));
		await user.click(screen.getByTestId("command-run-start"));
		expect(daemon.runs[0].request).toEqual({ command: "Get-Date", shell: "cmd" });
	});

	it("stops a running command", async () => {
		const user = userEvent.setup();
		renderChat(<ChatMarkdown text={"```bash\nsleep 100\n```"} />);
		await user.click(await screen.findByTestId("command-run-start"));
		act(() => daemon.runs[0].emit({ started: { runId: "cmdrun-7", shell: "Git Bash", shellPath: "bash.exe", workingDir: "C:\\repo", startedAt: "2026-11-12T10:00:00Z" } }));
		await user.click(screen.getByTestId("command-run-stop"));
		expect(daemon.stop).toHaveBeenCalledWith({ sessionId: "s1", hostId: undefined }, "cmdrun-7");
		act(() => {
			daemon.runs[0].emit({ exit: { exitCode: -1, durationMs: 3000, stopped: true } });
			daemon.runs[0].finish();
		});
		await waitFor(() => expect(within(screen.getByTestId("command-run-output")).getByText("Stopped")).toBeInTheDocument());
	});

	it("tells the reader a local run needs the desktop app", async () => {
		const user = userEvent.setup();
		renderChat(<ChatMarkdown text={POWERSHELL_BLOCK} />);
		await user.click(await screen.findByTestId("command-run-start"));
		// The store marks a run desktop-only when the API layer has no key
		// (command-run-store.test.ts); here only its rendering is under test.
		act(() => {
			useCommandRunStore.setState((state) => ({
				runs: Object.fromEntries(Object.entries(state.runs).map(([key, run]) => [key, { ...run, status: "error" as const, desktopOnly: true }])),
			}));
		});
		expect(await screen.findByText("Run works in the desktop app only")).toBeInTheDocument();
	});
});
