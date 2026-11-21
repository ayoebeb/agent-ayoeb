import { describe, expect, it } from "vitest";
import type { ConversationItem } from "../../types/conversation";
import { isBookkeepingCall, keptReplyIndexes } from "./turn-reply";

const at = "2026-08-11T10:01:00Z";
const call = (command: string): ConversationItem => ({
	kind: "activity", id: command, sequence: 1, revision: 0, activityKind: "command",
	status: "completed", summary: command, detail: { command }, createdAt: at,
});
const text = (body: string): ConversationItem => ({
	kind: "message", id: body.slice(0, 8), sequence: 1, revision: 0, role: "assistant",
	origin: "provider", text: body, streaming: false, createdAt: at,
});
const activities = (...commands: string[]) => ({ kind: "activities" as const, items: commands.map(call) });
const single = (body: string) => ({ kind: "single" as const, items: [text(body)] });
const LONG = "x".repeat(500);

describe("isBookkeepingCall", () => {
	it.each(["ao notify-user --message hi", "ao send --session a --message b", "ao report --done", "cd repo && ao report --checkpoint"])(
		"treats %s as bookkeeping",
		(command) => expect(isBookkeepingCall(call(command))).toBe(true),
	);
	it.each(["go build ./...", "git status", "cat ao-notes.md", "echo ao send"])("treats %s as work", (command) =>
		expect(isBookkeepingCall(call(command))).toBe(false),
	);
});

describe("keptReplyIndexes", () => {
	it("keeps text before trailing bookkeeping calls", () => {
		const runs = [single(LONG), activities("ao notify-user"), single("ok")];
		expect([...keptReplyIndexes(runs, 2, 0)]).toEqual([0]);
	});
	it("keeps a long text before work tools when the closing line is short", () => {
		const runs = [single(LONG), activities("npm test"), single("ok")];
		expect([...keptReplyIndexes(runs, 2, 0)]).toEqual([0]);
	});
	it("keeps nothing when the final text is long too", () => {
		const runs = [single(LONG), activities("npm test"), single(LONG + "y")];
		expect(keptReplyIndexes(runs, 2, 0).size).toBe(0);
	});
	it("keeps nothing when the text before work tools is short", () => {
		const runs = [single("Checking."), activities("npm test"), single("ok")];
		expect(keptReplyIndexes(runs, 2, 0).size).toBe(0);
	});
	it("does not reach across texts written back to back", () => {
		const runs = [single(LONG), single("ok")];
		expect(keptReplyIndexes(runs, 1, 0).size).toBe(0);
	});
	it("stops at the first real work run when walking past bookkeeping", () => {
		const runs = [single(LONG), activities("npm test"), single("Now reporting."), activities("ao report --done"), single("ok")];
		expect([...keptReplyIndexes(runs, 4, 0)]).toEqual([2]);
	});
});
