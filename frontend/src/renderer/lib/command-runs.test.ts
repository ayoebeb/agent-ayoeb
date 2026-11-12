import { beforeEach, describe, expect, it, vi } from "vitest";

const host = vi.hoisted(() => ({
	post: vi.fn(),
	del: vi.fn(),
	clientFor: vi.fn(),
	key: vi.fn(async () => "local-key"),
}));
vi.mock("./host-clients", () => ({
	clientForSessionHost: (hostId?: string) => {
		host.clientFor(hostId);
		return { POST: host.post, DELETE: host.del, GET: vi.fn() };
	},
}));
vi.mock("./bridge", () => ({ aoBridge: { commandRuns: { key: host.key } } }));

import {
	CommandRunUnavailableError,
	defaultRunShell,
	isCommandLine,
	runnableCommand,
	runShellOptions,
	stopCommandRun,
	streamCommandRun,
	type TerminalShellCatalog,
} from "./command-runs";

function sseStream(events: unknown[]): ReadableStream<Uint8Array> {
	const body = events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join("");
	return new ReadableStream({
		start(controller) {
			// Split mid-frame to prove frames are reassembled.
			const bytes = new TextEncoder().encode(body);
			controller.enqueue(bytes.slice(0, 7));
			controller.enqueue(bytes.slice(7));
			controller.close();
		},
	});
}

describe("runnableCommand: which blocks get Run", () => {
	it.each([
		["powershell", "powershell -ExecutionPolicy Bypass -File C:\\scripts\\setup.ps1", "powershell"],
		["pwsh", "Get-Date", "powershell"],
		["ps1", "Get-ChildItem", "powershell"],
		["bash", "npm install", "posix"],
		["sh", "./configure", "posix"],
		["zsh", "brew install gh", "posix"],
		["shell", "git status", "posix"],
		["cmd", "dir", "cmd"],
		["bat", "echo hi", "cmd"],
	])("runs a %s block", (language, code, kind) => {
		expect(runnableCommand(code, language)).toMatchObject({ command: code, kind });
	});

	it.each([
		["text", "claude mcp add --transport http yoobi-edb http://localhost:8090/mcp/e-dynamicsbv"],
		[undefined, "npm run build"],
		["", "git pull\nnpm ci\nnpm test"],
		["text", "Get-Process | Sort-Object CPU"],
		["text", "& \"C:\\Program Files\\Tool\\tool.exe\" --version"],
		["text", ".\\setup.ps1 -Force"],
		["txt", "FOO=1 npm test"],
	])("runs an unlabelled %s block that is a command: %s", (language, code) => {
		expect(runnableCommand(code, language)?.command).toBe(code);
	});

	it.each([
		["json", "{\"command\": \"npm test\"}"],
		["yaml", "steps:\n  - run: npm test"],
		["ts", "const x = await run('npm test');"],
		["go", "fmt.Println(\"hi\")"],
		["diff", "+ npm test\n- npm run build"],
		["python", "print('hello')"],
		["mermaid", "graph TD; A-->B"],
		["text", "The build failed because the cache was stale."],
		["text", "Open the settings page and check the output."],
		["text", "type: string\nrequired: true"],
		["text", "https://example.com/docs"],
		["text", "C:\\Users\\me\\project\\setup.ps1"],
		["text", "- install the app\n- restart"],
		["text", "npm install\nnpm test\nnpm run lint\nnpm run build\nnpm run e2e\nnpm run deploy"],
		["bash", "   \n"],
		["bash", Array.from({ length: 13 }, (_, i) => `echo ${i}`).join("\n")],
	])("does not run a %s block: %s", (language, code) => {
		expect(runnableCommand(code, language)).toBeNull();
	});

	it("strips a prompt only when every command line carries one", () => {
		expect(runnableCommand("$ npm install\n$ npm test", "bash")?.command).toBe("npm install\nnpm test");
		expect(runnableCommand("echo '$ not a prompt'", "bash")?.command).toBe("echo '$ not a prompt'");
	});

	it("runs only the prompted lines of a console transcript", () => {
		expect(runnableCommand("$ git --version\ngit version 2.45.0", "console")).toMatchObject({ command: "git --version", kind: "posix" });
		expect(runnableCommand("PS C:\\repo> Get-Date\nWednesday", "console")).toMatchObject({ command: "Get-Date", kind: "powershell" });
	});

	it("treats a transcript without prompts like an unlabelled block", () => {
		// Pasted output must never run as commands.
		for (const language of ["console", "terminal", "shellsession"]) {
			expect(runnableCommand("added 312 packages in 4s\nError: Cannot find module 'x'", language)).toBeNull();
		}
		expect(runnableCommand("npm ci\nnpm test", "console")).toMatchObject({ command: "npm ci\nnpm test" });
	});

	it("treats PowerShell-looking unlabelled blocks as PowerShell", () => {
		expect(runnableCommand("npm test", "text")?.kind).toBe(undefined);
		expect(runnableCommand("Get-Date", "text")?.kind).toBe("powershell");
		expect(runnableCommand("pwsh -NoProfile -File build.ps1", "text")?.kind).toBe("powershell");
		expect(isCommandLine("Set-Location C:\\repo")).toBe(true);
	});
});

const windowsCatalog: TerminalShellCatalog = {
	default: { id: "auto", name: "PowerShell", path: "C:\\pwsh.exe" },
	elevation: { available: false },
	shells: [
		{ id: "pwsh", name: "PowerShell", path: "C:\\pwsh.exe" },
		{ id: "powershell", name: "Windows PowerShell", path: "C:\\powershell.exe" },
		{ id: "cmd", name: "Command Prompt", path: "C:\\cmd.exe" },
		{ id: "git-bash", name: "Git Bash", path: "C:\\Git\\bin\\bash.exe" },
		{ id: "wsl", name: "WSL", path: "C:\\wsl.exe" },
	],
};
const macCatalog: TerminalShellCatalog = {
	default: { id: "auto", name: "zsh", path: "/bin/zsh" },
	elevation: { available: false },
	shells: [
		{ id: "/bin/bash", name: "bash", path: "/bin/bash" },
		{ id: "/bin/zsh", name: "zsh", path: "/bin/zsh" },
		{ id: "/opt/homebrew/bin/fish", name: "fish", path: "/opt/homebrew/bin/fish" },
	],
};

describe("defaultRunShell: which shell a block runs with", () => {
	it("follows the block's language on Windows", () => {
		expect(defaultRunShell({ command: "Get-Date", kind: "powershell" }, windowsCatalog, "cmd")).toBe("pwsh");
		expect(defaultRunShell({ command: "Get-Date", kind: "powershell" }, windowsCatalog, "powershell")).toBe("powershell");
		expect(defaultRunShell({ command: "ls", kind: "posix" }, windowsCatalog, "auto")).toBe("git-bash");
		expect(defaultRunShell({ command: "dir", kind: "cmd" }, windowsCatalog, "pwsh")).toBe("cmd");
	});

	it("uses the terminal shell setting for unlabelled blocks", () => {
		expect(defaultRunShell({ command: "npm test" }, windowsCatalog, "git-bash")).toBe("git-bash");
		expect(defaultRunShell({ command: "npm test" }, windowsCatalog, "auto")).toBe("auto");
		// WSL cannot run a command non-interactively, so it falls back to the default.
		expect(defaultRunShell({ command: "npm test" }, windowsCatalog, "wsl")).toBe("auto");
	});

	it("picks the named shell on macOS and Linux", () => {
		expect(defaultRunShell({ command: "ls", kind: "posix", shellName: "bash" }, macCatalog, "auto")).toBe("/bin/bash");
		expect(defaultRunShell({ command: "ls", kind: "posix", shellName: "fish" }, macCatalog, "auto")).toBe("/opt/homebrew/bin/fish");
		expect(defaultRunShell({ command: "ls", kind: "posix" }, macCatalog, "/bin/zsh")).toBe("/bin/zsh");
		expect(defaultRunShell({ command: "Get-Date", kind: "powershell" }, macCatalog, "auto")).toBe("auto");
	});

	it("never offers WSL", () => {
		expect(runShellOptions(windowsCatalog).map((shell) => shell.id)).not.toContain("wsl");
	});
});

describe("command run requests", () => {
	beforeEach(() => {
		host.post.mockReset();
		host.del.mockReset();
		host.clientFor.mockReset();
		host.key.mockReset();
		host.key.mockResolvedValue("local-key");
	});

	it("presents the desktop key to the local daemon and streams every event in order", async () => {
		host.post.mockResolvedValue({
			data: sseStream([{ started: { runId: "cmdrun-1", shell: "PowerShell", shellPath: "x", workingDir: "C:\\repo", startedAt: "2026-11-12T10:00:00Z" } }, { output: "hello\n" }, { exit: { exitCode: 0, durationMs: 12 } }]),
			error: undefined,
		});
		const events: unknown[] = [];
		await streamCommandRun({ sessionId: "s1" }, { command: "echo hello", shell: "pwsh" }, new AbortController().signal, (event) => events.push(event));
		expect(host.post).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/command-runs", expect.objectContaining({
			params: { path: { sessionId: "s1" } },
			body: { command: "echo hello", shell: "pwsh" },
			headers: { "X-AO-Command-Run-Key": "local-key" },
			parseAs: "stream",
		}));
		expect(events).toEqual([
			{ started: expect.objectContaining({ runId: "cmdrun-1" }) },
			{ output: "hello\n" },
			{ exit: { exitCode: 0, durationMs: 12 } },
		]);
	});

	it("never sends the local key to a remote host", async () => {
		host.post.mockResolvedValue({ data: sseStream([{ exit: { exitCode: 0, durationMs: 1 } }]), error: undefined });
		await streamCommandRun({ sessionId: "s1", hostId: "host-2" }, { command: "echo", shell: "auto" }, new AbortController().signal, () => undefined);
		expect(host.clientFor).toHaveBeenCalledWith("host-2");
		expect(host.post.mock.calls[0][1].headers).toEqual({});
		expect(host.key).not.toHaveBeenCalled();
	});

	it("refuses a local run without the desktop key", async () => {
		host.key.mockResolvedValue("");
		await expect(streamCommandRun({ sessionId: "s1" }, { command: "echo", shell: "auto" }, new AbortController().signal, () => undefined))
			.rejects.toBeInstanceOf(CommandRunUnavailableError);
		expect(host.post).not.toHaveBeenCalled();
	});

	it("surfaces the daemon's refusal", async () => {
		const refusal = { code: "COMMAND_RUN_USER_CLIENT_REQUIRED", message: "Commands can only be run from the Orchestrator app" };
		host.post.mockResolvedValue({ data: undefined, error: refusal });
		await expect(streamCommandRun({ sessionId: "s1" }, { command: "echo", shell: "auto" }, new AbortController().signal, () => undefined)).rejects.toBe(refusal);
	});

	it("treats stopping a run that already ended as done", async () => {
		host.del.mockResolvedValue({ error: { code: "COMMAND_RUN_NOT_FOUND" } });
		await expect(stopCommandRun({ sessionId: "s1" }, "cmdrun-1")).resolves.toBeUndefined();
		expect(host.del).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/command-runs/{runId}", {
			params: { path: { sessionId: "s1", runId: "cmdrun-1" } },
			headers: { "X-AO-Command-Run-Key": "local-key" },
		});
	});
});
