import type { TFunction } from "i18next";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, ShieldCheck, SquareTerminal } from "@/components/app-icons";
import { type TerminalShellCatalog, useTerminalShells } from "../hooks/useTerminalShells";
import { apiErrorCode, apiErrorMessage } from "../lib/api-client";
import { LOCAL_HOST, type HostId } from "../lib/hosts";
import { terminalShellRequestValue, useTerminalShellStore } from "../stores/terminal-shell-store";
import { Button } from "./ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuSub,
	DropdownMenuSubContent,
	DropdownMenuSubTrigger,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Popover, PopoverAnchor, PopoverContent } from "./ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

/** What a menu row asks for: a shell id from the daemon's catalog, as user or administrator. */
export type TerminalChoice = { shell?: string; elevated?: boolean };

/**
 * The open request a menu choice adds. Anything that is not a choice (such as
 * the click event of a plain button) adds nothing: the default shell as user.
 */
export function terminalChoiceInput(choice: unknown): TerminalChoice {
	if (!choice || typeof choice !== "object") return {};
	const { shell, elevated } = choice as TerminalChoice;
	return {
		...(typeof shell === "string" && shell ? { shell } : {}),
		...(elevated === true ? { elevated: true } : {}),
	};
}

/** detected: the daemon listed it, so it may also open as administrator. */
export type TerminalMenuShell = { id: string; name: string; isDefault: boolean; detected: boolean };

/**
 * available: this app on this computer may open administrator terminals.
 * remote / desktop-only: the daemon refuses, so the row is shown disabled with why.
 * hidden: the daemon's platform has none (macOS, Linux) or it predates them.
 */
export type TerminalAdminState = "available" | "remote" | "desktop-only" | "hidden";

/** adminShells: the detected shells, the only ones the daemon runs as administrator. */
export type TerminalMenuModel = { shells: TerminalMenuShell[]; adminShells: TerminalMenuShell[]; admin: TerminalAdminState };

const windowsPath = /^[a-z]:[\\/]/i;

function samePath(a: string | undefined, b: string | undefined): boolean {
	if (!a || !b) return false;
	if (windowsPath.test(a) || windowsPath.test(b)) return a.replace(/\//g, "\\").toLowerCase() === b.replace(/\//g, "\\").toLowerCase();
	return a === b;
}

function shellName(path: string): string {
	return path.split(/[\\/]/).filter(Boolean).at(-1)?.replace(/\.exe$/i, "") ?? path;
}

/**
 * The rows of the new-terminal menu: the detected shells with the default one
 * first, and whether the administrator rows apply. preferredShell is the
 * shell this computer's terminals open by default (a selector, a path, or
 * auto); a remote host has none and uses its own default.
 */
export function terminalMenuModel(catalog: TerminalShellCatalog | undefined, preferredShell?: string): TerminalMenuModel {
	// An older or unreachable daemon may answer without a catalog.
	if (!catalog?.shells?.length) return { shells: [], adminShells: [], admin: "hidden" };
	const preferred = preferredShell && preferredShell !== "auto" ? preferredShell : undefined;
	const isDefault = (shell: TerminalShellCatalog["shells"][number]) =>
		preferred ? shell.id === preferred || samePath(shell.path, preferred) : samePath(shell.path, catalog.default.path);
	const shells: TerminalMenuShell[] = catalog.shells.map((shell) => ({ id: shell.id, name: shell.name, isDefault: false, detected: true }));
	let defaultIndex = catalog.shells.findIndex(isDefault);
	if (defaultIndex < 0 && preferred) {
		// A custom shell from Settings that detection does not list: it opens as
		// the user, never as administrator (the daemon refuses that).
		shells.unshift({ id: preferred, name: shellName(preferred), isDefault: true, detected: false });
	} else {
		if (defaultIndex < 0) defaultIndex = 0;
		const [first] = shells.splice(defaultIndex, 1);
		shells.unshift({ ...first!, isDefault: true });
	}
	const elevation = catalog.elevation as TerminalShellCatalog["elevation"] | undefined;
	let admin: TerminalAdminState = "hidden";
	if (elevation?.available) admin = "available";
	else if (elevation?.reason === "remote") admin = "remote";
	else if (elevation?.reason === "desktop-only") admin = "desktop-only";
	return { shells, adminShells: shells.filter((shell) => shell.detected), admin };
}

/**
 * A user row for the default names no shell, so it opens exactly what the
 * shortcut opens: the saved default (or a remote host's own). An administrator
 * row always names its detected shell, which is all the daemon accepts there.
 */
export function rowChoice(shell: TerminalMenuShell, elevated: boolean): TerminalChoice {
	if (elevated) return { shell: shell.id, elevated: true };
	return shell.isDefault ? {} : { shell: shell.id };
}

/** A failed open, in words: the administrator refusals get their own. */
export function shellOpenErrorMessage(error: unknown, t: TFunction): string {
	switch (apiErrorCode(error)) {
		case "ELEVATION_DECLINED":
			return t("terminal.adminDeclined");
		case "ELEVATED_TERMINAL_REMOTE":
			return t("terminal.adminRemoteRefused");
		case "ELEVATED_TERMINAL_DESKTOP_ONLY":
			return t("terminal.adminDesktopRefused");
		default:
			return apiErrorMessage(error);
	}
}

const NOTICE_MS = 8000;

type NewTerminalMenuProps = {
	/** The daemon that owns the session; its shells are listed. */
	hostId?: HostId;
	onOpen: (choice: TerminalChoice) => void;
	/** Why the last open failed, shown under the button until dismissed. */
	failure?: string;
	/** Changes with every failed open so a repeat failure shows again. */
	failureKey?: number;
	/** Show the new-terminal shortcut in the tooltip, where that shortcut works. */
	shortcutHint?: boolean;
};

/**
 * The tab strip's "+": a menu of the session host's shells, each as user, and
 * on Windows as administrator. Administrator rows open only on this computer;
 * a remote host shows them disabled, macOS and Linux do not show them.
 */
export function NewTerminalMenu({ hostId, onOpen, failure, failureKey, shortcutHint = false }: NewTerminalMenuProps) {
	const { t } = useTranslation();
	const remote = Boolean(hostId && hostId !== LOCAL_HOST);
	const catalog = useTerminalShells(hostId);
	const preference = useTerminalShellStore((state) => state.preference);
	const loadPreference = useTerminalShellStore((state) => state.load);
	useEffect(() => {
		if (!remote) void loadPreference();
	}, [loadPreference, remote]);
	const model = useMemo(
		() => terminalMenuModel(catalog.data, remote ? undefined : terminalShellRequestValue(preference)),
		[catalog.data, preference, remote],
	);

	const [noticeOpen, setNoticeOpen] = useState(false);
	useEffect(() => {
		if (!failure || !failureKey) return;
		setNoticeOpen(true);
		const timer = window.setTimeout(() => setNoticeOpen(false), NOTICE_MS);
		return () => window.clearTimeout(timer);
	}, [failure, failureKey]);

	const adminReason =
		model.admin === "remote" ? t("terminal.adminOnlyAtComputer") : model.admin === "desktop-only" ? t("terminal.adminNeedsDesktop") : undefined;

	return (
		<Popover open={noticeOpen && Boolean(failure)} onOpenChange={setNoticeOpen}>
			<DropdownMenu onOpenChange={(open) => open && setNoticeOpen(false)}>
				<Tooltip>
					<TooltipTrigger asChild>
						<PopoverAnchor asChild>
							<DropdownMenuTrigger asChild>
								<Button
									aria-label={t("terminal.new")}
									data-terminal-focus-handoff="true"
									size="icon-sm"
									type="button"
									variant="quiet"
								>
									<Plus aria-hidden="true" className="size-icon-md" />
								</Button>
							</DropdownMenuTrigger>
						</PopoverAnchor>
					</TooltipTrigger>
					<TooltipContent shortcut={shortcutHint ? "new-shell-terminal" : undefined} side="bottom">
						{t("terminal.new")}
					</TooltipContent>
				</Tooltip>
				<DropdownMenuContent align="start" className="min-w-56" data-testid="new-terminal-menu">
					{model.shells.length === 0 ? (
						<DropdownMenuItem onSelect={() => onOpen({})}>
							<SquareTerminal aria-hidden="true" />
							{t("terminal.defaultShell")}
						</DropdownMenuItem>
					) : (
						model.shells.map((shell) => (
							<DropdownMenuItem key={shell.id} onSelect={() => onOpen(rowChoice(shell, false))}>
								<SquareTerminal aria-hidden="true" />
								<span className="truncate">{shell.name}</span>
								{shell.isDefault ? <span className="ml-auto pl-4 text-caption text-passive">{t("terminal.shellDefault")}</span> : null}
							</DropdownMenuItem>
						))
					)}
					{model.admin === "hidden" || model.adminShells.length === 0 ? null : (
						<>
							<DropdownMenuSeparator />
							{model.admin === "available" ? (
								<DropdownMenuSub>
									<DropdownMenuSubTrigger>
										<ShieldCheck aria-hidden="true" className="text-status-in-review!" />
										{t("terminal.runAsAdministrator")}
									</DropdownMenuSubTrigger>
									<DropdownMenuSubContent className="min-w-48" data-testid="new-admin-terminal-menu">
										{model.adminShells.map((shell) => (
											<DropdownMenuItem
												aria-label={t("terminal.shellAsAdministrator", { shell: shell.name })}
												key={shell.id}
												onSelect={() => onOpen(rowChoice(shell, true))}
											>
												<ShieldCheck aria-hidden="true" className="text-status-in-review!" />
												<span className="truncate">{shell.name}</span>
											</DropdownMenuItem>
										))}
									</DropdownMenuSubContent>
								</DropdownMenuSub>
							) : (
								<DropdownMenuItem aria-describedby="admin-terminal-unavailable" className="items-start" disabled>
									<ShieldCheck aria-hidden="true" className="mt-0.5" />
									<span className="flex min-w-0 flex-col">
										<span>{t("terminal.runAsAdministrator")}</span>
										<span className="text-caption text-passive" id="admin-terminal-unavailable">
											{adminReason}
										</span>
									</span>
								</DropdownMenuItem>
							)}
						</>
					)}
				</DropdownMenuContent>
			</DropdownMenu>
			<PopoverContent align="start" className="max-w-72 px-3 py-2 text-sm" role="status" side="bottom">
				{failure}
			</PopoverContent>
		</Popover>
	);
}
