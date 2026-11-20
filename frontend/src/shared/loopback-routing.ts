// Which destinations the Browser panel of a session on ANOTHER computer sends
// through that computer. A dev server there (`npm run dev` on localhost:5173)
// is on its loopback, so this PC's own localhost would be the wrong machine.
// Everything that is not loopback keeps using this PC's network.
//
// Shared by main (the routing proxy) and the renderer
// (the "via <computer>" label), so both agree on what counts.

// AO's own preview origins (remote-proxy.ts: ao-preview-<hash>.localhost) are
// served by a proxy on THIS PC. They are loopback by name but must stay local.
const AO_PREVIEW_HOST = /^ao-preview(?:-|\.)/;

/** A host name or IP literal the browser treats as loopback, as `new URL().hostname` spells it. */
export function isLoopbackHostname(hostname: string): boolean {
	let host = hostname.trim().toLowerCase().replace(/\.+$/, "");
	if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
	if (host === "localhost" || host.endsWith(".localhost")) return true;
	if (host === "::1" || host === "0:0:0:0:0:0:0:1") return true;
	if (/^::ffff:(?:7f[0-9a-f]{2}:[0-9a-f]{1,4}|127\.\d+\.\d+\.\d+)$/.test(host)) return true;
	const octets = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
	return octets !== null && Number(octets[1]) === 127 && octets.slice(2).every((part) => Number(part) <= 255);
}

/** Loopback that belongs to the remote computer: loopback, minus AO's own local preview origins. */
export function isRoutableLoopbackHostname(hostname: string): boolean {
	return isLoopbackHostname(hostname) && !AO_PREVIEW_HOST.test(hostname.trim().toLowerCase());
}

/**
 * The routing decision. A destination goes through the remote computer only
 * when the session runs on one (`hostId` set) AND the destination is loopback.
 * A local session (no hostId) never routes, and no other host ever does.
 */
export function routeThroughHost(hostId: string | null | undefined, hostname: string): boolean {
	return typeof hostId === "string" && hostId !== "" && isRoutableLoopbackHostname(hostname);
}

/** Same decision for the URL a tab shows; false for anything that is not a URL. */
export function urlRoutesThroughHost(hostId: string | null | undefined, url: string): boolean {
	try {
		const parsed = new URL(url);
		return (parsed.protocol === "http:" || parsed.protocol === "https:") && routeThroughHost(hostId, parsed.hostname);
	} catch {
		return false;
	}
}
