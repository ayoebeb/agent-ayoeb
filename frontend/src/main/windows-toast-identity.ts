import { execFile } from "node:child_process";
import path from "node:path";

/**
 * Gives an unpackaged Windows run the name and icon its toasts show.
 *
 * Windows labels a toast with the display name registered for the app's
 * AppUserModelID, which a packaged install gets from its Start-menu shortcut. A
 * dev or daily run (electron.exe from node_modules) has no shortcut, so Windows
 * falls back to the executable's name and every toast reads "Electron". This
 * registers the name and icon under
 * HKCU\Software\Classes\AppUserModelId\<id>, the documented place for an
 * unpackaged app.
 *
 * It writes outside ~/.ao, which AGENTS.md allows only for this and the Chrome
 * native-messaging manifest. It holds no AO state (a name and an icon path),
 * needs no elevation, and is skipped for a packaged build, whose installer
 * already carries the name.
 */

export const TOAST_IDENTITY_KEY_ROOT = "HKCU\\Software\\Classes\\AppUserModelId";

export type RegResult = { code: number; stdout: string };
export type RunReg = (args: string[]) => Promise<RegResult>;

export type ToastIdentityOutcome = "skipped" | "unchanged" | "registered" | "failed";

function regExePath(): string {
	return path.join(process.env.SystemRoot ?? "C:\\Windows", "System32", "reg.exe");
}

const runRegExe: RunReg = (args) =>
	new Promise((resolve) => {
		execFile(regExePath(), args, { windowsHide: true, timeout: 10_000 }, (error, stdout) => {
			const code = error ? (typeof error.code === "number" ? error.code : 1) : 0;
			resolve({ code, stdout: String(stdout ?? "") });
		});
	});

/** `reg query` prints `    Name    REG_SZ    value`; returns the value, or undefined when it is absent. */
export function parseRegSz(stdout: string, name: string): string | undefined {
	for (const line of stdout.split(/\r?\n/)) {
		const match = new RegExp(`^\\s*${name}\\s+REG_SZ\\s+(.*?)\\s*$`).exec(line);
		if (match) return match[1];
	}
	return undefined;
}

export async function ensureWindowsToastIdentity(options: {
	aumid: string;
	displayName: string;
	/** Absolute path of a PNG or ICO. Omitted: only the name is registered. */
	iconPath?: string;
	packaged: boolean;
	platform?: NodeJS.Platform;
	run?: RunReg;
}): Promise<ToastIdentityOutcome> {
	const platform = options.platform ?? process.platform;
	if (platform !== "win32" || options.packaged) return "skipped";
	const run = options.run ?? runRegExe;
	const key = `${TOAST_IDENTITY_KEY_ROOT}\\${options.aumid}`;
	const wanted: Array<[string, string]> = [["DisplayName", options.displayName]];
	if (options.iconPath) wanted.push(["IconUri", options.iconPath]);
	try {
		const current = await run(["query", key]);
		const missing = wanted.filter(([name, value]) => current.code !== 0 || parseRegSz(current.stdout, name) !== value);
		if (missing.length === 0) return "unchanged";
		for (const [name, value] of missing) {
			const added = await run(["add", key, "/v", name, "/t", "REG_SZ", "/d", value, "/f"]);
			if (added.code !== 0) return "failed";
		}
		return "registered";
	} catch {
		return "failed";
	}
}
