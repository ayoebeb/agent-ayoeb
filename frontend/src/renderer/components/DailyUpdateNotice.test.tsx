import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { DailyUpdateState } from "../../shared/daily-update";
import { DailyUpdateNoticeView } from "./DailyUpdateNotice";

function renderNotice(status: DailyUpdateState, onRestart = vi.fn()) {
	render(<DailyUpdateNoticeView onRestart={onRestart} status={status} />);
	return onRestart;
}

describe("DailyUpdateNotice", () => {
	it("renders nothing while no update waits", () => {
		renderNotice({ state: "hidden" });
		expect(screen.queryByTestId("daily-update-notice")).not.toBeInTheDocument();
	});

	it("says what is ready and restarts only on the click", async () => {
		const onRestart = renderNotice({ state: "ready", changes: 3, target: "abc" });
		expect(screen.getByTestId("daily-update-notice")).toHaveTextContent("Update ready: 3 changes");
		expect(onRestart).not.toHaveBeenCalled();
		await userEvent.click(screen.getByRole("button", { name: "Restart Ayo to apply the update" }));
		expect(onRestart).toHaveBeenCalledTimes(1);
	});

	it("uses the singular for one change", () => {
		renderNotice({ state: "ready", changes: 1, target: "abc" });
		expect(screen.getByTestId("daily-update-notice")).toHaveTextContent("Update ready: 1 change");
		expect(screen.getByTestId("daily-update-notice")).not.toHaveTextContent("1 changes");
	});

	it("shows progress without a button while restarting", () => {
		renderNotice({ state: "restarting", changes: 3, target: "abc" });
		expect(screen.getByTestId("daily-update-notice")).toHaveTextContent("Restarting to update…");
		expect(screen.queryByTestId("daily-update-restart")).not.toBeInTheDocument();
	});

	it("reports a failure and offers Retry", async () => {
		const onRestart = renderNotice({ state: "failed", changes: 3, target: "abc", message: "exit code 1" });
		expect(screen.getByTestId("daily-update-notice")).toHaveTextContent("Update failed");
		expect(screen.getByTestId("daily-update-restart")).toHaveTextContent("Retry");
		await userEvent.click(screen.getByTestId("daily-update-restart"));
		expect(onRestart).toHaveBeenCalledTimes(1);
	});
});
