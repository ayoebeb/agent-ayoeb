// @vitest-environment node
import { describe, expect, it } from "vitest";
import { ensureWindowsToastIdentity, parseRegSz, TOAST_IDENTITY_KEY_ROOT, type RunReg } from "./windows-toast-identity";

const AUMID = "dev.agent-orchestrator.desktop";
const KEY = `${TOAST_IDENTITY_KEY_ROOT}\\${AUMID}`;
const ICON = "C:\\repo\\frontend\\assets\\icon-dev.png";

function recorder(initial: Record<string, string> | null) {
	const calls: string[][] = [];
	const store: Record<string, string> = { ...(initial ?? {}) };
	const run: RunReg = async (args) => {
		calls.push(args);
		if (args[0] === "query") {
			if (initial === null && Object.keys(store).length === 0) return { code: 1, stdout: "" };
			const body = Object.entries(store).map(([name, value]) => `    ${name}    REG_SZ    ${value}`).join("\r\n");
			return { code: 0, stdout: `\r\n${KEY}\r\n${body}\r\n\r\n` };
		}
		store[args[3]] = args[7];
		return { code: 0, stdout: "" };
	};
	return { run, calls, store };
}

describe("ensureWindowsToastIdentity", () => {
	it("registers the product name and icon for an unpackaged Windows run that has none", async () => {
		const { run, calls, store } = recorder(null);
		await expect(ensureWindowsToastIdentity({ aumid: AUMID, displayName: "Ayo", iconPath: ICON, packaged: false, platform: "win32", run })).resolves.toBe(
			"registered",
		);
		expect(store).toEqual({ DisplayName: "Ayo", IconUri: ICON });
		expect(calls[0]).toEqual(["query", KEY]);
		expect(calls.slice(1)).toEqual([
			["add", KEY, "/v", "DisplayName", "/t", "REG_SZ", "/d", "Ayo", "/f"],
			["add", KEY, "/v", "IconUri", "/t", "REG_SZ", "/d", ICON, "/f"],
		]);
	});

	it("writes nothing when the registered values already match", async () => {
		const { run, calls } = recorder({ DisplayName: "Ayo", IconUri: ICON });
		await expect(ensureWindowsToastIdentity({ aumid: AUMID, displayName: "Ayo", iconPath: ICON, packaged: false, platform: "win32", run })).resolves.toBe(
			"unchanged",
		);
		expect(calls).toHaveLength(1);
	});

	it("rewrites only the value that differs", async () => {
		const { run, calls, store } = recorder({ DisplayName: "Electron", IconUri: ICON });
		await ensureWindowsToastIdentity({ aumid: AUMID, displayName: "Ayo", iconPath: ICON, packaged: false, platform: "win32", run });
		expect(store.DisplayName).toBe("Ayo");
		expect(calls.filter((args) => args[0] === "add")).toHaveLength(1);
	});

	it("registers just the name when there is no icon file", async () => {
		const { run, store } = recorder(null);
		await ensureWindowsToastIdentity({ aumid: AUMID, displayName: "Ayo", packaged: false, platform: "win32", run });
		expect(store).toEqual({ DisplayName: "Ayo" });
	});

	// A packaged install gets its name from the installer's shortcut; and only
	// Windows has this registry at all.
	it.each([
		{ packaged: true, platform: "win32" as const },
		{ packaged: false, platform: "darwin" as const },
		{ packaged: false, platform: "linux" as const },
	])("does nothing when packaged=$packaged on $platform", async ({ packaged, platform }) => {
		const { run, calls } = recorder(null);
		await expect(ensureWindowsToastIdentity({ aumid: AUMID, displayName: "Ayo", iconPath: ICON, packaged, platform, run })).resolves.toBe("skipped");
		expect(calls).toHaveLength(0);
	});

	it("never throws: a failed or crashing registry call is reported, not raised", async () => {
		await expect(
			ensureWindowsToastIdentity({
				aumid: AUMID, displayName: "Ayo", packaged: false, platform: "win32",
				run: async (args) => (args[0] === "query" ? { code: 1, stdout: "" } : { code: 5, stdout: "" }),
			}),
		).resolves.toBe("failed");
		await expect(
			ensureWindowsToastIdentity({
				aumid: AUMID, displayName: "Ayo", packaged: false, platform: "win32",
				run: async () => {
					throw new Error("spawn failed");
				},
			}),
		).resolves.toBe("failed");
	});
});

describe("parseRegSz", () => {
	it("reads a value with spaces and ignores other names", () => {
		const out = "\r\nHKEY\r\n    DisplayName    REG_SZ    Ayo (dev)  \r\n    IconUri    REG_SZ    C:\\a b\\i.png\r\n";
		expect(parseRegSz(out, "DisplayName")).toBe("Ayo (dev)");
		expect(parseRegSz(out, "IconUri")).toBe("C:\\a b\\i.png");
		expect(parseRegSz(out, "Missing")).toBeUndefined();
	});
});
