import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { apiClient } from "../../lib/api-client";
import { linkThisComputer } from "../../lib/link-this-computer";

// Removing a remote host offers this computer a new connection password: the
// removed machine may still hold the old one, which opens this computer to it
// whatever the account lists.

const fake = vi.hoisted(() => ({
	saved: [] as Array<{ hostId: string; label: string; url: string }>,
	status: { enabled: true, noPublicTunnel: false, password: "pw-1", hostId: "h_this", endpoints: [] as unknown[] },
	failRegenerate: false,
}));
const remotes = vi.hoisted(() => ({
	list: vi.fn(async () => fake.saved),
	add: vi.fn(),
	connect: vi.fn(),
	disconnect: vi.fn(async () => undefined),
	remove: vi.fn(async (url: string) => { fake.saved = fake.saved.filter((host) => host.url !== url); }),
	update: vi.fn(),
}));
vi.mock("../../lib/bridge", () => ({ aoBridge: { remotes } }));
vi.mock("../../lib/cloud-session", () => ({
	useCloudSession: () => ({ status: "authenticated", signIn: vi.fn(), session: { user: { id: "user_1" } } }),
}));
vi.mock("../../hooks/useCloudLocalAuth", () => ({ useCloudLocalAuth: () => ({ available: false }) }));
vi.mock("../../hooks/useSettings", () => ({ useSettings: () => ({ settings: { cloudControlPlaneUrl: "https://cp.example.test" } }) }));
vi.mock("../../lib/account-remote-hosts", async (original) => ({
	...(await original<typeof import("../../lib/account-remote-hosts")>()),
	deleteAccountRemoteHost: vi.fn(async () => undefined),
	listAccountRemoteHosts: vi.fn(async () => []),
}));
vi.mock("../../lib/link-this-computer", () => ({ linkThisComputer: vi.fn(async () => ({ hostId: "h_this", added: false })) }));
vi.mock("../../lib/api-client", () => ({
	apiClient: {
		GET: vi.fn(async () => ({ data: structuredClone(fake.status) })),
		POST: vi.fn(async (path: string) => {
			if (path === "/api/v1/mobile/regenerate") {
				if (fake.failRegenerate) return { error: { error: { message: "rotation failed" } } };
				fake.status.password = "pw-2";
			}
			return { data: structuredClone(fake.status) };
		}),
	},
	apiErrorMessage: () => "Could not change the password.",
}));

import { RemoteHostsSettings } from "./RemoteHostsSettings";

const posted = () => (vi.mocked(apiClient.POST).mock.calls as unknown[][]).map((call) => call[0]);

function renderSettings() {
	fake.saved = [{ hostId: "h_old_laptop", label: "Old laptop", url: "https://old-laptop.example.test" }];
	render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><RemoteHostsSettings /></QueryClientProvider>);
}

async function removeOldLaptop() {
	fireEvent.click(await screen.findByRole("button", { name: "Remove Old laptop" }));
	fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
	await waitFor(() => expect(remotes.remove).toHaveBeenCalledWith("https://old-laptop.example.test"));
}

afterEach(() => {
	fake.status = { enabled: true, noPublicTunnel: false, password: "pw-1", hostId: "h_this", endpoints: [] };
	fake.failRegenerate = false;
	vi.clearAllMocks();
});

it("offers a new connection password after a host is removed, and changes it in one click", async () => {
	renderSettings();
	await removeOldLaptop();
	const offer = await screen.findByRole("dialog", { name: "Change this computer's connection password?" });
	expect(offer).toHaveTextContent("If Old laptop was ever paired with this computer");
	expect(offer).toHaveTextContent("need to pair again");
	expect(posted()).not.toContain("/api/v1/mobile/regenerate");

	fireEvent.click(within(offer).getByRole("button", { name: "Change password" }));
	await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	expect(posted()).toEqual(["/api/v1/mobile/regenerate"]);
	// The new password revoked the account link; this computer is listed again with it.
	expect(linkThisComputer).toHaveBeenCalledWith("https://cp.example.test", "user_1", "h_this", "pw-2", { ifListed: true });
});

it("keeps the old password when the owner chooses Not now", async () => {
	renderSettings();
	await removeOldLaptop();
	const offer = await screen.findByRole("dialog", { name: "Change this computer's connection password?" });
	fireEvent.click(within(offer).getByRole("button", { name: "Not now" }));
	await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	expect(posted()).not.toContain("/api/v1/mobile/regenerate");
});

it("does not offer a new password when this computer is not reachable remotely", async () => {
	fake.status.enabled = false;
	renderSettings();
	await removeOldLaptop();
	await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	await new Promise((resolve) => setTimeout(resolve, 50));
	expect(screen.queryByRole("dialog", { name: "Change this computer's connection password?" })).toBeNull();
	expect(posted()).not.toContain("/api/v1/mobile/regenerate");
});

it("says when the account link fails after the new password, and retries the link without changing the password again", async () => {
	vi.mocked(linkThisComputer).mockRejectedValueOnce(new Error("AO Cloud host sync failed (502)."));
	renderSettings();
	await removeOldLaptop();
	const offer = await screen.findByRole("dialog", { name: "Change this computer's connection password?" });
	fireEvent.click(within(offer).getByRole("button", { name: "Change password" }));

	// The password did change, so the offer closes; the failed link is shown in the section.
	const alert = await screen.findByRole("alert");
	expect(alert).toHaveTextContent("The new password is set, but this computer could not be linked to your Orchestrator account again");
	expect(alert).toHaveTextContent("AO Cloud host sync failed (502).");
	expect(screen.queryByRole("dialog")).toBeNull();
	expect(posted()).toEqual(["/api/v1/mobile/regenerate"]);

	fireEvent.click(screen.getByRole("button", { name: "Retry" }));
	await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
	expect(linkThisComputer).toHaveBeenCalledTimes(2);
	expect(linkThisComputer).toHaveBeenLastCalledWith("https://cp.example.test", "user_1", "h_this", "pw-2", { ifListed: true });
	expect(posted()).toEqual(["/api/v1/mobile/regenerate"]);
});

it("explains a failed change and keeps the offer open to try again", async () => {
	fake.failRegenerate = true;
	renderSettings();
	await removeOldLaptop();
	const offer = await screen.findByRole("dialog", { name: "Change this computer's connection password?" });
	fireEvent.click(within(offer).getByRole("button", { name: "Change password" }));
	expect(await within(offer).findByRole("alert")).toHaveTextContent("Could not change the password.");
	expect(screen.getByRole("dialog", { name: "Change this computer's connection password?" })).toBeVisible();
});
