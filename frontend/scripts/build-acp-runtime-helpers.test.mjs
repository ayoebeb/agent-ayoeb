// @vitest-environment node
import {
	existsSync,
	mkdirSync,
	mkdtempSync,
	readFileSync,
	readdirSync,
	rmSync,
	symlinkSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	archiveExtraction,
	createWorkDirectory,
	npmInvocation,
	patchClaudeAlwaysAllowScope,
	patchClaudeContextUsage,
	patchClaudeFoldedPromptSettlement,
	patchClaudeHibernationCheck,
	patchClaudeRetryDetails,
	patchClaudeStaleIdleDebt,
	patchClaudeSteerSettlement,
	pruneNodeDistribution,
	runtimeSourceFiles,
} from "./build-acp-runtime-helpers.mjs";

const temporaryDirectories = [];

afterEach(() => {
	for (const directory of temporaryDirectories.splice(0)) {
		rmSync(directory, { recursive: true, force: true });
	}
});

describe("createWorkDirectory", () => {
	it("places extraction on the output filesystem", () => {
		const outputRoot = temporaryDirectory();
		const workDirectory = createWorkDirectory(outputRoot);

		expect(dirname(workDirectory)).toBe(outputRoot);
		expect(existsSync(workDirectory)).toBe(true);
	});
});

describe("runtimeSourceFiles", () => {
	it("packages and fingerprints the ACP runtime manifest", () => {
		expect(runtimeSourceFiles()).toEqual([
			"package.json",
			"package-lock.json",
		]);
	});
});

describe("npmInvocation", () => {
	it("runs the parent npm CLI through Node on Windows", () => {
		expect(
			npmInvocation(["ci", "--omit=dev"], {
				platform: "win32",
				execPath: "C:\\node\\node.exe",
				npmExecPath: "C:\\node\\node_modules\\npm\\bin\\npm-cli.js",
				commandInterpreter: "C:\\Windows\\System32\\cmd.exe",
			}),
		).toEqual({
			command: "C:\\node\\node.exe",
			args: ["C:\\node\\node_modules\\npm\\bin\\npm-cli.js", "ci", "--omit=dev"],
		});
	});

	it("falls back to cmd.exe for a directly invoked Windows build script", () => {
		expect(
			npmInvocation(["ci"], {
				platform: "win32",
				npmExecPath: null,
				commandInterpreter: "C:\\Windows\\System32\\cmd.exe",
			}),
		).toEqual({
			command: "C:\\Windows\\System32\\cmd.exe",
			args: ["/d", "/s", "/c", "npm.cmd", "ci"],
		});
	});

	it("invokes npm directly on Unix when no parent npm CLI is available", () => {
		expect(npmInvocation(["ci"], { platform: "linux", npmExecPath: null })).toEqual({
			command: "npm",
			args: ["ci"],
		});
	});
});

describe("patchClaudeRetryDetails", () => {
	it("keeps Claude's retry delay in the published session failure", () => {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, `
                            case "api_retry": {
                                const title = "retrying";
                                await publishSessionFailure(message.error_status === null
                                    ? "transport_lost"
                                    : providerFailureCategory(message.error), {
                                    title,
                                    severity: "warning",
                                });
                                break;
                            }
                            case "model_refusal_fallback": {
`);

		expect(patchClaudeRetryDetails(adapterPath)).toBe(true);
		expect(patchClaudeRetryDetails(adapterPath)).toBe(false);
		const patched = readFileSync(adapterPath, "utf8");
		expect(patched).toContain("message.retry_delay_ms / 1000");
		expect(patched).toContain("Trying again in ${retryDelay}.");
		expect(patched).toContain("details: retryDetails");
	});
});

describe("patchClaudeHibernationCheck", () => {
	const anchors = [
		"// session.liveBackgroundTasks.set(message.task_id",
		"// session.lastSessionState = message.state;",
		"agent.endTask = (session, message) => { session.liveBackgroundTasks.delete(message.task_id); };",
		"// session.liveBackgroundTasks.delete(message.task_id);",
		"connection.onRequest(GOAL_CONTROL_METHOD, { parse: parseGoalRequest }, (ctx) => agent.goal(ctx.params));",
	];

	it("keeps live native tasks awake and allows sleep after they settle", () => {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, anchors.join("\n"));
		expect(patchClaudeHibernationCheck(adapterPath)).toBe(true);
		expect(patchClaudeHibernationCheck(adapterPath)).toBe(false);
		const handlers = new Map();
		const connection = { onRequest: (method, parser, handler) => {
			expect(typeof parser.parse).toBe("function");
			expect(typeof handler).toBe("function");
			handlers.set(method, (ctx) => handler({ params: parser.parse(ctx.params) }));
			return connection;
		} };
		const tasks = new Map();
		const agent = { sessions: { native: { liveBackgroundTasks: tasks } } };
		new Function("connection", "agent", "GOAL_CONTROL_METHOD", "parseGoalRequest", "RequestError", readFileSync(adapterPath, "utf8"))(
			connection, agent, "goal", params => params, { invalidParams: () => new Error("invalid session") },
		);
		const check = handlers.get("_ao/session/can_hibernate");
		const ctx = { params: { sessionId: "native" } };
		expect(check(ctx)).toEqual({ canHibernate: true });
		tasks.set("server", { isSubagent: false });
		expect(check(ctx)).toEqual({ canHibernate: false });
		tasks.delete("server");
		expect(check(ctx)).toEqual({ canHibernate: true });
		tasks.set("ended", { isSubagent: true, endedPerLevel: "ended" });
		expect(check(ctx)).toEqual({ canHibernate: true });
		// A finished task's notification starts a cycle AO has no turn for.
		agent.sessions.native.lastSessionState = "running";
		expect(check(ctx)).toEqual({ canHibernate: false });
		agent.sessions.native.lastSessionState = "requires_action";
		expect(check(ctx)).toEqual({ canHibernate: false });
		agent.sessions.native.lastSessionState = "idle";
		expect(check(ctx)).toEqual({ canHibernate: true });
		// The notification cycle is reported as running a moment after the task leaves.
		tasks.set("shell", { isSubagent: false });
		agent.endTask(agent.sessions.native, { task_id: "shell" });
		expect(check(ctx)).toEqual({ canHibernate: false });
		agent.sessions.native.aoTaskEndedAt -= 10000;
		expect(check(ctx)).toEqual({ canHibernate: true });
		for (const sessionId of [undefined, "missing", "__proto__"]) {
			expect(() => check({ params: { sessionId } })).toThrow("invalid session");
		}
		agent.sessions.native.queryClosed = true;
		expect(() => check(ctx)).toThrow("invalid session");
	});

	it("fails packaging if the pinned native task registry changes", () => {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		for (const missing of anchors.keys()) {
			writeFileSync(adapterPath, anchors.filter((_, i) => i !== missing).join("\n"));
			expect(() => patchClaudeHibernationCheck(adapterPath)).toThrow("native-task hibernation check");
		}
	});
});

describe("patchClaudeContextUsage", () => {
	function patchedUsageBlock() {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, `
                            // Send usage_update notification
                            if (lastAssistantTotalUsage !== null) {
                                await sendUpdate({
                                    sessionId: params.sessionId,
                                    update: {
                                        sessionUpdate: "usage_update",
                                        used: lastAssistantTotalUsage,
                                        size: session.contextWindowSize,
                                        cost: {
                                            amount: message.total_cost_usd,
                                            currency: "USD",
                                        },
                                        ...(message.origin && {
                                            _meta: { "_claude/origin": message.origin },
                                        }),
                                    },
                                });
                            }
                            if (session.cancelled) {
`);

		expect(patchClaudeContextUsage(adapterPath)).toBe(true);
		expect(patchClaudeContextUsage(adapterPath)).toBe(false);
		const patched = readFileSync(adapterPath, "utf8");
		const start = patched.indexOf("// AO: publish the SDK's context snapshot");
		const end = patched.indexOf("if (session.cancelled) {", start);
		const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
		return new AsyncFunction("session", "sendUpdate", "state", `
			const params = { sessionId: "s" };
			const message = { total_cost_usd: 1.5 };
			let lastAssistantTotalUsage = 9;
			${patched.slice(start, end)}
			state.read = () => lastAssistantTotalUsage;
		`);
	}

	function claudeSession(getContextUsage) {
		return {
			contextWindowSize: 100,
			contextWindowAuthoritative: false,
			models: { currentModelId: "sonnet" },
			query: { getContextUsage },
		};
	}

	async function publish(run, session, sendUpdate) {
		const updates = [];
		const errors = [];
		const state = {};
		await run.call({ logger: { error: (...args) => errors.push(args) } }, session, async (notification) => {
			updates.push(notification.update);
			await sendUpdate?.();
		}, state);
		await vi.waitFor(() => expect(updates.length + errors.length).toBeGreaterThan(0));
		return { updates, errors, state };
	}

	it("settles the result before the SDK context snapshot arrives", async () => {
		const run = patchedUsageBlock();
		let resolveSnapshot;
		const session = claudeSession(() => new Promise((resolve) => { resolveSnapshot = resolve; }));
		const updates = [];
		const state = {};

		await run.call({ logger: { error: () => {} } }, session, (notification) => {
			updates.push(notification.update);
		}, state);
		expect(updates).toEqual([]);

		resolveSnapshot({ totalTokens: 17, rawMaxTokens: 200 });
		await vi.waitFor(() => expect(updates).toHaveLength(1));
		expect(updates[0]).toMatchObject({ sessionUpdate: "usage_update", used: 17, size: 200, cost: { amount: 1.5 } });
		expect(session.contextWindowAuthoritative).toBe(true);
		expect(state.read()).toBe(17);
	});

	it("falls back to the message usage when the snapshot is unusable", async () => {
		const run = patchedUsageBlock();
		for (const getContextUsage of [
			async () => { throw new Error("unavailable"); },
			async () => ({ totalTokens: 0, rawMaxTokens: 200 }),
		]) {
			const session = claudeSession(getContextUsage);
			const { updates } = await publish(run, session);
			expect(updates.map(({ used, size }) => [used, size])).toEqual([[9, 100]]);
			expect(session.contextWindowAuthoritative).toBe(false);
		}
	});

	it("keeps the window a model switch seeded while the snapshot was pending", async () => {
		const run = patchedUsageBlock();
		const session = claudeSession(async () => {
			session.models.currentModelId = "opus";
			session.contextWindowSize = 1000;
			return { totalTokens: 17, rawMaxTokens: 200 };
		});
		const { updates } = await publish(run, session);
		expect(updates.map(({ used, size }) => [used, size])).toEqual([[17, 1000]]);
		expect(session.contextWindowAuthoritative).toBe(false);
	});

	it("logs a failed usage publish instead of rejecting", async () => {
		const run = patchedUsageBlock();
		const session = claudeSession(async () => ({ totalTokens: 17, rawMaxTokens: 200 }));
		const { errors } = await publish(run, session, async () => { throw new Error("closed"); });
		await vi.waitFor(() => expect(errors).toHaveLength(1));
		expect(errors[0][0]).toBe("Failed to publish Claude context usage:");
	});
});

describe("patchClaudeFoldedPromptSettlement", () => {
	it("routes a task-notification result that names a pending prompt to the user lane", () => {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, `
        const findUnsettledTurn = (uuid) => (session.turnQueue ?? []).find((t) => t.promptUuid === uuid && !t.settled);
                    case "result": {
                        const isAutonomousResult = message.origin != null && AUTONOMOUS_RESULT_ORIGINS.has(message.origin.kind);
                        try {
`);

		expect(patchClaudeFoldedPromptSettlement(adapterPath)).toBe(true);
		expect(patchClaudeFoldedPromptSettlement(adapterPath)).toBe(false);
		const patched = readFileSync(adapterPath, "utf8");

		const start = patched.indexOf("// AO: a result naming a pending prompt answers it.");
		const end = patched.indexOf("try {", start);
		const classify = new Function("message", "session", `
			const AUTONOMOUS_RESULT_ORIGINS = new Set(["task-notification", "peer"]);
			const isHeldOpen = (turn) => turn.deferredSettle !== undefined && !turn.settled;
			const findUnsettledTurn = (uuid) => (session.turnQueue ?? []).find((t) => t.promptUuid === uuid && !t.settled);
			${patched.slice(start, end)}
			return isAutonomousResult;
		`);
		const notification = { kind: "task-notification" };
		const session = {
			turnQueue: [
				{ promptUuid: "pending", settled: false },
				{ promptUuid: "settled", settled: true },
				{ promptUuid: "held", settled: false, deferredSettle: { stopReason: "end_turn" } },
			],
		};

		expect(classify({ origin: notification, user_message_uuids: ["pending"] }, session)).toBe(false);
		expect(classify({ origin: notification, user_message_uuid: "pending" }, session)).toBe(false);
		expect(classify({ origin: notification, user_message_uuids: ["settled", "held", "unknown"] }, session)).toBe(true);
		expect(classify({ origin: notification }, session)).toBe(true);
		expect(classify({ origin: { kind: "human" } }, session)).toBe(false);
		expect(classify({}, session)).toBe(false);
	});

	it("fails packaging when the adapter's classification changes", () => {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, "const isAutonomousResult = isAutonomous(message);\n");

		expect(() => patchClaudeFoldedPromptSettlement(adapterPath)).toThrow(/folded-prompt patch/);
	});
});

describe("patchClaudeStaleIdleDebt", () => {
	it("drops unpaid idle debt when Claude starts running again", () => {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, `
                            case "session_state_changed": {
                                session.lastSessionState = message.state;
                                if (message.state === "idle") {
                                    if (session.owedTrailingIdles > 0) {
                                        session.owedTrailingIdles--;
                                    }
                                }
                                break;
                            }
`);

		expect(patchClaudeStaleIdleDebt(adapterPath)).toBe(true);
		expect(patchClaudeStaleIdleDebt(adapterPath)).toBe(false);
		const patched = readFileSync(adapterPath, "utf8");

		const start = patched.indexOf("// AO: drop idle debt that can no longer be paid.");
		const end = patched.indexOf("break;", start);
		const onState = new Function("message", "session", `${patched.slice(start, end)}`);
		const session = { lastSessionState: "idle", owedTrailingIdles: 2 };

		onState({ state: "idle" }, session);
		expect(session.owedTrailingIdles).toBe(1);
		onState({ state: "running" }, session);
		expect(session.owedTrailingIdles).toBe(0);
		session.owedTrailingIdles = 1;
		onState({ state: "running" }, session);
		expect(session.owedTrailingIdles).toBe(1);
		onState({ state: "idle" }, session);
		expect(session.owedTrailingIdles).toBe(0);
		expect(session.lastSessionState).toBe("idle");
	});

	it("fails packaging when the adapter's state handling changes", () => {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, 'case "session_state_changed": {\n');

		expect(() => patchClaudeStaleIdleDebt(adapterPath)).toThrow(/idle-debt patch/);
	});
});

describe("patchClaudeSteerSettlement", () => {
	// claude-agent-acp 0.70's steer and settle lanes, cut down to the lines the
	// settlement patches anchor on. Each test replays an SDK frame order a real
	// session produces.
	const consumerSource = `
function isSteering(turn) {
    return turn != null && turn.steeredEchoes !== undefined && !turn.settled;
}
const AUTONOMOUS_RESULT_ORIGINS = new Set(["task-notification", "peer"]);
const isHeldOpen = (turn) => turn != null && turn.deferredSettle !== undefined && !turn.settled;
const findUnsettledTurn = (uuid) => session.turnQueue.find((t) => t.promptUuid === uuid && !t.settled);
const ensureActiveTurn = () => { session.activeTurn ??= session.turnQueue.find((turn) => !turn.settled); };
const recordResultForOrphanCommands = () => {};
const settleActive = (outcome) => {
    session.activeTurn.settled = outcome;
    session.activeTurn = undefined;
};
const settleDeferredIfDrained = () => {
    if (isHeldOpen(session.activeTurn)) settleActive(session.activeTurn.deferredSettle);
};
function steer(steeredUuid) {
        const turnInFlight = session.turnQueue.find((turn) => !turn.settled);
        (turnInFlight.steeredEchoes ??= new Set()).add(steeredUuid);
        if (turnInFlight.deferredSettle !== undefined) {
            turnInFlight.steeredSettle = turnInFlight.deferredSettle;
            turnInFlight.deferredSettle = undefined;
        }
}
function onMessage(message) {
        const settleOrDefer = (outcome) => {
            if (isSteering(session.activeTurn)) {
                session.activeTurn.steeredSettle = outcome;
                return;
            }
            settleActive(outcome);
        };
        switch (message.type) {
                    case "result": {
                        const isAutonomousResult = message.origin != null && AUTONOMOUS_RESULT_ORIGINS.has(message.origin.kind);
                        try {
                            if (!isAutonomousResult) {
                                recordResultForOrphanCommands();
                                ensureActiveTurn();
                            }
                            const owesTrailingIdle = isAutonomousResult || !isSteering(session.activeTurn);
                            if (owesTrailingIdle &&
                                (isAutonomousResult || !session.cancelled || !session.activeTurn)) {
                                session.owedTrailingIdles++;
                            }
                            if (!isAutonomousResult && !session.cancelled && session.activeTurn) {
                                settleOrDefer(message.result);
                            }
                        } finally {}
                        break;
                    }
                    case "user": {
                            if ("isReplay" in message && message.isReplay) {
                                if (isSteering(session.activeTurn)) {
                                    session.activeTurn.steeredEchoes.delete(message.uuid);
                                }
                                break;
                            }
                        break;
                    }
                    case "system": {
                        switch (message.subtype) {
                            case "session_state_changed": {
                                session.lastSessionState = message.state;
                                if (message.state === "idle") {
                                    if (isHeldOpen(session.activeTurn)) {
                                        if (session.owedTrailingIdles > 0) {
                                            session.owedTrailingIdles--;
                                        }
                                        settleDeferredIfDrained();
                                    }
                                    else if (session.owedTrailingIdles > 0) {
                                        // Absorb a settled turn's trailing idle. Also covers a
                                        session.owedTrailingIdles--;
                                    }
                                    else if (isSteering(session.activeTurn)) {
                                        // Plain Turn so the fields can be cleared below; isSteering
                                        // narrows steeredEchoes to non-optional.
                                        const steered = session.activeTurn;
                                        if (steered.steeredEchoes?.size === 0 && steered.steeredSettle !== undefined) {
                                            // Via the subagent gate, not settleActive: a steered turn
                                            // can also have spawned background subagents, which own it
                                            // from here (settles now if none is live, holds otherwise).
                                            steered.deferredSettle = steered.steeredSettle;
                                            steered.steeredEchoes = undefined;
                                            steered.steeredSettle = undefined;
                                            settleDeferredIfDrained();
                                        }
                                    }
                                    else if (!session.cancelled && session.activeTurn) {
                                        settleActive("rejected: idle without a result (#825)");
                                    }
                                }
                                break;
                            }
                        }
                        break;
                    }
        }
}
`;

	function steerConsumer({ fold = true } = {}) {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, consumerSource);
		// Same order as scripts/build-acp-runtime.mjs.
		if (fold) patchClaudeFoldedPromptSettlement(adapterPath);
		patchClaudeStaleIdleDebt(adapterPath);
		expect(patchClaudeSteerSettlement(adapterPath)).toBe(true);
		expect(patchClaudeSteerSettlement(adapterPath)).toBe(false);

		const session = { turnQueue: [], owedTrailingIdles: 0, lastSessionState: "idle" };
		const consumer = new Function("session", `${readFileSync(adapterPath, "utf8")}\nreturn { steer, onMessage };`)(session);
		const turn = { promptUuid: "prompt" };
		session.turnQueue.push(turn);
		session.activeTurn = turn;
		return {
			session,
			turn,
			steer: consumer.steer,
			result: (fields) => consumer.onMessage({ type: "result", origin: { kind: "human" }, ...fields }),
			echo: (uuid) => consumer.onMessage({ type: "user", isReplay: true, uuid }),
			state: (state) => consumer.onMessage({ type: "system", subtype: "session_state_changed", state }),
		};
	}

	it("settles at the result that names the steer, which still owes its trailing idle", () => {
		const { session, turn, steer, result, echo, state } = steerConsumer();
		steer("steer");
		result({ result: "interrupted", user_message_uuid: "prompt" });
		echo("steer");
		result({ result: "steered", user_message_uuids: ["steer"] });
		expect(turn.settled).toBe("steered");
		expect(session.owedTrailingIdles).toBe(1);

		// The next prompt's echo can land before that trailing idle.
		const next = { promptUuid: "next" };
		session.turnQueue.push(next);
		session.activeTurn = next;
		state("idle");
		expect(next.settled).toBeUndefined();
		result({ result: "answer", user_message_uuid: "next" });
		expect(next.settled).toBe("answer");
	});

	it("waits for the steered result when an idle lands after the steered echo", () => {
		const { session, turn, steer, result, echo, state } = steerConsumer();
		result({ origin: { kind: "task-notification" }, result: "autonomous" });
		steer("steer");
		result({ result: "interrupted", user_message_uuid: "prompt" });
		echo("steer");
		state("idle"); // the autonomous cycle's lagging trailer
		expect(turn.settled).toBeUndefined();

		result({ result: "steered" });
		state("idle");
		expect(turn.settled).toBe("steered");
		expect(session.owedTrailingIdles).toBe(0);
	});

	it("settles a ready steered turn ahead of idle debt and leaves the debt to the running sweep", () => {
		const { session, turn, steer, result, echo, state } = steerConsumer();
		result({ origin: { kind: "task-notification" }, result: "autonomous" });
		steer("steer");
		result({ result: "interrupted", user_message_uuid: "prompt" });
		echo("steer");
		result({ result: "steered" });
		state("idle"); // Claude Code coalesced the autonomous trailer into this idle
		expect(turn.settled).toBe("steered");
		expect(session.owedTrailingIdles).toBe(1);
		state("running");
		expect(session.owedTrailingIdles).toBe(0);
	});

	it.each([true, false])("lets an autonomous result that names the steer answer it (fold patch: %s)", (fold) => {
		const { session, turn, steer, result, echo } = steerConsumer({ fold });
		steer("steer");
		echo("steer");
		result({ origin: { kind: "task-notification" }, result: "hand-back", user_message_uuids: ["steer"] });
		expect(turn.settled).toBe("hand-back");
		expect(session.owedTrailingIdles).toBe(1);
	});

	it.each([true, false])("settles a queued prompt whose steer an autonomous cycle folded in (fold patch: %s)", (fold) => {
		const { session, turn, steer, result } = steerConsumer({ fold });
		session.activeTurn = undefined; // Claude Code's own cycle is running; the prompt is still queued
		steer("steer");
		result({
			origin: { kind: "task-notification" },
			result: "folded",
			user_message_uuid: "steer",
			user_message_uuids: ["prompt", "steer"],
		});
		expect(turn.settled).toBe("folded");
		expect(session.owedTrailingIdles).toBe(1);
	});

	it("keeps the steer lane while another steer is still outstanding", () => {
		const { turn, steer, result, echo, state } = steerConsumer();
		steer("first");
		steer("second");
		result({ result: "first answer", user_message_uuids: ["first"] });
		expect(turn.settled).toBeUndefined();
		expect([...turn.steeredEchoes]).toEqual(["second"]);
		state("idle");
		expect(turn.settled).toBeUndefined();

		echo("second");
		result({ result: "second answer", user_message_uuids: ["second"] });
		expect(turn.settled).toBe("second answer");
	});

	it("leaves a cancelled turn's steers to cancel()", () => {
		const { session, turn, steer, result } = steerConsumer();
		steer("steer");
		session.cancelled = true;
		result({ origin: { kind: "task-notification" }, result: "cancelled", user_message_uuid: "steer" });
		expect(turn.steeredEchoes).toEqual(new Set(["steer"]));
	});

	it("fails packaging without modifying a changed adapter", () => {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		const changed = consumerSource.replace("ensureActiveTurn();", "await ensureActiveTurn(message.user_message_uuid);");
		writeFileSync(adapterPath, changed);

		expect(() => patchClaudeSteerSettlement(adapterPath)).toThrow(/steer settlement patch/);
		expect(readFileSync(adapterPath, "utf8")).toBe(changed);
	});
});

// The permission request of claude-agent-acp 0.70's canUseTool, at its upstream
// indentation, with the two helpers it calls reduced to what the tests observe.
const alwaysAllowConsumer = [
	"function permissionMetadataForAlwaysAllow(suggestions, toolName) {",
	'    const effective = suggestions && suggestions.length > 0 ? suggestions : [{ type: "addRules", rules: [{ toolName }], behavior: "allow", destination: "session" }];',
	"    return { version: 1, changes: effective };",
	"}",
	"async function canUseTool(requestPermissionFromClient, toolName, toolInput, suggestions) {",
	"            const response = await requestPermissionFromClient({",
	"                options: [",
	'                    { kind: "reject_once", name: "Deny", optionId: "reject" },',
	'                    { kind: "allow_once", name: "Allow Once", optionId: "allow" },',
	"                    {",
	'                        kind: "allow_always",',
	'                        name: "Always Allow",',
	'                        optionId: "allow_always",',
	"                        _meta: {",
	"                            permission: permissionMetadataForAlwaysAllow(suggestions, toolName),",
	"                        },",
	"                    },",
	"                ],",
	"            });",
	'            if (response.outcome?.outcome === "selected" &&',
	'                (response.outcome.optionId === "allow" || response.outcome.optionId === "allow_always")) {',
	"                // If Claude Code has suggestions, it will update their settings already",
	'                if (response.outcome.optionId === "allow_always") {',
	"                    return {",
	'                        behavior: "allow",',
	"                        updatedInput: toolInput,",
	"                        updatedPermissions: suggestions ?? [",
	"                            {",
	'                                type: "addRules",',
	"                                rules: [{ toolName }],",
	'                                behavior: "allow",',
	'                                destination: "session",',
	"                            },",
	"                        ],",
	"                    };",
	"                }",
	'                return { behavior: "allow", updatedInput: toolInput };',
	"            }",
	"            else {",
	'                return { behavior: "deny", message: "User refused permission to run tool" };',
	"            }",
	"}",
].join("\n");

describe("patchClaudeAlwaysAllowScope", () => {
	function patchedAdapter() {
		const adapterPath = join(temporaryDirectory(), "acp-agent.js");
		writeFileSync(adapterPath, alwaysAllowConsumer);
		expect(patchClaudeAlwaysAllowScope(adapterPath)).toBe(true);
		return { adapterPath, source: readFileSync(adapterPath, "utf8") };
	}

	// Runs the patched permission request against a client that picks `optionId`.
	// Returns what the client was offered and what Claude Code is told to apply.
	async function ask(toolName, toolInput, suggestions, optionId) {
		const { canUseTool } = new Function(`${patchedAdapter().source}\nreturn { canUseTool };`)();
		let offered;
		const result = await canUseTool(
			async (request) => {
				offered = request.options;
				return { outcome: { outcome: "selected", optionId } };
			},
			toolName,
			toolInput,
			suggestions,
		);
		return { offered, result };
	}

	const exactRule = (command) => [
		{ type: "addRules", rules: [{ toolName: "Bash", ruleContent: command }], behavior: "allow", destination: "localSettings" },
	];
	const rulesOf = (updates) => updates.flatMap((update) => update.rules.map((rule) => rule.ruleContent ?? rule.toolName));

	it("patches once and does not touch the unrelated permission paths", () => {
		const { adapterPath, source } = patchedAdapter();
		expect(patchClaudeAlwaysAllowScope(adapterPath)).toBe(false);
		expect(readFileSync(adapterPath, "utf8")).toBe(source);
		expect(source).toContain('{ kind: "reject_once", name: "Deny", optionId: "reject" },');
		expect(source).toContain('{ kind: "allow_once", name: "Allow Once", optionId: "allow" },');
	});

	it("fails packaging, leaving the adapter alone, when the pinned permission code changes", () => {
		const anchors = [
			"function permissionMetadataForAlwaysAllow(suggestions, toolName) {",
			'name: "Always Allow",',
			'response.outcome.optionId === "allow_always")) {',
			"updatedPermissions: suggestions ?? [",
		];
		for (const anchor of anchors) {
			const adapterPath = join(temporaryDirectory(), "acp-agent.js");
			const changed = alwaysAllowConsumer.replace(anchor, anchor.replace(/\w+/, "changed"));
			writeFileSync(adapterPath, changed);
			expect(() => patchClaudeAlwaysAllowScope(adapterPath), anchor).toThrow(/always-allow scope patch/);
			expect(readFileSync(adapterPath, "utf8")).toBe(changed);
		}
	});

	it("keeps allow_always narrow and offers the command kind as a separate choice", async () => {
		const command = 'node -e "console.log(1)"';
		const first = await ask("Bash", { command }, exactRule(command), "allow_always");
		expect(first.offered.map((option) => [option.optionId, option.kind, option.name])).toEqual([
			["reject", "reject_once", "Deny"],
			["allow", "allow_once", "Allow Once"],
			["allow_always", "allow_always", "Always allow only this exact command"],
			["allow_always_class", "allow_always", 'Always allow "node -e ..." in this workspace'],
		]);
		// The metadata a client shows is the rule that will be applied.
		expect(rulesOf(first.offered[2]._meta.permission.changes)).toEqual([command]);
		expect(rulesOf(first.offered[3]._meta.permission.changes)).toEqual(["node -e *"]);
		// A worker that sends the documented allow_always id never grants a class.
		expect(first.result).toEqual({
			behavior: "allow",
			updatedInput: { command },
			updatedPermissions: exactRule(command),
		});

		const kind = await ask("Bash", { command }, exactRule(command), "allow_always_class");
		expect(kind.result).toEqual({
			behavior: "allow",
			updatedInput: { command },
			updatedPermissions: [
				{ type: "addRules", rules: [{ toolName: "Bash", ruleContent: "node -e *" }], behavior: "allow", destination: "localSettings" },
			],
		});
	});

	it.each([
		['node -e "console.log(1)"', "node -e *"],
		['python -c "print(1)"', "python -c *"],
		["python3 -c x", "python3 -c *"],
		["node -p 1+1", "node -p *"],
		['node.exe -e "x"', "node.exe -e *"],
		["python script.py", "python script.py *"],
		["node script.js --flag", "node script.js *"],
		["pytest tests/a.py", "pytest tests/a.py *"],
		['node -e "a; b"', "node -e *"],
		// Programs outside the allowlist never get a wildcard from AO, whatever
		// their flags look like: Claude Code's own rule stands.
		["rm -rf node_modules", undefined],
		["rm -rf dir", undefined],
		["git --no-pager push --force", undefined],
		["git --no-pager log", undefined],
		["chmod -R 777 x", undefined],
		["chown -R me x", undefined],
		["mv -f a b", undefined],
		["curl -s -m 1 http://127.0.0.1:1/", undefined],
		["curl -s -d @secrets.txt http://127.0.0.1:1/", undefined],
		["wget -O out.sh http://127.0.0.1:1/", undefined],
		["npx --no-install some-package", undefined],
		["ssh -o x host", undefined],
		["docker --context x rm y", undefined],
		// Claude Code's own rule stands for these too.
		["git add *", undefined],
		["make build *", undefined],
		["node --version", undefined],
		["curl http://127.0.0.1:1/ -m 1", undefined],
		['bash -c "echo hi"', undefined],
		['sudo node -e "x"', undefined],
		["FOO=bar node --version", undefined],
		['node -e "$HOME"', undefined],
		["node -e `id`", undefined],
		['node -e "a\\"b"', undefined],
		["node -e x && rm -rf y", undefined],
		["node -e x | cat", undefined],
		["node -e (x)", undefined],
		['node -e "unterminated', undefined],
	])("%s becomes %s", async (command, widened) => {
		const { offered } = await ask("Bash", { command }, exactRule(command), "allow");
		const always = offered.filter((option) => option.kind === "allow_always");
		if (widened === undefined) {
			expect(always.map((option) => [option.optionId, option.name])).toEqual([["allow_always", "Always Allow"]]);
			return;
		}
		expect(always.map((option) => option.optionId)).toEqual(["allow_always", "allow_always_class"]);
		expect(rulesOf(always[0]._meta.permission.changes)).toEqual([command]);
		expect(rulesOf(always[1]._meta.permission.changes)).toEqual([widened]);
	});

	it("keeps Claude Code's behaviour for edits and for a request with no suggestions", async () => {
		const setMode = [{ type: "setMode", mode: "acceptEdits", destination: "session" }];
		const edit = await ask("Write", { file_path: "a.txt" }, setMode, "allow_always");
		expect(edit.offered.filter((option) => option.kind === "allow_always").map((option) => option.optionId)).toEqual(["allow_always"]);
		expect(edit.result.updatedPermissions).toEqual(setMode);

		const bare = await ask("Bash", { command: "node -e x" }, undefined, "allow_always");
		expect(bare.offered.filter((option) => option.kind === "allow_always").map((option) => option.optionId)).toEqual(["allow_always"]);
		expect(bare.result.updatedPermissions).toEqual([
			{ type: "addRules", rules: [{ toolName: "Bash" }], behavior: "allow", destination: "session" },
		]);
	});

	it("lets an MCP tool be remembered alone or as the whole server, only when asked", async () => {
		const tool = "mcp__atlassian__createJiraIssue";
		const suggestions = [{ type: "addRules", rules: [{ toolName: tool }], behavior: "allow", destination: "localSettings" }];

		const alone = await ask(tool, {}, suggestions, "allow_always");
		expect(alone.offered.filter((option) => option.kind === "allow_always").map((option) => [option.optionId, option.name])).toEqual([
			["allow_always", "Always allow this tool in this workspace"],
			["allow_always_server", "Always allow all atlassian tools in this workspace"],
		]);
		expect(alone.result.updatedPermissions).toEqual(suggestions);

		const server = await ask(tool, {}, suggestions, "allow_always_server");
		expect(server.result.updatedPermissions).toEqual([
			{ type: "addRules", rules: [{ toolName: "mcp__atlassian" }], behavior: "allow", destination: "localSettings" },
		]);

		// With no suggestion to copy a destination from, the wider grant stays in memory.
		const unsuggested = await ask(tool, {}, undefined, "allow_always_server");
		expect(unsuggested.result.updatedPermissions[0].destination).toBe("session");
		const once = await ask(tool, {}, suggestions, "allow");
		expect(once.result).toEqual({ behavior: "allow", updatedInput: {} });
	});

	it("refuses an always-choice that was not offered for the tool", async () => {
		const command = 'node -e "x"';
		// allow_always_exact is the id an earlier revision of this patch offered.
		for (const optionId of ["allow_always_server", "allow_always_exact", "allow_always_anything"]) {
			const { result } = await ask("Bash", { command }, exactRule(command), optionId);
			expect(result.behavior, optionId).toBe("deny");
		}
		const { result } = await ask("Bash", { command }, exactRule(command), "reject");
		expect(result.behavior).toBe("deny");
	});

	it("restores Claude Code's own scope when AO_CLAUDE_EXACT_ALWAYS_ALLOW is 1", async () => {
		const previous = process.env.AO_CLAUDE_EXACT_ALWAYS_ALLOW;
		process.env.AO_CLAUDE_EXACT_ALWAYS_ALLOW = "1";
		try {
			const command = 'node -e "x"';
			const bash = await ask("Bash", { command }, exactRule(command), "allow_always");
			expect(bash.offered.filter((option) => option.kind === "allow_always")).toHaveLength(1);
			expect(bash.result.updatedPermissions).toEqual(exactRule(command));
			const mcp = await ask("mcp__atlassian__createJiraIssue", {}, undefined, "allow_always");
			expect(mcp.offered.filter((option) => option.kind === "allow_always")).toHaveLength(1);
		} finally {
			if (previous === undefined) delete process.env.AO_CLAUDE_EXACT_ALWAYS_ALLOW;
			else process.env.AO_CLAUDE_EXACT_ALWAYS_ALLOW = previous;
		}
	});
});

describe("pruneNodeDistribution", () => {
	it("removes Unix package-manager links before deleting their targets", () => {
		const nodeRoot = temporaryDirectory();
		const bin = join(nodeRoot, "bin");
		const npmBin = join(nodeRoot, "lib", "node_modules", "npm", "bin");
		mkdirSync(bin, { recursive: true });
		mkdirSync(npmBin, { recursive: true });
		writeFileSync(join(bin, "node"), "node");
		writeFileSync(join(npmBin, "npm-cli.js"), "npm");
		writeFileSync(join(npmBin, "npx-cli.js"), "npx");
		symlinkSync("../lib/node_modules/npm/bin/npm-cli.js", join(bin, "npm"));
		symlinkSync("../lib/node_modules/npm/bin/npx-cli.js", join(bin, "npx"));
		symlinkSync("../lib/node_modules/corepack/dist/corepack.js", join(bin, "corepack"));

		pruneNodeDistribution(nodeRoot);

		expect(readdirSync(bin)).toEqual(["node"]);
		expect(existsSync(join(nodeRoot, "lib"))).toBe(false);
	});

	it("removes package-manager files and modules from a Windows distribution", () => {
		const nodeRoot = temporaryDirectory();
		writeFileSync(join(nodeRoot, "node.exe"), "node");
		writeFileSync(join(nodeRoot, "LICENSE"), "license");
		for (const name of ["corepack", "corepack.cmd", "npm", "npm.cmd", "npx", "npx.cmd"]) {
			writeFileSync(join(nodeRoot, name), name);
		}
		mkdirSync(join(nodeRoot, "node_modules", "npm"), { recursive: true });

		pruneNodeDistribution(nodeRoot);

		expect(readdirSync(nodeRoot).sort()).toEqual(["LICENSE", "node.exe"]);
	});
});

function temporaryDirectory() {
	const directory = mkdtempSync(join(tmpdir(), "ao-acp-runtime-test-"));
	temporaryDirectories.push(directory);
	return directory;
}

describe("archiveExtraction", () => {
	// Extraction must not go through PowerShell's Expand-Archive: it is bound by
	// MAX_PATH, and with LongPathsEnabled=0 a deep checkout pushes Node's bundled
	// npm tree past 260 characters, where it fails while still exiting zero.
	it("uses bsdtar for the Windows zip", () => {
		expect(archiveExtraction("C:\\w\\node.zip", "C:\\w", {
			platform: "win32",
			systemRoot: "C:\\Windows",
		})).toEqual({
			command: "C:\\Windows\\System32\\tar.exe",
			args: ["-xf", "C:\\w\\node.zip", "-C", "C:\\w"],
		});
	});

	it("keeps gzip handling on the other platforms", () => {
		for (const platform of ["darwin", "linux"]) {
			expect(archiveExtraction("/w/node.tar.gz", "/w", { platform })).toEqual({
				command: "tar",
				args: ["-xzf", "/w/node.tar.gz", "-C", "/w"],
			});
		}
	});

	it("never shells out to a command interpreter", () => {
		for (const platform of ["win32", "darwin", "linux"]) {
			const { command } = archiveExtraction("/w/a", "/w", { platform, systemRoot: "C:\\Windows" });
			expect(command).not.toMatch(/powershell|cmd\.exe|\bsh\b/i);
		}
	});
});
