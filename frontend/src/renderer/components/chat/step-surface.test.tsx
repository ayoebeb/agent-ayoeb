import { render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import type { ConversationActivity } from "../../types/conversation";
import { TooltipProvider } from "../ui/tooltip";
import { ActivityRun } from "./ActivityRun";
import { ActivityRow } from "./ChatTimelineItems";
import { StepSurface } from "./step-surface";

function render(ui: ReactElement) {
	return rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
}

function command(
	id: string,
	line: string,
	detail: Partial<NonNullable<ConversationActivity["detail"]>> = {},
	status: ConversationActivity["status"] = "completed",
): ConversationActivity {
	return {
		kind: "activity",
		id,
		sequence: Number(id.replace(/\D/g, "")),
		revision: 0,
		activityKind: "command",
		status,
		summary: line,
		detail: { command: line, cwd: "/work", exitCode: 0, output: "built\n", ...detail },
		createdAt: "2026-11-16T12:00:00Z",
	};
}

describe("a row of an open step list", () => {
	it("names the command that ran and how long it took", () => {
		render(
			<StepSurface>
				<ActivityRow activity={command("activity-1", "npm run build", { durationMs: 18_200 })} />
			</StepSurface>,
		);
		expect(screen.getByText("npm run build")).toBeInTheDocument();
		expect(screen.getByText("18s")).toBeInTheDocument();
	});

	it("keeps a step on its own to its verb, so the default turn stays calm", () => {
		render(<ActivityRow activity={command("activity-1", "npm run build", { durationMs: 18_200 })} />);
		expect(screen.getByRole("button", { name: /Built project/ })).toBeInTheDocument();
		expect(screen.queryByText("npm run build")).not.toBeInTheDocument();
		expect(screen.queryByText("18s")).not.toBeInTheDocument();
	});

	it("leaves the time off a command that took under a second", () => {
		render(
			<StepSurface>
				<ActivityRow activity={command("activity-1", "npm run build", { durationMs: 400 })} />
			</StepSurface>,
		);
		expect(screen.getByText("npm run build")).toBeInTheDocument();
		expect(screen.queryByText(/^\d+(ms|s|m)$/)).not.toBeInTheDocument();
	});

	it("leaves the time off a command that failed, where its exit code says it", () => {
		render(
			<StepSurface>
				<ActivityRow activity={command("activity-1", "npm run build", { durationMs: 5_000, exitCode: 1 }, "failed")} />
			</StepSurface>,
		);
		expect(screen.queryByText("5s")).not.toBeInTheDocument();
		expect(screen.getByText("exit 1")).toBeInTheDocument();
	});

	it("shows only the first line of a multi-line command", () => {
		render(
			<StepSurface>
				<ActivityRow activity={command("activity-1", "npm run build\necho done", { durationMs: 3_000 })} />
			</StepSurface>,
		);
		expect(screen.getByText("npm run build")).toBeInTheDocument();
		expect(screen.queryByText(/echo done/)).not.toBeInTheDocument();
	});

	it("does not repeat the target of a step that already names what it touched", () => {
		render(
			<StepSurface>
				<ActivityRow activity={command("activity-1", "cat README.md", { durationMs: 2_000 })} />
			</StepSurface>,
		);
		expect(screen.getByRole("button", { name: /Read README\.md/ })).toBeInTheDocument();
		expect(screen.queryByText("cat README.md")).not.toBeInTheDocument();
	});

	it("opens onto a body with no card of its own, but keeps the card anywhere else", async () => {
		const user = userEvent.setup();
		const inList = render(
			<StepSurface>
				<ActivityRow activity={command("activity-1", "npm run build", { durationMs: 3_000 })} />
			</StepSurface>,
		);
		await user.click(screen.getByRole("button", { name: /Built project/ }));
		expect(await screen.findByText("built")).toBeInTheDocument();
		expect(inList.container.querySelector(".cursor-chat-explore-box")).toBeNull();
		expect(inList.container.querySelector(".cursor-chat-explore-command")).toBeNull();
		inList.unmount();

		const alone = render(<ActivityRow activity={command("activity-2", "npm run build", { durationMs: 3_000 })} />);
		await user.click(screen.getByRole("button", { name: /Built project/ }));
		expect(await screen.findByText("built")).toBeInTheDocument();
		expect(alone.container.querySelector(".cursor-chat-explore-box")).not.toBeNull();
	});
});

describe("an open run of steps", () => {
	const calls = [
		command("activity-1", "npm run build", { durationMs: 18_200 }),
		command("activity-2", "npm test", { durationMs: 4_180 }),
	];

	it("is one surface of its own, with each call a row", async () => {
		const user = userEvent.setup();
		const view = render(<ActivityRun activities={calls} />);
		expect(view.container.querySelector("[data-step-surface]")).toBeNull();

		await user.click(screen.getByRole("button", { name: /Ran 2 tool calls/ }));
		expect(await screen.findByText("npm run build")).toBeInTheDocument();
		expect(view.container.querySelectorAll("[data-step-surface]")).toHaveLength(1);
		expect(screen.getByText("18s")).toBeInTheDocument();
		expect(screen.getByText("4s")).toBeInTheDocument();
	});

	it("is more rows of the surface it sits in, never a card inside a card", async () => {
		const user = userEvent.setup();
		const view = render(
			<StepSurface>
				<ActivityRun activities={calls} />
			</StepSurface>,
		);
		await user.click(screen.getByRole("button", { name: /Ran 2 tool calls/ }));
		expect(await screen.findByText("npm run build")).toBeInTheDocument();
		expect(view.container.querySelectorAll("[data-step-surface]")).toHaveLength(1);
	});
});
