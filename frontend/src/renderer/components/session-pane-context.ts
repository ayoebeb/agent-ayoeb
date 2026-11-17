import { createContext, useCallback, useContext, useRef, type ReactNode, type RefObject } from "react";

/**
 * A session shown as one pane of a side-by-side split. Absent for the normal
 * single session view, where everything behaves as before.
 */
export type SessionPaneValue = {
	paneKey: string;
	/** The pane takes keyboard input: its shortcuts, menus and autofocus are live. */
	focused: boolean;
	/** The pane's own element, so DOM lookups stay inside it. */
	rootRef: RefObject<HTMLElement | null>;
	/** Whether navigating to this session keeps the split, and so this pane, on screen. */
	keepsSplit: (sessionId: string | undefined, hostId?: string) => boolean;
};

export const SessionPaneContext = createContext<SessionPaneValue | null>(null);

export function useSessionPane(): SessionPaneValue | null {
	return useContext(SessionPaneContext);
}

/**
 * True unless this is a pane without focus, read when called: for listeners
 * registered once (tab cycling, ⌘W) that must leave the other panes alone.
 */
export function useSessionPaneFocusedGetter(): () => boolean {
	const pane = useContext(SessionPaneContext);
	const focusedRef = useRef(true);
	focusedRef.current = pane ? pane.focused : true;
	return useCallback(() => focusedRef.current, []);
}

/** The scope for document lookups made on this session's behalf. */
export function useSessionPaneRoot(): () => ParentNode {
	const pane = useContext(SessionPaneContext);
	const rootRef = pane?.rootRef;
	return useCallback(() => rootRef?.current ?? document, [rootRef]);
}

/**
 * What a pane adds to the session's own top bar, which doubles as the pane
 * header: the status glyph and project before the tabs, close after the
 * actions. Its own context, so a status change re-renders only the bar.
 */
export type SessionPaneChrome = { leading?: ReactNode; trailing?: ReactNode };

export const SessionPaneChromeContext = createContext<SessionPaneChrome | null>(null);

export function useSessionPaneChrome(): SessionPaneChrome | null {
	return useContext(SessionPaneChromeContext);
}
