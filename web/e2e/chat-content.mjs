// Chat content in a real browser against the mock hub (audit items 7, 50, 73, 77, 80 and 90): layout, focus and timing
// that jsdom cannot show. Synthetic data only; it writes nothing to the hub.
//
// Default run: start the mock with MOCK_TASKS=1 MOCK_MARKDOWN=1 MOCK_EXCHANGES=1 MOCK_MANAGERS=1 MOCK_RICH=1
// MOCK_TASKS_SENT=1 and Vite (or `vite preview`) pointing at it (PERSONA_HUB_URL). `--big`: the mock with MOCK_BIG=1
// instead (item 90's long fold).
// node web/e2e/chat-content.mjs <loopback web URL> <artifact directory> [--big] [Chromium executable]
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const big = args.includes("--big");
const [base, shots, executable] = args.filter((a) => a !== "--big");
if (!base || !shots || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?\/?$/.test(base)) {
  console.error("usage: node web/e2e/chat-content.mjs <loopback web URL> <artifact directory> [--big] [Chromium executable]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => {
  console.error("RESULT: chat-content timed out (240s)");
  process.exit(124);
}, 240_000);
const root = base.replace(/\/$/, "");
const WIDGET = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000001"; // a working session with a long first message
const TABLES = "/s/macbook/a1f3c2d4-0000-4000-8000-00000000000a"; // a reply with a narrow table and a wide one
const BIG = "/s/server/a1f3c2d4-0000-4000-8000-000000000008"; // MOCK_BIG: 650 commands folded into one work line
const CHIPS = "/s/macbook/a1f3c2d4-0000-4000-8000-000000009146"; // MOCK_TABLE_CHIPS: a table with code chips in a sentence
const results = [];
const check = (name, ok, detail) => {
  results.push({ name, ok: Boolean(ok), detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
};

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
async function page({ path, width, height, theme, touch = false }) {
  const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme, hasTouch: touch, deviceScaleFactor: 2, serviceWorkers: "block" });
  await context.addInitScript((theme) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "zh-Hans");
  }, theme);
  // nothing leaves the loopback origin, and nothing but reads reaches the mock
  await context.route("**/*", (route) => (new URL(route.request().url()).origin === new URL(root).origin ? route.continue() : route.abort()));
  const p = await context.newPage();
  await p.goto(`${root}/?token=mock`);
  await p.waitForFunction(() => localStorage.getItem("persona.token"));
  await p.goto(root + path);
  return p;
}
// the chat follows its end while it settles: bring a row into view twice
const into = async (p, loc) => {
  for (let i = 0; i < 2; i++) {
    await loc.evaluate((e) => e.scrollIntoView({ block: "center" }));
    await p.waitForTimeout(500);
  }
};

let failed = false;
try {
  if (!big) {
    for (const theme of ["light", "dark"]) {
      // 7: Persona's message to a task: its first line without marks, opened as Markdown
      {
        const p = await page({ path: "/persona", width: 900, height: 800, theme });
        const line = p.locator('[data-testid="sent-line"]').filter({ hasText: "先别动缓存层" }).first();
        await line.waitFor({ timeout: 20_000 });
        await into(p, line);
        const folded = await line.locator('[aria-expanded="false"]').innerText();
        check(`7 ${theme}: the folded line has no Markdown marks`, !/[*`]/.test(folded), folded);
        await line.locator('[aria-expanded="false"]').click();
        await p.waitForTimeout(500);
        const detail = line.locator('[data-testid="sent-detail"]');
        check(`7 ${theme}: the opened text is drawn as Markdown`, (await detail.locator("strong").count()) > 0 && (await detail.locator("code").count()) > 0 && !/\*\*|`/.test(await detail.innerText()));
        await line.screenshot({ path: join(shots, `item7-sent-line-${theme}.png`) });
        // the supervisor's rework note (MOCK_TASKS_SENT): written a line per point, drawn a line per point, as
        // Persona's replies are, not run together into one paragraph or one bullet
        const rework = p.locator('[data-testid="sent-line"]').filter({ hasText: "督办" }).first();
        await rework.waitFor({ timeout: 20_000 });
        await into(p, rework);
        await rework.locator('[aria-expanded="false"]').click();
        await p.waitForTimeout(500);
        const html = (await rework.locator('[data-testid="sent-detail"]').innerHTML()).replaceAll("\n", "");
        check(`7 ${theme}: the rework note keeps its lines`, html.includes("轮）<br>逐条验收：") && html.includes("只有竖屏截图<br>要你做的：<br>补一张"), html.slice(0, 200));
        await rework.screenshot({ path: join(shots, `item7-rework-${theme}.png`) });
        // 80: nothing about where the main chat runs is known on the mock: no line at all
        check(`80 ${theme}: no "not reported" line when the runtime is unknown`, (await p.locator("[data-main-runtime]").count()) === 0);
        await p.context().close();
      }
      // 80: with a report, the line names the main chat as the rest of the app does
      {
        const context = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: theme, hasTouch: true, deviceScaleFactor: 2 });
        await context.addInitScript((theme) => {
          localStorage.setItem("persona.theme", theme);
          localStorage.setItem("persona.language", "zh-Hans");
        }, theme);
        await context.route("**/*", (route) => (new URL(route.request().url()).origin === new URL(root).origin ? route.continue() : route.abort()));
        // the mock's main chat, read once: the page reads the report again on every visit and every 15 s
        const main = (await (await context.request.get(`${root}/api/v1/assistant`, { headers: { Authorization: "Bearer mock" } })).json()).main;
        await context.route("**/api/v1/assistant?view=main-runtime*", (route) =>
          route.fulfill({ json: { session_id: main, state: "running", source: "native", harness: "claude", location: { host: "my-computer" } } }));
        const p = await context.newPage();
        await p.goto(`${root}/?token=mock`);
        const badge = p.locator("[data-main-runtime]");
        await badge.waitFor({ timeout: 20_000 });
        const label = await badge.getAttribute("aria-label");
        check(`80 ${theme}: a known runtime reads 主对话 · 运行中, no engine or place`, label === "主对话 · 运行中", label);
        await p.screenshot({ path: join(shots, `item80-known-390-${theme}.png`), clip: { x: 0, y: 0, width: 390, height: 160 } });
        // where and with what it runs: in Settings › Assistant's Runs on group instead. MOCK_TASKS_SENT gives the mock's
        // MEMORY name to a session id, so its memory list is not a list there and the page's memory group would fail:
        // an empty list stands in
        await context.route("**/api/v1/assistant/memory*", (route) => route.fulfill({ json: { files: [] } }));
        await p.goto(`${root}/settings?tab=persona`);
        const where = p.locator("[data-main-runtime-place]");
        await where.waitFor({ timeout: 20_000 });
        const tool = await p.locator("[data-main-runtime-tool]").innerText();
        check(`80 ${theme}: Settings › Assistant says where and with what the main chat runs`, (await where.innerText()) === "my-computer" && tool === "Claude Code", { where: await where.innerText(), tool });
        await into(p, where);
        const group = where.locator("xpath=ancestor::section[1]");
        await ((await group.count()) ? group : where).screenshot({ path: join(shots, `item80-settings-390-${theme}.png`) });
        await context.close();
      }
      // 50: the model pill takes the width its name needs under a 600 px composer.
      for (const width of [600, 480]) {
        const p = await page({ path: WIDGET, width, height: 800, theme });
        const pill = p.locator("[data-model-pill]").first();
        await pill.locator('span[title="Opus 5.5"]:visible').waitFor({ timeout: 20_000 });
        const m = await pill.evaluate((el) => {
          const labels = [...el.querySelectorAll("span[title]")].filter((s) => s.getClientRects().length);
          const name = labels.find((label) => label.title === "Opus 5.5");
          const clippedBelow = labels.filter((label) => {
            const text = document.createRange();
            text.selectNodeContents(label);
            return text.getBoundingClientRect().bottom > label.getBoundingClientRect().bottom + 0.5;
          }).map((label) => label.textContent);
          return { composer: el.closest('[class*="@container/composer"]').clientWidth, cut: name ? name.scrollWidth > name.clientWidth : null, name: name?.textContent, overflow: el.parentElement.scrollWidth > el.parentElement.clientWidth, clippedBelow };
        });
        check(`50 ${theme} ${width}: the model's name is not cut beside free room`, m.composer < 600 && m.name === "Opus 5.5" && m.cut === false && !m.overflow, m);
        check(`11160 ${theme} ${width}: the pill labels leave room for descenders`, m.name && !m.clippedBelow.length, m.clippedBelow);
        await p.locator('[class*="@container/composer"]').first().screenshot({ path: join(shots, `item50-pill-${width}-${theme}.png`) });
        await p.context().close();
      }
      // A new chat has no Agent launcher. WIDGET explicitly runs claude and keeps its launcher and model on the phone.
      for (const path of ["/persona/new", WIDGET]) {
        const p = await page({ path, width: 390, height: 844, theme, touch: true });
        const pill = p.locator('button[data-model-pill="ready"]').first();
        await pill.waitFor({ timeout: 20_000 });
        if (path === WIDGET) {
          await pill.locator('span[title="Opus 5.5"]:visible').waitFor({ timeout: 20_000 });
          const label = await pill.locator("[data-pill-target]:visible").innerText();
          const model = await pill.locator('span[title="Opus 5.5"]:visible').innerText();
          const fits = await pill.evaluate((el) => {
            const button = el.getBoundingClientRect();
            const composer = el.closest('[class*="@container/composer"]').getBoundingClientRect();
            return button.left >= composer.left && button.right <= composer.right;
          });
          check(`50 ${theme} 390: the phone shows the explicit launcher and model inside the composer`, label === "claude" && model === "Opus 5.5" && fits, { label, model, fits });
        } else {
          const width = (await pill.boundingBox()).width;
          const engine = await pill.locator("[data-pill-engine]:visible").count();
          const chevron = await pill.locator(":scope > span > svg:last-child:visible").count();
          const words = await pill.locator("span[title]:visible").count();
          check(`50 ${theme} 390: the phone keeps the tile and chevron without an explicit launcher`, width === 64 && engine === 1 && chevron === 1 && words === 0, { width, engine, chevron, words });
        }
        await p.context().close();
      }
      // 73: a narrow window with a pointer: Tab reaches your bubble's buttons where they can be seen
      {
        const p = await page({ path: WIDGET, width: 390, height: 844, theme });
        await p.locator("[data-user-bubble]").first().waitFor({ timeout: 20_000 });
        await p.waitForTimeout(800);
        await p.locator("textarea").first().focus();
        let at = null;
        for (let i = 0; i < 80 && !at; i++) {
          await p.keyboard.press("Tab");
          at = await p.evaluate(() => {
            const a = document.activeElement;
            if (!a?.closest("[data-side-bar]")) return null;
            const r = a.getBoundingClientRect();
            return { label: a.getAttribute("aria-label"), left: Math.round(r.left), right: Math.round(r.right), place: a.closest("[data-side-bar]").getAttribute("data-side-bar") };
          });
        }
        check(`73 ${theme}: Tab reaches the bubble's first button inside the window`, at && at.left >= 0 && at.right <= 390 && at.place === "below", at);
        await p.waitForTimeout(400);
        const bubble = p.locator("[data-user-bubble]").first();
        const b = await bubble.boundingBox();
        await p.screenshot({ path: join(shots, `item73-tab-focus-${theme}.png`), clip: { x: 0, y: Math.max(0, b.y - 20), width: 390, height: Math.min(300, b.height + 90) } });
        // the pointer: over the bubble, then down onto the bar, then away
        await p.locator("textarea").first().focus();
        await p.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
        await p.waitForTimeout(400);
        const bar = p.locator("[data-user-bubble] [data-side-bar]").first();
        const r = await bar.boundingBox();
        await p.mouse.move(r.x + r.width / 2, b.y + b.height - 2);
        await p.mouse.move(r.x + r.width / 2, r.y + r.height / 2, { steps: 6 });
        await p.waitForTimeout(400);
        const shown = await bar.evaluate((e) => getComputedStyle(e).opacity);
        check(`73 ${theme}: the bar under the bubble stays while the pointer goes down to it`, shown === "1" && r.x >= 0, { opacity: shown, x: Math.round(r.x) });
        await p.screenshot({ path: join(shots, `item73-hover-${theme}.png`), clip: { x: 0, y: Math.max(0, b.y - 20), width: 390, height: Math.min(300, b.height + 90) } });
        await p.mouse.move(r.x + r.width / 2, r.y + r.height + 120, { steps: 4 });
        await p.waitForTimeout(400);
        const takes = await p.evaluate(({ x, y }) => Boolean(document.elementFromPoint(x, y)?.closest("[data-side-bar]")), { x: r.x + r.width / 2, y: r.y + r.height / 2 });
        check(`73 ${theme}: hidden again, it takes no clicks meant for what is under it`, !takes);
        await p.context().close();
      }
      // 73: a wide window: the bar stays beside the bubble, to its left, as before
      {
        const p = await page({ path: WIDGET, width: 1440, height: 900, theme });
        const bubble = p.locator("[data-user-bubble]").first();
        await bubble.waitFor({ timeout: 20_000 });
        await p.waitForTimeout(800);
        await into(p, bubble);
        const b = await bubble.boundingBox();
        await p.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
        await p.waitForTimeout(400);
        const bar = p.locator("[data-user-bubble] [data-side-bar]").first();
        const r = await bar.boundingBox();
        const place = await bar.getAttribute("data-side-bar");
        // the bubble itself: the bar's row ([data-user-bubble] also holds the column's left padding)
        const left = await bar.evaluate((e) => e.parentElement.getBoundingClientRect().left);
        const shown = await bar.evaluate((e) => getComputedStyle(e).opacity);
        check(`73 ${theme} 1440: the bar sits beside the bubble, left of it`, place === "beside" && shown === "1" && r && r.x >= 0 && r.x + r.width <= left && r.y < b.y + b.height, { place, opacity: shown, bar: r && Math.round(r.x + r.width), bubble: Math.round(left) });
        await p.screenshot({ path: join(shots, `item73-1440-${theme}.png`), clip: { x: Math.max(0, r.x - 40), y: Math.max(0, b.y - 20), width: Math.min(1440 - Math.max(0, r.x - 40), b.x + b.width - r.x + 80), height: Math.min(300, b.height + 60) } });
        await p.context().close();
      }
      // 77: on a phone, narrow columns are as narrow as their words, and the copy button is off header words
      {
        const p = await page({ path: TABLES, width: 390, height: 844, theme, touch: true });
        const first = p.locator(".md-table").first();
        await first.waitFor({ timeout: 20_000 });
        await p.waitForTimeout(800);
        const tables = await p.evaluate(() => [...document.querySelectorAll(".md-table")].map((w) => {
          const table = w.querySelector("table");
          const button = w.querySelector("[data-table-copy] button").getBoundingClientRect();
          const heads = [...table.rows[0].cells].map((th) => {
            const range = document.createRange();
            range.selectNodeContents(th);
            const r = range.getBoundingClientRect();
            return { text: th.textContent, width: Math.round(th.getBoundingClientRect().width), under: r.right > button.left && r.left < button.right && r.bottom > button.top && r.top < button.bottom };
          });
          return { scrolls: table.scrollWidth > table.clientWidth, heads };
        }));
        const narrow = tables[0].heads.find((h) => h.text === "#");
        check(`77 ${theme}: the # column is as narrow as its words`, narrow && narrow.width <= 40, narrow);
        check(`77 ${theme}: the copy button covers no header words of a table that fits`, tables.filter((t) => !t.scrolls).every((t) => t.heads.every((h) => !h.under)), tables.filter((t) => !t.scrolls).map((t) => t.heads.filter((h) => h.under).map((h) => h.text)));
        await into(p, first);
        const b = await first.boundingBox();
        await p.screenshot({ path: join(shots, `item77-tables-390-${theme}.png`), clip: { x: 0, y: Math.max(0, b.y - 20), width: 390, height: 640 } });
        await p.context().close();
      }
      // 9146: on a wide column a table takes the room its text needs, up to the column, and wraps at a reading length,
      // not at a phone's 18em; a short code chip in a cell is never broken across lines. On a phone it still fits.
      for (const [width, height] of [[1440, 900], [390, 844]]) {
        const p = await page({ path: CHIPS, width, height, theme, touch: width < 600 });
        const wrap = p.locator(".md-table").first();
        await wrap.waitFor({ timeout: 20_000 });
        await p.waitForTimeout(800);
        const m = await p.evaluate(() => {
          const table = document.querySelector(".md-table table");
          const cells = [...table.querySelectorAll("td:not([data-fit])")].map((td) => {
            const cs = getComputedStyle(td);
            const r = td.getBoundingClientRect();
            const text = r.height - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom) - parseFloat(cs.borderBottomWidth);
            return { lines: Math.round(text / parseFloat(cs.lineHeight)), width: Math.round(r.width), em: parseFloat(cs.fontSize) };
          });
          const chips = [...table.querySelectorAll("code[data-chip]")].map((c) => ({ text: c.textContent, rects: c.getClientRects().length }));
          return {
            table: Math.round(table.getBoundingClientRect().width),
            column: Math.round(table.closest(".prose-j").getBoundingClientRect().width),
            scrolls: table.scrollWidth > table.clientWidth,
            page: document.scrollingElement.scrollWidth,
            cells,
            chips,
          };
        });
        const em = m.cells[0]?.em ?? 13;
        if (width > 600) {
          check(`9146 ${theme} 1440: the table is no wider than the column and does not scroll`, m.table <= m.column && !m.scrolls, { table: m.table, column: m.column });
          check(`9146 ${theme} 1440: a long cell is wider than the old 18em cap`, m.cells[0].width > 18 * em + 20, m.cells[0]);
          check(`9146 ${theme} 1440: each long cell takes at most two lines`, m.cells.every((c) => c.lines <= 2), m.cells);
        } else {
          check(`9146 ${theme} 390: the table stays in the page (it fits or scrolls inside itself)`, m.page <= 390 && (m.table <= m.column || m.scrolls), { table: m.table, column: m.column, page: m.page });
          check(`9146 ${theme} 390: long cells keep their 7em`, m.cells.every((c) => c.width >= 7 * em), m.cells);
        }
        check(`9146 ${theme} ${width}: the short chips are marked and none is broken across lines`, m.chips.length >= 6 && m.chips.every((c) => c.rects === 1), m.chips);
        await into(p, wrap);
        const b = await wrap.boundingBox();
        const x = Math.max(0, b.x - 24);
        await p.screenshot({ path: join(shots, `item9146-${width}-${theme}.png`), clip: { x, y: Math.max(0, b.y - 60), width: Math.min(width - x, b.width + 48), height: Math.min(height, b.height + 100) } });
        await p.context().close();
      }
    }
  } else {
    // 90: a fold of 650 commands opens with a screenful at once, the rest in idle batches; the header stays put, and a
    // search hit deep inside opens it with its row there to land on
    for (const theme of ["light", "dark"]) {
      const p = await page({ path: BIG, width: 1440, height: 900, theme });
      const head = p.locator("[data-work-group] > button").first();
      await head.waitFor({ timeout: 30_000 });
      await p.waitForTimeout(1500);
      const y0 = Math.round((await head.boundingBox()).y);
      // the rows drawn in each frame for two seconds after the tap (the folded steps are fetched first, then drawn)
      const frames = p.evaluate(() => new Promise((done) => {
        const counts = [];
        const end = performance.now() + 2000;
        const tick = () => {
          counts.push(document.querySelectorAll("[data-work-detail] [data-action-row]").length);
          if (performance.now() < end) requestAnimationFrame(tick);
          else done(counts);
        };
        requestAnimationFrame(tick);
      }));
      await head.click();
      const counts = await frames;
      const most = Math.max(...counts.map((n, i) => n - (counts[i - 1] ?? 0)));
      check(`90 ${theme}: no frame draws more than a screenful of rows at once`, most > 0 && most <= 60, { most, frames: counts.length });
      await p.waitForFunction(() => document.querySelectorAll("[data-work-detail] [data-action-row]").length === 650, null, { timeout: 30_000 });
      const y1 = Math.round((await head.boundingBox()).y);
      check(`90 ${theme}: every step is drawn after it, the header where it was tapped`, y1 === y0, { y0, y1 });
      await p.screenshot({ path: join(shots, `item90-opened-${theme}.png`) });
      await p.context().close();
    }
    const p = await page({ path: BIG, width: 1440, height: 900, theme: "light" });
    await p.locator("[data-work-group] > button").first().waitFor({ timeout: 30_000 });
    await p.waitForTimeout(1000);
    await p.locator('button[aria-label="更多操作"]').first().click();
    await p.getByRole("menuitem", { name: "更多", exact: true }).click();
    await p.getByText("会话内搜索", { exact: true }).last().click();
    await p.locator("[data-chat-search] input").fill("make step-600");
    await p.waitForTimeout(2500);
    await p.locator("[data-chat-search] input").press("Enter");
    const hit = await p.waitForFunction(() => document.querySelector("[data-search-hit]")?.textContent, null, { timeout: 10_000 }).then((h) => h.jsonValue(), () => "");
    check("90: a search hit deep in the closed fold lands on its row", String(hit).includes("make step-600"), String(hit).slice(0, 40));
    await p.screenshot({ path: join(shots, "item90-search-hit-light.png") });
    await p.context().close();
  }
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  clearTimeout(watchdog);
}
writeFileSync(join(shots, big ? "results-big.json" : "results.json"), JSON.stringify(results, null, 2));
const bad = results.filter((r) => !r.ok);
assert.ok(results.length > 0 || failed);
if (failed || bad.length) {
  console.error(`RESULT: chat-content failed (${bad.length} of ${results.length} checks${failed ? ", and an error" : ""})`);
  process.exit(1);
}
console.log(`RESULT: chat-content passed (${results.length} checks${big ? ", --big" : ""})`);
