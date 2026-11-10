import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AoBridge } from "../../../preload";
import type { OtherMachinesResult } from "../../../shared/browser-profile-share";
import { TooltipProvider } from "../ui/tooltip";
import { BrowserProfilesSection } from "./BrowserProfilesSection";

const REMOTE_ID = "0f8fad5b-d9cb-469f-a165-70867728950e";
const COPY_ID = "22222222-2222-4222-8222-222222222222";
const copiedProfile = {
	id: COPY_ID,
	name: "Chrome (from Work laptop)",
	createdAt: "2026-11-10T10:00:00.000Z",
	updatedAt: "2026-11-10T10:00:00.000Z",
	source: {
		kind: "copy" as const,
		hostId: "h_laptop",
		machineName: "Work laptop",
		remoteProfileId: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
		copiedAt: "2026-11-10T10:00:00.000Z",
	},
};

const machines: OtherMachinesResult = {
	signedIn: true,
	machines: [
		{
			hostId: "h_brave",
			name: "Brave box",
			state: "online",
			profiles: [{ id: REMOTE_ID, name: "Brave", sourceBrowser: "Brave", cookieCount: 1234, historyCount: 56, updatedAt: new Date().toISOString() }],
		},
		{
			hostId: "h_laptop",
			name: "Work laptop",
			state: "online",
			profiles: [{
				id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
				name: "Chrome",
				cookieCount: 3,
				historyCount: 4,
				updatedAt: new Date().toISOString(),
				localCopy: { profileId: COPY_ID, copiedAt: new Date().toISOString() },
			}],
		},
		{ hostId: "h_closed", name: "Studio", state: "sharing-off", profiles: [] },
		{ hostId: "h_off", name: "Travel", state: "offline", profiles: [] },
		{ hostId: "h_lan", name: "Desk", state: "insecure", profiles: [] },
		{ hostId: "h_new", name: "Spare", state: "unverified", profiles: [] },
	],
};

function makeBridge(overrides: Partial<AoBridge["browserProfiles"]> = {}): AoBridge["browserProfiles"] {
	return {
		list: vi.fn(async () => ({ profiles: [copiedProfile], defaultProfileId: null })),
		create: vi.fn(),
		rename: vi.fn(),
		clear: vi.fn(async () => undefined),
		usage: vi.fn(async () => ({ sessions: [] })),
		delete: vi.fn(async () => ({})),
		setDefault: vi.fn(async () => undefined),
		discoverImportSources: vi.fn(async () => ({ sources: [] })),
		import: vi.fn(async () => ({ sourceName: "", entries: [] })),
		onImportProgress: vi.fn(() => () => undefined),
		getSharing: vi.fn(async () => ({ enabled: false })),
		setSharing: vi.fn(async ({ enabled }: { enabled: boolean }) => ({ enabled })),
		listOtherMachines: vi.fn(async () => machines),
		copyFromMachine: vi.fn(async () => ({
			profile: { ...copiedProfile, id: "33333333-3333-4333-8333-333333333333", name: "Brave (from Brave box)" },
			importedCookies: 1234,
			skippedCookies: 0,
			importedHistoryEntries: 56,
		})),
		updateCopy: vi.fn(async () => ({ profile: copiedProfile, importedCookies: 3, skippedCookies: 0, importedHistoryEntries: 4 })),
		...overrides,
	} as AoBridge["browserProfiles"];
}

describe("Browser settings: other machines", () => {
	let originalBridge: AoBridge["browserProfiles"];

	afterEach(() => {
		if (window.ao) window.ao.browserProfiles = originalBridge;
	});

	function install(bridge: AoBridge["browserProfiles"]) {
		originalBridge = window.ao!.browserProfiles;
		window.ao!.browserProfiles = bridge;
	}

	it("turns sharing on only when the owner flips the switch, asking through main's native confirmation", async () => {
		const bridge = makeBridge();
		install(bridge);
		render(<TooltipProvider><BrowserProfilesSection /></TooltipProvider>);
		const toggle = await screen.findByRole("switch", { name: "Share my browser profiles with my linked machines" });
		await waitFor(() => expect(toggle).toBeEnabled());
		expect(toggle).not.toBeChecked();
		expect(bridge.setSharing).not.toHaveBeenCalled();
		await userEvent.click(toggle);
		await waitFor(() => expect(bridge.setSharing).toHaveBeenCalledWith({
			enabled: true,
			labels: {
				title: "Share your browser profiles?",
				message: expect.stringContaining("including its sign-ins and history"),
				detail: expect.stringContaining("copies already made stay on those machines"),
				cancel: "Cancel",
				confirm: "Share profiles",
			},
		}));
		await waitFor(() => expect(toggle).toBeChecked());
		await userEvent.click(toggle);
		await waitFor(() => expect(bridge.setSharing).toHaveBeenLastCalledWith({ enabled: false }));
	});

	it("stays off when the owner declines the confirmation", async () => {
		const bridge = makeBridge({ setSharing: vi.fn(async () => ({ enabled: false, cancelled: true })) });
		install(bridge);
		render(<TooltipProvider><BrowserProfilesSection /></TooltipProvider>);
		const toggle = await screen.findByRole("switch", { name: "Share my browser profiles with my linked machines" });
		await waitFor(() => expect(toggle).toBeEnabled());
		await userEvent.click(toggle);
		await waitFor(() => expect(bridge.setSharing).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(toggle).toBeEnabled());
		expect(toggle).not.toBeChecked();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	it("lists each machine with its profiles or the reason it cannot", async () => {
		install(makeBridge());
		render(<TooltipProvider><BrowserProfilesSection /></TooltipProvider>);
		expect(await screen.findByText("Brave box")).toBeInTheDocument();
		expect(screen.getByText(/Brave · 1,234 cookies · 56 history entries · Updated/)).toBeInTheDocument();
		expect(screen.getByText("Sharing is off on Studio. Turn on sharing in its Browser settings.")).toBeInTheDocument();
		expect(screen.getByText("Travel is offline.")).toBeInTheDocument();
		expect(screen.getByText("Desk has no secure address, so its profiles can't be copied. Turn on Allow access from your devices on Desk.")).toBeInTheDocument();
		expect(screen.getByText(/^Spare could not confirm this machine\./)).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Copy to this machine" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Update" })).toBeInTheDocument();
	});

	it("copies after confirming, with the device sign-in note and the localized name", async () => {
		const bridge = makeBridge();
		install(bridge);
		render(<TooltipProvider><BrowserProfilesSection /></TooltipProvider>);
		await userEvent.click(await screen.findByRole("button", { name: "Copy to this machine" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Copy Brave to this machine?")).toBeInTheDocument();
		expect(within(dialog).getByText(/Some sites tie a sign-in to the device/)).toBeInTheDocument();
		expect(bridge.copyFromMachine).not.toHaveBeenCalled();
		await userEvent.click(within(dialog).getByRole("button", { name: "Copy" }));
		await waitFor(() => expect(bridge.copyFromMachine).toHaveBeenCalledWith({
			hostId: "h_brave",
			profileId: REMOTE_ID,
			name: "Brave (from Brave box)",
		}));
		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
		// The new profile is reloaded into the profile list (and so every picker).
		expect(bridge.list).toHaveBeenCalledTimes(2);
	});

	it("explains a refused copy in the dialog", async () => {
		install(makeBridge({ copyFromMachine: vi.fn(async () => ({ error: "x", code: "SHARING_OFF" as const })) }));
		render(<TooltipProvider><BrowserProfilesSection /></TooltipProvider>);
		await userEvent.click(await screen.findByRole("button", { name: "Copy to this machine" }));
		await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Copy" }));
		expect(await screen.findByText("Sharing is off on Brave box. Turn on sharing in its Browser settings.")).toBeInTheDocument();
	});

	it("updates a copy in place and removes it through the confirmed local delete", async () => {
		const bridge = makeBridge();
		install(bridge);
		render(<TooltipProvider><BrowserProfilesSection /></TooltipProvider>);
		await userEvent.click(await screen.findByRole("button", { name: "Update" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Update Chrome (from Work laptop)?")).toBeInTheDocument();
		await userEvent.click(within(dialog).getByRole("button", { name: "Update" }));
		await waitFor(() => expect(bridge.updateCopy).toHaveBeenCalledWith(COPY_ID));
		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

		await userEvent.click(screen.getByRole("button", { name: "Remove" }));
		const confirm = await screen.findByRole("dialog");
		expect(within(confirm).getByText("Delete Chrome (from Work laptop)?")).toBeInTheDocument();
		await userEvent.click(await within(confirm).findByRole("button", { name: "Delete profile" }));
		await waitFor(() => expect(bridge.delete).toHaveBeenCalledWith(COPY_ID, []));
	});

	it("says when no account is signed in", async () => {
		install(makeBridge({ listOtherMachines: vi.fn(async () => ({ signedIn: false, machines: [] })) }));
		render(<TooltipProvider><BrowserProfilesSection /></TooltipProvider>);
		expect(await screen.findByText("Sign in to Orchestrator Cloud to see your other machines.")).toBeInTheDocument();
	});
});
