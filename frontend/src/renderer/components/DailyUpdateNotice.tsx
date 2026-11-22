import { RefreshCw } from "@/components/app-icons";
import { useTranslation } from "react-i18next";
import { useDailyUpdate } from "../hooks/useDailyUpdate";
import type { DailyUpdateState } from "../../shared/daily-update";
import { Button } from "./ui/button";

/**
 * The daily Ayo's update notice, in the sidebar footer. The watcher found
 * merged changes that need a restart; nothing restarts until the user clicks.
 * Renders nothing when no update waits, so it costs a poll and no layout.
 */
export function DailyUpdateNotice({ tabIndex = 0 }: { tabIndex?: number }) {
	const { status, apply } = useDailyUpdate();
	return <DailyUpdateNoticeView onRestart={apply} status={status} tabIndex={tabIndex} />;
}

export function DailyUpdateNoticeView({
	onRestart,
	status,
	tabIndex = 0,
}: {
	onRestart: () => void;
	status: DailyUpdateState;
	tabIndex?: number;
}) {
	const { t } = useTranslation();
	if (status.state === "hidden") return null;
	const restarting = status.state === "restarting";
	const failed = status.state === "failed";
	return (
		<div
			className="mb-1 flex min-h-9 w-full items-center gap-2 rounded-lg bg-muted px-3 py-1 text-sm text-foreground"
			data-state={status.state}
			data-testid="daily-update-notice"
			role="status"
		>
			<RefreshCw
				aria-hidden="true"
				className={`size-icon-sm shrink-0 ${failed ? "text-destructive" : "text-muted-foreground"} ${restarting ? "animate-spin motion-reduce:animate-none" : ""}`}
			/>
			<span className="min-w-0 flex-1 text-xs leading-tight tracking-tight" title={failed ? status.message : undefined}>
				{restarting
					? t("shell.dailyUpdate.restarting")
					: failed
						? t("shell.dailyUpdate.failed")
						: t("shell.dailyUpdate.ready", { count: status.changes })}
			</span>
			{restarting ? null : (
				<Button
					aria-label={t("shell.dailyUpdate.restartAria")}
					data-testid="daily-update-restart"
					onClick={onRestart}
					size="xs"
					tabIndex={tabIndex}
					type="button"
					variant="secondary"
				>
					{failed ? t("shell.dailyUpdate.retry") : t("shell.dailyUpdate.restart")}
				</Button>
			)}
		</div>
	);
}
