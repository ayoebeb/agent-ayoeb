/**
 * How this computer reaches a linked one, worked out from its saved address.
 * The route matters because only a tunnel passes through a third party
 * (Cloudflare terminates its TLS); LAN and Tailscale traffic does not.
 */
export type ConnectionRoute = "tunnel" | "tailscale" | "lan" | "internet";

const TUNNEL_SUFFIXES = [".useao.dev", ".trycloudflare.com"];
const LAN_SUFFIXES = [".local", ".lan", ".home.arpa", ".internal"];

function ipv4Octets(hostname: string): [number, number, number, number] | null {
	const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(hostname);
	if (!match) return null;
	const octets = match.slice(1).map(Number);
	if (octets.some((octet) => octet > 255)) return null;
	return [octets[0], octets[1], octets[2], octets[3]];
}

export function connectionRoute(address: string): ConnectionRoute {
	let hostname: string;
	try {
		hostname = new URL(address.includes("://") ? address : `http://${address}`).hostname.toLowerCase();
	} catch {
		return "internet";
	}
	if (TUNNEL_SUFFIXES.some((suffix) => hostname.endsWith(suffix))) return "tunnel";
	if (hostname.endsWith(".ts.net")) return "tailscale";
	const octets = ipv4Octets(hostname);
	if (octets) {
		const [a, b] = octets;
		if (a === 100 && b >= 64 && b <= 127) return "tailscale";
		if (a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254)) return "lan";
		return "internet";
	}
	if (hostname.startsWith("[")) {
		const inner = hostname.slice(1, -1);
		return inner === "::1" || /^f[cd]/.test(inner) || /^fe[89ab]/.test(inner) ? "lan" : "internet";
	}
	if (hostname === "localhost" || !hostname.includes(".") || LAN_SUFFIXES.some((suffix) => hostname.endsWith(suffix))) return "lan";
	return "internet";
}
