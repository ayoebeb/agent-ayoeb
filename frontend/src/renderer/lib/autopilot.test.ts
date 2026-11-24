import { beforeEach, describe, expect, it, vi } from "vitest";
import { LOCAL_HOST } from "./hosts";

const { localGet, remoteGet } = vi.hoisted(() => ({ localGet: vi.fn(), remoteGet: vi.fn() }));

vi.mock("./api-client", () => ({ apiClient: { GET: localGet } }));
vi.mock("./host-clients", () => ({ clientForHost: () => ({ GET: remoteGet }) }));

import { appI18n } from "../i18n";
import { deMessages } from "../i18n/all-messages";
import {
	AUTOPILOT_PARKED_TITLE_SUFFIX,
	autopilotTone,
	fetchAutopilotRuns,
	fetchAutopilotView,
	hasUnseenReport,
	isOpenAutopilotRun,
	markReportSeen,
	refreshAutopilot,
	runForProject,
	runForSession,
	type AutopilotRun,
	type AutopilotRunOnHost,
	type AutopilotView,
} from "./autopilot";
import { localizedNotificationCopy } from "./orchestrator-waiting";

const run = (overrides: Partial<AutopilotRun> = {}): AutopilotRun => ({
	id: "ap_1",
	sessionId: "mer-orchestrator-1",
	projectId: "mer",
	task: "Ship it",
	criteria: "",
	state: "running",
	iteration: 2,
	maxIterations: 30,
	stagnantIterations: 0,
	lastGap: "",
	evidence: "",
	ciGap: "",
	permissionsAllowed: 0,
	permissionsDenied: 0,
	startedAt: "2026-11-17T09:00:00Z",
	updatedAt: "2026-11-17T09:00:00Z",
	openParkedItemCount: 0,
	...overrides,
});
const onHost = (overrides: Partial<AutopilotRun> = {}, hostId = LOCAL_HOST): AutopilotRunOnHost => ({ ...run(overrides), hostId });

beforeEach(() => {
	localGet.mockReset();
	remoteGet.mockReset();
	window.localStorage.clear();
});

describe("fetchAutopilotRuns", () => {
	it("reads this computer's runs and stamps them with the host", async () => {
		localGet.mockResolvedValue({ data: { runs: [run()] }, response: { status: 200 } });
		await expect(fetchAutopilotRuns(LOCAL_HOST)).resolves.toEqual([onHost()]);
		expect(localGet).toHaveBeenCalledWith("/api/v1/autopilot");
	});

	it("reads a remote host through that host's client", async () => {
		remoteGet.mockResolvedValue({ data: { runs: [run({ projectId: "venus" })] }, response: { status: 200 } });
		await expect(fetchAutopilotRuns("host-b")).resolves.toEqual([onHost({ projectId: "venus" }, "host-b")]);
		expect(localGet).not.toHaveBeenCalled();
	});

	// A daemon from before Autopilot has no such route: nothing is running, which
	// is not a failure the user can act on.
	it.each([404, 501])("treats a daemon without the route (%i) as no runs", async (status) => {
		remoteGet.mockResolvedValue({ error: { code: "NOT_FOUND" }, response: { status } });
		await expect(fetchAutopilotRuns("host-b")).resolves.toEqual([]);
	});

	it("surfaces a real failure", async () => {
		localGet.mockResolvedValue({ error: { message: "boom" }, response: { status: 500 } });
		await expect(fetchAutopilotRuns(LOCAL_HOST)).rejects.toBeTruthy();
	});
});

describe("fetchAutopilotView", () => {
	it("reads one session's run, log and report", async () => {
		const view: AutopilotView = { run: run(), events: [], parked: [], warnings: [] };
		localGet.mockResolvedValue({ data: view, response: { status: 200 } });
		await expect(fetchAutopilotView(LOCAL_HOST, "mer-orchestrator-1")).resolves.toEqual(view);
		expect(localGet).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/autopilot", {
			params: { path: { sessionId: "mer-orchestrator-1" } },
		});
	});

	it.each([404, 501])("reads a session or daemon the route does not cover (%i) as an empty view", async (status) => {
		localGet.mockResolvedValue({ error: { code: "NOT_FOUND" }, response: { status } });
		await expect(fetchAutopilotView(LOCAL_HOST, "x")).resolves.toEqual({ events: [], parked: [], warnings: [] });
	});

	it("surfaces a real failure", async () => {
		localGet.mockResolvedValue({ error: { message: "boom" }, response: { status: 500 } });
		await expect(fetchAutopilotView(LOCAL_HOST, "x")).rejects.toBeTruthy();
	});
});

describe("states", () => {
	it("calls a run open while it works, waits on the limit, or waits for the user", () => {
		expect(isOpenAutopilotRun(run({ state: "running" }))).toBe(true);
		expect(isOpenAutopilotRun(run({ state: "paused_usage_limit" }))).toBe(true);
		expect(isOpenAutopilotRun(run({ state: "parked" }))).toBe(true);
		expect(isOpenAutopilotRun(run({ state: "done" }))).toBe(false);
		expect(isOpenAutopilotRun(run({ state: "stopped" }))).toBe(false);
		expect(isOpenAutopilotRun(undefined)).toBe(false);
	});

	it("gives every state a tone the status tokens can colour", () => {
		expect(autopilotTone("running")).toBe("working");
		expect(autopilotTone("parked")).toBe("attention");
		expect(autopilotTone("paused_usage_limit")).toBe("paused");
		expect(autopilotTone("done")).toBe("done");
		expect(autopilotTone("stopped")).toBe("stopped");
	});
});

describe("lookups", () => {
	const runs = [
		onHost({ id: "a", sessionId: "s1", projectId: "mer", state: "running" }),
		onHost({ id: "b", sessionId: "s2", projectId: "mer", state: "parked" }),
		onHost({ id: "c", sessionId: "s1", projectId: "mer", state: "running" }, "host-b"),
	];

	it("finds a session's run by host, so the same session id on two hosts stays apart", () => {
		expect(runForSession(runs, LOCAL_HOST, "s1")?.id).toBe("a");
		expect(runForSession(runs, "host-b", "s1")?.id).toBe("c");
		expect(runForSession(runs, LOCAL_HOST, "nope")).toBeUndefined();
	});

	it("shows a project the run that needs the user before one that merely works", () => {
		expect(runForProject(runs, LOCAL_HOST, "mer")?.id).toBe("b");
		expect(runForProject(runs, "host-b", "mer")?.id).toBe("c");
		expect(runForProject(runs, LOCAL_HOST, "venus")).toBeUndefined();
	});
});

// A run that ended while the user was elsewhere leaves its return report waiting.
// Opening it is the user's acknowledgement, held in this app only.
describe("return report acknowledgement", () => {
	const finished: Pick<AutopilotView, "run"> = { run: run({ id: "ap_9", state: "done" }) };

	it("is unseen for a finished run until the user opens it", () => {
		expect(hasUnseenReport(LOCAL_HOST, finished)).toBe(true);
		markReportSeen(LOCAL_HOST, finished);
		expect(hasUnseenReport(LOCAL_HOST, finished)).toBe(false);
	});

	it("is unseen again for the next run on the same session", () => {
		markReportSeen(LOCAL_HOST, finished);
		expect(hasUnseenReport(LOCAL_HOST, { run: run({ id: "ap_10", state: "stopped" }) })).toBe(true);
	});

	it("does not mix up the same session on two hosts", () => {
		markReportSeen(LOCAL_HOST, finished);
		expect(hasUnseenReport("host-b", finished)).toBe(true);
	});

	it("has no report to see while a run is open or when there never was one", () => {
		expect(hasUnseenReport(LOCAL_HOST, { run: run({ state: "running" }) })).toBe(false);
		expect(hasUnseenReport(LOCAL_HOST, { run: run({ state: "parked" }) })).toBe(false);
		expect(hasUnseenReport(LOCAL_HOST, {})).toBe(false);
		expect(hasUnseenReport(LOCAL_HOST, undefined)).toBe(false);
	});

	it("never marks an open run as seen", () => {
		markReportSeen(LOCAL_HOST, { run: run({ state: "running" }) });
		expect(window.localStorage.getItem("ao.autopilot.reportSeen")).toBeNull();
	});

	it("survives a corrupt store", () => {
		window.localStorage.setItem("ao.autopilot.reportSeen", "{not json");
		expect(hasUnseenReport(LOCAL_HOST, finished)).toBe(true);
		markReportSeen(LOCAL_HOST, finished);
		expect(hasUnseenReport(LOCAL_HOST, finished)).toBe(false);
	});
});

describe("refreshAutopilot", () => {
	it("invalidates every Autopilot read, the markers and each open session view", async () => {
		const invalidateQueries = vi.fn().mockResolvedValue(undefined);
		await refreshAutopilot({ invalidateQueries } as never);
		expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ["autopilot"] });
	});
});

// The daemon writes English ("<session>: Autopilot parked an item for you"); the
// desktop shows the title in the user's own language and leaves the item alone.
describe("the parked-item notification copy", () => {
	const parked = (overrides: Partial<{ title: string; body: string }> = {}) => ({
		type: "autopilot_parked",
		title: `Orchestrator${AUTOPILOT_PARKED_TITLE_SUFFIX}`,
		body: "Upgrade the CI plan: money: the free minutes ran out",
		...overrides,
	});

	it("matches the daemon's pinned English", () => {
		expect(AUTOPILOT_PARKED_TITLE_SUFFIX).toBe(": Autopilot parked an item for you");
	});

	it("keeps the session name and never touches what the agent wrote", () => {
		expect(localizedNotificationCopy(parked())).toEqual({
			title: "Orchestrator: Autopilot parked an item for you",
			body: "Upgrade the CI plan: money: the free minutes ran out",
		});
	});

	it("shows a title it does not recognise exactly as the daemon wrote it", () => {
		expect(localizedNotificationCopy(parked({ title: "Something else entirely" })).title).toBe("Something else entirely");
	});

	it("follows the user's language, not the daemon's", async () => {
		appI18n.addResourceBundle("de", "translation", deMessages, true, true);
		await appI18n.changeLanguage("de");
		try {
			expect(localizedNotificationCopy(parked()).title).toBe("Orchestrator: Autopilot hat etwas für dich zurückgestellt");
		} finally {
			await appI18n.changeLanguage("en");
		}
	});
});
