import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { TerminalShellCatalog } from "../hooks/useTerminalShells";
import { appI18n } from "../i18n";
import { useTerminalShellStore } from "../stores/terminal-shell-store";
import { NewTerminalMenu, shellOpenErrorMessage, terminalChoiceInput, terminalMenuModel } from "./NewTerminalMenu";
import { TooltipProvider } from "./ui/tooltip";

const catalogState = vi.hoisted(() => ({ data: undefined as unknown }));
vi.mock("../hooks/useTerminalShells", () => ({
	useTerminalShells: () => ({ data: catalogState.data }),
}));

const PWSH = "C:\\Program Files\\PowerShell\\7\\pwsh.exe";
const windowsShells = [
	{ id: "pwsh", name: "PowerShell", path: PWSH },
	{ id: "powershell", name: "Windows PowerShell", path: "C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe" },
	{ id: "cmd", name: "Command Prompt", path: "C:\\Windows\\system32\\cmd.exe" },
	{ id: "git-bash", name: "Git Bash", path: "C:\\Program Files\\Git\\bin\\bash.exe" },
	{ id: "wsl", name: "WSL", path: "C:\\Windows\\system32\\wsl.exe" },
];

function windowsCatalog(elevation: TerminalShellCatalog["elevation"]): TerminalShellCatalog {
	return { default: { id: "auto", name: "PowerShell", path: PWSH }, shells: windowsShells, elevation };
}

const macCatalog: TerminalShellCatalog = {
	default: { id: "auto", name: "zsh", path: "/bin/zsh" },
	shells: [
		{ id: "/bin/bash", name: "bash", path: "/bin/bash" },
		{ id: "/opt/homebrew/bin/fish", name: "fish", path: "/opt/homebrew/bin/fish" },
		{ id: "/bin/zsh", name: "zsh", path: "/bin/zsh" },
	],
	elevation: { available: false, reason: "unsupported" },
};

function renderMenu(props: Partial<Parameters<typeof NewTerminalMenu>[0]> = {}) {
	const onOpen = vi.fn();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const wrap = (node: ReactNode) => (
		<QueryClientProvider client={client}>
			<TooltipProvider delayDuration={0}>{node}</TooltipProvider>
		</QueryClientProvider>
	);
	const view = render(wrap(<NewTerminalMenu onOpen={onOpen} {...props} />));
	return { onOpen, rerender: (next: Partial<Parameters<typeof NewTerminalMenu>[0]>) => view.rerender(wrap(<NewTerminalMenu onOpen={onOpen} {...props} {...next} />)) };
}

function openMenu() {
	fireEvent.keyDown(screen.getByRole("button", { name: "New terminal" }), { key: "ArrowDown" });
	return screen.getByTestId("new-terminal-menu");
}

function rowNames(menu: HTMLElement): string[] {
	return within(menu).getAllByRole("menuitem").map((row) => row.textContent ?? "");
}

beforeEach(() => {
	catalogState.data = undefined;
	useTerminalShellStore.setState({ preference: { kind: "auto" }, loaded: true });
});

describe("new terminal menu", () => {
	it("lists Windows shells with the default first, each also as administrator", () => {
		catalogState.data = windowsCatalog({ available: true });
		const { onOpen } = renderMenu();
		const menu = openMenu();

		expect(rowNames(menu)).toEqual([
			"PowerShellDefault",
			"Windows PowerShell",
			"Command Prompt",
			"Git Bash",
			"WSL",
			"Run as administrator",
		]);

		fireEvent.click(within(menu).getByRole("menuitem", { name: "Git Bash" }));
		expect(onOpen).toHaveBeenLastCalledWith({ shell: "git-bash" });

		openMenu();
		fireEvent.keyDown(screen.getByRole("menuitem", { name: "Run as administrator" }), { key: "ArrowRight" });
		const adminMenu = screen.getByTestId("new-admin-terminal-menu");
		expect(within(adminMenu).getAllByRole("menuitem").map((row) => row.getAttribute("aria-label"))).toEqual([
			"PowerShell as administrator",
			"Windows PowerShell as administrator",
			"Command Prompt as administrator",
			"Git Bash as administrator",
			"WSL as administrator",
		]);
		fireEvent.click(within(adminMenu).getByRole("menuitem", { name: "Command Prompt as administrator" }));
		expect(onOpen).toHaveBeenLastCalledWith({ shell: "cmd", elevated: true });
	});

	it("puts the saved default shell first", () => {
		catalogState.data = windowsCatalog({ available: true });
		useTerminalShellStore.setState({ preference: { kind: "git-bash" }, loaded: true });
		renderMenu();
		expect(rowNames(openMenu())[0]).toBe("Git BashDefault");
	});

	it("offers macOS and Linux shells as user only", () => {
		catalogState.data = macCatalog;
		renderMenu();
		const menu = openMenu();
		expect(rowNames(menu)).toEqual(["zshDefault", "bash", "fish"]);
		expect(screen.queryByText("Run as administrator")).toBeNull();
	});

	it("shows the administrator row disabled, with why, on a remote host", () => {
		catalogState.data = windowsCatalog({ available: false, reason: "remote" });
		const { onOpen } = renderMenu({ hostId: "box-b" });
		const menu = openMenu();
		const admin = within(menu).getByRole("menuitem", { name: /Run as administrator/ });
		expect(admin).toHaveAttribute("aria-disabled", "true");
		expect(admin).toHaveTextContent("Only at the computer itself");
		fireEvent.click(admin);
		expect(onOpen).not.toHaveBeenCalled();
		expect(screen.queryByTestId("new-admin-terminal-menu")).toBeNull();
	});

	it("explains a daemon this app did not start", () => {
		catalogState.data = windowsCatalog({ available: false, reason: "desktop-only" });
		renderMenu();
		const admin = within(openMenu()).getByRole("menuitem", { name: /Run as administrator/ });
		expect(admin).toHaveAttribute("aria-disabled", "true");
		expect(admin).toHaveTextContent("Needs AO started by this app");
	});

	it("still opens the default shell when the daemon lists none", () => {
		const { onOpen } = renderMenu();
		const menu = openMenu();
		expect(rowNames(menu)).toEqual(["Default shell"]);
		fireEvent.click(within(menu).getByRole("menuitem", { name: "Default shell" }));
		expect(onOpen).toHaveBeenCalledWith({});
	});

	it("shows the new-terminal shortcut in its tooltip only where the shortcut works", async () => {
		catalogState.data = windowsCatalog({ available: true });
		const view = renderMenu({ shortcutHint: true });
		fireEvent.focus(screen.getByRole("button", { name: "New terminal" }));
		const tip = await screen.findByRole("tooltip");
		expect(tip.parentElement?.querySelector('[data-slot="tooltip-shortcut"]')).not.toBeNull();
		view.rerender({ shortcutHint: false });
		expect(document.querySelector('[data-slot="tooltip-shortcut"]')).toBeNull();
	});

	it("says why an open failed, once per failure", () => {
		catalogState.data = windowsCatalog({ available: true });
		const { rerender } = renderMenu();
		expect(screen.queryByRole("status")).toBeNull();
		rerender({ failure: "Administrator access was declined. No terminal was opened.", failureKey: 1 });
		expect(screen.getByRole("status")).toHaveTextContent("Administrator access was declined. No terminal was opened.");
	});
});

describe("terminal menu model", () => {
	it("matches the automatic default by its path, ignoring Windows case and slashes", () => {
		const catalog = windowsCatalog({ available: true });
		catalog.default.path = "c:/program files/powershell/7/PWSH.EXE";
		const model = terminalMenuModel(catalog, "auto");
		expect(model.shells[0]).toEqual({ id: "pwsh", name: "PowerShell", isDefault: true, detected: true });
		expect(model.admin).toBe("available");
	});

	it("lists a custom default shell that detection does not know", () => {
		const model = terminalMenuModel(windowsCatalog({ available: true }), "D:\\tools\\nu.exe");
		expect(model.shells[0]).toEqual({ id: "D:\\tools\\nu.exe", name: "nu", isDefault: true, detected: false });
		expect(model.shells).toHaveLength(windowsShells.length + 1);
		// It opens as the user only: administrator rows list detected shells.
		expect(model.adminShells.map((shell) => shell.id)).toEqual(windowsShells.map((shell) => shell.id));
	});

	it("hides administrator rows for a daemon that predates them", () => {
		const catalog = windowsCatalog({ available: true }) as Partial<TerminalShellCatalog>;
		delete catalog.elevation;
		expect(terminalMenuModel(catalog as TerminalShellCatalog).admin).toBe("hidden");
	});
});

describe("menu choices", () => {
	it("ignores a click event passed where a choice was expected", () => {
		expect(terminalChoiceInput(new MouseEvent("click"))).toEqual({});
		expect(terminalChoiceInput(undefined)).toEqual({});
		expect(terminalChoiceInput({ shell: "pwsh", elevated: true })).toEqual({ shell: "pwsh", elevated: true });
		expect(terminalChoiceInput({ shell: "", elevated: "yes" })).toEqual({});
	});

	it("words the administrator refusals", () => {
		const t = appI18n.t.bind(appI18n);
		expect(shellOpenErrorMessage({ code: "ELEVATION_DECLINED" }, t)).toBe("Administrator access was declined. No terminal was opened.");
		expect(shellOpenErrorMessage({ code: "ELEVATED_TERMINAL_REMOTE" }, t)).toBe(
			"Administrator terminals only open at the computer itself, not remotely.",
		);
	});
});
