import { useTranslation } from "react-i18next";
import type { BoardView } from "../lib/board-view";
import { noDragStyle } from "./PageTopbar";
import { Button } from "./ui/button";

const VIEWS: readonly BoardView[] = ["agents", "jira"];

/** Switches a project's board between its agents and its Jira tickets. */
export function BoardViewToggle({ value, onChange }: { value: BoardView; onChange: (view: BoardView) => void }) {
	const { t } = useTranslation();
	return (
		<div role="tablist" aria-label={t("jira.tab.label")} className="flex shrink-0 items-center gap-0.5" style={noDragStyle} data-testid="board-view-toggle">
			{VIEWS.map((view) => (
				<Button
					key={view}
					type="button"
					role="tab"
					variant="quiet"
					size="xs"
					aria-selected={value === view}
					data-active={value === view ? "true" : undefined}
					data-testid={`board-view-${view}`}
					onClick={() => onChange(view)}
				>
					{t(view === "jira" ? "jira.tab.jira" : "jira.tab.agents")}
				</Button>
			))}
		</div>
	);
}
