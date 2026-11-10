import { create } from "zustand";
import type { SplitSide } from "../lib/pane-split";
import {
	closeSplitPane,
	focusSplitPane,
	openSplit,
	parseStoredSplit,
	resizeSplit,
	splitPaneBeside,
	type SessionPaneRef,
	type SessionSplit,
	type SplitBesideResult,
} from "../lib/session-split";

export const SESSION_SPLIT_STORAGE_KEY = "ao.sessionSplit";

function readStoredSplit(): SessionSplit | null {
	try {
		return parseStoredSplit(window.localStorage?.getItem(SESSION_SPLIT_STORAGE_KEY));
	} catch {
		return null;
	}
}

function writeStoredSplit(split: SessionSplit | null): void {
	try {
		if (split) window.localStorage?.setItem(SESSION_SPLIT_STORAGE_KEY, JSON.stringify(split));
		else window.localStorage?.removeItem(SESSION_SPLIT_STORAGE_KEY);
	} catch {
		// Storage full or unavailable: the split still works for this run.
	}
}

type SessionSplitState = {
	/** Sessions shown side by side; null shows one session at a time. */
	split: SessionSplit | null;
	/** Replaces the split with these panes, in order. False when fewer than two or more than four. */
	open: (refs: readonly SessionPaneRef[], focus?: string) => boolean;
	splitBeside: (anchor: SessionPaneRef, ref: SessionPaneRef, side: SplitSide) => SplitBesideResult["outcome"];
	close: (key: string) => void;
	resize: (path: string, ratio: number) => void;
	focus: (key: string) => void;
	clear: () => void;
};

/**
 * The one split of sessions, remembered across reloads. The shell shows it
 * while the route points at one of its panes (see LocalSessionHost), so
 * opening any other session leaves it for later and coming back restores it.
 */
export const useSessionSplitStore = create<SessionSplitState>((set, get) => {
	const commit = (split: SessionSplit | null) => {
		if (split === get().split) return;
		set({ split });
		writeStoredSplit(split);
	};
	return {
		split: readStoredSplit(),
		open: (refs, focus) => {
			const split = openSplit(refs, focus);
			if (!split) return false;
			commit(split);
			return true;
		},
		splitBeside: (anchor, ref, side) => {
			const result = splitPaneBeside(get().split, anchor, ref, side);
			commit(result.split);
			return result.outcome;
		},
		close: (key) => {
			const split = get().split;
			if (split) commit(closeSplitPane(split, key));
		},
		resize: (path, ratio) => {
			const split = get().split;
			if (split) commit(resizeSplit(split, path, ratio));
		},
		focus: (key) => {
			const split = get().split;
			if (split) commit(focusSplitPane(split, key));
		},
		clear: () => commit(null),
	};
});
