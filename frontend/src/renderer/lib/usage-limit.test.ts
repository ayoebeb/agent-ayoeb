import { describe, expect, it } from "vitest";
import { formatUsageLimitReset } from "./usage-limit";

describe("formatUsageLimitReset", () => {
	const now = Date.parse("2026-11-16T14:20:00Z");

	it("gives just a short clock time for a reset within a day", () => {
		const onTheHour = formatUsageLimitReset("2026-11-16T17:00:00Z", now, "en-US");
		expect(onTheHour).toMatch(/^\d{1,2} ?(AM|PM)$/i);
		const halfPast = formatUsageLimitReset("2026-11-16T17:30:00Z", now, "en-US");
		expect(halfPast).toMatch(/^\d{1,2}:30 ?(AM|PM)$/i);
	});

	it("adds the weekday for a reset more than a day away, such as a weekly limit", () => {
		expect(formatUsageLimitReset("2026-11-20T09:00:00Z", now, "en-US")).toMatch(/^[A-Za-z]{3}/);
	});

	it("says nothing when the provider gave no usable reset time", () => {
		expect(formatUsageLimitReset(undefined, now)).toBeUndefined();
		expect(formatUsageLimitReset("", now)).toBeUndefined();
		expect(formatUsageLimitReset("not a date", now)).toBeUndefined();
	});
});
