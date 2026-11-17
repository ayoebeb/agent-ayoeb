import { useCallback, useMemo } from "react";
import { sessionUiKey } from "../lib/hosts";
import {
	DEFAULT_CONTROL_ROOM,
	useUiStore,
	type ControlRoomState,
	type ControlRoomTab,
} from "../stores/ui-store";

export type ControlRoom = ControlRoomState & {
	/** Show a tab, keeping the worker the tabs are scoped to. */
	setTab: (tab: ControlRoomTab) => void;
	/** Scope every tab to one worker, or back to all agents with undefined. */
	setScope: (scope: string | undefined) => void;
	/** Jump to a tab scoped to one worker: what a crew row's quick actions do. */
	show: (tab: ControlRoomTab, scope?: string) => void;
};

/** The orchestrator's control room: which tab shows and which worker it is scoped to. */
export function useControlRoom(orchestrator: { id: string; hostId?: string } | undefined): ControlRoom {
	const key = orchestrator ? sessionUiKey(orchestrator.id, orchestrator.hostId) : "";
	const state = useUiStore((store) => store.controlRooms[key] ?? DEFAULT_CONTROL_ROOM);
	const setControlRoom = useUiStore((store) => store.setControlRoom);
	const setTab = useCallback((tab: ControlRoomTab) => key && setControlRoom(key, { tab }), [key, setControlRoom]);
	const setScope = useCallback((scope: string | undefined) => key && setControlRoom(key, { scope: scope ?? null }), [key, setControlRoom]);
	const show = useCallback(
		(tab: ControlRoomTab, scope?: string) => key && setControlRoom(key, { tab, scope: scope ?? null }),
		[key, setControlRoom],
	);
	return useMemo(() => ({ ...state, setTab, setScope, show }), [setScope, setTab, show, state]);
}
