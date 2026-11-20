import { useTranslation } from "react-i18next";
import { liveServerLabel, type LiveServer } from "../../hooks/useLiveServers";
import { cn } from "../../lib/utils";
import { Pressable } from "../ui/pressable";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

/** Green while the server answers HTTP, red while it hangs or refuses. */
export function LiveHealthDot({ health, className }: { health: LiveServer["health"]; className?: string }) {
	return (
		<span
			aria-hidden="true"
			className={cn("inline-block size-1.5 shrink-0 rounded-full", health === "up" ? "bg-success" : "bg-destructive", className)}
			data-health={health}
		/>
	);
}

/**
 * One chip per running server of the project, in the control room's info row.
 * Clicking a chip opens that server in the Live tab. The dot is the health
 * reading; the tooltip spells it out so colour is never the only signal.
 */
export function LiveServerChips({ servers, onOpen }: { servers: readonly LiveServer[]; onOpen: (key: string) => void }) {
	const { t } = useTranslation();
	if (servers.length === 0) return null;
	return (
		<div aria-label={t("live.chips.aria")} className="flex min-w-0 flex-wrap items-center gap-1" data-testid="live-server-chips" role="group">
			{servers.map((server) => {
				const label = liveServerLabel(server);
				const health = t(server.health === "up" ? "live.health.up" : "live.health.down");
				return (
					<Tooltip key={server.key}>
						<TooltipTrigger asChild>
							<Pressable
								aria-label={t("live.chip.aria", { label, health })}
								className="inline-flex h-5 min-w-0 items-center gap-1.5 rounded-full border border-border px-2 font-mono text-caption text-foreground outline-none hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring"
								data-health={server.health}
								data-testid={`live-chip-${server.key}`}
								onClick={() => onOpen(server.key)}
							>
								<LiveHealthDot health={server.health} />
								<span className="truncate">{label}</span>
							</Pressable>
						</TooltipTrigger>
						<TooltipContent side="bottom">
							{server.healthNote ? `${health} · ${server.healthNote}` : health}
						</TooltipContent>
					</Tooltip>
				);
			})}
		</div>
	);
}
