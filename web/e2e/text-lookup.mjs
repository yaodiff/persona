// Click-to-look-up on the real page, with every hub request answered in the browser.
// Usage: node web/e2e/text-lookup.mjs <web-origin> <artifactDir> [--chromium <executable>]
// Saves light and dark cards over an assistant reply, an Agent transcript and an Inbox item, the saved-word underline,
// and a page where lookup is off (no card, no style, no highlight).
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
const MAIN = "example-main";
const AGENT = "example-agent";
const persona = { machine_id: MACHINE.id, session_id: MAIN, title: "Persona", state: "idle", status: "idle", cwd: "/example", updated_at: AT, started_by_persona: true, assistant: true, capabilities: { send: true } };
const agent = { machine_id: MACHINE.id, session_id: AGENT, title: "Review the gateway", state: "idle", status: "idle", cwd: "/example", updated_at: AT, capabilities: { send: true } };
const CHAT = [
  { id: "u1", seq: 1, role: "user", kind: "text", text: "How is the gateway migration going?", ts: AT },
  { id: "a1", seq: 2, role: "assistant", kind: "text", text: "The gateway migration needs one more check before Friday.", ts: AT },
];
const TRANSCRIPT = [
  { id: "t1", seq: 1, role: "user", kind: "text", text: "Look at the gateway notes.", ts: AT },
  { id: "t2", seq: 2, role: "assistant", kind: "text", text: "The gateway migration needs one more check before Friday.", ts: AT },
];
const INBOX = {
  id: "note-1", source: "github", kind: "mention", account: "github", account_name: "GitHub", title: "Gateway notes",
  preview: "The gateway migration needs one more check before Friday.", summary: "One check left", count: 1, updated_at: AT, tier: "action",
};
const STATE = { available: true, extension: "example.dict", contribution: "dictionary", name: "Example Dictionary", underline_saved: false, words: [] };
const ENTRY = { found: true, word: "gateway", lemma: "gateway", phonetic: "/ˈɡeɪtweɪ/", senses: [{ pos: "noun", text: "a passage from one place to another" }], saved: false };

const result = { status: "running", runs: [] };
const browser = await chromium.launch(chromiumAt ? { executablePath: chromiumAt, args: ["--no-sandbox"] } : { channel: "chrome" });
const deadline = setTimeout(() => { result.status = "timeout"; void browser.close(); }, 180_000);

function install(context, features, state = STATE) {
  return context.route("**/api/v1/**", (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/v1", "");
    if (path === "/text-lookup/lookup" && route.request().method() === "POST") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(ENTRY) });
    }
    if (route.request().method() !== "GET") return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
    const data = path === "/text-lookup" ? (features.includes("text_lookup") ? state : { available: false, underline_saved: false, words: [] })
      : path === "/machines" ? { machines: [MACHINE] }
      : path === "/sessions" ? { sessions: [persona, agent] }
      : path === "/interactions" ? { interactions: [] }
      : path === "/assistant" ? { machine_id: MACHINE.id, main: MAIN, conversations: [persona], tasks: [], memory: [] }
      : path === "/inbox" ? { items: [INBOX] }
      : path === "/inbox/note-1" ? INBOX
      : path === "/todos" ? { todos: [] }
      : path.endsWith("/items") ? { items: path.includes(AGENT) ? TRANSCRIPT : CHAT, has_more: false }
      : /^\/sessions\/[^/]+\/[^/]+$/.test(path) ? { session: path.endsWith(AGENT) ? agent : persona }
      : {};
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(data) });
  });
}

async function openPage(theme, path, features, state) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: "en-US", colorScheme: theme, reducedMotion: "reduce" });
  await context.addInitScript(`localStorage.setItem('persona.token','example');localStorage.setItem('persona.language','en');localStorage.setItem('persona.theme','${theme}');`);
  await context.route(/^https?:\/\/(?!127\.0\.0\.1|localhost|\[::1\])/, (route) => route.abort());
  await install(context, features, state);
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "example", hub_name: "Example hub", features }));
    ws.onMessage((m) => { if (String(m).includes("app_ping")) ws.send(JSON.stringify({ type: "pong" })); });
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(new URL(path, base).href);
  return { context, page, errors };
}

async function clickWord(page) {
  const box = await page.evaluate(() => {
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) {
      const text = node.textContent ?? "";
      // the assistant's reply (or the Inbox body), not a title or the user's own line
      if (!text.includes("gateway migration needs")) continue;
      const at = text.indexOf("gateway");
      if (at < 0) continue;
      const parent = node.parentElement;
      if (!parent || parent.closest("a,button,code,pre,[data-no-lookup],[role=button]")) continue;
      const range = document.createRange();
      range.setStart(node, at);
      range.setEnd(node, at + "gateway".length);
      const rect = range.getBoundingClientRect();
      if (rect.width < 2 || rect.top < 0 || rect.bottom > innerHeight) continue;
      return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
    }
    return null;
  });
  assert.ok(box, "gateway is not on screen");
  await page.mouse.click(box.x, box.y);
}

try {
  for (const theme of ["light", "dark"]) {
    for (const [name, path] of [["chat", "/"], ["agent", `/s/${MACHINE.id}/${AGENT}`], ["inbox", "/inbox/note-1"]]) {
      const { context, page, errors } = await openPage(theme, path, ["text_lookup"]);
      await page.getByText("The gateway migration needs one more check before Friday.").first().waitFor({ timeout: 20_000 });
      await clickWord(page);
      const card = page.getByTestId("text-lookup");
      await card.waitFor({ timeout: 8_000 });
      await card.getByText("a passage from one place to another").waitFor({ timeout: 8_000 });
      const file = `${output}/${name}-${theme}.png`;
      await page.screenshot({ path: file });
      result.runs.push({ name: `${name}-${theme}`, errors });
      assert.deepEqual(errors, [], `${name}-${theme} page errors`);
      await context.close();
    }
  }
  for (const theme of ["light", "dark"]) {
    // saved words underlined: painted by the Custom Highlight API, the text nodes untouched
    const saved = await openPage(theme, "/", ["text_lookup"], { ...STATE, underline_saved: true, words: ["migration", "Friday"] });
    await saved.page.getByText("The gateway migration needs one more check before Friday.").first().waitFor({ timeout: 20_000 });
    await saved.page.waitForFunction(() => (CSS.highlights?.get("persona-saved-words")?.size ?? 0) >= 2, null, { timeout: 8_000 });
    assert.equal(await saved.page.locator("[data-text-lookup]").count(), 1);
    assert.equal(await saved.page.getByText("The gateway migration needs one more check before Friday.", { exact: true }).first().evaluate((el) => el.childNodes.length), 1);
    await saved.page.screenshot({ path: `${output}/underline-${theme}.png` });
    result.runs.push({ name: `underline-${theme}`, errors: saved.errors });
    assert.deepEqual(saved.errors, []);
    await saved.context.close();

    // the hub lists the feature but no dictionary is available: the page is as before
    for (const [name, features, state] of [["unavailable", ["text_lookup"], { available: false, underline_saved: false, words: [] }], ["no-feature", [], undefined]]) {
      const off = await openPage(theme, "/", features, state);
      await off.page.getByText("The gateway migration needs one more check before Friday.").first().waitFor({ timeout: 20_000 });
      await clickWord(off.page);
      await off.page.waitForTimeout(800);
      assert.equal(await off.page.getByTestId("text-lookup").count(), 0);
      assert.equal(await off.page.locator("[data-text-lookup]").count(), 0);
      assert.equal(await off.page.evaluate(() => CSS.highlights?.size ?? 0), 0);
      await off.page.screenshot({ path: `${output}/${name}-${theme}.png` });
      result.runs.push({ name: `${name}-${theme}`, errors: off.errors });
      assert.deepEqual(off.errors, []);
      await off.context.close();
    }
  }
  result.status = "pass";
} catch (error) {
  result.status = result.status === "timeout" ? "timeout" : "fail";
  result.error = String(error);
} finally {
  clearTimeout(deadline);
  await writeFile(`${output}/results.json`, JSON.stringify(result, null, 2));
  await browser.close().catch(() => {});
  console.log(`${result.status.toUpperCase()} text lookup screenshots${result.error ? `: ${result.error.split("\n")[0]}` : ""}`);
  process.exit(result.status === "pass" ? 0 : 1);
}
