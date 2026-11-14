// End-to-end check of the To-dos page's section order and Got it (#5707, docs/assistant/todo-status-sop.md): unfinished
// to-dos (PR to merge, Other open) are listed open above Just finished, Shelved and Done stay folded with Done last, and
// Got it on a Just finished row files it into Done at once, without opening the to-do, its toast's Undo putting it back.
// Got it sits at the row's right, under the time, on the status line (#6578): the row is as tall and its title as wide
// as without it, and a finger hits it across 44 px.
//
// Needs the web page against the fake hub's to-do list, and Chrome (or any Chromium through --executable):
//
//   MOCK_TODO_LIST=1 PORT=18957 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18957 npx vite --port 5957 --strictPort &)
//   node web/e2e/todo-seen.mjs http://127.0.0.1:5957 [--phase before|after] [--shots <dir>] [--executable <chromium>]
//
// Runs in Chinese, light and dark, wide (1280) and phone width (390); --shots saves each list and its Just finished
// section. --phase before only takes the screenshots and prints the order (the page as it was); after (the default) also
// checks the order, the folds, Got it's place and Got it. Prints one line per check and a verdict; exits 1 on a failure
// and after three minutes.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const phase = flag("--phase") ?? "after";
const shots = flag("--shots");
const executable = flag("--executable");
if (!base || (phase !== "before" && phase !== "after")) {
  console.error("usage: node web/e2e/todo-seen.mjs <web URL> [--phase before|after] [--shots <dir>] [--executable <chromium>]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 3 minutes");
  process.exit(1);
}, 180_000).unref();

const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}

const hub = (path, init = {}) =>
  fetch(`${base}/api/v1${path}`, { ...init, headers: { Authorization: "Bearer mock", ...(init.headers ?? {}) }, signal: AbortSignal.timeout(5000) });

async function open(browser, theme, width) {
  const ctx = await browser.newContext({ viewport: { width, height: width > 600 ? 2300 : 2600 }, deviceScaleFactor: 2, colorScheme: theme, locale: "zh-CN", hasTouch: width <= 600 });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
  await page.evaluate((t) => {
    localStorage.setItem("persona.theme", t);
    localStorage.setItem("persona.language", "zh-Hans");
    localStorage.setItem("persona.todos.view", "list");
  }, theme);
  await page.goto(`${base}/todos`);
  await page.waitForSelector('[data-testid="todos-sections"] [data-testid="todo-section"]', { timeout: 15000 });
  return { ctx, page };
}

// Each section in the page's order, with whether its rows show.
const sections = (page) =>
  page.$$eval('[data-testid="todos-sections"] [data-testid="todo-section"]', (els) =>
    els.map((el) => ({ section: el.dataset.section, open: el.querySelector("button[aria-expanded]")?.getAttribute("aria-expanded") === "true" })),
  );

// The list column down to its last section (the page is tall so every section is laid out at once).
async function shot(page, name) {
  if (!shots) return;
  const col = await page.locator('[data-testid="todos-list-column"]').boundingBox();
  const end = await page.locator('[data-testid="todos-sections"]').boundingBox();
  const height = Math.min(col.height, end.y + end.height + 24 - col.y);
  await page.screenshot({ path: join(shots, `${name}.png`), clip: { x: col.x, y: col.y, width: col.width, height } });
}

// The Just finished section alone, a little room around it.
async function sectionShot(page, name) {
  if (!shots) return;
  const col = await page.locator('[data-testid="todos-list-column"]').boundingBox();
  const box = await page.locator('[data-testid="todo-section"][data-section="just_done"]').boundingBox();
  const y = Math.max(col.y, box.y - 8);
  await page.screenshot({ path: join(shots, `${name}.png`), clip: { x: col.x, y, width: col.width, height: box.y + box.height + 8 - y } });
}

// Where Got it sits on the first Just finished row (#6578), measured with it and again with it taken out of the layout.
const placement = (page) =>
  page.evaluate(() => {
    const row = document.querySelector('[data-section="just_done"] [data-testid="todo-triage-row"] > [role="button"]');
    const btn = row?.querySelector('[data-testid="todo-seen"]');
    if (!row || !btn) return null;
    const box = (el) => el.getBoundingClientRect();
    const title = row.querySelector('[data-testid="todo-title"]');
    const age = row.querySelector('[data-testid="todo-age"]');
    const line = row.querySelector('[data-testid="todo-status-line"]');
    const b = box(btn);
    const at = (dy) => document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2 + dy)?.closest('[data-testid="todo-seen"]') === btn;
    const shown = { row: box(row).height, title: box(title).width };
    btn.style.display = "none";
    const hidden = { row: box(row).height, title: box(title).width };
    btn.style.display = "";
    return {
      shown,
      hidden,
      onStatusLine: Boolean(line?.contains(btn)),
      rightGap: Math.abs(box(age).right - b.right),
      belowTime: b.top >= box(age).bottom - 1,
      drawn: b.height,
      coarse: matchMedia("(pointer: coarse)").matches,
      reach: at(-20) && at(20),
    };
  });

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
try {
  for (const [theme, width] of [["light", 1280], ["dark", 1280], ["light", 390], ["dark", 390]]) {
    const where = `${theme} ${width}px`;
    const { ctx, page } = await open(browser, theme, width);
    await page.waitForTimeout(400); // the sessions' push settles the sections
    const order = await sections(page);
    console.log(`    ${where}: ${order.map((s) => `${s.section}${s.open ? "" : " (folded)"}`).join(", ")}`);
    await shot(page, `todos-${phase}-${width}-${theme}`);
    await sectionShot(page, `todos-${phase}-justdone-${width}-${theme}`);
    if (phase === "after") {
      const names = order.map((s) => s.section);
      const want = ["waiting", "working", "pr", "other", "just_done", "shelved", "done"].filter((s) => names.includes(s));
      if (names.join() !== want.join()) fail(`${where}: sections in order ${names.join(", ")}, want ${want.join(", ")}`);
      const folded = order.filter((s) => !s.open).map((s) => s.section);
      if (folded.join() !== ["shelved", "done"].filter((s) => names.includes(s)).join()) fail(`${where}: folded ${folded.join(", ")}, want shelved, done`);
      ok(`${where}: unfinished sections open above Just finished; Shelved and Done folded, Done last`);
      const p = await placement(page);
      if (!p) fail(`${where}: no Got it on the first Just finished row`);
      if (!p.onStatusLine) fail(`${where}: Got it is not on the status line`);
      if (p.rightGap > 1 || !p.belowTime) fail(`${where}: Got it is not under the time (${p.rightGap}px off its right edge, below it: ${p.belowTime})`);
      if (p.shown.row !== p.hidden.row) fail(`${where}: the row is ${p.shown.row}px tall with Got it, ${p.hidden.row}px without`);
      if (p.shown.title !== p.hidden.title) fail(`${where}: the title is ${p.shown.title}px wide with Got it, ${p.hidden.title}px without`);
      ok(`${where}: Got it on the status line under the time; row ${p.shown.row}px and title ${p.shown.title}px, as without it`);
      if (width <= 600) {
        if (!p.coarse) fail(`${where}: the phone context does not report a coarse pointer`);
        if (!p.reach) fail(`${where}: a finger 20px above or below Got it's middle misses it (drawn ${p.drawn}px tall)`);
        ok(`${where}: Got it drawn ${p.drawn}px tall, hit 20px above and below its middle`);
      }
    }
    await ctx.close();
  }

  if (phase === "after") {
    for (const theme of ["light", "dark"]) {
      const { ctx, page } = await open(browser, theme, 1280);
      const justDone = page.locator('[data-testid="todo-section"][data-section="just_done"]');
      const row = justDone.locator('[data-testid="todo-triage-row"]').first();
      const id = await row.getAttribute("data-id");
      // the row itself is a button whose name has every word in it: take Got it by its test id, and check its words
      const gotIt = row.locator('[data-testid="todo-seen"]').first();
      await gotIt.waitFor({ timeout: 5000 });
      if ((await gotIt.innerText()).trim() !== "知道了") fail(`${theme}: Got it reads ${await gotIt.innerText()}`);
      await shot(page, `todos-after-gotit-row-${theme}`);
      await gotIt.click();
      // filed into Done at once, before the hub's answer comes back
      await page.waitForFunction((tid) => !document.querySelector(`[data-section="just_done"] [data-testid="todo-triage-row"][data-id="${tid}"]`), id, { timeout: 2000 });
      // and the press stayed on Got it: the row under it did not open the to-do
      if (new URL(page.url()).searchParams.has("id")) fail(`${theme}: Got it also opened the to-do (${page.url()})`);
      ok(`${theme}: Got it did not open the to-do`);
      await page.waitForFunction((tid) => {
        const s = document.querySelector('[data-testid="todo-section"][data-section="done"]');
        return s && Number(s.getAttribute("data-todos")) > 0 && s.querySelector("button[aria-expanded]") && !document.querySelector(`[data-section="just_done"] [data-id="${tid}"]`);
      }, id, { timeout: 5000 });
      const toast = page.getByRole("button", { name: "撤销", exact: true }).first();
      await toast.waitFor({ timeout: 3000 });
      await page.waitForTimeout(300);
      if (shots) await page.screenshot({ path: join(shots, `todos-after-gotit-toast-${theme}.png`) });
      const seen = (await (await hub("/todos")).json()).todos.find((t) => t.id === id)?.seen_at;
      if (!seen) fail(`${theme}: the hub has no seen_at for ${id} after Got it`);
      ok(`${theme}: Got it filed ${id} into Done and the hub keeps seen_at`);
      await toast.click();
      await page.waitForFunction((tid) => document.querySelector(`[data-section="just_done"] [data-testid="todo-triage-row"][data-id="${tid}"]`), id, { timeout: 5000 });
      const after = (await (await hub("/todos")).json()).todos.find((t) => t.id === id)?.seen_at;
      if (after) fail(`${theme}: Undo left seen_at ${after} on the hub`);
      ok(`${theme}: Undo put ${id} back into Just finished`);
      await ctx.close();
    }
  }
  console.log(`VERDICT: ${phase === "before" ? "screenshots taken" : "pass"}`);
} finally {
  await browser.close();
}
