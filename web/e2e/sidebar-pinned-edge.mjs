// End-to-end check of the sidebar's pinned group headings (the web usability audit of 2026-11-14, audit item 51): on an
// Agent's page, answering the dock's last waiting card shortens the open row, the list (scrolled to its end) moves down,
// and Persona chats' "Show N more" row stops half under the pinned "Persona 对话" heading. The heading must stay on top
// of every row that passes under it, and while it is pinned it draws the Persona row's divider under it, so the row
// reads as scrolled beneath it; at rest no heading draws one.
//
// Needs the web page against a freshly started fake hub (answering the card changes its state), and Chrome:
//
//   MOCK_RECENT=1 MOCK_MANAGERS=1 MOCK_TASKS=1 MOCK_LEADERS=1 PORT=18791 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18791 npx vite --port 5191 --strictPort &)
//   node web/e2e/sidebar-pinned-edge.mjs http://127.0.0.1:5191 [--shots <dir>]
//
// Runs at 1440 × 900 in Chinese, light then dark (the card is answered in the first run only). Prints one line per check
// and a verdict; exits 1 on a failure.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const origin = args[0];
const shots = args.includes("--shots") ? args[args.indexOf("--shots") + 1] : undefined;
if (!/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin ?? "")) {
  console.log("FAIL sidebar-pinned-edge: give the web page's loopback origin");
  process.exit(1);
}
if (shots) mkdirSync(shots, { recursive: true });
const SESSION = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000002";
let failed = 0;
const check = (ok, what) => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) failed++;
};

/** The "Show N more" row against the "Persona 对话" heading: where each is, what is topmost where they overlap, and
 *  which headings draw the edge. */
const measure = (page) => page.evaluate(() => {
  const nav = document.querySelector("[data-sidebar-rows]");
  const heading = [...nav.querySelectorAll("[data-sidebar-group-heading]")].find((h) => /Persona 对话/.test(h.textContent));
  const row = [...nav.querySelectorAll("button")].find((b) => /再显示/.test(b.textContent));
  const r = row.getBoundingClientRect(), h = heading.getBoundingClientRect();
  let rowOnTop = 0, overlap = 0;
  for (let y = Math.max(r.top, h.top) + 0.5; y < Math.min(r.bottom, h.bottom); y += 1)
    for (let x = r.left + 4; x < r.right - 4; x += 6) {
      overlap++;
      if (row.contains(document.elementFromPoint(x, y))) rowOnTop++;
    }
  const line = getComputedStyle(heading, "::after");
  return {
    scrollTop: nav.scrollTop, row: [r.top, r.bottom], heading: [h.top, h.bottom], overlap, rowOnTop,
    edge: heading.hasAttribute("data-edge"), line: line.content === "none" ? "" : `${line.height} ${line.backgroundColor}`,
    edges: [...nav.querySelectorAll("[data-sidebar-group-heading][data-edge]")].length,
  };
});
const settle = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 50)))));

async function run(browser, theme, answer) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN", colorScheme: theme });
  await context.addInitScript((th) => { localStorage.setItem("persona.theme", th); localStorage.setItem("persona.lang", "zh-Hans"); }, theme);
  const page = await context.newPage();
  await page.goto(`${origin}${SESSION}?token=mock`);
  await page.locator("[data-sidebar-rows] button", { hasText: "再显示" }).waitFor();
  await page.waitForTimeout(1500);
  if (answer) {
    const option = page.getByRole("button", { name: /Swift Testing/ }).first();
    if (await option.count()) {
      await option.click();
      await page.getByRole("button", { name: "发送回答" }).click();
      await page.getByRole("button", { name: /^允许$/ }).first().click({ timeout: 10_000 });
      await page.waitForTimeout(2500);
    } else console.log("note the card was already answered: start the fake hub afresh to check the live update");
  }
  const m = await measure(page);
  check(m.overlap === 0 || m.rowOnTop === 0, `${theme}: the heading stays on top of the row half under it (scrollTop ${m.scrollTop}, row ${m.row.map(Math.round)}, heading ${m.heading.map(Math.round)})`);
  check(!m.overlap || (m.edge && m.line.startsWith("1px")), `${theme}: the pinned heading draws its 1 px edge (${m.line || "none"})`);
  if (shots) {
    const box = await page.locator("aside").first().boundingBox();
    await page.screenshot({ path: join(shots, `sidebar-pinned-edge-${theme}.png`), clip: { x: 0, y: 90, width: Math.ceil(box.width), height: 160 } });
  }
  // the row passes under the heading a pixel at a time: never drawn over it
  const start = m.scrollTop - (m.row[0] - m.heading[1]) - 8;
  let worst = 0, noEdge = 0;
  for (let d = 0; d <= 40; d++) {
    await page.evaluate((top) => { document.querySelector("[data-sidebar-rows]").scrollTop = top; }, start + d);
    await settle(page);
    const s = await measure(page);
    worst = Math.max(worst, s.rowOnTop);
    if (s.overlap && !s.edge) noEdge++;
  }
  check(worst === 0, `${theme}: scrolled under the heading a pixel at a time, the row is never on top`);
  check(noEdge === 0, `${theme}: and the heading draws its edge whenever the row is under it`);
  await page.evaluate(() => { document.querySelector("[data-sidebar-rows]").scrollTop = 0; });
  await settle(page);
  check((await measure(page)).edges === 0, `${theme}: at the list's top no heading draws an edge`);
  await context.close();
}

const browser = await chromium.launch({ channel: "chrome", headless: true });
const deadline = setTimeout(() => {
  console.log("FAIL sidebar-pinned-edge: timed out after 3 minutes");
  process.exit(1);
}, 3 * 60_000);
try {
  await run(browser, "light", true);
  await run(browser, "dark", false);
} finally {
  clearTimeout(deadline);
  await browser.close();
}
console.log(failed ? `FAIL sidebar-pinned-edge: ${failed} check(s) failed` : "PASS sidebar-pinned-edge");
process.exit(failed ? 1 : 0);
