import type { BrowserProfile, BrowserProfileId } from "./browser-profiles";

/**
 * Browser profiles shared between machines linked to the same AO account.
 * Cookie values exist only in the two main processes; between them they
 * travel sealed to the receiving machine. Nothing here that the renderer
 * receives carries a cookie value.
 */

/** A profile another machine offers: counts only, never cookie values. */
export type SharedBrowserProfileSummary = {
	id: BrowserProfileId;
	name: string;
	sourceBrowser?: string;
	cookieCount: number;
	historyCount: number;
	updatedAt: string;
};

/** Why a linked machine's profiles cannot be listed right now. */
export type OtherMachineState =
	| "online"
	| "offline"
	/** Listed without an https address: nothing is copied over plain http. */
	| "insecure"
	/** It could not confirm this machine: not on its account listing, or no secure address here. */
	| "unverified"
	| "sharing-off"
	| "unauthorized"
	| "outdated"
	| "desktop-closed"
	| "failed";

export type OtherMachineProfile = SharedBrowserProfileSummary & {
	/** This machine's copy of the profile, when it has one. */
	localCopy?: { profileId: BrowserProfileId; copiedAt: string };
};

export type OtherMachine = {
	hostId: string;
	name: string;
	state: OtherMachineState;
	profiles: OtherMachineProfile[];
};

export type OtherMachinesResult = {
	/** Linked machines belong to an AO account; signed out there are none. */
	signedIn: boolean;
	machines: OtherMachine[];
	error?: string;
};

export type BrowserProfileCopyErrorCode =
	| "NOT_LINKED"
	| "OFFLINE"
	| "INSECURE"
	| "UNVERIFIED"
	| "SHARING_OFF"
	| "UNAUTHORIZED"
	| "OUTDATED"
	| "DESKTOP_CLOSED"
	| "NOT_SHARED"
	| "ALREADY_COPIED"
	| "PROFILE_LIMIT"
	| "PROFILE_ACTIVE"
	| "INVALID_DATA"
	| "FAILED";

export type BrowserProfileCopyResult =
	| {
		profile: BrowserProfile;
		importedCookies: number;
		skippedCookies: number;
		importedHistoryEntries: number;
		error?: undefined;
	}
	| { error: string; code: BrowserProfileCopyErrorCode; profile?: undefined };

export type BrowserProfileSharingState = {
	enabled: boolean;
	error?: string;
	/** The person at this machine declined the confirmation; nothing changed. */
	cancelled?: boolean;
};

/** The native confirmation main shows before sharing is turned on, worded from main's own catalog. */
export type BrowserProfileSharingConfirmLabels = {
	title: string;
	message: string;
	detail: string;
	cancel: string;
	confirm: string;
};

export type BrowserProfileSharingInput = { enabled: boolean };
