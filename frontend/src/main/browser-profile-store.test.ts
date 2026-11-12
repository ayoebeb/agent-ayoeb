import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
	BROWSER_PROFILE_MAX_COUNT,
	BROWSER_PROFILE_REGISTRY_VERSION,
	browserProfilePartition,
	type BrowserProfileRegistry,
} from "../shared/browser-profiles";
import { BrowserProfileStore } from "./browser-profile-store";

const tempDirectories: string[] = [];

async function makeStateDir(): Promise<string> {
	const directory = await mkdtemp(path.join(os.tmpdir(), "ao-browser-profiles-"));
	tempDirectories.push(directory);
	return directory;
}

function profileId(index: number): string {
	return `00000000-0000-4000-8000-${index.toString(16).padStart(12, "0")}`;
}

function registryWithProfiles(count: number): BrowserProfileRegistry {
	const now = "2026-01-01T00:00:00.000Z";
	return {
		version: BROWSER_PROFILE_REGISTRY_VERSION,
		profiles: Array.from({ length: count }, (_, index) => ({
			id: profileId(index + 1),
			name: `Profile ${index + 1}`,
			createdAt: now,
			updatedAt: now,
		})),
		bindings: {},
		defaultProfileId: null,
		defaultProfileSeeded: true,
	};
}

afterEach(async () => {
	await Promise.all(tempDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

describe("BrowserProfileStore", () => {
	it("treats a missing registry as empty and persists named profiles atomically", async () => {
		const stateDir = await makeStateDir();
		const store = new BrowserProfileStore({ stateDir });

		expect(await store.load()).toEqual({ profiles: [], defaultProfileId: null });
		const work = await store.createProfile(" Work ");
		const personal = await store.createProfile("Personal");

		expect(work.name).toBe("Work");
		expect(store.profiles.map((profile) => profile.name)).toEqual(["Work", "Personal"]);
		const persisted = JSON.parse(await readFile(path.join(stateDir, "browser-profiles.json"), "utf8")) as BrowserProfileRegistry;
		expect(persisted.version).toBe(BROWSER_PROFILE_REGISTRY_VERSION);
		expect(persisted.profiles).toHaveLength(2);
		expect(await readdir(stateDir)).not.toContain(expect.stringContaining(".browser-profiles.json."));

		const reconstructed = new BrowserProfileStore({ stateDir });
		await reconstructed.load();
		expect(reconstructed.getProfile(work.id)?.name).toBe("Work");
		await reconstructed.bindSession("worker-1", personal.id);
		expect(reconstructed.getSessionProfileId("worker-1")).toBe(personal.id);

		const reloaded = new BrowserProfileStore({ stateDir });
		await reloaded.load();
		expect(reloaded.getSessionProfileId("worker-1")).toBe(personal.id);
	});

	it("serializes concurrent mutations without losing profiles", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });

		await Promise.all([store.createProfile("Work"), store.createProfile("Personal"), store.createProfile("Research")]);

		expect(new Set(store.profiles.map((profile) => profile.name))).toEqual(new Set(["Work", "Personal", "Research"]));
	});

	it("validates names, rejects duplicate names, and keeps partitions ID-derived", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const profile = await store.createProfile("Work");

		await expect(store.createProfile(" work ")).rejects.toMatchObject({ code: "BROWSER_PROFILE_NAME_TAKEN" });
		await expect(store.createProfile("\u0000bad")).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
		await expect(store.renameProfile("not-a-uuid", "Other")).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
		await expect(store.bindSession("__proto__", profile.id)).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
		await expect(store.bindSession("toString", profile.id)).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });

		const partition = browserProfilePartition(profile.id);
		await store.renameProfile(profile.id, "Personal");
		expect(browserProfilePartition(profile.id)).toBe(partition);
		expect(partition).toBe(`persist:ao-browser-profile-${profile.id}`);
	});

	it("preserves corrupt registries and surfaces a recoverable error", async () => {
		const stateDir = await makeStateDir();
		const registryPath = path.join(stateDir, "browser-profiles.json");
		const corrupt = JSON.stringify({ version: 99, profiles: [], bindings: {} });
		await writeFile(registryPath, corrupt, "utf8");
		const store = new BrowserProfileStore({ stateDir });

		expect((await store.load()).error).toMatchObject({ code: "BROWSER_PROFILE_STORE_CORRUPT" });
		await expect(store.createProfile("Work")).rejects.toMatchObject({ code: "BROWSER_PROFILE_STORE_CORRUPT" });
		expect(await readFile(registryPath, "utf8")).toBe(corrupt);
	});

	it("rejects duplicate or over-capacity registry records", async () => {
		const stateDir = await makeStateDir();
		const registryPath = path.join(stateDir, "browser-profiles.json");
		const duplicate = registryWithProfiles(2);
		duplicate.profiles[1]!.name = duplicate.profiles[0]!.name;
		await writeFile(registryPath, JSON.stringify(duplicate), "utf8");
		const duplicateStore = new BrowserProfileStore({ stateDir });
		expect((await duplicateStore.load()).error?.code).toBe("BROWSER_PROFILE_STORE_CORRUPT");

		const cappedStateDir = await makeStateDir();
		await writeFile(path.join(cappedStateDir, "browser-profiles.json"), JSON.stringify(registryWithProfiles(BROWSER_PROFILE_MAX_COUNT + 1)), "utf8");
		const cappedStore = new BrowserProfileStore({ stateDir: cappedStateDir });
		expect((await cappedStore.load()).error?.code).toBe("BROWSER_PROFILE_STORE_CORRUPT");
	});

	it("refuses destructive operations while a profile is live and deletes stale bindings after cleanup", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const profile = await store.createProfile("Work");
		await store.bindSession("worker-1", profile.id);
		const operation = async () => "cleared";

		await expect(store.runProfileOperation(profile.id, () => true, operation)).rejects.toMatchObject({
			code: "BROWSER_PROFILE_ACTIVE",
		});
		expect(store.isProfileOperationInProgress(profile.id)).toBe(false);

		await store.runProfileOperation(profile.id, () => false, async () => {
			await store.deleteProfile(profile.id);
		});
		expect(store.getProfile(profile.id)).toBeUndefined();
		expect(store.getSessionProfileId("worker-1")).toBeUndefined();
	});

	it.each([
		{ label: "before defaultProfileId existed", extra: {} },
		{ label: "with no default and no seeded marker", extra: { defaultProfileId: null } },
	])("makes the oldest profile the default once for a registry written $label", async ({ extra }) => {
		const stateDir = await makeStateDir();
		const registryPath = path.join(stateDir, "browser-profiles.json");
		const legacy = registryWithProfiles(2);
		await writeFile(registryPath, JSON.stringify({ version: legacy.version, profiles: legacy.profiles, bindings: legacy.bindings, ...extra }), "utf8");

		const store = new BrowserProfileStore({ stateDir });
		expect((await store.load()).error).toBeUndefined();
		expect(store.getDefaultProfileId()).toBe(legacy.profiles[0]!.id);

		// Unsetting afterwards is a durable choice: the upgrade must not run again.
		await store.setDefaultProfileId(null);
		const reloaded = new BrowserProfileStore({ stateDir });
		await reloaded.load();
		expect(reloaded.getDefaultProfileId()).toBeNull();
	});

	it("keeps a cleared default cleared for a registry that already has the seeded marker", async () => {
		const stateDir = await makeStateDir();
		await writeFile(path.join(stateDir, "browser-profiles.json"), JSON.stringify(registryWithProfiles(2)), "utf8");

		const store = new BrowserProfileStore({ stateDir });
		await store.load();
		expect(store.getDefaultProfileId()).toBeNull();
	});

	it("makes the first profile the default and clears it when the last profile is deleted", async () => {
		const stateDir = await makeStateDir();
		const store = new BrowserProfileStore({ stateDir });
		const profile = await store.createProfile("Work");

		expect(store.getDefaultProfileId()).toBe(profile.id);
		const reloaded = new BrowserProfileStore({ stateDir });
		await reloaded.load();
		expect(reloaded.getDefaultProfileId()).toBe(profile.id);

		await expect(store.setDefaultProfileId("not-a-uuid")).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });

		await store.deleteProfile(profile.id);
		expect(store.getDefaultProfileId()).toBeNull();
	});

	it("keeps the default when later profiles are added, and respects a cleared default", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const work = await store.createProfile("Work");
		await store.createProfile("Personal");
		expect(store.getDefaultProfileId()).toBe(work.id);

		await store.setDefaultProfileId(null);
		await store.createProfile("Research");
		expect(store.getDefaultProfileId()).toBeNull();
	});

	it("lets a deferred first profile claim the default only while it is the oldest", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const imported = await store.createProfile("Imported", { claimDefault: false });
		expect(store.getDefaultProfileId()).toBeNull();
		const later = await store.createProfile("Later", { claimDefault: false });

		await store.claimDefaultIfFirst(later.id);
		expect(store.getDefaultProfileId()).toBeNull();
		await store.claimDefaultIfFirst(imported.id);
		expect(store.getDefaultProfileId()).toBe(imported.id);

		await store.setDefaultProfileId(later.id);
		await store.claimDefaultIfFirst(imported.id);
		expect(store.getDefaultProfileId()).toBe(later.id);
	});

	it("promotes the oldest remaining profile when the default is deleted", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const work = await store.createProfile("Work");
		const personal = await store.createProfile("Personal");
		const research = await store.createProfile("Research");
		await store.setDefaultProfileId(research.id);

		await store.deleteProfile(personal.id);
		expect(store.getDefaultProfileId()).toBe(research.id);

		await store.deleteProfile(research.id);
		expect(store.getDefaultProfileId()).toBe(work.id);
	});

	it("keeps an explicit Temporary binding distinct from having no binding at all", async () => {
		const stateDir = await makeStateDir();
		const store = new BrowserProfileStore({ stateDir });
		const profile = await store.createProfile("Work");
		await store.setDefaultProfileId(profile.id);

		expect(store.getSessionProfileId("never-touched")).toBeUndefined();

		await store.bindSession("explicit-temporary", null);
		expect(store.getSessionProfileId("explicit-temporary")).toBeNull();

		const reloaded = new BrowserProfileStore({ stateDir });
		await reloaded.load();
		expect(reloaded.getSessionProfileId("never-touched")).toBeUndefined();
		expect(reloaded.getSessionProfileId("explicit-temporary")).toBeNull();
	});

	it("serializes profile data operations and keeps the live-operation marker until the full queue drains", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const profile = await store.createProfile("Work");
		let releaseFirst!: () => void;
		let releaseSecond!: () => void;
		const firstHeld = new Promise<void>((resolve) => {
			releaseFirst = resolve;
		});
		const secondHeld = new Promise<void>((resolve) => {
			releaseSecond = resolve;
		});
		const first = store.runProfileOperation(profile.id, () => false, async () => {
			await firstHeld;
			return "first";
		});
		const second = store.runProfileOperation(profile.id, () => false, async () => {
			await secondHeld;
			return "second";
		});
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(store.isProfileOperationInProgress(profile.id)).toBe(true);
		releaseFirst();
		expect(await first).toBe("first");
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(store.isProfileOperationInProgress(profile.id)).toBe(true);
		let queueDrained = false;
		const drain = store.waitForProfileOperation(profile.id).then(() => {
			queueDrained = true;
		});
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(queueDrained).toBe(false);
		releaseSecond();
		expect(await second).toBe("second");
		await drain;
		expect(store.isProfileOperationInProgress(profile.id)).toBe(false);
	});
});

describe("BrowserProfileStore profile sources", () => {
	it("persists where a profile came from and keeps it across renames", async () => {
		const stateDir = await makeStateDir();
		const store = new BrowserProfileStore({ stateDir });
		const imported = await store.createProfile("Brave", { source: { kind: "import", browser: "Brave" } });
		const copied = await store.createProfile("Chrome (from Laptop)", {
			source: {
				kind: "copy",
				browser: "Google Chrome",
				hostId: "h_laptop",
				machineName: "Laptop",
				remoteProfileId: "7C9E6679-7425-40DE-944B-E07FC1F90AE7",
				copiedAt: "2026-11-10T12:00:00.000Z",
			},
		});
		await store.renameProfile(copied.id, "Chrome");
		const reloaded = new BrowserProfileStore({ stateDir });
		await reloaded.load();
		expect(reloaded.getProfile(imported.id)?.source).toEqual({ kind: "import", browser: "Brave" });
		expect(reloaded.getProfile(copied.id)?.source).toEqual({
			kind: "copy",
			browser: "Google Chrome",
			hostId: "h_laptop",
			machineName: "Laptop",
			remoteProfileId: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
			copiedAt: "2026-11-10T12:00:00.000Z",
		});
		const updated = await reloaded.setProfileSource(copied.id, {
			kind: "copy", hostId: "h_laptop", machineName: "Laptop", remoteProfileId: "7c9e6679-7425-40de-944b-e07fc1f90ae7", copiedAt: "2026-11-11T08:00:00.000Z",
		});
		expect(updated.source).toMatchObject({ copiedAt: "2026-11-11T08:00:00.000Z" });
	});

	// Dropping an unreadable source would make a damaged copy look like a
	// local profile, and a local profile is shared: it is kept as "unknown".
	it("rejects an invalid source on create and keeps an unreadable stored one as unknown without losing the profile", async () => {
		const stateDir = await makeStateDir();
		const store = new BrowserProfileStore({ stateDir });
		await expect(store.createProfile("Bad", { source: { kind: "copy", hostId: "h/../x", machineName: "L", remoteProfileId: "nope", copiedAt: "x" } as never }))
			.rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
		const registry = registryWithProfiles(1);
		(registry.profiles[0] as Record<string, unknown>).source = { kind: "copy", hostId: 7 };
		await writeFile(path.join(stateDir, "browser-profiles.json"), JSON.stringify(registry));
		const reloaded = new BrowserProfileStore({ stateDir });
		const state = await reloaded.load();
		expect(state.error).toBeUndefined();
		expect(state.profiles[0]?.source).toEqual({ kind: "unknown" });
		// The marker survives the next write and reload.
		await reloaded.renameProfile(state.profiles[0]!.id, "Renamed");
		expect((await new BrowserProfileStore({ stateDir }).load()).profiles[0]?.source).toEqual({ kind: "unknown" });
	});
});
