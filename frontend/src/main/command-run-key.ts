import { createHmac } from "node:crypto";

/**
 * Scopes the key to Chat command runs; the daemon derives the same key
 * (backend/internal/httpd/command_run_gate.go). Change both together.
 */
const COMMAND_RUN_KEY_LABEL = "ao-command-run-v1";

/**
 * The key the renderer presents to run a command from Chat on the local
 * daemon. It is derived from the browser runtime token this app hands its
 * daemon on stdin, so a process that merely reaches the loopback port (an
 * agent, the ao CLI, a script) cannot present it.
 */
export function commandRunKey(runtimeToken: string): string {
	if (!runtimeToken) return "";
	return createHmac("sha256", runtimeToken).update(COMMAND_RUN_KEY_LABEL).digest("base64url");
}
