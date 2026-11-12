import { createHmac } from "node:crypto";

// Administrator terminals are local-desktop-only. The daemon accepts an
// elevated request only with this header, and only on its loopback listener
// from a request that does not name an agent session. The header's value is
// derived from the browser-runtime token the app hands its daemon over a
// private stdin pipe, so only this app and that daemon can compute it.
//
// The main process adds it, rather than the renderer holding it: agent pages
// framed in the chat, a compromised renderer script and remote hosts never see
// the value. It goes only to this computer's daemon, on its current port, and
// only on the routes that open, list or attach terminals.
export const DESKTOP_ELEVATION_HEADER = "X-AO-Desktop-Elevation";

const DERIVATION_LABEL = "ao-desktop-elevated-terminal-v1";
const LOOPBACK_HOSTS = new Set(["127.0.0.1", "localhost", "[::1]"]);
const TERMINAL_PATHS = ["/api/v1/shell-terminals", "/api/v1/terminal-shells", "/mux"];

/** Mirrors shellterm.DesktopElevationToken in the daemon. */
export function desktopElevationToken(runtimeToken: string): string {
	return createHmac("sha256", runtimeToken).update(DERIVATION_LABEL).digest("base64url");
}

/** Whether a request targets this computer daemon's terminal routes on its current port. */
export function isLocalDaemonTerminalRequest(url: string, daemonPort: number | undefined): boolean {
	if (!daemonPort) return false;
	let parsed: URL;
	try {
		parsed = new URL(url);
	} catch {
		return false;
	}
	if (parsed.protocol !== "http:" && parsed.protocol !== "ws:") return false;
	if (!LOOPBACK_HOSTS.has(parsed.hostname.toLowerCase()) || Number(parsed.port) !== daemonPort) return false;
	if (parsed.username || parsed.password) return false;
	return TERMINAL_PATHS.some((path) => parsed.pathname === path || parsed.pathname.startsWith(`${path}/`));
}

type FrameLike = { processId: number; routingId: number };

export type ElevationRequestDetails = {
	url: string;
	webContentsId?: number;
	frame?: FrameLike | null;
	requestHeaders: Record<string, string>;
};

export type ElevationHeaderTarget = {
	webContentsId: number;
	mainFrame: () => FrameLike | undefined;
	daemonPort: () => number | undefined;
	token: string;
};

/**
 * The request headers to send: the app window's own requests to its daemon's
 * terminal routes gain the credential; anything else (another window, a framed
 * agent page, a remote host's proxy) is sent as it was, with any credential a
 * page tried to set itself removed.
 */
export function withDesktopElevation(details: ElevationRequestDetails, target: ElevationHeaderTarget): Record<string, string> {
	const headers: Record<string, string> = {};
	for (const [name, value] of Object.entries(details.requestHeaders)) {
		if (name.toLowerCase() !== DESKTOP_ELEVATION_HEADER.toLowerCase()) headers[name] = value;
	}
	if (details.webContentsId !== target.webContentsId) return headers;
	// A request from a framed page carries its frame; only the app's own top
	// frame may vouch for the person at this computer.
	const main = target.mainFrame();
	if (details.frame && (!main || details.frame.processId !== main.processId || details.frame.routingId !== main.routingId)) {
		return headers;
	}
	if (!isLocalDaemonTerminalRequest(details.url, target.daemonPort())) return headers;
	headers[DESKTOP_ELEVATION_HEADER] = target.token;
	return headers;
}

type HeaderSession = {
	webRequest: {
		onBeforeSendHeaders(
			listener: (details: ElevationRequestDetails, callback: (response: { requestHeaders: Record<string, string> }) => void) => void,
		): void;
	};
};

/** Registers the header on the app window's session. Only one listener per session is possible. */
export function installDesktopElevationHeader(session: HeaderSession, target: ElevationHeaderTarget): void {
	session.webRequest.onBeforeSendHeaders((details, callback) => {
		callback({ requestHeaders: withDesktopElevation(details, target) });
	});
}
