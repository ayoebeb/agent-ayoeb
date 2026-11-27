import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { useAutopilotLearning, useAutopilotLearningActions } from "../hooks/useAutopilotLearning";
import { useRestoreFocusOnClose } from "../hooks/useRestoreFocusOnClose";
import { groupRules, isGlobalScope, type AutopilotLearningPair, type AutopilotLearningRule } from "../lib/autopilot-learning";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { Input } from "./ui/input";

/** How many stored decisions show before "Show more". */
const PAIRS_SHOWN = 12;

function Section({ title, children }: { title: string; children: ReactNode }) {
	return (
		<section className="flex flex-col gap-1.5">
			<h3 className="text-caption font-medium text-settings-muted">{title}</h3>
			{children}
		</section>
	);
}

/**
 * What Autopilot learned about how the owner decides: the profile rules it
 * follows and the decisions they came from. The owner can edit or delete both,
 * add a rule, import past conversations, and ask for a profile update. Nothing
 * here can lift a hard limit. The data is stored on this computer; a profile
 * update sends the decision texts to Claude, which the description says.
 */
export function AutopilotLearnedDialog({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const { t } = useTranslation();
	const restoreFocusOnClose = useRestoreFocusOnClose(open);
	const { data } = useAutopilotLearning(open);
	const actions = useAutopilotLearningActions();
	const [showAll, setShowAll] = useState(false);
	const [draft, setDraft] = useState("");
	const [editing, setEditing] = useState<{ id: string; text: string } | null>(null);
	const [importNote, setImportNote] = useState<string | null>(null);

	const rules = data?.rules ?? [];
	const pairs = data?.pairs ?? [];
	const shownPairs = showAll ? pairs : pairs.slice(0, PAIRS_SHOWN);

	const submitRule = () => {
		const text = draft.trim();
		if (!text) return;
		actions.addRule.mutate({ scope: "global", text }, { onSuccess: () => setDraft("") });
	};
	const saveEdit = () => {
		if (!editing || !editing.text.trim()) return;
		actions.editRule.mutate({ id: editing.id, text: editing.text.trim() }, { onSuccess: () => setEditing(null) });
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent
				showCloseButton={false}
				className={cn(settingsDialogContentClass, "w-[min(640px,calc(100vw-24px))]")}
				onCloseAutoFocus={restoreFocusOnClose}
			>
				<div className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title text-base">{t("autopilot.learned.title")}</DialogTitle>
					<DialogDescription className="text-control leading-5 text-settings-muted">
						{t("autopilot.learned.description")}
					</DialogDescription>
				</div>

				<div className={settingsDialogBodyClass} data-testid="autopilot-learned-body">
					{data ? (
						<p className="text-xs leading-5 text-settings-muted" data-testid="autopilot-learned-stats">
							{t("autopilot.learned.stats", { total: data.totalPairs, pending: data.unfoldedPairs })}
							{data.learning ? ` · ${t("autopilot.learned.updating")}` : ""}
						</p>
					) : null}
					{data?.lastError ? (
						<p role="alert" className="text-xs leading-5 text-error">{t("autopilot.learned.lastError", { message: data.lastError })}</p>
					) : null}
					{data && !data.canLearn ? (
						<p role="note" className="text-xs leading-5 text-status-in-review">{t("autopilot.learned.cannotLearn")}</p>
					) : null}

					<Section title={t("autopilot.learned.rules")}>
						{rules.length === 0 ? (
							<p className="text-[13px] leading-5 text-settings-muted" data-testid="autopilot-learned-no-rules">{t("autopilot.learned.rulesEmpty")}</p>
						) : (
							<div className="flex flex-col gap-2" data-testid="autopilot-learned-rules">
								{groupRules(rules).map((group) => (
									<div key={group.scope} className="flex flex-col gap-0.5">
										<span className="text-xs text-settings-muted">
											{isGlobalScope(group.scope) ? t("autopilot.learned.scopeGlobal") : group.scope}
										</span>
										<ul className="flex flex-col gap-1">
											{group.rules.map((rule) => (
												<RuleRow
													key={rule.id}
													rule={rule}
													editing={editing?.id === rule.id ? editing.text : null}
													busy={actions.editRule.isPending || actions.deleteRule.isPending}
													onEdit={() => setEditing({ id: rule.id, text: rule.text })}
													onChange={(text) => setEditing({ id: rule.id, text })}
													onSave={saveEdit}
													onCancel={() => setEditing(null)}
													onDelete={() => actions.deleteRule.mutate(rule.id)}
												/>
											))}
										</ul>
									</div>
								))}
							</div>
						)}
						<form
							className="mt-1 flex items-center gap-1.5"
							onSubmit={(event) => {
								event.preventDefault();
								submitRule();
							}}
						>
							<Input
								value={draft}
								onChange={(event) => setDraft(event.target.value)}
								placeholder={t("autopilot.learned.addPlaceholder")}
								aria-label={t("autopilot.learned.addPlaceholder")}
								maxLength={220}
							/>
							<Button type="submit" variant="secondary" disabled={!draft.trim() || actions.addRule.isPending}>
								{t("autopilot.learned.add")}
							</Button>
						</form>
					</Section>

					<Section title={t("autopilot.learned.pairs")}>
						{pairs.length === 0 ? (
							<p className="text-[13px] leading-5 text-settings-muted" data-testid="autopilot-learned-no-pairs">{t("autopilot.learned.pairsEmpty")}</p>
						) : (
							<ul className="flex flex-col gap-1.5" data-testid="autopilot-learned-pairs">
								{shownPairs.map((pair) => (
									<PairRow
										key={pair.id}
										pair={pair}
										busy={actions.deletePair.isPending}
										onDelete={() => actions.deletePair.mutate(pair.id)}
									/>
								))}
							</ul>
						)}
						{pairs.length > PAIRS_SHOWN && !showAll ? (
							<Button type="button" variant="ghost" size="xs" className="self-start" onClick={() => setShowAll(true)}>
								{t("autopilot.learned.showMore", { count: pairs.length - PAIRS_SHOWN })}
							</Button>
						) : null}
					</Section>

					<p role="note" className="text-xs leading-5 text-settings-muted">{t("autopilot.learned.hardLimits")}</p>
					{importNote ? <p className="text-xs leading-5 text-settings-muted" data-testid="autopilot-learned-import-note">{importNote}</p> : null}
					{actions.error ? (
						<p role="alert" className="text-xs leading-5 text-error">{t("autopilot.error.generic", { message: actions.error })}</p>
					) : null}
				</div>

				<div className={cn(settingsDialogFooterClass, "gap-1.5")}>
					<Button
						type="button"
						variant="secondary"
						disabled={actions.importHistory.isPending}
						onClick={() =>
							actions.importHistory.mutate(undefined, {
								onSuccess: (result) => setImportNote(t("autopilot.learned.importResult", { files: result.files, added: result.added })),
							})}
					>
						{t("autopilot.learned.import")}
					</Button>
					<Button
						type="button"
						variant="secondary"
						disabled={!data?.canLearn || data?.learning || actions.relearn.isPending || (data?.unfoldedPairs ?? 0) === 0}
						onClick={() => actions.relearn.mutate()}
					>
						{t("autopilot.learned.relearn")}
					</Button>
					<DialogClose asChild>
						<Button type="button" variant="ghost">{t("autopilot.dialog.close")}</Button>
					</DialogClose>
				</div>
			</DialogContent>
		</Dialog>
	);
}

function RuleRow({
	rule,
	editing,
	busy,
	onEdit,
	onChange,
	onSave,
	onCancel,
	onDelete,
}: {
	rule: AutopilotLearningRule;
	editing: string | null;
	busy: boolean;
	onEdit: () => void;
	onChange: (text: string) => void;
	onSave: () => void;
	onCancel: () => void;
	onDelete: () => void;
}) {
	const { t } = useTranslation();
	if (editing !== null) {
		return (
			<li className="flex items-center gap-1.5" data-testid="autopilot-learned-rule">
				<Input
					value={editing}
					onChange={(event) => onChange(event.target.value)}
					aria-label={t("autopilot.learned.edit")}
					maxLength={220}
					autoFocus
				/>
				<Button type="button" size="xs" variant="primary" disabled={busy || !editing.trim()} onClick={onSave}>
					{t("autopilot.learned.save")}
				</Button>
				<Button type="button" size="xs" variant="ghost" onClick={onCancel}>{t("autopilot.learned.cancel")}</Button>
			</li>
		);
	}
	return (
		<li className="flex items-start justify-between gap-3" data-testid="autopilot-learned-rule">
			<div className="min-w-0 text-[13px] leading-5 text-foreground">
				<span>{rule.text}</span>
				<span className="block text-xs text-settings-muted">
					{rule.origin === "owner" ? t("autopilot.learned.originOwner") : t("autopilot.learned.originLearned")}
					{rule.tag === "usage_limit_switch" ? ` · ${t("autopilot.learned.switchNote")}` : ""}
				</span>
			</div>
			<div className="flex shrink-0 gap-1">
				<Button type="button" size="xs" variant="secondary" onClick={onEdit}>{t("autopilot.learned.edit")}</Button>
				<Button type="button" size="xs" variant="destructive-ghost" disabled={busy} onClick={onDelete}>{t("autopilot.learned.delete")}</Button>
			</div>
		</li>
	);
}

function PairRow({ pair, busy, onDelete }: { pair: AutopilotLearningPair; busy: boolean; onDelete: () => void }) {
	const { t } = useTranslation();
	const when = new Date(pair.at);
	return (
		<li className="flex items-start justify-between gap-3" data-testid="autopilot-learned-pair">
			<div className="min-w-0 text-[13px] leading-5 text-foreground">
				<span className="block break-words">{pair.question}</span>
				<span className="block break-words text-xs text-settings-muted">
					{t("autopilot.learned.youAnswered", { answer: pair.answer })}
				</span>
				<span className="block text-xs text-settings-muted">
					{t(`autopilot.learned.source.${pair.source}`)}
					{pair.project ? ` · ${pair.project}` : ""}
					{Number.isNaN(when.getTime()) ? "" : ` · ${when.toLocaleDateString()}`}
				</span>
			</div>
			<Button type="button" size="xs" variant="destructive-ghost" disabled={busy} onClick={onDelete}>
				{t("autopilot.learned.delete")}
			</Button>
		</li>
	);
}
