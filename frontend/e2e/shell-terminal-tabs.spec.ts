import { expect, test } from "@playwright/test";

// Shell terminals (#2822): shells the user opens by hand inside a session.
// They render as tabs beside the session's own pane.
// The real pane needs a daemon-spawned PTY, so the preview build stands in with
// an in-memory shell list — enough to cover the parts that live in the renderer:
// which tab is current, and that opening/closing updates the strip.
test("opens, selects, and closes standalone shell terminals from the tab strip", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	await expect(page.getByRole("button", { name: "New terminal" })).toBeVisible();

	const closeButtons = page.getByRole("button", { name: /^Close terminal / });
	const initialCount = await closeButtons.count();

	// The button at the end of the tab strip opens the new-terminal menu (the
	// daemon's shells, the default first); its first row opens the default
	// shell, makes it the active pane, and closes the menu. No session picker.
	await page.getByRole("button", { name: "New terminal" }).click();
	await page.getByRole("menu").getByRole("menuitem").first().click();
	await expect(page.getByRole("menu")).toHaveCount(0);
	await expect(closeButtons).toHaveCount(initialCount + 1);

	// Selecting the session tab hands the pane back to the agent. Matched by the
	// session's own title: the tab's accessible name is that title, and its
	// title attribute falls back to the label once the strip truncates it.
	// Scoped to the terminal panel: the sidebar carries the same session name.
	const sessionTab = page
		.getByTestId("terminal")
		.getByRole("tab", { name: /^Build screenshot-ready dashboard data/ });
	await sessionTab.click();
	await expect(sessionTab).toHaveAttribute("aria-selected", "true");

	// Closing a shell removes exactly its own tab. Its close control takes the
	// glyph's slot on hover, so reach it the way a pointer does.
	await page.locator("[data-terminal-tab-frame]").filter({ has: closeButtons.last() }).hover();
	await closeButtons.last().click();
	await expect(closeButtons).toHaveCount(initialCount);
});
