import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { BROWSER_PROFILE_COPIES_FILE_NAME, BrowserProfileCopyLedger, BrowserProfileCopyLedgerError } from "./browser-profile-copy-ledger";

const A = "0f8fad5b-d9cb-469f-a165-70867728950e";
const B = "7c9e6679-7425-40de-944b-e07fc1f90ae7";
const SOURCE = {
	kind: "copy" as const,
	hostId: "h_laptop",
	machineName: "Laptop",
	remoteProfileId: B,
	copiedAt: "2026-11-10T12:00:00.000Z",
};
const tempDirectories: string[] = [];

async function stateDir(): Promise<string> {
	const directory = await mkdtemp(path.join(os.tmpdir(), "ao-browser-copies-"));
	tempDirectories.push(directory);
	return directory;
}

afterEach(async () => {
	await Promise.all(tempDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

describe("BrowserProfileCopyLedger", () => {
	it("starts empty and records copies durably", async () => {
		const dir = await stateDir();
		expect((await new BrowserProfileCopyLedger(dir).load()).ids.size).toBe(0);
		await new BrowserProfileCopyLedger(dir).record(A, SOURCE, [A]);
		const loaded = await new BrowserProfileCopyLedger(dir).load();
		expect([...loaded.ids]).toEqual([A]);
		expect(loaded.sources.get(A)).toEqual(SOURCE);
		expect(JSON.parse(await readFile(path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME), "utf8"))).toEqual({ version: 1, copies: { [A]: SOURCE } });
	});

	it("still withholds an entry whose provenance it cannot read", async () => {
		const dir = await stateDir();
		await writeFile(path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME), JSON.stringify({ version: 1, copies: { [A]: { kind: "copy", hostId: 5 }, "not-an-id": SOURCE } }));
		const loaded = await new BrowserProfileCopyLedger(dir).load();
		expect([...loaded.ids]).toEqual([A]);
		expect(loaded.sources.size).toBe(0);
		// A later write keeps the withheld id.
		await new BrowserProfileCopyLedger(dir).record(B, SOURCE, [A, B]);
		expect([...(await new BrowserProfileCopyLedger(dir).load()).ids].sort()).toEqual([A, B].sort());
	});

	it("fails closed on a damaged file and does not overwrite it", async () => {
		const dir = await stateDir();
		const file = path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME);
		for (const text of ["{ damaged", JSON.stringify({ version: 2, copies: {} }), JSON.stringify({ version: 1, copies: [] })]) {
			await writeFile(file, text);
			const ledger = new BrowserProfileCopyLedger(dir);
			await expect(ledger.load()).rejects.toBeInstanceOf(BrowserProfileCopyLedgerError);
			await expect(ledger.record(A, SOURCE, [A])).rejects.toBeInstanceOf(BrowserProfileCopyLedgerError);
			expect(await readFile(file, "utf8")).toBe(text);
		}
	});

	it("forgets profiles that no longer exist and removes an unfinished copy", async () => {
		const dir = await stateDir();
		const ledger = new BrowserProfileCopyLedger(dir);
		await ledger.record(A, SOURCE, [A]);
		await ledger.record(B, SOURCE, [B]);
		expect([...(await ledger.load()).ids]).toEqual([B]);
		await ledger.remove(B);
		expect((await ledger.load()).ids.size).toBe(0);
	});

	it("reports whether sharing can use it, and sets aside only a damaged file", async () => {
		const dir = await stateDir();
		const file = path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME);
		const kept = { expects: false };
		const ledger = new BrowserProfileCopyLedger(dir, { expectsCopyLedger: async () => kept.expects, markCopyLedger: async () => { kept.expects = true; } });
		expect(await ledger.health()).toBe("ok"); // never kept one
		await ledger.record(A, SOURCE, [A]);
		await ledger.setAsideDamaged();
		expect([...(await ledger.load()).ids]).toEqual([A]); // a readable ledger stays
		expect(await ledger.health()).toBe("ok");
		await rm(file);
		expect(await ledger.health()).toBe("lost");
		await writeFile(file, "{ damaged");
		expect(await ledger.health()).toBe("unreadable");
		await ledger.setAsideDamaged();
		await expect(readFile(file, "utf8")).rejects.toMatchObject({ code: "ENOENT" });
	});

	describe("ensure", () => {
		function marker(expects = false) {
			const state = { expects, marked: 0 };
			return {
				state,
				expectsCopyLedger: async () => state.expects,
				markCopyLedger: async () => { state.expects = true; state.marked += 1; },
			};
		}
		const profile = (id: string, source?: Record<string, unknown>) =>
			({ id, name: id, createdAt: "2026-11-10T12:00:00.000Z", updatedAt: "2026-11-10T12:00:00.000Z", ...(source ? { source } : {}) }) as never;

		it("starts a ledger on a machine that never kept one, and records that it does", async () => {
			const dir = await stateDir();
			const kept = marker(false);
			const copies = await new BrowserProfileCopyLedger(dir, kept).ensure([profile(A)]);
			expect(copies.ids.size).toBe(0);
			expect(JSON.parse(await readFile(path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME), "utf8"))).toEqual({ version: 1, copies: {} });
			expect(kept.state).toMatchObject({ expects: true, marked: 1 });
		});

		it("refuses when a ledger this machine kept is gone", async () => {
			const dir = await stateDir();
			await expect(new BrowserProfileCopyLedger(dir, marker(true)).ensure([])).rejects.toBeInstanceOf(BrowserProfileCopyLedgerError);
			await expect(readFile(path.join(dir, BROWSER_PROFILE_COPIES_FILE_NAME), "utf8")).rejects.toMatchObject({ code: "ENOENT" });
		});

		it("backfills copies and unreadable provenance from the registry, and leaves local profiles out", async () => {
			const dir = await stateDir();
			const ledger = new BrowserProfileCopyLedger(dir, marker(false));
			const C = "22222222-2222-4222-8222-222222222222";
			const copies = await ledger.ensure([profile(A, SOURCE), profile(B, { kind: "unknown" }), profile(C), profile("33333333-3333-4333-8333-333333333333", { kind: "import", browser: "Brave" })]);
			expect([...copies.ids].sort()).toEqual([A, B].sort());
			expect(copies.sources.get(A)).toEqual(SOURCE);
			const reloaded = await new BrowserProfileCopyLedger(dir).load();
			expect([...reloaded.ids].sort()).toEqual([A, B].sort());
		});
	});
});

