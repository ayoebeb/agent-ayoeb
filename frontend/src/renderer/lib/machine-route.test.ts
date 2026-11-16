import { describe, expect, it } from "vitest";
import { connectionRoute } from "./machine-route";

describe("connectionRoute", () => {
	it("recognizes the account tunnel and a quick tunnel", () => {
		expect(connectionRoute("https://h-4f2a.useao.dev")).toBe("tunnel");
		expect(connectionRoute("https://random-words.trycloudflare.com/ao")).toBe("tunnel");
		expect(connectionRoute("h-4f2a.useao.dev:443")).toBe("tunnel");
	});

	it("recognizes Tailscale by name and by its address range", () => {
		expect(connectionRoute("http://box.tail1234.ts.net:3001")).toBe("tailscale");
		expect(connectionRoute("http://100.101.2.3:3001")).toBe("tailscale");
		expect(connectionRoute("http://100.127.255.1")).toBe("tailscale");
	});

	it("recognizes a local network by private address, local name, or a bare host name", () => {
		for (const address of [
			"http://192.168.1.9:3001", "http://10.0.0.2", "http://172.16.0.1", "http://172.31.255.255", "http://169.254.1.1",
			"http://127.0.0.1:3071", "http://localhost:3001", "http://nlstation78:3001", "http://desk.local", "http://[::1]:3001", "http://[fd12::1]",
		]) expect(connectionRoute(address), address).toBe("lan");
	});

	it("falls back to the internet for a public address, and for text that is no address", () => {
		for (const address of ["https://example.com", "http://8.8.8.8", "http://172.32.0.1", "http://100.63.0.1", "http://[2001:db8::1]", "http://"]) {
			expect(connectionRoute(address), address).toBe("internet");
		}
	});
});
