// @vitest-environment node
import path from "node:path";
import { describe, expect, it } from "vitest";
import { dailyLinkForwardDir, deepLinkHost } from "./daily-link-launch";

const homeDir = path.resolve("/home/someone");
const dailyFrontend = path.join(homeDir, ".ao", "daily", "agent-ayoeb", "frontend");
const link = "ao-app://callback?code=c&state=s";

// A browser launch: `electron <daily frontend> <link>` with no AO_* environment.
const browserLaunch = {
	isPackaged: false,
	env: {},
	argv: ["electron", dailyFrontend, link],
	appPath: dailyFrontend,
	homeDir,
};

describe("dailyLinkForwardDir", () => {
	it("gives a browser-opened link in the daily checkout the daily profile", () => {
		expect(dailyLinkForwardDir(browserLaunch)).toBe(path.join(homeDir, ".ao", "electron"));
	});

	it.each([
		["the packaged app", { isPackaged: true }],
		["a launch that names its own profile", { env: { AO_DEV_ELECTRON_DIR: path.join(homeDir, "profile") } }],
		["a start without a link", { argv: ["electron", dailyFrontend] }],
		["a worktree", { appPath: path.join(homeDir, ".ao", "data", "worktrees", "p", "w", "frontend") }],
		["a checkout outside the AO home", { appPath: path.resolve("/src/agent-ayoeb/frontend") }],
		["another folder of the daily checkout", { appPath: path.join(homeDir, ".ao", "daily", "agent-ayoeb", "backend") }],
		["a folder next to the daily one", { appPath: path.join(homeDir, ".ao", "frontend") }],
	])("leaves %s alone", (_name, change) => {
		expect(dailyLinkForwardDir({ ...browserLaunch, ...change })).toBeNull();
	});
});

describe("deepLinkHost", () => {
	it("keeps codes and share tokens out of the log", () => {
		expect(deepLinkHost(link)).toBe("callback");
		expect(deepLinkHost("ao-app://share/org/secret-token")).toBe("share");
		expect(deepLinkHost("ao-app://billing")).toBe("billing");
	});
});
