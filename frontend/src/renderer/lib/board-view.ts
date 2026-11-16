/** Which board a project shows: its agents, or its Jira tickets. */
export type BoardView = "agents" | "jira";

const KEY_PREFIX = "ao.board-view:";

/** The view last chosen for a project's board; agents until the person picks Jira. */
export function readBoardView(scopeKey: string): BoardView {
	try {
		return window.localStorage.getItem(KEY_PREFIX + scopeKey) === "jira" ? "jira" : "agents";
	} catch {
		return "agents";
	}
}

export function writeBoardView(scopeKey: string, view: BoardView): void {
	try {
		if (view === "jira") window.localStorage.setItem(KEY_PREFIX + scopeKey, view);
		else window.localStorage.removeItem(KEY_PREFIX + scopeKey);
	} catch {
		// A blocked store only means the choice is not remembered.
	}
}
