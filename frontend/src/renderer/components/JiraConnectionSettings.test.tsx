import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "../i18n";

type Reply = { data?: unknown; error?: unknown; response: { status: number } };
type Call = { method: string; path: string; init?: { body?: Record<string, unknown> } };

const { daemon } = vi.hoisted(() => ({
	daemon: { calls: [] as Call[], replies: {} as Record<string, () => Reply> },
}));

vi.mock("../lib/host-clients", () => {
	const send = (method: string) => async (path: string, init?: Call["init"]) => {
		daemon.calls.push({ method, path, init });
		const reply = daemon.replies[`${method} ${path}`];
		if (!reply) throw new Error(`unexpected ${method} ${path}`);
		return reply();
	};
	return { clientForSessionHost: () => ({ GET: send("GET"), POST: send("POST"), PUT: send("PUT"), DELETE: send("DELETE") }) };
});

import { useUiStore } from "../stores/ui-store";
import { JiraConnectionSettings } from "./JiraConnectionSettings";

const ok = (data: unknown): Reply => ({ data, response: { status: 200 } });
const fail = (code: string, message: string, status: number): Reply => ({ error: { code, message }, response: { status } });

function renderSettings() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<JiraConnectionSettings projectId="yoobi-app" />
		</QueryClientProvider>,
	);
}

async function fill(label: string, value: string) {
	await userEvent.type(screen.getByLabelText(label), value);
}

beforeEach(() => {
	daemon.calls.length = 0;
	daemon.replies = { "GET /api/v1/projects/{id}/jira": () => ok({ connected: false }) };
	useUiStore.getState().clearGlobalToast();
});
afterEach(cleanup);

describe("JiraConnectionSettings", () => {
	it("keeps Test and Connect off until the site, email, token and a project key or JQL are entered", async () => {
		renderSettings();
		await screen.findByText("Not connected");
		const connect = screen.getByRole("button", { name: "Connect" });
		expect(connect).toBeDisabled();
		expect(screen.getByRole("button", { name: "Test connection" })).toBeDisabled();

		await fill("Jira site", "yoobi.atlassian.net");
		await fill("Atlassian email", "ada@example.com");
		await fill("API token", "secret-token");
		expect(connect).toBeDisabled();
		await fill("Project key", "YIA");
		expect(connect).toBeEnabled();
	});

	it("tests the connection without saving it and shows what Jira found", async () => {
		daemon.replies["POST /api/v1/projects/{id}/jira/test"] = () => ok({ accountName: "Ada Example", projectName: "Yoobi", jql: "project = YIA", ticketCount: 14 });
		renderSettings();
		await screen.findByText("Not connected");
		await fill("Jira site", "yoobi.atlassian.net");
		await fill("Atlassian email", "ada@example.com");
		await fill("API token", "secret-token");
		await fill("Project key", "YIA");

		await userEvent.click(screen.getByRole("button", { name: "Test connection" }));

		expect(await screen.findByTestId("jira-test-result")).toHaveTextContent("Connected as Ada Example. Project Yoobi, tickets found: 14.");
		expect(daemon.calls.find((call) => call.method === "POST")?.init?.body).toEqual({
			siteUrl: "yoobi.atlassian.net", email: "ada@example.com", apiToken: "secret-token", projectKey: "YIA",
		});
		expect(daemon.calls.some((call) => call.method === "PUT")).toBe(false);
	});

	it("saves the connection, clears the token field and never shows the token again", async () => {
		daemon.replies["PUT /api/v1/projects/{id}/jira"] = () => {
			daemon.replies["GET /api/v1/projects/{id}/jira"] = () => ok({ connected: true, siteUrl: "https://yoobi.atlassian.net", email: "ada@example.com", projectKey: "YIA", accountName: "Ada Example" });
			return ok({ connected: true });
		};
		renderSettings();
		await screen.findByText("Not connected");
		await fill("Jira site", "yoobi.atlassian.net");
		await fill("Atlassian email", "ada@example.com");
		await fill("API token", "secret-token");
		await fill("Project key", "yia");

		await userEvent.click(screen.getByRole("button", { name: "Connect" }));

		await waitFor(() => expect(screen.getByTestId("jira-connection-status")).toHaveTextContent("Connected as Ada Example"));
		expect(daemon.calls.find((call) => call.method === "PUT")?.init?.body).toMatchObject({ apiToken: "secret-token", projectKey: "yia" });
		expect(screen.getByLabelText("API token")).toHaveValue("");
		expect(screen.getByLabelText("API token")).toHaveAttribute("placeholder", "Saved. Leave empty to keep it.");
		expect(document.body.textContent).not.toContain("secret-token");
		expect(useUiStore.getState().globalToast).toMatchObject({ title: "Jira connected", tone: "success" });
	});

	it("fills the form from the saved connection, and updates it without re-entering the token", async () => {
		daemon.replies["GET /api/v1/projects/{id}/jira"] = () => ok({ connected: true, siteUrl: "https://yoobi.atlassian.net", email: "ada@example.com", projectKey: "YIA", boardId: 7, jql: "project = YIA", accountName: "Ada Example" });
		daemon.replies["PUT /api/v1/projects/{id}/jira"] = () => ok({ connected: true });
		renderSettings();

		await waitFor(() => expect(screen.getByLabelText("Jira site")).toHaveValue("https://yoobi.atlassian.net"));
		expect(screen.getByLabelText("Atlassian email")).toHaveValue("ada@example.com");
		expect(screen.getByLabelText("Board ID")).toHaveValue("7");
		expect(screen.getByLabelText("JQL")).toHaveValue("project = YIA");
		expect(screen.getByLabelText("API token")).toHaveValue("");

		await userEvent.clear(screen.getByLabelText("JQL"));
		await userEvent.type(screen.getByLabelText("JQL"), "project = YIA AND assignee = currentUser()");
		await userEvent.click(screen.getByRole("button", { name: "Save" }));

		await waitFor(() => expect(daemon.calls.some((call) => call.method === "PUT")).toBe(true));
		const body = daemon.calls.find((call) => call.method === "PUT")?.init?.body;
		expect(body).toMatchObject({ siteUrl: "https://yoobi.atlassian.net", boardId: 7, jql: "project = YIA AND assignee = currentUser()" });
		expect(body).not.toHaveProperty("apiToken");
	});

	it("shows Jira's refusal next to the buttons", async () => {
		daemon.replies["POST /api/v1/projects/{id}/jira/test"] = () => fail("JIRA_AUTH_FAILED", "Jira rejected the email or API token. Check both and try again.", 422);
		renderSettings();
		await screen.findByText("Not connected");
		await fill("Jira site", "yoobi.atlassian.net");
		await fill("Atlassian email", "ada@example.com");
		await fill("API token", "wrong");
		await fill("Project key", "YIA");

		await userEvent.click(screen.getByRole("button", { name: "Test connection" }));

		expect(await screen.findByTestId("jira-settings-error")).toHaveTextContent("Jira rejected the email or API token. Check both and try again.");
	});

	it("asks before disconnecting, then removes the connection", async () => {
		daemon.replies["GET /api/v1/projects/{id}/jira"] = () => ok({ connected: true, siteUrl: "https://yoobi.atlassian.net", email: "ada@example.com", projectKey: "YIA" });
		daemon.replies["DELETE /api/v1/projects/{id}/jira"] = () => {
			daemon.replies["GET /api/v1/projects/{id}/jira"] = () => ok({ connected: false });
			return ok({ status: "ok" });
		};
		renderSettings();
		await waitFor(() => expect(screen.getByLabelText("Jira site")).toHaveValue("https://yoobi.atlassian.net"));

		await userEvent.click(screen.getByRole("button", { name: "Disconnect" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Disconnect Jira?")).toBeInTheDocument();
		expect(daemon.calls.some((call) => call.method === "DELETE")).toBe(false);

		await userEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
		await waitFor(() => expect(daemon.calls.some((call) => call.method === "DELETE")).toBe(true));
		await waitFor(() => expect(screen.getByLabelText("Jira site")).toHaveValue(""));
	});
});
