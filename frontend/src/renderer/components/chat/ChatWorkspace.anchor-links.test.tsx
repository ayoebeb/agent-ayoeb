import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { chatFixtureLongHistory } from "../../lib/chat-fixture";
import { renderChat as render } from "../../test/render-chat";
import { useUiStore } from "../../stores/ui-store";
import type { ConversationMessage, ConversationSnapshot } from "../../types/conversation";
import { ChatWorkspace } from "./ChatWorkspace";

// A long chat whose newest message links to headings in much older ones: the
// link must bring an old, unmounted message into view and mark the heading.

vi.mock("../NotificationCenter", () => ({ NotificationCenter: () => <button type="button" aria-label="Notifications" /> }));

let restoreGeometry: (() => void) | undefined;

/** Browser geometry for the virtualized timeline, with headings placed where the scroll wants them. */
function stubGeometry() {
	const bounds = HTMLElement.prototype.getBoundingClientRect;
	const clientHeight = Object.getOwnPropertyDescriptor(Element.prototype, "clientHeight")!.get!;
	const scrollHeight = Object.getOwnPropertyDescriptor(Element.prototype, "scrollHeight")!.get!;
	const offsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight")!.get!;
	const spies = [
		vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
			const log = this.closest<HTMLElement>('[role="log"]');
			if (this.hasAttribute("data-index")) {
				const offset = this.style.transform
					? Number(this.style.transform.match(/translateY\(([-\d.]+)px\)/)?.[1] ?? 0)
					: Number(this.dataset.index) * 618;
				return { ...bounds.call(this), top: 20 + offset - (log?.scrollTop ?? 0), height: 600, width: 768 } as DOMRect;
			}
			// An anchored heading already sits where the scroll lands it (a fifth of the viewport, capped).
			if (this.id.startsWith("chat-anchor-")) return { ...bounds.call(this), top: 96, height: 20, width: 700 } as DOMRect;
			if (this.classList.contains("relative") && this.style.height) {
				return { ...bounds.call(this), top: 20 - (log?.scrollTop ?? 0), height: Number.parseFloat(this.style.height), width: 768 } as DOMRect;
			}
			return bounds.call(this);
		}),
		vi.spyOn(Element.prototype, "clientHeight", "get").mockImplementation(function (this: Element) {
			return this.getAttribute("role") === "log" ? 800 : clientHeight.call(this);
		}),
		vi.spyOn(Element.prototype, "scrollHeight", "get").mockImplementation(function (this: Element) {
			if (this.getAttribute("role") !== "log") return scrollHeight.call(this);
			return 20 + Array.from(this.querySelectorAll<HTMLElement>('[style*="height"]'))
				.reduce((sum, node) => sum + (Number.parseFloat(node.style.height) || 0), 0);
		}),
		vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
			return this.hasAttribute("data-index") ? 600 : Number.parseFloat(this.style.height) || offsetHeight.call(this);
		}),
	];
	restoreGeometry = () => spies.forEach((spy) => spy.mockRestore());
}

const heading = (anchor: string) => document.getElementById(`chat-anchor-${anchor}`);
const reply = (snapshot: ConversationSnapshot, id: string) =>
	snapshot.items.find((item): item is ConversationMessage => item.kind === "message" && item.id === id)!;

/** 100 turns; turn 5 and 6 each answer under "Which database?", the newest links to both. */
function longChat(): ConversationSnapshot {
	const snapshot = chatFixtureLongHistory(100);
	reply(snapshot, "hr-5").text = "## Which database?\n\nPostgres or SQLite for the cache?";
	reply(snapshot, "hr-6").text = "## Which database?\n\nA second question under the same heading.";
	reply(snapshot, "hr-99").text = [
		"## What you need to do",
		"",
		"- [Which database?](#which-database)",
		"- [The second one](#which-database-1)",
		"- [Nowhere](#no-such-heading)",
	].join("\n");
	return snapshot;
}

/** The chat as first opened: only the newest turns, with earlier ones a page away. */
function newestOnly(snapshot: ConversationSnapshot, firstTurn: number): ConversationSnapshot {
	const older = new Set(snapshot.turns.slice(0, firstTurn).map((turn) => turn.id));
	return {
		...snapshot,
		hasMoreBefore: true,
		turns: snapshot.turns.slice(firstTurn),
		items: snapshot.items.filter((item) => !older.has(item.turnId!)),
	};
}

const scrollTo = (log: HTMLElement) => fireEvent.scroll(log);

describe("anchor links in a long chat", () => {
	beforeEach(() => {
		stubGeometry();
		useUiStore.setState({ chatAnchorRequest: null, globalToasts: [], globalToast: null });
	});
	afterEach(() => {
		restoreGeometry?.();
		restoreGeometry = undefined;
	});

	it("scrolls to the heading in an older message that is not on screen, and marks it", async () => {
		render(<ChatWorkspace snapshot={longChat()} />);
		const log = screen.getByRole("log");
		expect(heading("which-database")).toBeNull();
		const link = await screen.findByRole("link", { name: "Which database?" });
		const before = log.scrollTop;

		await userEvent.click(link);
		await waitFor(() => {
			scrollTo(log);
			expect(heading("which-database")).not.toBeNull();
		});
		expect(log.scrollTop).toBeLessThan(before);
		await waitFor(() => expect(heading("which-database")).toHaveAttribute("data-anchor-flash"));
		expect(heading("which-database")).toHaveTextContent("Which database?");
		// The mark is a brief cue, not a state.
		await waitFor(() => expect(heading("which-database")).not.toHaveAttribute("data-anchor-flash"), { timeout: 4_000 });
	});

	it("goes to the right heading when two share a title", async () => {
		render(<ChatWorkspace snapshot={longChat()} />);
		const log = screen.getByRole("log");
		await userEvent.click(await screen.findByRole("link", { name: "The second one" }));
		await waitFor(() => {
			scrollTo(log);
			expect(heading("which-database-1")).not.toBeNull();
		});
		// The second heading lives in turn 6's reply, not turn 5's.
		expect(heading("which-database-1")?.closest("[data-chat-turn]")?.getAttribute("data-chat-turn")).toMatch(/^turn-h6\b/);
		await waitFor(() => expect(heading("which-database-1")).toHaveAttribute("data-anchor-flash"));
	});

	it("loads older messages until the heading appears, then goes to it", async () => {
		const full = longChat();
		const recent = newestOnly(full, 20);
		const onLoadOlder = vi.fn();
		const view = render(<ChatWorkspace snapshot={recent} hasOlder onLoadOlder={onLoadOlder} />);
		const log = screen.getByRole("log");
		await userEvent.click(await screen.findByRole("link", { name: "Which database?" }));
		await waitFor(() => expect(onLoadOlder).toHaveBeenCalledTimes(1));
		// Nothing yet: the heading is in a page that has not arrived.
		expect(heading("which-database")).toBeNull();
		expect(useUiStore.getState().globalToasts).toEqual([]);

		view.rerender(<ChatWorkspace snapshot={full} onLoadOlder={onLoadOlder} />);
		await waitFor(() => {
			scrollTo(log);
			expect(heading("which-database")).not.toBeNull();
		});
		await waitFor(() => expect(heading("which-database")).toHaveAttribute("data-anchor-flash"));
		expect(onLoadOlder).toHaveBeenCalledTimes(1);
	});

	it("says so, and does nothing else, when the anchor is not in the chat", async () => {
		render(<ChatWorkspace snapshot={longChat()} />);
		const log = screen.getByRole("log");
		const link = await screen.findByRole("link", { name: "Nowhere" });
		const before = log.scrollTop;
		const hashBefore = window.location.hash;

		await userEvent.click(link);
		await waitFor(() => expect(useUiStore.getState().globalToasts).toEqual([
			expect.objectContaining({
				title: "That part of the conversation could not be found.",
				placement: "top-center",
				dismissible: true,
				dedupeKey: "chat-anchor:missing",
			}),
		]));
		expect(log.scrollTop).toBe(before);
		expect(window.location.hash).toBe(hashBefore);
		expect(document.querySelector("[data-anchor-flash]")).toBeNull();
	});

	it("asks for older pages first, then says so when they do not hold it", async () => {
		const recent = newestOnly(longChat(), 20);
		const onLoadOlder = vi.fn();
		const view = render(<ChatWorkspace snapshot={recent} hasOlder onLoadOlder={onLoadOlder} />);
		await userEvent.click(await screen.findByRole("link", { name: "Nowhere" }));
		await waitFor(() => expect(onLoadOlder).toHaveBeenCalledTimes(1));
		expect(useUiStore.getState().globalToasts).toEqual([]);

		// The page that came back added nothing: stop asking.
		view.rerender(<ChatWorkspace snapshot={recent} hasOlder onLoadOlder={() => onLoadOlder()} />);
		await waitFor(() => expect(useUiStore.getState().globalToasts).toHaveLength(1));
		expect(onLoadOlder).toHaveBeenCalledTimes(1);
	});

	it("follows a link from another session once this chat is on screen, and only once", async () => {
		useUiStore.getState().requestChatAnchor("ao-long", "which-database");
		render(<ChatWorkspace snapshot={longChat()} />);
		const log = screen.getByRole("log");
		await waitFor(() => {
			scrollTo(log);
			expect(heading("which-database")).not.toBeNull();
		});
		await waitFor(() => expect(heading("which-database")).toHaveAttribute("data-anchor-flash"));
		expect(useUiStore.getState().chatAnchorRequest).toBeNull();
	});

	it("ignores a request for another session's chat and one that has gone stale", async () => {
		useUiStore.getState().requestChatAnchor("some-other-session", "which-database");
		const view = render(<ChatWorkspace snapshot={longChat()} />);
		expect(useUiStore.getState().chatAnchorRequest).toEqual(expect.objectContaining({ sessionId: "some-other-session" }));
		view.unmount();

		useUiStore.setState({ chatAnchorRequest: { sessionId: "ao-long", anchor: "which-database", nonce: 9, at: Date.now() - 60_000 } });
		render(<ChatWorkspace snapshot={longChat()} />);
		await waitFor(() => expect(useUiStore.getState().chatAnchorRequest).toBeNull());
		expect(heading("which-database")).toBeNull();
		expect(document.querySelector("[data-anchor-flash]")).toBeNull();
	});
});
