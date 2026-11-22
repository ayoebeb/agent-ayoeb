import { create } from "zustand";

const STORAGE_KEY = "ao.machines.claudeAccount";

/**
 * Which Claude account a linked computer uses, relative to this one. The user
 * says so; Ayo does not read another computer's agent login. It is kept per
 * desktop, not synced, and later lets "Run on" show how much quota is left.
 */
export type ClaudeAccountChoice = "same" | "different";

type Choices = Record<string, ClaudeAccountChoice>;

function load(): Choices {
	try {
		const saved: unknown = JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? "null");
		if (saved && typeof saved === "object" && !Array.isArray(saved)) {
			return Object.fromEntries(
				Object.entries(saved).filter((entry): entry is [string, ClaudeAccountChoice] => entry[1] === "same" || entry[1] === "different"),
			);
		}
	} catch {}
	return {};
}

function persist(choices: Choices) {
	try {
		window.localStorage.setItem(STORAGE_KEY, JSON.stringify(choices));
	} catch {}
}

type MachineAccountsStore = {
	byHost: Choices;
	/** `null` clears the choice, which reads as "not set". */
	setChoice: (hostId: string, choice: ClaudeAccountChoice | null) => void;
};

export const useMachineAccountsStore = create<MachineAccountsStore>((set, get) => ({
	byHost: load(),
	setChoice: (hostId, choice) => {
		const next = { ...get().byHost };
		if (choice === null) delete next[hostId];
		else next[hostId] = choice;
		persist(next);
		set({ byHost: next });
	},
}));
