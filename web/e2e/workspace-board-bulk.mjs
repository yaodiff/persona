// Real workflow board with synthetic HTTP/WebSocket responses only. No live hub, Agent or browser profile.
// node web/e2e/workspace-board-bulk.mjs <loopback-web-origin> <artifact-directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const [address, artifacts, ...args] = process.argv.slice(2);
assert(address && artifacts, "Supply an owned loopback web origin and an artifact directory");
const base = new URL(address);
assert(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Only a loopback web server is allowed");
assert(base.protocol === "http:" && !base.username && !base.password && base.pathname === "/" && !base.search && !base.hash, "Use an HTTP origin without credentials or a route");
const executable = args.includes("--chromium") ? args[args.indexOf("--chromium") + 1] : undefined;
const output = resolve(artifacts);
await mkdir(output, { recursive: true });
const result = { status: "running", browser: "Browser plugin not available; fresh headless Playwright context", checks: [], screenshots: [], runs: [] };
const deadline = setTimeout(() => { console.error("TIMEOUT workflow-board bulk acceptance (240s)"); process.exit(124); }, 240_000);
const machines = [
  { id: "example-laptop", name: "Example laptop", online: true, os: "linux" },
  { id: "example-desktop", name: "Example desktop", online: true, os: "linux" },
];
const card = (id, title, column = "todo", machine = machines[0].id) => ({ id, title, machine_id: machine, workspace_id: `workspace-${id}`, source: "folder", cwd: `/work/${id}`, branch: "main", column_id: column, pinned: false, archived: false });
const fixture = (mode) => ({
  revision: 1, can_write: mode !== "readonly", ...(mode !== "legacy" ? { batch_cards: true } : {}),
  columns: [{ id: "todo", title: "To do" }, { id: "review", title: "In review" }, { id: "done", title: "Done" }],
  cards: [card("alpha", "Alpha workspace"), card("beta", "Beta workspace"), card("gamma", "Gamma workspace", "todo", machines[1].id), card("delta", "Delta workspace", "review")],
});
const waitUntil = async (condition, message) => {
  const until = Date.now() + 10_000;
  while (!condition() && Date.now() < until) await new Promise((done) => setTimeout(done, 25));
  assert(condition(), message);
};

// The fixture admits metadata changes only. It deliberately has no Agent, filesystem or Git write handler.
function applyChanges(board, request) {
  assert.equal(request.action, "batch_cards");
  assert.equal(request.revision, board.revision, "The batch uses the currently acknowledged revision");
  assert(Array.isArray(request.changes) && request.changes.length > 0);
  assert.equal(new Set(request.changes.map((change) => change.id)).size, request.changes.length, "Each card occurs once");
  const next = structuredClone(board);
  for (const change of request.changes) {
    assert(Object.keys(change).every((key) => ["id", "column_id", "before_id", "pinned", "archived"].includes(key)), "Only reversible card metadata is sent");
    const before = next.cards.find((item) => item.id === change.id);
    assert(before, "Every changed card exists");
    if (change.column_id !== undefined) assert(next.columns.some((column) => column.id === change.column_id));
    const after = { ...before };
    for (const key of ["column_id", "pinned", "archived"]) if (change[key] !== undefined) after[key] = change[key];
    if (change.column_id !== undefined || change.before_id !== undefined) {
      next.cards = next.cards.filter((item) => item.id !== change.id);
      const index = change.before_id ? next.cards.findIndex((item) => item.id === change.before_id) : -1;
      assert(!change.before_id || index >= 0, "Ordering anchors exist");
      next.cards.splice(index < 0 ? next.cards.length : index, 0, after);
    } else next.cards = next.cards.map((item) => item.id === change.id ? after : item);
  }
  next.revision++;
  return next;
}

async function open(browser, size, theme, mode = "normal", language = "en", textSize = "standard") {
  const strings = JSON.parse(await readFile(new URL(`../src/i18n/locales/${language}/workspaceboard.json`, import.meta.url), "utf8"));
  const run = { size, theme, mode, language, textSize, writes: [], errors: [], blocked: [], hellos: 0 };
  result.runs.push(run);
  const context = await browser.newContext({ viewport: size === "wide" ? { width: 1440, height: 1000 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce", hasTouch: size === "narrow", isMobile: size === "narrow", serviceWorkers: "block" });
  await context.addInitScript(({ theme, language, textSize }) => {
    localStorage.setItem("persona.token", "synthetic-board-token");
    localStorage.setItem("persona.language", language);
    localStorage.setItem("persona.text-size", textSize);
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.workspace-board.view", "manual");
  }, { theme, language, textSize });
  let board = fixture(mode), rejection;
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base.origin) { run.blocked.push(url.pathname); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const path = url.pathname.replace(/^\/api\/v1/, "");
    try {
      if (!["GET", "HEAD"].includes(request.method())) {
        const body = request.postDataJSON();
        run.writes.push({ path, method: request.method(), body });
        assert.equal(path, "/workspace-board", "No Agent, filesystem or Git writes");
        assert.equal(request.method(), "POST");
        assert.equal(mode, "normal", "Legacy/read-only clients never attempt batch writes");
        if (rejection) {
          const status = rejection; rejection = undefined;
          if (status === 409) board = { ...board, revision: board.revision + 1 };
          return route.fulfill({ status, json: { code: status === 409 ? "conflict" : "internal", error: "Synthetic board write refused" } });
        }
        board = applyChanges(board, body);
        return route.fulfill({ json: board });
      }
      const data = {
        "/workspace-board": board,
        "/workspace-board/settings": { enabled: true, can_write: mode !== "readonly" },
        "/workspaces": { workspaces: board.cards.map((item) => ({ id: item.workspace_id, machine_id: item.machine_id, source: "folder", source_id: item.id, title: item.title, cwd: item.cwd, online: true, available: true })), errors: [] },
        "/machines": { machines }, "/sessions": { sessions: [] }, "/interactions": { interactions: [] },
        "/assistant": { machine_id: machines[0].id, conversations: [], tasks: [], memory: [] },
        "/todos": { todos: [] }, "/agents/sessions": { sessions: [] }, "/agents/prefs": {},
        "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
        "/managers": { managers: [{ id: "assistant", name: "Example assistant", builtin: true, online: true }], default: "assistant" },
        "/extensions": { extensions: [] }, "/extensions/ui": { slots: [] }, "/personas": { personas: [] },
      };
      return route.fulfill({ json: data[path] ?? {} });
    } catch (error) {
      run.errors.push(`Fixture: ${error.message}`);
      return route.fulfill({ status: 500, json: { error: "Unexpected fixture request" } });
    }
  });
  await context.routeWebSocket("**/api/v1/ws**", (socket) => {
    run.hellos++;
    socket.send(JSON.stringify({ type: "hello", server_version: "synthetic-board", hub_name: "Example hub", protocol: 1, features: [] }));
    socket.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      if (message.type === "app_ping") socket.send(JSON.stringify({ type: "pong", id: message.id }));
      else if (!["client_hello", "app_visible", "visibility", "activity"].includes(message.type)) run.errors.push(`Unexpected socket command: ${message.type}`);
    });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(10_000);
  page.on("pageerror", (error) => run.errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error" && !/Failed to load resource: the server responded with a status of (409|500)/.test(message.text())) run.errors.push(message.text());
  });
  await page.goto(`${base.origin}/workspace-board`);
  await page.getByRole("heading", { name: strings.title, exact: true }).waitFor();
  await page.locator('[data-card-id="alpha"]').waitFor();
  assert.equal(new URL(page.url()).pathname, "/workspace-board");
  assert((await page.title()).length > 0, "The application has a page title");
  const shot = async (state) => {
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(250); // Finish the UI's finite color/shadow transitions before capture.
    assert.equal(await page.locator("vite-error-overlay").count(), 0, "No framework overlay");
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "No horizontal page overflow");
    const variant = `${language !== "en" ? `-${language}` : ""}${textSize !== "standard" ? `-${textSize}` : ""}`;
    const filename = `${size}-${theme}-${mode}${variant}-${state}.png`;
    await page.screenshot({ path: join(output, filename) });
    result.screenshots.push(filename);
  };
  const chooseColumn = async (value) => {
    const picker = page.getByRole("combobox", { name: strings.pick_column, exact: true });
    if (await picker.isVisible()) await picker.selectOption(value);
  };
  const select = async (...names) => {
    if (!await page.getByRole("button", { name: strings.selection_done, exact: true }).count()) await page.getByRole("button", { name: strings.select_cards, exact: true }).click();
    for (const name of names) await page.getByRole("checkbox", { name: strings.select_card.replace("{card}", `${name} workspace`), exact: true }).check();
  };
  const undo = async () => {
    const rev = board.revision;
    await page.getByRole("button", { name: strings.bulk_undo, exact: true }).click();
    await waitUntil(() => board.revision > rev, "Undo reached the fixture");
    await page.getByRole("button", { name: strings.bulk_undo, exact: true }).waitFor({ state: "hidden" });
  };
  const finish = async () => {
    if (await page.getByRole("button", { name: strings.selection_done, exact: true }).count()) await page.getByRole("button", { name: strings.selection_done, exact: true }).click();
  };
  return { context, page, run, shot, strings, chooseColumn, select, undo, finish, getBoard: () => board, rejectNext: (status) => { rejection = status; } };
}

async function normal(browser, size, theme) {
  const x = await open(browser, size, theme);
  const { page, run, shot, select, undo, finish, chooseColumn, getBoard } = x;
  try {
    const initial = structuredClone(getBoard().cards);
    await shot("initial");
    await select();
    await page.getByRole("button", { name: "Select all visible", exact: true }).click();
    await page.getByText(`${size === "wide" ? 4 : 3} selected`, { exact: true }).waitFor();
    assert.equal(await page.locator('[data-card-id="delta"] input[type="checkbox"]').isChecked(), size === "wide", "A hidden column is excluded on narrow screens");
    await page.getByRole("button", { name: "Clear selection", exact: true }).click();
    await select("Alpha", "Beta");
    const alphaCheckbox = page.getByRole("checkbox", { name: "Select Alpha workspace", exact: true });
    await alphaCheckbox.focus();
    await alphaCheckbox.press("Space");
    await page.getByText("1 selected", { exact: true }).waitFor();
    await alphaCheckbox.press("Space");
    await page.getByText("2 selected", { exact: true }).waitFor();
    assert.equal(run.writes.length, 0, "Selection itself performs no write");
    await shot("selected");
    await page.getByRole("combobox", { name: "Move selected to", exact: true }).selectOption("review");
    await page.getByRole("button", { name: "Move", exact: true }).click();
    await waitUntil(() => getBoard().cards.filter((item) => ["alpha", "beta"].includes(item.id)).every((item) => item.column_id === "review"), "Both selected workspaces moved");
    assert.equal(run.writes.length, 1, "One batch request moves both workspaces");
    assert.deepEqual(run.writes[0].body.changes.map((change) => change.id).sort(), ["alpha", "beta"]);
    await chooseColumn("review");
    await page.getByRole("region", { name: "In review", exact: true }).locator('[data-card-id="alpha"]').waitFor();
    await shot("moved");
    await undo();
    assert.deepEqual(getBoard().cards, initial, "Undo restores original columns and exact order");
    await chooseColumn("todo");
    await finish();
    await select("Alpha", "Beta");
    await page.getByRole("group", { name: "Selected workspaces", exact: true }).getByRole("button", { name: "Pin", exact: true }).click();
    await waitUntil(() => getBoard().cards.filter((item) => ["alpha", "beta"].includes(item.id)).every((item) => item.pinned), "Both selected workspaces pinned");
    await undo();
    assert.deepEqual(getBoard().cards, initial, "Undo restores pin state and order");
    await finish();

    await select("Alpha");
    await page.getByRole("textbox", { name: "Search workspaces", exact: true }).fill("Gamma");
    await page.getByText("0 selected", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Select all visible", exact: true }).click();
    await page.getByText("1 selected", { exact: true }).waitFor();
    const count = run.writes.length;
    await page.getByRole("group", { name: "Selected workspaces", exact: true }).getByRole("button", { name: "Archive", exact: true }).click();
    await waitUntil(() => getBoard().cards.find((item) => item.id === "gamma").archived, "Filtered workspace archived");
    assert.equal(run.writes.length, count + 1);
    assert.deepEqual(run.writes.at(-1).body.changes.map((change) => change.id), ["gamma"], "A hidden previously selected card is excluded");
    assert.equal(getBoard().cards.find((item) => item.id === "alpha").archived, false);
    await undo();
    await page.getByRole("textbox", { name: "Search workspaces", exact: true }).fill("");
    await finish();

    await select("Alpha", "Beta");
    await page.getByRole("group", { name: "Selected workspaces", exact: true }).getByRole("button", { name: "Archive", exact: true }).click();
    await waitUntil(() => getBoard().cards.filter((item) => ["alpha", "beta"].includes(item.id)).every((item) => item.archived), "Selected workspaces archived");
    await page.getByRole("button", { name: "Archived", exact: true }).click();
    await chooseColumn("todo");
    await page.locator('[data-card-id="alpha"]').waitFor();
    await shot("archived");
    await select("Alpha", "Beta");
    await page.getByRole("group", { name: "Selected workspaces", exact: true }).getByRole("button", { name: "Restore to board", exact: true }).click();
    await waitUntil(() => getBoard().cards.every((item) => !item.archived), "Selected workspaces restored");
    await page.getByRole("button", { name: "Back to board", exact: true }).click();
    await chooseColumn("todo");
    await page.locator('[data-card-id="alpha"]').waitFor();
    await shot("restored");
    assert.deepEqual(getBoard().cards, initial, "Archive and restore retain card identity and order");
    await finish();

    await select("Alpha");
    await page.getByRole("combobox", { name: "Filter by machine", exact: true }).selectOption(machines[1].id);
    await page.getByText("0 selected", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Select all visible", exact: true }).click();
    await page.getByText("1 selected", { exact: true }).waitFor();
    assert.equal(await page.getByRole("checkbox", { name: "Select Gamma workspace", exact: true }).isChecked(), true);
    await page.getByRole("combobox", { name: "Filter by machine", exact: true }).selectOption("");
    await finish();
    await page.getByRole("button", { name: "Auto", exact: true }).click();
    assert.equal(await page.getByRole("button", { name: "Select cards", exact: true }).count(), 0, "Automatic lanes have no bulk manual controls");
    await shot("auto");
    assert(run.hellos > 0);
    assert.deepEqual(run.blocked, []);
    assert.deepEqual(run.errors, []);
    result.checks.push(`${size}/${theme}: selection, atomic move, exact Undo, pin Undo, search/machine reset, select-visible, archive, restore, auto boundary`);
  } catch (error) {
    await page.screenshot({ path: join(output, `${size}-${theme}-failure.png`) }).catch(() => {});
    await writeFile(join(output, `${size}-${theme}-failure.txt`), await page.locator("body").innerText().catch(() => "Page unavailable"));
    throw error;
  } finally { await x.context.close(); }
}

async function refusedWrite(browser, status) {
  const x = await open(browser, "wide", "light");
  const { page, run, getBoard } = x;
  try {
    const initial = structuredClone(getBoard().cards);
    await x.select("Alpha", "Beta");
    x.rejectNext(status);
    const archive = page.getByRole("group", { name: "Selected workspaces", exact: true }).getByRole("button", { name: "Archive", exact: true });
    await archive.click();
    const message = status === 409 ? "The board was updated elsewhere and has been reloaded. Try again." : /HTTP 500/;
    await page.getByRole("alert").filter({ hasText: message }).waitFor();
    await page.getByText("2 selected", { exact: true }).waitFor();
    assert.deepEqual(getBoard().cards, initial, "A refused batch changes no card");
    assert.equal(await page.getByRole("checkbox", { name: "Select Alpha workspace", exact: true }).isChecked(), true, "Rollback preserves selection for retry");
    assert.equal(await page.getByRole("button", { name: "Undo last change", exact: true }).count(), 0, "A refused write creates no Undo entry");
    await x.shot(`refused-${status}`);
    await archive.click();
    await waitUntil(() => getBoard().cards.filter((item) => ["alpha", "beta"].includes(item.id)).every((item) => item.archived), "Retry succeeds on the current revision");
    assert.equal(run.writes.length, 2, "One failed batch and one explicit retry");
    await x.undo();
    assert.deepEqual(getBoard().cards, initial);
    assert.deepEqual(run.errors, []);
    result.checks.push(`HTTP ${status}: rollback, retained selection, explicit retry, current revision and Undo`);
  } catch (error) {
    await page.screenshot({ path: join(output, `refused-${status}-failure.png`) }).catch(() => {});
    await writeFile(join(output, `refused-${status}-failure.txt`), await page.locator("body").innerText().catch(() => "Page unavailable"));
    throw error;
  } finally { await x.context.close(); }
}

async function presentation(browser, language, textSize) {
  const x = await open(browser, "narrow", "dark", "normal", language, textSize);
  const { page, strings: s, run } = x;
  try {
    if (textSize === "large") assert.equal(await page.locator("html").getAttribute("data-text-size"), "large", "The app's real Large text setting is active");
    await x.shot("presentation-initial");
    await page.getByRole("button", { name: s.select_cards, exact: true }).click();
    const alpha = page.getByRole("checkbox", { name: s.select_card.replace("{card}", "Alpha workspace"), exact: true });
    assert.equal(await alpha.evaluate((el) => el === document.activeElement), true, "Entering selection focuses the first visible checkbox");
    await x.select("Alpha", "Beta");
    const count = page.getByText(s.selected_count.replace("{count}", "2"), { exact: true });
    await count.waitFor();
    const countGeometry = await count.evaluate((el) => ({ height: el.getBoundingClientRect().height, line: parseFloat(getComputedStyle(el).lineHeight) }));
    assert(countGeometry.height <= countGeometry.line + 1, "The selected count stays on one readable line in every language");
    const actions = page.getByRole("group", { name: s.selection_actions, exact: true });
    const bounds = await actions.getByRole("button").evaluateAll((els) => els.map((el) => {
      const rect = el.getBoundingClientRect();
      return { label: el.textContent, left: rect.left, right: rect.right, bottom: rect.bottom, width: innerWidth, height: innerHeight };
    }));
    assert(bounds.every((b) => b.left >= 0 && b.right <= b.width && b.bottom <= b.height), "Every bulk action fits in the narrow viewport");
    assert(!(await actions.innerText()).includes("workspaceboard."), "Localized controls never expose translation keys");
    await x.shot("presentation-selected");
    await actions.getByRole("button", { name: s.bulk_archive, exact: true }).click();
    await waitUntil(() => x.getBoard().cards.filter((item) => ["alpha", "beta"].includes(item.id)).every((item) => item.archived), "Localized Archive performs one batch");
    assert.equal(run.writes.length, 1);
    await x.undo();
    assert(x.getBoard().cards.every((item) => !item.archived));
    await x.finish();
    assert.equal(await page.getByTestId("board-select-cards").evaluate((el) => el === document.activeElement), true, "Leaving selection returns focus to its entry button");
    assert.deepEqual(run.errors, []);
    assert.deepEqual(run.blocked, []);
    result.checks.push(`narrow/dark/${language}/${textSize}: real preference, localized controls, focus handoff, viewport fit, Archive and Undo`);
  } catch (error) {
    await page.screenshot({ path: join(output, `${language}-${textSize}-failure.png`) }).catch(() => {});
    await writeFile(join(output, `${language}-${textSize}-failure.txt`), await page.locator("body").innerText().catch(() => "Page unavailable"));
    throw error;
  } finally { await x.context.close(); }
}

let browser;
try {
  browser = await chromium.launch({ ...(executable ? { executablePath: executable } : { channel: "chrome" }), headless: true });
  for (const theme of ["light", "dark"]) for (const size of ["wide", "narrow"]) await normal(browser, size, theme);
  for (const [language, textSize] of [["en", "large"], ["zh-Hans", "standard"], ["ja", "standard"]]) await presentation(browser, language, textSize);
  for (const status of [500, 409]) await refusedWrite(browser, status);
  for (const mode of ["legacy", "readonly"]) {
    const x = await open(browser, "wide", "light", mode);
    try {
      assert.equal(await x.page.getByRole("button", { name: "Select cards", exact: true }).count(), 0, `${mode} hides batch selection`);
      assert.equal(await x.page.getByRole("button", { name: "Undo last change", exact: true }).count(), 0);
      assert.equal(x.run.writes.length, 0);
      await x.shot("boundary");
      assert.deepEqual(x.run.errors, []);
      result.checks.push(`${mode}: readable board without batch writes`);
    } finally { await x.context.close(); }
  }
  result.status = "passed";
} catch (error) {
  result.status = "failed";
  result.failure = error.stack;
  process.exitCode = 1;
  const page = browser?.contexts().at(-1)?.pages().at(-1);
  if (page) {
    await page.screenshot({ path: join(output, "failure.png") }).catch(() => {});
    await writeFile(join(output, "failure.txt"), await page.locator("body").innerText().catch(() => "Page unavailable"));
  }
} finally {
  clearTimeout(deadline);
  await browser?.close();
  await writeFile(join(output, "results.json"), `${JSON.stringify(result, null, 2)}\n`);
}
console.log(`${result.status === "passed" ? "PASS" : "FAIL"} workflow-board bulk acceptance: ${result.checks.length} scenarios, ${result.screenshots.length} screenshots`);
if (result.failure) console.error(result.failure);
