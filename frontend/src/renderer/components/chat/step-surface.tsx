/**
 * The one surface an open list of steps sits on.
 *
 * A turn's steps are rows of one list, not a card each: a fill that lifts the list off
 * the canvas, hairlines between the rows, and nothing drawn inside it that has its own
 * outline or fill. Everything under it asks `useInStepSurface()` to know it is a row of
 * that list, because a row in the list says more than a row on its own: it names the
 * command that ran and how long it took, where a lone step in the conversation stays a
 * calm single line.
 */

import { createContext, useContext, type ReactNode } from "react";
import { cn } from "../../lib/utils";

const StepSurfaceContext = createContext(false);

/** True under a `StepSurface`: the caller is a row of the list, not a step on its own. */
export function useInStepSurface(): boolean {
	return useContext(StepSurfaceContext);
}

/** Rows breathe a little more than a bare line, and the same for every row, however deep. */
const STEP_ROW_PADDING = "[&_.activity-summary-button]:px-2.5 [&_.activity-summary-button]:py-1.5";

/** The list's own classes: rows, hairlines between them, one fill. */
export const STEP_LIST_CLASS = cn("flex flex-col divide-y divide-border/50", STEP_ROW_PADDING);

export function StepSurface({ children, className }: { children: ReactNode; className?: string }) {
	return (
		<StepSurfaceContext.Provider value>
			<div className={cn(STEP_LIST_CLASS, "min-w-0 overflow-hidden rounded-lg bg-surface", className)} data-step-surface="">
				{children}
			</div>
		</StepSurfaceContext.Provider>
	);
}
