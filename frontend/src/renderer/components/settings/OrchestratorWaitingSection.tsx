import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useSettings, useUpdateOrchestratorWaiting } from "../../hooks/useSettings";
import { useOrchestratorWaitingSoundStore } from "../../stores/orchestrator-waiting-sound-store";
import { Switch } from "../ui/switch";
import { SettingsOptionMenu, type SettingsOption } from "./SettingsOptionMenu";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

/** Minutes the user can pick for the one reminder; 0 is no reminder. */
export const ORCHESTRATOR_WAITING_RENOTIFY_MINUTES = [5, 10, 30, 60];

/**
 * How AO tells the user a project orchestrator is waiting for them. The
 * notification and its reminder are daemon-owned, so the phone and every other
 * client agree on them; the sound is played by this desktop app, so its switch
 * lives with the app's other sound preference. Hidden on a daemon that predates
 * the notification, where the controls would do nothing.
 */
export function OrchestratorWaitingSection() {
	const { t } = useTranslation();
	const { settings, isLoading } = useSettings();
	const { update, saving, error } = useUpdateOrchestratorWaiting();
	const soundEnabled = useOrchestratorWaitingSoundStore((state) => state.enabled);
	const loadSound = useOrchestratorWaitingSoundStore((state) => state.load);
	const setSoundEnabled = useOrchestratorWaitingSoundStore((state) => state.setEnabled);
	const soundSaving = useOrchestratorWaitingSoundStore((state) => state.saving);
	const soundSaveError = useOrchestratorWaitingSoundStore((state) => state.saveError);
	useEffect(() => {
		void loadSound();
	}, [loadSound]);

	const enabled = settings?.orchestratorWaitingEnabled;
	const minutes = settings?.orchestratorWaitingRenotifyMinutes;
	if (enabled === undefined || minutes === undefined) return null;

	// A value set through the API outside the menu still shows as itself.
	const choices = ORCHESTRATOR_WAITING_RENOTIFY_MINUTES.includes(minutes) || minutes === 0
		? ORCHESTRATOR_WAITING_RENOTIFY_MINUTES
		: [...ORCHESTRATOR_WAITING_RENOTIFY_MINUTES, minutes].sort((a, b) => a - b);
	const options: SettingsOption<string>[] = [
		{ value: "0", label: t("settings.orchestratorWaiting.renotify.never") },
		...choices.map((choice) => ({
			value: String(choice),
			label: t("settings.orchestratorWaiting.renotify.afterMinutes", { minutes: choice }),
		})),
	];
	const disabled = isLoading || saving;

	return (
		<SettingsSection title={t("settings.orchestratorWaiting.section")} grouped>
			<SettingsRow
				label={t("settings.orchestratorWaiting.enabled.label")}
				description={
					error
						? <span role="alert" className="text-error">{error}</span>
						: t("settings.orchestratorWaiting.enabled.description")
				}
			>
				<Switch
					aria-label={t("settings.orchestratorWaiting.enabled.label")}
					checked={enabled}
					disabled={disabled}
					onCheckedChange={(next) => update({ enabled: next, renotifyMinutes: minutes })}
				/>
			</SettingsRow>
			<SettingsRow
				label={t("settings.orchestratorWaiting.sound.label")}
				description={
					soundSaveError
						? <span role="alert" className="text-error">{t("settings.orchestratorWaiting.sound.saveFailed")}</span>
						: undefined
				}
			>
				<Switch
					aria-label={t("settings.orchestratorWaiting.sound.label")}
					checked={soundEnabled}
					disabled={soundSaving || !enabled}
					onCheckedChange={(next) => {
						void setSoundEnabled(next);
					}}
				/>
			</SettingsRow>
			<SettingsRow
				label={t("settings.orchestratorWaiting.renotify.label")}
				description={t("settings.orchestratorWaiting.renotify.description")}
			>
				<SettingsOptionMenu
					aria-label={t("settings.orchestratorWaiting.renotify.label")}
					value={String(minutes)}
					options={options}
					onChange={(next) => update({ enabled, renotifyMinutes: Number(next) })}
					disabled={disabled || !enabled}
				/>
			</SettingsRow>
		</SettingsSection>
	);
}
