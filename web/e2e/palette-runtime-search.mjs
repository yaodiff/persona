// Real web app, synthetic HTTP/WebSocket only. Never uses an existing profile or sends work to an Agent.
// node web/e2e/palette-runtime-search.mjs <owned-loopback-origin> <artifact-directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";
const [address, artifacts, ...args] = process.argv.slice(2);
assert(address && artifacts, "Supply an owned loopback origin and artifact directory");
const base = new URL(address);
assert(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname) && base.protocol === "http:" && base.pathname === "/" && !base.search && !base.hash && !base.username && !base.password, "Use a loopback HTTP origin without credentials or a route");
const output = resolve(artifacts); await mkdir(output, { recursive: true });
const result = { status: "running", browser: "Browser plugin not available; fresh headless Playwright", checks: [], screenshots: [], runs: [] };
const watchdog = setTimeout(() => { console.error("TIMEOUT runtime search acceptance (300s)"); process.exit(124); }, 300_000);
const at = (index = 0) => new Date(Date.UTC(2026, 10, 14, 0, 0, 0) - index * 1000).toISOString();
const session = (id, title, index = 0, machine = "laptop") => ({ machine_id: machine, session_id: id, title, cwd: "/work/example", agent: "claude", state: "idle", status: "idle", updated_at: at(index), last_text: "A short retained preview" });
const initial = Array.from({ length: 50 }, (_, i) => session(`loaded-${String(i).padStart(2, "0")}`, `Loaded workspace ${i}`, i));
initial[0] = session("local-needle", "Needle local workspace");
initial[1] = session("keyboard-a", "Keyboard first choice", 1);
initial[2] = session("keyboard-b", "Keyboard second choice", 2);
initial[3] = session("unconfirmed", "Needle unconfirmed local workspace", 3);
const remote = [initial[0], ...Array.from({ length: 84 }, (_, i) => session(`unseen-${String(i).padStart(2, "0")}`, `Needle remote workspace ${String(i).padStart(2, "0")}`, i + 100))];
remote[1] = session("shared-id", "Needle on laptop", 100);
remote[2] = session("shared-id", "Needle on desktop", 101, "desktop");
remote[3] = session("truncated", "Short unrelated title…", 102);
const keyboard = [session("high-rank", "Keyboard inserted first", 0), ...initial.slice(1, 3)];
const machines = [{ id: "laptop", name: "Example laptop", online: true, os: "linux" }, { id: "desktop", name: "Example desktop", online: true, os: "linux" }];
const assistant = { ...session("assistant-home", "Example assistant conversation"), assistant: true };
const catalog = async (language, file) => JSON.parse(await readFile(new URL(`../src/i18n/locales/${language}/${file}.json`, import.meta.url), "utf8"));
const format = (text, fields) => Object.entries(fields).reduce((value, [key, replacement]) => value.replaceAll(`{${key}}`, String(replacement)), text);
const until = async (condition, message) => {
  const end = Date.now() + 10_000;
  while (!(await condition()) && Date.now() < end) await new Promise((done) => setTimeout(done, 20));
  assert(await condition(), message);
};

async function setup(browser, size = "wide", theme = "light", language = "en", textSize = "standard", mode = "normal") {
  const words = await catalog(language, "runtime-search");
  const paletteWords = await catalog(language, "palette");
  const run = { size, theme, language, textSize, mode, searches: [], assistantReads: [], listReads: [], writes: [], errors: [], external: [], aborts: [] };
  result.runs.push(run);
  const context = await browser.newContext({ viewport: size === "wide" ? { width: 1440, height: 1000 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce", hasTouch: size === "narrow", isMobile: size === "narrow", serviceWorkers: "block" });
  await context.addInitScript(({ theme, language, textSize }) => {
    localStorage.setItem("persona.token", "synthetic-search-token");
    localStorage.setItem("persona.language", language); localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.text-size", textSize); localStorage.setItem("persona.agentGrouping", "state");
  }, { theme, language, textSize });
  const held = new Map(), failures = new Map();
  const fail = (query, offset, status) => failures.set(`${query}/${offset}`, status);
  const hold = (query) => held.set(query, { release: undefined });
  const release = (query) => { const pending = held.get(query); held.delete(query); pending?.release?.(); };
  const searchRows = (query) => query.toLowerCase() === "needle" ? remote
    : query.toLowerCase() === "keyboard" ? keyboard
    : query === "fresh" ? [session("fresh", "Fresh result")]
    : query === "stale" ? [session("stale", "Stale result")]
    : query.startsWith("machine:desktop") ? [remote[2]] : [];
  const match = (row, query) => ({ score: query === "keyboard" ? row.session_id === "high-rank" ? 120 : 100 : 100,
    ...(row.session_id === "truncated" ? { evidence: { role: "agent", before: "Deep in the original response: ", match: "needle", after: " was found beyond the summary boundary." } } : {}) });
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname.replace(/^\/api\/v1/, "");
    if (url.origin !== base.origin) { run.external.push(url.pathname); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    try {
      if (!["GET", "HEAD"].includes(request.method())) { run.writes.push({ method: request.method(), path }); throw new Error(`Unexpected write ${path}`); }
      if (path === "/sessions" && url.searchParams.has("search")) {
        assert.notEqual(mode, "legacy", "An older hub receives no runtime search query");
        assert.equal(url.searchParams.get("summary"), "1"); assert.equal(url.searchParams.get("roots"), "1"); assert.equal(url.searchParams.get("limit"), "40");
        assert(!url.searchParams.has("q"), "The raw operator query goes to search, never q");
        const query = url.searchParams.get("search"), offset = Number(url.searchParams.get("cursor")?.split(":").at(-1) ?? 0);
        const read = { query, offset, params: Object.fromEntries(url.searchParams), status: 200, returned: 0 }; run.searches.push(read);
        const pending = held.get(query); if (pending) await new Promise((done) => { pending.release = done; });
        if (request.failure()) return;
        const failure = failures.get(`${query}/${offset}`);
        if (failure) { read.status = failure; return route.fulfill({ status: failure, json: { code: failure === 409 ? "cursor_expired" : "internal", error: "Synthetic search failure" } }); }
        const all = searchRows(query), rows = all.slice(offset, offset + 40); read.returned = rows.length;
        const next = offset + rows.length;
        return route.fulfill({ json: { sessions: rows, search_hits: Object.fromEntries(rows.map((row) => [JSON.stringify([row.machine_id, row.session_id]), match(row, query)])), has_more: next < all.length, ...(next < all.length ? { next_cursor: `search:${next}` } : {}), revision: "search-r1", total: all.length } });
      }
      if (path === "/assistant/search") {
        const query = url.searchParams.get("q"); run.assistantReads.push(query);
        return route.fulfill({ json: { results: query?.toLowerCase() === "needle" ? [{ session_id: "assistant-found", machine_id: "laptop", title: "Remembered conversation", updated_at: at(), title_match: false, hits: [{ item_id: "old-message", seq: 1, role: "user", ts: at(), snippet: "We discussed needle in this conversation.", ranges: [[13, 19]] }] }] : [], partial: false } });
      }
      if (path === "/sessions") {
        run.listReads.push(Object.fromEntries(url.searchParams));
        let rows = initial;
        if (url.searchParams.has("session_id")) rows = [...initial, ...remote, ...keyboard].filter((s) => s.session_id === url.searchParams.get("session_id") && s.machine_id === url.searchParams.get("machine_id")).slice(0, 1);
        else if (url.searchParams.has("machine_id")) rows = rows.filter((s) => s.machine_id === url.searchParams.get("machine_id"));
        const counts = { total: 135, machine: { laptop: 134, desktop: 1 }, project: {}, group: {}, state: { idle: 135 }, revision: "root-r1" };
        if (url.searchParams.has("counts")) return route.fulfill({ json: counts });
        const root = url.searchParams.get("limit") === "50" && !url.searchParams.has("state") && !url.searchParams.has("machine_id") && !url.searchParams.has("cursor");
        return route.fulfill({ json: { sessions: rows, remove: [], has_more: root, ...(root ? { next_cursor: "root-next" } : {}), revision: "root-r1", total: root ? 135 : rows.length, ...(url.searchParams.has("with_counts") ? { counts } : {}) } });
      }
      if (/^\/sessions\/[^/]+\/[^/]+$/.test(path)) {
        const [, , mid, sid] = path.split("/"); const item = [...initial, ...remote, ...keyboard].find((s) => s.machine_id === mid && s.session_id === sid);
        return route.fulfill({ json: { session: item } });
      }
      const data = {
        "/machines": { machines }, "/interactions": { interactions: [] }, "/assistant": { machine_id: "laptop", main: assistant.session_id, conversations: [assistant], tasks: [], memory: [] },
        "/todos": { todos: [] }, "/agents/sessions": { sessions: [] }, "/agents/prefs": {}, "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
        "/managers": { managers: [{ id: "assistant", name: "Example assistant", builtin: true, online: true }], default: "assistant" },
        "/extensions": { extensions: [] }, "/extensions/ui": { slots: [] }, "/personas": { personas: [] },
        "/workspace-board/settings": { enabled: false, can_write: true },
      };
      if (path.endsWith("/items") || path.endsWith("/feed")) return route.fulfill({ json: { items: [], has_more: false } });
      return route.fulfill({ json: data[path] ?? {} });
    } catch (error) { run.errors.push(`Fixture: ${error.message}`); return route.fulfill({ status: 500, json: { error: "Unexpected fixture request" } }); }
  });
  await context.routeWebSocket("**/api/v1/ws**", (socket) => {
    socket.send(JSON.stringify({ type: "hello", server_version: "synthetic-runtime-search", hub_name: "Example hub", protocol: 1, features: ["sessions_list", "sessions_lookup", "sessions_hierarchy", "assistant_search", ...(mode === "legacy" ? [] : ["sessions_search"])] }));
    socket.onMessage((raw) => {
      const event = JSON.parse(String(raw));
      if (event.type === "ping") socket.send(JSON.stringify({ type: "pong", id: event.id }));
      else if (!["client_hello", "subscribe", "unsubscribe", "seen"].includes(event.type)) run.errors.push(`Unexpected socket command: ${event.type}`);
      // Subscriptions and read receipts are acknowledged only inside this synthetic fixture; no upstream exists.
    });
  });
  const page = await context.newPage(); page.setDefaultTimeout(12_000);
  page.on("pageerror", (error) => run.errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error" && !/Failed to load resource: the server responded with a status of (500|409)/.test(message.text())) run.errors.push(message.text()); });
  page.on("requestfailed", (request) => { const url = new URL(request.url()); if (url.searchParams.has("search")) run.aborts.push({ query: url.searchParams.get("search"), reason: request.failure()?.errorText }); });
  await page.goto(`${base.origin}/workspace-board`); await page.locator("main").waitFor();
  await until(() => run.listReads.some((read) => read.limit === "50" && read.summary === "1"), "The browser loaded its first 50 summary rows");
  const dialog = () => page.getByRole("dialog");
  const input = () => dialog().getByRole("combobox");
  const options = () => dialog().getByRole("option");
  const row = (title) => options().filter({ has: page.getByText(title, { exact: true }) });
  const status = () => page.locator("[data-runtime-search-status]");
  const open = async () => { await page.keyboard.press("ControlOrMeta+k"); await input().waitFor(); };
  const query = async (value) => { await input().fill(value); };
  const loaded = async (count, total) => { await status().getByText(format(words.loaded_count, { count, total }), { exact: true }).waitFor(); };
  const shot = async (state) => {
    await page.evaluate(() => document.fonts.ready); await page.waitForTimeout(200);
    assert.equal(await page.locator("vite-error-overlay").count(), 0);
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "No page overflow");
    if (await dialog().count()) assert(await dialog().evaluate((el) => { const r = el.getBoundingClientRect(); return el.scrollWidth <= el.clientWidth + 1 && r.left >= 0 && r.right <= innerWidth + 1 && r.bottom <= innerHeight + 1; }), "Palette stays within the viewport");
    const file = `${size}-${theme}-${language}-${textSize}-${mode}-${state}.png`; await page.screenshot({ path: join(output, file) }); result.screenshots.push(file);
  };
  const healthy = () => { assert.deepEqual(run.errors, []); assert.deepEqual(run.external, []); assert.deepEqual(run.writes, []); };
  const failure = async (error) => {
    const stem = `${size}-${theme}-${language}-${textSize}-${mode}-failure`;
    await page.screenshot({ path: join(output, `${stem}.png`) }).catch(() => {});
    await writeFile(join(output, `${stem}.txt`), await page.locator("body").innerText().catch(() => "Unavailable")); throw error;
  };
  return { context, page, run, words, paletteWords, dialog, input, options, row, status, open, query, loaded, shot, healthy, failure, fail, hold, release };
}

async function normal(browser, size, theme, language = "en", textSize = "standard") {
  const x = await setup(browser, size, theme, language, textSize), { page, run, words } = x;
  try {
    await x.open(); await page.waitForTimeout(300); assert.equal(run.searches.length, 0, "Empty search makes no request");
    await x.query(">settings"); await page.waitForTimeout(300); assert.equal(run.searches.length, 0, "Command mode makes no runtime search request");
    await x.query("needle"); await x.loaded(40, 85);
    await x.row("Needle on laptop").waitFor(); await x.row("Needle on desktop").waitFor();
    assert.equal(await x.row("Needle local workspace").count(), 1, "The same local/remote tuple renders once");
    assert.equal(await x.row("Needle unconfirmed local workspace").count(), 0, "The first authoritative server page removes local-only matches even while more pages remain");
    assert.equal(await x.row("Short unrelated title…").locator("[data-evidence] mark").innerText(), "needle", "A hit beyond truncated summary remains searchable by server evidence");
    await x.row("Remembered conversation").waitFor();
    assert(run.assistantReads.includes("needle"), "Plain queries still search assistant chat history");
    await x.row("Needle on laptop").scrollIntoViewIfNeeded(); await x.shot("first-page");
    await x.status().getByRole("button", { name: words.more, exact: true }).click(); await x.loaded(80, 85);
    await x.row("Needle remote workspace 78").waitFor();
    assert.equal(await x.options().count(), 81, "All 80 runtime matches plus the assistant match remain reachable beyond the old 40-row cap");
    const finalMore = x.status().getByRole("button", { name: words.more, exact: true });
    await finalMore.focus(); await page.keyboard.press("Enter"); await x.loaded(85, 85);
    await until(() => x.input().evaluate((el) => el === document.activeElement), "Completing the final page returns keyboard focus from the removed More button to search");
    await page.keyboard.press("Tab");
    assert(await x.dialog().evaluate((el) => el.contains(document.activeElement)), "Tab remains in the palette after the final More control disappears");
    await x.row("Needle remote workspace 83").waitFor();
    assert.equal(await x.options().count(), 86);
    assert.equal(await x.status().getByRole("button", { name: words.more, exact: true }).count(), 0);
    await x.status().scrollIntoViewIfNeeded(); await x.shot("all-pages");
    assert.deepEqual(run.searches.filter((read) => read.query === "needle").map((read) => read.offset), [0, 40, 80]);
    const operator = "machine:desktop needle";
    await x.query(operator); await x.loaded(1, 1); await x.row("Needle on desktop").waitFor();
    assert.equal(await x.row("Needle on laptop").count(), 0); assert.equal(await x.row("Remembered conversation").count(), 0);
    assert.equal(run.searches.at(-1).query, operator, "Operator spelling travels intact to the server");
    await page.waitForTimeout(300); assert(!run.assistantReads.includes(operator), "Agent operators do not enter assistant search");
    await x.shot("operator");
    if (textSize === "large") assert.equal(await page.locator("html").getAttribute("data-text-size"), "large");
    await x.query(""); await page.waitForTimeout(300);
    assert.equal(await x.row("Needle on desktop").count(), 0, "Remote query rows never pollute the global initial session window");
    x.healthy(); result.checks.push(`${size}/${theme}/${language}/${textSize}: unseen Agent, full evidence, tuple identity/dedup, authoritative membership, 85 matches across3pages, final-page focus, raw operators, assistant search, empty/command isolation`);
  } catch (error) { await x.failure(error); } finally { await x.context.close(); }
}

async function keyboardSelection(browser) {
  const x = await setup(browser);
  try {
    x.hold("keyboard"); await x.open(); await x.query("keyboard");
    await x.row("Keyboard second choice").waitFor();
    await until(() => x.run.searches.some((read) => read.query === "keyboard"), "Delayed runtime query started");
    await x.input().press("ArrowDown");
    assert.equal(await x.row("Keyboard second choice").getAttribute("aria-selected"), "true");
    x.release("keyboard"); await x.loaded(3, 3); await x.row("Keyboard inserted first").waitFor();
    assert.equal(await x.row("Keyboard second choice").getAttribute("aria-selected"), "true", "Async higher-ranked insertion preserves the keyboard-selected identity");
    await x.shot("keyboard-identity"); await x.input().press("Enter");
    await x.page.waitForURL("**/s/laptop/keyboard-b");
    assert.equal(new URL(x.page.url()).pathname, "/s/laptop/keyboard-b");
    x.healthy(); result.checks.push("Keyboard: delayed high-rank insertion preserves selected identity and Enter opens the original second choice");
  } catch (error) { await x.failure(error); } finally { x.release("keyboard"); await x.context.close(); }
}

async function failures(browser) {
  const x = await setup(browser), { words } = x;
  const errorInView = async () => assert(await x.status().getByRole("alert").evaluate((el) => {
    const rect = el.getBoundingClientRect();
    let parent = el.parentElement;
    while (parent && !/auto|scroll/.test(getComputedStyle(parent).overflowY)) parent = parent.parentElement;
    const clip = parent?.getBoundingClientRect() ?? { top: 0, bottom: innerHeight };
    return rect.top >= clip.top - 1 && rect.bottom <= clip.bottom + 1;
  }), "A page failure remains visible where More was clicked without scrolling back to the first result");
  try {
    x.fail("needle", 0, 500); await x.open(); await x.query("needle"); await x.status().getByRole("alert").waitFor();
    await x.row("Needle local workspace").waitFor(); await x.row("Needle unconfirmed local workspace").waitFor(); await x.status().scrollIntoViewIfNeeded(); await x.shot("first-error");
    x.fail("needle", 0, undefined); await x.status().getByRole("button", { name: words.retry, exact: true }).focus(); await x.page.keyboard.press("Enter"); await x.loaded(40, 85);
    await until(() => x.input().evaluate((el) => el === document.activeElement), "A successful retry returns keyboard focus from its removed button");
    assert.equal(await x.row("Needle unconfirmed local workspace").count(), 0, "Confirmed server matches supersede local-only results after retry");
    x.fail("needle", 40, 500); await x.status().getByRole("button", { name: words.more, exact: true }).click(); await x.status().getByRole("alert").waitFor();
    await x.row("Needle on desktop").waitFor(); await errorInView();
    assert.equal(await x.row("Needle unconfirmed local workspace").count(), 0, "A later-page error cannot bring back unconfirmed local matches");
    await x.status().scrollIntoViewIfNeeded(); await x.shot("more-error");
    x.fail("needle", 40, undefined); await x.status().getByRole("button", { name: words.retry, exact: true }).focus(); await x.page.keyboard.press("Enter"); await x.loaded(80, 85);
    await until(() => x.input().evaluate((el) => el === document.activeElement), "A successful page retry returns keyboard focus to search");
    x.fail("needle", 80, 409); await x.status().getByRole("button", { name: words.more, exact: true }).click(); await x.status().getByText(words.expired, { exact: true }).waitFor();
    await x.row("Needle remote workspace 78").waitFor();
    assert.equal(await x.status().getByRole("button", { name: words.more, exact: true }).count(), 0);
    assert(await x.status().getByRole("button", { name: words.search_again, exact: true }).evaluate((el) => el === document.activeElement), "Cursor expiry transfers focus to Search again without hiding the explanation");
    await errorInView(); await x.status().scrollIntoViewIfNeeded(); await x.shot("expired");
    x.fail("needle", 80, undefined); await x.status().getByRole("button", { name: words.search_again, exact: true }).click(); await x.loaded(40, 85);
    assert.equal(x.run.searches.at(-1).offset, 0, "Expired cursor starts a new explicit query baseline");
    x.healthy(); result.checks.push("Failures: first/more retry retain local/prior matches; expired cursor offers explicit Search again");
  } catch (error) { await x.failure(error); } finally { await x.context.close(); }
}

async function cancellation(browser) {
  const x = await setup(browser);
  try {
    x.hold("stale"); await x.open(); await x.query("stale"); await until(() => x.run.searches.some((r) => r.query === "stale"), "Old query started");
    await x.query("fresh"); await x.loaded(1, 1); await x.row("Fresh result").waitFor(); x.release("stale"); await x.page.waitForTimeout(200);
    assert.equal(await x.row("Stale result").count(), 0);
    await until(() => x.run.aborts.some((r) => r.query === "stale"), "A replaced query aborts its outstanding read");
    x.hold("stale"); await x.query("stale"); await until(() => x.run.searches.filter((r) => r.query === "stale").length === 2, "Close-cancellation query started");
    await x.input().press("Escape"); await x.dialog().waitFor({ state: "hidden" }); x.release("stale");
    await until(() => x.run.aborts.filter((r) => r.query === "stale").length === 2, "Closing cancels the pending query");
    await x.open(); await x.query("fresh"); await x.loaded(1, 1); x.healthy();
    result.checks.push("Cancellation: replacing a query and closing the palette aborts pending reads; stale results never return");
  } catch (error) { await x.failure(error); } finally { x.release("stale"); await x.context.close(); }
}

async function legacy(browser) {
  const x = await setup(browser, "narrow", "dark", "ja", "standard", "legacy");
  try {
    await x.open(); await x.query("needle"); await x.status().getByText(x.words.loaded_only, { exact: true }).waitFor();
    await x.row("Needle local workspace").waitFor(); await x.row("Remembered conversation").waitFor();
    assert.equal(await x.row("Needle on desktop").count(), 0); assert.equal(x.run.searches.length, 0);
    await x.shot("loaded-only"); x.healthy(); result.checks.push("Older hub: visible loaded-only scope, local matches and assistant search remain available without unsupported requests");
  } catch (error) { await x.failure(error); } finally { await x.context.close(); }
}

let browser;
try {
  const index = args.indexOf("--chromium"); browser = await chromium.launch({ ...(index >= 0 ? { executablePath: args[index + 1] } : { channel: "chrome" }), headless: true });
  for (const theme of ["light", "dark"]) for (const size of ["wide", "narrow"]) await normal(browser, size, theme);
  for (const [language, textSize] of [["en", "large"], ["zh-Hans", "standard"], ["ja", "standard"]]) await normal(browser, "narrow", "dark", language, textSize);
  await keyboardSelection(browser); await failures(browser); await cancellation(browser); await legacy(browser);
  result.status = "passed";
} catch (error) { result.status = "failed"; result.failure = error.stack; process.exitCode = 1; }
finally { clearTimeout(watchdog); await browser?.close(); await writeFile(join(output, "results.json"), `${JSON.stringify(result, null, 2)}\n`); }
console.log(`${result.status === "passed" ? "PASS" : "FAIL"} runtime-Agent search acceptance: ${result.checks.length} scenarios, ${result.screenshots.length} screenshots`);
if (result.failure) console.error(result.failure);
