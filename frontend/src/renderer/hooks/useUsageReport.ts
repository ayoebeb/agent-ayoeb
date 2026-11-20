import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { clientForSessionHost } from "../lib/host-clients";
import { apiErrorException } from "../lib/api-error-exception";
import type { UsageReport } from "../lib/usage-report";
import type { UsageLimitWindows } from "../lib/usage-limits";

export const usageReportQueryKey = ["usage", "report"] as const;

export function localTimeZone(): string {
	return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

/** hostId reads another computer's own daemon; omitted, this computer's. */
export function useUsageReport(days: number, offset: number, projectId: string | undefined, hostId?: string, enabled = true) {
	const timeZone = localTimeZone();
	return useQuery({
		queryKey: [...usageReportQueryKey, days, offset, timeZone, projectId ?? "", hostId ?? ""],
		enabled,
		queryFn: async (): Promise<UsageReport> => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/usage/report", {
				params: { query: { days, offset, tz: timeZone, ...(projectId ? { projectId } : {}) } },
			});
			if (error) throw apiErrorException(error);
			return data as UsageReport;
		},
		// Page flips and period changes keep the last report on screen; another
		// computer's numbers must never stand in for this one's while it loads.
		placeholderData: (previous, previousQuery) => (previousQuery?.queryKey[6] === (hostId ?? "") ? previous : undefined),
		refetchInterval: 60_000,
	});
}

export function useUsageLimitWindows(days: number, enabled: boolean, hostId?: string) {
	return useQuery({
		queryKey: [...usageReportQueryKey, "limit-windows", days, ...(hostId ? [hostId] : [])],
		queryFn: async (): Promise<UsageLimitWindows> => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/usage/limit-windows", { params: { query: { days } } });
			if (error) throw apiErrorException(error);
			return data as UsageLimitWindows;
		},
		enabled,
		refetchInterval: 60_000,
	});
}

export type UsagePriceOverride = components["schemas"]["UsagePriceOverride"];
export type UsagePrices = components["schemas"]["UsagePricesResponse"];
const usagePricesQueryKey = ["usage", "prices"] as const;

export function useUsagePrices() {
	return useQuery({
		queryKey: usagePricesQueryKey,
		queryFn: async (): Promise<UsagePrices> => {
			const { data, error } = await apiClient.GET("/api/v1/usage/prices");
			if (error) throw apiErrorException(error);
			return data;
		},
	});
}

export function useSaveUsagePrices() {
	const queryClient = useQueryClient();
	return async (overrides: UsagePriceOverride[]) => {
		const { data, error } = await apiClient.PUT("/api/v1/usage/prices", { body: { overrides } });
		if (error) throw apiErrorException(error);
		queryClient.setQueryData(usagePricesQueryKey, data);
		await queryClient.invalidateQueries({ queryKey: usageReportQueryKey });
		window.setTimeout(() => void queryClient.invalidateQueries({ queryKey: usageReportQueryKey }), 2_000);
	};
}
