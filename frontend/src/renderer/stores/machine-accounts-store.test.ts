import { beforeEach, describe, expect, it, vi } from "vitest";

const STORAGE_KEY = "ao.machines.claudeAccount";

async function freshStore() {
	vi.resetModules();
	return (await import("./machine-accounts-store")).useMachineAccountsStore;
}

describe("machine accounts store", () => {
	beforeEach(() => window.localStorage.clear());

	it("starts empty, remembers a choice per computer, and clears it back to not set", async () => {
		const store = await freshStore();
		expect(store.getState().byHost).toEqual({});
		store.getState().setChoice("box-a", "same");
		store.getState().setChoice("box-b", "different");
		expect(store.getState().byHost).toEqual({ "box-a": "same", "box-b": "different" });
		store.getState().setChoice("box-a", null);
		expect(store.getState().byHost).toEqual({ "box-b": "different" });
	});

	it("keeps the choices across a restart", async () => {
		const first = await freshStore();
		first.getState().setChoice("box-a", "different");
		expect(JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? "null")).toEqual({ "box-a": "different" });
		const second = await freshStore();
		expect(second.getState().byHost).toEqual({ "box-a": "different" });
	});

	it("drops anything it did not write itself", async () => {
		window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ a: "same", b: "bogus", c: 3, d: "different" }));
		expect((await freshStore()).getState().byHost).toEqual({ a: "same", d: "different" });
		window.localStorage.setItem(STORAGE_KEY, "[1,2]");
		expect((await freshStore()).getState().byHost).toEqual({});
		window.localStorage.setItem(STORAGE_KEY, "not json");
		expect((await freshStore()).getState().byHost).toEqual({});
	});
});
