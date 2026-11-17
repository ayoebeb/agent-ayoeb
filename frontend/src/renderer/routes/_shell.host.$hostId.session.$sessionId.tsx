import { createFileRoute } from "@tanstack/react-router";
import { SessionView } from "../components/SessionView";
import { useRouteInSplit } from "../hooks/useSplitWorkspaces";
import { refKey } from "../lib/hosts";

export const Route = createFileRoute("/_shell/host/$hostId/session/$sessionId")({
	component: HostSessionRoute,
});

function HostSessionRoute() {
	const { hostId, sessionId } = Route.useParams();
	// While the split is on screen the shell's split view renders this session as a pane.
	const inSplit = useRouteInSplit({ sessionId, hostId });
	if (inSplit) return null;
	return <SessionView key={refKey({ host: hostId, id: sessionId })} hostId={hostId} sessionId={sessionId} />;
}
