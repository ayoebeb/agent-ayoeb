import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Settings } from "../../hooks/useSettings";

const { settingsMock, updateRetentionMock, updateUsageLimitResumeMock } = vi.hoisted(() => ({
	settingsMock: vi.fn(),
	updateRetentionMock: vi.fn(),
	updateUsageLimitResumeMock: vi.fn(),
}));

const idle = { update: vi.fn(), saving: false, error: undefined };
vi.mock("../../hooks/useSettings", () => ({
	useSettings: () => ({ settings: settingsMock(), isLoading: false, error: null }),
	useUpdateSessionInterface: () => idle,
	useUpdateVersionRefresh: () => idle,
	useUpdateSessionRetention: () => ({ update: updateRetentionMock, saving: false, error: undefined }),
	useUpdateUsageLimitResume: () => ({ update: updateUsageLimitResumeMock, saving: false, error: undefined }),
}));

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
	updateUsageLimitResumeMock.mockReset();
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

describe("continuing sessions after a usage limit", () => {
	it("turns the automatic resume off and keeps the cap", async () => {
		settingsMock.mockReturnValue(settings({ usageLimitAutoResume: true, usageLimitMaxResumes: 3 }));
		renderSection();

		await userEvent.click(screen.getByRole("switch", { name: "Continue after a usage limit" }));

		expect(updateUsageLimitResumeMock).toHaveBeenCalledWith({ enabled: false, maxConcurrent: 3 });
	});

	it("saves the most sessions that continue at once", async () => {
		settingsMock.mockReturnValue(settings({ usageLimitAutoResume: true, usageLimitMaxResumes: 3 }));
		renderSection();

		const menu = screen.getByRole("button", { name: "Most that continue at once" });
		expect(menu).toHaveTextContent("3");
		await userEvent.click(menu);
		await userEvent.click(await screen.findByRole("menuitem", { name: "2" }));

		expect(updateUsageLimitResumeMock).toHaveBeenCalledWith({ enabled: true, maxConcurrent: 2 });
	});

	it("hides the cap while the setting is off", () => {
		settingsMock.mockReturnValue(settings({ usageLimitAutoResume: false, usageLimitMaxResumes: 3 }));
		renderSection();

		expect(screen.getByRole("switch", { name: "Continue after a usage limit" })).not.toBeChecked();
		expect(screen.queryByRole("button", { name: "Most that continue at once" })).not.toBeInTheDocument();
	});

	it("hides the control on a daemon that cannot apply it", () => {
		settingsMock.mockReturnValue(settings({}));
		renderSection();

		expect(screen.queryByRole("switch", { name: "Continue after a usage limit" })).not.toBeInTheDocument();
	});
});
