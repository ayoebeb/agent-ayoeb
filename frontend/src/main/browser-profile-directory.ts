import { ACCOUNT_TOKEN } from "./remotes-store";

/**
 * The machines of the signed-in AO account, read from AO Cloud's host
 * directory with this desktop's session. Both ends of a browser profile copy
 * read it: the receiving machine to find its other machines and their
 * account credentials, and the sharing machine to confirm, on every request,
 * that the machine asking is still on the account. Removing a machine from
 * the account therefore ends its access to shared profiles, whatever
 * credential it kept.
 */

/** One machine of the account, as AO Cloud lists it. */
export type AccountDirectoryHost = { hostId: string; label: string; url: string; token: string };

/** A machine of the signed-in account, other than this one, with its account credential. */
export type LinkedMachine = { hostId: string; name: string; url: string; credential: string };

/** Linked machines belong to an AO account; signed out there are none to read. */
export class LinkedMachinesSignedOutError extends Error {
	constructor() {
		super("Sign in to Ayo Cloud to see your other machines.");
		this.name = "LinkedMachinesSignedOutError";
	}
}

/** AO Cloud did not answer with a usable directory. */
export class AccountDirectoryUnavailableError extends Error {
	constructor() {
		super("Ayo Cloud did not list this account's machines.");
		this.name = "AccountDirectoryUnavailableError";
	}
}

const HOST_ID_PATTERN = /^[A-Za-z0-9_.:-]{1,128}$/;
const LABEL_MAX_LENGTH = 64;
const MAX_HOSTS = 64;
const MAX_DIRECTORY_BYTES = 256 * 1024;

/** The directory's machines that are well-formed; anything else is dropped. */
export function parseAccountDirectory(body: string): AccountDirectoryHost[] {
	if (body.length > MAX_DIRECTORY_BYTES) throw new AccountDirectoryUnavailableError();
	let value: unknown;
	try {
		value = body ? JSON.parse(body) : {};
	} catch {
		throw new AccountDirectoryUnavailableError();
	}
	const hosts = typeof value === "object" && value !== null && Array.isArray((value as { hosts?: unknown }).hosts)
		? (value as { hosts: unknown[] }).hosts
		: null;
	if (!hosts) throw new AccountDirectoryUnavailableError();
	const seen = new Set<string>();
	return hosts.slice(0, MAX_HOSTS).flatMap((entry) => {
		if (typeof entry !== "object" || entry === null) return [];
		const { hostId, label, url, token } = entry as Record<string, unknown>;
		if (typeof hostId !== "string" || !HOST_ID_PATTERN.test(hostId) || seen.has(hostId)) return [];
		if (typeof url !== "string" || typeof token !== "string" || !ACCOUNT_TOKEN.test(token)) return [];
		seen.add(hostId);
		const name = typeof label === "string" && label.trim() ? label.trim().slice(0, LABEL_MAX_LENGTH) : hostId;
		return [{ hostId, label: name, url, token }];
	});
}

export type AccountDirectoryOptions = {
	/** The signed-in account's id, or nothing when signed out. */
	accountId: () => Promise<string | null | undefined>;
	/** GET /api/cloud/v1/me/hosts with this desktop's session. */
	request: (signal: AbortSignal) => Promise<{ status: number; body: string }>;
	timeoutMs?: number;
	/** How long one listing is reused; short, so a removed machine loses access quickly. */
	cacheMs?: number;
	now?: () => number;
};

export class AccountDirectory {
	private cached: { account: string; at: number; hosts: AccountDirectoryHost[] } | null = null;
	private pending: { account: string; hosts: Promise<AccountDirectoryHost[]> } | null = null;
	private readonly timeoutMs: number;
	private readonly cacheMs: number;
	private readonly now: () => number;

	constructor(private readonly options: AccountDirectoryOptions) {
		this.timeoutMs = options.timeoutMs ?? 10_000;
		this.cacheMs = options.cacheMs ?? 30_000;
		this.now = options.now ?? Date.now;
	}

	/** The account's machines; throws LinkedMachinesSignedOutError or AccountDirectoryUnavailableError. */
	async hosts(): Promise<AccountDirectoryHost[]> {
		const account = await this.options.accountId();
		if (!account) {
			this.cached = null;
			throw new LinkedMachinesSignedOutError();
		}
		const cached = this.cached;
		if (cached && cached.account === account && this.now() - cached.at < this.cacheMs) return cached.hosts;
		if (this.pending?.account === account) return this.pending.hosts;
		const pending = { account, hosts: this.load(account) };
		this.pending = pending;
		const clear = () => {
			if (this.pending === pending) this.pending = null;
		};
		pending.hosts.then(clear, clear);
		return pending.hosts;
	}

	private async load(account: string): Promise<AccountDirectoryHost[]> {
		let response: { status: number; body: string };
		try {
			response = await this.options.request(AbortSignal.timeout(this.timeoutMs));
		} catch {
			throw new AccountDirectoryUnavailableError();
		}
		if (response.status !== 200) throw new AccountDirectoryUnavailableError();
		const hosts = parseAccountDirectory(response.body);
		// A listing read for another account, or after signing out, is not kept.
		if (await this.options.accountId() === account) this.cached = { account, at: this.now(), hosts };
		return hosts;
	}

	/** The account's machines other than this one, as the receiving side copies from them. */
	async linkedMachines(localHostId: string | null): Promise<LinkedMachine[]> {
		return (await this.hosts())
			.filter((host) => host.hostId !== localHostId)
			.map((host) => ({ hostId: host.hostId, name: host.label, url: host.url, credential: host.token }));
	}
}
