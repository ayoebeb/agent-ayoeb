import { X509Certificate } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { BrowserCertificateError, BrowserTrustedCertificate } from "../shared/browser-certificate";

// "Trust this certificate" in the Browser panel. An entry is exactly one
// (host, SHA-256 fingerprint) pair: it never covers another host, another
// certificate for the same host, or anything outside AO's browser sessions.
// The Windows certificate store and Chromium's own verification stay untouched;
// the verify proc only overrides a rejection for a pair listed here.

const FILE_NAME = "browser-trusted-certificates.json";
const FILE_VERSION = 1;
const MAX_ENTRIES = 256;
const MAX_FAILURES = 64;
// net::OK from Chromium's verifier. Anything else is a rejection.
const NET_OK = "net::OK";

type TrustFile = { version: typeof FILE_VERSION; entries: BrowserTrustedCertificate[] };

export function normalizeHost(host: string): string {
	return host.trim().toLowerCase().replace(/\.$/, "");
}

// Lowercase hex without separators, or "" when the text is not a SHA-256.
export function normalizeFingerprint(fingerprint: string): string {
	const hex = fingerprint.replace(/[:\s]/g, "").toLowerCase();
	return /^[0-9a-f]{64}$/.test(hex) ? hex : "";
}

function displayFingerprint(fingerprint: string): string {
	return (normalizeFingerprint(fingerprint).toUpperCase().match(/.{2}/g) ?? []).join(":");
}

function parseCertificate(pem: string): X509Certificate | undefined {
	try {
		return new X509Certificate(pem);
	} catch {
		return undefined;
	}
}

function issuerLabel(certificate: X509Certificate): string {
	const fields = new Map<string, string>();
	for (const line of certificate.issuer.split("\n")) {
		const index = line.indexOf("=");
		if (index > 0 && !fields.has(line.slice(0, index))) fields.set(line.slice(0, index), line.slice(index + 1));
	}
	return fields.get("CN") ?? fields.get("O") ?? certificate.issuer.replace(/\n/g, ", ");
}

export type CertificateRequest = {
	hostname: string;
	certificateData: string;
	// Chromium's own result, e.g. "net::ERR_CERT_AUTHORITY_INVALID".
	verificationResult: string;
};

export type CertificateDecision = {
	// Electron's verify callback value: 0 accepts, -3 keeps Chromium's result.
	result: 0 | -3;
	// Set when the certificate was rejected, so the panel can explain it.
	failure?: BrowserCertificateError;
};

export class CertificateTrustStore {
	private entries: BrowserTrustedCertificate[] = [];
	private readonly failures = new Map<string, BrowserCertificateError>();

	// `stateDir` is under ~/.ao (never an OS app-data folder); `undefined` keeps
	// the store in memory only.
	constructor(private readonly stateDir?: string) {
		this.entries = this.readFile();
	}

	private get file(): string | undefined {
		return this.stateDir ? path.join(this.stateDir, FILE_NAME) : undefined;
	}

	private readFile(): BrowserTrustedCertificate[] {
		const file = this.file;
		if (!file) return [];
		try {
			return parseEntries(JSON.parse(readFileSync(file, "utf8")));
		} catch {
			return [];
		}
	}

	private writeFile(): void {
		const file = this.file;
		if (!file) return;
		const temporary = `${file}.${process.pid}.tmp`;
		try {
			mkdirSync(path.dirname(file), { recursive: true, mode: 0o750 });
			const body: TrustFile = { version: FILE_VERSION, entries: this.entries };
			writeFileSync(temporary, `${JSON.stringify(body, null, 2)}\n`, { mode: 0o600 });
			renameSync(temporary, file);
		} catch (error) {
			rmSync(temporary, { force: true });
			throw error;
		}
	}

	list(): BrowserTrustedCertificate[] {
		return this.entries.map((entry) => ({ ...entry }));
	}

	isTrusted(host: string, fingerprint: string): boolean {
		const wantHost = normalizeHost(host);
		const wantFingerprint = normalizeFingerprint(fingerprint);
		if (!wantHost || !wantFingerprint) return false;
		return this.entries.some(
			(entry) => entry.host === wantHost && normalizeFingerprint(entry.fingerprint) === wantFingerprint,
		);
	}

	trust(host: string, fingerprint: string, issuer: string, now: Date = new Date()): boolean {
		const hostName = normalizeHost(host);
		const hex = normalizeFingerprint(fingerprint);
		if (!hostName || !hex) return false;
		if (this.isTrusted(hostName, hex)) return true;
		if (this.entries.length >= MAX_ENTRIES) return false;
		const previous = this.entries;
		this.entries = [
			...previous,
			{ host: hostName, fingerprint: displayFingerprint(hex), issuer: issuer.slice(0, 256), trustedAt: now.toISOString() },
		];
		try {
			this.writeFile();
		} catch (error) {
			this.entries = previous;
			throw error;
		}
		return true;
	}

	remove(host: string, fingerprint: string): boolean {
		const hostName = normalizeHost(host);
		const hex = normalizeFingerprint(fingerprint);
		const previous = this.entries;
		this.entries = previous.filter(
			(entry) => !(entry.host === hostName && normalizeFingerprint(entry.fingerprint) === hex),
		);
		if (this.entries.length === previous.length) return false;
		try {
			this.writeFile();
		} catch (error) {
			this.entries = previous;
			throw error;
		}
		return true;
	}

	// The last certificate Chromium rejected for a host in this process.
	failureFor(host: string): BrowserCertificateError | undefined {
		return this.failures.get(normalizeHost(host));
	}

	// Decides one verification request. Only an exact (host, fingerprint) match
	// overrides Chromium; every other certificate keeps its result.
	decide(request: CertificateRequest): CertificateDecision {
		const certificate = parseCertificate(request.certificateData);
		if (!certificate) return { result: -3 };
		const fingerprint = displayFingerprint(certificate.fingerprint256);
		if (this.isTrusted(request.hostname, fingerprint)) return { result: 0 };
		if (request.verificationResult === NET_OK) return { result: -3 };
		const failure: BrowserCertificateError = {
			host: normalizeHost(request.hostname),
			issuer: issuerLabel(certificate),
			fingerprint,
			error: request.verificationResult.replace(/^net::/, ""),
		};
		this.failures.delete(failure.host);
		this.failures.set(failure.host, failure);
		while (this.failures.size > MAX_FAILURES) {
			const oldest = this.failures.keys().next().value;
			if (oldest === undefined) break;
			this.failures.delete(oldest);
		}
		return { result: -3, failure };
	}
}

function parseEntries(value: unknown): BrowserTrustedCertificate[] {
	if (typeof value !== "object" || value === null) return [];
	const { version, entries } = value as { version?: unknown; entries?: unknown };
	if (version !== FILE_VERSION || !Array.isArray(entries)) return [];
	const seen = new Set<string>();
	const result: BrowserTrustedCertificate[] = [];
	for (const raw of entries.slice(0, MAX_ENTRIES)) {
		if (typeof raw !== "object" || raw === null) continue;
		const { host, fingerprint, issuer, trustedAt } = raw as Record<string, unknown>;
		if (typeof host !== "string" || typeof fingerprint !== "string") continue;
		const hostName = normalizeHost(host);
		const hex = normalizeFingerprint(fingerprint);
		if (!hostName || !hex || seen.has(`${hostName}|${hex}`)) continue;
		seen.add(`${hostName}|${hex}`);
		result.push({
			host: hostName,
			fingerprint: displayFingerprint(hex),
			issuer: typeof issuer === "string" ? issuer.slice(0, 256) : "",
			trustedAt: typeof trustedAt === "string" ? trustedAt : "",
		});
	}
	return result;
}
