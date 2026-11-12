/**
 * Chat's Run button for command code blocks, and the output it shows under
 * the block.
 *
 * Run is always the reader's click on a settled block: a block still
 * streaming, quoted from a third party (safe-origin content), or written by
 * the person themself never offers it, and a reload never re-runs anything.
 * The output is local UI state and is never sent to the agent.
 */

import { createContext, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronRight, Loader2, Play, Square, SquareTerminal, X } from "@/components/app-icons";
import { apiErrorMessage } from "../../lib/api-client";
import { formatScriptDuration } from "../../lib/cloud-script-runs";
import {
	defaultRunShell,
	fetchTerminalShells,
	openCommandInTerminal,
	runnableCommand,
	runShellName,
	runShellOptions,
	type CommandRunTarget,
	type RunnableCommand,
} from "../../lib/command-runs";
import { markTerminalHandleFresh } from "../../lib/fresh-terminal-handles";
import { LOCAL_HOST } from "../../lib/hosts";
import { cn } from "../../lib/utils";
import { shellTerminalsQueryKeyForHost, toShellTerminal, type ShellTerminal } from "../../hooks/useShellTerminals";
import { commandRunText, isCommandRunActive, useCommandRunStore, type CommandRun } from "../../stores/command-run-store";
import { terminalShellRequestValue, useTerminalShellStore } from "../../stores/terminal-shell-store";
import { useUiStore } from "../../stores/ui-store";
import { Button } from "../ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuLabel,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import { Pressable } from "../ui/pressable";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { CopyButton } from "./CopyButton";

/** The session whose daemon runs a chat's commands; null where Run is not offered (Cloud sessions, no session). */
export const CodeRunTargetContext = createContext<CommandRunTarget | null>(null);

/** The message a code block belongs to, so the same command in two replies keeps two results. */
export const CodeRunScope = createContext<string>("");

export type CodeBlockRun = {
	key: string;
	block: RunnableCommand;
	target: CommandRunTarget;
};

/** The run a code block offers, or null when it offers none. */
export function useCodeBlockRun(code: string, language: string | undefined, offered: boolean): CodeBlockRun | null {
	const target = useContext(CodeRunTargetContext);
	const scope = useContext(CodeRunScope);
	return useMemo(() => {
		if (!offered || !target) return null;
		const block = runnableCommand(code, language);
		if (!block) return null;
		const key = [target.hostId ?? LOCAL_HOST, target.sessionId, scope, language ?? "", code].join("\u0000");
		return { key, block, target };
	}, [offered, target, scope, code, language]);
}

function useRunShells(run: CodeBlockRun) {
	const remote = Boolean(run.target.hostId && run.target.hostId !== LOCAL_HOST);
	const catalog = useQuery({
		queryKey: ["command-run-shells", run.target.hostId ?? LOCAL_HOST],
		queryFn: () => fetchTerminalShells(run.target.hostId),
		staleTime: 5 * 60_000,
	});
	const preference = useTerminalShellStore((state) => state.preference);
	const loadPreference = useTerminalShellStore((state) => state.load);
	useEffect(() => {
		if (!remote) void loadPreference();
	}, [loadPreference, remote]);
	// The Settings shell is this machine's; a remote host uses its own default.
	const preferred = remote ? "auto" : terminalShellRequestValue(preference);
	const picked = useCommandRunStore((state) => state.shells[run.key]);
	const shell = picked ?? defaultRunShell(run.block, catalog.data, preferred);
	return { catalog: catalog.data, shell };
}

/** Run (or Stop) and the shell picker, for the code block's header. */
export function CodeRunControls({ run }: { run: CodeBlockRun }) {
	const { t } = useTranslation();
	const { catalog, shell } = useRunShells(run);
	const current = useCommandRunStore((state) => state.runs[run.key]);
	const start = useCommandRunStore((state) => state.start);
	const stop = useCommandRunStore((state) => state.stop);
	const setShell = useCommandRunStore((state) => state.setShell);
	const active = isCommandRunActive(current);
	const shellName = runShellName(shell, catalog);
	const options = runShellOptions(catalog);

	return (
		<div className="flex items-center gap-0.5">
			<DropdownMenu modal={false}>
				<DropdownMenuTrigger asChild>
					<Button
						type="button"
						variant="quiet"
						size="xs"
						disabled={active}
						aria-label={t("chat.commandRun.shellPicker", { shell: shellName })}
						className="max-w-40 gap-0.5 px-1.5 text-caption"
					>
						<span className="truncate">{shellName}</span>
						<ChevronDown aria-hidden="true" className="size-3" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end" side="bottom">
					<DropdownMenuLabel>{t("chat.commandRun.runWith")}</DropdownMenuLabel>
					<DropdownMenuSeparator />
					<DropdownMenuRadioGroup value={shell} onValueChange={(value) => setShell(run.key, value)}>
						<DropdownMenuRadioItem value="auto">
							{t("chat.commandRun.defaultShell", { shell: catalog?.default.name || "auto" })}
						</DropdownMenuRadioItem>
						{options.map((option) => (
							<DropdownMenuRadioItem key={option.id} value={option.id}>
								{option.name}
							</DropdownMenuRadioItem>
						))}
					</DropdownMenuRadioGroup>
				</DropdownMenuContent>
			</DropdownMenu>
			{active ? (
				<Button
					type="button"
					variant="quiet"
					size="xs"
					disabled={current?.stopping}
					onClick={() => void stop(run.key)}
					data-testid="command-run-stop"
				>
					<Square aria-hidden="true" className="size-3" />
					{t("chat.commandRun.stop")}
				</Button>
			) : (
				<Tooltip>
					<TooltipTrigger asChild>
						<Button
							type="button"
							variant="quiet"
							size="xs"
							className="text-foreground"
							onClick={() => start(run.key, run.target, run.block.command, shell)}
							aria-label={t("chat.commandRun.runWithShell", { shell: shellName })}
							data-testid="command-run-start"
						>
							<Play aria-hidden="true" className="size-3" />
							{t("chat.commandRun.run")}
						</Button>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("chat.commandRun.runWithShell", { shell: shellName })}</TooltipContent>
				</Tooltip>
			)}
		</div>
	);
}

function useNow(active: boolean): number {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		if (!active) return;
		setNow(Date.now());
		const timer = window.setInterval(() => setNow(Date.now()), 1_000);
		return () => window.clearInterval(timer);
	}, [active]);
	return now;
}

function formatRunDuration(ms: number): string {
	return ms < 10_000 ? `${(Math.max(0, ms) / 1000).toFixed(1)}s` : formatScriptDuration(ms);
}

function StatusIcon({ run }: { run: CommandRun }) {
	if (isCommandRunActive(run)) return <Loader2 aria-hidden="true" className="size-3.5 shrink-0 animate-spin text-status-working" />;
	if (run.status === "succeeded") return <Check aria-hidden="true" className="size-3.5 shrink-0 text-success" />;
	if (run.status === "stopped") return <Square aria-hidden="true" className="size-3 shrink-0 text-passive" />;
	return <X aria-hidden="true" className="size-3.5 shrink-0 text-error" />;
}

function useStatusText(run: CommandRun): string {
	const { t } = useTranslation();
	switch (run.status) {
		case "starting":
			return t("chat.commandRun.starting");
		case "running":
			return run.stopping ? t("chat.commandRun.stopping") : t("chat.commandRun.running");
		case "succeeded":
		case "failed":
			return t("chat.commandRun.exitCode", { code: run.exitCode ?? -1 });
		case "stopped":
			return t("chat.commandRun.stopped");
		case "timedOut":
			return t("chat.commandRun.timedOut");
		default:
			if (run.desktopOnly) return t("chat.commandRun.desktopOnly");
			if (run.error === "connection") return t("chat.commandRun.connectionLost");
			return run.error || t("chat.commandRun.failedToRun");
	}
}

/** The run's result under the code block: state, the exact command, and its output. */
export function CodeRunOutput({ run }: { run: CodeBlockRun }) {
	const { t } = useTranslation();
	const current = useCommandRunStore((state) => state.runs[run.key]);
	const toggle = useCommandRunStore((state) => state.toggle);
	const dismiss = useCommandRunStore((state) => state.dismiss);
	if (!current) return null;
	const active = isCommandRunActive(current);
	return (
		<div data-chat-find-exclude="" data-testid="command-run-output" data-status={current.status} className="border-t border-border">
			<div className="flex min-h-8 items-center gap-1 py-0.5 pl-1 pr-1.5">
				<Pressable
					aria-expanded={!current.collapsed}
					aria-label={current.collapsed ? t("chat.commandRun.showOutput") : t("chat.commandRun.hideOutput")}
					onClick={() => toggle(run.key)}
					className="group/run-status flex min-h-7 min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 hover:bg-interactive-hover"
				>
					<ChevronRight
						aria-hidden="true"
						className={cn("size-3 shrink-0 text-muted-foreground/60 transition-transform group-hover/run-status:text-muted-foreground", !current.collapsed && "rotate-90")}
					/>
					<StatusIcon run={current} />
					<RunSummary run={current} />
				</Pressable>
				{!active ? <RunActions run={run} current={current} onDismiss={() => dismiss(run.key)} /> : null}
			</div>
			{!current.collapsed ? <RunOutputBody run={current} /> : null}
		</div>
	);
}

function RunSummary({ run }: { run: CommandRun }) {
	const { t } = useTranslation();
	const active = isCommandRunActive(run);
	const now = useNow(active);
	const status = useStatusText(run);
	const duration = active ? now - run.startedAt : run.durationMs;
	const failed = run.status === "failed" || run.status === "error" || run.status === "timedOut";
	return (
		<span className="flex min-w-0 flex-1 items-baseline gap-2 text-xs">
			<span className={cn("shrink-0", failed ? "text-error" : "text-foreground")} role="status">{status}</span>
			{run.shellName ? (
				<span className="min-w-0 truncate text-caption text-muted-foreground" title={run.workingDir}>
					{run.workingDir ? t("chat.commandRun.ranWith", { shell: run.shellName, dir: run.workingDir }) : run.shellName}
				</span>
			) : null}
			{duration !== undefined && run.status !== "error" ? (
				<span className="ml-auto shrink-0 font-mono text-caption tabular-nums text-muted-foreground">{formatRunDuration(duration)}</span>
			) : null}
		</span>
	);
}

function RunActions({ run, current, onDismiss }: { run: CodeBlockRun; current: CommandRun; onDismiss: () => void }) {
	const { t } = useTranslation();
	const { screen, tail } = current;
	const output = useMemo(() => commandRunText({ screen, tail }), [screen, tail]);
	const openInTerminal = useOpenInTerminal(run, current);
	return (
		<div className="flex shrink-0 items-center gap-0.5">
			{output ? (
				<CopyButton text={output} label={t("chat.commandRun.copyOutput")} copiedLabel={t("chat.commandRun.copied")} compact size="icon-xs" />
			) : null}
			<IconAction label={t("chat.commandRun.openInTerminal")} onClick={openInTerminal.open} disabled={openInTerminal.pending} testId="command-run-open-terminal">
				{openInTerminal.pending ? <Loader2 aria-hidden="true" className="size-3.5 animate-spin" /> : <SquareTerminal aria-hidden="true" className="size-3.5" />}
			</IconAction>
			<IconAction label={t("chat.commandRun.clear")} onClick={onDismiss} testId="command-run-clear">
				<X aria-hidden="true" className="size-3.5" />
			</IconAction>
		</div>
	);
}

function IconAction({ label, onClick, disabled, testId, children }: { label: string; onClick: () => void; disabled?: boolean; testId?: string; children: ReactNode }) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Button type="button" variant="quiet" size="icon-xs" aria-label={label} onClick={onClick} disabled={disabled} data-testid={testId}>
					{children}
				</Button>
			</TooltipTrigger>
			<TooltipContent side="bottom">{label}</TooltipContent>
		</Tooltip>
	);
}

/** Retypes the command in a new shell tab of the session, for a command that needs input. */
function useOpenInTerminal(run: CodeBlockRun, current: CommandRun) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const setActiveShellTerminal = useUiStore((state) => state.setActiveShellTerminal);
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const [pending, setPending] = useState(false);
	const open = async () => {
		if (pending) return;
		setPending(true);
		try {
			const created = await openCommandInTerminal(run.target, { command: current.command, shell: current.shell });
			const terminal = toShellTerminal(created, run.target.hostId);
			const queryKey = shellTerminalsQueryKeyForHost(run.target.hostId);
			if (!run.target.hostId || run.target.hostId === LOCAL_HOST) markTerminalHandleFresh(terminal.handleId);
			queryClient.setQueryData<ShellTerminal[]>(queryKey, (list = []) => [
				...list.filter((item) => item.handleId !== terminal.handleId),
				terminal,
			]);
			void queryClient.invalidateQueries({ queryKey });
			setActiveShellTerminal(terminal.handleId);
		} catch (error) {
			showGlobalToast(t("chat.commandRun.openInTerminalFailed"), apiErrorMessage(error, t("chat.commandRun.openInTerminalFailed")), "error");
		} finally {
			setPending(false);
		}
	};
	return { open: () => void open(), pending };
}

function RunOutputBody({ run }: { run: CommandRun }) {
	const { t } = useTranslation();
	const pre = useRef<HTMLPreElement>(null);
	const follow = useRef(true);
	const { screen, tail } = run;
	const output = useMemo(() => commandRunText({ screen, tail }), [screen, tail]);
	useLayoutEffect(() => {
		const node = pre.current;
		if (node && follow.current) node.scrollTop = node.scrollHeight;
	}, [output]);
	const active = isCommandRunActive(run);
	return (
		<div className="px-2.5 pb-2.5">
			<pre
				ref={pre}
				tabIndex={0}
				aria-label={t("chat.commandRun.outputLabel")}
				data-testid="command-run-output-text"
				onScroll={(event) => {
					const node = event.currentTarget;
					// Within a line of the bottom follows along; further up is the reader's.
					follow.current = node.scrollHeight - node.scrollTop - node.clientHeight < 24;
				}}
				className="scrollbar-none max-h-72 cursor-text select-text overflow-auto whitespace-pre-wrap break-words rounded-md bg-background px-2.5 py-2 font-mono text-xs leading-[1.6] text-foreground"
			>
				<span className="text-muted-foreground" data-testid="command-run-command">
					<span aria-hidden="true">{"> "}</span>
					{run.command}
				</span>
				{"\n"}
				{output ? output : <span className="text-muted-foreground">{active ? t("chat.commandRun.waiting") : t("chat.commandRun.noOutput")}</span>}
			</pre>
			{run.truncated ? <p className="mt-1.5 text-caption text-muted-foreground">{t("chat.commandRun.truncated")}</p> : null}
		</div>
	);
}
