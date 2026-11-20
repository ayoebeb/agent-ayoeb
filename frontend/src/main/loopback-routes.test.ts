import { describe, expect, it } from "vitest";
import { LoopbackRoutes } from "./loopback-routes";

function setup() {
	const applied: Array<[string, string | null]> = [];
	const started: string[] = [];
	const closed: string[] = [];
	const routes = new LoopbackRoutes({
		credential: { username: "ao", password: "pw" },
		startProxy: async (hostId) => {
			started.push(hostId);
			return { address: `127.0.0.1:${4000 + started.length}`, close: async () => void closed.push(hostId) };
		},
		applyProxy: async (partition, address) => void applied.push([partition, address]),
	});
	return { routes, applied, started, closed };
}

describe("loopback routes per partition", () => {
	it("routes a partition used only by sessions on one remote computer", async () => {
		const { routes, applied } = setup();
		expect(await routes.claim("p1", "s1", "h_a")).toEqual({ routed: true });
		expect(applied).toEqual([["p1", "127.0.0.1:4001"]]);
		expect(routes.routedHost("p1")).toBe("h_a");
	});

	it("never routes a local session", async () => {
		const { routes, applied, started } = setup();
		expect(await routes.claim("p1", "s1", null)).toEqual({ routed: false });
		expect(applied).toEqual([]);
		expect(started).toEqual([]);
	});

	it("leaves a partition shared with a local or other-host session unrouted", async () => {
		const { routes, applied } = setup();
		await routes.claim("p1", "remote", "h_a");
		expect(await routes.claim("p1", "local", null)).toEqual({ routed: false });
		expect(applied[applied.length - 1]).toEqual(["p1", null]);
		await routes.release("p1", "local");
		expect(routes.routedHost("p1")).toBe("h_a");
		expect(await routes.claim("p1", "other", "h_b")).toEqual({ routed: false });
	});

	it("shares one proxy per computer and retires it when no partition uses it", async () => {
		const { routes, started, closed } = setup();
		await routes.claim("p1", "s1", "h_a");
		await routes.claim("p2", "s2", "h_a");
		expect(started).toEqual(["h_a"]);
		await routes.release("p1", "s1");
		expect(closed).toEqual([]);
		await routes.release("p2", "s2");
		expect(closed).toEqual(["h_a"]);
	});

	it("answers a proxy challenge only for its own proxies", async () => {
		const { routes } = setup();
		await routes.claim("p1", "s1", "h_a");
		expect(routes.credentialFor("127.0.0.1", 4001)).toEqual({ username: "ao", password: "pw" });
		expect(routes.credentialFor("127.0.0.1", 4999)).toBeNull();
		expect(routes.credentialFor("proxy.corp", 4001)).toBeNull();
	});
});
