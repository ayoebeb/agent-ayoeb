import { Trash2 } from "@/components/app-icons";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { BrowserTrustedCertificate } from "../../../shared/browser-certificate";
import type { AoBridge } from "../../../preload";
import { aoBridge } from "../../lib/bridge";
import { Button } from "../ui/button";
import { SettingsRow } from "./SettingsRow";

// Certificates trusted from the Browser panel's "Trust this certificate"
// button. One row each, with a remove button; nothing renders when there are
// none, so the Browser settings stay as they were for most people.
export function BrowserTrustedCertificates() {
	const { t } = useTranslation();
	const [certificates, setCertificates] = useState<BrowserTrustedCertificate[]>([]);
	const certificateBridge = (aoBridge as Partial<AoBridge>).browser?.certificates;

	useEffect(() => {
		let active = true;
		void certificateBridge
			?.list()
			.then((list) => {
				if (active) setCertificates(list);
			})
			.catch(() => undefined);
		return () => {
			active = false;
		};
	}, [certificateBridge]);

	if (certificates.length === 0) return null;
	return (
		<>
			{certificates.map((certificate) => (
				<SettingsRow
					key={`${certificate.host}|${certificate.fingerprint}`}
					label={
						<span className="block truncate">
							{t("settings.browserProfiles.certificates")}: {certificate.host}
						</span>
					}
					description={<span className="break-all font-mono">{certificate.fingerprint}</span>}
				>
					<Button
						aria-label={t("settings.browserProfiles.certificates.remove", { host: certificate.host })}
						onClick={() =>
							void certificateBridge
								?.remove(certificate.host, certificate.fingerprint)
								.then(setCertificates)
								.catch(() => undefined)
						}
						size="sm"
						type="button"
						variant="quiet"
					>
						<Trash2 aria-hidden="true" className="size-icon-base" />
					</Button>
				</SettingsRow>
			))}
		</>
	);
}
