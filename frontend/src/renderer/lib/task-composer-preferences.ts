export const TASK_COMPOSER_PREFERENCES_STORAGE_KEY = "ao.taskComposer.preferences.v1";

export type TaskComposerAgentPreference = {
	model: string;
	mode: string;
	effort?: string;
	/**
	 * The user chose `effort`. An effort saved without it predates the marker,
	 * when a launch saved the level shown whether or not anyone chose it.
	 */
	effortChosen?: true;
};

export type TaskComposerInterface = "chat" | "tui";

/**
 * The interface (Chat or Terminal) a task was last started in, when the user
 * picked it over the Settings default. It applies only while Settings still
 * default to `defaultMode`, so changing the default there is never overridden.
 */
export type TaskComposerInterfacePreference = {
	mode: TaskComposerInterface;
	defaultMode: TaskComposerInterface;
};

export type TaskComposerContextPreferences = {
	lastAgent: string;
	agents: Record<string, TaskComposerAgentPreference>;
	sessionInterface?: TaskComposerInterfacePreference;
};

type TaskComposerPreferences = Record<string, TaskComposerContextPreferences>;

function storage(): Storage | undefined {
	if (typeof window === "undefined") return undefined;
	try {
		return window.localStorage;
	} catch {
		return undefined;
	}
}

function readAll(): TaskComposerPreferences {
	try {
		const parsed: unknown = JSON.parse(storage()?.getItem(TASK_COMPOSER_PREFERENCES_STORAGE_KEY) ?? "{}");
		if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
		const preferences: TaskComposerPreferences = {};
		for (const [context, value] of Object.entries(parsed)) {
			if (!value || typeof value !== "object" || Array.isArray(value)) continue;
			const candidate = value as { lastAgent?: unknown; agents?: unknown; sessionInterface?: unknown };
			if (
				typeof candidate.lastAgent !== "string" ||
				!candidate.agents ||
				typeof candidate.agents !== "object" ||
				Array.isArray(candidate.agents)
			) {
				continue;
			}
			const agents: Record<string, TaskComposerAgentPreference> = {};
			for (const [agent, config] of Object.entries(candidate.agents)) {
				if (!config || typeof config !== "object" || Array.isArray(config)) continue;
				const fields = config as { model?: unknown; mode?: unknown; effort?: unknown; effortChosen?: unknown };
				if (typeof fields.model !== "string" || typeof fields.mode !== "string") continue;
				if (fields.effort !== undefined && typeof fields.effort !== "string") continue;
				agents[agent] = {
					model: fields.model,
					mode: fields.mode,
					...(typeof fields.effort === "string" ? { effort: fields.effort } : {}),
					...(typeof fields.effort === "string" && fields.effortChosen === true ? { effortChosen: true as const } : {}),
				};
			}
			const sessionInterface = interfacePreference(candidate.sessionInterface);
			preferences[context] = { lastAgent: candidate.lastAgent, agents, ...(sessionInterface ? { sessionInterface } : {}) };
		}
		return preferences;
	} catch {
		return {};
	}
}

function isInterface(value: unknown): value is TaskComposerInterface {
	return value === "chat" || value === "tui";
}

function interfacePreference(value: unknown): TaskComposerInterfacePreference | undefined {
	if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
	const { mode, defaultMode } = value as { mode?: unknown; defaultMode?: unknown };
	return isInterface(mode) && isInterface(defaultMode) && mode !== defaultMode ? { mode, defaultMode } : undefined;
}

function writeAll(preferences: TaskComposerPreferences): void {
	try {
		storage()?.setItem(TASK_COMPOSER_PREFERENCES_STORAGE_KEY, JSON.stringify(preferences));
	} catch {
		// Remembering composer choices is optional; spawning must still succeed.
	}
}

export function readTaskComposerPreferences(context: string): TaskComposerContextPreferences | undefined {
	return readAll()[context];
}

export function rememberTaskComposerPreference(
	context: string,
	agent: string,
	config: TaskComposerAgentPreference,
): void {
	const preferences = readAll();
	const current = preferences[context];
	preferences[context] = {
		...current,
		lastAgent: agent,
		agents: { ...current?.agents, [agent]: config },
	};
	writeAll(preferences);
}

/** Remembers the interface picked over the Settings default; undefined forgets it. */
export function rememberTaskComposerInterface(
	context: string,
	preference: TaskComposerInterfacePreference | undefined,
): void {
	const preferences = readAll();
	const { sessionInterface: _previous, ...current } = preferences[context] ?? { lastAgent: "", agents: {} };
	preferences[context] = preference && preference.mode !== preference.defaultMode
		? { ...current, sessionInterface: preference }
		: current;
	writeAll(preferences);
}
