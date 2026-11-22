import { Agent, createServer, request as httpRequest, type IncomingHttpHeaders, type Server } from "node:http";
import { connect as netConnect, type AddressInfo, type Socket } from "node:net";
import type { Duplex } from "node:stream";
import { randomBytes, timingSafeEqual } from "node:crypto";
import { routeThroughHost } from "../shared/loopback-routing";

// A small HTTP proxy (CONNECT plus plain HTTP) on this PC's loopback that sends
// loopback destinations through another computer. One proxy serves one remote
// computer: the address a Browser partition's Chromium is pointed at therefore
// says WHICH computer, and a proxy cannot tell its callers apart otherwise.
//
// TLS and WebSockets stay end to end: a CONNECT, and every connection of a plain
// HTTP request, is a raw byte stream to the dev server over the stream
// `openStream` returns (a WebSocket to the remote daemon, see remote-proxy.ts).
// Destinations that are not loopback never leave this PC's network: the proxy
// dials them itself, because Chromium sends everything on a partition through
// the one proxy it is given.
//
// The proxy is loopback ambient authority to ANOTHER computer's loopback, so
// every request carries the credential only Chromium (via the app's "login"
// handler) knows, as remote-proxy.ts does with its path token.

/** Opens a byte stream to host:port on the remote computer's own loopback. */
export type OpenLoopbackStream = (host: string, port: number) => Promise<Duplex>;

export type LoopbackProxyOptions = {
	/** The remote computer this proxy is for. */
	hostId: string;
	openStream: OpenLoopbackStream;
	credential: { username: string; password: string };
	/** Dials a destination from this PC; injectable for tests. */
	dialDirect?: (host: string, port: number) => Socket;
};

export type LoopbackProxy = { address: string; close: () => Promise<void> };

const HOP_BY_HOP = ["connection", "proxy-connection", "proxy-authorization", "proxy-authenticate", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade"];

// net.Socket methods the HTTP agent calls on its connections; a plain stream has none.
function asSocketLike(stream: Duplex): Duplex {
	const noop = () => stream;
	return Object.assign(stream, { setNoDelay: noop, setKeepAlive: noop, ref: noop, unref: noop, setTimeout: noop });
}

class TunnelAgent extends Agent {
	constructor(private readonly open: (host: string, port: number) => Promise<Duplex>) {
		super({ keepAlive: true, maxSockets: 32 });
	}
	createConnection(options: { host?: string; hostname?: string; port?: number | string }, callback: (error: Error | null, socket?: Duplex) => void): undefined {
		const host = String(options.host ?? options.hostname ?? "");
		this.open(host, Number(options.port)).then((stream) => callback(null, asSocketLike(stream)), (error: Error) => callback(error));
		return undefined;
	}
}

function parseHostPort(authority: string): { host: string; port: number } | null {
	const match = /^(?:\[([^\]]+)\]|([^:]+)):(\d{1,5})$/.exec(authority);
	const port = match ? Number(match[3]) : 0;
	return match && port >= 1 && port <= 65535 ? { host: match[1] ?? match[2], port } : null;
}

export async function startLoopbackProxy(options: LoopbackProxyOptions): Promise<LoopbackProxy> {
	const expected = Buffer.from(`Basic ${Buffer.from(`${options.credential.username}:${options.credential.password}`).toString("base64")}`);
	const authorized = (headers: IncomingHttpHeaders): boolean => {
		const given = Buffer.from(String(headers["proxy-authorization"] ?? ""));
		return given.length === expected.length && timingSafeEqual(given, expected);
	};
	const routed = (host: string) => routeThroughHost(options.hostId, host);
	const dialDirect = options.dialDirect ?? ((host, port) => netConnect(port, host));
	const open = (host: string, port: number) => options.openStream(host, port);
	const agent = new TunnelAgent(open);
	const clients = new Set<{ destroy: () => void }>();

	const server: Server = createServer((req, res) => {
		if (!authorized(req.headers)) {
			res.writeHead(407, { "proxy-authenticate": 'Basic realm="ao"', connection: "close", "content-length": "0" });
			res.end();
			return;
		}
		let target: URL;
		try {
			target = new URL(req.url ?? "");
		} catch {
			res.writeHead(400).end();
			return;
		}
		// Plain HTTP only: HTTPS and WebSockets arrive as CONNECT.
		if (target.protocol !== "http:") {
			res.writeHead(400).end();
			return;
		}
		const headers = Object.fromEntries(Object.entries(req.headers).filter(([name]) => !HOP_BY_HOP.includes(name)));
		const forwarded = httpRequest(
			{
				host: target.hostname.replace(/^\[|\]$/g, ""),
				port: Number(target.port || 80),
				method: req.method,
				path: `${target.pathname}${target.search}`,
				headers,
				agent: routed(target.hostname) ? agent : false,
			},
			(upstream) => {
				const responseHeaders = Object.fromEntries(Object.entries(upstream.headers).filter(([name]) => !HOP_BY_HOP.includes(name)));
				res.writeHead(upstream.statusCode ?? 502, responseHeaders);
				upstream.pipe(res);
			},
		);
		forwarded.on("error", () => {
			if (!res.headersSent) res.writeHead(502, { "content-length": "0" });
			res.end();
		});
		res.once("close", () => {
			if (!res.writableEnded) forwarded.destroy();
		});
		req.pipe(forwarded);
	});
	server.on("connect", (req, clientSocket: Socket, head: Buffer) => {
		const track = { destroy: () => clientSocket.destroy() };
		clients.add(track);
		clientSocket.once("close", () => clients.delete(track));
		clientSocket.on("error", () => undefined);
		const refuse = (status: string) => clientSocket.end(`HTTP/1.1 ${status}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n`);
		if (!authorized(req.headers)) return void refuse('407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm="ao"');
		const destination = parseHostPort(req.url ?? "");
		if (!destination) return void refuse("400 Bad Request");
		const join = (upstream: Duplex) => {
			upstream.on("error", () => clientSocket.destroy());
			clientSocket.once("close", () => upstream.destroy());
			upstream.once("close", () => clientSocket.destroy());
			clientSocket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
			if (head.length > 0) upstream.write(head);
			upstream.pipe(clientSocket);
			clientSocket.pipe(upstream);
		};
		if (routed(destination.host)) {
			open(destination.host, destination.port).then(join, () => {
				if (!clientSocket.destroyed) refuse("502 Bad Gateway");
			});
			return;
		}
		const direct = dialDirect(destination.host, destination.port);
		direct.once("error", () => {
			if (!clientSocket.destroyed && !clientSocket.writableEnded) refuse("502 Bad Gateway");
		});
		direct.once("connect", () => join(direct));
	});
	server.on("upgrade", (_req, socket: Duplex) => socket.destroy());
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	const { port } = server.address() as AddressInfo;
	return {
		address: `127.0.0.1:${port}`,
		close: () =>
			new Promise<void>((resolve) => {
				agent.destroy();
				for (const client of clients) client.destroy();
				server.closeAllConnections();
				server.close(() => resolve());
			}),
	};
}

export const newLoopbackCredential = () => ({ username: "ao", password: randomBytes(24).toString("hex") });
