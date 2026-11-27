import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AutopilotLearnedDialog } from "../AutopilotLearnedDialog";
import { Button } from "../ui/button";
import { useAutopilotLearning } from "../../hooks/useAutopilotLearning";
import { useSettings, useUpdateAutopilotMaxIterations } from "../../hooks/useSettings";
import { SettingsOptionMenu, type SettingsOption } from "./SettingsOptionMenu";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

/** Iteration caps the user can pick; a value set through the API outside the menu still shows as itself. */
export const AUTOPILOT_MAX_ITERATION_CHOICES = [10, 20, 30, 50, 100];

/**
 * The one Autopilot preference: how many iterations a task run may take before
 * it parks itself and tells the user. Daemon-owned, so the phone and the CLI
 * agree on it. Hidden on a daemon that predates Autopilot, where it would do nothing.
 */
export function AutopilotSection() {
	const { t } = useTranslation();
	const { settings, isLoading } = useSettings();
	const { update, saving, error } = useUpdateAutopilotMaxIterations();
	const [learnedOpen, setLearnedOpen] = useState(false);
	const { data: learning } = useAutopilotLearning(settings?.autopilotMaxIterations !== undefined);
	const current = settings?.autopilotMaxIterations;
	if (current === undefined) return null;

	const choices = AUTOPILOT_MAX_ITERATION_CHOICES.includes(current)
		? AUTOPILOT_MAX_ITERATION_CHOICES
		: [...AUTOPILOT_MAX_ITERATION_CHOICES, current].sort((a, b) => a - b);
	const options: SettingsOption<string>[] = choices.map((choice) => ({
		value: String(choice),
		label: t("settings.autopilot.maxIterations.option", { iterations: choice }),
	}));

	return (
		<SettingsSection title={t("settings.autopilot.section")} grouped>
			<SettingsRow
				label={t("settings.autopilot.maxIterations.label")}
				description={
					error
						? <span role="alert" className="text-error">{error}</span>
						: t("settings.autopilot.maxIterations.description")
				}
			>
				<SettingsOptionMenu
					aria-label={t("settings.autopilot.maxIterations.label")}
					value={String(current)}
					options={options}
					onChange={(next) => update(Number(next))}
					disabled={isLoading || saving}
				/>
			</SettingsRow>
			<SettingsRow
				label={t("settings.autopilot.learned.label")}
				description={
					learning
						? t("settings.autopilot.learned.summary", { rules: learning.rules.length, decisions: learning.totalPairs })
						: t("settings.autopilot.learned.description")
				}
			>
				<Button type="button" variant="secondary" onClick={() => setLearnedOpen(true)}>
					{t("settings.autopilot.learned.open")}
				</Button>
			</SettingsRow>
			<AutopilotLearnedDialog open={learnedOpen} onOpenChange={setLearnedOpen} />
		</SettingsSection>
	);
}
