// Real app with a shared synthetic settings server across fresh browser contexts. No real hub or Agent writes.
// node web/e2e/saved-agent-searches.mjs <owned-loopback-origin> <artifact-directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";
const [address, artifacts, ...args] = process.argv.slice(2);
assert(address && artifacts, "Supply a loopback web origin and an artifact directory");
const base = new URL(address);
assert(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname) && base.protocol === "http:" && base.pathname === "/" && !base.username && !base.password && !base.search && !base.hash, "Use an owned loopback HTTP origin without a route or credentials");
const output = resolve(artifacts); await mkdir(output, { recursive: true });
const results = { status: "running", browser: "Browser plugin not available; fresh headless Playwright", checks: [], screenshots: [], runs: [] };
const watchdog = setTimeout(() => { console.error("TIMEOUT saved Agent searches acceptance (300s)"); process.exit(124); }, 300_000);
const AT = "2026-11-14T10:00:00Z", PATH = "/saved-agent-searches";
const sid = (n) => `search_${n.toString(16).padStart(24, "0")}`;
const item = (n, name, query) => ({ id: sid(n), name, query, created_at: AT, updated_at: AT });
const initial = () => [item(1, "Working sessions", "state:working"), item(2, "Recent sessions", "since:7d")];
const machines = [{ id: "laptop", name: "Example laptop", online: true, os: "linux" }, { id: "desktop", name: "Example desktop", online: true, os: "linux" }];
const sessions = machines.map((m, n) => ({ machine_id: m.id, session_id: `agent-${n}`, title: `${m.name} task`, cwd: "/work/example", agent: "claude", state: n ? "working" : "idle", status: n ? "working" : "idle", updated_at: AT }));
const readWords = async (language, file) => JSON.parse(await readFile(new URL(`../src/i18n/locales/${language}/${file}.json`, import.meta.url), "utf8"));
const fill = (text, values) => Object.entries(values).reduce((out, [key, value]) => out.replaceAll(`{${key}}`, String(value)), text);
const until = async (condition, message) => { const end = Date.now() + 10_000; while (!(await condition()) && Date.now() < end) await new Promise((done) => setTimeout(done, 20)); assert(await condition(), message); };

class Fixture {
  defaultManager = "assistant";
  documents = new Map([["assistant", { revision: "1", searches: initial() }], ["second", { revision: "1", searches: [item(10, "Other manager search", "machine:laptop")] }]]);
  sockets = new Set();
  nextID = 20;
  failNext = false;
  document(manager = this.defaultManager) { return this.documents.get(manager); }
  push(manager = this.defaultManager) { for (const ws of this.sockets) ws.send(JSON.stringify({ type: "saved_agent_searches", revision: this.document(manager)?.revision ?? "2", manager })); }
  switchDefault() { this.defaultManager = "second"; for (const ws of this.sockets) ws.send(JSON.stringify({ type: "managers" })); }
  externalEdit(id, name, query, notify = true) {
    const old = this.document();
    this.documents.set(this.defaultManager, { revision: String(BigInt(old.revision) + 1n), searches: old.searches.map((value) => value.id === id ? { ...value, name, query, updated_at: AT } : value) });
    if (notify) this.push();
  }
  mutate(body, manager) {
    const previous = this.document(manager);
    assert.equal(typeof body.revision, "string", "CAS revisions retain decimal precision");
    if (body.revision !== previous.revision) return { status: 409, json: { code: "conflict", error: "Saved searches changed elsewhere" } };
    if (this.failNext) { this.failNext = false; return { status: 500, json: { code: "internal", error: "Synthetic save failed" } }; }
    const next = structuredClone(previous), index = next.searches.findIndex((value) => value.id === body.id);
    assert(["add", "update", "delete", "move", "restore"].includes(body.action), "Only saved query definitions may change");
    const allowed = { add: ["name", "query"], update: ["id", "name", "query"], delete: ["id"], move: ["id", "before_id"], restore: ["item", "before_id"] };
    assert(Object.keys(body).every((key) => ["action", "revision", ...allowed[body.action]].includes(key)), "No execution fields enter saved searches");
    if (body.action === "add") next.searches.push(item(this.nextID++, body.name.trim(), body.query.trim()));
    else if (body.action === "update") { assert(index >= 0); next.searches[index] = { ...next.searches[index], name: body.name.trim(), query: body.query.trim(), updated_at: AT }; }
    else if (body.action === "delete") { assert(index >= 0); next.searches.splice(index, 1); }
    else {
      let value;
      if (body.action === "restore") { value = structuredClone(body.item); assert(!next.searches.some((other) => other.id === value.id)); }
      else { assert(index >= 0); [value] = next.searches.splice(index, 1); }
      const target = body.before_id ? next.searches.findIndex((other) => other.id === body.before_id) : next.searches.length;
      assert(target >= 0); next.searches.splice(target, 0, value);
    }
    next.revision = String(BigInt(previous.revision) + 1n); this.documents.set(manager, next);
    return { status: 200, json: { ...next, can_write: true } };
  }
}

async function open(browser, fixture, size = "wide", theme = "light", language = "en", textSize = "standard", mode = "normal") {
  const words = await readWords(language, "saved-agent-searches"), paletteWords = await readWords(language, "palette"), atoms = await readWords(language, "ui-atoms");
  words.manage = paletteWords.saved_searches; words.save_current = paletteWords.save_search; words.cancel = (await readWords(language, "common")).cancel;
  const run = { size, theme, language, textSize, mode, reads: [], writes: [], errors: [], external: [], importFailures: 0, runtimeReads: 0 }; results.runs.push(run);
  const context = await browser.newContext({ viewport: size === "wide" ? { width: 1440, height: 1000 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce", hasTouch: size === "narrow", isMobile: size === "narrow", serviceWorkers: "block" });
  await context.addInitScript(({ theme, language, textSize }) => { localStorage.setItem("persona.token", "synthetic-saved-search-token"); localStorage.setItem("persona.theme", theme); localStorage.setItem("persona.language", language); localStorage.setItem("persona.text-size", textSize); }, { theme, language, textSize });
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname.replace(/^\/api\/v1/, "");
    if (url.origin !== base.origin) { run.external.push(url.pathname); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) {
      if (mode === "import-once" && /\/SavedSearchesDialog-[^/]+\.js$/.test(url.pathname) && !run.importFailures) {
        run.importFailures++; return route.abort("failed");
      }
      return route.continue();
    }
    try {
      if (path === PATH) {
        const manager = request.headers()["x-persona-manager"];
        assert(manager && fixture.documents.has(manager), "Saved queries explicitly name their owning manager");
        if (request.method() === "GET") {
          run.reads.push({ manager });
          if (mode === "unsupported") return route.fulfill({ status: 503, json: { code: "not_supported", error: "Unsupported manager" } });
          return route.fulfill({ json: { ...fixture.document(manager), can_write: mode !== "readonly" } });
        }
        assert.equal(request.method(), "POST"); assert.equal(mode, "normal");
        const body = request.postDataJSON(), response = fixture.mutate(body, manager); run.writes.push({ body, manager, status: response.status });
        await route.fulfill(response); if (response.status === 200) fixture.push(manager); return;
      }
      assert(["GET", "HEAD"].includes(request.method()), `No Agent send, start, stop, file or Git writes: ${path}`);
      if (mode === "import-once" && path === "/sessions" && url.searchParams.has("search")) {
        run.runtimeReads++;
        assert(!url.searchParams.has("manager"), "Runtime search remains hub-wide");
        const remote = { ...sessions[1], session_id: "remote-working", title: "Remote working task" };
        return route.fulfill({ json: { sessions: [remote], revision: "recovered-query", total: 1, has_more: false,
          search_hits: { [JSON.stringify([remote.machine_id, remote.session_id])]: { score: 105 } } } });
      }
      const data = {
        "/machines": { machines }, "/sessions": { sessions }, "/interactions": { interactions: [] }, "/assistant": { machine_id: "laptop", conversations: [], tasks: [], memory: [] },
        "/todos": { todos: [] }, "/agents/sessions": { sessions: [] }, "/agents/prefs": {}, "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
        "/managers": { managers: [{ id: "assistant", name: "Example assistant", builtin: true, online: true }, { id: "second", name: "Other assistant", online: true }], default: fixture.defaultManager },
        "/extensions": { extensions: [] }, "/extensions/ui": { slots: [] }, "/personas": { personas: [] }, "/workspace-board/settings": { enabled: false, can_write: true },
      };
      return route.fulfill({ json: data[path] ?? {} });
    } catch (error) { run.errors.push(`Fixture: ${error.message}`); return route.fulfill({ status: 500, json: { error: "Unexpected synthetic request" } }); }
  });
  const ownSockets = new Set();
  await context.routeWebSocket("**/api/v1/ws**", (socket) => {
    fixture.sockets.add(socket); ownSockets.add(socket);
    socket.send(JSON.stringify({ type: "hello", server_version: "synthetic-saved-search", hub_name: "Example hub", protocol: 1, features: mode === "import-once" ? ["managers", "sessions_search", "saved_agent_searches"] : ["managers"] }));
    socket.onMessage((raw) => { const message = JSON.parse(String(raw)); if (message.type === "ping") socket.send(JSON.stringify({ type: "pong", id: message.id })); else assert.equal(message.type, "client_hello", "No Agent commands over WebSocket"); });
  });
  const page = await context.newPage(); page.setDefaultTimeout(12_000);
  page.on("pageerror", (error) => run.errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error" && !/Failed to load resource: the server responded with a status of (500|503|409)/.test(message.text())
      && !(mode === "import-once" && run.importFailures === 1 && /Failed to load resource: net::ERR_FAILED/.test(message.text()))) run.errors.push(message.text()); });
  await page.goto(`${base.origin}/workspace-board`); await page.locator("main").waitFor(); await page.waitForTimeout(200);
  assert.equal(run.reads.length, 0, "No saved-query reads before opening search");
  const dialog = () => page.getByRole("dialog");
  const row = (id) => dialog().locator(`[data-saved-search="${id}"]`);
  const palette = async () => { await page.keyboard.press("ControlOrMeta+k"); await dialog().getByRole("combobox").waitFor(); };
  const manage = async () => { await dialog().getByRole("button", { name: words.manage, exact: true }).click(); await dialog().getByRole("heading", { name: words.title, exact: true }).waitFor(); };
  const nameInput = () => dialog().getByRole("textbox", { name: words.name, exact: true });
  const queryInput = () => dialog().getByRole("textbox", { name: words.query, exact: true });
  const save = () => dialog().getByRole("button", { name: words.save, exact: true });
  const close = async () => { await dialog().getByRole("button", { name: atoms.close, exact: true }).click(); await dialog().getByRole("combobox").waitFor(); await dialog().getByRole("combobox").press("Escape"); await dialog().waitFor({ state: "hidden" }); };
  const shot = async (state) => {
    await page.evaluate(() => document.fonts.ready); await page.waitForTimeout(200);
    assert.equal(await page.locator("vite-error-overlay").count(), 0); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "No page overflow");
    assert.equal(await dialog().count(), 1, "Saved-search editing uses one modal at a time");
    assert(await dialog().evaluate((el) => el.scrollWidth <= el.clientWidth + 1), "No horizontal modal overflow");
    const file = `${size}-${theme}-${language}-${textSize}-${mode}-${state}.png`; await page.screenshot({ path: join(output, file) }); results.screenshots.push(file);
  };
  const healthy = () => { assert.deepEqual(run.errors, []); assert.deepEqual(run.external, []); };
  const capture = async (error) => { const file = `${size}-${theme}-${language}-${textSize}-${mode}-failure`; await page.screenshot({ path: join(output, `${file}.png`) }).catch(() => {}); await writeFile(join(output, `${file}.txt`), await page.locator("body").innerText().catch(() => "Unavailable")); throw error; };
  const dispose = async () => { for (const socket of ownSockets) fixture.sockets.delete(socket); await context.close(); };
  return { context, page, words, paletteWords, run, dialog, row, palette, manage, nameInput, queryInput, save, close, shot, healthy, capture, dispose };
}

async function workflow(browser, size, theme, language = "en", textSize = "standard", twoBrowsers = false) {
  const fixture = new Fixture(), x = await open(browser, fixture, size, theme, language, textSize);
  let peer;
  try {
    if (twoBrowsers) { peer = await open(browser, fixture); await peer.palette(); await peer.manage(); await peer.row(sid(1)).waitFor(); }
    await x.palette(); await x.dialog().getByRole("combobox").fill("machine:desktop");
    await x.dialog().getByRole("button", { name: x.words.save_current, exact: true }).click(); await x.nameInput().waitFor();
    assert.equal(await x.queryInput().inputValue(), "machine:desktop", "Saving starts with the actual current query");
    await x.nameInput().fill("Desktop work");
    await x.nameInput().focus(); await x.page.keyboard.press("Tab");
    assert(await x.queryInput().evaluate((el) => el === document.activeElement), "Keyboard Tab reaches the query editor");
    await x.shot("save-draft"); await x.save().focus(); await x.page.keyboard.press("Enter");
    await x.dialog().getByRole("combobox").waitFor();
    assert(await x.dialog().getByRole("combobox").evaluate((el) => el === document.activeElement), "Saving returns focus to the search field");
    await x.manage();
    await until(() => fixture.document().searches.some((value) => value.name === "Desktop work"), "The named query was saved");
    const id = fixture.document().searches.find((value) => value.name === "Desktop work").id;
    await x.row(id).waitFor(); if (peer) await peer.row(id).waitFor();
    await x.shot("saved");
    await x.row(id).getByRole("button", { name: fill(x.words.apply_named, { name: "Desktop work" }), exact: true }).click();
    await x.dialog().getByRole("combobox").waitFor(); assert.equal(await x.dialog().getByRole("combobox").inputValue(), "machine:desktop");
    assert.equal(new URL(x.page.url()).pathname, "/workspace-board", "Applying a saved query never opens or executes a result");
    await x.shot("applied");
    await x.manage(); await x.row(id).getByRole("button", { name: fill(x.words.edit_named, { name: "Desktop work" }), exact: true }).click();
    await x.nameInput().fill("Discard this draft");
    const writesBeforeCancel = x.run.writes.length;
    await x.dialog().getByRole("button", { name: x.words.cancel, exact: true }).focus(); await x.page.keyboard.press("Enter");
    await x.row(id).getByText("Desktop work", { exact: true }).waitFor();
    assert(await x.dialog().evaluate((el) => el.contains(document.activeElement)), "Cancel returns keyboard focus to the list inside the same dialog");
    await x.page.keyboard.press("Tab");
    assert(await x.dialog().evaluate((el) => el.contains(document.activeElement)), "Tab remains inside management after cancelling an editor");
    assert.equal(x.run.writes.length, writesBeforeCancel, "Cancelling a draft writes nothing");
    await x.row(id).getByRole("button", { name: fill(x.words.edit_named, { name: "Desktop work" }), exact: true }).click();
    await x.nameInput().fill("Desktop review"); await x.queryInput().fill("machine:desktop state:working");
    await until(() => x.save().isEnabled(), "The edited draft is ready to save");
    await x.save().focus(); await x.page.keyboard.press("Enter");
    await x.row(id).getByText("Desktop review", { exact: true }).waitFor();
    assert(await x.dialog().evaluate((el) => el.contains(document.activeElement)), "Saving an edit returns keyboard focus to the list inside the same dialog");
    await x.page.keyboard.press("Tab");
    assert(await x.dialog().evaluate((el) => el.contains(document.activeElement)), "Tab remains inside management after saving an editor");
    await x.row(id).getByRole("button", { name: fill(x.words.move_up, { name: "Desktop review" }), exact: true }).click();
    await until(() => fixture.document().searches[1]?.id === id, "Move up persists the selected query's order");
    const beforeDelete = structuredClone(fixture.document().searches);
    const remove = x.row(id).getByRole("button", { name: fill(x.words.delete_named, { name: "Desktop review" }), exact: true });
    await until(() => remove.isEnabled(), "The preceding reorder is acknowledged before another action");
    await remove.focus(); assert(await remove.evaluate((el) => el === document.activeElement)); await x.page.keyboard.press("Enter");
    await x.row(id).waitFor({ state: "hidden" });
    await x.dialog().getByRole("button", { name: x.words.undo, exact: true }).waitFor();
    await x.dialog().getByRole("button", { name: x.words.undo, exact: true }).click(); await x.row(id).waitFor();
    assert.deepEqual(fixture.document().searches, beforeDelete, "Undo restores identity, query, timestamps and exact position");
    if (peer) { await peer.row(id).getByText("Desktop review", { exact: true }).waitFor(); assert.equal(peer.run.writes.length, 0, "A second browser synchronizes without writing"); }
    await x.shot("restored");
    await x.dialog().getByRole("button", { name: x.words.back_to_search, exact: true }).click();
    await x.dialog().getByRole("combobox").fill("");
    await x.dialog().locator(`[data-saved-search-quick="${id}"]`).click();
    assert.equal(await x.dialog().getByRole("combobox").inputValue(), "machine:desktop state:working", "A quick saved row only fills the search field");
    await x.manage();
    const reads = x.run.reads.length; fixture.push("unrelated-manager"); await x.page.waitForTimeout(250); assert.equal(x.run.reads.length, reads, "Another manager's hint is ignored");
    await x.close(); const closedReads = x.run.reads.length; fixture.push(); await x.page.waitForTimeout(500); assert.equal(x.run.reads.length, closedReads, "Closed search surfaces neither poll nor refresh on hints");
    if (textSize === "large") assert.equal(await x.page.locator("html").getAttribute("data-text-size"), "large");
    x.healthy(); peer?.healthy(); results.checks.push(`${size}/${theme}/${language}/${textSize}: save current, apply, keyboard edit Cancel/Save focus, reorder, delete/Undo, single modal, owner hints and closed lifetime${peer ? ", second-browser live sync" : ""}`);
  } catch (error) { await x.capture(error); } finally { await x.dispose(); await peer?.dispose(); }
}

async function conflictAndFailure(browser) {
  const fixture = new Fixture(), x = await open(browser, fixture);
  try {
    await x.palette(); await x.manage(); await x.row(sid(1)).waitFor();
    await x.row(sid(1)).getByRole("button", { name: fill(x.words.edit_named, { name: "Working sessions" }), exact: true }).click();
    await x.nameInput().fill("My reviewed query"); await x.queryInput().fill("machine:desktop state:working");
    fixture.externalEdit(sid(1), "Changed elsewhere", "machine:laptop", false);
    await x.save().click(); await x.dialog().getByRole("alert").waitFor();
    assert.equal(x.run.writes.at(-1).status, 409); assert.equal(await x.nameInput().inputValue(), "My reviewed query"); assert.equal(await x.queryInput().inputValue(), "machine:desktop state:working");
    await x.dialog().getByText("Changed elsewhere", { exact: true }).waitFor(); await x.shot("conflict");
    await x.dialog().getByRole("button", { name: x.words.review_save, exact: true }).click();
    await x.row(sid(1)).getByText("My reviewed query", { exact: true }).waitFor();
    assert.equal(fixture.document().searches[0].name, "My reviewed query");
    await x.dialog().getByRole("button", { name: x.words.add, exact: true }).click();
    await x.nameInput().fill("Recoverable search"); await x.queryInput().fill("state:idle"); fixture.failNext = true;
    await x.save().click(); await x.dialog().getByRole("alert").waitFor();
    assert.equal(await x.nameInput().inputValue(), "Recoverable search"); assert.equal(await x.queryInput().inputValue(), "state:idle");
    assert(!fixture.document().searches.some((value) => value.name === "Recoverable search")); await x.shot("save-failure");
    await x.save().click(); await until(() => fixture.document().searches.some((value) => value.name === "Recoverable search"), "Explicit retry saved the retained draft");
    x.healthy(); results.checks.push("CAS conflict and failed save: dirty drafts preserved, latest saved version visible, explicit reviewed overwrite and retry");
  } catch (error) { await x.capture(error); } finally { await x.dispose(); }
}

async function boundaries(browser) {
  for (const mode of ["readonly", "unsupported"]) {
    const x = await open(browser, new Fixture(), "narrow", "dark", "en", "standard", mode);
    try {
      await x.palette(); await x.manage();
      if (mode === "readonly") {
        await x.row(sid(1)).waitFor(); const edits = x.row(sid(1)).getByRole("button", { name: fill(x.words.edit_named, { name: "Working sessions" }), exact: true });
        assert(await edits.count() === 0 || !await edits.isEnabled(), "Read-only clients cannot edit definitions");
        await x.shot("read-only");
        await x.row(sid(1)).getByRole("button", { name: fill(x.words.apply_named, { name: "Working sessions" }), exact: true }).click();
        assert.equal(await x.dialog().getByRole("combobox").inputValue(), "state:working");
      } else { await x.dialog().getByText(x.words.unsupported, { exact: true }).waitFor(); await x.shot("older-manager"); }
      assert.equal(x.run.writes.length, 0); x.healthy(); results.checks.push(`${mode}: useful read/apply or upgrade explanation without any setting writes`);
    } catch (error) { await x.capture(error); } finally { await x.dispose(); }
  }
}

async function scopeChange(browser) {
  const fixture = new Fixture(), x = await open(browser, fixture);
  try {
    await x.palette(); await x.manage(); await x.row(sid(1)).waitFor();
    await x.row(sid(1)).getByRole("button", { name: fill(x.words.edit_named, { name: "Working sessions" }), exact: true }).click();
    await x.nameInput().fill("Old-owner private draft");
    fixture.switchDefault();
    await until(async () => x.run.reads.some((read) => read.manager === "second") || await x.dialog().count() === 0, "The reader retires or moves when the default manager changes");
    if (!await x.dialog().count()) await x.palette();
    if (await x.dialog().getByRole("combobox").count()) await x.manage();
    await x.row(sid(10)).waitFor();
    assert.equal(await x.dialog().locator('input[value="Old-owner private draft"]').count(), 0);
    assert(!(await x.dialog().innerText()).includes("Working sessions"), "The new default cannot reuse another manager's definitions");
    assert.equal(x.run.writes.length, 0, "Changing scope never saves the old draft");
    await x.shot("default-manager"); x.healthy(); results.checks.push("Default-manager change: previous draft and definitions retired, reads explicitly use the new owner, no writes");
  } catch (error) { await x.capture(error); } finally { await x.dispose(); }
}

async function importRecovery(browser) {
  const x = await open(browser, new Fixture(), "wide", "light", "en", "standard", "import-once");
  try {
    await x.palette(); await x.dialog().getByRole("combobox").fill("state:working");
    await x.dialog().getByRole("alert").filter({ hasText: x.paletteWords.saved_searches_failed }).waitFor();
    assert.equal(x.run.importFailures, 1, "The shared search-tools chunk failed once");
    assert.equal(x.run.reads.length, 0, "No saved-query request ran before its tools loaded");
    assert.equal(x.run.runtimeReads, 0, "No runtime query ran before its tools loaded");
    await x.shot("import-failed");
    await x.dialog().getByRole("button", { name: x.paletteWords.saved_searches_retry, exact: true }).click();
    await x.dialog().getByRole("option").filter({ hasText: "Remote working task" }).waitFor();
    assert(x.run.reads.length > 0 && x.run.runtimeReads > 0, "Explicit import retry restores both saved and runtime readers");
    await x.shot("import-recovered");
    await x.manage(); await x.row(sid(1)).waitFor();
    assert.equal(x.run.writes.length, 0, "Recovering search tools never performs a mutation");
    x.healthy(); results.checks.push("Shared search-tool import failure: explicit retry restores saved definitions and an unloaded runtime match without mutations");
  } catch (error) { await x.capture(error); } finally { await x.dispose(); }
}

let browser;
try {
  const i = args.indexOf("--chromium"); browser = await chromium.launch({ ...(i >= 0 ? { executablePath: args[i + 1] } : { channel: "chrome" }), headless: true });
  for (const theme of ["light", "dark"]) for (const size of ["wide", "narrow"]) await workflow(browser, size, theme, "en", "standard", size === "wide" && theme === "light");
  for (const [language, textSize] of [["en", "large"], ["zh-Hans", "standard"], ["ja", "standard"]]) await workflow(browser, "narrow", "dark", language, textSize);
  await conflictAndFailure(browser); await boundaries(browser); await scopeChange(browser); await importRecovery(browser); results.status = "passed";
} catch (error) { results.status = "failed"; results.failure = error.stack; process.exitCode = 1; }
finally { clearTimeout(watchdog); await browser?.close(); await writeFile(join(output, "results.json"), `${JSON.stringify(results, null, 2)}\n`); }
console.log(`${results.status === "passed" ? "PASS" : "FAIL"} saved Agent searches acceptance: ${results.checks.length} scenarios, ${results.screenshots.length} screenshots`);
if (results.failure) console.error(results.failure);
