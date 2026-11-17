import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AutopilotEvent, AutopilotRun, AutopilotView } from "../lib/autopilot";

const { viewMock, startMock, stopMock, resumeMock, resolveMock, settingsMock } = vi.hoisted(() => ({
	viewMock: vi.fn(),
	startMock: vi.fn(),
	stopMock: vi.fn(),
	resumeMock: vi.fn(),
	resolveMock: vi.fn(),
	settingsMock: vi.fn(),
}));

const idle = (mutate: ReturnType<typeof vi.fn>) => ({ mutate, isPending: false, error: null });
vi.mock("../hooks/useAutopilot", () => ({
	useAutopilotView: () => ({ data: viewMock() }),
	useAutopilotActions: () => ({
		start: idle(startMock),
		stop: idle(stopMock),
		resume: idle(resumeMock),
		resolveItem: idle(resolveMock),
		error: undefined,
	}),
}));
vi.mock("../hooks/useSettings", () => ({
	useSettings: () => ({ settings: settingsMock(), isLoading: false, error: undefined }),
}));

import { AutopilotTopbarControl } from "./AutopilotTopbarControl";
import { TooltipProvider } from "./ui/tooltip";

const run = (overrides: Partial<AutopilotRun> = {}): AutopilotRun => ({
	id: "ap_1",
	sessionId: "mer-orchestrator-1",
	projectId: "mer",
	task: "Make the importer twice as fast",
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

const event = (overrides: Partial<AutopilotEvent> = {}): AutopilotEvent => ({
	id: "ape_1",
	runId: "ap_1",
	sessionId: "mer-orchestrator-1",
	kind: "decision",
	iteration: 1,
	summary: "Patch or refactor? → Patch the one function",
	detail: "The task is one bug",
	createdAt: "2026-11-17T09:10:00Z",
	...overrides,
});

const view = (overrides: Partial<AutopilotView> = {}): AutopilotView => ({ events: [], parked: [], warnings: [], ...overrides });

function renderControl(props: { sessionTerminated?: boolean } = {}) {
	return render(
		<TooltipProvider>
			<AutopilotTopbarControl sessionId="mer-orchestrator-1" {...props} />
		</TooltipProvider>,
	);
}

beforeEach(() => {
	for (const mock of [viewMock, startMock, stopMock, resumeMock, resolveMock, settingsMock]) mock.mockReset();
	viewMock.mockReturnValue(view());
	settingsMock.mockReturnValue({ autopilotMaxIterations: 30 });
	window.localStorage.clear();
});

describe("with no run", () => {
	it("is a quiet Autopilot button with no Stop beside it", () => {
		renderControl();

		expect(screen.getByRole("button", { name: "Autopilot" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Stop Autopilot" })).not.toBeInTheDocument();
	});

	it("starts a run with the task the user wrote", async () => {
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot" }));

		const start = await screen.findByRole("button", { name: "Start Autopilot" });
		expect(start).toBeDisabled();
		await userEvent.type(screen.getByRole("textbox", { name: "Task" }), "  Make the importer twice as fast  ");
		expect(start).toBeEnabled();
		await userEvent.click(start);

		expect(startMock).toHaveBeenCalledTimes(1);
		expect(startMock.mock.calls[0][0]).toEqual({ task: "Make the importer twice as fast", maxIterations: undefined });
	});

	it("sends the cap the user typed, and refuses one the daemon would", async () => {
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot" }));
		await userEvent.type(await screen.findByRole("textbox", { name: "Task" }), "Ship it");

		const max = screen.getByRole("spinbutton", { name: "Most iterations" });
		expect(max).toHaveAttribute("placeholder", "30");
		await userEvent.type(max, "0");
		expect(screen.getByRole("button", { name: "Start Autopilot" })).toBeDisabled();
		await userEvent.clear(max);
		await userEvent.type(max, "12");
		await userEvent.click(screen.getByRole("button", { name: "Start Autopilot" }));

		expect(startMock.mock.calls[0][0]).toEqual({ task: "Ship it", maxIterations: 12 });
	});

	it("states the hard limits before the user starts a run", async () => {
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot" }));

		const text = (await screen.findByRole("dialog")).textContent ?? "";
		for (const limit of ["spends money", "weakens CI", "pushes to main without review", "uses admin rights", "changes credentials"]) {
			expect(text).toContain(limit);
		}
	});

	it("offers nothing for a session that ended and never had a run", () => {
		renderControl({ sessionTerminated: true });
		expect(screen.queryByTestId("autopilot-control")).not.toBeInTheDocument();
	});
});

describe("with a run open", () => {
	it("shows the state on the badge and keeps Stop in the bar, never in a menu", async () => {
		viewMock.mockReturnValue(view({ run: run() }));
		renderControl();

		const badge = screen.getByRole("button", { name: "Autopilot on · 3 of 30" });
		expect(badge).toHaveAttribute("data-autopilot-state", "running");
		const stop = screen.getByRole("button", { name: "Stop Autopilot" });
		expect(stop).toBeVisible();
		await userEvent.click(stop);

		expect(stopMock).toHaveBeenCalledTimes(1);
	});

	it.each([
		["paused_usage_limit", "Autopilot paused"],
		["parked", "Autopilot parked · needs you"],
	] as const)("names a %s run in words, so colour is never the only signal", (state, label) => {
		viewMock.mockReturnValue(view({ run: run({ state }) }));
		renderControl();

		expect(screen.getByRole("button", { name: label })).toHaveAttribute("data-autopilot-state", state);
		expect(screen.getByRole("button", { name: "Stop Autopilot" })).toBeInTheDocument();
	});

	it("shows the task, the criteria, what is still open and the log", async () => {
		viewMock.mockReturnValue(view({
			run: run({ criteria: "Done when go test passes", lastGap: "PR #7 has no completed AO review", permissionsAllowed: 12, permissionsDenied: 1 }),
			events: [event({ id: "ape_2", kind: "iteration", summary: "Iteration 3 of 30: the task was sent again" }), event({ id: "ape_0", kind: "started", summary: "Autopilot started" })],
		}));
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot on · 3 of 30" }));

		const body = await screen.findByTestId("autopilot-dialog-body");
		expect(within(body).getByTestId("autopilot-state")).toHaveTextContent("Running: iteration 3 of 30");
		expect(body).toHaveTextContent("Make the importer twice as fast");
		expect(body).toHaveTextContent("Done when go test passes");
		expect(body).toHaveTextContent("PR #7 has no completed AO review");
		expect(body).toHaveTextContent("12 allowed, 1 denied");
		const log = within(body).getByTestId("autopilot-log");
		expect(within(log).getAllByRole("listitem")).toHaveLength(2);
		expect(log).toHaveTextContent("Iteration");
		expect(log).toHaveTextContent("Autopilot started");
		// While a run is open the dialog offers no second run.
		expect(screen.queryByRole("button", { name: "Start Autopilot" })).not.toBeInTheDocument();
	});

	it("says criteria are not written yet rather than showing a blank", async () => {
		viewMock.mockReturnValue(view({ run: run() }));
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot on · 3 of 30" }));

		expect(await screen.findByTestId("autopilot-dialog-body")).toHaveTextContent("Not written yet. The agent writes it in its first turn.");
	});

	it("shows a gap Autopilot cannot close, such as a terminal prompt", async () => {
		viewMock.mockReturnValue(view({ run: run(), warnings: ["This is a terminal session: a permission prompt inside it cannot be answered."] }));
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot on · 3 of 30" }));

		expect(await screen.findByRole("note")).toHaveTextContent("This is a terminal session");
	});

	it("stops the run from inside the dialog too", async () => {
		viewMock.mockReturnValue(view({ run: run() }));
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot on · 3 of 30" }));

		const dialog = await screen.findByRole("dialog");
		await userEvent.click(within(dialog).getByRole("button", { name: "Stop" }));
		expect(stopMock).toHaveBeenCalledTimes(1);
	});
});

describe("a parked run", () => {
	const parkedView = () => view({
		run: run({ state: "parked", endReason: "stuck", openParkedItemCount: 1 }),
		parked: [event({ id: "ape_7", kind: "parked", summary: "Upgrade the CI plan", detail: "money: the free minutes ran out" })],
		report: {
			runId: "ap_1", task: "Make the importer twice as fast", state: "parked", endReason: "stuck", iterations: 4,
			startedAt: "2026-11-17T09:00:00Z", mergedPrs: [], gaps: ["The run changed nothing for 3 iterations in a row."],
			decisions: [event({ id: "ape_3", summary: "Patch or refactor? → Patch", detail: "Smaller risk" })],
			parked: [event({ id: "ape_7", kind: "parked", summary: "Upgrade the CI plan", detail: "money: the free minutes ran out" })],
			permissionsAllowed: 0, permissionsDenied: 0,
		},
	});

	it("says why it stopped and offers to continue", async () => {
		viewMock.mockReturnValue(parkedView());
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot parked · needs you" }));

		expect(await screen.findByTestId("autopilot-state")).toHaveTextContent("Parked. It waits for you. Nothing changed for three iterations in a row.");
		await userEvent.click(screen.getByRole("button", { name: "Continue" }));
		expect(resumeMock).toHaveBeenCalledTimes(1);
	});

	it("lists what is parked for the user and lets them mark it handled", async () => {
		viewMock.mockReturnValue(parkedView());
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot parked · needs you" }));

		const item = await screen.findByTestId("autopilot-parked-item");
		expect(item).toHaveTextContent("Upgrade the CI plan");
		expect(item).toHaveTextContent("money: the free minutes ran out");
		await userEvent.click(within(item).getByRole("button", { name: "Mark handled" }));
		expect(resolveMock).toHaveBeenCalledWith("ape_7");
	});

	it("shows the decisions it took instead of asking", async () => {
		viewMock.mockReturnValue(parkedView());
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot parked · needs you" }));

		const decisions = await screen.findByTestId("autopilot-decisions");
		expect(decisions).toHaveTextContent("Patch or refactor? → Patch");
		expect(decisions).toHaveTextContent("Smaller risk");
	});

	it("shows an item the user already handled as handled, without a button", async () => {
		const parked = parkedView();
		parked.report!.parked = [{ ...parked.report!.parked[0], resolvedAt: "2026-11-17T11:00:00Z" }];
		parked.parked = [];
		viewMock.mockReturnValue(parked);
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot parked · needs you" }));

		const item = await screen.findByTestId("autopilot-parked-item");
		expect(item).toHaveTextContent("Handled");
		expect(within(item).queryByRole("button", { name: "Mark handled" })).not.toBeInTheDocument();
	});
});

describe("a run that ended while the user was elsewhere", () => {
	const finishedView = () => view({
		run: run({ id: "ap_9", state: "done", endReason: "goal_done", criteria: "Done when green" }),
		events: [event({ kind: "done", summary: "Done: go test ok" })],
		report: {
			runId: "ap_9", task: "Make the importer twice as fast", state: "done", endReason: "goal_done", iterations: 4,
			startedAt: "2026-11-17T09:00:00Z", decisions: [], parked: [], gaps: ["CI could not run: billing limit"],
			mergedPrs: [{ url: "https://github.com/o/r/pull/4", title: "PR #4 merged", at: "2026-11-17T10:00:00Z" }],
			permissionsAllowed: 0, permissionsDenied: 0,
		},
	});

	it("leaves a report-ready mark on the button, with no Stop", () => {
		viewMock.mockReturnValue(finishedView());
		renderControl();

		expect(screen.getByRole("button", { name: "Autopilot report ready" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Stop Autopilot" })).not.toBeInTheDocument();
	});

	it("shows what Autopilot did, the gaps, and a start form for the next task; opening it acknowledges the report", async () => {
		viewMock.mockReturnValue(finishedView());
		renderControl();
		await userEvent.click(screen.getByRole("button", { name: "Autopilot report ready" }));

		const body = await screen.findByTestId("autopilot-dialog-body");
		expect(body).toHaveTextContent("Last run");
		expect(body).toHaveTextContent("Finished: the task was declared done and every check passed.");
		expect(body).toHaveTextContent("PR #4 merged");
		expect(body).toHaveTextContent("CI could not run: billing limit");
		expect(screen.getByRole("textbox", { name: "Task" })).toBeInTheDocument();
		expect(JSON.parse(window.localStorage.getItem("ao.autopilot.reportSeen") ?? "{}")).toEqual({ "local:mer-orchestrator-1": "ap_9" });
	});

	it("is a plain Autopilot button once the report was opened", () => {
		window.localStorage.setItem("ao.autopilot.reportSeen", JSON.stringify({ "local:mer-orchestrator-1": "ap_9" }));
		viewMock.mockReturnValue(finishedView());
		renderControl();

		expect(screen.getByRole("button", { name: "Autopilot" })).toBeInTheDocument();
	});

	it("keeps a finished run readable on a session that has since ended", () => {
		window.localStorage.setItem("ao.autopilot.reportSeen", JSON.stringify({ "local:mer-orchestrator-1": "ap_9" }));
		viewMock.mockReturnValue(finishedView());
		renderControl({ sessionTerminated: true });

		expect(screen.getByRole("button", { name: "Autopilot" })).toBeInTheDocument();
	});
});
