// Actual-app check: an Agent's question card and tool approval in the session page's dock at once, the approval cut off
// below the dock's box (its 55% cap, #5375). The approval's answers stay in view at the box's bottom edge, so the row
// must say what they answer (the card's title and its command) and the stack how many cards it holds (audit item 10).
// Usage: node web/e2e/pending-cards-cut.mjs <web-origin> <artifactDir> [--chromium <executable>]
// Every hub request and the socket are answered in the browser with invented data; nothing reaches a hub and no card is
// answered. Checks at 1440 × 900 and 1280 × 820 (the approval's answer row stuck at the box's bottom edge) and 1024 × 700
// (the question alone fills the box, the approval wholly below it), light and dark: wherever the approval's Allow is in
// view, its title and command are in view too, either in the card or on the answer row's line; "2 pending" shows on the
// first card while the stack is cut off; where the line shows, it brings the whole approval into view with the focus on
// it. At 1440 × 1600, where both cards fit, neither the line nor the count is drawn.
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const chromiumAt = args.includes("--chromium") ? args.splice(args.indexOf("--chromium"), 2)[1] : undefined;
const [address, artifacts, ...extra] = args;
assert.ok(address && artifacts && !extra.length, "Supply the web origin and artifact directory");
const base = new URL(address);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback web server");
const output = resolve(artifacts);
await mkdir(output, { recursive: true });

const AT = "2026-01-01T10:00:00Z";
const MACHINE = { id: "example", name: "Computer A", online: true, os: "linux" };
const SID = "example-agent";
const SESSION = { machine_id: MACHINE.id, session_id: SID, title: "Add unit tests for the network layer", state: "needs_you", status: "blocked", cwd: "/example/app", updated_at: AT, agent: "claude", capabilities: { send: true, interrupt: true, approve: true } };
const ITEMS = [
  { id: "u1", seq: 1, role: "user", kind: "text", text: "Add unit tests for the network layer, retries and timeouts included.", ts: AT },
  { id: "a1", seq: 2, role: "assistant", kind: "text", text: "Before I write them: which test framework should I use?", ts: AT },
];
const QUESTION = {
  id: "example-question", machine_id: MACHINE.id, session_id: SID, kind: "question", agent: "claude", tool_name: "AskUserQuestion", created_at: AT,
  questions: [{ question: "Which framework should the network tests use?", header: "Framework", options: [
    { label: "Swift Testing", description: "New code uses @Test; parameterized tests stay short" },
    { label: "XCTest", description: "Matches the existing UI tests" },
  ] }],
};
const COMMAND = "swift test --filter NetworkTests";
const APPROVAL = { id: "example-approval", machine_id: MACHINE.id, session_id: SID, kind: "permission", agent: "claude", tool_name: "Bash", created_at: AT, summary: COMMAND, tool_input: { command: COMMAND, description: "Run the network tests" } };

const result = { status: "running", runs: [] };
const browser = await chromium.launch(chromiumAt ? { executablePath: chromiumAt, args: ["--no-sandbox"] } : { channel: "chrome" });
const deadline = setTimeout(() => { result.status = "timeout"; void browser.close(); }, 240_000);
try {
  for (const [width, height] of [[1440, 900], [1280, 820], [1024, 700], [1440, 1600]]) for (const theme of ["light", "dark"]) {
    const name = `${width}x${height}-${theme}`;
    const context = await browser.newContext({ viewport: { width, height }, locale: "en-US", colorScheme: theme, reducedMotion: "reduce" });
    await context.addInitScript(`localStorage.setItem('persona.token','example');localStorage.setItem('persona.language','en');localStorage.setItem('persona.theme','${theme}');`);
    await context.route(/^https?:\/\/(?!127\.0\.0\.1|localhost|\[::1\])/, (route) => route.abort());
    await context.route("**/api/v1/**", (route) => {
      if (route.request().method() !== "GET") return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
      const path = new URL(route.request().url()).pathname.replace("/api/v1", "");
      const data = path === "/machines" ? { machines: [MACHINE] }
        : path === "/sessions" ? { sessions: [SESSION] }
        : path === "/interactions" ? { interactions: [QUESTION, APPROVAL] }
        : path === "/todos" ? { todos: [] }
        : path.endsWith("/items") ? { items: ITEMS, has_more: false }
        : /^\/sessions\/[^/]+\/[^/]+$/.test(path) ? { session: SESSION }
        : {};
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(data) });
    });
    await context.routeWebSocket("**/api/v1/ws**", (ws) => {
      ws.send(JSON.stringify({ type: "hello", server_version: "example", hub_name: "Example hub", features: [] }));
      ws.onMessage((m) => { if (String(m).includes("app_ping")) ws.send(JSON.stringify({ type: "pong" })); });
    });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(new URL(`/s/${MACHINE.id}/${SID}`, base).href);
    await page.locator(`[data-card="${APPROVAL.id}"] [data-card-actions]`).waitFor({ timeout: 15_000 });
    await page.waitForTimeout(400);
    // what of the approval is the user's to see: an element counts when its center is its own on screen (not clipped by
    // the dock's box, not under the composer or another card part)
    const seen = () => page.evaluate(([id, command]) => {
      const card = document.querySelector(`[data-card="${id}"]`);
      const own = (el) => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const top = document.elementFromPoint(r.left + r.width / 2, r.top + Math.min(r.height / 2, 8));
        return Boolean(top && (top === el || el.contains(top)));
      };
      const allow = [...card.querySelectorAll("[data-card-actions] button")].at(-1);
      const title = card.querySelector("[data-pending-card] .font-medium.text-ink, [data-pending-card] [class*='text-[13.5px]']");
      const code = [...card.querySelectorAll("pre")].find((p) => p.textContent?.includes(command));
      const line = card.querySelector("[data-card-answers]");
      const count = document.querySelector("[data-chat-cards-count]");
      return {
        allow: own(allow), title: own(title), command: own(code), line: own(line) ? line.textContent : null,
        count: count && own(count) ? count.textContent : null, focused: document.activeElement === card.querySelector("[data-pending-card]"),
      };
    }, [APPROVAL.id, COMMAND]);
    const first = await seen();
    await page.screenshot({ path: `${output}/${name}-initial.png` });
    const run = { name, first, errors };
    result.runs.push(run);
    if (height >= 1600) {
      assert.ok(first.allow && first.title && first.command, `${name}: both cards fit, yet the approval is not all in view`);
      assert.equal(first.line, null, `${name}: the answer row names its card while the card is in view`);
      assert.equal(first.count, null, `${name}: the stack is counted while nothing is cut off`);
    } else {
      // never an Allow next to nothing: its title and command in view in the card, or on its answer row's line
      if (first.allow) assert.ok((first.title && first.command) || (first.line?.includes("Bash") && first.line.includes(COMMAND)), `${name}: Allow is in view but what it allows is not (${JSON.stringify(first)})`);
      assert.match(first.count ?? "", /2 pending/, `${name}: the cut-off stack does not say how many it holds`);
      if (!first.allow) {
        // wholly below the box: nothing of it to press; the count says it is there, and scrolling the box brings it
        assert.equal(first.line, null, `${name}: a line is drawn for an answer row out of view`);
        await page.locator("[data-chat-dock-above]").evaluate((box) => { box.scrollTop = box.scrollHeight; });
        await page.waitForTimeout(300);
        const scrolled = await seen();
        run.scrolled = scrolled;
        await page.screenshot({ path: `${output}/${name}-scrolled.png` });
        assert.ok(scrolled.allow && scrolled.title && scrolled.command, `${name}: scrolled to its end, the approval is not all in view (${JSON.stringify(scrolled)})`);
        console.log(JSON.stringify(run));
        assert.deepEqual(errors, [], `${name}: page errors`);
        await context.close();
        continue;
      }
      assert.ok(first.line?.includes(COMMAND), `${name}: the cut-off approval's answer row does not say what it answers`);
      await page.locator(`[data-card="${APPROVAL.id}"] [data-card-answers]`).click();
      await page.waitForTimeout(300);
      const revealed = await seen();
      run.revealed = revealed;
      await page.screenshot({ path: `${output}/${name}-revealed.png` });
      assert.ok(revealed.title && revealed.command && revealed.allow, `${name}: the line did not bring the whole approval into view (${JSON.stringify(revealed)})`);
      assert.ok(revealed.focused, `${name}: the focus did not move onto the approval`);
    }
    console.log(JSON.stringify(run));
    assert.deepEqual(errors, [], `${name}: page errors`);
    await context.close();
  }
  result.status = "pass";
} catch (error) {
  result.status = result.status === "timeout" ? "timeout" : "fail";
  result.error = String(error);
} finally {
  clearTimeout(deadline);
  await writeFile(`${output}/results.json`, JSON.stringify(result, null, 2));
  await browser.close().catch(() => {});
  console.log(`${result.status.toUpperCase()} pending cards cut off check${result.error ? `: ${result.error.split("\n")[0]}` : ""}`);
  process.exit(result.status === "pass" ? 0 : 1);
}
