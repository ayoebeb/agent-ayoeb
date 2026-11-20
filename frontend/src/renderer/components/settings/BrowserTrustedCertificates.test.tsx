import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../../lib/bridge";
import { BrowserTrustedCertificates } from "./BrowserTrustedCertificates";

const entry = {
	host: "devtestayoeb.yoobi.ayoeb",
	fingerprint: "B9:51:F8:65",
	issuer: "Yoobi Test CA",
	trustedAt: "2026-11-20T10:00:00.000Z",
};

describe("BrowserTrustedCertificates", () => {
	const certificates = aoBridge.browser.certificates;
	const originalList = certificates.list;
	const originalRemove = certificates.remove;

	afterEach(() => {
		certificates.list = originalList;
		certificates.remove = originalRemove;
	});

	it("renders nothing when no certificate is trusted", async () => {
		certificates.list = vi.fn(async () => []);
		const { container } = render(<BrowserTrustedCertificates />);
		await waitFor(() => expect(certificates.list).toHaveBeenCalled());
		expect(container).toBeEmptyDOMElement();
	});

	it("lists trusted certificates and removes one", async () => {
		certificates.list = vi.fn(async () => [entry]);
		certificates.remove = vi.fn(async () => []);
		render(<BrowserTrustedCertificates />);

		expect(await screen.findByText("B9:51:F8:65")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Stop trusting devtestayoeb.yoobi.ayoeb" }));

		expect(certificates.remove).toHaveBeenCalledWith("devtestayoeb.yoobi.ayoeb", "B9:51:F8:65");
		await waitFor(() => expect(screen.queryByText("B9:51:F8:65")).toBeNull());
	});
});
