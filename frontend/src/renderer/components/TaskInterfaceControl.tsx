import { ToggleGroup } from "radix-ui";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../lib/utils";
import { RoundedInterfaceIcon } from "./SessionInterfaceSwitch";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./ui/tooltip";

export type TaskInterface = "chat" | "tui";

export type TaskInterfaceControlProps = {
	value: TaskInterface;
	/** Why the agent starts only in `value`; the other choice is then unavailable. */
	lockReason?: string;
	disabled?: boolean;
	onChange?: (value: TaskInterface) => void;
	className?: string;
};

const CHOICES: readonly TaskInterface[] = ["chat", "tui"];

/**
 * Chat or Terminal for the task about to start, beside the agent and model
 * pickers. Tab reaches the selected choice, arrow keys move between the two,
 * and Space or Enter picks one.
 */
export function TaskInterfaceControl({ value, lockReason, disabled, onChange, className }: TaskInterfaceControlProps) {
	const { t } = useTranslation();
	const reasonId = useId();
	return (
		<TooltipProvider>
			<Tooltip>
				<TooltipTrigger asChild>
					<span className={cn("inline-flex shrink-0", className)}>
						<ToggleGroup.Root
							type="single"
							value={value}
							// Picking the selected choice again would clear it; there is always one.
							onValueChange={(next) => {
								if ((next === "chat" || next === "tui") && next !== value) onChange?.(next);
							}}
							disabled={disabled}
							aria-label={t("newTask.interface.label")}
							aria-describedby={lockReason ? reasonId : undefined}
							className="inline-flex h-7 items-center gap-0.5"
						>
							{CHOICES.map((choice) => (
								<ToggleGroup.Item key={choice} value={choice} disabled={Boolean(lockReason) && choice !== value} asChild>
									<Button variant="quiet" size="sm" data-active={choice === value} className="rounded-lg">
										<RoundedInterfaceIcon target={choice} className="size-3.5" />
										{choice === "chat" ? t("newTask.interface.chat") : t("newTask.interface.terminal")}
									</Button>
								</ToggleGroup.Item>
							))}
						</ToggleGroup.Root>
						{lockReason ? <span id={reasonId} className="sr-only">{lockReason}</span> : null}
					</span>
				</TooltipTrigger>
				{lockReason ? <TooltipContent side="top">{lockReason}</TooltipContent> : null}
			</Tooltip>
		</TooltipProvider>
	);
}
