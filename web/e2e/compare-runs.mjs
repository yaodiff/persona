// End-to-end check of Compare runs (#8916, docs/web/web.md "Compare runs"): New agent's Runs choice appears only with the
// worktree switch on; Start 3 closes the sheet on the runs' page at once, which shows each run's worktree and start as they
// go (the fake hub's worktree/add held back a little so the steps can be seen), then three sessions with their titles,
// state words and branches; a branch the fake node refuses ("taken") fails every run with its reason and a Retry; a run's
// chat has ⋯ › Compare runs back to the page.
//
// Needs the web page against the fake hub with its agent worktrees and reviews, and Chrome (playwright-core drives it):
//
//   MOCK_AGENT_WORKTREE=1 MOCK_REVIEW=1 PORT=18916 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18916 npx vite --port 5916 --strictPort &)
//   node web/e2e/compare-runs.mjs http://127.0.0.1:5916 [--shots <dir>] [--channel msedge]
//
// Runs wide (1280) and phone width (390), light and dark, in English, then once in Chinese (1280, light); --shots saves each
// state as web-compare-runs-<state>-<width>-<theme>.png (the Chinese pass adds -zh). Prints one line per check and a
// verdict; exits 1 on a failure.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const shots = flag("--shots");
const channel = flag("--channel") ?? "chrome";
if (!base) {
  console.error("usage: node web/e2e/compare-runs.mjs <web URL> [--shots <dir>] [--channel msedge]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });

// the words the checks look for, as the catalogs say them
const WORDS = {
  en: { locale: "en-US", title: "New agent", start: "Start", start3: "Start 3", retry: "Retry", didnt: "Didn't start", menu: "Compare runs", open: "Open", more: "More actions", submenu: "More" },
  "zh-Hans": { locale: "zh-CN", title: "新建 Agent", start: "启动", start3: "启动 3 次", retry: "重试", didnt: "没开起来", menu: "对比运行", open: "打开", more: "更多操作", submenu: "更多" },
};
const REPO = "/Users/me/code/weather";

let failed = false;
const ok = (msg) => console.log(`ok   ${msg}`);
function fail(msg) {
  failed = true;
  throw new Error(msg);
}
async function until(check, what, timeout = 10000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    if (await check()) return;
    await new Promise((r) => setTimeout(r, 120));
  }
  fail(typeof what === "function" ? await what() : what);
}

async function open(browser, { theme, width, lang }) {
  const ctx = await browser.newContext({
    viewport: { width, height: width > 600 ? 1000 : 844 }, deviceScaleFactor: 2, colorScheme: theme, locale: WORDS[lang].locale, hasTouch: width <= 600,
  });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
  await page.evaluate(({ theme, lang, repo }) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", lang);
    localStorage.setItem("new.machine", "studio");
    localStorage.setItem("new.cwd.studio", repo);
  }, { theme, lang, repo: REPO });
  await page.reload();
  await page.waitForSelector("main", { timeout: 15000 });
  return { ctx, page };
}

function shooter(page, { theme, width, lang }) {
  return async (state) => {
    if (!shots) return;
    await page.waitForTimeout(350);
    await page.screenshot({ path: join(shots, `web-compare-runs-${state}-${width}-${theme}${lang === "en" ? "" : "-zh"}.png`) });
  };
}

async function newAgent(page, w) {
  const dialog = page.getByRole("dialog", { name: w.title, exact: true });
  // a page just loaded may not listen for the request yet: ask again until the sheet is there
  await until(async () => {
    if (await dialog.count()) return true;
    await page.evaluate(() => import("/src/lib/new-agent.ts").then((m) => m.openNewAgent()));
    await page.waitForTimeout(400);
    return (await dialog.count()) > 0;
  // a cold dev server compiles the sheet on its first open
  }, async () => {
    if (shots) await page.screenshot({ path: join(shots, "web-compare-runs-no-sheet.png") });
    const dialogs = await page.getByRole("dialog").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label") || e.textContent?.slice(0, 40)));
    return `New agent didn't open (at ${page.url()}, dialogs: ${JSON.stringify(dialogs)})`;
  }, 45000);
  await dialog.getByRole("button", { name: w.start, exact: true }).waitFor({ timeout: 15000 });
  // the worktree switch waits for worktree/status (400 ms after the folder settles)
  await dialog.locator('[data-testid="new-agent-worktree"]').waitFor({ timeout: 10000 });
  return dialog;
}

async function startRuns(page, w, tag, { prompt, branch, shot }) {
  const dialog = await newAgent(page, w);
  if (await dialog.locator("[data-new-agent-runs]").count()) fail(`${tag}: Runs shows with the worktree switch off`);
  if (shot) await shot("sheet-worktree-off");
  await dialog.locator('[data-testid="new-agent-worktree"] [role="switch"]').click();
  const runs = dialog.locator('[data-new-agent-runs] [role="radio"]');
  await until(async () => (await runs.count()) === 4, `${tag}: Runs doesn't offer 1–4`);
  if ((await runs.nth(0).getAttribute("aria-checked")) !== "true") fail(`${tag}: Runs isn't 1 by default`);
  await runs.nth(2).click();
  await dialog.locator("textarea").fill(prompt);
  if (branch) await dialog.locator("[data-worktree-branch]").fill(branch);
  if (shot) await shot("sheet-runs");
  ok(`${tag}: Runs 1–4 only with the worktree switch on, 1 by default`);
  const at = Date.now();
  await dialog.getByRole("button", { name: w.start3, exact: true }).click();
  await page.waitForURL(/\/compare\/[^/]+$/, { timeout: 5000 });
  await page.locator('[data-run="3"]').waitFor({ timeout: 2000 });
  // its closing animation may still be on its way out
  await page.getByRole("dialog", { name: w.title, exact: true }).waitFor({ state: "detached", timeout: 1500 }).catch(() => fail(`${tag}: the sheet stayed open`));
  ok(`${tag}: Start 3 closed the sheet on the runs' page with three cards in ${Date.now() - at} ms`);
}

async function run(browser, pass) {
  const { theme, width, lang } = pass;
  const w = WORDS[lang];
  const tag = `${width}/${theme}/${lang}`;
  const { ctx, page } = await open(browser, pass);
  const shot = shooter(page, pass);
  // hold each worktree/add back so the steps can be seen
  await page.route("**/worktree/add", async (route) => {
    await new Promise((r) => setTimeout(r, 900));
    await route.continue();
  });

  await startRuns(page, w, tag, { prompt: "Add an hourly forecast view", shot });
  await until(async () => (await page.locator('[data-run-status="worktree"]').count()) === 1 && (await page.locator('[data-run-status="waiting"]').count()) >= 1,
    `${tag}: no run making its worktree while the next waits`);
  await shot("progress");
  ok(`${tag}: one run makes its worktree while the next waits its turn`);
  await until(async () => (await page.locator('[data-run-status="started"]').count()) === 3, async () => `${tag}: the runs didn't all start: ${await page.locator("[data-run]").evaluateAll((els) => els.map((e) => e.dataset.runStatus).join(","))}`, 20000);
  const branches = await page.locator("[data-run] .font-mono").allTextContents();
  if (branches.join(",") !== "persona/add-an-hourly-forecast-view-1,persona/add-an-hourly-forecast-view-2,persona/add-an-hourly-forecast-view-3") fail(`${tag}: branches ${branches}`);
  await until(async () => (await page.locator("[data-run-changes]").count()) === 3, `${tag}: no changes line on the started runs`);
  await shot("started");
  ok(`${tag}: three sessions with their branches -1…-3 and their changes`);

  // ⋯ › Compare runs in a run's chat goes back
  const compareURL = page.url();
  await page.locator('[data-run="1"]').getByRole("button", { name: w.open, exact: true }).click();
  await page.waitForURL((u) => u.href !== compareURL, { timeout: 5000 });
  if (!/\/(s|t)\/studio\//.test(page.url())) fail(`${tag}: Open went to ${page.url()}`);
  await page.getByRole("button", { name: w.more, exact: true }).first().click();
  await page.getByRole("menuitem", { name: w.submenu, exact: true }).click();
  const item = page.getByRole("menuitem", { name: w.menu, exact: true });
  await item.waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: a run's ⋯ has no ${w.menu}`));
  if (shots) await shot("chat-menu");
  await item.click();
  await page.waitForURL(compareURL, { timeout: 5000 });
  ok(`${tag}: a run's ⋯ › ${w.menu} goes back to the page`);

  // a refused branch fails each run, with its reason and Retry
  await startRuns(page, w, tag, { prompt: "Try a taken branch", branch: "persona/taken" });
  await until(async () => (await page.locator('[data-run-status="failed"]').count()) === 3, `${tag}: the refused runs didn't all fail`, 20000);
  const card = page.locator('[data-run="1"]');
  if (!(await card.textContent()).includes(w.didnt)) fail(`${tag}: a refused run doesn't say ${w.didnt}`);
  if (!(await card.locator('[role="alert"]').textContent()).includes("already exists")) fail(`${tag}: a refused run doesn't give the node's reason`);
  if (!(await card.getByRole("button", { name: w.retry, exact: true }).count())) fail(`${tag}: a refused run has no ${w.retry}`);
  await shot("failed");
  ok(`${tag}: a refused branch fails each run alone, with the node's reason and ${w.retry}`);
  await ctx.close();
}

const PASSES = [
  ...[1280, 390].flatMap((width) => ["light", "dark"].map((theme) => ({ width, theme, lang: "en" }))),
  { width: 1280, theme: "light", lang: "zh-Hans" },
];
const browser = await chromium.launch({ channel });
try {
  for (const pass of PASSES) await run(browser, pass);
} catch (e) {
  failed = true;
  console.log(`FAIL ${e instanceof Error ? e.message.split("\n")[0] : String(e)}`);
} finally {
  await browser.close();
}
console.log(failed ? "verdict: FAIL" : "verdict: PASS (Compare runs)");
process.exit(failed ? 1 : 0);
