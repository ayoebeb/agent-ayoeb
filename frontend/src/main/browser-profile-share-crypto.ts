import {
	createCipheriv,
	createDecipheriv,
	createPublicKey,
	diffieHellman,
	generateKeyPairSync,
	hkdfSync,
	randomBytes,
	verify,
	type KeyObject,
} from "node:crypto";

/**
 * How two machines of one account move browser profiles so that only the
 * receiving machine can read them.
 *
 * The receiving desktop makes a one-time X25519 key and has its own daemon
 * sign a ticket naming it with the machine's Ed25519 host key (see
 * backend/internal/mobilebridge/browser_profile_ticket.go). The sharing
 * desktop checks that signature against the host key the receiver serves at
 * the https address AO Cloud lists for it, then seals every answer to the
 * ticket's key with AES-256-GCM. Its daemon relays ciphertext, so a process
 * holding only a connection credential, or anything on the path, gets nothing
 * it can read.
 */

/** Matches BrowserProfileTicketSignaturePrefix in the daemon. */
export const RECEIVER_TICKET_SIGNATURE_PREFIX = "ao-browser-profile-receiver-v1\n";
export const RECEIVER_TICKET_PURPOSE = "ao-browser-profile-share";
const SEAL_INFO = "ao-browser-profile-share-seal-v1";
const HOST_ID_PATTERN = /^[A-Za-z0-9_.:-]{1,128}$/;
const BASE64URL = /^[A-Za-z0-9_-]+$/;
const TICKET_MAX_LENGTH = 2_048;

export type ReceiverTicket = {
	v: 1;
	purpose: typeof RECEIVER_TICKET_PURPOSE;
	sourceHostId: string;
	receiverHostId: string;
	/** The receiver's one-time X25519 public key, unpadded base64url. */
	key: string;
	/** Unix seconds on the receiver's clock. */
	issuedAt: number;
};

export type SealedPayload = { version: 1; key: string; iv: string; data: string };

/** A one-time X25519 key; the private half never leaves this process. */
export type OneTimeKey = { publicKey: string; privateKey: KeyObject };

/** The raw 32-byte key as unpadded base64url, or null when it is not exactly that. */
export function rawKey(value: unknown): Buffer | null {
	if (typeof value !== "string" || value.length !== 43 || !BASE64URL.test(value)) return null;
	const raw = Buffer.from(value, "base64url");
	return raw.length === 32 && raw.toString("base64url") === value ? raw : null;
}

function okpKey(curve: "X25519" | "Ed25519", value: string): KeyObject {
	return createPublicKey({ key: { kty: "OKP", crv: curve, x: value }, format: "jwk" });
}

export function createOneTimeKey(): OneTimeKey {
	const { publicKey, privateKey } = generateKeyPairSync("x25519");
	const x = publicKey.export({ format: "jwk" }).x;
	if (typeof x !== "string") throw new Error("could not export a one-time key");
	return { publicKey: x, privateKey };
}

/** The ticket's payload, or null unless it has exactly the fields the daemon signs. */
export function parseReceiverTicket(ticket: unknown): ReceiverTicket | null {
	if (typeof ticket !== "string" || ticket.length < 16 || ticket.length > TICKET_MAX_LENGTH || !BASE64URL.test(ticket)) return null;
	let value: unknown;
	try {
		value = JSON.parse(Buffer.from(ticket, "base64url").toString("utf8"));
	} catch {
		return null;
	}
	if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
	const record = value as Record<string, unknown>;
	const keys = Object.keys(record).sort().join(",");
	if (keys !== "issuedAt,key,purpose,receiverHostId,sourceHostId,v") return null;
	if (
		record.v !== 1 || record.purpose !== RECEIVER_TICKET_PURPOSE ||
		typeof record.sourceHostId !== "string" || !HOST_ID_PATTERN.test(record.sourceHostId) ||
		typeof record.receiverHostId !== "string" || !HOST_ID_PATTERN.test(record.receiverHostId) ||
		record.sourceHostId === record.receiverHostId ||
		!rawKey(record.key) ||
		typeof record.issuedAt !== "number" || !Number.isSafeInteger(record.issuedAt) || record.issuedAt <= 0
	) {
		return null;
	}
	return record as ReceiverTicket;
}

/** Whether signature is hostKey's Ed25519 signature over the prefixed ticket. */
export function verifyReceiverTicket(ticket: string, signature: unknown, hostKey: string): boolean {
	if (typeof signature !== "string" || signature.length !== 86 || !BASE64URL.test(signature) || !rawKey(hostKey)) return false;
	const raw = Buffer.from(signature, "base64url");
	if (raw.length !== 64) return false;
	try {
		return verify(null, Buffer.from(RECEIVER_TICKET_SIGNATURE_PREFIX + ticket, "utf8"), okpKey("Ed25519", hostKey), raw);
	} catch {
		return false;
	}
}

/**
 * What a sealed answer is bound to: both machines and the request it answers.
 * A page sealed for one request does not open as the answer to another.
 */
export function sealContext(parts: {
	sourceHostId: string;
	receiverHostId: string;
	action: "list" | "export";
	profileId?: string;
	cursor?: string;
}): string {
	return [SEAL_INFO, parts.sourceHostId, parts.receiverHostId, parts.action, parts.profileId ?? "", parts.cursor ?? ""].join("\n");
}

function sealKey(shared: Buffer, senderKey: string, receiverKey: string, context: string): Buffer {
	// X25519 against a small-order point yields all zeros; never key with that.
	if (shared.every((byte) => byte === 0)) throw new Error("one-time key agreement failed");
	return Buffer.from(hkdfSync("sha256", shared, Buffer.from(`${senderKey}.${receiverKey}`, "utf8"), Buffer.from(context, "utf8"), 32));
}

/** Seals value's JSON so only the holder of receiverKey's private half can read it. */
export function sealPayload(value: unknown, receiverKey: string, context: string): SealedPayload {
	if (!rawKey(receiverKey)) throw new Error("the receiver's key is invalid");
	const sender = createOneTimeKey();
	const shared = diffieHellman({ privateKey: sender.privateKey, publicKey: okpKey("X25519", receiverKey) });
	const key = sealKey(shared, sender.publicKey, receiverKey, context);
	const iv = randomBytes(12);
	const cipher = createCipheriv("aes-256-gcm", key, iv);
	cipher.setAAD(Buffer.from(context, "utf8"));
	const data = Buffer.concat([cipher.update(JSON.stringify(value), "utf8"), cipher.final(), cipher.getAuthTag()]);
	return { version: 1, key: sender.publicKey, iv: iv.toString("base64url"), data: data.toString("base64url") };
}

export class SealedPayloadError extends Error {
	constructor() {
		super("The sealed answer could not be opened.");
		this.name = "SealedPayloadError";
	}
}

/** Opens a sealed answer with the one-time key it was sealed to; throws SealedPayloadError otherwise. */
export function openPayload(value: unknown, receiver: OneTimeKey, context: string): unknown {
	if (typeof value !== "object" || value === null || Array.isArray(value)) throw new SealedPayloadError();
	const { version, key: senderKey, iv, data } = value as Record<string, unknown>;
	if (
		version !== 1 || !rawKey(senderKey) ||
		typeof iv !== "string" || iv.length !== 16 || !BASE64URL.test(iv) ||
		typeof data !== "string" || data.length < 22 || !BASE64URL.test(data)
	) {
		throw new SealedPayloadError();
	}
	try {
		const shared = diffieHellman({ privateKey: receiver.privateKey, publicKey: okpKey("X25519", senderKey as string) });
		const sealed = Buffer.from(data, "base64url");
		const decipher = createDecipheriv("aes-256-gcm", sealKey(shared, senderKey as string, receiver.publicKey, context), Buffer.from(iv, "base64url"));
		decipher.setAAD(Buffer.from(context, "utf8"));
		decipher.setAuthTag(sealed.subarray(sealed.length - 16));
		const plain = Buffer.concat([decipher.update(sealed.subarray(0, sealed.length - 16)), decipher.final()]);
		return JSON.parse(plain.toString("utf8")) as unknown;
	} catch {
		throw new SealedPayloadError();
	}
}
