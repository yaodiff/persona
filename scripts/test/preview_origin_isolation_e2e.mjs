#!/usr/bin/env node
/**
 * Real-browser isolation E2E test for isolated wildcard-subdomain preview origins.
 *
 * Launches a throwaway Go hub + SNI wildcard TLS + portNode + local dev server
 * (`TestPreviewOriginE2EHarness`, never touching ~/.persona or any running hub),
 * opens Chromium with `--host-resolver-rules` mapping `hub.example.test`,
 * `*.preview.example.test`, and `evil.example.test` to 127.0.0.1, and verifies:
 *   1. The hub web page stores a token in localStorage and grants a preview URL.
 *   2. Inside the preview iframe (on https://<label>.preview.example.test:<port>):
 *      - window.parent.localStorage / window.parent.document access throws SecurityError
 *      - document.cookie never exposes __Host-persona_preview or the hub token
 *      - cross-origin fetch(..., {credentials: "include"}) to hub /api/v1/machines & /health is blocked by CORS
 *      - same-origin fetch("/api/v1/health") and fetch("/api/v1/machines") return 404
 *      - document.referrer is empty and location.href contains no redemption code or token
 *      - the dev page itself works end-to-end: ES module, native <select>, dev cookie + 302, HMR WebSocket
 *   3. The upstream dev server never saw Authorization, hub token, or __Host-persona_preview,
 *      and saw Host=localhost:<port> and WS Origin=http://localhost:<port>.
 *   4. An unreachable port delivers `{persona_preview: "unreachable", code: "port_gone"}` via
 *      postMessage from the preview origin to the hub parent with no secrets.
 *   5. An untrusted origin (`evil.example.test`) cannot frame the preview (blocked by CSP frame-ancestors).
 *
 * Usage (from repository root; requires Go, Chromium/Chrome, and web's playwright-core):
 *   node scripts/test/preview_origin_isolation_e2e.mjs
 *
 * Optional env vars:
 *   PLAYWRIGHT_CORE_PATH        Path to playwright-core entry module (defaults to web/node_modules/playwright-core)
 *   PERSONA_CHROME              Path to Chrome/Chromium executable (optional override)
 *   PERSONA_PREVIEW_HARNESS_BIN Prebuilt `go test -c ./internal/hub` test binary (optional override)
 */

import { spawn, execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import readline from "node:readline";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "..", "..");
const BUILD_TIMEOUT_MS = 180_000;
const READY_TIMEOUT_MS = 30_000;
const OVERALL_TIMEOUT_MS = 240_000;

function assert(cond, msg) {
  if (!cond) {
    throw new Error(`assertion failed: ${msg}`);
  }
}

function loadPlaywrightCore() {
  const require = createRequire(import.meta.url);
  const candidates = [];
  if (process.env.PLAYWRIGHT_CORE_PATH) {
    candidates.push(process.env.PLAYWRIGHT_CORE_PATH);
  }
  candidates.push(path.join(REPO_ROOT, "web", "node_modules", "playwright-core"));
  try {
    const commonDir = execFileSync("git", ["rev-parse", "--git-common-dir"], {
      cwd: REPO_ROOT,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
    if (commonDir) {
      const mainRoot = path.resolve(REPO_ROOT, commonDir, "..");
      candidates.push(path.join(mainRoot, "web", "node_modules", "playwright-core"));
    }
  } catch {
    // Ignore git lookup failure outside a repo.
  }
  candidates.push("playwright-core");

  for (const candidate of candidates) {
    try {
      return require(candidate);
    } catch {
      // Try next candidate.
    }
  }
  throw new Error(
    "playwright-core not found; install web dependencies (`cd web && npm ci`) or set PLAYWRIGHT_CORE_PATH",
  );
}

function resolveBrowserExecutable() {
  const candidates = [
    process.env.PERSONA_CHROME,
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  ].filter(Boolean);
  for (const p of candidates) {
    if (fs.existsSync(p)) {
      return p;
    }
  }
  return undefined;
}

function buildHarnessBinary() {
  if (process.env.PERSONA_PREVIEW_HARNESS_BIN && fs.existsSync(process.env.PERSONA_PREVIEW_HARNESS_BIN)) {
    return { binPath: process.env.PERSONA_PREVIEW_HARNESS_BIN, cleanupDir: null };
  }
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "persona-preview-e2e-"));
  const binPath = path.join(tmpDir, "hub.test");
  try {
    execFileSync("go", ["test", "-c", "-o", binPath, "./internal/hub"], {
      cwd: path.join(REPO_ROOT, "server"),
      timeout: BUILD_TIMEOUT_MS,
      stdio: ["ignore", "pipe", "pipe"],
    });
    return { binPath, cleanupDir: tmpDir };
  } catch (err) {
    fs.rmSync(tmpDir, { recursive: true, force: true });
    const stderr = err?.stderr ? `\n${err.stderr.toString("utf8")}` : "";
    throw new Error(`failed to compile hub test binary: ${err.message}${stderr}`);
  }
}

function startHarness() {
  const { binPath, cleanupDir } = buildHarnessBinary();
  return new Promise((resolve, reject) => {
    const proc = spawn(
      binPath,
      ["-test.v", "-test.count=1", "-test.run", "^TestPreviewOriginE2EHarness$"],
      {
        cwd: path.join(REPO_ROOT, "server"),
        env: { ...process.env, PERSONA_PREVIEW_ORIGIN_HARNESS: "1" },
        stdio: ["pipe", "pipe", "pipe"],
      },
    );

    const stderrChunks = [];
    proc.stderr.on("data", (chunk) => stderrChunks.push(chunk.toString("utf8")));

    let settled = false;
    const readyTimer = setTimeout(() => {
      if (!settled) {
        settled = true;
        proc.kill("SIGKILL");
        if (cleanupDir) fs.rmSync(cleanupDir, { recursive: true, force: true });
        reject(new Error(`timed out waiting for Go harness\n${stderrChunks.join("")}`));
      }
    }, READY_TIMEOUT_MS);

    const rl = readline.createInterface({ input: proc.stdout });
    const stdoutLines = [];
    rl.on("line", (line) => {
      stdoutLines.push(line);
      const marker = "HARNESS_READY ";
      const idx = line.indexOf(marker);
      if (idx !== -1 && !settled) {
        settled = true;
        clearTimeout(readyTimer);
        try {
          const info = JSON.parse(line.slice(idx + marker.length));
          resolve({ proc, info, cleanupDir });
        } catch (err) {
          proc.kill("SIGKILL");
          if (cleanupDir) fs.rmSync(cleanupDir, { recursive: true, force: true });
          reject(new Error(`failed to parse HARNESS_READY JSON: ${err.message}`));
        }
      }
    });

    proc.on("exit", (code, signal) => {
      if (!settled) {
        settled = true;
        clearTimeout(readyTimer);
        if (cleanupDir) fs.rmSync(cleanupDir, { recursive: true, force: true });
        reject(
          new Error(
            `Go harness exited early (code=${code}, signal=${signal})\nstdout:\n${stdoutLines.join("\n")}\nstderr:\n${stderrChunks.join("")}`,
          ),
        );
      }
    });
  });
}

async function stopHarness(proc, ctrlURL) {
  if (!proc || proc.exitCode !== null) {
    return;
  }
  if (ctrlURL) {
    await fetch(ctrlURL + "/stop", { method: "POST" }).catch(() => {});
  }
  await new Promise((resolve) => {
    const timer = setTimeout(() => {
      proc.kill("SIGKILL");
      resolve();
    }, 5_000);
    proc.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

async function run() {
  const { chromium } = loadPlaywrightCore();
  const { proc, info, cleanupDir } = await startHarness();
  let browser;
  try {
    const launchArgs = [
      "--host-resolver-rules=MAP hub.example.test 127.0.0.1,MAP *.preview.example.test 127.0.0.1,MAP evil.example.test 127.0.0.1",
      "--no-sandbox",
      "--disable-gpu",
    ];
    try {
      browser = await chromium.launch({ headless: true, args: launchArgs });
    } catch {
      const executablePath = resolveBrowserExecutable();
      if (!executablePath) {
        throw new Error("no Chromium/Chrome binary available for playwright-core");
      }
      browser = await chromium.launch({ headless: true, executablePath, args: launchArgs });
    }

    const context = await browser.newContext({ ignoreHTTPSErrors: true });
    const page = await context.newPage();

    // 1. Open hub web origin and store the hub device token in localStorage.
    await page.goto(info.hub_origin + "/", { waitUntil: "domcontentloaded" });
    const grant = await page.evaluate(
      async ({ token, machineID, port }) => {
        localStorage.setItem("persona.token", token);
        const res = await fetch(`/api/v1/machines/${machineID}/preview/grant`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({ port, path: "/?from=hub#part" }),
        });
        const body = await res.json();
        return { status: res.status, body };
      },
      { token: info.token, machineID: info.machine_id, port: info.port },
    );

    assert(grant.status === 200, `preview grant status=${grant.status}`);
    assert(typeof grant.body.url === "string" && grant.body.url.includes("/__persona_preview_auth?code="), "grant url");
    assert(typeof grant.body.origin === "string" && grant.body.origin.startsWith("https://p-"), "grant origin");
    assert(!grant.body.url.includes(info.token), "grant url must not contain hub token");

    // 2. Embed the granted preview URL in an iframe and collect the in-iframe isolation & feature probe.
    const probe = await page.evaluate(
      ({ grantURL, expectedOrigin }) =>
        new Promise((resolve, reject) => {
          const timer = setTimeout(() => reject(new Error("timed out waiting for e2e_probe_done")), 15_000);
          window.addEventListener("message", function onMsg(ev) {
            if (ev.origin !== expectedOrigin || !ev.data || !ev.data.e2e_probe_done) {
              return;
            }
            window.removeEventListener("message", onMsg);
            clearTimeout(timer);
            resolve(ev.data);
          });
          const iframe = document.createElement("iframe");
          iframe.id = "preview-frame";
          iframe.src = grantURL;
          iframe.style.width = "800px";
          iframe.style.height = "600px";
          document.getElementById("mount").appendChild(iframe);
        }),
      { grantURL: grant.body.url, expectedOrigin: grant.body.origin },
    );

    assert(!probe.error, `in-iframe probe errored: ${probe.error}`);
    const r = probe.result;
    assert(r.origin === grant.body.origin, `origin mismatch: ${r.origin} vs ${grant.body.origin}`);
    assert(r.parentLocalStorageBlocked === true, "parent localStorage was NOT blocked");
    assert(r.parentLocalStorageLeaked === undefined, `parent localStorage leaked: ${r.parentLocalStorageLeaked}`);
    assert(r.parentDocumentBlocked === true, "parent document was NOT blocked");
    assert(r.parentDocumentLeaked === undefined, `parent document leaked: ${r.parentDocumentLeaked}`);
    assert(!r.documentCookie.includes("__Host-persona_preview"), `HttpOnly preview cookie exposed: ${r.documentCookie}`);
    assert(!r.documentCookie.includes(info.token), "hub token leaked into document.cookie");
    assert(r.documentCookie.includes("dev_visible=from_dev_server"), `dev-server cookie missing: ${r.documentCookie}`);
    assert(r.crossOriginHubAPIFailed === true, `cross-origin hub API fetch succeeded: ${r.crossOriginHubAPIStatus}`);
    assert(r.crossOriginHubHealthFailed === true, `cross-origin hub health fetch succeeded: ${r.crossOriginHubHealthStatus}`);
    assert(r.sameOriginAPIHealthStatus === 404, `preview /api/v1/health status=${r.sameOriginAPIHealthStatus}, want 404`);
    assert(r.sameOriginAPIMachinesStatus === 404, `preview /api/v1/machines status=${r.sameOriginAPIMachinesStatus}, want 404`);
    assert(r.referrer === "", `document.referrer leaked: ${r.referrer}`);
    assert(!r.href.includes("code=") && !r.href.includes("__persona_preview_auth") && !r.href.includes(info.token), `redemption code or token left in location.href: ${r.href}`);
    assert(r.href.endsWith("/?from=hub#part"), `target path/query/hash not preserved: ${r.href}`);

    // Verify real dev page interactivity (ES module, native <select>, dev cookie + 302, HMR WebSocket).
    assert(r.moduleLoaded === true, "ES module did not run");
    assert(r.redirectCookieBody === "dev-me:dev42", `dev cookie + 302 body=${r.redirectCookieBody}`);
    assert(r.hmrReply === "echo:hmr-ping", `HMR WebSocket reply=${r.hmrReply}`);
    const frame = page.frameLocator("#preview-frame");
    await frame.locator("#fruit").selectOption("banana");
    const selected = await frame.locator("#fruit").inputValue();
    assert(selected === "banana", `native <select> value=${selected}, want banana`);

    // 3. Verify what the upstream dev server received (no credentials, rewritten Host and WS Origin).
    const ctrlRes = await fetch(info.ctrl_url);
    const upstream = await ctrlRes.json();
    const wantHost = `localhost:${info.port}`;
    assert(upstream.seen_auth === false, "dev server received Authorization header");
    assert(upstream.seen_hub_token === false, "dev server received hub token");
    assert(upstream.seen_preview_ck === false, "dev server received __Host-persona_preview cookie");
    assert(Array.isArray(upstream.seen_hosts) && upstream.seen_hosts.length > 0, "dev server saw no HTTP requests");
    assert(upstream.seen_hosts.every((h) => h === wantHost), `unexpected upstream Host(s): ${JSON.stringify(upstream.seen_hosts)}`);
    assert(Array.isArray(upstream.seen_ws_hosts) && upstream.seen_ws_hosts.length === 1 && upstream.seen_ws_hosts[0] === wantHost, `unexpected upstream WS Host(s): ${JSON.stringify(upstream.seen_ws_hosts)}`);
    assert(Array.isArray(upstream.seen_ws_origins) && upstream.seen_ws_origins.length === 1 && upstream.seen_ws_origins[0] === `http://${wantHost}`, `unexpected upstream WS Origin(s): ${JSON.stringify(upstream.seen_ws_origins)}`);

    // 4. Unreachable port delivers fallback postMessage {persona_preview: "unreachable", code: "port_gone"}.
    const stoppedGrant = await page.evaluate(
      async ({ token, machineID, port }) => {
        const res = await fetch(`/api/v1/machines/${machineID}/preview/grant`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({ port, path: "/" }),
        });
        return { status: res.status, body: await res.json() };
      },
      { token: info.token, machineID: info.machine_id, port: info.stopped_port },
    );
    assert(stoppedGrant.status === 200, `stopped port grant status=${stoppedGrant.status}`);

    const unreachableMsg = await page.evaluate(
      ({ grantURL, expectedOrigin }) =>
        new Promise((resolve, reject) => {
          const timer = setTimeout(() => reject(new Error("timed out waiting for unreachable postMessage")), 10_000);
          window.addEventListener("message", function onMsg(ev) {
            if (ev.origin !== expectedOrigin || !ev.data || ev.data.persona_preview !== "unreachable") {
              return;
            }
            window.removeEventListener("message", onMsg);
            clearTimeout(timer);
            resolve({ origin: ev.origin, data: ev.data });
          });
          const iframe = document.createElement("iframe");
          iframe.id = "stopped-frame";
          iframe.src = grantURL;
          document.getElementById("mount").appendChild(iframe);
        }),
      { grantURL: stoppedGrant.body.url, expectedOrigin: stoppedGrant.body.origin },
    );
    assert(unreachableMsg.data.persona_preview === "unreachable", "missing unreachable signal");
    assert(unreachableMsg.data.code === "port_gone", `unreachable code=${unreachableMsg.data.code}, want port_gone`);
    assert(
      !JSON.stringify(unreachableMsg.data).includes(info.token),
      "unreachable postMessage leaked hub token",
    );

    // 5. Untrusted origin cannot embed the preview (blocked by CSP frame-ancestors).
    const evilGrant = await page.evaluate(
      async ({ token, machineID, port }) => {
        const res = await fetch(`/api/v1/machines/${machineID}/preview/grant`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({ port, path: "/" }),
        });
        return { status: res.status, body: await res.json() };
      },
      { token: info.token, machineID: info.machine_id, port: info.port },
    );
    assert(evilGrant.status === 200, `evil grant status=${evilGrant.status}`);

    const evilPage = await context.newPage();
    await evilPage.goto(info.evil_origin + "/", { waitUntil: "domcontentloaded" });
    const evilFramed = await evilPage.evaluate(
      ({ grantURL, previewOrigin }) =>
        new Promise((resolve) => {
          let sawProbe = false;
          const onMsg = (ev) => {
            if (ev.origin === previewOrigin && ev.data && ev.data.e2e_probe_done) {
              sawProbe = true;
            }
          };
          window.addEventListener("message", onMsg);
          const iframe = document.createElement("iframe");
          iframe.src = grantURL;
          document.getElementById("mount").appendChild(iframe);
          setTimeout(() => {
            window.removeEventListener("message", onMsg);
            resolve(sawProbe);
          }, 1500);
        }),
      { grantURL: evilGrant.body.url, previewOrigin: evilGrant.body.origin },
    );
    assert(evilFramed === false, "evil.example.test was able to frame the preview despite CSP frame-ancestors");

    // Verify no frame on evilPage actually committed to the preview origin.
    const evilFrameURLs = evilPage.frames().map((f) => f.url());
    assert(
      !evilFrameURLs.some((u) => u.startsWith(evilGrant.body.origin)),
      `evil page committed a preview frame: ${JSON.stringify(evilFrameURLs)}`,
    );

    console.log("PASS preview_origin_isolation_e2e: isolated origin, no token/cookie/API leak, HMR + select + fallback + CSP frame-ancestors verified");
  } finally {
    if (browser) {
      await browser.close().catch(() => {});
    }
    await stopHarness(proc, info.ctrl_url);
    if (cleanupDir) {
      fs.rmSync(cleanupDir, { recursive: true, force: true });
    }
  }
}

const overallTimer = setTimeout(() => {
  console.error(`TIMEOUT preview_origin_isolation_e2e: exceeded ${OVERALL_TIMEOUT_MS}ms`);
  process.exit(124);
}, OVERALL_TIMEOUT_MS);
overallTimer.unref();

run()
  .then(() => {
    clearTimeout(overallTimer);
    process.exit(0);
  })
  .catch((err) => {
    clearTimeout(overallTimer);
    console.error(`FAIL preview_origin_isolation_e2e: ${err?.stack || err}`);
    process.exit(1);
  });
