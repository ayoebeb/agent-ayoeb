import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn() }));
vi.mock("expo/fetch", () => ({ fetch: vi.fn() }));

import { ApiError } from "./api";
import type { ServerConfig } from "./config";
import { UnreachableError } from "./connectionError";
import { getJiraBoard, jiraFailure, jiraSections, startJiraTicket, type JiraBoard, type JiraTicket } from "./jira";

const cfg: ServerConfig = { host: "ao.test", httpPort: "3011", muxPort: "3011", secure: false, password: "secret12" };

function ticket(key: string, overrides: Partial<JiraTicket> = {}): JiraTicket {
	return { key, summary: `Summary of ${key}`, url: `https://yoobi.atlassian.net/browse/${key}`, status: { id: "1", name: "To Do", category: "new" }, ...overrides };
}

describe("Jira requests", () => {
	beforeEach(() => {
		vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ columns: [], issueKey: "YIA-10", orchestratorId: "orch-1" }), { status: 200 })));
	});
	afterEach(() => vi.unstubAllGlobals());

	it("reads a project's board from the paired machine, authenticated with the pairing password", async () => {
		await getJiraBoard(cfg, "yoobi-app");
		const [url, init] = vi.mocked(fetch).mock.calls[0];
		expect(url).toBe("http://ao.test:3011/api/v1/projects/yoobi-app/jira/board");
		expect(JSON.stringify(init?.headers)).toContain("secret12");
		expect(JSON.stringify(init?.headers)).not.toContain("jira");
	});

	it("starts a ticket by key alone: the machine reads the ticket from Jira itself", async () => {
		await expect(startJiraTicket(cfg, "yoobi-app", "YIA-10")).resolves.toMatchObject({ orchestratorId: "orch-1" });
		const [url, init] = vi.mocked(fetch).mock.calls[0];
		expect(url).toBe("http://ao.test:3011/api/v1/projects/yoobi-app/jira/tickets/YIA-10/start");
		expect(init?.method).toBe("POST");
		expect(init?.body).toBeUndefined();
	});

	it("escapes a project id that has a path separator in it", async () => {
		await getJiraBoard(cfg, "team/app");
		expect(vi.mocked(fetch).mock.calls[0][0]).toBe("http://ao.test:3011/api/v1/projects/team%2Fapp/jira/board");
	});
});

describe("jiraFailure", () => {
	it("maps the daemon's Jira codes to what the page says", () => {
		expect(jiraFailure(new ApiError(409, "409", "JIRA_NOT_CONNECTED"))).toBe("not_connected");
		expect(jiraFailure(new ApiError(422, "422", "JIRA_AUTH_FAILED"))).toBe("auth");
		expect(jiraFailure(new ApiError(503, "503", "JIRA_UNREACHABLE"))).toBe("offline");
		expect(jiraFailure(new ApiError(400, "400", "JIRA_BAD_QUERY"))).toBe("bad_query");
		expect(jiraFailure(new ApiError(502, "502", "JIRA_UPSTREAM_ERROR"))).toBe("other");
		expect(jiraFailure(new UnreachableError("offline"))).toBe("offline");
		expect(jiraFailure(new Error("boom"))).toBe("other");
	});
});

describe("jiraSections", () => {
	const board: JiraBoard = {
		siteUrl: "https://yoobi.atlassian.net", total: 3, fetchedAt: "2026-11-16T12:00:00Z", accountId: "acc-1",
		columns: [
			{ name: "To Do", tickets: [ticket("YIA-1", { assignee: { accountId: "acc-1", displayName: "Ada" } }), ticket("YIA-2", { assignee: { accountId: "acc-2", displayName: "Bo" } })] },
			{ name: "In Progress", tickets: [] },
			{ name: "Parked", other: true, tickets: [ticket("YIA-3")] },
		],
	};

	it("lists the columns that have tickets, in board order, naming the catch-all column", () => {
		expect(jiraSections(board, false).map((s) => [s.title, s.data.map((t) => t.key)])).toEqual([["To Do", ["YIA-1", "YIA-2"]], ["Other", ["YIA-3"]]]);
	});

	it("narrows to the connected account's tickets and drops columns left empty", () => {
		expect(jiraSections(board, true).map((s) => [s.title, s.data.map((t) => t.key)])).toEqual([["To Do", ["YIA-1"]]]);
		expect(jiraSections({ ...board, accountId: undefined }, true).flatMap((s) => s.data)).toHaveLength(3);
	});
});
