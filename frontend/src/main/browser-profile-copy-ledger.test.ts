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
});
