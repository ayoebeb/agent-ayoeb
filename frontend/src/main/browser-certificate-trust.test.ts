import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
	CertificateTrustStore,
	normalizeFingerprint,
	normalizeHost,
} from "./browser-certificate-trust";
import { trustPreviewCertificate } from "./preview-certificate";

import { CERT_A, CERT_B, FINGERPRINT_A, FINGERPRINT_B } from "./browser-certificate-fixtures";
const HOST = "devtestayoeb.yoobi.ayoeb";
const REJECTED = "net::ERR_CERT_AUTHORITY_INVALID";

const dirs: string[] = [];
function stateDir(): string {
	const dir = mkdtempSync(path.join(os.tmpdir(), "ao-cert-trust-"));
	dirs.push(dir);
	return dir;
}
afterEach(() => {
	for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

describe("CertificateTrustStore", () => {
	it("matches only the exact host and fingerprint", () => {
		const store = new CertificateTrustStore();
		expect(store.trust(HOST, FINGERPRINT_A, "Yoobi CA")).toBe(true);
		expect(store.isTrusted(HOST, FINGERPRINT_A)).toBe(true);
		expect(store.isTrusted(HOST.toUpperCase(), FINGERPRINT_A.toLowerCase().replace(/:/g, ""))).toBe(true);
		expect(store.isTrusted(HOST, FINGERPRINT_B)).toBe(false);
		expect(store.isTrusted("other.yoobi.ayoeb", FINGERPRINT_A)).toBe(false);
		expect(store.isTrusted(`sub.${HOST}`, FINGERPRINT_A)).toBe(false);
		expect(store.isTrusted("yoobi.ayoeb", FINGERPRINT_A)).toBe(false);
		expect(store.isTrusted(HOST, "")).toBe(false);
		expect(store.isTrusted("", FINGERPRINT_A)).toBe(false);
	});

	it("rejects malformed fingerprints and does not store duplicates", () => {
		const store = new CertificateTrustStore();
		expect(store.trust(HOST, "not-a-fingerprint", "x")).toBe(false);
		expect(store.trust("", FINGERPRINT_A, "x")).toBe(false);
		store.trust(HOST, FINGERPRINT_A, "Yoobi CA");
		store.trust(HOST, FINGERPRINT_A.toLowerCase(), "Yoobi CA");
		expect(store.list()).toHaveLength(1);
		expect(normalizeFingerprint("zz")).toBe("");
		expect(normalizeHost(" Example.COM. ")).toBe("example.com");
	});

	it("removes exactly one entry", () => {
		const store = new CertificateTrustStore();
		store.trust(HOST, FINGERPRINT_A, "Yoobi CA");
		store.trust(HOST, FINGERPRINT_B, "Yoobi CA 2");
		expect(store.remove(HOST, FINGERPRINT_A)).toBe(true);
		expect(store.isTrusted(HOST, FINGERPRINT_A)).toBe(false);
		expect(store.isTrusted(HOST, FINGERPRINT_B)).toBe(true);
		expect(store.remove(HOST, FINGERPRINT_A)).toBe(false);
	});

	it("persists to its state directory and reloads", () => {
		const dir = stateDir();
		new CertificateTrustStore(dir).trust(HOST, FINGERPRINT_A, "Yoobi CA");
		const file = path.join(dir, "browser-trusted-certificates.json");
		expect(JSON.parse(readFileSync(file, "utf8")).entries).toEqual([
			expect.objectContaining({ host: HOST, fingerprint: FINGERPRINT_A, issuer: "Yoobi CA" }),
		]);
		const reloaded = new CertificateTrustStore(dir);
		expect(reloaded.isTrusted(HOST, FINGERPRINT_A)).toBe(true);
		reloaded.remove(HOST, FINGERPRINT_A);
		expect(new CertificateTrustStore(dir).list()).toEqual([]);
	});

	it("ignores a corrupt or foreign file", () => {
		const dir = stateDir();
		const file = path.join(dir, "browser-trusted-certificates.json");
		writeFileSync(file, "{not json");
		expect(new CertificateTrustStore(dir).list()).toEqual([]);
		writeFileSync(file, JSON.stringify({ version: 9, entries: [{ host: HOST, fingerprint: FINGERPRINT_A }] }));
		expect(new CertificateTrustStore(dir).list()).toEqual([]);
		writeFileSync(file, JSON.stringify({ version: 1, entries: [{ host: HOST, fingerprint: "bad" }, 7, { host: HOST, fingerprint: FINGERPRINT_B }] }));
		expect(new CertificateTrustStore(dir).list().map((entry) => entry.fingerprint)).toEqual([FINGERPRINT_B]);
	});
});

describe("CertificateTrustStore.decide", () => {
	it("keeps Chromium's result for an untrusted certificate and records why", () => {
		const store = new CertificateTrustStore();
		const decision = store.decide({ hostname: HOST, certificateData: CERT_A, verificationResult: REJECTED });
		expect(decision.result).toBe(-3);
		expect(decision.failure).toEqual({
			host: HOST,
			issuer: "Yoobi Test CA a",
			fingerprint: FINGERPRINT_A,
			error: "ERR_CERT_AUTHORITY_INVALID",
		});
		expect(store.failureFor(HOST)).toEqual(decision.failure);
	});

	it("accepts the trusted certificate for its host", () => {
		const store = new CertificateTrustStore();
		store.trust(HOST, FINGERPRINT_A, "Yoobi Test CA a");
		expect(store.decide({ hostname: HOST, certificateData: CERT_A, verificationResult: REJECTED }).result).toBe(0);
	});

	it("rejects a different certificate for the trusted host", () => {
		const store = new CertificateTrustStore();
		store.trust(HOST, FINGERPRINT_A, "Yoobi Test CA a");
		const decision = store.decide({ hostname: HOST, certificateData: CERT_B, verificationResult: REJECTED });
		expect(decision.result).toBe(-3);
		expect(decision.failure?.fingerprint).toBe(FINGERPRINT_B);
	});

	it("rejects the trusted certificate on another host", () => {
		const store = new CertificateTrustStore();
		store.trust(HOST, FINGERPRINT_A, "Yoobi Test CA a");
		for (const other of ["evil.example", `x.${HOST}`, "yoobi.ayoeb"]) {
			expect(store.decide({ hostname: other, certificateData: CERT_A, verificationResult: REJECTED }).result).toBe(-3);
		}
	});

	it("stops accepting once the entry is removed", () => {
		const store = new CertificateTrustStore();
		store.trust(HOST, FINGERPRINT_A, "Yoobi Test CA a");
		store.remove(HOST, FINGERPRINT_A);
		expect(store.decide({ hostname: HOST, certificateData: CERT_A, verificationResult: REJECTED }).result).toBe(-3);
	});

	it("does not record a failure for a valid certificate or unparsable data", () => {
		const store = new CertificateTrustStore();
		expect(store.decide({ hostname: HOST, certificateData: CERT_A, verificationResult: "net::OK" })).toEqual({ result: -3 });
		expect(store.decide({ hostname: HOST, certificateData: "garbage", verificationResult: REJECTED })).toEqual({ result: -3 });
		expect(store.failureFor(HOST)).toBeUndefined();
	});
});

describe("trustPreviewCertificate with user trust", () => {
	type VerifyProc = (
		request: { hostname: string; verificationResult: string; certificate: { data: string } },
		callback: (result: number) => void,
	) => void;

	it("accepts only the trusted pair through the session verify proc", () => {
		const procs: VerifyProc[] = [];
		const store = new CertificateTrustStore();
		store.trust(HOST, FINGERPRINT_A, "Yoobi Test CA a");
		trustPreviewCertificate({ setCertificateVerifyProc: (proc) => procs.push(proc as unknown as VerifyProc) }, store);
		const verdict = (hostname: string, data: string): number => {
			let result = Number.NaN;
			procs[0]({ hostname, verificationResult: REJECTED, certificate: { data } }, (value) => (result = value));
			return result;
		};
		expect(verdict(HOST, CERT_A)).toBe(0);
		expect(verdict(HOST, CERT_B)).toBe(-3);
		expect(verdict("example.com", CERT_A)).toBe(-3);
	});
});
