import { create } from "zustand";
import {
	selectRange,
	toggleSelection,
	type SelectionKind,
	type SessionPaneRef,
	type SidebarSelection,
} from "../lib/session-split";

type SidebarSelectionState = {
	selection: SidebarSelection | null;
	/** What each selected row opens as a pane, by row key. */
	refs: Record<string, SessionPaneRef>;
	/**
	 * Toggles one row (Ctrl/Cmd-click) or selects up to it (Shift-click).
	 * `seed` is the row already open, which joins a new selection. Returns
	 * false when the change would select more than four rows.
	 */
	select: (input: {
		kind: SelectionKind;
		key: string;
		ref: SessionPaneRef;
		range?: { ordered: readonly string[]; refs: Readonly<Record<string, SessionPaneRef>> };
		seed?: { key: string; ref: SessionPaneRef };
	}) => boolean;
	clear: () => void;
};

/** The sidebar's Ctrl/Shift-click selection. Not remembered across reloads. */
export const useSidebarSelectionStore = create<SidebarSelectionState>((set, get) => ({
	selection: null,
	refs: {},
	select: ({ kind, key, ref, range, seed }) => {
		const { selection, refs } = get();
		const change = range
			? selectRange(selection, kind, range.ordered, key, seed?.key)
			: toggleSelection(selection, kind, key, seed?.key);
		if (change.overflow) return false;
		const known: Record<string, SessionPaneRef> = {
			...(selection?.kind === kind ? refs : {}),
			...(seed ? { [seed.key]: seed.ref } : {}),
			...(range?.refs ?? {}),
			[key]: ref,
		};
		const next = change.selection;
		set({
			selection: next,
			refs: next ? Object.fromEntries(next.keys.flatMap((candidate) => (known[candidate] ? [[candidate, known[candidate]]] : []))) : {},
		});
		return true;
	},
	clear: () => {
		if (get().selection) set({ selection: null, refs: {} });
	},
}));

/** The selected rows' panes, in selection order. */
export function selectedPaneRefs(state: Pick<SidebarSelectionState, "selection" | "refs">): SessionPaneRef[] {
	return state.selection?.keys.flatMap((key) => (state.refs[key] ? [state.refs[key]] : [])) ?? [];
}
