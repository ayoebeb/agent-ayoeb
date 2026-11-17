import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ProjectPermissionsSettings } from "./ProjectPermissionsSettings";

const { getMock, deleteMock } = vi.hoisted(() => ({ getMock: vi.fn(), deleteMock: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: getMock, DELETE: deleteMock }, apiErrorMessage: () => "request failed" }));

const rules = [
	{ id: "r1", kind: "command", label: "node -e ...", tool: "Bash", sourceSessionId: "mer-3", createdAt: "2026-11-17T09:00:00Z" },
	{ id: "r2", kind: "server", label: "atlassian", tool: "mcp__atlassian", sourceSessionId: "mer-5", createdAt: "2026-11-16T09:00:00Z" },
];

function renderList() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	render(<QueryClientProvider client={client}><ProjectPermissionsSettings projectId="mer" /></QueryClientProvider>);
}

beforeEach(() => {
	getMock.mockReset().mockResolvedValue({ data: { rules } });
	deleteMock.mockReset().mockResolvedValue({ data: undefined });
});

it("lists what was remembered with its kind, date and source session", async () => {
	renderList();
	const first = (await screen.findByText("node -e ...")).closest(".settings-row-bar") as HTMLElement;
	expect(getMock).toHaveBeenCalledWith("/api/v1/projects/{id}/permission-rules", { params: { path: { id: "mer" } } });
	expect(within(first).getByText(/Command kind/)).toHaveTextContent("mer-3");
	const second = screen.getByText("atlassian").closest(".settings-row-bar") as HTMLElement;
	expect(within(second).getByText(/All tools of a server/)).toHaveTextContent("mer-5");
	// A command is shown as code, an MCP server as plain text.
	expect(screen.getByText("node -e ...")).toHaveClass("font-mono");
	expect(screen.getByText("atlassian")).not.toHaveClass("font-mono");
});

it("revokes one rule and refreshes the list", async () => {
	renderList();
	await screen.findByText("node -e ...");
	getMock.mockResolvedValue({ data: { rules: [rules[1]] } });

	await userEvent.click(screen.getByRole("button", { name: "Revoke node -e ..." }));

	await waitFor(() => expect(deleteMock).toHaveBeenCalledOnce());
	expect(deleteMock).toHaveBeenCalledWith("/api/v1/projects/{id}/permission-rules/{ruleId}", { params: { path: { id: "mer", ruleId: "r1" } } });
	await waitFor(() => expect(screen.queryByText("node -e ...")).not.toBeInTheDocument());
	expect(screen.getByText("atlassian")).toBeInTheDocument();
});

it("says so, close to the action, when a rule could not be revoked", async () => {
	deleteMock.mockResolvedValue({ error: { error: { message: "The project has no such remembered permission rule" } } });
	renderList();
	await userEvent.click(await screen.findByRole("button", { name: "Revoke atlassian" }));
	expect(await screen.findByRole("alert")).toBeInTheDocument();
});

it("shows one quiet line when nothing is remembered", async () => {
	getMock.mockResolvedValue({ data: { rules: [] } });
	renderList();
	expect(await screen.findByText("Nothing is remembered for this project yet")).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: /Revoke/ })).not.toBeInTheDocument();
});
