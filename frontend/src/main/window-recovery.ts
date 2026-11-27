import type { WebContents } from "electron";
import type { DiagnosticsLog } from "./window-diagnostics";

/**
 * Notices a shell window that has stopped showing the app, records why, and
 * reloads it. Every recovery is logged and capped, so a real fault stays visible
 * instead of turning into a reload loop.
 */

export const UNRESPONSIVE_RELOAD_MS = 30_000;
export const BLANK_CHECK_DELAY_MS = 5_000;
export const HEALTH_CHECK_INTERVAL_MS = 30_000;
export const CRASH_RELOAD_DELAY_MS = 1_000;
const MAX_RELOADS = 3;
const RELOAD_WINDOW_MS = 60 * 60_000;
const MAX_CONSOLE_LINES_PER_MINUTE = 20;
const GPU_CRASH_LIMIT = 3;
// -3 (ERR_ABORTED) is a navigation replaced by another, not a failure.
const ERR_ABORTED = -3;
const RENDERED_PROBE = `(() => { const root = document.getElementById("root"); return !!root && root.childElementCount > 0; })()`;

export type ReloadBudget = { take: () => boolean };

/** At most `max` automatic reloads per rolling hour, shared by every cause. */
export function createReloadBudget(options: { max?: number; windowMs?: number; now?: () => number } = {}): ReloadBudget {
	const max = options.max ?? MAX_RELOADS;
	const windowMs = options.windowMs ?? RELOAD_WINDOW_MS;
	const now = options.now ?? Date.now;
	let taken: number[] = [];
	return {
		take: () => {
			const time = now();
			taken = taken.filter((at) => time - at < windowMs);
			if (taken.length >= max) return false;
			taken.push(time);
			return true;
		},
	};
}

/** Query strings and fragments can carry tokens; the path is enough here. */
function loggableUrl(url: string): string {
	return url.split(/[?#]/, 1)[0] ?? "";
}

export type RecoverableContents = Pick<
	WebContents,
	"on" | "executeJavaScript" | "reload" | "loadURL" | "getURL" | "isDestroyed" | "isLoadingMainFrame" | "forcefullyCrashRenderer"
>;

export function installWindowRecovery(options: {
	contents: RecoverableContents;
	rendererUrl: () => string;
	log: DiagnosticsLog;
	budget: ReloadBudget;
	isQuitting?: () => boolean;
	/** Puts a mis-sized shell view back; returns what was wrong, or null. */
	repairShellView?: () => string | null;
}): () => void {
	const { contents, log } = options;
	let disposed = false;
	let loaded = false;
	let forcedCrash = false;
	let checking = false;
	let blankReloadUsed = false;
	let blankPersistsLogged = false;
	let unresponsiveSince = 0;
	let hangTimer: ReturnType<typeof setTimeout> | undefined;
	let blankTimer: ReturnType<typeof setTimeout> | undefined;
	let crashTimer: ReturnType<typeof setTimeout> | undefined;
	let consoleWindowStart = 0;
	let consoleLines = 0;
	let consoleSuppressed = 0;

	const gone = () => disposed || contents.isDestroyed() || options.isQuitting?.() === true;

	const clearHang = () => {
		if (hangTimer) clearTimeout(hangTimer);
		hangTimer = undefined;
	};
	const clearBlankTimer = () => {
		if (blankTimer) clearTimeout(blankTimer);
		blankTimer = undefined;
	};

	const reload = (cause: string): void => {
		if (gone()) return;
		if (!options.budget.take()) {
			log("reload-skipped", { cause, reason: "hourly-limit" });
			return;
		}
		log("reload", { cause });
		const url = contents.getURL();
		// A first load that never committed has nothing to reload.
		if (!url || url.startsWith("chrome-error:")) {
			void contents.loadURL(options.rendererUrl()).catch((error: unknown) => log("reload-failed", { cause, error: String(error) }));
		} else {
			contents.reload();
		}
	};

	const checkRendered = async (trigger: "load" | "periodic"): Promise<void> => {
		if (checking || gone()) return;
		checking = true;
		try {
			const rendered: unknown = await contents.executeJavaScript(RENDERED_PROBE);
			if (gone()) return;
			if (rendered === true) {
				if (blankReloadUsed) log("blank-screen-recovered");
				blankReloadUsed = false;
				blankPersistsLogged = false;
				return;
			}
			if (blankReloadUsed) {
				if (!blankPersistsLogged) log("blank-screen-persists", { trigger });
				blankPersistsLogged = true;
				return;
			}
			log("blank-screen", { trigger, url: loggableUrl(contents.getURL()) });
			blankReloadUsed = true;
			reload("blank-screen");
		} catch (error) {
			log("blank-check-failed", { error: String(error) });
		} finally {
			checking = false;
		}
	};

	const scheduleBlankCheck = () => {
		clearBlankTimer();
		blankTimer = setTimeout(() => {
			blankTimer = undefined;
			void checkRendered("load");
		}, BLANK_CHECK_DELAY_MS);
	};

	contents.on("render-process-gone", (_event, details) => {
		log("render-process-gone", { reason: details.reason, exitCode: details.exitCode });
		clearHang();
		clearBlankTimer();
		// The unresponsive path crashed it on purpose and is already reloading.
		if (forcedCrash) {
			forcedCrash = false;
			return;
		}
		if (details.reason === "clean-exit") return;
		if (crashTimer) clearTimeout(crashTimer);
		crashTimer = setTimeout(() => reload("render-process-gone"), CRASH_RELOAD_DELAY_MS);
	});

	contents.on("unresponsive", () => {
		if (hangTimer || gone()) return;
		unresponsiveSince = Date.now();
		log("unresponsive");
		hangTimer = setTimeout(() => {
			hangTimer = undefined;
			if (gone()) return;
			log("unresponsive-timeout", { afterMs: UNRESPONSIVE_RELOAD_MS });
			if (!options.budget.take()) {
				log("reload-skipped", { cause: "unresponsive", reason: "hourly-limit" });
				return;
			}
			log("reload", { cause: "unresponsive" });
			// A hung renderer ignores reload(), so end its process first.
			forcedCrash = true;
			contents.forcefullyCrashRenderer();
			contents.reload();
		}, UNRESPONSIVE_RELOAD_MS);
	});

	contents.on("responsive", () => {
		if (!hangTimer) return;
		clearHang();
		log("responsive", { afterMs: Date.now() - unresponsiveSince });
	});

	// Main-frame document navigations after the first load are full reloads
	// (Vite's page reload, the error screen's button, our own recovery).
	contents.on("did-start-navigation", (details) => {
		if (!details.isMainFrame || details.isSameDocument) return;
		clearBlankTimer();
		if (loaded) log("page-reload", { url: loggableUrl(details.url) });
	});

	contents.on("did-finish-load", () => {
		log("did-finish-load");
		loaded = true;
		forcedCrash = false;
		scheduleBlankCheck();
	});

	contents.on("did-fail-load", (_event, errorCode, errorDescription, url, isMainFrame) => {
		if (errorCode === ERR_ABORTED) return;
		log("did-fail-load", { errorCode, errorDescription, url: loggableUrl(url), isMainFrame });
		if (isMainFrame) scheduleBlankCheck();
	});

	contents.on("console-message", (details) => {
		const { level, message } = details;
		const vite = message.startsWith("[vite]") && !message.startsWith("[vite] hot updated");
		if (level !== "error" && !vite) return;
		const time = Date.now();
		if (time - consoleWindowStart >= 60_000) {
			if (consoleSuppressed > 0) log("console-suppressed", { count: consoleSuppressed });
			consoleWindowStart = time;
			consoleLines = 0;
			consoleSuppressed = 0;
		}
		if (consoleLines >= MAX_CONSOLE_LINES_PER_MINUTE) {
			consoleSuppressed += 1;
			return;
		}
		consoleLines += 1;
		log(vite ? "vite" : "console-error", { message, source: loggableUrl(details.sourceId), line: details.lineNumber });
	});

	// The window can also go blank long after loading, so keep looking.
	const healthTimer = setInterval(() => {
		if (gone()) return;
		const repaired = options.repairShellView?.();
		if (repaired) log("shell-view-repaired", { detail: repaired });
		if (!loaded || blankTimer || hangTimer || contents.isLoadingMainFrame()) return;
		void checkRendered("periodic");
	}, HEALTH_CHECK_INTERVAL_MS);

	const dispose = () => {
		disposed = true;
		clearHang();
		clearBlankTimer();
		if (crashTimer) clearTimeout(crashTimer);
		clearInterval(healthTimer);
	};
	contents.on("destroyed", dispose);
	return dispose;
}

/**
 * Logs helper processes that die (GPU, network, utility) and reports once per run
 * when the GPU process keeps crashing.
 */
export function createChildProcessMonitor(options: {
	log: DiagnosticsLog;
	onRepeatedGpuCrash: () => void;
	now?: () => number;
}): (details: Electron.Details) => void {
	const now = options.now ?? Date.now;
	let crashes: number[] = [];
	let reported = false;
	return (details) => {
		if (details.reason === "clean-exit") return;
		options.log("child-process-gone", {
			type: details.type,
			reason: details.reason,
			exitCode: details.exitCode,
			name: details.name,
			serviceName: details.serviceName,
		});
		if (details.type !== "GPU") return;
		const time = now();
		crashes = [...crashes.filter((at) => time - at < RELOAD_WINDOW_MS), time];
		if (crashes.length < GPU_CRASH_LIMIT || reported) return;
		reported = true;
		options.log("gpu-repeated-crash", { count: crashes.length });
		options.onRepeatedGpuCrash();
	};
}
