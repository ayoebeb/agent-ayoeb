import type { AccountDirectoryHost } from "./browser-profile-directory";
import { parseReceiverTicket, rawKey, verifyReceiverTicket } from "./browser-profile-share-crypto";

/**
 * The sharing machine's check of who is asking for its browser profiles.
 * A request passes only when all of these hold:
 * - its ticket names this machine as the source and another as the receiver;
 * - this desktop's own AO Cloud session still lists the receiver on the
 *   account, so a machine removed from the account loses access;
 * - the receiver is listed at an https address, and the host key it serves
 *   there signed the ticket.
 * The answer is then sealed to the ticket's one-time key, which only the
 * receiver's desktop holds. A local process, a phone, or anything on the
 * path that holds a connection credential can satisfy none of this.
 */

export type VerifiedReceiver = { sourceHostId: string; receiverHostId: string; key: string };

export class BrowserProfileReceiverUnverifiedError extends Error {
	readonly code = "BROWSER_PROFILE_RECEIVER_UNVERIFIED";
	constructor() {
		super("The machine asking for browser profiles could not be verified.");
		this.name = "BrowserProfileReceiverUnverifiedError";
	}
}

export type BrowserProfileReceiverVerifierOptions = {
	localHostId: () => Promise<string | null>;
	/** This desktop's AO Cloud directory of the signed-in account. */
	accountHosts: () => Promise<AccountDirectoryHost[]>;
	fetch?: typeof fetch;
	now?: () => number;
};

/** How far a ticket's time may be from this machine's clock either way. */
const TICKET_SKEW_SECONDS = 10 * 60;
const HOST_KEY_TTL_MS = 10 * 60_000;
/** A failed check fetches the receiver's key again at most this often. */
const HOST_KEY_REFETCH_MS = 60_000;
const HOST_KEY_TIMEOUT_MS = 8_000;
const HOST_KEY_MAX_BYTES = 4_096;

/** The origin of an https address with no user info, or null. */
export function secureOrigin(url: string): string | null {
	try {
		const parsed = new URL(url);
		if (parsed.protocol !== "https:" || parsed.username || parsed.password) return null;
		return parsed.origin;
	} catch {
		return null;
	}
}

async function readCapped(response: Response, maxBytes: number): Promise<string> {
	const reader = response.body?.getReader();
	if (!reader) return "";
	const chunks: Uint8Array[] = [];
	let total = 0;
	for (;;) {
		const { done, value } = await reader.read();
		if (done) break;
		total += value.byteLength;
		if (total > maxBytes) {
			await reader.cancel().catch(() => undefined);
			throw new BrowserProfileReceiverUnverifiedError();
		}
		chunks.push(value);
	}
	return Buffer.concat(chunks).toString("utf8");
}

export class BrowserProfileReceiverVerifier {
	private readonly keys = new Map<string, { origin: string; key: string; at: number }>();
	private readonly fetchedAt = new Map<string, number>();
	private readonly fetchImpl: typeof fetch;
	private readonly now: () => number;

	constructor(private readonly options: BrowserProfileReceiverVerifierOptions) {
		this.fetchImpl = options.fetch ?? fetch;
		this.now = options.now ?? Date.now;
	}

	async verify(ticketValue: unknown, signature: unknown): Promise<VerifiedReceiver> {
		const ticket = parseReceiverTicket(ticketValue);
		if (!ticket || typeof ticketValue !== "string") throw new BrowserProfileReceiverUnverifiedError();
		const local = await this.options.localHostId();
		if (!local || ticket.sourceHostId !== local || ticket.receiverHostId === local) throw new BrowserProfileReceiverUnverifiedError();
		if (Math.abs(this.now() / 1_000 - ticket.issuedAt) > TICKET_SKEW_SECONDS) throw new BrowserProfileReceiverUnverifiedError();
		let hosts: AccountDirectoryHost[];
		try {
			hosts = await this.options.accountHosts();
		} catch {
			throw new BrowserProfileReceiverUnverifiedError();
		}
		const host = hosts.find((candidate) => candidate.hostId === ticket.receiverHostId);
		const origin = host ? secureOrigin(host.url) : null;
		if (!host || !origin) throw new BrowserProfileReceiverUnverifiedError();
		const verified: VerifiedReceiver = { sourceHostId: local, receiverHostId: host.hostId, key: ticket.key };
		const cached = this.keys.get(host.hostId);
		if (cached && cached.origin === origin && this.now() - cached.at < HOST_KEY_TTL_MS) {
			if (verifyReceiverTicket(ticketValue, signature, cached.key)) return verified;
			// The receiver may have a new host key; asking again is rate-limited,
			// so bad tickets cannot make this machine fetch on every request.
			if (this.now() - (this.fetchedAt.get(host.hostId) ?? 0) < HOST_KEY_REFETCH_MS) throw new BrowserProfileReceiverUnverifiedError();
		}
		const key = await this.fetchHostKey(host, origin);
		if (!verifyReceiverTicket(ticketValue, signature, key)) throw new BrowserProfileReceiverUnverifiedError();
		this.keys.set(host.hostId, { origin, key, at: this.now() });
		return verified;
	}

	/** The receiver's public host key, read from the https address the account lists for it. */
	private async fetchHostKey(host: AccountDirectoryHost, origin: string): Promise<string> {
		this.fetchedAt.set(host.hostId, this.now());
		try {
			const response = await this.fetchImpl(`${origin}/api/v1/remote-host/host-key`, {
				method: "GET",
				redirect: "error",
				headers: { Accept: "application/json", Authorization: `Bearer ${host.token}`, "X-AO-Expected-Host-ID": host.hostId },
				signal: AbortSignal.timeout(HOST_KEY_TIMEOUT_MS),
			});
			const text = await readCapped(response, HOST_KEY_MAX_BYTES);
			if (!response.ok) throw new BrowserProfileReceiverUnverifiedError();
			const body = JSON.parse(text) as { hostId?: unknown; hostKey?: unknown };
			if (body.hostId !== host.hostId || !rawKey(body.hostKey)) throw new BrowserProfileReceiverUnverifiedError();
			return body.hostKey as string;
		} catch {
			throw new BrowserProfileReceiverUnverifiedError();
		}
	}
}
