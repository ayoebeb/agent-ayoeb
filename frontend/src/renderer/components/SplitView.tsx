import { Fragment, useCallback, useState, type ReactNode } from "react";
import { agentLabel } from "@aoagents/product-ui";
import { X } from "@/components/app-icons";
import { useTranslation } from "react-i18next";
import { beginPaneTabDrag } from "../lib/pane-drag";
import { clampTerminalFontSize, initialTerminalFontSize, terminalFontSizeStorageKey } from "../lib/terminal-font-size";
import { cn } from "../lib/utils";
import type { Theme } from "../stores/ui-store";
import type { TerminalTarget } from "../types/terminal";
import type { WorkspaceSession } from "../types/workspace";
import { useShellAgentStore, useShellAgentStream } from "../stores/shell-agent-store";
import { AgentAvatar } from "./AgentAvatar";
import { ShellAgentStatus } from "./ShellTerminalTab";
import { TerminalPane } from "./TerminalPane";
import { Pressable } from "./ui/pressable";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { TabContextMenu, type TabMenuSpec } from "./TabContextMenu";
import { terminalTabActionClassName } from "./TerminalTabFrame";

/**
 * A terminal shown in a split pane. Shell terminals come from the shared
 * cache, so moving one into or around a split re-parents the live xterm
 * instead of remounting it.
 */
export function SplitPaneTerminal({
	session,
	target,
	theme,
	daemonReady,
	createMux,
	focusRequested,
}: {
	session?: WorkspaceSession;
	target: TerminalTarget;
	theme: Theme;
	daemonReady: boolean;
	createMux?: Parameters<typeof TerminalPane>[0]["createMux"];
	focusRequested: boolean;
}) {
	const [fontSize, setFontSize] = useState(initialTerminalFontSize);
	const changeFontSize = useCallback((delta: number) => {
		setFontSize((current) => {
			const next = clampTerminalFontSize(current + delta);
			window.localStorage?.setItem(terminalFontSizeStorageKey, String(next));
			return next;
		});
	}, []);
	return (
		<TerminalPane
			createMux={createMux}
			daemonReady={daemonReady}
			focusRequested={focusRequested}
			fontSize={fontSize}
			onChangeFontSize={changeFontSize}
			session={session}
			terminalTarget={target}
			theme={theme}
		/>
	);
}

export { SplitDivider } from "./SplitDivider";

export type SplitGroupMember = {
	key: string;
	label: string;
	icon: ReactNode;
	onSelect: () => void;
	/** Closes the member's pane (a member that cannot close leaves the group). */
	onClose: () => void;
	/** A shell whose running agent should show, as on its strip tab. */
	agentHandleId?: string;
};

/**
 * One strip tab for tabs shown together. The focused member shows its name;
 * the others show only their icon, named by a tooltip. Each member is its own
 * target: clicking it shows the group with that pane focused, and dragging it
 * moves it within the split or out of the group. Hovering the tab swaps the
 * focused member's icon for a close button, as on other tabs, and its menu
 * (`useTabMenuItems`) ungroups it, followed by the focused member's own rows.
 */
export function SplitGroupTab({
	active,
	focusedKey,
	members,
	menu,
}: {
	active: boolean;
	focusedKey?: string;
	members: SplitGroupMember[];
	menu: TabMenuSpec & { kind: "group" };
}) {
	const { t } = useTranslation();
	// A shell in a split has no strip tab of its own, so the group tab keeps
	// the agent watch open and shows the agent as that tab would.
	useShellAgentStream(members.some((member) => member.agentHandleId));
	const agents = useShellAgentStore((state) => state.agents);
	const shown = members.map((member) => {
		const agent = member.agentHandleId ? agents[member.agentHandleId] : undefined;
		if (!agent) return { ...member, agent };
		// A default-named shell ("Terminal 3") reads as its agent.
		const label = /^Terminal \d+$/.test(member.label) ? agentLabel(agent.harness) : member.label;
		return { ...member, agent, label, icon: <AgentAvatar className="size-icon-sm!" decorative provider={agent.harness} /> };
	});
	const frame = (
		<span
			aria-label={t("splitPane.group", { names: shown.map((member) => member.label).join(", ") })}
			className={cn(
				"group relative inline-flex h-full shrink-0 self-stretch items-stretch border-r border-border",
				active ? "bg-overlay text-foreground" : "text-passive hover:bg-raised",
			)}
			// The floating top bar draws the selected tab's pill from data-active.
			data-active={active || undefined}
			data-split-group-tab
			data-terminal-tab-frame
			role="group"
		>
			{shown.map((member, index) => {
				const focused = member.key === focusedKey;
				const segment = (
					<Pressable
						aria-label={member.label}
						aria-selected={active && focused}
						className={cn(
							"inline-flex h-full min-w-0 cursor-pointer items-center gap-2 leading-none",
							focused ? "max-w-shell-tab-max px-2 text-control" : "px-1.5",
							active && focused ? "text-foreground" : active ? "text-muted-foreground hover:text-foreground" : "hover:text-foreground",
						)}
						data-split-member={member.key}
						onClick={(event) => {
							if (event.detail > 1) return;
							member.onSelect();
						}}
						onPointerDown={(event) => {
							// A member drags on its own; the group tab itself stays in place.
							event.stopPropagation();
							beginPaneTabDrag(event, member.key, event.currentTarget);
						}}
						role="tab"
						tabIndex={active && focused ? 0 : -1}
						type="button"
					>
						<span
							className={cn(
								"pointer-events-none grid size-icon-base shrink-0 place-items-center",
								focused && "group-hover:opacity-0 group-has-[:focus-visible]:opacity-0",
							)}
						>
							{member.icon}
						</span>
						{focused ? <span className="truncate">{member.label}</span> : null}
						{focused && member.agent ? <ShellAgentStatus agent={member.agent} done={false} /> : null}
					</Pressable>
				);
				return (
					<Fragment key={member.key}>
						{index > 0 ? (
							<span aria-hidden="true" className="my-2.5 w-px shrink-0 self-stretch bg-border" />
						) : null}
						{focused ? (
							<span className="relative inline-flex min-w-0">
								{segment}
								{/* Takes the icon's slot on hover, like the close button on other tabs. */}
								<span
									className="pointer-events-none absolute left-2 top-1/2 grid size-icon-base -translate-y-1/2 place-items-center opacity-0 group-hover:pointer-events-auto group-hover:opacity-100 group-has-[:focus-visible]:pointer-events-auto group-has-[:focus-visible]:opacity-100"
									data-terminal-tab-action
								>
									<Pressable
										aria-label={t("splitPane.closePane", { name: member.label })}
										className={terminalTabActionClassName}
										onClick={(event) => {
											event.stopPropagation();
											member.onClose();
										}}
										onPointerDown={(event) => event.stopPropagation()}
										type="button"
									>
										<X aria-hidden="true" className="size-icon-sm" />
									</Pressable>
								</span>
							</span>
						) : (
							<Tooltip>
								<TooltipTrigger asChild>{segment}</TooltipTrigger>
								<TooltipContent side="bottom">{member.label}</TooltipContent>
							</Tooltip>
						)}
					</Fragment>
				);
			})}
			{active ? (
				<span
					aria-hidden="true"
					className="pointer-events-none absolute inset-x-0 bottom-0 h-0.5 bg-foreground/80"
					data-testid="active-terminal-tab-indicator"
				/>
			) : null}
		</span>
	);
	return <TabContextMenu spec={menu}>{frame}</TabContextMenu>;
}
