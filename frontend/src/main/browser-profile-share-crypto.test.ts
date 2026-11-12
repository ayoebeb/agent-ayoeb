import { generateKeyPairSync, sign } from "node:crypto";
import { describe, expect, it } from "vitest";
import {
	RECEIVER_TICKET_SIGNATURE_PREFIX,
	createOneTimeKey,
	openPayload,
	parseReceiverTicket,
	sealContext,
	sealPayload,
	verifyReceiverTicket,
} from "./browser-profile-share-crypto";

// Made by the daemon's own IssueBrowserProfileTicket from a fixed host key
// seed (bytes 1..32) and time, so this proves Go signs what TypeScript checks.
const GO_VECTOR = {
	hostKey: "ebVWLo_mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ",
	ticket: "eyJ2IjoxLCJwdXJwb3NlIjoiYW8tYnJvd3Nlci1wcm9maWxlLXNoYXJlIiwic291cmNlSG9zdElkIjoiaF9zb3VyY2UiLCJyZWNlaXZlckhvc3RJZCI6ImhfcmVjZWl2ZXIiLCJrZXkiOiJxODN2RWpSV2VKQ3J6ZThTTkZaNGtLdk43eEkwVm5pUXE4M3ZFalJXZUpBIiwiaXNzdWVkQXQiOjE3OTQzMjU2NTl9",
	signature: "PExp_OKuPGOEtlPu9fEtVjVLaIkgDpMtThj67aQkIewMlUA8WUiChRRwsaNGmA0mnSQIJWETsdudJ1WHikFFDg",
};

const CONTEXT = sealContext({ sourceHostId: "h_source", receiverHostId: "h_receiver", action: "export", profileId: "p", cursor: "" });

function ticketFor(payload: Record<string, unknown>): string {
	return Buffer.from(JSON.stringify(payload)).toString("base64url");
}

const VALID_PAYLOAD = {
	v: 1,
	purpose: "ao-browser-profile-share",
	sourceHostId: "h_source",
	receiverHostId: "h_receiver",
	key: "q83vEjRWeJCrze8SNFZ4kKvN7xI0VniQq83vEjRWeJA",
	issuedAt: 1_794_325_659,
};

describe("receiver tickets", () => {
	it("verifies a ticket the daemon signed, and nothing else", () => {
		expect(parseReceiverTicket(GO_VECTOR.ticket)).toEqual(VALID_PAYLOAD);
		expect(verifyReceiverTicket(GO_VECTOR.ticket, GO_VECTOR.signature, GO_VECTOR.hostKey)).toBe(true);
		const other = generateKeyPairSync("ed25519").publicKey.export({ format: "jwk" }).x!;
		expect(verifyReceiverTicket(GO_VECTOR.ticket, GO_VECTOR.signature, other)).toBe(false);
		expect(verifyReceiverTicket(`${GO_VECTOR.ticket}x`, GO_VECTOR.signature, GO_VECTOR.hostKey)).toBe(false);
		expect(verifyReceiverTicket(GO_VECTOR.ticket, `${GO_VECTOR.signature.slice(0, -1)}A`, GO_VECTOR.hostKey)).toBe(false);
		expect(verifyReceiverTicket(GO_VECTOR.ticket, "short", GO_VECTOR.hostKey)).toBe(false);
	});

	it("needs the signature's prefix, so no other signature of the host key passes", () => {
		const { privateKey, publicKey } = generateKeyPairSync("ed25519");
		const hostKey = publicKey.export({ format: "jwk" }).x!;
		const ticket = ticketFor(VALID_PAYLOAD);
		const bare = sign(null, Buffer.from(ticket), privateKey).toString("base64url");
		const prefixed = sign(null, Buffer.from(RECEIVER_TICKET_SIGNATURE_PREFIX + ticket), privateKey).toString("base64url");
		expect(verifyReceiverTicket(ticket, bare, hostKey)).toBe(false);
		expect(verifyReceiverTicket(ticket, prefixed, hostKey)).toBe(true);
	});

	it("parses only the exact fields the daemon signs", () => {
		for (const payload of [
			{ ...VALID_PAYLOAD, v: 2 },
			{ ...VALID_PAYLOAD, purpose: "something-else" },
			{ ...VALID_PAYLOAD, receiverHostId: "h_source" },
			{ ...VALID_PAYLOAD, sourceHostId: "h/../x" },
			{ ...VALID_PAYLOAD, key: "short" },
			{ ...VALID_PAYLOAD, issuedAt: 1.5 },
			{ ...VALID_PAYLOAD, extra: true },
		]) {
			expect(parseReceiverTicket(ticketFor(payload)), JSON.stringify(payload)).toBeNull();
		}
		const { key: _key, ...missing } = VALID_PAYLOAD;
		expect(parseReceiverTicket(ticketFor(missing))).toBeNull();
		expect(parseReceiverTicket("not base64url!")).toBeNull();
		expect(parseReceiverTicket(Buffer.from("not json").toString("base64url"))).toBeNull();
		expect(parseReceiverTicket(42)).toBeNull();
	});
});

describe("sealed payloads", () => {
	it("open only with the receiver's one-time key and the same context", () => {
		const receiver = createOneTimeKey();
		const sealed = sealPayload({ cookies: [{ value: "dummy-cookie-value" }] }, receiver.publicKey, CONTEXT);
		expect(JSON.stringify(sealed)).not.toContain("dummy-cookie-value");
		expect(openPayload(sealed, receiver, CONTEXT)).toEqual({ cookies: [{ value: "dummy-cookie-value" }] });
		expect(() => openPayload(sealed, createOneTimeKey(), CONTEXT)).toThrow();
		expect(() => openPayload(sealed, receiver, sealContext({ sourceHostId: "h_source", receiverHostId: "h_receiver", action: "list" }))).toThrow();
	});

	it("refuse anything tampered with or malformed", () => {
		const receiver = createOneTimeKey();
		const sealed = sealPayload({ ok: true }, receiver.publicKey, CONTEXT);
		const flipped = Buffer.from(sealed.data, "base64url");
		flipped[0] = flipped[0]! ^ 1;
		for (const value of [
			{ ...sealed, data: flipped.toString("base64url") },
			{ ...sealed, iv: Buffer.alloc(12).toString("base64url") },
			{ ...sealed, key: createOneTimeKey().publicKey },
			{ ...sealed, version: 2 },
			{ ...sealed, data: "" },
			{ ok: true },
			"sealed",
		]) {
			expect(() => openPayload(value, receiver, CONTEXT)).toThrow();
		}
	});

	it("uses a new sender key and nonce for every answer", () => {
		const receiver = createOneTimeKey();
		const first = sealPayload({ same: true }, receiver.publicKey, CONTEXT);
		const second = sealPayload({ same: true }, receiver.publicKey, CONTEXT);
		expect(first.key).not.toBe(second.key);
		expect(first.iv).not.toBe(second.iv);
		expect(first.data).not.toBe(second.data);
	});

	it("refuses to seal to a key that is not a raw X25519 public key", () => {
		expect(() => sealPayload({}, "short", CONTEXT)).toThrow();
	});
});
