// A decision answered on its Needs you row (#3804): the real web app with synthetic HTTP/WS only; it never connects to a
// user's hub or conversation. Two answerable decisions and a legacy note-only review wait.
// node web/e2e/decision-row.mjs <web URL> --shots <directory> [--large] [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
const base = args[0]?.replace(/\/$/, "");
const shots = flag("--shots");
const textSize = args.includes("--large") ? "large" : "standard";
if (!base || !shots) {
  console.error("usage: node web/e2e/decision-row.mjs <web URL> --shots <directory> [--chromium <executable>]");
  process.exit(2);
}
const origin = new URL(base);
assert(["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname) && origin.protocol === "http:", "only an owned loopback HTTP web server is allowed");
assert(!origin.username && !origin.password && !origin.search && !origin.hash, "the web URL must not contain credentials");
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => { console.error("TIMEOUT decision-row browser verification (180s)"); process.exit(124); }, 180_000);
const AT = "2026-11-10T08:00:00Z";
const sid = "decision-row-demo";
const direction = "direction-demo";
const askLabel = { en: "Ask in main chat", "zh-Hans": "在主对话问", ja: "メインチャットで聞く" };
const discussionContext = { en: "The owner is asking about this pending decision.", "zh-Hans": "用户在询问这项待定决策。" };
const composerInput = "[data-composer-input]";
const RESPONSES = ["decide_yourself", "not_what_i_asked", "explain_first"];
const menuWords = {
  en: ["You decide", "This isn't what I asked for", "Tell me the background first"],
  "zh-Hans": ["你来定", "这不是我要的", "先告诉我背景"],
  ja: ["任せます", "頼んだことと違います", "先に背景を教えて"],
};

/** The Needs you rows are folded to a line until opened (#5569): open every one that still is. */
async function openFolded(page) {
  const folded = page.locator('button[data-needs-row][aria-expanded="false"]');
  while (await folded.count() > 0) await folded.first().click();
}

function todos() {
  // a deadline later today in the browser's zone, so the row says a time of day
  const deadline = new Date();
  deadline.setHours(23, 30, 0, 0);
  const task = (id, title, ask) => ({ id, title, origin: sid, created_at: AT, updated_at: AT, agents: [{ machine_id: "demo", session_id: `${id}-worker`, linked_at: AT }],
    supervision: { supervisor: sid, state: "needs_user", reviews: [{ at: AT, verdict: "needs_user", note: "Example extension checks passed; the rollout still needs your decision." }] }, decision_review: { review_at: AT }, ask: { outcome: "decide", asked_at: AT, responses: RESPONSES, ...ask } });
  return [
    task("demo-rollout", "Example rollout", {
      verdict: "Needs your decision: roll out to everyone now, or to 10% first",
      recommend: "10% first",
      link: "https://github.com/nwverse/persona/pull/14659",
      question: "Roll the example change out to everyone now, or to 10% first?",
      why_owner: "priority",
      options: [
        { label: "10% first", leads_to: "one more day; a bad build reaches few people" },
        { label: "Everyone now", leads_to: "ships today; a bad build reaches everyone" },
        { label: "Hold the rollout", leads_to: "nothing ships this week" },
      ],
      cost_of_waiting: "the rollout slips by a day",
      background: "From the main conversation on Nov 10: \"roll out the example change\" · merged and checked in staging, not released",
      deadline: deadline.toISOString(),
      default: "taken",
    }),
    task("demo-cleanup", "Example data cleanup", {
      verdict: "Needs your decision: delete the example records older than a year",
      recommend: "Delete after an export",
      link: "https://gitlab.example.com/team/app/-/merge_requests/1468",
      question: "Delete the example records older than a year?",
      why_owner: "irreversible",
      options: [
        { label: "Delete after an export", leads_to: "an export is kept for 30 days, then the records are gone" },
        { label: "Keep everything", leads_to: "storage keeps growing; nothing is lost" },
      ],
      cost_of_waiting: "none this week",
      background: "Started by the assistant on Nov 6 · the export script is written and tested on a copy",
      default: "never",
    }),
    task("demo-custom", "Example custom decision", {
      verdict: "Needs your decision: resume the report now, or change its reminder",
      recommend: "Resume now",
      link: "https://hub.example.com/todos/demo-custom",
      question: "Resume the report now, or change its reminder?",
      why_owner: "priority",
      options: [
        { label: "Resume now", leads_to: "the report starts immediately" },
        { label: "Change reminder", leads_to: "the owner chooses a new day" },
      ],
      cost_of_waiting: "the report stays paused",
      background: "From the main conversation on Nov 10 · the owner wants to add a condition in words",
      default: "never",
    }),
    { ...task("demo-legacy", "Example extension rollout", {}), ask: undefined },
  ];
}

async function run(browser, size, theme, language) {
  const context = await browser.newContext({ viewport: size === "wide" ? { width: 1280, height: 900 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce" });
  await context.addInitScript(({ theme, language, textSize }) => {
    localStorage.setItem("persona.token", "synthetic-demo-token");
    localStorage.setItem("persona.language", language);
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.text-size", textSize);
  }, { theme, language, textSize });
  const session = { machine_id: "demo", session_id: sid, title: "Example conversation", assistant: true, state: "idle", status: "idle", updated_at: AT };
  const lead = { ...session, session_id: direction, title: "Example direction" };
  let list = todos();
  list[0] = { ...list[0], quiet: true, supervision: { ...list[0].supervision, supervisor: direction },
    view: { phase: "needs_you", section: "needs_you", reason: "owner_decides", rank: 0 } };
  const agents = list.map((t) => ({ machine_id: "demo", session_id: `${t.id}-worker`, title: `${t.title} Agent`, todo: { id: t.id, title: t.title }, state: "idle", status: "idle", updated_at: AT, state_since: AT, ...(t.id === "demo-legacy" ? { turn_error: "Synthetic transient runtime error", turn_error_kind: "transient" } : {}) }));
  const sessions = [session, lead, ...agents];
  let dismissalGate;
  const writes = [];
  await context.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api\/v1/, "");
    if (path === `/assistant/conversations/${sid}/today`) {
      await route.fulfill({ contentType: "application/json", body: "{}" });
      return;
    }
    if (request.method() !== "GET") {
      const body = request.postDataJSON();
      writes.push({ path, method: request.method(), body });
      if (path === `/sessions/demo/${sid}/messages`) {
        await route.fulfill({ contentType: "application/json", body: JSON.stringify({ ok: true }) });
        return;
      }
      const dismissal = /^\/todos\/([^/]+)\/decision-(dismiss|undo-dismiss)$/.exec(path);
      if (dismissal) {
        assert.equal(body.review_at, AT);
        if (dismissal[2] === "dismiss" && dismissalGate) await dismissalGate;
        const todo = list.find((t) => t.id === dismissal[1]);
        assert(todo && !todo.done_at);
        if (dismissal[2] === "undo-dismiss") assert.equal(body.dismissal_id, "demo-dismissal");
        todo.decision_review = { review_at: AT, ...(dismissal[2] === "dismiss" ? { dismissal_id: "demo-dismissal" } : {}) };
        await route.fulfill({ contentType: "application/json", body: JSON.stringify(todo.decision_review) });
        return;
      }
      if (path === `/assistant/conversations/${sid}/route`) {
        assert.equal(body.todo_id, "demo-custom");
        assert.equal(body.text, "先 Resume，但把提醒改到周四");
        assert.equal(body.asked_at, AT);
        list = list.map((t) => t.id === "demo-custom" ? { ...t, ask: undefined, supervision: { ...t.supervision, state: "" } } : t);
        await route.fulfill({ contentType: "application/json", body: JSON.stringify({ item: { id: "custom-answer", conversation: sid, at: AT, kind: "routed", ref: "demo-custom", body: { text: body.text } } }) });
        return;
      }
      const m = /^\/todos\/([^/]+)\/accept$/.exec(path);
      assert(m && request.method() === "POST", `only decision routes are written: ${request.method()} ${path}`);
      list = list.map((t) => (t.id === m[1] ? { ...t, ask: undefined, supervision: { ...t.supervision, state: "" } } : t));
      agents.find((a) => a.session_id === `${m[1]}-worker`).turn_error = undefined;
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ ok: true }) });
      return;
    }
    let body = {};
    if (path === "/machines") body = { machines: [{ id: "demo", name: "Example computer", online: true, os: "linux" }] };
    else if (path === "/sessions") body = { sessions };
    else if (path === "/interactions") body = { interactions: [] };
    else if (path === "/assistant") body = { machine_id: "demo", main: sid, conversations: [session, lead], tasks: [], memory: [] };
    else if (path === "/todos") body = { todos: list };
    else if (path.startsWith("/sessions/demo/") && path.split("/").length === 4) body = { session: sessions.find((s) => s.session_id === path.split("/").at(-1)) };
    else if (path.endsWith("/feed")) body = { items: [] };
    else if (path.endsWith("/items")) body = { items: [], has_more: false };
    else if (path === "/extensions/ui") body = { slots: [] };
    else if (path === "/extensions") body = { extensions: [] };
    else if (path === "/managers") body = { managers: [] };
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: [] }));
    ws.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      if (message.type === "app_ping") ws.send(JSON.stringify({ type: "pong" }));
    });
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(String(error)));
  page.setDefaultTimeout(12_000);
  const name = (step) => join(shots, `decision-${size}-${theme}-${language}-${step}${textSize === "large" ? "-large" : ""}.png`);
  try {
    await page.goto(`${base}/`);
    await page.locator('[data-tray-entry="needs"]').click();
    await page.locator('button[data-needs-row]').first().waitFor();
    await openFolded(page);
    const rows = page.locator("[data-decision-row]");
    await rows.first().waitFor();
    assert.equal(await rows.count(), 3);
    const rollout = page.locator('[data-needs-row="task:demo-rollout"]');
    const cleanup = page.locator('[data-needs-row="task:demo-cleanup"]');
    const custom = page.locator('[data-needs-row="task:demo-custom"]');
    assert.equal(await rollout.locator('[data-decision-default="taken"]').count(), 1, "the reversible decision says when its default is taken");
    assert.match(await rollout.locator('[data-decision-default="taken"]').innerText(), /23:30/);
    assert.equal(await cleanup.locator('[data-decision-default="never"]').count(), 1, "the irreversible one says it is never taken by default");
    const rolloutLink = rollout.locator('[data-decision-link="external"]');
    assert.equal(await rolloutLink.innerText(), "PR #14659");
    assert.equal(await rolloutLink.getAttribute("href"), "https://github.com/nwverse/persona/pull/14659");
    assert.equal(await rolloutLink.getAttribute("target"), "_blank");
    assert.equal(await rolloutLink.getAttribute("rel"), "noopener noreferrer");
    const cleanupLink = cleanup.locator('[data-decision-link="external"]');
    assert.equal(await cleanupLink.innerText(), "MR !1468");
    assert.equal(await cleanupLink.getAttribute("href"), "https://gitlab.example.com/team/app/-/merge_requests/1468");
    assert.equal(await custom.locator("[data-decision-link]").count(), 0, "redundant self-link is not rendered");
    for (const row of [rollout, cleanup]) {
      assert.equal(await row.locator("[data-decision-actions] button").count(), 3, "Later, Don't handle, and Ask in main chat");
      const buttons = row.locator("div.overflow-hidden > button, div.overflow-hidden > span > button");
      const geometry = await buttons.evaluateAll((els) => els.map((el) => ({ top: Math.round(el.getBoundingClientRect().top + el.getBoundingClientRect().height / 2), height: el.clientHeight, scroll: el.scrollHeight })));
      assert(geometry.every((g) => g.top === geometry[0].top), `the buttons are on one line: ${JSON.stringify(geometry)}`);
      assert(geometry.every((g) => g.scroll <= g.height + 1), `no button's text wraps: ${JSON.stringify(geometry)}`);
      const shown = await row.locator("[data-decision-option]").count();
      const primary = await row.locator("[data-decision-option]").first().evaluate((el) => el.closest("button").className);
      assert(primary.includes("bg-ink"), "the recommended option is the solid button");
      assert(shown >= 1);
      const separate = await row.evaluate((el) => {
        const footer = el.querySelector("[data-decision-actions]").getBoundingClientRect();
        return [...el.querySelectorAll("[data-decision-option]")].every((label) => label.closest("button").getBoundingClientRect().bottom <= footer.top + 1);
      });
      assert(separate, "deferral controls never cover the decision option buttons");
    }
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "no horizontal page overflow");
    await rollout.scrollIntoViewIfNeeded(); // keep this decision's whole action group in the evidence capture
    await page.waitForTimeout(300); // the entry has turned to its selected colors
    await page.screenshot({ path: name("tray") });
    const exerciseDiscussion = (size === "wide" && theme === "dark" && language === "zh-Hans") ||
      (size === "narrow" && theme === "light" && language === "en");
    const sources = exerciseDiscussion ? [["main", `/persona/${sid}`], ["direction", `/persona/${direction}`], ["agent", "/s/demo/demo-rollout-worker"]] : [];
    for (const [source, path] of sources) {
      await page.goto(base + path);
      if (source !== "agent") {
        await page.locator('[data-tray-entry="needs"]').click();
        await rollout.waitFor();
        await openFolded(page);
      }
      const input = page.locator(composerInput);
      if (source === "main") await input.fill("Existing draft");
      const askButton = rollout.getByRole("button", { name: askLabel[language], exact: true });
      await askButton.click();
      await page.waitForURL(base + `/persona/${sid}`);
      const draftQuote = page.locator("[data-quotes]").filter({ hasText: "Example rollout" });
      await draftQuote.waitFor();
      assert.equal(await page.getByRole("dialog").count(), 0, "the composer opens without a dialog");
      assert.equal(writes.length, 0, "opening the composer only adds a draft quote");
      if (source === "main") {
        assert.equal(await input.textContent(), "Existing draft");
        await draftQuote.getByRole("button").click();
        await draftQuote.waitFor({ state: "detached" });
        assert.equal(await input.textContent(), "Existing draft");
        await page.locator('[data-tray-entry="needs"]').click();
        await rollout.waitFor();
        await openFolded(page);
        await askButton.click();
        await draftQuote.waitFor();
      }
      const question = `What would change for existing users? (${source})`;
      await input.fill(question);
      if (source === "main") await page.screenshot({ path: name("ask-composer") });
      const response = page.waitForResponse((res) => new URL(res.url()).pathname === `/api/v1/sessions/demo/${sid}/messages`);
      await page.getByTestId("composer-send").click();
      assert.equal((await response).status(), 200);
      await draftQuote.waitFor({ state: "detached" });
      assert.equal(new URL(page.url()).pathname, `/persona/${sid}`);
      const write = writes.at(-1);
      assert.equal(writes.length, 1, "the chat send is the only write");
      assert.equal(write.path, `/sessions/demo/${sid}/messages`);
      assert.match(write.body.text, /^> 任务「Example rollout」（todo id: demo-rollout）/);
      const decision = list.find((todo) => todo.id === "demo-rollout").ask;
      assert(write.body.text.includes(decision.question));
      for (const option of decision.options) assert(write.body.text.includes(`${option.label}: ${option.leads_to}`));
      assert(write.body.text.includes(language === "en" ? `Recommended: ${decision.recommend}` : `推荐：${decision.recommend}`));
      assert(write.body.text.includes(discussionContext[language]));
      assert(write.body.text.endsWith(`\n\n${question}`));
      const message = page.locator("[data-bubble-text]").filter({ hasText: question });
      await message.waitFor();
      const quote = message.locator("..").locator("[data-quote]").filter({ hasText: "Example rollout" });
      await quote.waitFor();
      if (source === "main") {
        const expand = quote.locator('button[aria-expanded="false"]');
        if (await expand.count()) await expand.click();
        await page.screenshot({ path: name("ask-main-chat") });
      }
      writes.length = 0;
      await page.goto(base + path);
      if (source !== "agent") {
        await page.locator('[data-tray-entry="needs"]').click();
        await rollout.waitFor();
        await openFolded(page);
      }
      await rollout.locator("[data-decision-option]").first().waitFor();
      assert.equal(decision.asked_at, AT);
    }
    if (exerciseDiscussion) {
      await page.goto(`${base}/`);
      await page.locator('[data-tray-entry="needs"]').click();
      await openFolded(page);
    }
    // Other… opens the originating conversation with this decision routed to its composer, so the owner can add a
    // condition in words instead of choosing an unchanged suggestion (#10760).
    await custom.locator("span[data-decision-tail] button").first().click();
    await page.locator(composerInput).waitFor();
    assert.equal(new URL(page.url()).pathname, `/persona/${sid}`);
    await page.getByText("Example custom decision", { exact: false }).first().waitFor();
    await page.locator(composerInput).fill("先 Resume，但把提醒改到周四");
    await page.screenshot({ path: name("custom-answer") });
    const customAnswer = page.waitForResponse((res) => new URL(res.url()).pathname === `/api/v1/assistant/conversations/${sid}/route`);
    await page.getByTestId("composer-send").click();
    await customAnswer;
    await page.goto(`${base}/`);
    await page.locator('[data-tray-entry="needs"]').click();
    await openFolded(page);
    assert.equal(await page.locator('[data-needs-row="task:demo-custom"]').count(), 0, "the written owner answer settles its decision");
    assert.deepEqual(writes, [{ path: `/assistant/conversations/${sid}/route`, method: "POST", body: { todo_id: "demo-custom", text: "先 Resume，但把提醒改到周四", asked_at: AT } }]);
    writes.length = 0;
    // the menu: the options that did not fit, then the three answers that are no option
    const shownOptions = await rollout.locator("[data-decision-option]").count();
    await rollout.locator("span[data-decision-tail] button[aria-label]").last().click();
    const items = page.locator("[role=menuitem]");
    await items.first().waitFor();
    const labels = await items.evaluateAll((els) => els.map((el) => el.querySelector("[data-menu-label]")?.textContent ?? el.textContent));
    assert.equal(labels.length, 3 - shownOptions + 3, `the menu holds the overflowed options and the three responses: ${JSON.stringify(labels)}`);
    menuWords[language].forEach((word, i) => assert(labels[3 - shownOptions + i].startsWith(word), `${labels[3 - shownOptions + i]} starts with ${word}`));
    await page.waitForTimeout(300); // the menu has faded in
    await page.screenshot({ path: name("menu") });
    await items.nth(3 - shownOptions + 1).click(); // "This isn't what I asked for"
    await rollout.waitFor({ state: "detached" }); // answered: the hub lists it without its ask, and the row goes
    assert.deepEqual(writes, [{ path: "/todos/demo-rollout/accept", method: "POST", body: { asked_at: AT, response: "not_what_i_asked" } }]);
    // an option on the other row
    await cleanup.locator("[data-decision-option]").nth(0).click();
    assert.equal(writes.length, 1, "irreversible options require confirmation");
    await cleanup.locator("[data-decision-option]").nth(0).click();
    await cleanup.waitFor({ state: "detached" });
    assert.deepEqual(writes[1], { path: "/todos/demo-cleanup/accept", method: "POST", body: { asked_at: AT, option: "Delete after an export" } });
    const legacy = page.locator('[data-needs-row="task:demo-legacy"]');
    await openFolded(page);
    await legacy.locator("[data-decision-actions]").scrollIntoViewIfNeeded();
    assert.equal(await legacy.locator("[data-decision-option]").count(), 0, "no invented options for a review without an ask");
    assert.match(await legacy.locator("[data-decision-legacy]").innerText(), /Example extension checks passed/);
    await page.screenshot({ path: name("legacy") });
    await legacy.locator("[data-decision-actions] button").nth(0).click();
    await legacy.waitFor({ state: "detached" });
    assert.equal(writes.length, 2, "Later writes nothing");
    await page.reload();
    await page.locator('[data-tray-entry="needs"]').click();
    await legacy.waitFor();
    await openFolded(page);
    await legacy.locator("[data-decision-actions]").scrollIntoViewIfNeeded();
    await page.screenshot({ path: name("dismiss-before") });
    let releaseDismiss;
    dismissalGate = new Promise((resolve) => { releaseDismiss = resolve; });
    await legacy.locator("[data-decision-actions] button").nth(1).click();
    await legacy.waitFor({ state: "detached" });
    assert(!list.find((t) => t.id === "demo-legacy").decision_review.dismissal_id, "the row disappears before the server confirms");
    await page.screenshot({ path: name("dismiss-pending") });
    releaseDismiss();
    dismissalGate = undefined;
    assert.deepEqual(writes[2], { path: "/todos/demo-legacy/decision-dismiss", method: "POST", body: { review_at: AT } });
    const undo = page.locator('[aria-live="polite"] button').filter({ hasText: /Undo|撤销|元に戻す/ });
    await undo.waitFor();
    await page.screenshot({ path: name("dismiss-confirmed") });
    await undo.click();
    await legacy.waitFor();
    assert.deepEqual(writes[3], { path: "/todos/demo-legacy/decision-undo-dismiss", method: "POST", body: { review_at: AT, dismissal_id: "demo-dismissal" } });
    await legacy.locator("[data-decision-actions] button").nth(1).click();
    await legacy.waitFor({ state: "detached" });
    await undo.waitFor();
    await page.reload();
    await page.locator('[data-tray-entry="needs"]').click();
    assert.equal(await legacy.count(), 0, "dismissal survives a read and reload without completing the task");
    assert(!list.find((t) => t.id === "demo-legacy").done_at);
    await page.screenshot({ path: name("refreshed") });
    const other = await context.newPage();
    await other.goto(`${base}/`);
    await other.locator('[data-tray-entry="needs"]').click();
    assert.equal(await other.locator('[data-needs-row="task:demo-legacy"]').count(), 0, "a fresh tab also honors the persisted dismissal");
    await other.screenshot({ path: name("second-tab") });
    await other.close();
    list = list.map((t) => t.id === "demo-rollout" ? todos()[0] : t);
    agents.find((a) => a.session_id === "demo-rollout-worker").turn_error = "Synthetic transient runtime error";
    await page.reload();
    await page.locator('[data-tray-entry="needs"]').click();
    await rollout.waitFor();
    await openFolded(page);
    await rollout.locator("[data-decision-actions]").scrollIntoViewIfNeeded();
    await page.screenshot({ path: name("structured-before") });
    dismissalGate = new Promise((resolve) => { releaseDismiss = resolve; });
    await rollout.locator("[data-decision-actions] button").nth(1).click();
    await rollout.waitFor({ state: "detached" });
    assert(!list.find((t) => t.id === "demo-rollout").decision_review.dismissal_id);
    await page.screenshot({ path: name("structured-pending") });
    releaseDismiss();
    dismissalGate = undefined;
    await undo.waitFor();
    await page.reload();
    await page.locator('[data-tray-entry="needs"]').click();
    assert.equal(await rollout.count(), 0, "structured decision dismissal survives reload despite a runtime error");
    await page.screenshot({ path: name("structured-refreshed") });
    assert.deepEqual(errors, []);
    console.log(`PASS ${size} ${theme} ${language}: rows=2, option buttons on the first row=${shownOptions}, menu=${labels.length}, POST=${writes.length}`);
  } catch (error) {
    await page.screenshot({ path: name("failed") }).catch(() => {});
    throw error;
  } finally {
    await context.close();
  }
}

let browser;
try {
  browser = await chromium.launch({ executablePath: flag("--chromium") || "/usr/bin/google-chrome", headless: true });
  for (const theme of ["light", "dark"]) {
    await run(browser, "wide", theme, "en");
    await run(browser, "narrow", theme, "en");
    await run(browser, "wide", theme, "zh-Hans");
  }
  for (const language of ["zh-Hans", "ja"]) for (const theme of ["light", "dark"]) await run(browser, "narrow", theme, language);
  console.log("PASS decision-row browser verification");
} catch (error) {
  console.error("FAIL decision-row browser verification", error);
  process.exitCode = 1;
} finally {
  clearTimeout(watchdog);
  await browser?.close();
}
