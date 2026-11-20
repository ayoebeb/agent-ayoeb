import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo, Socket } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import { WebSocketServer, createWebSocketStream } from "ws";
import { startRemoteProxy, type ActiveProxy } from "./remote-proxy";

// A stand-in for the remote daemon: the identity probe, plus the loopback
// tunnel (/api/v1/loopback-tunnel) that echoes bytes and refuses non-loopback.
let daemon: Server | undefined;
let proxy: ActiveProxy | undefined;
const upgrades: IncomingMessage[] = [];

async function startDaemon(): Promise<RemoteEntryForTest> {
	upgrades.length = 0;
	const wss = new WebSocketServer({ noServer: true, perMessageDeflate: false });
	daemon = createServer((req, res) => {
		if (req.url === "/api/v1/identity") {
			res.setHeader("content-type", "application/json");
			res.end(JSON.stringify({ hostId: "h_remote", apiVersion: 1 }));
			return;
		}
		res.writeHead(404).end();
	});
	daemon.on("upgrade", (req: IncomingMessage, socket: Socket, head: Buffer) => {
		upgrades.push(req);
		const url = new URL(req.url ?? "", "http://daemon");
		if (url.pathname !== "/api/v1/loopback-tunnel" || req.headers.authorization !== "Bearer secret12") {
			socket.end("HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n");
			return;
		}
		if (url.searchParams.get("host") !== "localhost") {
			socket.end("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n");
			return;
		}
		wss.handleUpgrade(req, socket, head, (ws) => {
			const stream = createWebSocketStream(ws);
			stream.on("error", () => undefined);
			stream.on("data", (chunk: Buffer) => stream.write(Buffer.concat([Buffer.from("echo:"), chunk])));
		});
	});
	await new Promise<void>((resolve) => daemon!.listen(0, "127.0.0.1", resolve));
	return { label: "box", url: `http://127.0.0.1:${(daemon.address() as AddressInfo).port}`, password: "secret12", hostId: "h_remote" };
}
type RemoteEntryForTest = { label: string; url: string; password: string; hostId: string };

afterEach(async () => {
	await proxy?.close();
	await new Promise((resolve) => (daemon ? daemon.close(resolve) : resolve(undefined)));
	daemon?.closeAllConnections();
	proxy = daemon = undefined;
});

describe("remote proxy loopback stream", () => {
	it("opens a stream to the host's loopback with the credential and carries bytes both ways", async () => {
		proxy = await startRemoteProxy(await startDaemon());
		const stream = await proxy.openLoopbackStream("localhost", 5173);
		const answer = new Promise<string>((resolve) => stream.once("data", (chunk) => resolve(String(chunk))));
		stream.write("hello");
		expect(await answer).toBe("echo:hello");
		const request = upgrades[upgrades.length - 1];
		expect(new URL(request.url ?? "", "http://x").searchParams.get("port")).toBe("5173");
		expect(request.headers.authorization).toBe("Bearer secret12");
		expect(request.headers["x-ao-expected-host-id"]).toBe("h_remote");
		stream.destroy();
	});

	it("reports the daemon's refusal of a non-loopback host", async () => {
		proxy = await startRemoteProxy(await startDaemon());
		await expect(proxy.openLoopbackStream("example.com", 80)).rejects.toThrow(/403/);
	});

	it("never opens a stream for an invalid port", async () => {
		proxy = await startRemoteProxy(await startDaemon());
		await expect(proxy.openLoopbackStream("localhost", 0)).rejects.toThrow(/invalid loopback port/);
		await expect(proxy.openLoopbackStream("localhost", 70000)).rejects.toThrow(/invalid loopback port/);
		expect(upgrades).toEqual([]);
	});
});
