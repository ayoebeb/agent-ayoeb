import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Settings } from "../../hooks/useSettings";

const { settingsMock, updateRetentionMock, updateOrchestratorWaitingMock, setSoundMock } = vi.hoisted(() => ({
	settingsMock: vi.fn(),
	updateRetentionMock: vi.fn(),
	updateOrchestratorWaitingMock: vi.fn(),
	setSoundMock: vi.fn(),
}));

const idle = { update: vi.fn(), saving: false, error: undefined };
vi.mock("../../hooks/useSettings", () => ({
	useSettings: () => ({ settings: settingsMock(), isLoading: false, error: null }),
	useUpdateSessionInterface: () => idle,
	useUpdateVersionRefresh: () => idle,
	useUpdateSessionRetention: () => ({ update: updateRetentionMock, saving: false, error: undefined }),
	useUpdateOrchestratorWaiting: () => ({ update: updateOrchestratorWaitingMock, saving: false, error: undefined }),
}));

// The desktop app plays the sound, so its switch lives in the app's own UI settings.
vi.mock("../../lib/bridge", async (importOriginal) => {
	const original = await importOriginal<typeof import("../../lib/bridge")>();
	return {
		...original,
		aoBridge: {
			...original.aoBridge,
			uiSettings: {
				get: async () => ({ locale: "en", soundNotificationsEnabled: true, orchestratorWaitingSoundEnabled: true, terminalShell: { kind: "auto" } }),
				set: setSoundMock,
			},
		},
	};
});

vi.mock("../../hooks/useEditorHandoff", () => ({
	useEditorPreference: () => ({ data: { editors: [] }, isPending: false, isError: false }),
	useSetPreferredEditor: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
}));

// The real rows read the cloud gate and the CLI link; this file covers retention.
vi.mock("./DefaultCloudSandboxRow", () => ({ DefaultCloudSandboxSection: () => null }));
vi.mock("./CliLinkSettingsRow", () => ({ CliLinkSettings: () => null }));

import { GeneralSettingsSection } from "./GeneralSettingsSection";
import { TooltipProvider } from "../ui/tooltip";

function renderSection() {
	render(
		<QueryClientProvider client={new QueryClient()}>
			<TooltipProvider>
				<GeneralSettingsSection />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

function settings(overrides: Partial<Settings>): Settings {
	return {
		defaultSessionMode: "tui",
		harnessDefaults: {},
		chatHarnesses: [],
		client: "",
		localEnabled: true,
		cloudEnabled: false,
		cloudControlPlaneUrl: "",
		trackerIntakeEnabled: false,
		...overrides,
	};
}

beforeEach(() => {
	settingsMock.mockReset();
	updateRetentionMock.mockReset();
	updateOrchestratorWaitingMock.mockReset();
	setSoundMock.mockReset().mockResolvedValue({});
});

describe("orchestrator waiting alerts", () => {
	const withAlerts = (overrides: Partial<Settings> = {}) =>
		settings({ orchestratorWaitingEnabled: true, orchestratorWaitingRenotifyMinutes: 10, ...overrides });

	it("is on by default with a ten-minute reminder", () => {
		settingsMock.mockReturnValue(withAlerts());
		renderSection();

		expect(screen.getByRole("switch", { name: "Tell me when an orchestrator is waiting for me" })).toBeChecked();
		expect(screen.getByRole("switch", { name: "Play a sound when an orchestrator is waiting" })).toBeChecked();
		expect(screen.getByRole("button", { name: "Remind me again if I have not replied" })).toHaveTextContent("After 10 minutes");
	});

	// The daemon rejects a partial write, so each change carries the other value.
	it("turns the notification off without losing the reminder delay", async () => {
		settingsMock.mockReturnValue(withAlerts({ orchestratorWaitingRenotifyMinutes: 30 }));
		renderSection();

		await userEvent.click(screen.getByRole("switch", { name: "Tell me when an orchestrator is waiting for me" }));

		expect(updateOrchestratorWaitingMock).toHaveBeenCalledWith({ enabled: false, renotifyMinutes: 30 });
	});

	it("saves the chosen reminder delay, including no reminder, and keeps the switch as it is", async () => {
		settingsMock.mockReturnValue(withAlerts());
		renderSection();

		await userEvent.click(screen.getByRole("button", { name: "Remind me again if I have not replied" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "After 30 minutes" }));
		expect(updateOrchestratorWaitingMock).toHaveBeenLastCalledWith({ enabled: true, renotifyMinutes: 30 });

		await userEvent.click(screen.getByRole("button", { name: "Remind me again if I have not replied" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Never" }));
		expect(updateOrchestratorWaitingMock).toHaveBeenLastCalledWith({ enabled: true, renotifyMinutes: 0 });
	});

	it("shows a delay set outside the menu as itself", () => {
		settingsMock.mockReturnValue(withAlerts({ orchestratorWaitingRenotifyMinutes: 17 }));
		renderSection();

		expect(screen.getByRole("button", { name: "Remind me again if I have not replied" })).toHaveTextContent("After 17 minutes");
	});

	it("saves the sound preference in the app, separately from the general sound", async () => {
		settingsMock.mockReturnValue(withAlerts());
		renderSection();

		await userEvent.click(screen.getByRole("switch", { name: "Play a sound when an orchestrator is waiting" }));

		expect(setSoundMock).toHaveBeenCalledWith({ orchestratorWaitingSoundEnabled: false });
		expect(setSoundMock).not.toHaveBeenCalledWith(expect.objectContaining({ soundNotificationsEnabled: expect.anything() }));
	});

	it("leaves the sound and reminder controls off while the notification is off", () => {
		settingsMock.mockReturnValue(withAlerts({ orchestratorWaitingEnabled: false }));
		renderSection();

		expect(screen.getByRole("switch", { name: "Tell me when an orchestrator is waiting for me" })).not.toBeChecked();
		expect(screen.getByRole("switch", { name: "Play a sound when an orchestrator is waiting" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Remind me again if I have not replied" })).toBeDisabled();
	});

	it("hides the controls on a daemon that cannot apply them", () => {
		settingsMock.mockReturnValue(settings({}));
		renderSection();

		expect(screen.queryByRole("switch", { name: "Tell me when an orchestrator is waiting for me" })).not.toBeInTheDocument();
	});
});

describe("archived session retention", () => {
	it("saves the chosen number of days", async () => {
		settingsMock.mockReturnValue(settings({ sessionRetentionDays: 0 }));
		renderSection();

		const menu = screen.getByRole("button", { name: "Delete archived sessions" });
		expect(menu).toHaveTextContent("Never");
		await userEvent.click(menu);
		await userEvent.click(await screen.findByRole("menuitem", { name: "After 30 days" }));

		expect(updateRetentionMock).toHaveBeenCalledWith(30);
	});

	it("shows a value set outside the menu as itself", () => {
		settingsMock.mockReturnValue(settings({ sessionRetentionDays: 14 }));
		renderSection();

		expect(screen.getByRole("button", { name: "Delete archived sessions" })).toHaveTextContent("After 14 days");
	});

	it("hides the control on a daemon that cannot apply it", () => {
		settingsMock.mockReturnValue(settings({}));
		renderSection();

		expect(screen.queryByRole("button", { name: "Delete archived sessions" })).not.toBeInTheDocument();
	});
});
