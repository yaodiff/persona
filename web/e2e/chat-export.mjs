// End-to-end check of Export Conversation… (#7375, docs/web/web.md "Exporting a conversation"), as the user does it: an
// Agent's chat › ⋯ › Export Conversation… reads the whole conversation and says how many messages it holds; Download .md
// saves a file named after the chat with its messages and none of its tool calls; with Include work steps the file has
// each tool call in a collapsible block; a Persona conversation's file has the assistant's replies.
//
// Needs the web page against the fake hub and Chrome (Edge with --channel msedge, any Chromium through --executable):
//
//   PORT=18798 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18798 npx vite --host 127.0.0.1 --port 5798 --strictPort &)
//   node web/e2e/chat-export.mjs http://127.0.0.1:5798 --hub http://127.0.0.1:18798 [--shots <dir>] [--channel msedge | --executable <chromium>]
//
// Runs wide (1440×900) and phone width (390×844, touch), light and dark, in English; --shots saves the dialog without and
// with work steps. Prints one line per check and a verdict; exits 1 on a failure and after six minutes. Every request
// outside the two loopback servers is refused; it changes nothing on the hub.
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const hub = (flag("--hub") ?? "").replace(/\/$/, "");
const shots = flag("--shots");
const executable = flag("--executable");
const channel = flag("--channel") ?? "chrome";
if (!base || !hub) {
  console.error("usage: node web/e2e/chat-export.mjs <web URL> --hub <mock hub URL> [--shots <dir>] [--channel msedge | --executable <chromium>]");
  process.exit(2);
}
for (const url of [base, hub]) {
  if (!["127.0.0.1", "localhost"].includes(new URL(url).hostname)) {
    console.error(`only a loopback mock is allowed: ${url}`);
    process.exit(2);
  }
}
if (shots) mkdirSync(shots, { recursive: true });
const saved = mkdtempSync(join(tmpdir(), "persona-chat-export-"));
setTimeout(() => {
  console.log("FAIL timed out after 6 minutes");
  process.exit(1);
}, 360_000).unref();

const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}

// the mock hub's widget Agent on the MacBook (five tool calls), and the assistant's reading-notes conversation
const WIDGET = "a1f3c2d4-0000-4000-8000-000000000001";
const NOTES = "b2e4d6f8-0000-4000-8000-000000000004";
const get = async (path) => (await fetch(`${hub}${path}`, { headers: { Authorization: "Bearer mock" }, signal: AbortSignal.timeout(5000) })).json();
const widget = (await get(`/api/v1/sessions/macbook/${WIDGET}/items?turns=10&lazy_images=1`)).items ?? [];
const asked = widget.find((i) => i.role === "user" && i.kind === "text")?.text;
const tools = widget.filter((i) => i.kind === "tool_use").length;
if (!asked || tools < 2) fail("the fake hub's widget Agent has no message of yours or no tool calls");

async function open(browser, theme, phone) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    colorScheme: theme,
    locale: "en-US",
    hasTouch: phone,
    isMobile: phone,
    acceptDownloads: true,
  });
  await ctx.route("**/*", (route) => {
    const origin = new URL(route.request().url()).origin;
    return [new URL(base).origin, new URL(hub).origin].includes(origin) ? route.continue() : route.abort();
  });
  await ctx.addInitScript((theme) => {
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
  }, theme);
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
  return { ctx, page, errors };
}

/** Moves to an in-app path the way a link does (no reload: the page keeps what it holds). */
const go = (page, path) =>
  page.evaluate((path) => {
    history.pushState(null, "", path);
    dispatchEvent(new PopStateEvent("popstate"));
  }, path);

const shot = async (page, name) => {
  if (!shots) return;
  await page.waitForTimeout(350); // let the open animations finish
  await page.screenshot({ path: join(shots, `${name}.png`) });
};

/** ⋯ › Export Conversation…, until it has read the conversation; the dialog. */
async function exportDialog(page, where) {
  const more = page.locator('header button[aria-label="More actions"]:visible').first();
  await more.click();
  const item = page.locator('[role="menu"]').last().getByRole("menuitem", { name: "Export Conversation…" });
  if (!(await item.count())) fail(`${where}: no Export Conversation… in the chat's ⋯`);
  await item.click();
  const dialog = page.locator('[role="dialog"]').filter({ has: page.locator("[data-export-dialog]") });
  await dialog.waitFor({ state: "visible" });
  await dialog.locator('[data-testid="export-status"]').filter({ hasText: /\d+ messages · / }).waitFor();
  return dialog;
}

async function download(page, dialog, where) {
  const [file] = await Promise.all([page.waitForEvent("download"), dialog.locator('[data-testid="export-download"]').click()]);
  const path = join(saved, `${where}-${file.suggestedFilename()}`);
  await file.saveAs(path);
  await dialog.waitFor({ state: "hidden" });
  return { name: file.suggestedFilename(), md: readFileSync(path, "utf8") };
}

async function agentChat(page, where) {
  await go(page, `/s/macbook/${WIDGET}`);
  await page.locator("main").getByText(asked, { exact: false }).first().waitFor({ state: "visible" });
  let dialog = await exportDialog(page, where);
  await shot(page, `${where}-1-dialog`);
  let file = await download(page, dialog, where);
  if (!/^.+ \d{4}-\d{2}-\d{2}\.md$/.test(file.name)) fail(`${where}: the file is called ${file.name}`);
  if (!file.md.startsWith("# ") || !file.md.includes("## You · ") || !file.md.includes(asked.trim().split("\n")[0])) fail(`${where}: the file has not the conversation:\n${file.md.slice(0, 400)}`);
  if (file.md.includes("<details>")) fail(`${where}: work steps are in a file without them`);
  ok(`${where}: Download .md saves ${file.name} with the conversation and no work steps`);

  dialog = await exportDialog(page, where);
  await dialog.locator('[data-testid="export-work"]').check();
  await dialog.locator('[data-testid="export-status"]').filter({ hasText: /\d+ messages · / }).waitFor();
  await shot(page, `${where}-2-work`);
  file = await download(page, dialog, where);
  const blocks = (file.md.match(/<summary>/g) ?? []).length;
  if (blocks < tools) fail(`${where}: ${blocks} work blocks for ${tools} tool calls`);
  ok(`${where}: with work steps the file has ${blocks} collapsible blocks (${tools} tool calls)`);
}

async function personaChat(page, where) {
  await go(page, `/persona/${NOTES}`);
  await page.locator("main").waitFor({ state: "visible" });
  const dialog = await exportDialog(page, `${where} (Persona)`);
  const file = await download(page, dialog, `${where}-persona`);
  if (!file.md.includes("## You · ") || (file.md.match(/^## /gm) ?? []).length < 2) fail(`${where}: the Persona conversation's file has no parts:\n${file.md.slice(0, 400)}`);
  ok(`${where}: a Persona conversation exports too`);
}

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel });
try {
  for (const phone of [false, true]) {
    for (const theme of ["light", "dark"]) {
      const where = `${phone ? "phone" : "wide"}-${theme}`;
      const { ctx, page, errors } = await open(browser, theme, phone);
      await agentChat(page, where);
      await personaChat(page, where);
      if (errors.length) fail(`${where}: page errors: ${errors.join(" | ")}`);
      await ctx.close();
    }
  }
} finally {
  await browser.close();
  rmSync(saved, { recursive: true, force: true });
}
console.log("PASS chat export: the dialog, Download .md with and without work steps, Persona conversations, wide and phone, light and dark");
process.exit(0);
