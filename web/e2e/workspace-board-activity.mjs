// Production app with invented HTTP/WebSocket responses; no live hub, existing profile, or permitted writes.
// node web/e2e/workspace-board-activity.mjs <loopback-web-origin> <artifact-directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const [address, artifacts, ...args] = process.argv.slice(2);
assert(address && artifacts, "Supply an owned loopback web origin and artifact directory");
const base = new URL(address);
assert(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname) && base.protocol === "http:", "Only an owned loopback HTTP server is allowed");
assert(!base.username && !base.password && base.pathname === "/" && !base.search && !base.hash, "Supply an origin without credentials or a route");
const output = resolve(artifacts);
await mkdir(output, { recursive: true });
const executable = args.includes("--chromium") ? args[args.indexOf("--chromium") + 1] : undefined;
const result = { status: "running", browser: "Browser plugin not available; fresh headless Playwright contexts", checks: [], screenshots: [], runs: [] };
const watchdog = setTimeout(() => { console.error("TIMEOUT board activity acceptance (300s)"); process.exit(124); }, 300_000);
const AT = "2026-11-13T12:00:00Z";
const machine = { id: "example-computer", name: "Example computer", online: true, os: "linux" };
const card = (id, title) => ({ id, title, machine_id: machine.id, workspace_id: `workspace-${id}`, source: "folder", cwd: `/work/${id}`, branch: "main", column_id: "todo", pinned: false, archived: false });
const cards = [card("alpha", "Alpha workspace"), card("beta", "Beta workspace")];
const changes = Array.from({ length: 500 }, (_, index) => ({
  kind: "card_updated", id: index === 0 ? "alpha" : index === 1 ? "beta" : `past-workspace-${index}`,
  title: index === 0 ? "Historical Alpha name" : index === 1 ? "Historical Beta name" : `Historical workspace ${index}`,
  fields: index === 0 ? ["title", "column", "pinned"] : ["archived"],
  before: { title: index === 0 ? "Original Alpha name" : `Workspace ${index}`, column_id: "todo", column_title: "To do", pinned: false, archived: false },
  after: { title: index === 0 ? "Historical Alpha name" : `Workspace ${index}`, column_id: "review", column_title: "In review", pinned: true, archived: true },
}));
const summaries = Array.from({ length: 45 }, (_, index) => ({ revision: String(600 - index), at: AT, change_count: index === 0 ? 500 : 1, card_count: index === 1 ? 0 : index === 0 ? 500 : 1, kinds: [index === 1 ? "column_renamed" : index === 2 ? "card_removed" : "card_updated"] }));
const latestSummary = { revision: "601", at: AT, change_count: 1, card_count: 1, kinds: ["card_added"] };
const catalog = async (language, name) => JSON.parse(await readFile(new URL(`../src/i18n/locales/${language}/${name}.json`, import.meta.url), "utf8"));
const format = (value, fields) => Object.entries(fields).reduce((text, [key, replacement]) => text.replaceAll(`{${key}}`, String(replacement)), value);
const waitForCount = (locator, count) => locator.evaluateAll((elements) => elements.length).then((actual) => assert.equal(actual, count));

async function open(browser, size = "wide", theme = "light", language = "en", textSize = "standard", mode = "normal") {
  const [strings, boardWords, atomWords, sidebarWords] = await Promise.all([catalog(language, "workspaceboard-activity"), catalog(language, "workspaceboard"), catalog(language, "ui-atoms"), catalog(language, "sidebar")]);
  const run = { size, theme, language, textSize, mode, reads: [], writes: [], errors: [], blocked: [], aborted: [] };
  result.runs.push(run);
  const context = await browser.newContext({ viewport: size === "wide" ? { width: 1440, height: 1000 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce", hasTouch: size === "narrow", isMobile: size === "narrow", serviceWorkers: "block" });
  await context.addInitScript(({ theme, language, textSize }) => {
    localStorage.setItem("persona.token", "synthetic-activity-token");
    localStorage.setItem("persona.language", language);
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.text-size", textSize);
    localStorage.setItem("persona.workspace-board.view", "manual");
  }, { theme, language, textSize });
  let socket, newer = false, failList = false, failOlder = false, failDetailOffset, holdList = false, releaseList;
  const board = () => ({ revision: newer ? 601 : 600, columns: [{ id: "todo", title: "To do" }, { id: "review", title: "In review" }], cards, can_write: mode !== "readonly", ...(mode !== "legacy" ? { activity: true } : {}) });
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base.origin) { run.blocked.push(url.pathname); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const path = url.pathname.replace(/^\/api\/v1/, "");
    try {
      if (!["GET", "HEAD"].includes(request.method())) {
        run.writes.push({ path, method: request.method() });
        throw new Error(`Activity never writes: ${request.method()} ${path}`);
      }
      if (path.startsWith("/workspace-board/activity")) {
        assert.equal(request.headers()["x-persona-manager"], "assistant", "History reads explicitly name their owning default manager");
        assert.notEqual(mode, "legacy", "Older managers receive no history requests");
        const entry = { path, query: Object.fromEntries(url.searchParams), manager: request.headers()["x-persona-manager"], returned: 0, status: 200 };
        run.reads.push(entry);
        if (path === "/workspace-board/activity") {
          assert.equal(url.searchParams.get("limit"), "20", "Summary requests remain bounded");
          if (holdList) await new Promise((done) => { releaseList = done; });
          if (request.failure()) return; // The dialog deliberately aborted this held read.
          if (failList || (failOlder && url.searchParams.has("before"))) { entry.status = 500; return route.fulfill({ status: 500, json: { code: "internal", error: "Synthetic activity read failed" } }); }
          let rows = newer ? [latestSummary, ...summaries] : summaries;
          if (url.searchParams.has("card_id")) {
            assert.equal(url.searchParams.get("card_id"), "alpha");
            rows = rows.filter((item) => ["600", "590", "580"].includes(item.revision));
          }
          if (url.searchParams.has("before")) rows = rows.filter((item) => BigInt(item.revision) < BigInt(url.searchParams.get("before")));
          const page = rows.slice(0, 20); entry.returned = page.length;
          return route.fulfill({ json: { entries: page, latest_revision: newer ? "601" : "600", oldest_revision: "556", retention_limit: 500, ...(rows.length > page.length ? { next_cursor: page.at(-1).revision } : {}) } });
        }
        const revision = path.split("/").at(-1), offset = Number(url.searchParams.get("offset"));
        assert.equal(url.searchParams.get("limit"), "30", "Detail requests remain bounded");
        assert(!url.searchParams.has("card_id"), "A grouped revision detail is not silently reduced to the card filter");
        assert(Number.isSafeInteger(offset) && offset >= 0);
        if (failDetailOffset === offset) { entry.status = 500; return route.fulfill({ status: 500, json: { code: "internal", error: "Synthetic detail read failed" } }); }
        const source = revision === "600" ? changes : revision === "598" ? [{ kind: "card_removed", id: "removed", title: "Removed workspace", before: { title: "Removed workspace", column_id: "todo", column_title: "To do" } }]
          : [{ kind: "column_renamed", id: "review", title: "In review", before: { title: "Review" }, after: { title: "In review" } }];
        const page = source.slice(offset, offset + 30); entry.returned = page.length;
        return route.fulfill({ json: { revision, at: AT, changes: page, total: source.length, ...(offset + page.length < source.length ? { next_offset: offset + page.length } : {}) } });
      }
      const data = {
        "/workspace-board": board(), "/workspace-board/settings": { enabled: true, can_write: mode !== "readonly" },
        "/workspaces": { workspaces: cards.map((item) => ({ id: item.workspace_id, machine_id: item.machine_id, source: "folder", source_id: item.id, title: item.title, cwd: item.cwd, online: true, available: true })), errors: [] },
        "/machines": { machines: [machine] }, "/sessions": { sessions: [] }, "/interactions": { interactions: [] },
        "/assistant": { machine_id: machine.id, conversations: [], tasks: [], memory: [] }, "/todos": { todos: [] },
        "/agents/sessions": { sessions: [] }, "/agents/prefs": {}, "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
        "/managers": { managers: [{ id: "assistant", name: "Example assistant", builtin: true, online: true }], default: "assistant" },
        "/extensions": { extensions: [] }, "/extensions/ui": { slots: [] }, "/personas": { personas: [] },
      };
      return route.fulfill({ json: data[path] ?? {} });
    } catch (error) {
      run.errors.push(`Fixture: ${error.message}`);
      return route.fulfill({ status: 500, json: { error: "Unexpected fixture request" } });
    }
  });
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    socket = ws;
    ws.send(JSON.stringify({ type: "hello", server_version: "synthetic-activity", hub_name: "Example hub", protocol: 1, features: [] }));
    ws.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      if (message.type === "ping" || message.type === "app_ping") ws.send(JSON.stringify({ type: "pong", id: message.id }));
      else if (message.type !== "client_hello") run.errors.push(`Unexpected socket command: ${message.type}`);
    });
  });
  const page = await context.newPage(); page.setDefaultTimeout(12_000);
  page.on("pageerror", (error) => run.errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error" && !/Failed to load resource: the server responded with a status of 500/.test(message.text())) run.errors.push(message.text()); });
  page.on("requestfailed", (request) => { if (request.url().includes("/workspace-board/activity")) run.aborted.push({ url: new URL(request.url()).pathname, error: request.failure()?.errorText }); });
  await page.goto(`${base.origin}/workspace-board`);
  await page.getByRole("heading", { name: boardWords.title, exact: true }).waitFor();
  await page.locator('[data-card-id="alpha"]').waitFor();
  assert.equal(new URL(page.url()).pathname, "/workspace-board"); assert((await page.title()).length > 0);
  await page.waitForTimeout(250);
  assert.equal(run.reads.length, 0, "A closed activity reader sends no requests");
  const reader = () => page.locator("[data-board-activity]");
  const row = (revision = "600") => reader().locator(`[data-activity-revision="${revision}"]`);
  const listReads = () => run.reads.filter((entry) => entry.path === "/workspace-board/activity");
  const detailReads = () => run.reads.filter((entry) => entry.path !== "/workspace-board/activity");
  const openActivity = async () => { await page.getByRole("button", { name: boardWords.activity, exact: true }).click(); await reader().waitFor(); };
  const expand = async (revision = "600") => { await row(revision).locator(":scope > button").click(); };
  const shot = async (state) => {
    await page.evaluate(() => document.fonts.ready); await page.waitForTimeout(200);
    assert.equal(await page.locator("vite-error-overlay").count(), 0);
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "No horizontal page overflow");
    const dialog = page.getByRole("dialog");
    if (await dialog.count()) assert(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), "No horizontal dialog overflow");
    const filename = `${size}-${theme}-${language}-${textSize}-${mode}-${state}.png`;
    await page.screenshot({ path: join(output, filename) }); result.screenshots.push(filename);
  };
  const close = async () => { await page.getByRole("dialog").getByRole("button", { name: atomWords.close, exact: true }).click(); await page.getByRole("dialog").waitFor({ state: "hidden" }); };
  const healthy = () => { assert.deepEqual(run.errors, []); assert.deepEqual(run.blocked, []); assert.deepEqual(run.writes, []); };
  const captureFailure = async (error) => {
    const stem = `${size}-${theme}-${language}-${textSize}-${mode}-failure`;
    await page.screenshot({ path: join(output, `${stem}.png`) }).catch(() => {});
    await writeFile(join(output, `${stem}.txt`), await page.locator("body").innerText().catch(() => "Page unavailable"));
    throw error;
  };
  return { page, context, run, strings, boardWords, sidebarWords, reader, row, listReads, detailReads, openActivity, expand, shot, close, healthy, captureFailure,
    setFailList: (value) => { failList = value; }, setFailOlder: (value) => { failOlder = value; }, setFailDetail: (offset) => { failDetailOffset = offset; },
    hold: () => { holdList = true; }, release: () => { holdList = false; releaseList?.(); },
    push: (manager = "assistant") => { if (manager === "assistant") newer = true; socket.send(JSON.stringify({ type: "workspace_board", manager, revision: 601 })); },
  };
}

async function normal(browser, size, theme, language = "en", textSize = "standard") {
  const x = await open(browser, size, theme, language, textSize);
  const { page, strings: s, reader, row, listReads, detailReads } = x;
  try {
    await x.openActivity(); await row().waitFor();
    await waitForCount(reader().locator("[data-activity-revision]"), 20);
    assert.equal(detailReads().length, 0, "The first page contains summaries only");
    assert.equal(await reader().getByText("Historical Alpha name", { exact: true }).count(), 0);
    assert.equal(listReads().length, 1);
    if (textSize === "large") assert.equal(await page.locator("html").getAttribute("data-text-size"), "large");
    await x.shot("summaries");
    await x.expand(); await row().locator("[data-activity-change]").nth(29).waitFor();
    assert.equal(detailReads().length, 1); assert.equal(detailReads()[0].returned, 30);
    await row().locator(":scope > button").scrollIntoViewIfNeeded();
    await x.shot("changes");
    await row().getByRole("button", { name: format(s.more_details, { count: 470 }), exact: true }).click();
    await row().locator("[data-activity-change]").nth(59).waitFor();
    assert.equal(detailReads().length, 2); assert.equal(detailReads()[1].query.offset, "30");
    await x.expand(); await x.expand(); assert.equal(detailReads().length, 2, "Collapsing and reopening reuses loaded detail pages");
    await reader().getByRole("button", { name: s.load_more, exact: true }).click();
    await row("561").waitFor();
    await waitForCount(reader().locator("[data-activity-revision]"), 40);
    assert.equal(await row().locator(":scope > button").getAttribute("aria-expanded"), "true");
    await waitForCount(row().locator("[data-activity-change]"), 60);
    await reader().getByRole("button", { name: s.load_more, exact: true }).click(); await row("556").waitFor();
    await waitForCount(reader().locator("[data-activity-revision]"), 45);
    assert.equal(await reader().getByRole("button", { name: s.load_more, exact: true }).count(), 0);
    const beforePush = x.run.reads.length;
    x.push("different-manager"); await page.waitForTimeout(100);
    assert.equal(await reader().getByText(s.newer, { exact: true }).count(), 0, "Another manager's event does not affect this reader");
    x.push(); await reader().getByText(s.newer, { exact: true }).waitFor();
    assert.equal(x.run.reads.length, beforePush, "Live activity offers refresh without reading or appending");
    assert.equal(await row("601").count(), 0);
    await reader().getByRole("button", { name: s.show_latest, exact: true }).scrollIntoViewIfNeeded(); await x.shot("newer-hint");
    await reader().getByRole("button", { name: s.show_latest, exact: true }).click(); await row("601").waitFor();
    await waitForCount(reader().locator("[data-activity-revision]"), 20);
    assert.equal(detailReads().length, 2, "Refresh does not eagerly reload details");
    await x.close();
    assert.equal(await page.getByRole("button", { name: x.boardWords.activity, exact: true }).evaluate((el) => el === document.activeElement), true, "Dialog close restores focus to Activity");
    await x.openActivity(); await row().waitFor(); await x.expand(); await row().locator("[data-activity-change]").nth(29).waitFor();
    await row().getByRole("button", { name: format(s.open_workspace_named, { title: "Alpha workspace" }), exact: true }).click();
    await page.getByRole("dialog", { name: "Alpha workspace", exact: true }).waitFor();
    assert.equal(await page.getByRole("dialog").count(), 1, "Opening a current workspace replaces rather than stacks dialogs");
    assert.equal(await reader().count(), 0, "Per-card history remains closed until requested");
    const beforeCard = listReads().length;
    await page.getByRole("dialog").getByRole("button", { name: x.boardWords.activity, exact: true }).click();
    await row().waitFor();
    assert.equal(listReads().length, beforeCard + 1); assert.equal(listReads().at(-1).query.card_id, "alpha");
    await waitForCount(reader().locator("[data-activity-revision]"), 3);
    await x.expand(); await row().locator("[data-activity-change]").nth(29).waitFor();
    await row().getByText(s.all_changes, { exact: true }).waitFor();
    await row().getByText("Historical Beta name", { exact: true }).waitFor();
    await row().locator(":scope > button").scrollIntoViewIfNeeded(); await x.shot("workspace-history");
    x.healthy();
    result.checks.push(`${size}/${theme}/${language}/${textSize}: lazy summary/detail pages, preserved expansion, explicit newer refresh, focus, card filter with complete grouped changes`);
  } catch (error) { await x.captureFailure(error); } finally { await x.context.close(); }
}

async function stress(browser) {
  const x = await open(browser), { strings: s, row } = x;
  try {
    await x.openActivity(); await row().waitFor(); await x.expand();
    for (let loaded = 30; loaded < 500; loaded += 30) {
      await row().locator("[data-activity-change]").nth(loaded - 1).waitFor();
      await row().getByRole("button", { name: format(s.more_details, { count: 500 - loaded }), exact: true }).click();
    }
    await row().locator("[data-activity-change]").nth(499).waitFor();
    await waitForCount(row().locator("[data-activity-change]"), 500);
    assert.equal(x.detailReads().length, 17);
    assert.deepEqual(x.detailReads().map((entry) => Number(entry.query.offset)), Array.from({ length: 17 }, (_, index) => index * 30));
    assert(x.detailReads().every((entry) => entry.returned <= 30), "A 500-change update is never downloaded as one oversized page");
    await x.expand("598");
    await row("598").getByText("Removed workspace", { exact: true }).first().waitFor();
    assert.equal(await row("598").locator("[data-activity-change]").getByRole("button").count(), 0, "Historical removed cards have no live workspace action");
    await row("598").locator(":scope > button").scrollIntoViewIfNeeded(); await x.shot("removed-workspace");
    x.healthy(); result.checks.push("500 changes:17 bounded pages, all changes reachable; removed workspace remains historical only");
  } catch (error) { await x.captureFailure(error); } finally { await x.context.close(); }
}

async function failures(browser) {
  const x = await open(browser), { page, strings: s, row, reader } = x;
  const retry = (scope) => scope.getByRole("button", { name: x.sidebarWords.retry, exact: true });
  try {
    x.setFailList(true); await x.openActivity(); await reader().getByRole("alert").waitFor(); await x.shot("list-failure");
    x.setFailList(false); await retry(reader()).click(); await row().waitFor();
    x.setFailDetail(0); await x.expand(); await row().getByRole("alert").waitFor();
    assert.equal(await row().locator("[data-activity-change]").count(), 0);
    x.setFailDetail(undefined); await retry(row()).click(); await row().locator("[data-activity-change]").nth(29).waitFor();
    x.setFailDetail(30); await row().getByRole("button", { name: format(s.more_details, { count: 470 }), exact: true }).click(); await row().getByRole("alert").waitFor();
    await waitForCount(row().locator("[data-activity-change]"), 30);
    await row().getByRole("alert").scrollIntoViewIfNeeded(); await x.shot("detail-failure");
    x.setFailDetail(undefined); await retry(row()).click(); await row().locator("[data-activity-change]").nth(59).waitFor();
    x.setFailDetail(60); await row().getByRole("button", { name: format(s.more_details, { count: 440 }), exact: true }).click(); await row().getByRole("alert").waitFor();
    await waitForCount(row().locator("[data-activity-change]"), 60);
    const readsBeforeToggle = x.detailReads().length;
    await x.expand(); await x.expand(); await page.waitForTimeout(200);
    await waitForCount(row().locator("[data-activity-change]"), 60);
    assert.equal(x.detailReads().length, readsBeforeToggle, "Reopening after a later-page failure keeps both loaded pages without refetching offset zero");
    await row().getByRole("alert").scrollIntoViewIfNeeded(); await x.shot("reopened-detail-failure");
    x.setFailDetail(undefined); await retry(row()).click(); await row().locator("[data-activity-change]").nth(89).waitFor();
    assert.equal(x.detailReads().at(-1).query.offset, "60", "Retry resumes the failed page rather than replacing already loaded changes");
    x.setFailOlder(true); await reader().getByRole("button", { name: s.load_more, exact: true }).click(); await reader().getByRole("alert").waitFor();
    await waitForCount(reader().locator("[data-activity-revision]"), 20); await waitForCount(row().locator("[data-activity-change]"), 90);
    x.setFailOlder(false); await retry(reader()).click(); await row("561").waitFor();
    x.healthy(); result.checks.push("Failures: initial summaries/details, later pages, collapse/reopen and older summaries preserve loaded data; retry resumes the failed offset");
  } catch (error) { await x.captureFailure(error); } finally { await x.context.close(); }
}

async function boundaries(browser) {
  for (const mode of ["legacy", "readonly"]) {
    const x = await open(browser, "narrow", "light", "en", "standard", mode);
    try {
      const entry = x.page.getByRole("button", { name: x.boardWords.activity, exact: true });
      if (mode === "legacy") assert.equal(await entry.count(), 0);
      else { await entry.click(); await x.row().waitFor(); await x.expand(); await x.row().locator("[data-activity-change]").nth(29).waitFor(); }
      await x.shot("compatibility"); x.healthy(); result.checks.push(`${mode}: capability and read-only behavior without any writes`);
    } catch (error) { await x.captureFailure(error); } finally { await x.context.close(); }
  }
  const x = await open(browser);
  try {
    x.hold(); await x.openActivity(); await x.reader().getByText(x.strings.loading, { exact: true }).waitFor();
    await x.close(); x.release();
    await x.page.waitForTimeout(300);
    assert(x.run.aborted.some((item) => /ABORTED/.test(item.error ?? "")), "Closing aborts the pending activity request");
    assert.equal(await x.reader().count(), 0);
    await x.openActivity(); await x.row().waitFor(); x.healthy();
    result.checks.push("Close/reopen: aborts pending history read, restores focus, starts an independent fresh reader");
  } catch (error) { await x.captureFailure(error); } finally { x.release(); await x.context.close(); }
}

let browser;
try {
  browser = await chromium.launch({ ...(executable ? { executablePath: executable } : { channel: "chrome" }), headless: true });
  for (const theme of ["light", "dark"]) for (const size of ["wide", "narrow"]) await normal(browser, size, theme);
  for (const [language, textSize] of [["en", "large"], ["zh-Hans", "standard"], ["ja", "standard"]]) await normal(browser, "narrow", "dark", language, textSize);
  await stress(browser); await failures(browser); await boundaries(browser);
  result.status = "passed";
} catch (error) {
  result.status = "failed"; result.failure = error.stack; process.exitCode = 1;
} finally {
  clearTimeout(watchdog); await browser?.close();
  await writeFile(join(output, "results.json"), `${JSON.stringify(result, null, 2)}\n`);
}
console.log(`${result.status === "passed" ? "PASS" : "FAIL"} board activity acceptance: ${result.checks.length} scenarios, ${result.screenshots.length} screenshots`);
if (result.failure) console.error(result.failure);
