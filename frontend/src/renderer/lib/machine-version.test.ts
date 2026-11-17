import { describe, expect, it } from "vitest";
import { compareVersions, parseVersion, versionRelation } from "./machine-version";

describe("parseVersion", () => {
	it("reads a release, a v prefix, a pre-release and build metadata", () => {
		expect(parseVersion("0.44.0")).toEqual({ core: [0, 44, 0], pre: [] });
		expect(parseVersion(" v1.2.3 ")).toEqual({ core: [1, 2, 3], pre: [] });
		expect(parseVersion("1.2.3-nightly.20261116.1+abc")).toEqual({ core: [1, 2, 3], pre: ["nightly", "20261116", "1"] });
	});

	it("returns null for anything that is not a version", () => {
		for (const raw of ["", "dev", "1.2", "1.2.3.4", "latest", null, undefined]) expect(parseVersion(raw)).toBeNull();
	});
});

describe("compareVersions", () => {
	it("compares numbers, not text", () => {
		expect(compareVersions("0.9.0", "0.10.0")).toBeLessThan(0);
		expect(compareVersions("2.0.0", "1.99.99")).toBeGreaterThan(0);
		expect(compareVersions("0.44.0", "v0.44.0")).toBe(0);
	});

	it("ranks a release above its own pre-releases and orders pre-releases by identifier", () => {
		expect(compareVersions("1.0.0", "1.0.0-rc.1")).toBeGreaterThan(0);
		expect(compareVersions("1.0.0-nightly.9", "1.0.0-nightly.10")).toBeLessThan(0);
		expect(compareVersions("1.0.0-1", "1.0.0-alpha")).toBeLessThan(0);
		expect(compareVersions("1.0.0-alpha", "1.0.0-alpha.1")).toBeLessThan(0);
		expect(compareVersions("1.0.0-beta", "1.0.0-alpha")).toBeGreaterThan(0);
	});

	it("is null when either side is not a version", () => {
		expect(compareVersions("dev", "1.0.0")).toBeNull();
		expect(compareVersions("1.0.0", undefined)).toBeNull();
	});
});

describe("versionRelation", () => {
	it("says whether a computer is behind, ahead of, or on the same release as this one", () => {
		expect(versionRelation("0.43.2", "0.44.0")).toBe("behind");
		expect(versionRelation("0.45.0", "0.44.0")).toBe("ahead");
		expect(versionRelation("0.44.0", "0.44.0")).toBe("same");
	});

	it("never counts an unknown version as a mismatch", () => {
		expect(versionRelation(null, "0.44.0")).toBe("unknown");
		expect(versionRelation(undefined, "0.44.0")).toBe("unknown");
		expect(versionRelation("dev", "0.44.0")).toBe("unknown");
		expect(versionRelation("0.44.0", null)).toBe("unknown");
	});
});
