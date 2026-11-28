import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type PlaywrightTestConfig } from "@playwright/test";

const webDir = dirname(fileURLToPath(import.meta.url));
const isListOnly = process.argv.includes("--list");

if (!isListOnly && !existsSync(resolve(webDir, "dist/index.html"))) {
  throw new Error(
    `Missing web/dist/index.html at ${resolve(webDir, "dist/index.html")}. Run "cd web && npm run build" before running UI E2E tests.`,
  );
}

function resolvePort(): number {
  if (process.env.PERSONA_UI_E2E_PORT) return Number(process.env.PERSONA_UI_E2E_PORT);
  if (isListOnly) return 4197;
  // Probe a free loopback port once in the coordinator process and pass it via env so workers share it.
  const probed = execFileSync(
    process.execPath,
    ["-e", "const net=require('node:net');const s=net.createServer();s.listen(0,'127.0.0.1',()=>{process.stdout.write(String(s.address().port));s.close();});"],
    { encoding: "utf8" },
  ).trim();
  process.env.PERSONA_UI_E2E_PORT = probed;
  return Number(probed);
}

const port = resolvePort();
const baseURL = `http://127.0.0.1:${port}`;

function resolveBrowserUse(): PlaywrightTestConfig["use"] {
  const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE;
  if (executablePath) {
    return { launchOptions: { executablePath, args: ["--no-sandbox"] } };
  }
  const channel = process.env.PLAYWRIGHT_CHANNEL;
  if (channel) {
    return { channel };
  }
  if (!process.env.CI && process.platform === "darwin" && existsSync("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")) {
    return { channel: "chrome" };
  }
  return { launchOptions: { args: ["--no-sandbox"] } };
}

export default defineConfig({
  testDir: resolve(webDir, "e2e/ui"),
  testMatch: "**/*.spec.ts",
  timeout: 30_000,
  expect: { timeout: 10_000 },
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  outputDir: resolve(webDir, "test-results/ui-artifacts"),
  reporter: process.env.CI
    ? [["list"], ["html", { open: "never", outputFolder: resolve(webDir, "test-results/ui-report") }]]
    : [["list"]],
  use: {
    baseURL,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    locale: "en-US",
    reducedMotion: "reduce",
    serviceWorkers: "block",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    ...resolveBrowserUse(),
  },
  webServer: {
    command: `npx --no -- vite preview --host 127.0.0.1 --port ${port} --strictPort`,
    cwd: webDir,
    url: `${baseURL}/`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
