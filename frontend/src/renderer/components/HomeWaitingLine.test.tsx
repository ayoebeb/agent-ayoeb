import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { OrchestratorWaiting } from "../lib/orchestrator-waiting";
import { HomeWaitingLine } from "./HomeWaitingLine";

const mocks = vi.hoisted(() => ({ navigate: vi.fn(), waiting: [] as unknown[] }));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	useNavigate: () => mocks.navigate,
}));
vi.mock("../hooks/useOrchestratorWaiting", async (importOriginal) => ({
	...(await importOriginal<typeof import("../hooks/useOrchestratorWaiting")>()),
	useOrchestratorWaiting: () => ({ waiting: mocks.waiting }),
}));

const wait = (projectId: string, sessionId: string, hostId = "local"): OrchestratorWaiting => ({
	notificationId: `n-${projectId}`,
	projectId,
	sessionId,
	question: "Merge PR #42 now?",
	reminded: false,
	since: "2026-11-17T10:00:00Z",
	title: `${projectId}: orchestrator is waiting for you`,
	hostId,
});

describe("HomeWaitingLine", () => {
	beforeEach(() => {
		mocks.navigate.mockClear();
		mocks.waiting = [];
	});

	it("shows nothing while no orchestrator is waiting", () => {
		const { container } = render(<HomeWaitingLine />);
		expect(container).toBeEmptyDOMElement();
		expect(screen.queryByTestId("home-waiting-line")).toBeNull();
	});

	it("names the one orchestrator that waits and opens its chat", () => {
		mocks.waiting = [wait("mercury", "o-1")];
		render(<HomeWaitingLine />);
		expect(screen.getByTestId("home-waiting-line")).toHaveTextContent("mercury: orchestrator is waiting for you");
		fireEvent.click(screen.getByTestId("home-waiting-line"));
		expect(mocks.navigate).toHaveBeenCalledWith({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "mercury", sessionId: "o-1" } });
	});

	it("opens a waiting orchestrator on a linked computer on that computer", () => {
		mocks.waiting = [wait("mercury", "o-1", "box")];
		render(<HomeWaitingLine />);
		fireEvent.click(screen.getByTestId("home-waiting-line"));
		expect(mocks.navigate).toHaveBeenCalledWith({
			to: "/host/$hostId/project/$projectId/session/$sessionId",
			params: { hostId: "box", projectId: "mercury", sessionId: "o-1" },
		});
	});

	it("counts several and leads to the Projects page", () => {
		mocks.waiting = [wait("mercury", "o-1"), wait("venus", "o-2", "box")];
		render(<HomeWaitingLine />);
		expect(screen.getByTestId("home-waiting-line")).toHaveTextContent("2 orchestrators are waiting for you");
		fireEvent.click(screen.getByTestId("home-waiting-line"));
		expect(mocks.navigate).toHaveBeenCalledWith({ to: "/projects" });
	});
});
