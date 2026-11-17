import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { BoardCardView, BoardColumnFrame, BoardColumnsGrid } from "./SessionsBoardView";

describe("BoardCardView and the column frames", () => {
	it("renders the Agents card shell: title, meta, sub-rows, then a status and time footer", () => {
		const onOpen = vi.fn();
		const { container } = render(
			<BoardColumnsGrid columnCount={3} scrollTestId="scroll">
				<BoardColumnFrame ariaLabel="To Do column" count={1} dot="red" label="To Do" testId="col">
					<BoardCardView
						dataKey="WEB-1"
						meta="WEB-1 · Bug"
						onOpen={onOpen}
						openLabel="Open WEB-1"
						statusClassName="text-status-working"
						statusLabel="In Progress"
						testId="card"
						time="3h"
						timeTitle="exact"
						title="Fix it"
						footerAction={<button type="button">go</button>}
					>
						<span>row</span>
					</BoardCardView>
				</BoardColumnFrame>
			</BoardColumnsGrid>,
		);
		const card = screen.getByTestId("card");
		expect(card.className).toContain("rounded-lg");
		expect(card.className).toContain("border-border");
		expect(card).toHaveAttribute("data-ticket-key", "WEB-1");
		expect(screen.getByText("Fix it").className).toContain("text-sm-md font-semibold");
		expect(screen.getByText("WEB-1 · Bug").className).toContain("text-2xs");
		expect(screen.getByTestId("board-card-status")).toHaveTextContent("In Progress");
		expect(screen.getByText("3h")).toHaveAttribute("title", "exact");
		expect(screen.getByText("row")).toBeInTheDocument();
		expect(screen.getByRole("region", { name: "To Do column" })).toHaveTextContent("To Do1");
		// Three columns get the 16rem minimum each; four use the Agents grid.
		expect(container.querySelector("[data-testid=scroll] > div")).toHaveStyle({ minWidth: "48rem" });

		fireEvent.click(screen.getByText("go"));
		expect(onOpen).toHaveBeenCalledTimes(1); // the click bubbles; apps stop it on their own controls
	});

	it("keeps the four-column Agents grid as it was", () => {
		const { container } = render(<BoardColumnsGrid>{null}</BoardColumnsGrid>);
		expect(container.querySelector("[data-testid=board-horizontal-scroll] > div")?.className).toContain("grid-cols-4");
	});
});
