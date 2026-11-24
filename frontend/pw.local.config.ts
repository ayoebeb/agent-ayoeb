import { defineConfig } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
const root = path.join(process.env.LOCALAPPDATA as string, "ms-playwright");
const dir = fs.readdirSync(root).find((d) => d.startsWith("chromium_headless_shell"))!;
const exe = path.join(root, dir, "chrome-headless-shell-win64", "chrome-headless-shell.exe");
const port = 5399;
export default defineConfig({
	testDir: "e2e",
	retries: 0,
	reporter: "list",
	timeout: 120000,
	use: { baseURL: `http://127.0.0.1:${port}`, launchOptions: { executablePath: exe, args: ["--no-sandbox"] } },
	webServer: { command: `npm run dev:web -- --port ${port} --host 127.0.0.1`, port, reuseExistingServer: false, timeout: 180000 },
});
