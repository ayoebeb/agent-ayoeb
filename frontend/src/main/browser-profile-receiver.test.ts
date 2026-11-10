import { generateKeyPairSync, sign, type KeyObject } from "node:crypto";
import { describe, expect, it, vi } from "vitest";
import type { AccountDirectoryHost } from "./browser-profile-directory";
import { BrowserProfileReceiverVerifier, secureOrigin } from "./browser-profile-receiver";
import { RECEIVER_TICKET_PURPOSE, RECEIVER_TICKET_SIGNATURE_PREFIX, createOneTimeKey } from "./browser-profile-share-crypto";

const NOW = Date.parse("2026-11-10T12:00:00.000Z");
const SOURCE = "h_source";
const RECEIVER = "h_receiver";
const TOKEN = "b".repeat(64);

function hostKeyPair() {
	const { privateKey, publicKey } = generateKeyPairSync("ed25519");
	return { privateKey, publicKey: publicKey.export({ format: "jwk" }).x! };
}

function signedTicket(privateKey: KeyObject, overrides: Record<string, unknown> = {}) {
	const payload = {
		v: 1,
		purpose: RECEIVER_TICKET_PURPOSE,
		sourceHostId: SOURCE,
		receiverHostId: RECEIVER,
		key: createOneTimeKey().publicKey,
		issuedAt: Math.floor(NOW / 1_000),
		...overrides,
	};
	const ticket = Buffer.from(JSON.stringify(payload)).toString("base64url");
	const signature = sign(null, Buffer.from(RECEIVER_TICKET_SIGNATURE_PREFIX + ticket), privateKey).toString("base64url");
	return { ticket, signature, key: payload.key as string };
}

function setup(options: { hosts?: AccountDirectoryHost[] | (() => Promise<AccountDirectoryHost[]>); servedKey?: () => string; servedHostId?: string; status?: number } = {}) {
	const keys = hostKeyPair();
	let served = keys.publicKey;
	let now = NOW;
	const fetchImpl = vi.fn(async (input: string | URL | Request, init: RequestInit = {}) => {
		const headers = new Headers(init.headers);
		if (headers.get("authorization") !== `Bearer ${TOKEN}` || headers.get("x-ao-expected-host-id") !== RECEIVER) {
			return new Response(JSON.stringify({ code: "BAD_PASSWORD" }), { status: 401 });
		}
		return new Response(JSON.stringify({ hostId: options.servedHostId ?? RECEIVER, hostKey: options.servedKey?.() ?? served, url: String(input) }), { status: options.status ?? 200 });
	});
	const hosts = options.hosts ?? [{ hostId: RECEIVER, label: "Receiver", url: "https://receiver.example.test", token: TOKEN }];
	const verifier = new BrowserProfileReceiverVerifier({
		localHostId: async () => SOURCE,
		accountHosts: typeof hosts === "function" ? hosts : async () => hosts,
		fetch: fetchImpl as unknown as typeof fetch,
		now: () => now,
	});
	return {
		keys,
		verifier,
		fetchImpl,
		rotate: () => { const next = hostKeyPair(); served = next.publicKey; return next; },
		advance: (ms: number) => { now += ms; },
	};
}

describe("secureOrigin", () => {
	it("accepts only https origins without user info", () => {
		expect(secureOrigin("https://receiver.example.test/")).toBe("https://receiver.example.test");
		for (const url of ["http://receiver.example.test", "receiver.example.test:3011", "https://u:p@receiver.example.test", "wss://receiver.example.test", ""]) {
			expect(secureOrigin(url), url).toBeNull();
		}
	});
});

describe("BrowserProfileReceiverVerifier", () => {
	it("accepts a ticket the listed receiver's host key signed, reading the key from its https address", async () => {
		const { keys, verifier, fetchImpl } = setup();
		const ticket = signedTicket(keys.privateKey);
		expect(await verifier.verify(ticket.ticket, ticket.signature)).toEqual({ sourceHostId: SOURCE, receiverHostId: RECEIVER, key: ticket.key });
		expect(fetchImpl).toHaveBeenCalledTimes(1);
		expect(String(fetchImpl.mock.calls[0]?.[0])).toBe("https://receiver.example.test/api/v1/remote-host/host-key");
		expect(fetchImpl.mock.calls[0]?.[1]).toMatchObject({ redirect: "error" });
		// The key is kept for a while: a copy's pages do not each fetch it.
		const next = signedTicket(keys.privateKey);
		await verifier.verify(next.ticket, next.signature);
		expect(fetchImpl).toHaveBeenCalledTimes(1);
	});

	// Finding M2: removing a machine from the account ends its access, whatever
	// credential it kept, because the sharing machine reads its own listing.
	it("refuses a machine the account no longer lists", async () => {
		const { keys, verifier, fetchImpl } = setup({ hosts: [] });
		const ticket = signedTicket(keys.privateKey);
		await expect(verifier.verify(ticket.ticket, ticket.signature)).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
		expect(fetchImpl).not.toHaveBeenCalled();
	});

	it("refuses when the account listing cannot be read, such as when signed out", async () => {
		const { keys, verifier } = setup({ hosts: async () => { throw new Error("signed out"); } });
		const ticket = signedTicket(keys.privateKey);
		await expect(verifier.verify(ticket.ticket, ticket.signature)).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
	});

	it("refuses a receiver listed without an https address, without contacting it", async () => {
		const { keys, verifier, fetchImpl } = setup({ hosts: [{ hostId: RECEIVER, label: "Receiver", url: "http://192.0.2.20:3011", token: TOKEN }] });
		const ticket = signedTicket(keys.privateKey);
		await expect(verifier.verify(ticket.ticket, ticket.signature)).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
		expect(fetchImpl).not.toHaveBeenCalled();
	});

	// Finding H1: a local process can read this machine's own host key and
	// connection credentials, but it cannot sign as another machine.
	it("refuses tickets signed by any other key, naming this machine as receiver, or for another source", async () => {
		const { keys, verifier } = setup();
		const stranger = hostKeyPair();
		for (const ticket of [
			signedTicket(stranger.privateKey),
			signedTicket(keys.privateKey, { receiverHostId: SOURCE, sourceHostId: "h_other" }),
			signedTicket(keys.privateKey, { sourceHostId: "h_other" }),
			signedTicket(keys.privateKey, { issuedAt: Math.floor(NOW / 1_000) - 11 * 60 }),
			signedTicket(keys.privateKey, { issuedAt: Math.floor(NOW / 1_000) + 11 * 60 }),
		]) {
			await expect(verifier.verify(ticket.ticket, ticket.signature)).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
		}
		await expect(verifier.verify(undefined, undefined)).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
	});

	it("refuses a host key served for another machine or with an error", async () => {
		for (const options of [{ servedHostId: "h_someone_else" }, { status: 503 }, { servedKey: () => "short" }]) {
			const { keys, verifier } = setup(options);
			const ticket = signedTicket(keys.privateKey);
			await expect(verifier.verify(ticket.ticket, ticket.signature), JSON.stringify(options)).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
		}
	});

	it("follows a receiver's new host key, but bad tickets cannot make it fetch on every request", async () => {
		const { keys, verifier, fetchImpl, rotate, advance } = setup();
		const first = signedTicket(keys.privateKey);
		await verifier.verify(first.ticket, first.signature);
		const rotated = rotate();
		const fresh = signedTicket(rotated.privateKey);
		// Within a minute of the last fetch, a ticket the cached key rejects is refused without fetching.
		await expect(verifier.verify(fresh.ticket, fresh.signature)).rejects.toMatchObject({ code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED" });
		expect(fetchImpl).toHaveBeenCalledTimes(1);
		advance(61_000);
		expect(await verifier.verify(fresh.ticket, fresh.signature)).toMatchObject({ receiverHostId: RECEIVER });
		expect(fetchImpl).toHaveBeenCalledTimes(2);
	});
});
