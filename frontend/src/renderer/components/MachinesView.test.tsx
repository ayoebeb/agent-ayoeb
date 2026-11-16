import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RemoteHost } from "../hooks/useRemoteHosts";
import { ShellProvider } from "../lib/shell-context";
import { useMachineAccountsStore } from "../stores/machine-accounts-store";
import { useUiStore } from "../stores/ui-store";
import { MachinesView } from "./MachinesView";
import { TooltipProvider } from "./ui/tooltip";

vi.mock("../lib/api-client", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/api-client")>()),
	getApiBaseUrl: () => "http://local.test",
}));
vi.mock("../lib/host-clients", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/host-clients")>()),
	baseUrlForHost: (hostId: string) => `http://host.test/${hostId}`,
}));

const probe = (version?: string) => ({ status: "ok", service: "agent-orchestrator-daemon", pid: 1, ...(version ? { version } : {}) });

function stubHealth(responses: Record<string, unknown>) {
	vi.stubGlobal("fetch", vi.fn(async (url: string) => {
		const body = responses[url];
		return body ? new Response(JSON.stringify(body), { status: 200 }) : new Response("no", { status: 503 });
	}));
}

const boxA: RemoteHost = { hostId: "a", label: "Box A", url: "https://h-4f2a.useao.dev", status: "connected" };
const boxB: RemoteHost = { hostId: "b", label: "Box B", url: "http://192.168.1.9:3001", status: "connected" };

function renderView(hosts: RemoteHost[], refreshRemoteHosts = vi.fn(async () => undefined)) {
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<ShellProvider value={{ remoteHosts: hosts, refreshRemoteHosts } as never}>
				<TooltipProvider>
					<MachinesView />
				</TooltipProvider>
			</ShellProvider>
		</QueryClientProvider>,
	);
	return { refreshRemoteHosts };
}

const rowFor = (hostId: string) => document.querySelector<HTMLElement>(`[data-host-id="${hostId}"]`) as HTMLElement;

describe("MachinesView", () => {
	beforeEach(() => {
		window.localStorage.clear();
		useMachineAccountsStore.setState({ byHost: {} });
	});
	afterEach(() => vi.unstubAllGlobals());

	it("lists this computer and each linked one with state, route and version, and warns about one that is behind", async () => {
		stubHealth({
			"http://local.test/healthz": probe("0.44.0"),
			"http://host.test/a/healthz": probe("0.43.2"),
		});
		renderView([boxA, { ...boxB, status: "offline" }]);

		expect(await screen.findByText("Ayo 0.43.2")).toBeVisible();
		expect(screen.getByText("Ayo 0.44.0")).toBeVisible();
		expect(screen.getAllByTestId("machine-row")).toHaveLength(3);
		const a = within(rowFor("a"));
		expect(a.getByText("Online")).toBeVisible();
		expect(a.getByText("Through a tunnel")).toBeVisible();
		expect(rowFor("a")).toHaveTextContent("behind this computer");
		const b = within(rowFor("b"));
		expect(b.getByText("Not reachable")).toBeVisible();
		expect(b.getByText("Local network")).toBeVisible();
		// A computer that does not answer has no version to show or compare.
		expect(rowFor("b")).not.toHaveTextContent("Ayo ");
		expect(screen.getByTestId("machines-version-warning")).toHaveTextContent(
			"Box A runs Ayo 0.43.2, and this computer runs 0.44.0. Update Ayo on Box A, then check again.",
		);
	});

	it("warns the other way when a linked computer is newer than this one", async () => {
		stubHealth({ "http://local.test/healthz": probe("0.44.0"), "http://host.test/a/healthz": probe("0.45.1") });
		renderView([boxA]);
		expect(await screen.findByTestId("machines-version-warning")).toHaveTextContent(
			"Box A runs Ayo 0.45.1, which is newer than the 0.44.0 on this computer. Update Ayo here",
		);
		expect(rowFor("a")).toHaveTextContent("newer than this computer");
	});

	it("does not warn when versions match, or when a computer reports no version", async () => {
		stubHealth({
			"http://local.test/healthz": probe("0.44.0"),
			"http://host.test/a/healthz": probe("0.44.0"),
			"http://host.test/b/healthz": probe(),
		});
		renderView([boxA, boxB]);
		expect(await within(rowFor("b")).findByText("Version not reported")).toBeVisible();
		expect(rowFor("a")).toHaveTextContent("same as this computer");
		expect(screen.queryByTestId("machines-version-warning")).toBeNull();
	});

	it("names why a refused or incompatible computer is not usable", async () => {
		stubHealth({ "http://local.test/healthz": probe("0.44.0") });
		renderView([
			{ hostId: "c", label: "Box C", url: "https://c.test", status: "offline", failureReason: "unauthorized" },
			{ hostId: "d", label: "Box D", url: "https://d.test", status: "offline", failureReason: "incompatible" },
			{ hostId: "e", label: "Box E", url: "https://e.test", status: "connecting" },
		]);
		expect(await within(rowFor("c")).findByText("Password rejected")).toBeVisible();
		expect(within(rowFor("d")).getByText("Ayo versions are incompatible")).toBeVisible();
		expect(within(rowFor("e")).getByText("Connecting…")).toBeVisible();
	});

	it("remembers which Claude account a computer uses, and can take the answer back", async () => {
		stubHealth({ "http://local.test/healthz": probe("0.44.0"), "http://host.test/a/healthz": probe("0.44.0") });
		renderView([boxA]);
		const trigger = await screen.findByRole("button", { name: "Claude account used on Box A" });
		expect(trigger).toHaveTextContent("Claude account not set");

		await userEvent.click(trigger);
		await userEvent.click(await screen.findByRole("menuitem", { name: "Different Claude account" }));
		expect(useMachineAccountsStore.getState().byHost).toEqual({ a: "different" });
		expect(screen.getByRole("button", { name: "Claude account used on Box A" })).toHaveTextContent("Different Claude account");

		await userEvent.click(screen.getByRole("button", { name: "Claude account used on Box A" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Claude account not set" }));
		expect(useMachineAccountsStore.getState().byHost).toEqual({});
	});

	it("shows the saved Claude account choice when the page opens", async () => {
		useMachineAccountsStore.setState({ byHost: { a: "same" } });
		stubHealth({ "http://local.test/healthz": probe("0.44.0"), "http://host.test/a/healthz": probe("0.44.0") });
		renderView([boxA]);
		expect(await screen.findByRole("button", { name: "Claude account used on Box A" })).toHaveTextContent("Same Claude account");
	});

	it("checks again on request", async () => {
		stubHealth({ "http://local.test/healthz": probe("0.44.0") });
		const { refreshRemoteHosts } = renderView([]);
		const button = await screen.findByRole("button", { name: "Check again" });
		// It is disabled while a check is already running.
		await waitFor(() => expect(button).toBeEnabled());
		fireEvent.click(button);
		expect(refreshRemoteHosts).toHaveBeenCalledOnce();
	});

	it("points to remote host settings when no other computer is linked", async () => {
		stubHealth({ "http://local.test/healthz": probe("0.44.0") });
		const openGlobalSettings = vi.fn();
		const original = useUiStore.getState().openGlobalSettings;
		useUiStore.setState({ openGlobalSettings });
		try {
			renderView([]);
			expect(await screen.findByText("No other computers are linked yet.")).toBeVisible();
			fireEvent.click(screen.getByRole("button", { name: "Link a computer" }));
			expect(openGlobalSettings).toHaveBeenCalledWith("remoteHosts");
		} finally {
			useUiStore.setState({ openGlobalSettings: original });
		}
	});
});
