import { readFileSync } from "node:fs";
import path from "node:path";

// Where an unpackaged AO finds its ACP runtime. On Windows the dev/daily build
// writes each runtime to resources/acp-runtime-dev/rt-<time>-<pid> and names the
// newest in current.json (scripts/acp-runtime-versions.mjs), because running
// chat sessions lock the previous directory. Anywhere else, or when no manifest
// exists yet, the fixed resources/acp-runtime applies.
export function devAcpRuntimeDir(appPath: string, platform: NodeJS.Platform = process.platform): string {
	const fixed = path.join(appPath, "resources", "acp-runtime");
	if (platform !== "win32") return fixed;
	const buildsRoot = path.join(appPath, "resources", "acp-runtime-dev");
	try {
		const parsed = JSON.parse(readFileSync(path.join(buildsRoot, "current.json"), "utf8")) as { path?: unknown };
		if (typeof parsed.path !== "string") return fixed;
		const dir = path.resolve(parsed.path.trim());
		const rel = path.relative(buildsRoot, dir);
		if (!rel || rel.startsWith("..") || path.isAbsolute(rel)) return fixed;
		return dir;
	} catch {
		return fixed;
	}
}
