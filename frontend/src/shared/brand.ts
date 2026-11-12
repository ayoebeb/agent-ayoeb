// The one place the product's display name lives. Everything people read as the
// app's name (window titles, menus, tray, About, installer and shortcut labels,
// onboarding and update copy) derives from PRODUCT_NAME, so a rename is a
// one-line change and upstream merges conflict here and nowhere else.
//
// This is a DISPLAY name only. Never derive an identifier or a path from it.
// These stay fixed on purpose, whatever the product is called:
//   - the Electron appId / Windows AppUserModelID ("dev.agent-orchestrator.desktop")
//   - the packaged binary name ("agent-orchestrator") and the macOS bundle name
//     ("Agent Orchestrator.app"), which `ao start` and the updater look for
//   - Electron's pinned userData dir (~/.ao/electron), the single-instance lock,
//     the "ao-app" protocol, the `ao` CLI, ~/.ao and the AO_* environment
// Locale catalogs (src/renderer/i18n/*.json) are plain JSON and carry the name
// as literal text; keep them in step with this constant.
export const PRODUCT_NAME = "Ayo";

/** Suffix that keeps an unpackaged dev run from impersonating the installed app. */
export const DEV_NAME_SUFFIX = " (dev)";

/** The name for the running build: "Ayo" when packaged, "Ayo (dev)" otherwise. */
export function productDisplayName(isPackaged: boolean): string {
	return isPackaged ? PRODUCT_NAME : `${PRODUCT_NAME}${DEV_NAME_SUFFIX}`;
}
