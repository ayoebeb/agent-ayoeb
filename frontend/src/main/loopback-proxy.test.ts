import { createServer, request, type Server } from "node:http";
import { connect, createServer as createNetServer, type AddressInfo, type Server as NetServer, type Socket } from "node:net";
import type { Duplex } from "node:stream";
import { afterEach, describe, expect, it } from "vitest";
import { startLoopbackProxy, type LoopbackProxy } from "./loopback-proxy";

const credential = { username: "ao", password: "pw" };
const basic = `Basic ${Buffer.from("ao:pw").toString("base64")}`;

// The fake transport stands in for the remote daemon loopback tunnel: it
// "dials" the dev server of this test and records what it was asked for.
let devServer: Server | undefined;
let proxy: LoopbackProxy | undefined;
let directServer: NetServer | undefined;
const opened: Array<[string, number]> = [];
const direct: Array<[string, number]> = [];

async function start(options: { failOpen?: boolean } = {}) {
	opened.length = 0;
	direct.length = 0;
	devServer = createServer((req, res) => res.end(`dev:${req.url}`));
	await new Promise<void>((resolve) => devServer!.listen(0, "127.0.0.1", resolve));
	const devPort = (devServer.address() as AddressInfo).port;
	directServer = createNetServer((socket) => socket.once("data", () => socket.end("direct-hello")));
	await new Promise<void>((resolve) => directServer!.listen(0, "127.0.0.1", resolve));
	const directPort = (directServer.address() as AddressInfo).port;
	proxy = await startLoopbackProxy({
		hostId: "h_remote",
		credential,
		openStream: async (host, port) => {
			opened.push([host, port]);
			if (options.failOpen) throw new Error("tunnel refused");
			return connect(devPort, "127.0.0.1") as Duplex;
		},
		// Never touches a real network: every direct dial lands on a local server.
		dialDirect: (host, port) => {
			direct.push([host, port]);
			return connect(directPort, "127.0.0.1");
		},
	});
}

afterEach(async () => {
	await proxy?.close();
	await new Promise((resolve) => (devServer ? devServer.close(resolve) : resolve(undefined)));
	await new Promise((resolve) => (directServer ? directServer.close(resolve) : resolve(undefined)));
	proxy = devServer = directServer = undefined;
});

const proxyHost = () => proxy!.address.split(":")[0];
const proxyPort = () => Number(proxy!.address.split(":")[1]);

function viaProxy(url: string, headers: Record<string, string> = { "proxy-authorization": basic }): Promise<{ status: number; body: string }> {
	return new Promise((resolve, reject) => {
		const req = request({ host: proxyHost(), port: proxyPort(), method: "GET", path: url, headers: { host: new URL(url).host, ...headers } }, (res) => {
			let body = "";
			res.on("data", (chunk) => (body += chunk));
			res.on("end", () => resolve({ status: res.statusCode ?? 0, body }));
		});
		req.on("error", reject);
		req.end();
	});
}

function connectVia(target: string, headers: Record<string, string> = { "proxy-authorization": basic }): Promise<{ status: number; socket: Socket }> {
	return new Promise((resolve, reject) => {
		const req = request({ host: proxyHost(), port: proxyPort(), method: "CONNECT", path: target, headers });
		req.on("connect", (res, socket) => resolve({ status: res.statusCode ?? 0, socket }));
		req.on("response", (res) => resolve({ status: res.statusCode ?? 0, socket: res.socket }));
		req.on("error", reject);
		req.end();
	});
}

const firstChunk = (socket: Socket) => new Promise<string>((resolve) => socket.once("data", (chunk) => resolve(String(chunk))));

describe("loopback routing proxy", () => {
	it("asks for the credential before doing anything", async () => {
		await start();
		expect((await viaProxy("http://localhost:5173/", {})).status).toBe(407);
		expect((await connectVia("localhost:5173", {})).status).toBe(407);
		expect(opened).toEqual([]);
	});

	it("carries a plain HTTP request to the remote loopback and keeps the connection alive", async () => {
		await start();
		expect(await viaProxy("http://localhost:5173/a")).toEqual({ status: 200, body: "dev:/a" });
		expect(await viaProxy("http://localhost:5173/b?x=1")).toEqual({ status: 200, body: "dev:/b?x=1" });
		expect(opened.every(([host, port]) => host === "localhost" && port === 5173)).toBe(true);
		expect(opened.length).toBe(1); // the second request reused the tunnel
		expect(direct).toEqual([]);
	});

	it("tunnels a CONNECT (TLS and WebSocket traffic) as raw bytes to the remote loopback", async () => {
		await start();
		const { status, socket } = await connectVia("127.0.0.1:5174");
		expect(status).toBe(200);
		const answer = firstChunk(socket);
		socket.write("GET /raw HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
		expect(await answer).toContain("dev:/raw");
		expect(opened).toEqual([["127.0.0.1", 5174]]);
		socket.destroy();
	});

	it("answers 502 when the remote cannot open the stream", async () => {
		await start({ failOpen: true });
		expect((await connectVia("localhost:5173")).status).toBe(502);
		expect((await viaProxy("http://localhost:5173/")).status).toBe(502);
	});

	it("dials everything that is not loopback from this PC, never through the remote", async () => {
		await start();
		const external = await connectVia("example.com:443");
		expect(external.status).toBe(200);
		const reply = firstChunk(external.socket);
		external.socket.write("ping");
		expect(await reply).toBe("direct-hello");
		external.socket.destroy();
		const preview = await connectVia("ao-preview-0123456789abcdef0123456789abcdef.localhost:55000");
		expect(preview.status).toBe(200);
		preview.socket.destroy();
		expect(direct).toEqual([["example.com", 443], ["ao-preview-0123456789abcdef0123456789abcdef.localhost", 55000]]);
		expect(opened).toEqual([]);
	});

	it("refuses a malformed CONNECT target", async () => {
		await start();
		expect((await connectVia("no-port")).status).toBe(400);
		expect((await connectVia("localhost:99999")).status).toBe(400);
		expect(opened).toEqual([]);
	});
});
