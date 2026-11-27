import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TelemetryBoundary } from "./TelemetryBoundary";

vi.mock("../lib/telemetry", () => ({
	bindRendererErrorHandlers: vi.fn(),
	captureRendererException: vi.fn(),
}));

function Broken(): never {
	throw new Error("view exploded");
}

afterEach(() => {
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
});

describe("TelemetryBoundary", () => {
	it("renders its children when nothing fails", () => {
		render(
			<TelemetryBoundary>
				<p>app</p>
			</TelemetryBoundary>,
		);
		expect(screen.getByText("app")).toBeInTheDocument();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	it("shows the error with a reload button instead of a blank window, and logs it", async () => {
		const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
		const reload = vi.fn();
		vi.stubGlobal("location", { ...window.location, reload });

		render(
			<TelemetryBoundary>
				<Broken />
			</TelemetryBoundary>,
		);

		expect(screen.getByRole("heading", { name: "Er ging iets mis" })).toBeInTheDocument();
		expect(screen.getByText("Error: view exploded")).toBeInTheDocument();
		expect(consoleError).toHaveBeenCalledWith(expect.stringContaining("AO: app error screen shown: Error: view exploded"));
		await userEvent.click(screen.getByRole("button", { name: "Reload" }));
		expect(reload).toHaveBeenCalledTimes(1);
	});
});
