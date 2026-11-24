import { QueryClient, QueryClientProvider, focusManager } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { autopilotRunsQueryKey, type AutopilotRunOnHost, type AutopilotView } from "../lib/autopilot";

const { fetchRuns, fetchView, previewMode } = vi.hoisted(() => ({ fetchRuns: vi.fn(), fetchView: vi.fn(), previewMode: { value: false } }));

vi.mock("../lib/preview-mode", () => ({
	get usesPreviewWorkspaceData() {
		return previewMode.value;
	},
}));

vi.mock("../lib/autopilot", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/autopilot")>()),
	fetchAutopilotRuns: fetchRuns,
	fetchAutopilotView: fetchView,
}));
vi.mock("./useHostConnection", () => ({ useConnectedHosts: () => [] }));

import { useAutopilotRuns, useAutopilotView, VIEW_POLL_IDLE_MS, VIEW_POLL_OPEN_MS } from "./useAutopilot";

const emptyView: AutopilotView = { events: [], parked: [], warnings: [] };

const run = (overrides: Partial<AutopilotRunOnHost> = {}): AutopilotRunOnHost =>
	({
		id: "ap_1", sessionId: "s-1", projectId: "p-1", task: "Make it fast", state: "running", iteration: 0, maxIterations: 30,
		permissionsAllowed: 0, permissionsDenied: 0, startedAt: "2026-11-24T09:00:00Z", updatedAt: "2026-11-24T09:00:00Z",
		openParkedItemCount: 0, hostId: "local", ...overrides,
	}) as AutopilotRunOnHost;

const openView = (): AutopilotView => ({ ...emptyView, run: run() });

function setup() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
	return { client, wrapper };
}
const wrapper = () => setup().wrapper;
// The sidebar keeps the open-run list in the cache; the hook only listens to it.
const publishRuns = (client: QueryClient, runs: AutopilotRunOnHost[]) => act(() => client.setQueryData(autopilotRunsQueryKey("local"), runs));

beforeEach(() => {
	previewMode.value = false;
	fetchRuns.mockReset().mockResolvedValue([]);
	fetchView.mockReset().mockResolvedValue(emptyView);
});
afterEach(() => {
	vi.useRealTimers();
	focusManager.setFocused(undefined);
});

describe("in the browser preview", () => {
	test("nothing is read: there is no daemon, and a read would hang on the dev proxy", async () => {
		previewMode.value = true;
		renderHook(() => useAutopilotView("local", "s-1"), { wrapper: wrapper() });
		renderHook(() => useAutopilotRuns(), { wrapper: wrapper() });
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(fetchView).not.toHaveBeenCalled();
		expect(fetchRuns).not.toHaveBeenCalled();
	});

	test("outside the preview the open-run list is read", async () => {
		renderHook(() => useAutopilotRuns(), { wrapper: wrapper() });
		await waitFor(() => expect(fetchRuns).toHaveBeenCalled());
	});
});

describe("useAutopilotView", () => {
	test("reads a session once and does not read it again just because it is shown again", async () => {
		const { result, rerender } = renderHook(() => useAutopilotView("local", "s-1"), { wrapper: wrapper() });
		await waitFor(() => expect(result.current.data).toEqual(emptyView));
		rerender();
		rerender();
		expect(fetchView).toHaveBeenCalledTimes(1);
	});

	test("reads nothing for a session that is not selected or a hook that is switched off", async () => {
		renderHook(() => useAutopilotView("local", undefined), { wrapper: wrapper() });
		renderHook(() => useAutopilotView("local", "s-1", false), { wrapper: wrapper() });
		await Promise.resolve();
		expect(fetchView).not.toHaveBeenCalled();
	});

	test("a run the open-run list names is read as soon as the view has loaded", async () => {
		const { client, wrapper } = setup();
		publishRuns(client, [run()]);
		fetchView.mockResolvedValueOnce(emptyView).mockResolvedValue({ ...emptyView, run: run() });
		const { result } = renderHook(() => useAutopilotView("local", "s-1"), { wrapper });
		await waitFor(() => expect(result.current.data?.run?.state).toBe("running"));
		// One read for the mount, one because the list knew better, and then it stops.
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(fetchView).toHaveBeenCalledTimes(2);
	});

	test("a run started elsewhere while the view is open is read when the list announces it", async () => {
		const { client, wrapper } = setup();
		const { result } = renderHook(() => useAutopilotView("local", "s-1"), { wrapper });
		await waitFor(() => expect(result.current.data).toEqual(emptyView));
		expect(fetchView).toHaveBeenCalledTimes(1);
		fetchView.mockResolvedValue({ ...emptyView, run: run() });
		publishRuns(client, [run()]);
		await waitFor(() => expect(result.current.data?.run?.state).toBe("running"));
		expect(fetchView).toHaveBeenCalledTimes(2);
		// The list changing again (the same run, a new poll) does not read it again.
		publishRuns(client, [run()]);
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(fetchView).toHaveBeenCalledTimes(2);
	});

	test("a list that says open while the view says otherwise is read once, not in a loop", async () => {
		const { client, wrapper } = setup();
		publishRuns(client, [run()]);
		const { result } = renderHook(() => useAutopilotView("local", "s-1"), { wrapper });
		await waitFor(() => expect(result.current.data).toEqual(emptyView));
		await new Promise((resolve) => setTimeout(resolve, 100));
		// The mount read and the one forced read; the daemon keeps saying "no run".
		expect(fetchView).toHaveBeenCalledTimes(2);
		publishRuns(client, [run()]);
		await new Promise((resolve) => setTimeout(resolve, 100));
		expect(fetchView).toHaveBeenCalledTimes(2);
	});

	test("a run on another session does not trigger a read", async () => {
		const { client, wrapper } = setup();
		const { result } = renderHook(() => useAutopilotView("local", "s-1"), { wrapper });
		await waitFor(() => expect(result.current.data).toEqual(emptyView));
		publishRuns(client, [run({ sessionId: "someone-else" })]);
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(fetchView).toHaveBeenCalledTimes(1);
	});

	test("a finished run in the list does not trigger a read either", async () => {
		const { client, wrapper } = setup();
		const { result } = renderHook(() => useAutopilotView("local", "s-1"), { wrapper });
		await waitFor(() => expect(result.current.data).toEqual(emptyView));
		publishRuns(client, [run({ state: "done" as AutopilotRunOnHost["state"] })]);
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(fetchView).toHaveBeenCalledTimes(1);
	});

	test("the hook starts no read of the open-run list: the sidebar owns that request", async () => {
		const { result } = renderHook(() => useAutopilotView("local", "s-1"), { wrapper: wrapper() });
		await waitFor(() => expect(result.current.data).toEqual(emptyView));
		expect(fetchRuns).not.toHaveBeenCalled();
	});

	test("a stale view is read again when the window regains focus, a fresh one is not", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		const { result } = renderHook(() => useAutopilotView("local", "s-1"), { wrapper: wrapper() });
		await waitFor(() => expect(result.current.data).toEqual(emptyView));
		// Fresh: focus changes nothing.
		act(() => {
			focusManager.setFocused(false);
			focusManager.setFocused(true);
		});
		expect(fetchView).toHaveBeenCalledTimes(1);
		// Stale: focus reads it again.
		await act(async () => {
			await vi.advanceTimersByTimeAsync(31_000);
		});
		fetchView.mockClear();
		act(() => {
			focusManager.setFocused(false);
			focusManager.setFocused(true);
		});
		await waitFor(() => expect(fetchView).toHaveBeenCalled());
	});

	test("polls slowly while no run is open and quickly while one is", async () => {
		expect(VIEW_POLL_IDLE_MS).toBeGreaterThanOrEqual(30_000);
		expect(VIEW_POLL_OPEN_MS).toBeLessThanOrEqual(10_000);

		vi.useFakeTimers({ shouldAdvanceTime: true });
		const idle = renderHook(() => useAutopilotView("local", "s-1"), { wrapper: wrapper() });
		await waitFor(() => expect(idle.result.current.data).toEqual(emptyView));
		fetchView.mockClear();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(VIEW_POLL_OPEN_MS * 3);
		});
		expect(fetchView).not.toHaveBeenCalled();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(VIEW_POLL_IDLE_MS);
		});
		expect(fetchView).toHaveBeenCalledTimes(1);
		idle.unmount();

		fetchView.mockReset().mockResolvedValue(openView());
		const open = renderHook(() => useAutopilotView("local", "s-2"), { wrapper: wrapper() });
		await waitFor(() => expect(open.result.current.data?.run).toBeDefined());
		fetchView.mockClear();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(VIEW_POLL_OPEN_MS * 2 + 100);
		});
		expect(fetchView.mock.calls.length).toBeGreaterThanOrEqual(2);
	});
});
