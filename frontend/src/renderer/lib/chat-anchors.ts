/**
 * Anchors for the headings in an agent's messages.
 *
 * An agent can link to an earlier question with a plain markdown link,
 * `[Which database?](#which-database)`, and the chat scrolls there. This module
 * decides what each heading's anchor is, so the heading that carries the id and
 * the link that names it always agree.
 *
 * The rule is GitHub's, which every agent already knows: the heading's plain
 * text, lowercased, punctuation removed, spaces turned into hyphens. A heading
 * whose anchor is taken gets `-1`, `-2` and so on, in the order the headings
 * were written, so an anchor never moves once the chat is fully loaded. The
 * orchestrator's built-in prompt (communication_style_prompt.go) states this
 * same rule; change both together.
 */

import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import type { Nodes } from "mdast";
import { remarkMath } from "./remark-math";

const parser = unified().use(remarkParse).use(remarkGfm);
const mathParser = unified().use(remarkParse).use(remarkGfm).use(remarkMath);

/** The anchor a heading's text asks for, before collisions are resolved. */
export function headingSlug(text: string): string {
	const slug = text
		.normalize("NFC")
		.toLowerCase()
		// Keep letters in any script, combining marks, digits, `_` and `-`; everything else goes.
		.replace(/[^\p{L}\p{M}\p{N}\p{Pc}\s-]/gu, "")
		.trim()
		.replace(/\s+/g, "-")
		.replace(/^-+|-+$/g, "");
	return slug || "section";
}

/** The anchor named by a `#fragment` href, or undefined when the href is not one. */
export function anchorFromHref(href: string): string | undefined {
	if (!href.startsWith("#") || href.length < 2) return undefined;
	return normalizeAnchor(href.slice(1));
}

/** Compares anchors the way agents write them: percent-decoded, lowercased. */
export function normalizeAnchor(raw: string): string {
	let value = raw;
	try {
		value = decodeURIComponent(raw);
	} catch {
		// A stray `%` is part of the text.
	}
	return value.normalize("NFC").toLowerCase();
}

export type ChatHeading = { /** Where the heading starts in the message source. */ offset: number; text: string };

// A line that could open a heading: `#` text, or text over a `===`/`---` rule.
const MAYBE_HEADING = /^ {0,3}#{1,6}(?:[ \t]|$)|^[^\n]+\n {0,3}(?:=+|-+)[ \t]*$/m;
const HEADING_CACHE_LIMIT = 400;
// One cache per dialect, keyed by the message text itself: the transcript asks again
// for every message on every streamed delta, and most of them have not changed.
const headingCaches = { text: new Map<string, ChatHeading[]>(), math: new Map<string, ChatHeading[]>() };

function plainText(node: Nodes): string {
	switch (node.type) {
		case "text":
		case "inlineCode":
		case "inlineMath":
			return node.value;
		case "break":
			return " ";
		case "image":
			return node.alt ?? "";
		default:
			return "children" in node ? (node.children as Nodes[]).map(plainText).join("") : "";
	}
}

function collectHeadings(node: Nodes, into: ChatHeading[]): void {
	if (node.type === "heading") {
		const offset = node.position?.start.offset;
		if (offset !== undefined) into.push({ offset, text: plainText(node).trim() });
		return;
	}
	if ("children" in node) for (const child of node.children as Nodes[]) collectHeadings(child, into);
}

/**
 * The headings of one message, in source order, as the chat's markdown parser
 * sees them (so a `#` line inside a code fence is not one). `math` is the
 * dialect the message renders in.
 */
export function extractHeadings(markdown: string, math = false): ChatHeading[] {
	const cache = math ? headingCaches.math : headingCaches.text;
	const cached = cache.get(markdown);
	if (cached) return cached;
	const headings: ChatHeading[] = [];
	if (MAYBE_HEADING.test(markdown)) collectHeadings((math ? mathParser : parser).parse(markdown), headings);
	if (cache.size >= HEADING_CACHE_LIMIT) cache.delete(cache.keys().next().value!);
	cache.set(markdown, headings);
	return headings;
}

/** Heading offset in a message's source -> the anchor (and DOM id) that heading carries. */
export type HeadingAnchors = ReadonlyMap<number, string>;

export type AnchoredMessage = { id: string; text: string };

/**
 * Assigns every heading an anchor unique among `messages`, oldest first.
 * Returns each message's heading anchors and the message each anchor lives in.
 */
export function assignAnchors(messages: readonly AnchoredMessage[], math = false) {
	const taken = new Set<string>();
	const next = new Map<string, number>();
	const byMessage = new Map<string, HeadingAnchors>();
	const messageByAnchor = new Map<string, string>();
	for (const message of messages) {
		const headings = extractHeadings(message.text, math);
		if (headings.length === 0) continue;
		const anchors = new Map<number, string>();
		for (const heading of headings) {
			const base = headingSlug(heading.text);
			let anchor = base;
			let suffix = next.get(base) ?? 0;
			while (taken.has(anchor)) anchor = `${base}-${++suffix}`;
			next.set(base, suffix);
			taken.add(anchor);
			anchors.set(heading.offset, anchor);
			messageByAnchor.set(anchor, message.id);
		}
		byMessage.set(message.id, anchors);
	}
	return { byMessage, messageByAnchor };
}

const sameAnchors = (a: HeadingAnchors | undefined, b: HeadingAnchors | undefined) =>
	a === b || (!!a && !!b && a.size === b.size && [...a].every(([offset, anchor]) => b.get(offset) === anchor));

/**
 * One chat's anchors, kept outside React so a message re-renders only when its
 * own headings' anchors change, not on every streamed delta elsewhere.
 */
export class ChatAnchorStore {
	private byMessage = new Map<string, HeadingAnchors>();
	private messageByAnchor = new Map<string, string>();
	private listeners = new Set<() => void>();

	/** Recomputes from the chat's assistant messages, oldest first. */
	update(messages: readonly AnchoredMessage[], math = false): void {
		const next = assignAnchors(messages, math);
		const changed = new Set<string>();
		for (const [id, anchors] of next.byMessage) {
			const previous = this.byMessage.get(id);
			if (sameAnchors(previous, anchors)) next.byMessage.set(id, previous!);
			else changed.add(id);
		}
		for (const id of this.byMessage.keys()) if (!next.byMessage.has(id)) changed.add(id);
		this.byMessage = next.byMessage;
		this.messageByAnchor = next.messageByAnchor;
		if (changed.size > 0) for (const listener of [...this.listeners]) listener();
	}

	anchorsFor = (messageId: string): HeadingAnchors | undefined => this.byMessage.get(messageId);

	/** The message holding the heading a link names; undefined for an unknown anchor. */
	messageFor(anchor: string): string | undefined {
		return this.messageByAnchor.get(normalizeAnchor(anchor));
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};
}

/** The DOM id of the heading that owns `anchor`. Prefixed so a heading called "Root" is not `#root`. */
export const chatAnchorDomId = (anchor: string) => `chat-anchor-${anchor}`;
