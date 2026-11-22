import type { ConversationItem } from "../../types/conversation";

/** A closing line at or under this many characters is a sign-off, not the answer. */
export const SHORT_CLOSING_CHARS = 400;
/** The text before a short closing line is kept only when it is a real write-up, not a "Now the tests." aside. */
const SUBSTANTIAL_TEXT_CHARS = 200;

const BOOKKEEPING_COMMAND = /^(?:(?:cd\s+\S+|pushd\s+\S+)\s*(?:&&|;)\s*)?["']?ao(?:\.exe)?["']?\s+(?:notify-user|send|report)\b/;
const TASK_TOOLS = new Set(["TaskCreate", "TaskUpdate", "TaskGet", "TaskList", "TodoWrite"]);

/**
 * A tool call that only reports on the work: `ao notify-user`, `ao send`, `ao report`,
 * and task or todo updates. Agents write their answer, run one of these, then add a
 * short closing line, so such a call does not end the answer before it.
 */
export function isBookkeepingCall(item: ConversationItem): boolean {
	if (item.kind !== "activity") return false;
	if (item.activityKind === "plan") return true;
	if (item.activityKind === "mcp_tool") return TASK_TOOLS.has(item.detail?.toolName ?? item.summary);
	if (item.activityKind !== "command") return false;
	const command = (item.detail?.command ?? item.detail?.rawCommand ?? item.summary).trim();
	return BOOKKEEPING_COMMAND.test(command);
}

interface RunLike {
	kind: "activities" | "single";
	items: readonly ConversationItem[];
}

function replyLength(run: RunLike | undefined): number | undefined {
	const item = run?.kind === "single" ? run.items[0] : undefined;
	if (item?.kind !== "message" || item.role !== "assistant") return undefined;
	const length = item.text.trim().length;
	return length > 0 ? length : undefined;
}

/**
 * The assistant text runs, before a settled turn's final one, that stay visible
 * instead of folding into "Worked for". Walking back from the final text:
 *
 *   - tool calls that are all bookkeeping do not split the reply, so the text
 *     before them stays with the final text;
 *   - when the final text is a short closing line, the substantial text before the
 *     tool calls that precede it stays too, because it is the answer being closed.
 *
 * Only tool calls sit between kept texts, so two texts written back to back, or
 * ones split by a question or a message from the reader, fold as before.
 */
export function keptReplyIndexes(runs: readonly RunLike[], finalIndex: number, firstIndex: number): Set<number> {
	const kept = new Set<number>();
	const finalLength = replyLength(runs[finalIndex]);
	if (finalLength === undefined) return kept;
	let cursor = finalIndex;
	let first = true;
	while (cursor > firstIndex) {
		let index = cursor - 1;
		let calls = 0;
		let onlyBookkeeping = true;
		while (index >= firstIndex && runs[index]!.kind === "activities") {
			calls += 1;
			if (!runs[index]!.items.every(isBookkeepingCall)) onlyBookkeeping = false;
			index -= 1;
		}
		const length = index >= firstIndex ? replyLength(runs[index]) : undefined;
		if (calls === 0 || length === undefined) break;
		const closesAnswer =
			first && finalLength <= SHORT_CLOSING_CHARS && length > finalLength && length >= SUBSTANTIAL_TEXT_CHARS;
		if (!onlyBookkeeping && !closesAnswer) break;
		kept.add(index);
		cursor = index;
		first = false;
	}
	return kept;
}
