import { useFocusEffect, useLocalSearchParams, useNavigation } from "expo-router";
import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Linking, RefreshControl, SectionList, StyleSheet, Text, View } from "react-native";
import { getJiraBoard, jiraFailure, jiraSections, startJiraTicket, type JiraBoard, type JiraFailure, type JiraTicket } from "../../lib/jira";
import { refreshControlColors } from "../../lib/refreshControlColors";
import { Spinner } from "../../lib/Spinner";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { ToastHost } from "../../lib/Toast";
import { fontScaleCap, gutter, radius, space, type } from "../../lib/tokens";
import { Button, EmptyState, ListSectionHeader } from "../../lib/ui";
import { useToast } from "../../lib/useToast";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

/** How often an open page re-reads the board. */
const REFRESH_MS = 60_000;

type Failure = { kind: JiraFailure; message: string };

/**
 * A project's Jira tickets in the columns of its Jira board, each with a button
 * that sends it to the project's orchestrator. Jira is connected once, in the
 * desktop app's project settings; the machine holds the token.
 */
export default function JiraTicketsScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const navigation = useNavigation();
	const { projectId, hostId } = useLocalSearchParams<{ projectId: string; hostId?: string }>();
	const { config, configForHost } = useApp();
	const cfg = hostId ? configForHost(hostId) : config;
	const toast = useToast();
	const { show: showToast } = toast;
	const [board, setBoard] = useState<JiraBoard>();
	const [failure, setFailure] = useState<Failure>();
	const [loading, setLoading] = useState(true);
	const [refreshing, setRefreshing] = useState(false);
	const [startingKey, setStartingKey] = useState<string>();
	const [mineOnly, setMineOnly] = useState(false);
	const requestId = useRef(0);

	useLayoutEffect(() => navigation.setOptions({ title: "Jira" }), [navigation]);

	const load = useCallback(async () => {
		if (!cfg || !projectId) return;
		const mine = ++requestId.current;
		try {
			const next = await getJiraBoard(cfg, projectId);
			if (mine !== requestId.current) return;
			setBoard(next);
			setFailure(undefined);
		} catch (error) {
			if (mine !== requestId.current) return;
			setFailure({ kind: jiraFailure(error), message: error instanceof Error ? error.message : "Could not load Jira tickets." });
		} finally {
			if (mine === requestId.current) setLoading(false);
		}
	}, [cfg, projectId]);

	// Read when the page opens and every minute while it is in front.
	useFocusEffect(useCallback(() => {
		void load();
		const timer = setInterval(() => void load(), REFRESH_MS);
		return () => clearInterval(timer);
	}, [load]));

	const onRefresh = useCallback(async () => {
		setRefreshing(true);
		try {
			await load();
		} finally {
			setRefreshing(false);
		}
	}, [load]);

	const start = useCallback(async (ticket: JiraTicket) => {
		if (!cfg || !projectId || startingKey) return;
		setStartingKey(ticket.key);
		try {
			const result = await startJiraTicket(cfg, projectId, ticket.key);
			showToast(result.duplicate ? `${ticket.key} was just sent` : `Sent ${ticket.key} to the orchestrator`, { tone: "success" });
			void load();
		} catch (error) {
			showToast(error instanceof Error ? error.message : `Could not send ${ticket.key}`, { tone: "warning" });
		} finally {
			setStartingKey(undefined);
		}
	}, [cfg, projectId, startingKey, showToast, load]);

	const sections = useMemo(() => (board ? jiraSections(board, mineOnly) : []), [board, mineOnly]);

	if (!cfg) {
		return <View style={styles.screen}><EmptyState icon="server" title="Open this project from its machine" message="Jira tickets load through the machine that runs the project." /></View>;
	}
	if (loading && !board) {
		return <View style={[styles.screen, styles.center]}><Spinner color={t.accent} accessibilityLabel="Loading Jira tickets" /></View>;
	}
	if (!board) {
		return <View style={styles.screen}><FailureState failure={failure} onRetry={onRefresh} /></View>;
	}

	return (
		<View style={styles.screen}>
			<SectionList
				sections={sections}
				keyExtractor={(ticket) => ticket.key}
				contentInsetAdjustmentBehavior="automatic"
				stickySectionHeadersEnabled={false}
				refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} {...refreshControlColors(t)} />}
				ListHeaderComponent={(
					<View style={styles.toolbar}>
						<Button title="Assigned to me" variant={mineOnly ? "primary" : "secondary"} disabled={!board.accountId} onPress={() => setMineOnly((current) => !current)} />
						{failure ? <Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.stale}>Couldn't refresh. Showing the last tickets.</Text> : null}
					</View>
				)}
				ListEmptyComponent={(
					<EmptyState
						icon="inbox"
						title={mineOnly ? "Nothing is assigned to you" : "No tickets to show"}
						message={mineOnly ? "Show all tickets to see the rest." : "Jira returned no tickets for this project's query."}
						action={mineOnly ? <Button title="Show all tickets" onPress={() => setMineOnly(false)} /> : undefined}
					/>
				)}
				renderSectionHeader={({ section }) => <ListSectionHeader label={section.title} count={section.data.length} />}
				renderItem={({ item }) => (
					<TicketCard ticket={item} starting={startingKey === item.key} onStart={() => void start(item)} />
				)}
				contentContainerStyle={styles.list}
			/>
			<ToastHost toast={toast} />
		</View>
	);
}

function TicketCard({ ticket, starting, onStart }: { ticket: JiraTicket; starting: boolean; onStart: () => void }) {
	const styles = useThemedStyles(makeStyles);
	const meta = [ticket.issueType, ticket.priority].filter(Boolean).join(" · ");
	const sent = (ticket.sessions?.length ?? 0) > 0;
	return (
		<View style={styles.card} testID={`jira-ticket-${ticket.key}`}>
			<View style={styles.cardHead}>
				<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.key}>{ticket.key}</Text>
				{meta ? <Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.meta} numberOfLines={1}>{meta}</Text> : null}
			</View>
			<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.summary}>{ticket.summary}</Text>
			<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.meta} numberOfLines={1}>{ticket.assignee?.displayName ?? "Unassigned"}</Text>
			{sent ? <Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.sent}>Sent to the orchestrator</Text> : null}
			<View style={styles.actions}>
				<Button title="Start with orchestrator" variant="secondary" loading={starting} onPress={onStart} />
				<Button title="Open in Jira" variant="ghost" onPress={() => void Linking.openURL(ticket.url)} />
			</View>
		</View>
	);
}

function FailureState({ failure, onRetry }: { failure?: Failure; onRetry: () => void }) {
	const retry = <Button title="Try again" icon="refresh-cw" onPress={onRetry} />;
	switch (failure?.kind) {
		case "not_connected":
			return <EmptyState icon="link" title="Jira isn't connected" message="Connect it in the desktop app: project settings, Jira." />;
		case "auth":
			return <EmptyState icon="alert-circle" title="Jira rejected the sign-in" message="The email or API token no longer works. Update the connection in the desktop app's project settings." action={retry} />;
		case "offline":
			return <EmptyState icon="wifi-off" title="Can't reach Jira" message="Check the connection. The page tries again every minute." action={retry} />;
		case "bad_query":
			return <EmptyState icon="alert-circle" title="Jira didn't accept the query" message={failure.message} action={retry} />;
		default:
			return <EmptyState icon="alert-circle" title="Couldn't load Jira tickets" message={failure?.message ?? "Try again in a moment."} action={retry} />;
	}
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { alignItems: "center", justifyContent: "center" },
		list: { paddingBottom: space.xxxl },
		toolbar: { flexDirection: "row", alignItems: "center", gap: space.md, paddingHorizontal: gutter, paddingVertical: space.md },
		stale: { ...type.footnote, color: t.textTertiary, flexShrink: 1 },
		card: {
			marginHorizontal: gutter,
			marginBottom: space.md,
			padding: space.md,
			gap: space.xs,
			borderRadius: radius.md,
			backgroundColor: t.bgSurface,
		},
		cardHead: { flexDirection: "row", alignItems: "center", gap: space.sm },
		key: { ...type.caption1, color: t.textSecondary },
		meta: { ...type.footnote, color: t.textTertiary, flexShrink: 1 },
		summary: { ...type.headline, color: t.textPrimary },
		sent: { ...type.caption1, color: t.accent },
		actions: { flexDirection: "row", alignItems: "center", gap: space.sm, paddingTop: space.sm },
	});
