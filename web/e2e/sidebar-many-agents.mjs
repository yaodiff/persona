// The complete Agents list with hundreds of Agents, in a real browser: folded groups hold no rows, an open long group
// draws a bounded number of them yet every row is there when scrolled or tabbed to, its heading still sticks, and the
// open Agent's row is drawn and brought into view wherever it is; By machine, a workspace of 400 tabs too. Prints the
// main thread's long tasks at 4x CPU.
// Usage (README "The complete Agents list with hundreds of Agents"):
//   node web/e2e/sidebar-many-agents.mjs <loopback-web-origin> <artifact-directory>
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [web, output] = process.argv.slice(2);
assert(/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(web ?? "") && output, "usage: sidebar-many-agents.mjs <loopback-web-origin> <artifact-directory>");
mkdirSync(output, { recursive: true });
// the list's metrics (web/src/ui/list-metrics.ts windowHead / windowPart)
const HEAD = 30, PART = 20;
// the first rows and the parts around the open row (the list's last row, always drawn, comes on top where it counts)
const BOUND = HEAD + 3 * PART;
const results = [];
let browser;
const watchdog = setTimeout(() => {
  console.log("SIDEBAR_MANY_AGENTS TIMEOUT after 4 minutes");
  void browser?.close();
  process.exit(1);
}, 4 * 60_000);

function check(name, ok, detail = "") {
  results.push({ name, ok, detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail ? ` (${detail})` : ""}`);
}

async function open({ width, height, theme = "dark", unfold = false, grouping = "todo", folds = {} }) {
  const touch = width < 700;
  const context = await browser.newContext({ viewport: { width, height }, hasTouch: touch, colorScheme: theme, locale: "zh-CN" });
  await context.addInitScript(({ theme, unfold, grouping, folds }) => {
    localStorage.setItem("persona.token", "mock");
    localStorage.setItem("persona.language", "zh-Hans");
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.agentGrouping", grouping);
    localStorage.setItem("fold:todo:none", unfold ? "0" : "1");
    for (const [k, v] of Object.entries(folds)) localStorage.setItem(`fold:${k}`, v);
    window.__longtasks = [];
    try {
      new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__longtasks.push(Math.round(e.duration)); }).observe({ type: "longtask", buffered: true });
    } catch { /* no long tasks in this browser */ }
  }, { theme, unfold, grouping, folds });
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });
  return { context, page };
}

const group = (scope) => `${scope} [data-tree-branch="todo:none"]`;
const settle = (page) => page.waitForTimeout(1200);

try {
  browser = await chromium.launch({ channel: "chrome", headless: true });

  // ── the phone: Overview with the drawer's sidebar, then the Agents page ──
  {
    const { context, page } = await open({ width: 390, height: 844 });
    await page.goto(`${web}/overview`);
    await page.waitForSelector(group("aside"), { state: "attached", timeout: 60_000 });
    await settle(page);
    const aside = await page.evaluate(() => ({ nodes: document.querySelectorAll("aside *").length, inert: document.querySelectorAll("aside [inert] a").length }));
    check("a folded group holds no rows (phone sidebar)", aside.inert === 0, `aside [inert] a: ${aside.inert}`);
    check("the phone sidebar stays small with 400 Agents", aside.nodes < 2000, `${aside.nodes} elements`);
    const overviewTasks = await page.evaluate(() => window.__longtasks.slice());
    await page.goto(`${web}/agents`);
    await page.waitForSelector(group("main"), { timeout: 60_000 });
    await settle(page);
    const agentsTasks = await page.evaluate(() => window.__longtasks.slice());
    const folded = await page.evaluate(() => ({ nodes: document.querySelectorAll("main *").length, rows: document.querySelectorAll('main [data-tree-branch="todo:none"] a[href^="/s/"]').length }));
    check("the Agents page with Not on a to-do folded draws none of its rows", folded.rows === 0, `${folded.rows} rows, ${folded.nodes} elements`);
    await page.locator(`${group("main")} button[aria-expanded]`).first().click();
    await settle(page);
    const rows = () => page.evaluate(() => [...document.querySelectorAll('main [data-tree-branch="todo:none"] a[href^="/s/"]')].map((a) => a.textContent));
    const opened = await rows();
    check("opened, it draws a bounded number of its 400 rows", opened.length >= HEAD && opened.length <= BOUND, `${opened.length} rows`);
    await page.screenshot({ path: join(output, "agents-open-phone.png") });
    // the middle: the heading sticks, and what is on screen is rows, not an empty box
    const middle = await page.evaluate(async () => {
      const scroller = document.querySelector("main [data-row-scroller]");
      scroller.scrollTop = scroller.scrollHeight / 2;
      await new Promise((r) => setTimeout(r, 1200));
      const box = scroller.getBoundingClientRect();
      // stuck where its sticky top puts it: under the Agents controls, which stick above it
      const headingEl = document.querySelector('main [data-tree-branch="todo:none"] [data-sidebar-group-heading]');
      const heading = headingEl.getBoundingClientRect();
      const stickyTop = Number.parseFloat(getComputedStyle(headingEl).top);
      const probes = [0.35, 0.5, 0.65, 0.8].map((f) => document.elementFromPoint(box.left + box.width / 2, box.top + box.height * f));
      // a point between two rows is the list's own box: only a part's empty box is not rows
      return { stuck: Math.abs(heading.top - (box.top + stickyTop)) < 2, at: `${Math.round(heading.top - box.top)} px from the top, sticky top ${stickyTop} px`, onRows: probes.every((el) => el?.closest('[data-tree-branch="todo:none"]') && !el.closest("[data-rows-gap]")) };
    });
    check("scrolled into the middle, the group's heading sticks under the controls", middle.stuck, middle.at);
    check("scrolled into the middle, the view shows rows, not empty boxes", middle.onRows);
    await page.screenshot({ path: join(output, "agents-middle-phone.png") });
    const end = await page.evaluate(async () => {
      const scroller = document.querySelector("main [data-row-scroller]");
      scroller.scrollTop = scroller.scrollHeight;
      await new Promise((r) => setTimeout(r, 1200));
      // the list's last row is always drawn: the part before it is the one at the end of the view
      const parts = document.querySelectorAll('main [data-tree-branch="todo:none"] [data-rows-part]');
      return { lastDrawn: parts.length > 1 && !parts[parts.length - 2].hasAttribute("data-rows-gap"), rows: document.querySelectorAll('main [data-tree-branch="todo:none"] a[href^="/s/"]').length };
    });
    check("at the end, the last rows are drawn and the count stays bounded", end.lastDrawn && end.rows <= BOUND + PART, `${end.rows} rows`);
    console.log(`long tasks (ms) at 4x CPU, cold /overview: [${overviewTasks.join(", ")}], then /agents: [${agentsTasks.slice(overviewTasks.length).join(", ")}]`);
    await context.close();
  }

  // ── a wide screen: the sidebar's long group walked with Tab, and an open Agent deep in it ──
  {
    const { context, page } = await open({ width: 1280, height: 800, theme: "light", unfold: true });
    await page.goto(`${web}/overview`);
    // a part's mark is hidden: attached is all it can be
    await page.waitForSelector(`${group("aside")} [data-rows-part]`, { state: "attached", timeout: 60_000 });
    await settle(page);
    const shown = await page.evaluate(() => document.querySelectorAll('aside [data-tree-branch="todo:none"] a[href^="/s/"]').length);
    check("the sidebar's open long group draws a bounded number of rows", shown <= BOUND, `${shown} rows`);
    // Tab from the last of the first rows on: every row comes in turn, none skipped where a part begins
    await page.locator('aside [data-tree-branch="todo:none"] a[href^="/s/"]').nth(HEAD - 1).focus();
    const walked = [];
    for (let i = 0; i < 2 * (PART * 2 + 4); i++) {
      await page.keyboard.press("Tab");
      await page.waitForTimeout(40);
      const text = await page.evaluate(() => (document.activeElement?.matches('a[href^="/s/"]') ? document.activeElement.textContent : null));
      if (text) walked.push(text);
    }
    const numbers = walked.map((x) => Number(/Session (\d{3})/.exec(x)?.[1]));
    const steady = numbers.length > PART && numbers.every((n, i) => Number.isFinite(n) && (i === 0 || n === numbers[i - 1] + 1));
    check("Tab walks the rows in order across the parts", steady, `${numbers[0]}…${numbers[numbers.length - 1]}, ${numbers.length} rows${steady ? "" : `: ${walked.join(" | ")}`}`);
    // the open Agent's row, deep in the group, is drawn and brought into view
    // the mock's Session 330 (MOCK_SESSIONS_MANY), far below the first rows
    const target = "/s/server/e4410a00-0000-4000-8000-000000000330";
    check("Session 330 starts out of the drawn rows", !(await page.locator(`aside a[href="${target}"]`).count()));
    await page.evaluate((to) => { history.pushState(null, "", to); dispatchEvent(new PopStateEvent("popstate")); }, target);
    await page.waitForTimeout(1500);
    const reveal = await page.evaluate((to) => {
      const row = document.querySelector(`aside a[href="${to}"]`);
      const scroller = document.querySelector("aside [data-row-scroller]");
      if (!row || !scroller) return { drawn: Boolean(row), inView: false };
      const r = row.getBoundingClientRect(), b = scroller.getBoundingClientRect();
      return { drawn: true, inView: r.top >= b.top && r.bottom <= b.bottom };
    }, target);
    check("the open Agent's row deep in the group is drawn and in view", reveal.drawn && reveal.inView, target.split("/").pop());
    await page.screenshot({ path: join(output, "sidebar-open-agent-wide.png") });
    await context.close();
  }

  // ── a wide screen: an Agent deep in a folded group opened from elsewhere; the group opens for it and its row comes into view ──
  {
    const { context, page } = await open({ width: 1280, height: 800 });
    await page.goto(`${web}/overview`);
    await page.waitForSelector(group("aside"), { state: "attached", timeout: 60_000 });
    await page.waitForTimeout(3000);
    const target = "/s/server/e4410a00-0000-4000-8000-000000000330";
    await page.evaluate((to) => { history.pushState(null, "", to); dispatchEvent(new PopStateEvent("popstate")); }, target);
    await page.waitForTimeout(2000);
    const reveal = await page.evaluate((to) => {
      const row = document.querySelector(`aside a[href="${to}"]`);
      const scroller = document.querySelector("aside [data-row-scroller]");
      const rows = document.querySelectorAll('aside [data-tree-branch="todo:none"] a[href^="/s/"]').length;
      if (!row || !scroller) return { drawn: Boolean(row), inView: false, rows };
      const r = row.getBoundingClientRect(), b = scroller.getBoundingClientRect();
      return { drawn: true, inView: r.top >= b.top && r.bottom <= b.bottom, rows };
    }, target);
    check("an Agent opened deep in a folded group: the group opens and its row is in view", reveal.drawn && reveal.inView && reveal.rows <= BOUND + 2 * PART + 1, `${reveal.rows} rows drawn`);
    await page.screenshot({ path: join(output, "sidebar-folded-open-agent-wide.png") });
    await context.close();
  }

  // ── By machine: the mock's 400 Agents are tabs of one workspace (fixture), open, on the phone ──
  {
    const tabs = (scope) => `${scope} a[href^="/t/server/"]`;
    const { context, page } = await open({ width: 390, height: 844, grouping: "machine", folds: { "tree:server": "0", "tree:server/fixture": "0" } });
    await page.goto(`${web}/overview`);
    await page.waitForSelector(tabs("aside"), { state: "attached", timeout: 60_000 });
    await settle(page);
    const aside = await page.evaluate((sel) => ({ nodes: document.querySelectorAll("aside *").length, tabs: document.querySelectorAll(sel).length, gaps: document.querySelectorAll("aside [data-rows-gap]").length }), tabs("aside"));
    check("By machine, a workspace of 400 tabs draws a bounded number of them (phone sidebar)", aside.tabs <= BOUND + PART && aside.gaps > 0, `${aside.tabs} tab rows, ${aside.gaps} empty boxes, ${aside.nodes} elements`);
    check("By machine, the phone sidebar stays small with the workspace open", aside.nodes < 3000, `${aside.nodes} elements`);
    const before = await page.evaluate(() => window.__longtasks.length);
    await page.evaluate(() => { history.pushState(null, "", "/agents"); dispatchEvent(new PopStateEvent("popstate")); });
    await page.waitForSelector(tabs("main"), { timeout: 60_000 });
    await settle(page);
    const main = await page.evaluate((sel) => ({ nodes: document.querySelectorAll("main *").length, tabs: document.querySelectorAll(sel).length }), tabs("main"));
    check("By machine, the Agents page draws a bounded number of the workspace's tabs", main.tabs <= BOUND + PART, `${main.tabs} tab rows, ${main.nodes} elements`);
    await page.screenshot({ path: join(output, "agents-machine-phone.png") });
    await page.evaluate(() => { history.pushState(null, "", "/overview"); dispatchEvent(new PopStateEvent("popstate")); });
    await settle(page);
    const switched = await page.evaluate((n) => window.__longtasks.slice(n), before);
    console.log(`long tasks (ms) at 4x CPU, By machine with the workspace open, /overview → /agents → /overview: [${switched.join(", ")}]`);
    await context.close();
  }
} catch (error) {
  check("the run finished", false, String(error?.message ?? error));
} finally {
  await browser?.close();
  clearTimeout(watchdog);
}
writeFileSync(join(output, "results.json"), JSON.stringify(results, null, 2));
const failed = results.filter((r) => !r.ok);
console.log(failed.length ? `SIDEBAR_MANY_AGENTS FAIL: ${failed.length} of ${results.length} checks` : `SIDEBAR_MANY_AGENTS PASS: ${results.length} checks`);
process.exit(failed.length ? 1 : 0);
