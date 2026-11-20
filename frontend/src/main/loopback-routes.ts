import type { LoopbackProxy } from "./loopback-proxy";

// Which Browser partitions send their loopback traffic through another
// computer. Chromium's proxy setting belongs to a partition (an Electron
// session), not to a tab, and a profile's partition can be shared by sessions
// on different computers. So each session CLAIMS its partition with the host it
// runs on (null for this PC), and the partition is routed only while every
// claim on it names the same remote computer. A mixed partition stays local:
// a local session's localhost must never silently reach another machine.
export type LoopbackRouteDeps = {
	/** Starts the routing proxy for one remote computer. */
	startProxy: (hostId: string) => Promise<LoopbackProxy>;
	/** Points a partition's Chromium at a routing proxy, or back at the default (null). */
	applyProxy: (partition: string, proxyAddress: string | null) => Promise<void>;
	credential: { username: string; password: string };
};

export class LoopbackRoutes {
	private readonly claims = new Map<string, Map<string, string | null>>();
	private readonly applied = new Map<string, string>(); // partition -> hostId it is routed to
	private readonly proxies = new Map<string, Promise<LoopbackProxy>>();
	private readonly addresses = new Set<string>();
	private tail: Promise<unknown> = Promise.resolve();

	constructor(private readonly deps: LoopbackRouteDeps) {}

	/** Record that `sessionId` browses in `partition` and runs on `hostId` (null: this PC). */
	claim(partition: string, sessionId: string, hostId: string | null): Promise<{ routed: boolean }> {
		let claims = this.claims.get(partition);
		if (!claims) this.claims.set(partition, (claims = new Map()));
		claims.set(sessionId, hostId);
		return this.sync(partition);
	}

	/** The session left the partition (closed, or switched profile). */
	async release(partition: string, sessionId: string): Promise<void> {
		const claims = this.claims.get(partition);
		if (!claims?.delete(sessionId)) return;
		if (claims.size === 0) this.claims.delete(partition);
		await this.sync(partition);
	}

	/** The remote computer a partition's loopback currently goes through, if any. */
	routedHost(partition: string): string | null {
		return this.applied.get(partition) ?? null;
	}

	/**
	 * The credential for a proxy challenge from one of our proxies; null for any
	 * other (a corporate proxy asking for its own login must not get ours). Sync:
	 * the app's login handler has to claim the event before it returns.
	 */
	credentialFor(host: string, port: number): { username: string; password: string } | null {
		return host === "127.0.0.1" && this.addresses.has(`127.0.0.1:${port}`) ? this.deps.credential : null;
	}

	async closeAll(): Promise<void> {
		await this.tail;
		this.claims.clear();
		await Promise.all([...this.applied.keys()].map((partition) => this.sync(partition)));
	}

	private effective(partition: string): string | null {
		const hosts = new Set(this.claims.get(partition)?.values());
		const [only] = hosts;
		return hosts.size === 1 && typeof only === "string" ? only : null;
	}

	private sync(partition: string): Promise<{ routed: boolean }> {
		const run = this.tail.then(async () => {
			const want = this.effective(partition);
			const have = this.applied.get(partition) ?? null;
			if (want !== have) {
				if (want) {
					let proxy = this.proxies.get(want);
					if (!proxy) this.proxies.set(want, (proxy = this.deps.startProxy(want)));
					try {
						const { address } = await proxy;
						this.addresses.add(address);
						await this.deps.applyProxy(partition, address);
						this.applied.set(partition, want);
					} catch (error) {
						this.proxies.delete(want);
						throw error;
					}
				} else {
					await this.deps.applyProxy(partition, null);
					this.applied.delete(partition);
				}
				await this.retireUnusedProxies();
			}
			return { routed: this.applied.has(partition) };
		});
		this.tail = run.catch(() => undefined);
		return run;
	}

	private async retireUnusedProxies(): Promise<void> {
		const used = new Set(this.applied.values());
		for (const [hostId, pending] of [...this.proxies]) {
			if (used.has(hostId)) continue;
			this.proxies.delete(hostId);
			const proxy = await pending.catch(() => null);
			if (proxy) this.addresses.delete(proxy.address);
			await proxy?.close();
		}
	}
}
