import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import { Switch } from "../ui/switch";
import { SettingsRow } from "./SettingsRow";

/** Chromium GPU acceleration; main reads it once at startup. */
export function HardwareAccelerationRow() {
	const { t } = useTranslation();
	const [enabled, setEnabled] = useState<boolean | null>(null);
	const [saving, setSaving] = useState(false);
	const [saveFailed, setSaveFailed] = useState(false);

	useEffect(() => {
		let active = true;
		void aoBridge.uiSettings
			.get()
			.then((settings) => {
				if (active) setEnabled(settings.hardwareAcceleration);
			})
			.catch(() => undefined);
		return () => {
			active = false;
		};
	}, []);

	const change = (next: boolean) => {
		setSaving(true);
		setSaveFailed(false);
		void aoBridge.uiSettings
			.set({ hardwareAcceleration: next })
			.then((settings) => setEnabled(settings.hardwareAcceleration))
			.catch(() => setSaveFailed(true))
			.finally(() => setSaving(false));
	};

	return (
		<SettingsRow
			label={t("settings.hardwareAcceleration.label")}
			description={
				saveFailed
					? <span role="alert" className="text-error">{t("settings.hardwareAcceleration.saveFailed")}</span>
					: t("settings.hardwareAcceleration.description")
			}
		>
			<Switch
				aria-label={t("settings.hardwareAcceleration.label")}
				checked={enabled ?? true}
				disabled={enabled === null || saving}
				onCheckedChange={change}
			/>
		</SettingsRow>
	);
}
