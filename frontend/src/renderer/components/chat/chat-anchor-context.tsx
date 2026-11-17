import { createContext, useContext, useSyncExternalStore } from "react";
import { ChatAnchorStore, type HeadingAnchors } from "../../lib/chat-anchors";

/** The anchors of the chat whose messages are on screen; provided by the transcript. */
export const ChatAnchorStoreContext = createContext<ChatAnchorStore | undefined>(undefined);

/** Scrolls the transcript to a heading by anchor; provided by the transcript. */
export const ChatAnchorNavContext = createContext<((anchor: string) => void) | undefined>(undefined);

const noSubscription = () => () => {};
const noAnchors = () => undefined;

/** The anchors of one message's headings, or undefined when it has none or no transcript owns it. */
export function useMessageHeadingAnchors(messageId: string | undefined): HeadingAnchors | undefined {
	const store = useContext(ChatAnchorStoreContext);
	return useSyncExternalStore(
		store && messageId ? store.subscribe : noSubscription,
		store && messageId ? () => store.anchorsFor(messageId) : noAnchors,
	);
}
