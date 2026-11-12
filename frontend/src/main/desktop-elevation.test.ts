import { describe, expect, it } from "vitest";
import {
	DESKTOP_ELEVATION_HEADER,
	createDesktopElevationSecret,
	isLocalDaemonTerminalRequest,
	withDesktopElevation,
	type ElevationHeaderTarget,
} from "./desktop-elevation";

const mainFrame = { processId: 7, routingId: 1 };
const target: ElevationHeaderTarget = {
	webContentsId: 3,
	mainFrame: () => mainFrame,
	daemonPort: () => 3011,
	token: "credential",
};

function headersFor(details: Partial<Parameters<typeof withDesktopElevation>[0]>) {
	return withDesktopElevation(
		{ url: "http://127.0.0.1:3011/api/v1/shell-terminals", webContentsId: 3, frame: mainFrame, requestHeaders: {}, ...details },
		target,
	);
}

describe("desktop elevation credential", () => {
	it("is a fresh 32-byte secret for each app run, not derived from anything", () => {
		const first = createDesktopElevationSecret();
		const second = createDesktopElevationSecret();
		expect(first).toMatch(/^[A-Za-z0-9_-]{43}$/);
		expect(Buffer.from(first, "base64url")).toHaveLength(32);
		expect(second).not.toBe(first);
	});

	it("goes only to this computer's daemon terminal routes on its current port", () => {
		expect(isLocalDaemonTerminalRequest("http://127.0.0.1:3011/api/v1/shell-terminals", 3011)).toBe(true);
		expect(isLocalDaemonTerminalRequest("http://localhost:3011/api/v1/terminal-shells", 3011)).toBe(true);
		expect(isLocalDaemonTerminalRequest("ws://127.0.0.1:3011/mux", 3011)).toBe(true);
		expect(isLocalDaemonTerminalRequest("http://[::1]:3011/api/v1/shell-terminals/shellterm-admin-1", 3011)).toBe(true);
		// Another port is a remote host's loopback proxy, not this daemon.
		expect(isLocalDaemonTerminalRequest("http://127.0.0.1:49152/api/v1/shell-terminals", 3011)).toBe(false);
		expect(isLocalDaemonTerminalRequest("http://192.168.1.20:3011/api/v1/shell-terminals", 3011)).toBe(false);
		expect(isLocalDaemonTerminalRequest("https://host.trycloudflare.com/api/v1/shell-terminals", 3011)).toBe(false);
		expect(isLocalDaemonTerminalRequest("http://ao-1.preview.localhost:3011/api/v1/shell-terminals", 3011)).toBe(false);
		expect(isLocalDaemonTerminalRequest("http://127.0.0.1:3011/api/v1/sessions", 3011)).toBe(false);
		expect(isLocalDaemonTerminalRequest("http://127.0.0.1:3011/api/v1/shell-terminalsx", 3011)).toBe(false);
		expect(isLocalDaemonTerminalRequest("http://127.0.0.1:3011/mux", undefined)).toBe(false);
	});

	it("is added to the app window's own requests", () => {
		expect(headersFor({})[DESKTOP_ELEVATION_HEADER]).toBe("credential");
		expect(headersFor({ frame: null, url: "ws://127.0.0.1:3011/mux" })[DESKTOP_ELEVATION_HEADER]).toBe("credential");
	});

	it("is never given to framed agent pages, other windows or remote hosts", () => {
		expect(headersFor({ frame: { processId: 9, routingId: 4 } })[DESKTOP_ELEVATION_HEADER]).toBeUndefined();
		expect(headersFor({ webContentsId: 8 })[DESKTOP_ELEVATION_HEADER]).toBeUndefined();
		expect(headersFor({ url: "http://127.0.0.1:49152/api/v1/shell-terminals" })[DESKTOP_ELEVATION_HEADER]).toBeUndefined();
	});

	it("strips a credential a page tried to send itself", () => {
		const headers = headersFor({
			frame: { processId: 9, routingId: 4 },
			requestHeaders: { "x-ao-desktop-elevation": "forged", Accept: "application/json" },
		});
		expect(headers).toEqual({ Accept: "application/json" });
	});
});
