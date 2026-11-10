/**
 * ⌘⇧M: opens the chat composer's model menu, on a session's chat and on the new
 * chat page alike. Closing the menu hands focus back to the composer
 * (TurnSettingsBar's restoreComposerFocus reads the mark set here).
 */
export function openComposerModelPicker(): void {
	openComposerModelPickerIn(document);
}

/** The same, for the composer inside `root` (one pane of sessions side by side). */
export function openComposerModelPickerIn(root: ParentNode): void {
	const picker = root.querySelector<HTMLElement>("[data-shortcut-target='model-picker']:not([disabled])");
	if (!picker) return;
	// Radix menu triggers open on pointerdown or Enter, never on a synthetic click.
	picker.dataset.shortcutOpened = "";
	picker.focus();
	picker.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
}

/** Dictate (⌥D on macOS): starts or finishes dictation in the focused chat composer, else the first one on screen. */
export function toggleComposerDictation(): void {
	toggleComposerDictationIn(document);
}

/** The same, for the composer inside `root`. */
export function toggleComposerDictationIn(root: ParentNode): void {
	const buttons = Array.from(root.querySelectorAll<HTMLButtonElement>("[data-shortcut-target='dictate']:not([disabled])"));
	const active = document.activeElement;
	(buttons.find((button) => active && button.closest("form")?.contains(active)) ?? buttons[0])?.click();
}
