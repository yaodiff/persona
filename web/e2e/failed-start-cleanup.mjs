// With the companion Python fixture and Vite proxying it, exercise both real delete menus on an archive-capable Hub.
// python3 -B web/e2e/failed-start-cleanup-fixture.py 19603
// (cd web && PERSONA_HUB_URL=http://127.0.0.1:19603 npx vite --host 127.0.0.1 --port 16603)
// node web/e2e/failed-start-cleanup.mjs http://127.0.0.1:16603 http://127.0.0.1:19603 /tmp/failed-start [--before]
import assert from "node:assert/strict";
import { EventEmitter, on } from "node:events";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [web, hub, out, mode] = process.argv.slice(2);
if (!out || ![web, hub].every(value => /^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(value))) {
  throw new Error("Pass the loopback Vite and fixture origins, then an artifact directory.");
}
const before = mode === "--before";
const failed = "studio/f0116003-0000-4000-8000-000000000001";
const active = "macbook/f0116003-0000-4000-8000-000000000002";
const history = "studio/f0116003-0000-4000-8000-000000000003";
const transcript = "Review the release notes before publishing.";
const deletedPath = "/api/v1/sessions/" + failed;
const profiles = [
  { width: 1280, height: 900, theme: "light", text: "standard" },
  { width: 1280, height: 900, theme: "dark", text: "large" },
  { width: 390, height: 844, theme: "light", text: "large" },
  { width: 390, height: 844, theme: "dark", text: "standard" },
];

async function read(path) {
  const response = await fetch(hub + path, { headers: { Authorization: "Bearer mock" } });
  assert.equal(response.status, 200, path);
  return response.json();
}
const fixture = command => read("/mock/failed-start/" + command);
const rowSelector = id => `[data-sidebar-column] a[href="/s/${id}"]`;
async function sidebarLink(page, id, resumed = false) {
  const columns = page.locator("[data-sidebar-column]");
  const onscreen = elements => elements.findIndex(e => {
    const box = e.getBoundingClientRect();
    return box.width > 0 && box.left >= -1 && box.left + box.width / 2 < innerWidth;
  });
  if (page.viewportSize().width < 640 && await columns.evaluateAll(onscreen) < 0) {
    await visibleButton(page, "Sidebar").click();
  }
  await page.waitForFunction(() => Array.from(document.querySelectorAll("[data-sidebar-column]")).some(e => {
    const box = e.getBoundingClientRect();
    return box.width > 0 && box.left >= -1 && box.left + box.width / 2 < innerWidth;
  }));
  const side = columns.nth(await columns.evaluateAll(onscreen));
  if (resumed) {
    const studio = side.getByRole("button", { name: /^Studio.*Online/i });
    await studio.waitFor();
    if (await studio.getAttribute("aria-expanded") === "false") await studio.click();
    const other = side.getByRole("button", { name: "Other", exact: true }).first();
    await other.waitFor();
    if (await other.getAttribute("aria-expanded") === "false") await other.click();
  }
  return side.locator(`a[href="/s/${id}"]`).first();
}
const item = (menu, name) => menu.getByRole("menuitem", { name, exact: true });
const visibleButton = (page, name) => page.getByRole("button", { name, exact: true }).locator("visible=true").first();

async function chat(page, id, title) {
  await page.goto(web + "/s/" + id);
  await page.locator("[data-chat-title]:visible").filter({ hasText: title }).waitFor();
}

async function openMenu(page, via, id = failed) {
  if (via === "chat") {
    await visibleButton(page, "More actions").click();
  } else {
    const link = await sidebarLink(page, id);
    await link.scrollIntoViewIfNeeded();
    if (page.viewportSize().width >= 640) await link.hover();
    await link.locator("..").getByRole("button", { name: "Agent actions", exact: true }).click();
  }
  const menu = page.getByRole("menu").locator("visible=true").first();
  await menu.waitFor();
  return menu;
}

async function checkControls(context) {
  for (const [id, title, busy] of [[active, "Prepare documentation", true], [history, "Earlier notes", false]]) {
    const page = await context.newPage();
    try {
      await chat(page, id, title);
      const menu = await openMenu(page, "chat", id);
      await item(menu, "More").click();
      const archive = page.getByRole("menuitem", { name: /^Archive\b/ }).first();
      assert.equal(await archive.isDisabled(), busy, title + " archive");
      assert.equal(await item(menu, "Close…").count(), busy ? 1 : 0, title + " close");
      assert.equal(await item(page, "Delete chat…").count(), 0, title + " delete");
    } finally {
      await page.close();
    }
  }
}

async function run(page, entry, tag) {
  await fixture("reset");
  const rows = (await read("/api/v1/sessions")).sessions;
  const row = rows.find(s => s.machine_id + "/" + s.session_id === failed);
  assert.equal(row.started_by_persona, true);
  assert.equal(row.status, "unknown"); assert.equal(row.state, "unknown");
  assert.equal(row.updated_at, "0001-01-01T00:00:00Z");
  assert.equal(row.capabilities, undefined);
  assert.equal((await read("/api/v1/machines")).machines.find(m => m.id === "studio").online, false);
  const name = entry === "chat" ? "Review release notes" : "Earlier notes";
  const events = new EventEmitter();
  page.on("websocket", socket => {
    if (new URL(socket.url()).pathname !== "/api/v1/ws") return;
    socket.on("framereceived", frame => {
      const data = JSON.parse(frame.payload);
      if (data.type === "sessions_delta" && data.machine_id === "studio") events.emit("delta", data);
    });
  });
  async function pushed(matches) {
    for await (const [frame] of on(events, "delta", { signal: AbortSignal.timeout(12_000) })) {
      if (matches(frame)) return frame;
    }
  }
  await chat(page, entry === "chat" ? failed : history, name);
  assert.equal(await page.evaluate(() => document.documentElement.dataset.textSize ?? "standard"), tag.endsWith("-large") ? "large" : "standard");
  const machineProbes = [];
  page.on("request", req => {
    const path = new URL(req.url()).pathname;
    if (req.method() === "POST" && path.startsWith("/api/v1/machines/studio/")) machineProbes.push(path);
  });
  const show = () => openMenu(page, entry);
  const shot = state => page.screenshot({ path: join(out, `${tag}-${entry}-${state}.png`), animations: "disabled" });
  let menu = await show();
  const remove = item(menu, "Delete chat…");
  const close = item(menu, "Close…");
  if (before) {
    await item(menu, "Archive").waitFor();
    assert.equal(await remove.count(), 0, "original menu unexpectedly offers Delete");
    await close.scrollIntoViewIfNeeded();
    await shot("before");
    return { tag, entry, archive: true, close: await close.isEnabled(), delete: false };
  }
  await item(menu, "More").click();
  await item(page, "Archive").waitFor();
  await page.keyboard.press("ArrowLeft");
  assert.equal(await close.count(), 0, "there is no computer process to close");
  await remove.scrollIntoViewIfNeeded();
  await shot("menu");
  await remove.click();
  const dialog = page.getByRole("dialog", { name: "Delete this chat?" });
  await dialog.waitFor();
  const detail = await dialog.innerText();
  assert.ok(detail.includes("Review release notes") && detail.includes("transcript on Studio stays") && detail.includes("comes back"), detail);
  for (const text of ["Cancel", "Delete"]) {
    const control = dialog.getByRole("button", { name: text, exact: true });
    await control.click({ trial: true }); // the phone confirmation slides up from the bottom
    const box = await control.boundingBox();
    const vp = page.viewportSize();
    assert.ok(box && box.x >= 0 && box.y >= 0 && box.x + box.width <= vp.width + 1 && box.y + box.height <= vp.height + 1, tag + " " + text + " " + JSON.stringify(box));
  }
  await shot("confirm");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  assert.deepEqual(await fixture("state"), { mutations: [], listed: true, transcript: [transcript] });
  assert.deepEqual(machineProbes, [], "cancel must work with the computer offline");
  menu = await show();
  await item(menu, "Delete chat…").click();
  const removal = pushed(frame => frame.remove.includes(row.session_id));
  const deleted = page.waitForResponse(r => r.request().method() === "DELETE" && new URL(r.url()).pathname === deletedPath);
  await dialog.getByRole("button", { name: "Delete", exact: true }).click();
  assert.equal((await deleted).status(), 200);
  await removal;
  await dialog.waitFor({ state: "hidden" });
  const state = await fixture("state");
  assert.deepEqual(state, { mutations: [["DELETE", deletedPath, "full"]], listed: false, transcript: [transcript] });
  assert.ok(!(await read("/api/v1/sessions")).sessions.some(s => s.session_id === row.session_id));
  assert.deepEqual(machineProbes, [], "only the Hub gets a cleanup request");
  const retained = (await read(deletedPath + "/items")).items.map(i => i.text);
  assert.deepEqual(retained, [transcript]);
  await page.locator(rowSelector(failed)).first().waitFor({ state: "detached" });
  const laterWork = pushed(frame => frame.upsert.some(s => s.session_id === row.session_id && s.state === "working"));
  const resumed = await fixture("relist");
  assert.equal(resumed.listed, true);
  await laterWork;
  const working = (await read("/api/v1/sessions")).sessions.find(s => s.session_id === row.session_id);
  assert.equal(working.state, "working");
  await (await sidebarLink(page, failed, true)).click();
  await page.getByText(transcript, { exact: true }).first().waitFor();
  await page.getByText("Review resumed on Studio.", { exact: true }).first().waitFor();
  return { tag, entry, canceled: true, ...state, relisted: working.state, machineProbes };
}

mkdirSync(out, { recursive: true });
const results = [];
const browser = await chromium.launch({ channel: "chrome" });
try {
  for (const { width, height, theme, text } of profiles) {
    const tag = `${width}-${theme}-${text}`;
    for (const entry of ["sidebar", "chat"]) {
      const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: theme, locale: "en-US",
        hasTouch: width < 640, reducedMotion: "reduce", serviceWorkers: "block" });
      try {
        await ctx.addInitScript(({ theme, text }) => {
          localStorage.setItem("persona.token", "mock");
          localStorage.setItem("persona.language", "en");
          localStorage.setItem("persona.theme", theme);
          localStorage.setItem("persona.text-size", text);
          localStorage.setItem("persona.agentGrouping", "machine");
        }, { theme, text });
        await ctx.route("**/*", route => new URL(route.request().url()).origin === web ? route.continue() : route.abort());
        const page = await ctx.newPage();
        page.setDefaultTimeout(12_000);
        const errors = [];
        page.on("pageerror", error => errors.push(error.message));
        const report = await run(page, entry, tag).catch(async error => {
          const sidebar = page.locator("[data-sidebar-column]").first();
          console.error("Current sidebar:", (await sidebar.textContent().catch(() => "")).slice(-1300));
          await page.screenshot({ path: join(out, "failure.png"), animations: "disabled" });
          throw error;
        });
        if (!before && entry === "chat") await checkControls(ctx);
        assert.deepEqual(errors, [], tag);
        results.push(report);
        console.log("PASS", tag, entry);
      } finally {
        await ctx.close();
      }
    }
  }
} finally {
  await browser.close();
  writeFileSync(join(out, "results.json"), JSON.stringify(results, null, 2) + "\n");
}
