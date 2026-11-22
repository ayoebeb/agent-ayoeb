import { useCallback, useEffect, useRef, useState } from "react";
import { HIDDEN_DAILY_UPDATE, type DailyUpdateState } from "../../shared/daily-update";
import { aoBridge } from "../lib/bridge";

/** A file read in the main process, so a few seconds is cheap. */
export const dailyUpdatePollMs = 5_000;

/** The daily Ayo's "update ready" state, and the click that applies it. */
export function useDailyUpdate() {
	const [status, setStatus] = useState<DailyUpdateState>(HIDDEN_DAILY_UPDATE);
	const mounted = useRef(true);
	useEffect(() => {
		mounted.current = true;
		const read = () => {
			// Optional: a bridge from before this feature (or a test double) has no such channel.
			void aoBridge.dailyUpdate?.get().then(
				(next) => mounted.current && setStatus(next),
				() => undefined,
			);
		};
		read();
		const timer = setInterval(read, dailyUpdatePollMs);
		return () => {
			mounted.current = false;
			clearInterval(timer);
		};
	}, []);
	const apply = useCallback(() => {
		// Show "restarting" at once; the main process confirms or reports the failure.
		setStatus((current) => (current.state === "hidden" ? current : { ...current, state: "restarting" }));
		void aoBridge.dailyUpdate?.apply().then(
			(next) => mounted.current && setStatus(next),
			() => undefined,
		);
	}, []);
	return { status, apply };
}
