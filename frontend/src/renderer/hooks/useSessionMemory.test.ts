import { describe, expect, it } from "vitest";
import { formatCPU, formatMemory, resourceStatusParts, type AppMemoryReading, type SystemMemoryReading } from "./useSessionMemory";
import { formatDuration, processKind } from "../components/SessionMemoryPanel";

describe("formatMemory", () => {
	it("shows whole megabytes and switches to GB at a thousand, in 1024s", () => {
		expect(formatMemory(641_728_512)).toBe("612 MB");
		expect(formatMemory(2_254_857_830)).toBe("2.1 GB");
	});
});

describe("formatCPU", () => {
	it("is a whole percent of one core", () => {
		expect(formatCPU(82.4)).toBe("82%");
		expect(formatCPU(0.34)).toBe("0%");
	});
});

describe("processKind", () => {
	it("names the kind of process, keeping AO's subcommand", () => {
		expect(processKind("/tmp/.mount_x/resources/daemon/ao chat-host s1 /home/u")).toBe("ao chat-host");
		expect(processKind("/usr/bin/git status --short")).toBe("git");
		expect(processKind("sh -c go test ./...")).toBe("sh");
		expect(processKind("")).toBe("?");
	});
});

describe("formatDuration", () => {
	it("reads as seconds, then minutes, then hours", () => {
		expect(formatDuration(38_400)).toBe("38s");
		expect(formatDuration(250_000)).toBe("4m 10s");
		expect(formatDuration(3_720_000)).toBe("1h 2m");
	});
});

describe("resourceStatusParts", () => {
	const GIB = 1024 ** 3;
	const app = { rssBytes: 2 * GIB } as AppMemoryReading;
	const host = (over: Partial<SystemMemoryReading> = {}) =>
		({ totalBytes: 32 * GIB, availableBytes: 20 * GIB, cpuPercent: 14.4, cpuMeasured: true, ...over }) as SystemMemoryReading;

	it("reads RAM used of total, host CPU and Ayo's own size", () => {
		expect(resourceStatusParts(host(), app)).toEqual({ ram: { used: "12.0 GB", total: "32.0 GB" }, cpu: "14%", ayo: "2.0 GB" });
	});

	it("shows an unknown CPU as a dash, not an idle machine", () => {
		expect(resourceStatusParts(host({ cpuMeasured: false, cpuPercent: 0 }), app).cpu).toBe("—");
	});

	it("keeps only Ayo's size where the host cannot be read", () => {
		expect(resourceStatusParts(undefined, app)).toEqual({ ayo: "2.0 GB" });
	});
});
