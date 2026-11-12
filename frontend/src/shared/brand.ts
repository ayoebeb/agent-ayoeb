// The product's display name, and the single constant the TypeScript shell reads
// it from. This is a DISPLAY name only: never derive an identifier or a path from it.
//
// Derives from PRODUCT_NAME (change it here):
//   - window and dev-window titles, the About dialog, the macOS app menu
//   - the tray tooltip, the sidebar and titlebar brand, the startup loader label
//   - the Windows installer / shortcut / exe metadata and the AppImage name
//   - the Chrome protocol handler label
//
// Does NOT derive from it. These carry the literal name and must be edited by
// hand on a rename (search for the old name), so a rename is NOT a one-line
// change and an upstream merge can conflict at these sites, not only here:
//   - frontend/index.html (<title>) and assets/mac/en.lproj/InfoPlist.strings
//     (the Finder/Dock label)
//   - src/browser-extension: manifest.json, pages/*.html and string literals, and
//     the Go text that repeats the extension name (backend/internal/cli/
//     browser_chrome.go, browserchrome/setup.go, skillassets/using-ao/commands/
//     browser.md): Chrome lists the extension by its manifest name
//   - src/renderer/i18n/*.json (about 160 keys per locale) and the generated tray
//     catalog (src/main/tray-catalog.generated.ts, regenerated with
//     `node scripts/sync-tray-catalog.mjs`; tray-catalog.test.ts fails on drift)
//   - defaultValue and other string literals in renderer components, and the
//     main-process dialogs, errors and sign-in pages
//   - the tests that assert any of the above, including test/e2e-pod/real-app.spec.ts,
//     which declares its own copy of the name (the pod fetches no repo code)
//
// These stay fixed on purpose, whatever the product is called:
//   - the Electron appId / Windows AppUserModelID ("dev.agent-orchestrator.desktop")
//   - the packaged binary name ("agent-orchestrator") and the macOS bundle name
//     ("Agent Orchestrator.app"), which `ao start` and the updater look for
//   - Electron's pinned userData dir (~/.ao/electron), the single-instance lock,
//     the "ao-app" protocol, the `ao` CLI, ~/.ao and the AO_* environment
export const PRODUCT_NAME = "Ayo";

/** Suffix that keeps an unpackaged dev run from impersonating the installed app. */
export const DEV_NAME_SUFFIX = " (dev)";

/** The name for the running build: "Ayo" when packaged, "Ayo (dev)" otherwise. */
export function productDisplayName(isPackaged: boolean): string {
	return isPackaged ? PRODUCT_NAME : `${PRODUCT_NAME}${DEV_NAME_SUFFIX}`;
}
