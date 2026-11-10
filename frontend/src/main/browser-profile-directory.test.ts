import { describe, expect, it, vi } from "vitest";
import {
	AccountDirectory,
	AccountDirectoryUnavailableError,
	LinkedMachinesSignedOutError,
	parseAccountDirectory,
} from "./browser-profile-directory";

const TOKEN = "c".repeat(64);

function listing(hosts: unknown[]): string {
	return JSON.stringify({ hosts, fixedAddresses: true, hostKeys: true });
}

const HOSTS = [
	{ hostId: "h_this", label: "This machine", url: "https://this.example.test", token: TOKEN, updatedAt: "2026-11-10T00:00:00Z" },
	{ hostId: "h_brave", label: " Brave machine ", url: "https://brave.example.test", token: TOKEN, updatedAt: "2026-11-10T00:00:00Z" },
];

describe("parseAccountDirectory", () => {
	it("keeps well-formed machines and drops the rest", () => {
		expect(parseAccountDirectory(listing([
			...HOSTS,
			{ hostId: "h_bad_token", label: "x", url: "https://x.example.test", token: "pairing-password" },
			{ hostId: "../h", label: "x", url: "https://x.example.test", token: TOKEN },
			{ hostId: "h_brave", label: "duplicate", url: "https://evil.example.test", token: TOKEN },
			{ hostId: "h_no_label", url: "https://n.example.test", token: TOKEN },
			"nope",
		]))).toEqual([
			{ hostId: "h_this", label: "This machine", url: "https://this.example.test", token: TOKEN },
			{ hostId: "h_brave", label: "Brave machine", url: "https://brave.example.test", token: TOKEN },
			{ hostId: "h_no_label", label: "h_no_label", url: "https://n.example.test", token: TOKEN },
		]);
	});

	it("refuses an answer that is not a listing", () => {
		for (const body of ["{", "[]", JSON.stringify({ hosts: "x" }), "x".repeat(300 * 1024)]) {
			expect(() => parseAccountDirectory(body)).toThrow(AccountDirectoryUnavailableError);
		}
	});
});

describe("AccountDirectory", () => {
	it("reads nothing while signed out", async () => {
		const request = vi.fn(async () => ({ status: 200, body: listing(HOSTS) }));
		const directory = new AccountDirectory({ accountId: async () => undefined, request });
		await expect(directory.hosts()).rejects.toBeInstanceOf(LinkedMachinesSignedOutError);
		await expect(directory.linkedMachines("h_this")).rejects.toBeInstanceOf(LinkedMachinesSignedOutError);
		expect(request).not.toHaveBeenCalled();
	});

	it("lists the account's other machines with their account credential", async () => {
		const directory = new AccountDirectory({ accountId: async () => "user_1", request: async () => ({ status: 200, body: listing(HOSTS) }) });
		expect(await directory.linkedMachines("h_this")).toEqual([
			{ hostId: "h_brave", name: "Brave machine", url: "https://brave.example.test", credential: TOKEN },
		]);
	});

	it("reports AO Cloud failures as unavailable", async () => {
		for (const request of [
			async () => ({ status: 401, body: "" }),
			async () => ({ status: 200, body: "not json" }),
			async () => { throw new Error("offline"); },
		]) {
			await expect(new AccountDirectory({ accountId: async () => "user_1", request }).hosts()).rejects.toBeInstanceOf(AccountDirectoryUnavailableError);
		}
	});

	it("reuses a listing briefly, per account, and reads again after", async () => {
		let now = 0;
		let account = "user_1";
		const request = vi.fn(async () => ({ status: 200, body: listing(HOSTS) }));
		const directory = new AccountDirectory({ accountId: async () => account, request, cacheMs: 30_000, now: () => now });
		await Promise.all([directory.hosts(), directory.hosts()]);
		await directory.hosts();
		expect(request).toHaveBeenCalledTimes(1);
		account = "user_2";
		await directory.hosts();
		expect(request).toHaveBeenCalledTimes(2);
		now += 31_000;
		await directory.hosts();
		expect(request).toHaveBeenCalledTimes(3);
	});
});
