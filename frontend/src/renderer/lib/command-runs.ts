/**
 * Chat's Run button: which code blocks are commands, which shell runs them,
 * and the daemon calls that run them.
 *
 * Running is always a person's click. Nothing here runs on its own, and the
 * daemon refuses these routes to anything but a person's client: the local
 * desktop proves itself with a key only this app window can read
 * (window.ao.commandRuns.key), and a paired remote host is reached through the
 * remote proxy, which authenticates with the pairing credential. The local key
 * is never sent to a remote host.
 */

import type { components } from "../../api/schema";
import { aoBridge } from "./bridge";
import { clientForSessionHost } from "./host-clients";
import { LOCAL_HOST, type HostId } from "./hosts";

export type CommandShellKind = "powershell" | "posix" | "cmd";
export type TerminalShellOption = components["schemas"]["TerminalShellResponse"];
export type TerminalShellCatalog = components["schemas"]["TerminalShellsResponse"];
export type CommandRunStreamEvent = components["schemas"]["CommandRunStreamEvent"];
export type CommandRunStarted = components["schemas"]["CommandRunStarted"];
export type CommandRunExited = components["schemas"]["CommandRunExited"];

/** A code block that can be run: the exact command to run, and the shell its language names. */
export type RunnableCommand = {
	command: string;
	/** The shell family the block's language names; undefined uses the default shell. */
	kind?: CommandShellKind;
	/** The language's own shell when it names one (zsh, bash, fish), for picking that binary. */
	shellName?: string;
};

/** Matches the daemon's limit (shellterm.MaxCommandRunBytes). */
export const MAX_COMMAND_BYTES = 8000;
/** A block longer than this is a script to read, not a command to click. */
const MAX_COMMAND_LINES = 12;
/** An unlabelled block must be short to count as a command. */
const MAX_UNLABELLED_LINES = 5;

const POWERSHELL_LANGUAGES = new Set(["powershell", "pwsh", "ps1", "ps", "posh"]);
const POSIX_LANGUAGES = new Map<string, string | undefined>([
	["bash", "bash"],
	["sh", "sh"],
	["zsh", "zsh"],
	["fish", "fish"],
	["shell", undefined],
	["shellscript", undefined],
]);
const CMD_LANGUAGES = new Set(["cmd", "bat", "batch", "dos"]);
// Transcripts: the commands are the prompted lines; the rest is output.
const SESSION_LANGUAGES = new Set(["console", "shellsession", "shell-session", "terminal"]);
const UNLABELLED_LANGUAGES = new Set(["", "text", "txt", "plaintext", "plain"]);

// Programs an agent typically tells a person to run. A bare unlabelled block
// counts as a command only when each line starts with one of these, a
// PowerShell cmdlet, or a path to something executable.
const KNOWN_COMMANDS = new Set([
	"ao", "claude", "codex", "gemini", "cursor", "code", "opencode", "aider",
	"powershell", "pwsh", "cmd", "bash", "sh", "zsh", "fish", "wsl", "start", "open", "xdg-open",
	"npm", "npx", "pnpm", "pnpx", "yarn", "bun", "bunx", "node", "deno", "corepack", "nvm", "fnm", "volta",
	"python", "python3", "py", "pip", "pip3", "pipx", "uv", "uvx", "poetry", "conda", "pyenv", "pytest",
	"go", "gofmt", "cargo", "rustup", "rustc", "dotnet", "java", "javac", "mvn", "gradle", "gradlew",
	"ruby", "gem", "bundle", "rails", "rake", "php", "composer", "swift", "xcodebuild", "xcrun", "flutter", "dart",
	"git", "gh", "glab", "hub", "svn",
	"docker", "docker-compose", "podman", "kubectl", "helm", "minikube", "kind", "terraform", "tofu", "pulumi",
	"aws", "az", "gcloud", "firebase", "vercel", "netlify", "wrangler", "cf", "flyctl", "fly", "heroku", "supabase", "prisma",
	"make", "cmake", "ninja", "just", "task", "tsc", "vite", "vitest", "jest", "eslint", "prettier", "turbo", "nx",
	"brew", "apt", "apt-get", "dnf", "yum", "pacman", "snap", "winget", "choco", "scoop", "sudo",
	"curl", "wget", "ssh", "scp", "rsync", "ping", "nslookup", "dig", "ipconfig", "ifconfig", "netstat", "tailscale", "ngrok",
	"cd", "ls", "dir", "pwd", "echo", "cat", "type", "mkdir", "md", "rmdir", "rm", "del", "cp", "copy", "mv", "move", "ren",
	"touch", "chmod", "chown", "ln", "find", "grep", "rg", "sed", "awk", "head", "tail", "tar", "zip", "unzip", "which", "where",
	"whoami", "hostname", "set", "setx", "export", "env", "source", "reg", "sc", "net", "netsh", "taskkill", "tasklist", "icacls",
]);

const POWERSHELL_CMDLET = /^[A-Z][a-z]+-[A-Z][A-Za-z]+$/;
const ABSOLUTE_PATH = /^(?:~[\\/]|\/|[A-Za-z]:[\\/])/;
const EXECUTABLE_EXTENSION = /\.(?:exe|cmd|bat|ps1|sh|py|js|mjs)$/i;

/** The prompt a transcript line starts with: `$ `, `% `, `> `, `PS C:\x> `, `C:\x>`. */
const PROMPT = /^(?:PS(?: [^>\n]*)?> ?|[A-Za-z]:\\[^>\n]*> ?|[$%>] )/;

function normalizedLanguage(language: string | undefined): string {
	return (language ?? "").trim().toLowerCase();
}

/**
 * The command a code block asks the reader to run, or null for a block that is
 * not a command (source code, data, diffs, prose, or a script too long to run
 * with a click).
 */
export function runnableCommand(code: string, language: string | undefined): RunnableCommand | null {
	const lang = normalizedLanguage(language);
	const text = code.replace(/\r\n/g, "\n").replace(/\n+$/, "");
	if (!text.trim() || new TextEncoder().encode(text).length > MAX_COMMAND_BYTES) return null;
	const lines = text.split("\n");
	if (lines.filter((line) => line.trim()).length > MAX_COMMAND_LINES) return null;

	if (POWERSHELL_LANGUAGES.has(lang)) return { command: stripUniformPrompt(text), kind: "powershell" };
	if (CMD_LANGUAGES.has(lang)) return { command: stripUniformPrompt(text), kind: "cmd" };
	if (POSIX_LANGUAGES.has(lang)) return { command: stripUniformPrompt(text), kind: "posix", shellName: POSIX_LANGUAGES.get(lang) };
	if (SESSION_LANGUAGES.has(lang)) {
		const prompted = lines.filter((line) => PROMPT.test(line));
		// No prompts: agents label pasted output `console` too, so the block
		// must pass the same test as an unlabelled one, line by line.
		if (!prompted.length) return unlabelledCommand(text);
		const command = prompted.map((line) => line.replace(PROMPT, "")).join("\n");
		return command.trim() ? { command, kind: kindFromPrompt(prompted[0]) } : null;
	}
	if (!UNLABELLED_LANGUAGES.has(lang)) return null;
	return unlabelledCommand(text);
}

function kindFromPrompt(line: string | undefined): CommandShellKind | undefined {
	if (!line) return undefined;
	if (/^PS(?: |>)/.test(line)) return "powershell";
	if (/^[A-Za-z]:\\/.test(line)) return "cmd";
	return "posix";
}

/** Drops a `$ ` prompt only when every command line carries one. */
function stripUniformPrompt(text: string): string {
	const lines = text.split("\n");
	const commands = lines.filter((line) => line.trim() && !isComment(line));
	if (!commands.length || !commands.every((line) => /^\s*[$%] /.test(line))) return text;
	return lines.map((line) => line.replace(/^(\s*)[$%] /, "$1")).join("\n");
}

function isComment(line: string): boolean {
	return /^\s*(?:#|REM\b|::)/i.test(line);
}

/** Lines that continue onto the next (\ in POSIX shells, ` in PowerShell, ^ in cmd) are one command. */
function logicalLines(text: string): string[] {
	const out: string[] = [];
	let current = "";
	for (const line of text.split("\n")) {
		const trimmed = line.trimEnd();
		if (/[\\`^]$/.test(trimmed) && !/\\\\$/.test(trimmed)) {
			current += trimmed.slice(0, -1) + " ";
			continue;
		}
		out.push(current + line);
		current = "";
	}
	if (current) out.push(current);
	return out;
}

function unlabelledCommand(text: string): RunnableCommand | null {
	const lines = text.split("\n").filter((line) => line.trim());
	if (lines.length > MAX_UNLABELLED_LINES) return null;
	const commands = logicalLines(text).filter((line) => line.trim() && !isComment(line));
	if (!commands.length) return null;
	const prompted = commands.every((line) => PROMPT.test(line.trimStart()));
	const stripped = prompted ? commands.map((line) => line.trimStart().replace(PROMPT, "")) : commands;
	if (!stripped.every(isCommandLine)) return null;
	const command = prompted
		? text.split("\n").map((line) => line.trimStart().replace(PROMPT, "")).join("\n")
		: text;
	return { command, kind: looksLikePowerShell(stripped) ? "powershell" : undefined };
}

function looksLikePowerShell(lines: string[]): boolean {
	return lines.some((line) => {
		const first = firstToken(line);
		return POWERSHELL_CMDLET.test(first) || /\$env:/i.test(line) || /^(?:powershell|pwsh)(?:\.exe)?$/i.test(first);
	});
}

function firstToken(line: string): string {
	let rest = line.trim();
	// `FOO=1 npm test`, `sudo npm i`, `& "C:\x\y.exe"`
	for (;;) {
		const assignment = /^[A-Za-z_][A-Za-z0-9_]*=\S*\s+/.exec(rest);
		if (assignment) {
			rest = rest.slice(assignment[0].length);
			continue;
		}
		break;
	}
	const token = /^(?:&\s*)?("[^"]*"|'[^']*'|\S+)/.exec(rest);
	return token ? token[1] : "";
}

/** Whether one line of an unlabelled block reads as a command rather than prose or data. */
export function isCommandLine(line: string): boolean {
	const trimmed = line.trim();
	if (!trimmed || trimmed.length > 1000) return false;
	// JSON, YAML, markup, diffs and list items are not commands.
	if (/^[[{<]/.test(trimmed) || /^[-+@]{1,3}\s/.test(trimmed) || /^\*\s/.test(trimmed)) return false;
	const first = firstToken(trimmed);
	if (!first || first.endsWith(":") || first.endsWith(",")) return false;
	const bare = first.replace(/^["']|["']$/g, "");
	const name = bare.split(/[\\/]/).pop()!.replace(/\.(?:exe|cmd|bat|ps1)$/i, "").toLowerCase();
	if (KNOWN_COMMANDS.has(name)) return !looksLikeProse(trimmed);
	if (POWERSHELL_CMDLET.test(bare)) return true;
	// An invoked script or program: `./setup.sh`, `& "C:\x\tool.exe"`, or an
	// absolute path to one followed by arguments. A bare path alone is more
	// often a file the reply mentions than a command to run.
	if (/^\.{1,2}[\\/]\S/.test(bare)) return true;
	if (ABSOLUTE_PATH.test(bare) && EXECUTABLE_EXTENSION.test(bare)) {
		const invoked = /^&\s*/.test(trimmed);
		const hasArguments = trimmed.slice(trimmed.indexOf(first) + first.length).trim() !== "";
		return invoked || hasArguments;
	}
	return false;
}

// "Open the file and check the output." starts with a command word but is a sentence.
function looksLikeProse(line: string): boolean {
	const words = line.split(/\s+/);
	return words.length >= 6 && /[.!?]$/.test(line) && !/[|&;$=<>"'`\\/-]/.test(line);
}

const WINDOWS_SELECTORS = new Set(["pwsh", "powershell", "cmd", "git-bash", "wsl"]);

/** The shells Run can use: every installed shell except WSL, which cannot run a command non-interactively yet. */
export function runShellOptions(catalog: TerminalShellCatalog | undefined): TerminalShellOption[] {
	return (catalog?.shells ?? []).filter((shell) => shell.id !== "wsl");
}

function basename(path: string): string {
	return path.split(/[\\/]/).pop()?.replace(/\.exe$/i, "").toLowerCase() ?? "";
}

/**
 * The shell a block runs with when the reader has not picked one: the shell
 * its language names when it is installed, otherwise the user's default
 * terminal shell. `preference` is a terminal shell request value ("auto",
 * a Windows selector, or a path).
 */
export function defaultRunShell(block: RunnableCommand, catalog: TerminalShellCatalog | undefined, preference: string): string {
	const shells = runShellOptions(catalog);
	const has = (id: string) => shells.some((shell) => shell.id === id);
	const byName = (...names: string[]) => names.map((name) => shells.find((shell) => basename(shell.path ?? shell.id) === name)).find(Boolean)?.id;
	const windows = shells.some((shell) => WINDOWS_SELECTORS.has(shell.id));
	const preferred = preference && preference !== "wsl" ? preference : "auto";
	const preferredName = basename(preferred);
	switch (block.kind) {
		case "powershell":
			if (preferredName === "pwsh" || preferredName === "powershell") return preferred;
			if (windows) return has("pwsh") ? "pwsh" : has("powershell") ? "powershell" : preferred;
			return byName("pwsh") ?? preferred;
		case "cmd":
			return windows && has("cmd") ? "cmd" : preferred;
		case "posix": {
			if (windows) return has("git-bash") ? "git-bash" : preferred;
			if (block.shellName) {
				const named = byName(block.shellName);
				if (named) return named;
			}
			if (preferred !== "auto" && !["pwsh", "powershell", "cmd"].includes(preferredName)) return preferred;
			return "auto";
		}
		default:
			return preferred;
	}
}

/** The name the shell picker shows for a selector. */
export function runShellName(shell: string, catalog: TerminalShellCatalog | undefined): string {
	if (shell === "auto") return catalog?.default.name || "auto";
	return catalog?.shells.find((option) => option.id === shell)?.name ?? basename(shell);
}

/** A local Run without the desktop's key: the browser preview, or a subframe. */
export class CommandRunUnavailableError extends Error {
	constructor() {
		super("COMMAND_RUN_DESKTOP_ONLY");
		this.name = "CommandRunUnavailableError";
	}
}

const COMMAND_RUN_KEY_HEADER = "X-AO-Command-Run-Key";

async function userClientHeaders(hostId: HostId | undefined): Promise<Record<string, string>> {
	// A remote host is reached through the remote proxy, which authenticates
	// with the pairing credential. The local key belongs to this machine's
	// daemon only and must never travel to another host.
	if (hostId && hostId !== LOCAL_HOST) return {};
	const key = await aoBridge.commandRuns.key().catch(() => "");
	if (!key) throw new CommandRunUnavailableError();
	return { [COMMAND_RUN_KEY_HEADER]: key };
}

export type CommandRunTarget = { sessionId: string; hostId?: HostId };

/**
 * Runs a command and reports each event as it arrives. Resolves when the
 * stream ends; rejects with the daemon's error envelope when the command could
 * not start, or when the connection failed.
 */
export async function streamCommandRun(
	target: CommandRunTarget,
	request: { command: string; shell: string },
	signal: AbortSignal,
	onEvent: (event: CommandRunStreamEvent) => void,
): Promise<void> {
	const headers = await userClientHeaders(target.hostId);
	const { data: stream, error } = await clientForSessionHost(target.hostId).POST("/api/v1/sessions/{sessionId}/command-runs", {
		params: { path: { sessionId: target.sessionId } },
		body: request,
		headers,
		signal,
		parseAs: "stream",
	});
	if (error) throw error;
	if (!(stream instanceof ReadableStream)) throw new Error("The daemon returned no output stream");
	const reader = stream.pipeThrough(new TextDecoderStream()).getReader();
	let buffered = "";
	try {
		for (;;) {
			const { done, value } = await reader.read();
			if (done) break;
			buffered += value;
			let boundary = buffered.indexOf("\n\n");
			while (boundary >= 0) {
				const frame = buffered.slice(0, boundary);
				buffered = buffered.slice(boundary + 2);
				boundary = buffered.indexOf("\n\n");
				const data = frame.split("\n").filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trimStart()).join("\n");
				if (data) onEvent(JSON.parse(data) as CommandRunStreamEvent);
			}
		}
	} finally {
		void reader.cancel().catch(() => undefined);
	}
}

/** Stops a running command; a run that already ended is not an error. */
export async function stopCommandRun(target: CommandRunTarget, runId: string): Promise<void> {
	const headers = await userClientHeaders(target.hostId);
	const { error } = await clientForSessionHost(target.hostId).DELETE("/api/v1/sessions/{sessionId}/command-runs/{runId}", {
		params: { path: { sessionId: target.sessionId, runId } },
		headers,
	});
	if (error && (error as { code?: string }).code !== "COMMAND_RUN_NOT_FOUND") throw error;
}

/** Opens a new shell tab in the session and types the command there. */
export async function openCommandInTerminal(target: CommandRunTarget, request: { command: string; shell: string }) {
	const headers = await userClientHeaders(target.hostId);
	const { data, error } = await clientForSessionHost(target.hostId).POST("/api/v1/sessions/{sessionId}/command-runs/terminal", {
		params: { path: { sessionId: target.sessionId } },
		body: request,
		headers,
	});
	if (error) throw error;
	if (!data) throw new Error("The daemon returned no terminal");
	return data.shellTerminal;
}

/** Shells available on the daemon that owns the session. */
export async function fetchTerminalShells(hostId?: HostId): Promise<TerminalShellCatalog> {
	const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/terminal-shells");
	if (error) throw error;
	if (!data) throw new Error("The daemon returned no shells");
	return data;
}
