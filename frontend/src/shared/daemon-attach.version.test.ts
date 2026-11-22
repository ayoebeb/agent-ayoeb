import { describe, expect, it } from "vitest";
import { DAEMON_SERVICE_NAME, parseDaemonProbe } from "./daemon-attach";

const body = (extra: Record<string, unknown> = {}) => ({ status: "ok", service: DAEMON_SERVICE_NAME, pid: 42, ...extra });

describe("parseDaemonProbe version", () => {
	it("carries the release the daemon reports, trimmed", () => {
		expect(parseDaemonProbe("healthz", body({ version: " 0.44.0 " }))?.version).toBe("0.44.0");
	});

	it("leaves the version out when an older daemon or a development build reports none", () => {
		expect(parseDaemonProbe("healthz", body())).not.toHaveProperty("version");
		expect(parseDaemonProbe("healthz", body({ version: "  " }))).not.toHaveProperty("version");
		expect(parseDaemonProbe("healthz", body({ version: 44 }))).not.toHaveProperty("version");
	});
});
