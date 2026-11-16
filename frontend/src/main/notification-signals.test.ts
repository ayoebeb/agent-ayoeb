// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
	dockBounceType,
	lookingAtNotification,
	osToastChimeControllable,
	shouldReplaceBounce,
	shouldSignalAttention,
	shouldToast,
	soundEnabledFor,
	toastSilent,
	type NotificationType,
} from "./notification-signals";

const ALL_TYPES: NotificationType[] = [
	"needs_input",
	"ready_to_merge",
	"pr_merged",
	"pr_closed_unmerged",
	"orchestrator_waiting",
];

describe("orchestrator_waiting", () => {
	it("toasts, flashes the taskbar and bounces the dock until the app is opened", () => {
		expect(shouldToast({ title: "Mercury: orchestrator wacht op u" }, true)).toBe(true);
		expect(shouldSignalAttention("orchestrator_waiting")).toBe(true);
		expect(dockBounceType("orchestrator_waiting")).toBe("critical");
	});

	// A focused window can be showing anything. Only the renderer saying the
	// orchestrator's own conversation is open counts as looking at the question.
	it("is only seen when its own conversation is on screen, not merely when the window is focused", () => {
		expect(lookingAtNotification("orchestrator_waiting", { windowFocused: true, watched: false })).toBe(false);
		expect(lookingAtNotification("orchestrator_waiting", { windowFocused: true, watched: undefined })).toBe(false);
		expect(lookingAtNotification("orchestrator_waiting", { windowFocused: false, watched: true })).toBe(true);
	});

	it("keeps the focused-window rule for every other type", () => {
		for (const type of ["needs_input", "ready_to_merge", "pr_merged", undefined]) {
			expect(lookingAtNotification(type, { windowFocused: true, watched: false })).toBe(true);
			expect(lookingAtNotification(type, { windowFocused: false, watched: true })).toBe(true);
			expect(lookingAtNotification(type, { windowFocused: false, watched: false })).toBe(false);
		}
	});

	it("has its own sound switch, so silencing the general sound does not silence it", () => {
		expect(soundEnabledFor("orchestrator_waiting", { general: false, orchestratorWaiting: true })).toBe(true);
		expect(soundEnabledFor("orchestrator_waiting", { general: true, orchestratorWaiting: false })).toBe(false);
		expect(soundEnabledFor("needs_input", { general: false, orchestratorWaiting: true })).toBe(false);
		expect(soundEnabledFor("needs_input", { general: true, orchestratorWaiting: false })).toBe(true);
		expect(soundEnabledFor(undefined, { general: true, orchestratorWaiting: false })).toBe(true);
	});
});

describe("shouldToast", () => {
	it("fires a toast for every backend notification type", () => {
		for (const type of ALL_TYPES) {
			expect(shouldToast({ title: `${type} title` }, true)).toBe(true);
		}
	});

	it("does not toast without a title or when notifications are unsupported", () => {
		expect(shouldToast({ title: "" }, true)).toBe(false);
		expect(shouldToast({}, true)).toBe(false);
		expect(shouldToast({ title: "needs input" }, false)).toBe(false);
	});
});

describe("shouldSignalAttention", () => {
	it("flashes the taskbar for the actionable types", () => {
		expect(shouldSignalAttention("needs_input")).toBe(true);
		expect(shouldSignalAttention("ready_to_merge")).toBe(true);
	});

	it("does not flash the taskbar for informational PR outcomes", () => {
		expect(shouldSignalAttention("pr_merged")).toBe(false);
		expect(shouldSignalAttention("pr_closed_unmerged")).toBe(false);
	});

	it("does not signal for unknown or missing types", () => {
		expect(shouldSignalAttention("some_future_type")).toBe(false);
		expect(shouldSignalAttention(undefined)).toBe(false);
	});
});

describe("shouldReplaceBounce", () => {
	it("replaces no bounce or a pending informational bounce", () => {
		expect(shouldReplaceBounce(null)).toBe(true);
		expect(shouldReplaceBounce({ critical: false })).toBe(true);
	});

	it("never replaces a pending critical bounce, so a blocked agent stays loud", () => {
		expect(shouldReplaceBounce({ critical: true })).toBe(false);
	});
});

describe("osToastChimeControllable", () => {
	// Platform contract: Electron's mac and win presenters read `silent`;
	// the Linux libnotify presenter does not (no suppress-sound hint).
	it.each(["darwin", "win32"] as const)("is controllable on %s", (platform) => {
		expect(osToastChimeControllable(platform)).toBe(true);
	});

	it.each(["linux", "freebsd", "openbsd"] as const)("is not controllable on %s", (platform) => {
		expect(osToastChimeControllable(platform)).toBe(false);
	});
});

describe("toastSilent", () => {
	const HONOURING: NodeJS.Platform[] = ["darwin", "win32"];

	it.each(HONOURING)("on %s mutes the toast whenever sound notifications are off", (platform) => {
		expect(toastSilent(platform, false, false)).toBe(true);
		expect(toastSilent(platform, false, true)).toBe(true);
	});

	it.each(HONOURING)("on %s mutes the toast when AO plays its own sound", (platform) => {
		expect(toastSilent(platform, true, true)).toBe(true);
	});

	it.each(HONOURING)("on %s keeps the native chime for an informational toast when sound is on", (platform) => {
		expect(toastSilent(platform, true, false)).toBe(false);
	});

	// On Linux the value would be ignored, so no combination may claim a mute.
	it.each([
		[false, false],
		[false, true],
		[true, false],
		[true, true],
	])("on linux never claims control (enabled=%s, playsSound=%s)", (enabled, playsSound) => {
		expect(toastSilent("linux", enabled, playsSound)).toBeUndefined();
	});
});

describe("dockBounceType", () => {
	it("bounces critically for a blocked agent waiting on the user", () => {
		expect(dockBounceType("needs_input")).toBe("critical");
	});

	it("bounces critically for a project orchestrator waiting on the user", () => {
		expect(dockBounceType("orchestrator_waiting")).toBe("critical");
	});

	it("bounces once for the other backend types", () => {
		for (const type of ALL_TYPES.filter((t) => t !== "needs_input" && t !== "orchestrator_waiting")) {
			expect(dockBounceType(type)).toBe("informational");
		}
	});

	it("bounces once for unknown or missing types", () => {
		expect(dockBounceType("some_future_type")).toBe("informational");
		expect(dockBounceType(undefined)).toBe("informational");
	});
});
