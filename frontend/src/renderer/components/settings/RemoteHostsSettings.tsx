import { Pencil, Server, Trash2 } from "@/components/app-icons";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import { disconnectHost } from "../../lib/host-clients";
import { requestRemoteHostsRefresh } from "../../hooks/useRemoteHosts";
import { useCloudLocalAuth } from "../../hooks/useCloudLocalAuth";
import { useCloudSession } from "../../lib/cloud-session";
import { deleteAccountRemoteHost } from "../../lib/account-remote-hosts";
import { useSettings } from "../../hooks/useSettings";
import { useLocalSignInDialogStore } from "../../stores/local-signin-dialog-store";
import { useUiStore } from "../../stores/ui-store";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Switch } from "../ui/switch";
import { SettingsRow } from "./SettingsRow";
import { fetchMobileStatus, mobileStatusQueryKey, relinkThisComputer, rotatePairingPassword } from "./ConnectMobileContent";
import { SettingsSection } from "./SettingsSection";
import { ThisComputerAccess } from "./ThisComputerAccess";

type SavedHost = { hostId: string; label: string; url: string };

export function RemoteHostsSettings({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const enabled = useUiStore((state) => state.remoteHosts);
	const setEnabled = useUiStore((state) => state.setRemoteHosts);
	const { status, signIn, session } = useCloudSession();
	const queryClient = useQueryClient();
	const { settings } = useSettings();
	const cloudBaseUrl = settings?.cloudControlPlaneUrl ?? "";
	const { available: localAuthAvailable } = useCloudLocalAuth();
	const openLocalSignIn = useLocalSignInDialogStore((state) => state.openDialog);
	const [saved, setSaved] = useState<SavedHost[]>([]);
	const [loading, setLoading] = useState(true);
	const [loadError, setLoadError] = useState("");
	const [label, setLabel] = useState("");
	const [url, setUrl] = useState("");
	const [password, setPassword] = useState("");
	const [editing, setEditing] = useState<SavedHost | null>(null);
	const [busy, setBusy] = useState(false);
	const [formError, setFormError] = useState("");
	const [confirmingRemoval, setConfirmingRemoval] = useState<SavedHost | null>(null);
	const [removeBusy, setRemoveBusy] = useState(false);
	const [removeError, setRemoveError] = useState("");
	// The label of a host just removed, while offering this computer a new connection password.
	const [rotationOffer, setRotationOffer] = useState<string | null>(null);
	const [rotateBusy, setRotateBusy] = useState(false);
	const [rotateError, setRotateError] = useState("");
	// The new password is set, but the account still lists this computer with
	// the old one: why the link failed, until a retry links it.
	const [relinkError, setRelinkError] = useState("");
	const [relinkBusy, setRelinkBusy] = useState(false);
	const formRef = useRef<HTMLFormElement>(null);

	const load = async () => {
		setLoading(true);
		setLoadError("");
		try {
			setSaved(await aoBridge.remotes.list());
		} catch (cause) {
			setLoadError(cause instanceof Error ? cause.message : t("remote.loadHostsFailed"));
		} finally {
			setLoading(false);
		}
	};

	useEffect(() => {
		if (status === "authenticated") void load();
		else setSaved([]);
		// The bridge is fixed for the lifetime of this Settings page.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [status]);

	const resetForm = () => {
		setEditing(null);
		setLabel("");
		setUrl("");
		setPassword("");
		setFormError("");
	};

	const save = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		const nextLabel = label.trim();
		const nextUrl = url.trim();
		if (!nextLabel || !nextUrl || (!editing?.hostId && !password)) return;
		setBusy(true);
		setFormError("");
		try {
			// Old saved records have no identity, so re-pair them through add.
			const health = editing?.hostId
				? await aoBridge.remotes.update(editing.url, {
					label: nextLabel,
					url: nextUrl,
					...(password ? { password } : {}),
				})
				: await aoBridge.remotes.add({ label: nextLabel, url: nextUrl, password });
			if (health === "offline") throw new Error(t("remote.hostOffline"));
			if (health === "unauthorized") throw new Error(t("remote.hostUnauthorized"));
			if (health === "not-a-daemon") throw new Error(t("remote.hostNotDaemon"));
			if (health === "incompatible") throw new Error(t("remote.hostIncompatible"));
			if (editing?.hostId) await disconnectHost(editing.hostId);
			if (editing && !editing.hostId && editing.url !== nextUrl) await aoBridge.remotes.remove(editing.url);
			const current = (await aoBridge.remotes.list()).find((item) => item.url === nextUrl);
			if (current?.hostId && cloudBaseUrl) await aoBridge.remotes.linkAccountHost(current.url);
			await load();
			resetForm();
			if (!enabled) setEnabled(true);
			requestRemoteHostsRefresh();
		} catch (cause) {
			setFormError(cause instanceof Error ? cause.message : t("remote.addHostFailed"));
		} finally {
			setBusy(false);
		}
	};

	const remove = async () => {
		if (!confirmingRemoval) return;
		const removed = confirmingRemoval;
		setRemoveBusy(true);
		setRemoveError("");
		try {
			if (removed.hostId && cloudBaseUrl) await deleteAccountRemoteHost(cloudBaseUrl, removed.hostId);
			await aoBridge.remotes.remove(removed.url);
			if (removed.hostId) await disconnectHost(removed.hostId);
			await load();
			setConfirmingRemoval(null);
			requestRemoteHostsRefresh();
		} catch (cause) {
			setRemoveError(cause instanceof Error ? cause.message : t("remote.removeHostFailed"));
			return;
		} finally {
			setRemoveBusy(false);
		}
		// Removing a host here does not take this computer's connection password
		// back from it: a machine that was ever paired with this computer can
		// keep using it. While this computer is reachable remotely, offer a new one.
		const reachable = await fetchMobileStatus().then((mobile) => mobile.enabled === true, () => false);
		if (reachable) {
			setRotateError("");
			setRotationOffer(removed.label);
		}
	};

	const relinkOptions = () => ({
		cloudBaseUrl,
		accountId: session?.user.id ?? "",
		tunnelTimeoutMessage: t("remote.shareThisComputerTimeout"),
		// Shown outside the offer, which has closed: retrying links again with the new password, never a second one.
		onLinkError: (cause: unknown) => setRelinkError(cause instanceof Error && cause.message ? cause.message : t("remote.shareThisComputerFailed")),
	});

	const rotate = async () => {
		setRotateBusy(true);
		setRotateError("");
		setRelinkError("");
		try {
			await rotatePairingPassword({
				...relinkOptions(),
				onRotated: () => {
					// The password changed: the offer is done whatever the link does next.
					setRotationOffer(null);
					void queryClient.invalidateQueries({ queryKey: mobileStatusQueryKey });
				},
			});
		} catch (cause) {
			setRotateError(cause instanceof Error && cause.message ? cause.message : t("remote.rotatePasswordFailed"));
		} finally {
			setRotateBusy(false);
		}
	};

	const relink = async () => {
		setRelinkBusy(true);
		setRelinkError("");
		try {
			await relinkThisComputer(await fetchMobileStatus(), relinkOptions());
		} catch (cause) {
			relinkOptions().onLinkError(cause);
		} finally {
			setRelinkBusy(false);
		}
	};

	if (status !== "authenticated") return (
		<SettingsSection title={t("settings.remoteHosts")} sectionId="remoteHosts" titleHidden={titleHidden} grouped>
			<SettingsRow label={t("remote.aoCloud")} description={t("remote.signInDescription")}>{status === "unauthenticated" ? (
				<Button onClick={() => localAuthAvailable ? openLocalSignIn() : signIn()} size="sm" type="button" variant="quiet">
					{t("shell.signIn")}
				</Button>
			) : null}</SettingsRow>
		</SettingsSection>
	);

	return <>
		<SettingsSection title={t("settings.remoteHosts")} sectionId="remoteHosts" titleHidden={titleHidden} grouped>
			<SettingsRow label={t("remote.enable")} description={t("remote.enableDescription")}>
				<Switch aria-label={t("remote.enable")} checked={enabled} onCheckedChange={setEnabled} />
			</SettingsRow>
		</SettingsSection>

		<ThisComputerAccess />

		<SettingsSection title={t("remote.savedHosts")} grouped>
			{relinkError ? (
				<SettingsRow
					label={t("remote.thisComputer")}
					description={<span className="block text-pretty text-error" role="alert">{t("remote.rotateRelinkFailed")} {relinkError}</span>}
				>
					<Button disabled={relinkBusy} onClick={() => void relink()} size="sm" type="button" variant="quiet">{t("notify.retry")}</Button>
				</SettingsRow>
			) : null}
			{loading && saved.length === 0 ? <div className="settings-row-bar text-sm text-settings-muted" role="status">{t("remote.loadingHosts")}</div> : null}
			{!loading && saved.length === 0 && !loadError ? <div className="settings-row-bar text-pretty text-sm text-settings-muted">{t("remote.emptyHosts")}</div> : null}
			{saved.map((host) => (
				<SettingsRow
					key={host.url}
					icon={Server}
					label={host.label}
					description={host.hostId ? host.url : (
						<>
							{host.url}
							<span className="mt-0.5 block text-warning">{t("remote.repairRequired")}</span>
						</>
					)}
				>
					<div className="flex items-center gap-1">
						<Button aria-label={t("remote.editHost", { label: host.label })} disabled={busy || removeBusy} onClick={() => {
							setEditing(host);
							setLabel(host.label);
							setUrl(host.url);
							setPassword("");
							setFormError("");
							formRef.current?.scrollIntoView?.({ block: "nearest" });
						}} size="icon-sm" type="button" variant="quiet"><Pencil aria-hidden="true" className="size-icon-base" /></Button>
						<Button aria-label={t("remote.removeHost", { label: host.label })} disabled={busy || removeBusy} onClick={() => {
							setConfirmingRemoval(host);
							setRemoveError("");
						}} size="icon-sm" type="button" variant="destructive-ghost"><Trash2 aria-hidden="true" className="size-icon-base" /></Button>
					</div>
				</SettingsRow>
			))}
			{loadError ? (
				<SettingsRow label={t("remote.savedHosts")} description={<span className="text-error" role="alert">{loadError}</span>}>
					<Button onClick={() => void load()} size="sm" type="button" variant="quiet">{t("notify.retry")}</Button>
				</SettingsRow>
			) : null}
		</SettingsSection>

		{/* The form wraps the whole section so its rows stay direct children of
		    the grouped card and pick up the card's dividers. */}
		<form className="flex w-full flex-col" onSubmit={(event) => void save(event)} ref={formRef}>
			<SettingsSection title={editing ? t("remote.editHost", { label: editing.label }) : t("remote.addHost")} grouped>
				<p className="settings-content-row text-pretty text-control leading-4 text-settings-muted">{t("remote.setupHint")}</p>
				<SettingsRow label={t("remote.name")}><Input aria-label={t("remote.name")} className="w-80 max-w-full" disabled={busy} maxLength={64} onChange={(event) => setLabel(event.target.value)} required value={label} /></SettingsRow>
				<SettingsRow label={t("mobile.address")}><Input aria-label={t("mobile.address")} className="w-80 max-w-full" disabled={busy} inputMode="url" onChange={(event) => setUrl(event.target.value)} placeholder={t("remote.addressPlaceholder")} required value={url} /></SettingsRow>
				<SettingsRow label={t("remote.connectionPassword")}><Input aria-label={t("remote.connectionPassword")} autoComplete="off" className="w-80 max-w-full" disabled={busy} onChange={(event) => setPassword(event.target.value)} placeholder={editing?.hostId ? t("remote.keepPassword") : undefined} required={!editing?.hostId} type="password" value={password} /></SettingsRow>
				<div className="settings-content-row flex items-center justify-end gap-2">
					{formError ? <p className="mr-auto min-w-0 text-xs text-error" role="alert">{formError}</p> : null}
					{editing ? <Button disabled={busy} onClick={resetForm} size="sm" type="button" variant="quiet">{t("confirm.cancel")}</Button> : null}
					<Button disabled={busy || !label.trim() || !url.trim() || (!editing?.hostId && !password)} size="sm" type="submit" variant="quiet">{busy ? t("remote.saving") : editing ? t("remote.saveHost") : t("remote.addHost")}</Button>
				</div>
			</SettingsSection>
		</form>

		<ConfirmDialog
			busy={removeBusy}
			confirmLabel={t("shell.remove")}
			description={t("remote.removeDescription")}
			destructive
			error={removeError || null}
			onConfirm={() => void remove()}
			onOpenChange={(open) => { if (!open && !removeBusy) setConfirmingRemoval(null); }}
			open={confirmingRemoval !== null}
			title={t("remote.removeTitle", { label: confirmingRemoval?.label })}
		/>

		<ConfirmDialog
			busy={rotateBusy}
			cancelLabel={t("remote.rotateAfterRemoveLater")}
			confirmLabel={t("remote.rotateAfterRemoveConfirm")}
			description={t("remote.rotateAfterRemoveDescription", { label: rotationOffer ?? "" })}
			error={rotateError || null}
			onConfirm={() => void rotate()}
			onOpenChange={(open) => { if (!open && !rotateBusy) setRotationOffer(null); }}
			open={rotationOffer !== null}
			title={t("remote.rotateAfterRemoveTitle")}
		/>
	</>;
}
