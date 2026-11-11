import { render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppMenuActionId } from "../../shared/shortcuts";
import { SessionPaneContext, type SessionPaneValue } from "../components/session-pane-context";
import { useSessionMenuAction } from "./useAppMenuCommand";

function OpenInFileManager({ onAction }: { onAction: () => void }) {
	useSessionMenuAction("open-in-file-manager", onAction);
	return null;
}

function pane(paneKey: string, focused: boolean): SessionPaneValue {
	return { paneKey, focused, rootRef: { current: null }, keepsSplit: () => true };
}

function captureMenuListeners() {
	const listeners: Array<(action: AppMenuActionId) => void> = [];
	vi.spyOn(window.ao!.app, "onMenuAction").mockImplementation((listener) => {
		listeners.push(listener);
		return () => undefined;
	});
	return (action: AppMenuActionId) => listeners.forEach((listener) => listener(action));
}

describe("useSessionMenuAction", () => {
	afterEach(() => vi.restoreAllMocks());

	it("runs a menu action once, in the focused pane of a split", () => {
		const choose = captureMenuListeners();
		const unfocused = vi.fn();
		const focused = vi.fn();
		render(
			<>
				<SessionPaneContext.Provider value={pane("session:a", false)}>
					<OpenInFileManager onAction={unfocused} />
				</SessionPaneContext.Provider>
				<SessionPaneContext.Provider value={pane("session:b", true)}>
					<OpenInFileManager onAction={focused} />
				</SessionPaneContext.Provider>
			</>,
		);

		choose("open-in-file-manager");
		expect(focused).toHaveBeenCalledTimes(1);
		expect(unfocused).not.toHaveBeenCalled();
	});

	it("follows focus moving to another pane", () => {
		const choose = captureMenuListeners();
		const first = vi.fn();
		const second = vi.fn();
		const view = (focusFirst: boolean) => (
			<>
				<SessionPaneContext.Provider value={pane("session:a", focusFirst)}>
					<OpenInFileManager onAction={first} />
				</SessionPaneContext.Provider>
				<SessionPaneContext.Provider value={pane("session:b", !focusFirst)}>
					<OpenInFileManager onAction={second} />
				</SessionPaneContext.Provider>
			</>
		);
		const { rerender } = render(view(true));
		rerender(view(false));

		choose("open-in-file-manager");
		expect(second).toHaveBeenCalledTimes(1);
		expect(first).not.toHaveBeenCalled();
	});

	it("runs outside a split as before", () => {
		const choose = captureMenuListeners();
		const handler = vi.fn();
		render(<OpenInFileManager onAction={handler} />);

		choose("open-in-file-manager");
		choose("toggle-notifications");
		expect(handler).toHaveBeenCalledTimes(1);
	});
});
