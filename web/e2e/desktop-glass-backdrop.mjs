// Real-browser verification of CSS backdrop-filter suppression under macOS Desktop Frosted Glass (#17761).
// Usage: node web/e2e/desktop-glass-backdrop.mjs <loopback-web-origin> [--chromium <executable>] [--shots <dir>]
// Intercepts /api/v1/** and /api/v1/ws** with synthetic data; nothing reaches a live hub.
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const idx = args.indexOf(name);
  return idx >= 0 ? args.splice(idx, 2)[1] : undefined;
};
const chromiumAt = flag("--chromium");
const shotsDir = flag("--shots");
const [address, ...extra] = args;
assert.ok(address && extra.length === 0, "Usage: node web/e2e/desktop-glass-backdrop.mjs <loopback-web-origin> [--chromium <path>] [--shots <dir>]");
const base = new URL(address);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback web origin");
if (shotsDir) await mkdir(resolve(shotsDir), { recursive: true });

const AT = "2026-11-28T08:00:00Z";
const MAIN_ID = "main-chat";
const AGENT_ID = "agent-side";
const MACHINE = { id: "demo", name: "Demo Mac", online: true, os: "darwin", host: "herdr" };
const MAIN_SESSION = {
  machine_id: MACHINE.id,
  session_id: MAIN_ID,
  title: "Persona",
  state: "idle",
  status: "idle",
  cwd: "/demo",
  updated_at: AT,
  started_by_persona: true,
  assistant: true,
  capabilities: { send: true, interrupt: true, approve: true },
};
const AGENT_SESSION = {
  machine_id: MACHINE.id,
  session_id: AGENT_ID,
  title: "Inspect split panel",
  name: "Inspect split panel",
  state: "working",
  status: "working",
  agent: "claude",
  online: true,
  cwd: "/demo/project",
  updated_at: AT,
  state_since: AT,
  pending_interactions: 0,
  capabilities: { send: true, interrupt: true, approve: true },
};
const PNG_1X1 = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64");

const failures = [];
const check = (ok, label, detail = "") => {
  console.log(`${ok ? "ok  " : "FAIL"} ${label}${detail ? `: ${detail}` : ""}`);
  if (!ok) failures.push(detail ? `${label}: ${detail}` : label);
};

const browser = await chromium.launch(
  chromiumAt
    ? { executablePath: chromiumAt, headless: true, args: ["--no-sandbox"] }
    : { channel: "chrome", headless: true, args: ["--no-sandbox"] },
);
const watchdog = setTimeout(() => {
  console.log("VERDICT TIMEOUT desktop-glass-backdrop");
  void browser.close().finally(() => process.exit(2));
}, 180_000);

async function createAppContext({ theme, surfaceStyle, desktopBridge }) {
  const context = await browser.newContext({
    viewport: { width: 1280, height: 900 },
    deviceScaleFactor: 2,
    locale: "en-US",
    colorScheme: theme,
    serviceWorkers: "block",
  });
  await context.addInitScript(({ theme, surfaceStyle, desktopBridge }) => {
    localStorage.setItem("persona.token", "mock");
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
    if (surfaceStyle === "liquid-glass") localStorage.setItem("persona.surface-style", "liquid-glass");
    else localStorage.removeItem("persona.surface-style");
    if (desktopBridge) {
      window.personaDesktop = {
        version: 1,
        platform: "darwin",
        deviceName: "Demo Mac",
        notificationPermission: () => "granted",
        setBadge: () => {},
        notify: () => false,
        closeNotification: () => {},
        clearNotifications: () => {},
        onOpenNotification: () => () => {},
        desktopPresence: () => ({ active: true, notifies: true }),
        onPresenceChange: () => () => {},
        setLaunchAtLogin: async (enabled) => ({ enabled }),
        getLaunchAtLogin: async () => ({ enabled: false }),
        windowChrome: () => ({ unifiedTitlebar: true, fullScreen: false, focused: true }),
        onWindowChromeChange: () => () => {},
        setSurfaceStyle: () => {},
        setThemeSource: () => {},
      };
    }
  }, { theme, surfaceStyle, desktopBridge });

  await context.route(/^https?:\/\/(?!127\.0\.0\.1|localhost|\[::1\])/, (route) => route.abort());
  await context.route("**/api/v1/**", (route) => {
    const reqUrl = new URL(route.request().url());
    const path = reqUrl.pathname.replace(/^.*\/api\/v1/, "");
    if (path.includes("/file")) {
      return route.fulfill({ status: 200, contentType: "image/png", body: PNG_1X1 });
    }
    if (route.request().method() !== "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
    }
    let data = {};
    if (path === "/machines") data = { machines: [MACHINE] };
    else if (path === "/sessions") data = { sessions: [MAIN_SESSION, AGENT_SESSION] };
    else if (path === "/interactions") data = { interactions: [] };
    else if (path === "/assistant") data = { machine_id: MACHINE.id, main: MAIN_ID, conversations: [MAIN_SESSION], tasks: [], memory: [] };
    else if (path.endsWith("/feed")) data = { items: [], plans: {}, task_cards: [] };
    else if (path === "/todos") data = { todos: [] };
    else if (path === "/extensions/ui") data = { slots: [] };
    else if (path === "/extensions") data = { extensions: [] };
    else if (path === "/managers") data = { managers: [] };
    else if (path === `/sessions/${MACHINE.id}/${MAIN_ID}/items`) {
      data = {
        items: [
          { id: "u1", seq: 1, role: "user", kind: "text", text: "<attached_images>\n/demo/shot.png\n</attached_images>\nCheck image viewer backdrop", ts: AT },
          { id: "a1", seq: 2, role: "assistant", kind: "text", text: "Ready to inspect split panel and overlays.", ts: AT },
        ],
        has_more: false,
      };
    } else if (path === `/sessions/${MACHINE.id}/${AGENT_ID}/items`) {
      data = {
        items: [
          { id: "su1", seq: 1, role: "user", kind: "text", text: "Run split panel check", ts: AT },
          { id: "sa1", seq: 2, role: "assistant", kind: "text", text: "Split panel active.", ts: AT },
        ],
        has_more: false,
      };
    } else if (path === `/sessions/${MACHINE.id}/${MAIN_ID}`) {
      data = { session: MAIN_SESSION };
    } else if (path === `/sessions/${MACHINE.id}/${AGENT_ID}`) {
      data = { session: AGENT_SESSION };
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(data) });
  });
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "demo", hub_name: "Demo hub", features: [] }));
    ws.onMessage((msg) => {
      if (String(msg).includes("app_ping")) ws.send(JSON.stringify({ type: "pong" }));
    });
  });

  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.setDefaultTimeout(15_000);
  await page.goto(new URL("/", base).href);
  await page.locator("[data-app-shell] > main header").first().waitFor();
  return { context, page, errors };
}

async function openSplitPanelAgent(page) {
  await page.getByRole("button", { name: /In progress/ }).click();
  const agentRow = page.locator(`[data-running-agent="${MACHINE.id}/${AGENT_ID}"]`);
  await agentRow.waitFor();
  await agentRow.click();
  await page.locator("aside[data-side-panel] header").waitFor();
  await page.locator("aside[data-side-panel] textarea").waitFor();
}

async function readBackdropStates(page) {
  return page.evaluate(() => {
    const visible = (el) => {
      const r = el.getBoundingClientRect();
      return r.width > 0 && r.height > 0;
    };
    const inspect = (el) => {
      const cs = getComputedStyle(el);
      return {
        tag: el.tagName.toLowerCase(),
        inMain: Boolean(el.closest("[data-app-shell] > main")),
        inAside: Boolean(el.closest("[data-app-shell] > aside")),
        classes: typeof el.className === "string" ? el.className : "",
        backdropFilter: cs.backdropFilter,
        webkitBackdropFilter: cs.getPropertyValue("-webkit-backdrop-filter"),
      };
    };
    const shell = document.querySelector("[data-app-shell]");
    const main = document.querySelector("[data-app-shell] > main");
    const aside = document.querySelector("[data-app-shell] > aside[data-side-panel]");
    const backdropEls = [...document.querySelectorAll('[class*="backdrop-"]')].filter(visible).map(inspect);
    return {
      surfaceStyle: document.documentElement.dataset.surfaceStyle ?? null,
      desktopGlass: document.documentElement.dataset.desktopGlass ?? null,
      shell: shell ? inspect(shell) : null,
      main: main ? inspect(main) : null,
      aside: aside ? inspect(aside) : null,
      backdropEls,
    };
  });
}

const isBackdropNone = (entry) =>
  entry?.backdropFilter === "none" && (!entry.webkitBackdropFilter || entry.webkitBackdropFilter === "none");

try {
  for (const theme of ["light", "dark"]) {
    const { context, page, errors } = await createAppContext({ theme, surfaceStyle: "liquid-glass", desktopBridge: true });
    await openSplitPanelAgent(page);

    const splitState = await readBackdropStates(page);
    check(splitState.surfaceStyle === "liquid-glass" && splitState.desktopGlass === "1", `desktop-${theme}: surface & desktop-glass attrs set`);
    check(splitState.main?.backdropFilter === "none" && splitState.aside?.backdropFilter === "none", `desktop-${theme}: shell <main> & <aside> backdropFilter is none`);
    check(
      splitState.backdropEls.some((e) => e.inMain) && splitState.backdropEls.some((e) => e.inAside),
      `desktop-${theme}: both <main> and <aside> contain visible [class*="backdrop-"] elements`,
      `main=${splitState.backdropEls.filter((e) => e.inMain).length}, aside=${splitState.backdropEls.filter((e) => e.inAside).length}`,
    );
    const activeDesktopFilters = splitState.backdropEls.filter((e) => !isBackdropNone(e));
    check(activeDesktopFilters.length === 0, `desktop-${theme}: every [class*="backdrop-"] in split view resolves none`, JSON.stringify(activeDesktopFilters));

    if (shotsDir) await page.screenshot({ path: resolve(shotsDir, `desktop-frosted-${theme}-split.png`) });

    // Open Palette (portaled to document.body with backdrop-blur-[2px])
    await page.locator("[data-mac-bay-tools] button").nth(1).click();
    await page.locator('[role="dialog"]').waitFor();
    const paletteState = await readBackdropStates(page);
    const paletteBackdrop = paletteState.backdropEls.find((e) => !e.inMain && !e.inAside && e.classes.includes("backdrop-blur-[2px]"));
    check(Boolean(paletteBackdrop), `desktop-${theme}: Palette portal backdrop-blur-[2px] rendered`);
    check(isBackdropNone(paletteBackdrop), `desktop-${theme}: Palette portal backdropFilter is none`, JSON.stringify(paletteBackdrop));
    await page.keyboard.press("Escape");
    await page.locator('[role="dialog"]').waitFor({ state: "detached" });

    // Open NewAgent Dialog (AgentsAdd menu -> "New agent" menuitem -> portaled Dialog with backdrop-blur-[2px])
    await page.getByRole("button", { name: "New agent" }).first().click();
    await page.getByRole("menuitem", { name: "New agent" }).click();
    await page.locator('[role="dialog"]').waitFor();
    const modalState = await readBackdropStates(page);
    const modalBackdrop = modalState.backdropEls.find((e) => !e.inMain && !e.inAside && e.classes.includes("backdrop-blur-[2px]"));
    check(Boolean(modalBackdrop), `desktop-${theme}: Modal portal backdrop-blur-[2px] rendered`);
    check(isBackdropNone(modalBackdrop), `desktop-${theme}: Modal portal backdropFilter is none`, JSON.stringify(modalBackdrop));
    await page.keyboard.press("Escape");
    await page.locator('[role="dialog"]').waitFor({ state: "detached" });

    // Open ImageViewer (portaled to document.body with backdrop-blur-sm)
    const thumb = page.locator("[data-attached-photos] button").first();
    await thumb.waitFor();
    await thumb.click();
    await page.locator('[role="dialog"]').waitFor();
    const viewerState = await readBackdropStates(page);
    const viewerBackdrop = viewerState.backdropEls.find((e) => !e.inMain && !e.inAside && e.classes.includes("backdrop-blur-sm"));
    check(Boolean(viewerBackdrop), `desktop-${theme}: ImageViewer portal backdrop-blur-sm rendered`);
    check(isBackdropNone(viewerBackdrop), `desktop-${theme}: ImageViewer portal backdropFilter is none`, JSON.stringify(viewerBackdrop));
    await page.keyboard.press("Escape");
    await page.locator('[role="dialog"]').waitFor({ state: "detached" });

    check(errors.length === 0, `desktop-${theme}: zero page errors`, errors.join("; "));
    await context.close();
  }

  // Web-only Frosted Glass (no personaDesktop -> data-desktop-glass unset)
  {
    const { context, page, errors } = await createAppContext({ theme: "light", surfaceStyle: "liquid-glass", desktopBridge: false });
    await openSplitPanelAgent(page);
    const webState = await readBackdropStates(page);
    check(webState.surfaceStyle === "liquid-glass" && webState.desktopGlass === null, "web-frosted: data-desktop-glass is unset");
    check(
      Boolean(webState.shell?.backdropFilter.startsWith("blur("))
        && Boolean(webState.main?.backdropFilter.startsWith("blur("))
        && Boolean(webState.aside?.backdropFilter.startsWith("blur(")),
      "web-frosted: [data-app-shell], > main, and > aside keep CSS glass blur",
      JSON.stringify({ shell: webState.shell?.backdropFilter, main: webState.main?.backdropFilter, aside: webState.aside?.backdropFilter }),
    );
    const innerBlurred = webState.backdropEls.filter((e) => e.backdropFilter.startsWith("blur("));
    check(innerBlurred.length === webState.backdropEls.length && innerBlurred.length > 0, "web-frosted: inner backdrop-* utilities keep blur");
    check(errors.length === 0, "web-frosted: zero page errors", errors.join("; "));
    await context.close();
  }

  // Default opaque appearance with desktop bridge
  {
    const { context, page, errors } = await createAppContext({ theme: "dark", surfaceStyle: "default", desktopBridge: true });
    await openSplitPanelAgent(page);
    const defState = await readBackdropStates(page);
    check(defState.surfaceStyle === null && defState.desktopGlass === null, "desktop-default: liquid-glass attrs absent");
    check(defState.shell?.backdropFilter === "none" && defState.main?.backdropFilter === "none" && defState.aside?.backdropFilter === "none", "desktop-default: shell/main/aside remain opaque without glass blur");
    check(errors.length === 0, "desktop-default: zero page errors", errors.join("; "));
    await context.close();
  }

  if (shotsDir) {
    await writeFile(resolve(shotsDir, "results.json"), JSON.stringify({ failures }, null, 2) + "\n");
  }
} finally {
  clearTimeout(watchdog);
  await browser.close();
}

console.log(`VERDICT ${failures.length ? "FAIL" : "PASS"} desktop-glass-backdrop${failures.length ? ` (${failures.length} failures)` : ""}`);
process.exit(failures.length ? 1 : 0);
