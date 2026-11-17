import { describe, expect, it, vi } from "vitest";
import {
	anchorFromHref,
	assignAnchors,
	ChatAnchorStore,
	chatAnchorDomId,
	extractHeadings,
	headingSlug,
	normalizeAnchor,
} from "./chat-anchors";

describe("headingSlug", () => {
	it.each([
		["Which database?", "which-database"],
		["Welke database gebruiken we?", "welke-database-gebruiken-we"],
		["Q1: Which database should we use?", "q1-which-database-should-we-use"],
		["  Spaced   out  ", "spaced-out"],
		["Don't ship it", "dont-ship-it"],
		["snake_case & kebab-case", "snake_case-kebab-case"],
		["Café décision", "café-décision"],
		["决定: 数据库", "决定-数据库"],
		["1. First question", "1-first-question"],
	])("turns %j into %j", (text, slug) => expect(headingSlug(text)).toBe(slug));

	it("never returns an empty anchor", () => {
		expect(headingSlug("???")).toBe("section");
		expect(headingSlug("")).toBe("section");
	});
});

describe("anchorFromHref / normalizeAnchor", () => {
	it("reads a fragment href, percent-decoded and lowercased", () => {
		expect(anchorFromHref("#which-database")).toBe("which-database");
		expect(anchorFromHref("#Which-Database")).toBe("which-database");
		expect(anchorFromHref("#caf%C3%A9-d%C3%A9cision")).toBe("café-décision");
	});

	it.each(["", "#", "https://example.com/#x", "ao://sessions/p/s#x", "relative#x"])("ignores %j", (href) => {
		expect(anchorFromHref(href)).toBeUndefined();
	});

	it("keeps a malformed escape as written", () => {
		expect(normalizeAnchor("100%-sure")).toBe("100%-sure");
	});
});

describe("extractHeadings", () => {
	it("returns plain heading text with source offsets, formatting stripped", () => {
		const markdown = "Intro\n\n## **Which** `database` [now](https://x.test)?\n\nBody\n\n### Second one";
		const headings = extractHeadings(markdown);
		expect(headings.map((heading) => heading.text)).toEqual(["Which database now?", "Second one"]);
		expect(headings.map((heading) => markdown.slice(heading.offset, heading.offset + 2))).toEqual(["##", "##"]);
	});

	it("reads setext headings and headings inside lists and quotes", () => {
		expect(extractHeadings("Title\n=====\n\n> ## Quoted\n\n- ## Listed").map((heading) => heading.text))
			.toEqual(["Title", "Quoted", "Listed"]);
	});

	it("does not take a `#` line in a code fence for a heading", () => {
		const markdown = "```sh\n# not a heading\n```\n\n## Real";
		expect(extractHeadings(markdown)).toEqual([{ offset: markdown.indexOf("## Real"), text: "Real" }]);
	});

	it("finds nothing in prose without heading syntax", () => {
		expect(extractHeadings("No headings here, just words and a #hashtag.")).toEqual([]);
	});
});

describe("assignAnchors", () => {
	const message = (id: string, text: string) => ({ id, text });

	it("gives each heading its slug and says which message holds it", () => {
		const { byMessage, messageByAnchor } = assignAnchors([
			message("m1", "## Which database?\n\nPick one."),
			message("m2", "Nothing to anchor."),
			message("m3", "## Where to deploy?"),
		]);
		expect([...byMessage.get("m1")!.values()]).toEqual(["which-database"]);
		expect(byMessage.has("m2")).toBe(false);
		expect(messageByAnchor.get("which-database")).toBe("m1");
		expect(messageByAnchor.get("where-to-deploy")).toBe("m3");
	});

	it("suffixes a repeated heading, oldest first, across messages and within one", () => {
		const { byMessage, messageByAnchor } = assignAnchors([
			message("m1", "## What you need to do\n\n## Which database?"),
			message("m2", "## What you need to do\n\n## What you need to do"),
			message("m3", "## What you need to do"),
		]);
		expect([...byMessage.get("m1")!.values()]).toEqual(["what-you-need-to-do", "which-database"]);
		expect([...byMessage.get("m2")!.values()]).toEqual(["what-you-need-to-do-1", "what-you-need-to-do-2"]);
		expect([...byMessage.get("m3")!.values()]).toEqual(["what-you-need-to-do-3"]);
		expect(messageByAnchor.get("what-you-need-to-do-3")).toBe("m3");
		expect(messageByAnchor.get("what-you-need-to-do")).toBe("m1");
	});

	it("never hands out an anchor twice, even when a suffix was already a heading", () => {
		const { byMessage } = assignAnchors([
			message("m1", "## Option\n\n## Option 1\n\n## Option"),
		]);
		const anchors = [...byMessage.get("m1")!.values()];
		expect(anchors).toEqual(["option", "option-1", "option-2"]);
		expect(new Set(anchors).size).toBe(anchors.length);
	});

	it("is stable: the same chat always yields the same anchors", () => {
		const messages = [message("a", "## Same\n\n## Same"), message("b", "## Same")];
		expect(assignAnchors(messages)).toEqual(assignAnchors(messages));
	});
});

describe("ChatAnchorStore", () => {
	it("looks messages up by anchor, forgivingly", () => {
		const store = new ChatAnchorStore();
		store.update([{ id: "m1", text: "## Welke database?" }]);
		expect(store.messageFor("welke-database")).toBe("m1");
		expect(store.messageFor("Welke-Database")).toBe("m1");
		expect(store.messageFor("unknown")).toBeUndefined();
	});

	it("tells a message's subscribers only when its own anchors changed", () => {
		const store = new ChatAnchorStore();
		const listener = vi.fn();
		store.subscribe(listener);
		const first = { id: "m1", text: "## One" };
		const second = { id: "m2", text: "## Two" };
		store.update([first, second]);
		expect(listener).toHaveBeenCalledTimes(1);
		const anchors = store.anchorsFor("m1");

		// A new message with another heading leaves the earlier messages' anchors alone.
		store.update([first, second, { id: "m3", text: "## Three" }]);
		expect(store.anchorsFor("m1")).toBe(anchors);
		expect(listener).toHaveBeenCalledTimes(2);

		// The same content again changes nothing, so nothing re-renders.
		store.update([first, second, { id: "m3", text: "## Three" }]);
		expect(listener).toHaveBeenCalledTimes(2);
	});

	it("renumbers a later duplicate when an older message arrives, and says so", () => {
		const store = new ChatAnchorStore();
		const listener = vi.fn();
		store.update([{ id: "new", text: "## Same" }]);
		expect([...store.anchorsFor("new")!.values()]).toEqual(["same"]);
		store.subscribe(listener);
		store.update([{ id: "old", text: "## Same" }, { id: "new", text: "## Same" }]);
		expect([...store.anchorsFor("new")!.values()]).toEqual(["same-1"]);
		expect(store.messageFor("same")).toBe("old");
		expect(listener).toHaveBeenCalled();
	});

	it("builds DOM ids that cannot collide with the app's own", () => {
		expect(chatAnchorDomId("root")).toBe("chat-anchor-root");
	});
});
