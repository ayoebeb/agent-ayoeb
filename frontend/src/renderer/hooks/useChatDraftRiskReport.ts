import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { CHAT_DRAFT_BOUNDARY_KINDS, type ChatDraftBoundaryKind } from "../../shared/chat-draft-risk";
import { aoBridge } from "../lib/bridge";
import { chatDraftDialogCopy } from "../lib/chat-draft-boundary";

// Every mounted session view reports its own chat drafts at risk: the one
// session on screen, or each pane of a split, focused or not. Quitting or
// closing the window weighs what they hold together, so a draft in a pane
// without focus still gets the native confirmation.
const reports = new Map<symbol, readonly ChatDraftBoundaryKind[]>();

function publish(): void {
	if (reports.size === 0) {
		aoBridge.app.setChatDraftRisk?.([]);
		return;
	}
	const held = new Set([...reports.values()].flat());
	const risks = CHAT_DRAFT_BOUNDARY_KINDS.filter((kind) => held.has(kind));
	aoBridge.app.setChatDraftRisk?.(risks, chatDraftDialogCopy(risks));
}

/** Reports this view's chat drafts at risk to the native quit guard while it is mounted. */
export function useChatDraftRiskReport(risks: readonly ChatDraftBoundaryKind[]): void {
	// The dialog copy is translated when published, so a language switch republishes.
	const { t } = useTranslation();
	const [key] = useState(() => Symbol("chat-draft-risk"));
	useEffect(() => {
		reports.set(key, risks);
		publish();
	}, [key, risks, t]);
	useEffect(
		() => () => {
			reports.delete(key);
			publish();
		},
		[key],
	);
}
