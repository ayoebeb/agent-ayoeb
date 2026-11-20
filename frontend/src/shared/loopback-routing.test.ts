import { describe, expect, it } from "vitest";
import { isLoopbackHostname, routeThroughHost, urlRoutesThroughHost } from "./loopback-routing";

describe("loopback routing decision", () => {
	it("sends loopback through the host only for a session that runs on another computer", () => {
		for (const host of ["localhost", "LOCALHOST", "app.localhost", "127.0.0.1", "127.9.9.9", "[::1]", "::1", "localhost."]) {
			expect(routeThroughHost("h_nlstation78", host), host).toBe(true);
			expect(routeThroughHost(undefined, host), `local ${host}`).toBe(false);
			expect(routeThroughHost(null, host), `local ${host}`).toBe(false);
			expect(routeThroughHost("", host), `local ${host}`).toBe(false);
		}
	});

	it("keeps every other destination on this PC network", () => {
		for (const host of ["example.com", "192.168.1.5", "10.0.0.2", "8.8.8.8", "0.0.0.0", "localhost.evil.com", "127.0.0.1.nip.io", "128.0.0.1", "127.0.0.256", "fe80::1"]) {
			expect(routeThroughHost("h_nlstation78", host), host).toBe(false);
		}
	});

	it("keeps the own preview origins of AO local even though they are *.localhost", () => {
		expect(isLoopbackHostname("ao-preview-0123456789abcdef0123456789abcdef.localhost")).toBe(true);
		expect(routeThroughHost("h_nlstation78", "ao-preview-0123456789abcdef0123456789abcdef.localhost")).toBe(false);
		expect(routeThroughHost("h_nlstation78", "ao-preview.abc.localhost")).toBe(false);
	});

	it("decides on the URL a tab shows", () => {
		expect(urlRoutesThroughHost("h_x", "http://localhost:5173/")).toBe(true);
		expect(urlRoutesThroughHost("h_x", "https://127.0.0.1:8443/app")).toBe(true);
		expect(urlRoutesThroughHost("h_x", "https://example.com/")).toBe(false);
		expect(urlRoutesThroughHost("h_x", "about:blank")).toBe(false);
		expect(urlRoutesThroughHost("h_x", "not a url")).toBe(false);
		expect(urlRoutesThroughHost(undefined, "http://localhost:5173/")).toBe(false);
	});
});
