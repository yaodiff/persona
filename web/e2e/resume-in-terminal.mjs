// End-to-end check of Resume in terminal (#5521, docs/web/web.md "Terminal ⇄ chat"): the chat's ⋯ of an Agent that isn't
// running offers it where its computer has a terminal engine. On the fake hub's MacBook (herdr): a Claude Code from a
// terminal that has been closed since and a Codex thread nothing has open, both stopped, offer it; picking it opens the
// terminal and resumes the Agent there (the fake hub's start with resume_session_id). A claude still running in a
// terminal of its own offers none. On the Mac mini, which enables no terminal engine, it is there, disabled, with why.
//
// Needs the web page against the fake hub with MOCK_RESUME_TERMINAL=1, and Chrome or Edge (playwright-core drives it):
//
//   MOCK_RESUME_TERMINAL=1 MOCK_TERMINAL_STREAM=1 PORT=18921 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18921 npx vite --port 5921 --strictPort &)
//   node web/e2e/resume-in-terminal.mjs http://127.0.0.1:5921 --hub http://127.0.0.1:18921 [--shots <dir>] [--channel msedge] [--before]
//
// Runs wide (1280) and phone width (390), light and dark, in Chinese; --shots saves each state. --before only takes the
// shots (a build without the feature). Prints one line per check and a verdict; exits 1 on a failure.
import { mkdirSync } from "node:fs";
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
const channel = flag("--channel") ?? "chrome";
const before = args.includes("--before");
if (!base || !hub) {
  console.error("usage: node web/e2e/resume-in-terminal.mjs <web URL> --hub <fake hub URL> [--shots <dir>] [--channel msedge] [--before]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });

// the fake hub's sessions (scripts/mock/mock_hub.py MOCK_RESUME_TERMINAL)
const STOPPED = "macbook/5e000000-0000-4000-8000-000000005521";
const CODEX = "macbook/019a5521-0000-7000-8000-000000000001";
const LIVE = "macbook/5e000000-0000-4000-8000-000000005522";
const NO_ENGINE = "mini/5e000000-0000-4000-8000-000000005523";
const ITEM = "在终端里恢复";

const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}
const mock = async (path) => (await fetch(`${hub}${path}`, { signal: AbortSignal.timeout(5000) })).json();
const resumes = async () => ((await mock("/mock/started")).started ?? []).filter((s) => s.resume_session_id).map((s) => s.resume_session_id);

async function until(check, what) {
  for (let i = 0; i < 60; i++) {
    if (await check()) return;
    await new Promise((r) => setTimeout(r, 150));
  }
  fail(what);
}

async function open(browser, theme, width) {
  const ctx = await browser.newContext({ viewport: { width, height: width > 600 ? 860 : 844 }, deviceScaleFactor: 2, colorScheme: theme, locale: "zh-CN", hasTouch: width <= 600 });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
  await page.evaluate((t) => {
    localStorage.setItem("persona.theme", t);
    localStorage.setItem("persona.language", "zh-Hans");
  }, theme);
  return { ctx, page };
}

const shot = async (page, name) => {
  if (!shots) return;
  await page.waitForTimeout(500); // the menu's and the panel's motion settle first
  await page.screenshot({ path: join(shots, `web-resume-${name}.png`) });
};

/** The chat of `key` ("machine/session"), its messages loaded. */
async function chat(page, key, tag) {
  await page.goto(`${base}/s/${key}`);
  await page.waitForSelector("[data-chat-title]", { timeout: 15000 }).catch(() => fail(`${tag}: ${key} did not open`));
  await page.waitForTimeout(800);
}

/** Its ⋯ menu, open: the menu element. */
async function menu(page, tag) {
  const more = page.getByRole("button", { name: "更多操作", exact: true }).locator("visible=true").first();
  await more.click();
  const m = page.getByRole("menu").first();
  await m.waitFor({ state: "visible", timeout: 5000 }).catch(() => fail(`${tag}: ⋯ did not open`));
  if (before) return m;
  await m.getByRole("menuitem", { name: "更多", exact: true }).click();
  return page.getByRole("menu").last();
}

const closeMenu = async (page) => {
  await page.keyboard.press("Escape");
  await page.waitForTimeout(250);
};

async function run(browser, theme, width) {
  const tag = `${width}/${theme}`;
  await mock("/mock/reset");
  const { ctx, page } = await open(browser, theme, width);

  // A Claude Code whose terminal was closed: stopped. ⋯ offers Resume in terminal; it opens the terminal and resumes there.
  await chat(page, STOPPED, tag);
  await shot(page, `${width}-${theme}-stopped`);
  let m = await menu(page, tag);
  await shot(page, `${width}-${theme}-stopped-menu`);
  if (!before) {
    const item = m.getByRole("menuitem", { name: ITEM, exact: true });
    if (!(await item.count()) || (await item.isDisabled())) fail(`${tag}: no Resume in terminal for a stopped Agent`);
    await item.click();
    await until(async () => (await resumes()).includes(STOPPED.split("/")[1]), `${tag}: no resume reached the hub`);
    // the terminal is the page's view now: the header's switch goes back to the chat
    await page.getByRole("button", { name: "对话", exact: true }).locator("visible=true").first().waitFor({ state: "visible", timeout: 10000 }).catch(() => fail(`${tag}: the terminal did not open`));
    await until(async () => !(await page.locator("[data-stopped-agent]").count()), `${tag}: still a stopped Agent after Resume`);
    await shot(page, `${width}-${theme}-stopped-resumed`);
    ok(`${tag}: a stopped Agent resumes in its terminal from ⋯`);
  } else await closeMenu(page);

  // A Codex thread nothing has open: stopped the same way.
  await chat(page, CODEX, tag);
  await shot(page, `${width}-${theme}-codex`);
  m = await menu(page, tag);
  await shot(page, `${width}-${theme}-codex-menu`);
  if (!before) {
    const item = m.getByRole("menuitem", { name: ITEM, exact: true });
    if (!(await item.count()) || (await item.isDisabled())) fail(`${tag}: no Resume in terminal for a stopped Codex thread`);
    if (!(await page.locator("[data-stopped-agent]").count())) fail(`${tag}: the Codex thread's chat has no stopped line`);
    ok(`${tag}: a stopped Codex thread offers it too, with the stopped line`);
  }
  await closeMenu(page);

  // Still running in a terminal of its own: nothing to resume.
  await chat(page, LIVE, tag);
  m = await menu(page, tag);
  await shot(page, `${width}-${theme}-live-menu`);
  if (!before && (await m.getByRole("menuitem", { name: ITEM, exact: true }).count())) fail(`${tag}: Resume in terminal offered while it runs`);
  if (!before) ok(`${tag}: not offered while it runs in another terminal`);
  await closeMenu(page);

  // No terminal engine on its computer: there, disabled, saying why.
  await chat(page, NO_ENGINE, tag);
  m = await menu(page, tag);
  await shot(page, `${width}-${theme}-no-engine-menu`);
  if (!before) {
    const item = m.getByRole("menuitem", { name: new RegExp(`^${ITEM}`) });
    if (!(await item.count()) || !(await item.isDisabled())) fail(`${tag}: no disabled Resume in terminal on a computer without a terminal engine`);
    if (!(await item.textContent()).includes("Mac mini 没有启用终端引擎")) fail(`${tag}: the disabled item doesn't say why: ${await item.textContent()}`);
    ok(`${tag}: disabled with its reason where no terminal engine is enabled`);
  }
  await closeMenu(page);
  await ctx.close();
}

const browser = await chromium.launch({ channel });
try {
  for (const theme of ["light", "dark"]) for (const width of [1280, 390]) await run(browser, theme, width);
} finally {
  await browser.close();
}
console.log(before ? "PASS (shots only)" : "PASS resume-in-terminal");
