// Start a fresh mock_hub.py with PORT=0 and these fixture settings:
// MOCK_CHANNELS=1 MOCK_EXTENSIONS=1 MOCK_EXTENSION_MANAGEMENT=1 MOCK_MANAGERS=1
// MOCK_SUBSCRIPTION_INBOX=1 MOCK_SPACES=1 MOCK_TASKS=1 MOCK_HUB_UPDATE=available MOCK_LARK_ACCOUNTS=1
// Build web and run vite preview --host 127.0.0.1 with PERSONA_HUB_URL set to that mock.
// Set CHROME_BIN when using Chromium or a nonstandard Chrome installation.
// node web/e2e/integrations-v2.mjs <loopback-preview-origin> [screenshot-directory]
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [address, shots] = process.argv.slice(2);
const base = new URL(address);
assert.ok(base.protocol === "http:" && ["127.0.0.1", "[::1]"].includes(base.hostname));
const health = await fetch(new URL("/api/v1/health", base), { signal: AbortSignal.timeout(5000), redirect: "error" });
assert.equal((await health.json()).version, "mock");
if (shots) await mkdir(shots, { recursive: true });

async function noOverflow(page) {
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth),
    `Horizontal document overflow at ${page.url()}`);
}

async function legacyPages(page) {
  for (const [tab, content] of [
    ["connectors", '[data-settings-group="mail"]'],
    ["channels", 'button:has-text("Example Chat")'],
    ["extensions", '[data-settings-group="extensions"]'],
    ["sources", '[data-settings-group="inbox-sources"] >> text=Newsletters'],
  ]) {
    await page.goto(`/settings?tab=${tab}`);
    await page.locator(content).waitFor();
    assert.equal(new URL(page.url()).searchParams.get("tab"), tab);
    await noOverflow(page);
  }
}

async function save(page, path, action) {
  const [response] = await Promise.all([
    page.waitForResponse(r => new URL(r.url()).pathname === "/api/v1/" + path && r.request().method() === "PUT"),
    action(),
  ]);
  assert.equal(response.status(), 200, path);
  return response;
}

async function labs(page, enabled) {
  await page.goto("/settings?tab=labs");
  const field = page.locator('[data-configuration-field="integrations_v2"]');
  const toggle = field.getByRole("switch", { checked: !enabled });
  const response = await save(page, "assistant", () => toggle.click());
  assert.deepEqual(response.request().postDataJSON(), { integrations_v2: enabled, expected_integrations_v2: !enabled });
  assert.equal((await response.json()).integrations_v2, enabled);
  await field.getByRole("switch", { checked: enabled }).waitFor();
}

async function layout(page, width) {
  await noOverflow(page);
  assert.equal(await page.locator("[data-integrations-split]").count(), width === 1440 ? 1 : 0);
  if (width !== 1440) {
    assert.equal(await page.locator("[data-integrations-list]").count(), 0);
    return;
  }
  const list = page.locator("[data-integrations-list-scroll]");
  const detail = page.locator("[data-integrations-detail-scroll]");
  const top = pane => pane.evaluate(el => el.scrollTop);
  for (const [pane, other] of [[list, detail], [detail, list]]) {
    const before = await top(pane);
    const otherBefore = await top(other);
    await pane.hover();
    await page.mouse.wheel(0, before > 0 ? -180 : 180);
    await page.waitForFunction(([selector, old]) => document.querySelector(selector)?.scrollTop !== old,
      [pane === list ? "[data-integrations-list-scroll]" : "[data-integrations-detail-scroll]", before]);
    assert.equal(await top(other), otherBefore, "Scrolling one pane moved the other");
  }
}

async function scenario(browser, width, theme) {
  const context = await browser.newContext({ baseURL: base.origin, viewport: { width, height: 800 },
    hasTouch: width === 390, colorScheme: theme, reducedMotion: "reduce", serviceWorkers: "block" });
  const errors = [];
  try {
    await context.addInitScript(theme => {
      localStorage.setItem("persona.token", "mock");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme);
    }, theme);
    // Unrelated read-only panels have no board, linked work or daily bot sync in this fixture.
    await context.route("**/*", route => {
      const url = new URL(route.request().url());
      if (url.origin !== base.origin) return route.abort();
      if (route.request().method() === "GET") {
        if (url.pathname === "/api/v1/bots/feishu/sync") return route.fulfill({ json: { available: false } });
        if (url.pathname === "/api/v1/workspace-board/settings") return route.fulfill({ json: { enabled: false } });
        if (url.pathname === "/api/v1/assistant/external-source-bindings/summary") return route.fulfill({ json: { bindings: [] } });
      }
      return route.continue();
    });
    const page = await context.newPage();
    page.setDefaultTimeout(12000);
    page.on("pageerror", e => errors.push(e.message));
    page.on("console", m => { if (m.type() === "error") errors.push(m.text() + " " + m.location().url); });
    const profile = await context.request.get("/api/v1/assistant");
    assert.equal((await profile.json()).integrations_v2, false);
    await legacyPages(page);
    assert.equal(await page.evaluate(() => document.documentElement.classList.contains("dark")), theme === "dark");
    await labs(page, true);
    const openNav = async () => {
      if (width === 390) await page.getByRole("button", { name: "Back to Settings", exact: true }).click();
    };
    await openNav();
    await page.getByRole("button", { name: /^Integrations & Extensions/ }).click();
    await page.locator('[data-integration-id="wechat"]').click();
    const detail = page.locator('[data-integration-detail="wechat"]');
    const routing = detail.locator("[data-channel-routing]");
    const hours = routing.locator('[data-configuration-field="thread_idle_hours"] input');
    await hours.waitFor();
    const group = routing.getByRole("radio", { name: /^Per group and topic/ });
    const topic = routing.getByRole("radio", { name: /^Per topic only/ });
    const [mode, radio] = (await group.getAttribute("aria-checked")) === "true"
      ? ["per_topic", topic] : ["per_group_and_topic", group];
    const idle = (await hours.inputValue()) === "48" ? 72 : 48;
    const updatedMode = await save(page, "channels/wechat", () => radio.click());
    assert.equal((await updatedMode.json()).thread_mode, mode);
    await hours.fill(String(idle));
    const updatedHours = await save(page, "channels/wechat", () => hours.press("Enter"));
    assert.equal((await updatedHours.json()).thread_idle_hours, idle);

    const url = page.url();
    const reloadRoutes = [];
    const record = f => { if (f === page.mainFrame()) reloadRoutes.push(f.url()); };
    page.on("framenavigated", record);
    await page.reload();
    await hours.waitFor();
    page.off("framenavigated", record);
    assert.deepEqual([...new Set(reloadRoutes)], [url], "Reload left the selected detail");
    assert.equal(await hours.inputValue(), String(idle));
    assert.equal(await radio.getAttribute("aria-checked"), "true");
    await layout(page, width);
    if (shots) await page.screenshot({ path: join(shots, `${width}-${theme}.png`) });

    await page.goto("/settings?tab=integrations");
    await page.locator('[data-integration-id="feishu-bot"]').click();
    const bot = page.locator('[data-integration-detail="feishu-bot"]');
    await bot.getByRole("switch", { name: "Chat with Persona", exact: true }).waitFor();
    await bot.getByText("Send to", { exact: true }).waitFor();
    assert.equal(await bot.locator("[data-channel-routing]").count(), 0);
    assert.equal(await bot.getByRole("button", { name: "Send test message", exact: true }).count(), 0);
    assert.equal(await bot.getByRole("button", { name: "Send test", exact: true }).count(), 1);
    await noOverflow(page);

    await labs(page, false);
    await openNav();
    await page.getByRole("button", { name: /^Connectors\b/ }).click();
    await page.locator('[data-settings-group="mail"]').waitFor();
    await page.goto(url);
    await page.waitForURL(u => u.searchParams.get("tab") === "extensions");
    await legacyPages(page);
    const saved = await (await context.request.get("/api/v1/channels/wechat")).json();
    assert.equal(saved.thread_mode, mode);
    assert.equal(saved.thread_idle_hours, idle);
    assert.deepEqual(errors, []);
    console.log(`PASS ${width}px ${theme}: legacy tabs, Labs, reload, routing, layout and rollback`);
  } catch (error) {
    console.error(`FAIL ${width}px ${theme}`, errors);
    throw error;
  } finally {
    await context.close();
  }
}

const browser = await chromium.launch(process.env.CHROME_BIN ? { executablePath: process.env.CHROME_BIN } : { channel: "chrome" });
try {
  for (const width of [390, 1080, 1440]) for (const theme of ["light", "dark"]) await scenario(browser, width, theme);
} finally {
  await browser.close();
}
