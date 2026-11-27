import path from "node:path";

// The daily Ayo (scripts/daily-ao) is this repository's dev build in
// <home>/.ao/daily/<checkout>/frontend, with its profile in <home>/.ao/electron,
// which the start script passes as AO_DEV_ELECTRON_DIR. A browser opening an
// ao-app:// link starts that checkout without the variable, so on the shared dev
// profile: a second, empty app that the running Ayo never hears from.

/**
 * The daily Ayo's profile when this process is such a browser launch, else null.
 * A launch it matches must only forward the link and quit: it has none of the
 * rest of the daily environment.
 */
export function dailyLinkForwardDir(launch: {
	isPackaged: boolean;
	env: Record<string, string | undefined>;
	argv: readonly string[];
	appPath: string;
	homeDir: string;
}): string | null {
	const { isPackaged, env, argv, appPath, homeDir } = launch;
	if (isPackaged || env.AO_DEV_ELECTRON_DIR !== undefined) return null;
	if (!argv.some((value) => value.startsWith("ao-app://"))) return null;
	// On another drive, relative() returns the absolute path.
	const relative = path.relative(path.join(homeDir, ".ao", "daily"), appPath);
	const parts = relative.split(path.sep);
	if (path.isAbsolute(relative) || parts.length !== 2 || parts[0] === ".." || parts[1] !== "frontend") return null;
	return path.join(homeDir, ".ao", "electron");
}

/** What may be logged of a deep link: the rest carries sign-in codes and share tokens. */
export function deepLinkHost(link: string): string {
	return link.slice("ao-app://".length).split(/[/?#]/, 1)[0];
}
