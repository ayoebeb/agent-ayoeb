/**
 * The clock time a usage-limit window resets, in the person's own locale and
 * zone, kept short for a sidebar row: "11 PM" or "6:30 PM", with the weekday
 * ("Fri 9 AM") only when the reset is more than a day away, as a weekly limit
 * is. Undefined when the provider gave no usable time.
 */
export function formatUsageLimitReset(resetsAt: string | undefined, now: number = Date.now(), locale?: string): string | undefined {
	if (!resetsAt) return undefined;
	const reset = Date.parse(resetsAt);
	if (!Number.isFinite(reset)) return undefined;
	const date = new Date(reset);
	const farOff = reset - now >= 24 * 60 * 60 * 1000;
	return date.toLocaleString(locale, {
		...(farOff ? { weekday: "short" } : {}),
		hour: "numeric",
		// "11 PM" rather than "11:00 PM": the minutes only matter when they are not zero.
		...(date.getMinutes() === 0 ? {} : { minute: "2-digit" }),
	});
}
