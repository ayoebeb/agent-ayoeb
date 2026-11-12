import { describe, expect, it } from "vitest";
import { commandRunKey } from "./command-run-key";

describe("commandRunKey", () => {
	it("matches the daemon's derivation", () => {
		// backend/internal/httpd/command_run_gate_test.go asserts the same vector.
		expect(commandRunKey("test-runtime-token")).toBe("qrOL0IYLaW6w7XNg5pRjypUW6XPECINUGsUeljoIa0w");
	});

	it("yields no key without a token", () => {
		expect(commandRunKey("")).toBe("");
	});
});
