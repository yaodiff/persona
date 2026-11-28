// Actual-app check with an isolated synthetic hub. Usage: node web/e2e/decision-pager.mjs <loopback origin> <shots dir>.
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { chromium } from "playwright-core";

const [address, output] = process.argv.slice(2);
const origin = new URL(address).origin;
assert.equal(new URL(origin).hostname, "127.0.0.1");
await mkdir(output, { recursive: true });

const now = new Date().toISOString();
const machine = { id: "example", name: "Computer A", os: "linux", online: true };
const session = (sid, title) => ({ machine_id: machine.id, session_id: sid, title, status: "needs_you", state: "needs_you",
  agent: "claude", updated_at: now, capabilities: { send: true, approve: true, interrupt: true } });
const main = { ...session("example-main", "Persona"), assistant: true };
const agent = session("example-agent", "Backup Agent");
const task = { id: "example-task", title: "Prepare a backup", created_at: now, updated_at: now, origin: main.session_id,
  agents: [{ machine_id: machine.id, session_id: agent.session_id, title: agent.title, linked_at: now }] };
const question = (id, sid, text, created_at) => ({ id, machine_id: machine.id, session_id: sid, kind: "question", tool_name: "ask_user", created_at,
  questions: [{ question: text, options: [{ label: "Use the defaults" }] }] });
const own = question("own-question", main.session_id, "Where should these backups go?", "2026-01-02T00:00:00Z");
const ask = { ...question("task-question", agent.session_id, "How long should each backup be kept?", "2026-01-01T00:00:00Z"),
  tool_use_id: "tool-backup", can_cancel: true, folded_under: main.session_id };
const plan = { id: "task-plan", kind: "plan", machine_id: machine.id, session_id: agent.session_id, created_at: "2026-01-03T00:00:00Z",
  plan: "Save an encrypted backup every Sunday." };
const agentItems = [
  { id: "backup-input", seq: 1, role: "user", kind: "text", text: "Prepare a backup.", ts: now },
  { id: "backup-explanation", seq: 2, role: "assistant", kind: "text", text: "The backup duration affects available disk space.", ts: now },
  { id: "backup-call", seq: 3, role: "assistant", kind: "tool_use", tool_use_id: "tool-backup", tool_name: "ask_user", ts: now },
];

const browser = await chromium.launch({ channel: "chrome" });
const receipts = [];
try {
  for (const [theme, width, height] of [["light", 1280, 800], ["dark", 390, 790]]) {
    const mobile = width < 500;
    let interactions = [plan, own, ask];
    const errors = [];
    const answers = [];
    const cancellations = [];
    const taskAgent = mobile ? { ...agent, capabilities: { ...agent.capabilities, approve: false } } : agent;
    let updateInteraction;
    const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme, locale: "en-US",
      reducedMotion: "reduce", isMobile: mobile, hasTouch: mobile });
    await context.addInitScript((mode) => {
      localStorage.setItem("persona.token", "example");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", mode);
    }, theme);
    await context.route("**/*", (route) => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    await context.route("**/api/v1/**", (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname.replace("/api/v1", "");
      if (request.method() !== "GET") {
        if (path === `/interactions/${own.id}/resolve` && request.method() === "POST") {
          answers.push(request.postDataJSON());
          interactions = interactions.filter(({ id }) => id !== own.id);
          return route.fulfill({ status: 200, json: {} });
        }
        if (path === `/interactions/${ask.id}/resolve` && request.method() === "POST") {
          cancellations.push(request.postDataJSON());
          if (cancellations.length === 1) return route.fulfill({ status: 400, json: { error: "Try the question again" } });
          interactions = interactions.filter(({ id }) => id !== ask.id);
          return route.fulfill({ status: 200, json: {} });
        }
        return route.fulfill({ status: 409, json: {} });
      }
      const data = path === "/machines" ? { machines: [machine] }
        : path === "/sessions" ? { sessions: [main, taskAgent] }
        : path === "/interactions" ? { interactions }
        : path === "/assistant" ? { machine_id: machine.id, main: main.session_id, conversations: [main], tasks: [], memory: [] }
        : path.endsWith("/feed") ? { items: [], plans: {}, task_cards: [] }
        : path === "/todos" ? { todos: [task] }
        : path.endsWith("/items") ? { items: path.includes(agent.session_id) ? agentItems : [], has_more: false }
        : /^\/sessions\/[^/]+\/[^/]+$/.test(path) ? { session: path.endsWith(agent.session_id) ? taskAgent : main }
        : {};
      return route.fulfill({ status: 200, json: data });
    });
    await context.routeWebSocket("**/api/v1/ws**", (ws) => {
      updateInteraction = (interaction) => ws.send(JSON.stringify({ type: "interaction", interaction }));
      ws.send(JSON.stringify({ type: "hello", server_version: "example", hub_name: "Example hub", features: [] }));
      ws.onMessage((message) => { if (String(message).includes("app_ping")) ws.send(JSON.stringify({ type: "pong" })); });
    });
    const page = await context.newPage();
    page.on("pageerror", (err) => errors.push(String(err)));
    await page.goto(origin);
    const pager = page.locator("[data-decision-pager]");
    const entry = page.locator("[data-decision-entry]");
    const card = (id) => pager.locator(`[data-card="${id}"]`);
    await card(own.id).waitFor();
    await page.getByText("1 / 3", { exact: true }).waitFor();
    assert.equal(await pager.locator("[data-card]").count(), 1);
    assert.equal(await card(own.id).getByRole("button", { name: "Skip", exact: true }).count(), 0);
    assert.equal(await page.locator(`[data-card="${ask.id}"]`).count(), 0);
    assert.notEqual(await entry.locator("..").locator("button").first().getAttribute("data-decision-entry"), null);
    await page.screenshot({ path: `${output}/${theme}-first.png`, animations: "disabled" });

    const draft = card(own.id).locator("input").first();
    await draft.fill("Keep the off-site copy");
    const needsYou = page.locator('[data-tray-entry="needs"]');
    await needsYou.click();
    await page.locator("[data-tray-list]").waitFor();
    await pager.waitFor({ state: "hidden" });
    await needsYou.click();
    await card(own.id).waitFor();
    assert.equal(await draft.inputValue(), "Keep the off-site copy");
    if (mobile) {
      await pager.evaluate((target) => {
        const touch = (clientX) => new Touch({ identifier: 1, target, clientX, clientY: 400 });
        target.dispatchEvent(new TouchEvent("touchstart", { touches: [touch(270)], bubbles: true }));
        target.dispatchEvent(new TouchEvent("touchend", { changedTouches: [touch(130)], bubbles: true }));
      });
    } else {
      await pager.getByRole("button", { name: "Next card" }).click();
    }
    await card(ask.id).waitFor();
    assert.equal(await page.locator(`[data-card="${ask.id}"]`).count(), 1);
    assert.equal(await card(ask.id).locator("input").first().isDisabled(), mobile);
    await pager.getByRole("button", { name: "View in Chat" }).click();
    const source = page.locator("[data-decision-source-chat]");
    await source.locator("[data-search-hit]").filter({ hasText: "The backup duration affects available disk space." }).waitFor();
    await page.screenshot({ path: `${output}/${theme}-source.png`, animations: "disabled" });
    await page.goBack({ waitUntil: "commit" });
    await source.waitFor({ state: "hidden" });
    await card(ask.id).waitFor();
    await pager.getByRole("button", { name: "Next card" }).click();
    await card(plan.id).waitFor();
    assert.equal(await pager.getByRole("button", { name: "Skip", exact: true }).isDisabled(), true);
    await pager.getByText("Send the plan back to stop it from going ahead.").waitFor();
    await pager.getByRole("button", { name: "Previous card" }).click();
    await pager.getByRole("button", { name: "Previous card" }).click();
    assert.equal(await draft.inputValue(), "Keep the off-site copy");
    await pager.getByRole("button", { name: "Collapse" }).click();
    await pager.waitFor({ state: "hidden" });
    await entry.click();
    assert.equal(await draft.inputValue(), "Keep the off-site copy");
    await card(own.id).locator("[data-card-actions] button").last().click();
    await card(ask.id).waitFor();
    await pager.locator("[data-decision-position]").getByText("1 / 2", { exact: true }).waitFor();
    assert.equal(answers.length, 1);
    if (!mobile) {
      const taskDraft = card(ask.id).locator("input").first();
      await taskDraft.fill("A draft for the previous question");
      const revised = { ...ask, questions: [{ ...ask.questions[0], question: "How many backup copies should be kept?" }] };
      interactions = interactions.map((interaction) => interaction.id === ask.id ? revised : interaction);
      updateInteraction(revised);
      await card(ask.id).getByText(revised.questions[0].question, { exact: true }).waitFor();
      assert.equal(await taskDraft.inputValue(), "");
      await taskDraft.fill("Keep seven copies");
      await card(ask.id).getByRole("button", { name: "Skip", exact: true })
        .and(card(ask.id).locator("button:not([data-tap-guard])")).click();
      await card(ask.id).getByRole("alert").waitFor();
      assert.equal(await taskDraft.inputValue(), "Keep seven copies");
      await card(ask.id).getByRole("button", { name: "Retry", exact: true }).click();
      await card(plan.id).waitFor();
      assert.deepEqual(cancellations, [{ cancel: true }, { cancel: true }]);
      await page.getByText("Question closed without an answer", { exact: true }).first().waitFor({ state: "attached" });
    } else {
      assert.deepEqual(cancellations, []);
    }
    assert.deepEqual(errors, []);
    receipts.push({ theme, first: own.id, task: ask.id, source: "backup-explanation", afterAnswer: ask.id, readOnlyTask: mobile, cancellations });
    await context.close();
  }
} finally {
  await browser.close();
}
await writeFile(`${output}/results.json`, JSON.stringify(receipts, null, 2) + "\n");
console.log("Decision pager checks passed in both appearances");
