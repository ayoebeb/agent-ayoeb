import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AutopilotLearning } from "../lib/autopilot-learning";

const { dataMock, addMock, editMock, deleteRuleMock, deletePairMock, importMock, relearnMock } = vi.hoisted(() => ({
	dataMock: vi.fn(),
	addMock: vi.fn(),
	editMock: vi.fn(),
	deleteRuleMock: vi.fn(),
	deletePairMock: vi.fn(),
	importMock: vi.fn(),
	relearnMock: vi.fn(),
}));

const idle = (mutate: ReturnType<typeof vi.fn>) => ({ mutate, isPending: false, error: null });
vi.mock("../hooks/useAutopilotLearning", () => ({
	useAutopilotLearning: () => ({ data: dataMock() }),
	useAutopilotLearningActions: () => ({
		importHistory: idle(importMock),
		relearn: idle(relearnMock),
		addRule: idle(addMock),
		editRule: idle(editMock),
		deleteRule: idle(deleteRuleMock),
		deletePair: idle(deletePairMock),
		error: undefined,
	}),
}));

import { AutopilotLearnedDialog } from "./AutopilotLearnedDialog";

const learning = (overrides: Partial<AutopilotLearning> = {}): AutopilotLearning => ({
	rules: [
		{ id: "r1", scope: "global", text: "Quality over cost for big features.", origin: "learned", updatedAt: "2026-11-24T09:00:00Z" },
		{ id: "r2", scope: "global", text: "Restart Ayo only with my OK.", origin: "owner", updatedAt: "2026-11-24T09:00:00Z" },
		{ id: "r3", scope: "mer", text: "Small fixes: merge without waiting for CI.", origin: "learned", updatedAt: "2026-11-24T09:00:00Z" },
		{ id: "r4", scope: "global", text: "Move to another account when limited.", origin: "learned", tag: "usage_limit_switch", updatedAt: "2026-11-24T09:00:00Z" },
	],
	pairs: [
		{
			id: "p1", project: "mer", at: "2026-11-20T09:00:00Z", source: "live", question: "Merge the small fix now?",
			options: [], recommendation: "", answer: "ja merge", folded: true,
		},
		{
			id: "p2", project: "mer", at: "2026-11-21T09:00:00Z", source: "correction", question: "Merge without CI?",
			options: [], recommendation: "", answer: "The owner undid this choice", folded: false,
		},
	],
	totalPairs: 2,
	unfoldedPairs: 1,
	lastError: "",
	canLearn: true,
	learning: false,
	...overrides,
});

function renderDialog() {
	return render(<AutopilotLearnedDialog open onOpenChange={() => {}} />);
}

beforeEach(() => {
	for (const mock of [dataMock, addMock, editMock, deleteRuleMock, deletePairMock, importMock, relearnMock]) mock.mockReset();
	dataMock.mockReturnValue(learning());
});

describe("AutopilotLearnedDialog", () => {
	it("shows the rules grouped by scope, who wrote them, and the decisions they came from", () => {
		renderDialog();
		const rules = within(screen.getByTestId("autopilot-learned-rules")).getAllByTestId("autopilot-learned-rule");
		expect(rules).toHaveLength(4);
		expect(screen.getByText("All projects")).toBeTruthy();
		expect(screen.getByText("mer")).toBeTruthy();
		expect(screen.getByText("Yours")).toBeTruthy();
		expect(screen.getByText(/lets Autopilot move work to another account/)).toBeTruthy();
		expect(screen.getByTestId("autopilot-learned-stats").textContent).toContain("2 decisions stored, 1 not yet in the profile");
		const pairs = screen.getAllByTestId("autopilot-learned-pair");
		expect(pairs).toHaveLength(2);
		expect(screen.getByText("You answered: ja merge")).toBeTruthy();
		expect(screen.getByText(/You undid Autopilot's choice/)).toBeTruthy();
		expect(screen.getByText(/hard limits/i)).toBeTruthy();
	});

	it("edits a rule in place and saves it", async () => {
		const user = userEvent.setup();
		renderDialog();
		const row = screen.getAllByTestId("autopilot-learned-rule")[0] as HTMLElement;
		await user.click(within(row).getByRole("button", { name: "Edit" }));
		const input = within(row).getByRole("textbox");
		await user.clear(input);
		await user.type(input, "Quality first.");
		await user.click(within(row).getByRole("button", { name: "Save" }));
		expect(editMock).toHaveBeenCalledWith({ id: "r1", text: "Quality first." }, expect.anything());
	});

	it("deletes a rule and a decision", async () => {
		const user = userEvent.setup();
		renderDialog();
		await user.click(within(screen.getAllByTestId("autopilot-learned-rule")[1] as HTMLElement).getByRole("button", { name: "Delete" }));
		expect(deleteRuleMock).toHaveBeenCalledWith("r2");
		await user.click(within(screen.getAllByTestId("autopilot-learned-pair")[0] as HTMLElement).getByRole("button", { name: "Delete" }));
		expect(deletePairMock).toHaveBeenCalledWith("p1");
	});

	it("adds a rule of the owner's own as a global rule", async () => {
		const user = userEvent.setup();
		renderDialog();
		const add = screen.getByRole("button", { name: "Add" }) as HTMLButtonElement;
		expect(add.disabled).toBe(true);
		await user.type(screen.getByPlaceholderText(/Add a rule/), "Never touch main.");
		await user.click(add);
		expect(addMock).toHaveBeenCalledWith({ scope: "global", text: "Never touch main." }, expect.anything());
	});

	it("imports past conversations and asks for a profile update", async () => {
		const user = userEvent.setup();
		renderDialog();
		await user.click(screen.getByRole("button", { name: "Learn from past conversations" }));
		expect(importMock).toHaveBeenCalledTimes(1);
		await user.click(screen.getByRole("button", { name: "Update profile now" }));
		expect(relearnMock).toHaveBeenCalledTimes(1);
	});

	it("explains an empty state, a failed update and a missing model", () => {
		dataMock.mockReturnValue(learning({ rules: [], pairs: [], totalPairs: 0, unfoldedPairs: 0, canLearn: false, lastError: "claude not found" }));
		renderDialog();
		expect(screen.getByTestId("autopilot-learned-no-rules")).toBeTruthy();
		expect(screen.getByTestId("autopilot-learned-no-pairs")).toBeTruthy();
		expect(screen.getByRole("alert").textContent).toContain("claude not found");
		expect(screen.getByText(/need the claude command/)).toBeTruthy();
		expect((screen.getByRole("button", { name: "Update profile now" }) as HTMLButtonElement).disabled).toBe(true);
	});
});
