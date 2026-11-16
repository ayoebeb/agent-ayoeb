import { beforeEach, describe, expect, it, vi } from "vitest";
import { LOCAL_HOST } from "./hosts";

const { localGet, remoteGet } = vi.hoisted(() => ({ localGet: vi.fn(), remoteGet: vi.fn() }));

vi.mock("./api-client", () => ({ apiClient: { GET: localGet } }));
vi.mock("./host-clients", () => ({ clientForHost: () => ({ GET: remoteGet }) }));

import {
	fetchOrchestratorWaiting,
	refreshOrchestratorWaiting,
	waitingForProject,
	waitingForSession,
	waitsBeyondUnread,
	type OrchestratorWaiting,
} from "./orchestrator-waiting";

const wait = (overrides: Partial<OrchestratorWaiting> = {}): OrchestratorWaiting => ({
	notificationId: "ntf_1",
	sessionId: "mer-orchestrator-1",
	projectId: "mer",
	title: "Mercury: orchestrator wacht op u",
	question: "Merge PR 42 now, or wait?",
	since: "2026-11-16T12:00:00Z",
	reminded: false,
	hostId: LOCAL_HOST,
	...overrides,
});

beforeEach(() => {
	localGet.mockReset();
	remoteGet.mockReset();
});

describe("fetchOrchestratorWaiting", () => {
	it("reads this computer's waits and stamps them with the host", async () => {
		const { hostId: _host, ...item } = wait();
		localGet.mockResolvedValue({ data: { waiting: [item] }, response: { status: 200 } });

		await expect(fetchOrchestratorWaiting(LOCAL_HOST)).resolves.toEqual([wait()]);
		expect(localGet).toHaveBeenCalledWith("/api/v1/orchestrator-waiting");
	});

	it("reads a remote host's waits through that host's client", async () => {
		const { hostId: _host, ...item } = wait({ projectId: "venus" });
		remoteGet.mockResolvedValue({ data: { waiting: [item] }, response: { status: 200 } });

		await expect(fetchOrchestratorWaiting("host-b")).resolves.toEqual([wait({ projectId: "venus", hostId: "host-b" })]);
		expect(localGet).not.toHaveBeenCalled();
	});

	// A host from before this existed has no such route; that is nothing waiting,
	// not a failure the user can act on.
	it.each([404, 501])("treats a host without the route (%i) as nothing waiting", async (status) => {
		remoteGet.mockResolvedValue({ error: { code: "NOT_FOUND" }, response: { status } });
		await expect(fetchOrchestratorWaiting("host-b")).resolves.toEqual([]);
	});

	it("surfaces a real failure", async () => {
		localGet.mockResolvedValue({ error: { message: "boom" }, response: { status: 500 } });
		await expect(fetchOrchestratorWaiting(LOCAL_HOST)).rejects.toBeTruthy();
	});
});

describe("lookups", () => {
	const waits = [wait(), wait({ projectId: "venus", sessionId: "venus-orchestrator-1", hostId: "host-b", notificationId: "ntf_2" })];

	it("finds a wait by project and host, so the same project id on two hosts stays apart", () => {
		expect(waitingForProject(waits, LOCAL_HOST, "mer")?.notificationId).toBe("ntf_1");
		expect(waitingForProject(waits, "host-b", "venus")?.notificationId).toBe("ntf_2");
		expect(waitingForProject(waits, LOCAL_HOST, "venus")).toBeUndefined();
		expect(waitingForProject(waits, "host-b", "mer")).toBeUndefined();
	});

	it("finds a wait by session and host", () => {
		expect(waitingForSession(waits, "host-b", "venus-orchestrator-1")?.projectId).toBe("venus");
		expect(waitingForSession(waits, LOCAL_HOST, "venus-orchestrator-1")).toBeUndefined();
	});
});

// The unread count already holds a wait whose notification is still unread. Once
// the user opens or clears the panel it stops counting while the orchestrator is
// still waiting, and the badge must keep saying so.
describe("waitsBeyondUnread", () => {
	const waits = [wait(), wait({ notificationId: "ntf_2", projectId: "venus", hostId: "host-b" })];

	it("adds nothing for a wait whose notification is still unread", () => {
		expect(waitsBeyondUnread(waits, new Set([`${LOCAL_HOST}:ntf_1`, "host-b:ntf_2"]))).toBe(0);
	});

	it("adds the waits the user already saw or cleared", () => {
		expect(waitsBeyondUnread(waits, new Set())).toBe(2);
		expect(waitsBeyondUnread(waits, new Set([`${LOCAL_HOST}:ntf_1`]))).toBe(1);
	});

	it("does not mix up the same notification id on different hosts", () => {
		expect(waitsBeyondUnread(waits, new Set(["host-b:ntf_1"]))).toBe(2);
	});
});

describe("refreshOrchestratorWaiting", () => {
	it("refetches only mounted readers, for one host or all of them", () => {
		const refetchQueries = vi.fn().mockResolvedValue(undefined);
		const queryClient = { refetchQueries } as never;
		refreshOrchestratorWaiting(queryClient);
		refreshOrchestratorWaiting(queryClient, "host-b");
		expect(refetchQueries).toHaveBeenNthCalledWith(1, { queryKey: ["orchestrator-waiting"], type: "active" });
		expect(refetchQueries).toHaveBeenNthCalledWith(2, { queryKey: ["orchestrator-waiting", "host-b"], type: "active" });
	});
});
