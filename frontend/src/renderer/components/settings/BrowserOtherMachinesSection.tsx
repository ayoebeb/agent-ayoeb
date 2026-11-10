import { Laptop, RefreshCw, UserRound } from "@/components/app-icons";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import type { AoBridge } from "../../../preload";
import type {
	BrowserProfileCopyErrorCode,
	OtherMachine,
	OtherMachineProfile,
	OtherMachinesResult,
} from "../../../shared/browser-profile-share";
import type { BrowserProfile } from "../../../shared/browser-profiles";
import type { MessageKey } from "../../i18n/messages";
import { formatRelativeTime } from "../../lib/usage-format";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

type ProfileBridge = AoBridge["browserProfiles"];

type PendingCopy = {
	kind: "copy" | "update";
	machine: OtherMachine;
	profile: OtherMachineProfile;
};

const STATE_KEYS: Record<Exclude<OtherMachine["state"], "online">, MessageKey> = {
	offline: "settings.browserProfiles.machines.state.offline",
	insecure: "settings.browserProfiles.machines.state.insecure",
	unverified: "settings.browserProfiles.machines.state.unverified",
	"sharing-off": "settings.browserProfiles.machines.state.sharingOff",
	unauthorized: "settings.browserProfiles.machines.state.unauthorized",
	outdated: "settings.browserProfiles.machines.state.outdated",
	"desktop-closed": "settings.browserProfiles.machines.state.desktopClosed",
	failed: "settings.browserProfiles.machines.state.failed",
};

const ERROR_KEYS: Record<BrowserProfileCopyErrorCode, MessageKey> = {
	NOT_LINKED: "settings.browserProfiles.machines.error.notLinked",
	OFFLINE: "settings.browserProfiles.machines.state.offline",
	INSECURE: "settings.browserProfiles.machines.state.insecure",
	UNVERIFIED: "settings.browserProfiles.machines.state.unverified",
	SHARING_OFF: "settings.browserProfiles.machines.state.sharingOff",
	UNAUTHORIZED: "settings.browserProfiles.machines.state.unauthorized",
	OUTDATED: "settings.browserProfiles.machines.state.outdated",
	DESKTOP_CLOSED: "settings.browserProfiles.machines.state.desktopClosed",
	NOT_SHARED: "settings.browserProfiles.machines.error.notShared",
	ALREADY_COPIED: "settings.browserProfiles.machines.error.alreadyCopied",
	PROFILE_LIMIT: "settings.browserProfiles.machines.error.profileLimit",
	PROFILE_ACTIVE: "settings.browserProfiles.machines.error.profileActive",
	INVALID_DATA: "settings.browserProfiles.machines.error.invalidData",
	FAILED: "settings.browserProfiles.machines.error.failed",
};

function profileDetails(profile: OtherMachineProfile, t: TFunction, locale: string, now: Date): string {
	const number = new Intl.NumberFormat(locale);
	return [
		profile.sourceBrowser,
		t("settings.browserProfiles.machines.counts", {
			cookies: number.format(profile.cookieCount),
			history: number.format(profile.historyCount),
		}),
		t("settings.browserProfiles.machines.updated", { time: formatRelativeTime(profile.updatedAt, now, locale) }),
		profile.localCopy
			? t("settings.browserProfiles.machines.copied", { time: formatRelativeTime(profile.localCopy.copiedAt, now, locale) })
			: null,
	].filter(Boolean).join(" · ");
}

/**
 * The browser profiles this account's other machines share. Copying makes a
 * normal local profile, so it appears in every profile picker; Remove goes
 * through the same confirmed delete as any local profile.
 */
export function BrowserOtherMachinesSection({
	bridge,
	localProfiles,
	onChanged,
	onRemove,
}: {
	bridge: ProfileBridge | undefined;
	localProfiles: BrowserProfile[];
	onChanged: () => void;
	onRemove: (profile: BrowserProfile) => void;
}) {
	const { t, i18n } = useTranslation();
	const [result, setResult] = useState<OtherMachinesResult | null>(null);
	const [loading, setLoading] = useState(Boolean(bridge));
	const [pending, setPending] = useState<PendingCopy | null>(null);
	const [busy, setBusy] = useState(false);
	const [copyError, setCopyError] = useState("");

	const refresh = useCallback(async () => {
		if (!bridge) return;
		setLoading(true);
		try {
			setResult(await bridge.listOtherMachines());
		} catch {
			setResult({ signedIn: true, machines: [], error: t("settings.browserProfiles.machines.loadFailed") });
		} finally {
			setLoading(false);
		}
	}, [bridge, t]);

	useEffect(() => {
		void refresh();
	}, [refresh]);

	const confirm = async () => {
		if (!bridge || !pending || busy) return;
		setBusy(true);
		setCopyError("");
		try {
			const outcome = pending.kind === "copy"
				? await bridge.copyFromMachine({ hostId: pending.machine.hostId, profileId: pending.profile.id, name: copyName })
				: await bridge.updateCopy(pending.profile.localCopy!.profileId);
			if (outcome.error) {
				setCopyError(t(ERROR_KEYS[outcome.code] ?? ERROR_KEYS.FAILED, { machine: pending.machine.name }));
				return;
			}
			setPending(null);
			onChanged();
			await refresh();
		} catch {
			setCopyError(t(ERROR_KEYS.FAILED, { machine: pending.machine.name }));
		} finally {
			setBusy(false);
		}
	};

	const now = new Date();
	const machines = result?.machines ?? [];
	// A copy's name: the one it already has here, or the one it is about to get.
	const localName = pending?.profile.localCopy
		? localProfiles.find((profile) => profile.id === pending.profile.localCopy?.profileId)?.name
		: undefined;
	const copyName = pending
		? localName ?? t("settings.browserProfiles.machines.copyName", { profile: pending.profile.name, machine: pending.machine.name })
		: "";

	return (
		<>
			<SettingsSection
				title={t("settings.browserProfiles.machines.title")}
				grouped
				action={bridge ? (
					<Tooltip>
						<TooltipTrigger asChild>
							<Button
								aria-label={t("settings.browserProfiles.machines.refresh")}
								disabled={loading}
								onClick={() => void refresh()}
								size="icon-sm"
								type="button"
								variant="quiet"
							>
								<RefreshCw aria-hidden="true" className="size-icon-base" />
							</Button>
						</TooltipTrigger>
						<TooltipContent>{t("settings.browserProfiles.machines.refresh")}</TooltipContent>
					</Tooltip>
				) : undefined}
			>
				{loading && !result ? (
					<div className="settings-row-bar text-sm text-settings-muted" role="status">{t("settings.browserProfiles.machines.loading")}</div>
				) : result?.error ? (
					<div className="settings-row-bar text-sm text-error" role="alert">{result.error}</div>
				) : !result?.signedIn ? (
					<div className="settings-row-bar text-sm text-settings-muted">{t("settings.browserProfiles.machines.signedOut")}</div>
				) : machines.length === 0 ? (
					<div className="settings-row-bar text-sm text-settings-muted">{t("settings.browserProfiles.machines.none")}</div>
				) : (
					machines.map((machine) => (
						<MachineRows
							key={machine.hostId}
							machine={machine}
							details={(profile) => profileDetails(profile, t, i18n.language, now)}
							onCopy={(profile) => { setCopyError(""); setPending({ kind: "copy", machine, profile }); }}
							onUpdate={(profile) => { setCopyError(""); setPending({ kind: "update", machine, profile }); }}
							onRemove={(profile) => {
								const local = localProfiles.find((candidate) => candidate.id === profile.localCopy?.profileId);
								if (local) onRemove(local);
							}}
						/>
					))
				)}
			</SettingsSection>
			<ConfirmDialog
				open={pending !== null}
				onOpenChange={(open) => {
					if (open || busy) return;
					setPending(null);
					setCopyError("");
				}}
				title={pending
					? t(pending.kind === "copy" ? "settings.browserProfiles.machines.copyTitle" : "settings.browserProfiles.machines.updateTitle", {
						profile: pending.kind === "copy" ? pending.profile.name : copyName,
					})
					: ""}
				description={pending
					? t(pending.kind === "copy" ? "settings.browserProfiles.machines.copyDescription" : "settings.browserProfiles.machines.updateDescription", {
						name: copyName,
						machine: pending.machine.name,
					})
					: ""}
				confirmLabel={t(pending?.kind === "update" ? "settings.browserProfiles.machines.update" : "settings.browserProfiles.machines.copyConfirm")}
				busy={busy}
				error={copyError || null}
				onConfirm={() => void confirm()}
			>
				<p className="text-sm text-settings-muted">{t("settings.browserProfiles.machines.deviceNote")}</p>
			</ConfirmDialog>
		</>
	);
}

function MachineRows({
	machine,
	details,
	onCopy,
	onUpdate,
	onRemove,
}: {
	machine: OtherMachine;
	details: (profile: OtherMachineProfile) => string;
	onCopy: (profile: OtherMachineProfile) => void;
	onUpdate: (profile: OtherMachineProfile) => void;
	onRemove: (profile: OtherMachineProfile) => void;
}) {
	const { t } = useTranslation();
	const status = machine.state === "online"
		? machine.profiles.length === 0 ? t("settings.browserProfiles.machines.noProfiles") : undefined
		: t(STATE_KEYS[machine.state], { machine: machine.name });
	return (
		<>
			<SettingsRow
				data-machine-state={machine.state}
				description={status ? <span className={machine.state === "online" ? undefined : "text-settings-muted"}>{status}</span> : undefined}
				icon={Laptop}
				label={machine.name}
			/>
			{machine.profiles.map((profile) => (
				<SettingsRow
					description={details(profile)}
					icon={UserRound}
					key={profile.id}
					label={profile.name}
					// Indented under its machine: a child row, not a sibling.
					leading={<span aria-hidden="true" className="w-5" />}
				>
					{profile.localCopy ? (
						<div className="flex items-center gap-1">
							<Button onClick={() => onUpdate(profile)} size="sm" type="button" variant="quiet">
								{t("settings.browserProfiles.machines.update")}
							</Button>
							<Button onClick={() => onRemove(profile)} size="sm" type="button" variant="destructive-ghost">
								{t("settings.browserProfiles.machines.remove")}
							</Button>
						</div>
					) : (
						<Button onClick={() => onCopy(profile)} size="sm" type="button" variant="quiet">
							{t("settings.browserProfiles.machines.copy")}
						</Button>
					)}
				</SettingsRow>
			))}
		</>
	);
}
