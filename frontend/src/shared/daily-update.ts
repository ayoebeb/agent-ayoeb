/**
 * The "update ready" notice of a daily (checkout-based) Ayo. The daily update
 * watcher (scripts/daily-ao/update-daily-ao.ps1 -Watch) leaves a
 * pending-restart.json in the daily dir when merged commits need a restart;
 * the app shows it and the Restart button runs the same script with -Yes.
 *
 * hidden      nothing waiting, or this is not a daily run
 * ready       an update waits for the user's click
 * restarting  the update script is running (the window closes when it restarts Ayo)
 * failed      the script exited with an error; the checkout was rolled back
 */
export type DailyUpdateState =
	| { state: "hidden" }
	| { state: "ready"; changes: number; target: string }
	| { state: "restarting"; changes: number; target: string }
	| { state: "failed"; changes: number; target: string; message: string };

export const HIDDEN_DAILY_UPDATE: DailyUpdateState = { state: "hidden" };
