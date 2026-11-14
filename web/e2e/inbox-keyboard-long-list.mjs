// Inbox keyboard, layout and long-list checks in a real browser (web usability audit items 15, 16, 34, 60, 71, 74, 84).
// Usage: node web/e2e/inbox-keyboard-long-list.mjs <loopback-web-origin> <artifact-directory>
// The origin is an exclusively owned web server (Vite, or `vite preview` of a production build for the timings) whose hub
// is scripts/mock/mock_hub.py started with MOCK_INBOX_EXTRA=300 MOCK_INBOX_HISTORY=busy. Only the mock's invented data;
// the read failure is answered in the browser.
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import assert from "node:assert/strict";
import { chromium } from "playwright-core";

const [origin, output] = process.argv.slice(2);
if (!origin || !output || !/^http:\/\/(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$/.test(origin)) {
  console.error("usage: node web/e2e/inbox-keyboard-long-list.mjs <loopback-web-origin> <artifact-directory>");
  process.exit(2);
}
await mkdir(output, { recursive: true });
let browser;
const deadline = setTimeout(() => {
  console.error("INBOX_KEYBOARD_LONG_LIST TIMEOUT");
  void browser?.close();
  process.exit(1);
}, 240_000);

const results = {};
const errors = [];
const lark = "lark:f1:100";

async function page(theme, { width = 1280, height = 820, touch = false, setup } = {}) {
  const context = await browser.newContext({ viewport: { width, height }, hasTouch: touch, isMobile: touch, locale: "en-US", colorScheme: theme });
  await context.addInitScript(([th]) => {
    localStorage.setItem("persona.token", "mock");
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", th);
    for (const k of ["inbox.source", "inbox.kind", "inbox.folder", "inbox.grouping"]) localStorage.removeItem(k);
    // how many rows the list had the first time it drew any, and the long tasks from the start
    window.__firstRows = 0;
    window.__long = [];
    new PerformanceObserver((list) => { for (const e of list.getEntries()) window.__long.push(Math.round(e.duration)); }).observe({ type: "longtask", buffered: true });
    const seen = new MutationObserver(() => {
      const n = document.querySelectorAll("main [data-item]").length;
      if (n) { window.__firstRows = n; seen.disconnect(); }
    });
    seen.observe(document, { childList: true, subtree: true });
  }, [theme]);
  const p = await context.newPage();
  p.on("pageerror", (e) => errors.push(e.message));
  if (setup) await setup(p);
  return { context, p };
}
const active = (p) => p.evaluate(() => {
  const a = document.activeElement;
  return !a || a === document.body ? "BODY" : `${a.tagName}|${a.getAttribute("aria-label") ?? ""}|${(a.textContent ?? "").trim().slice(0, 20)}`;
});
const shot = (p, name) => p.screenshot({ path: join(output, `${name}.png`) });

try {
  browser = await chromium.launch({ channel: "chrome", headless: true });
  for (const theme of ["light", "dark"]) {
    // audit item 15: chips that leave nothing are not "All caught up"; Clear filters brings the list back
    {
      const { context, p } = await page(theme);
      await p.goto(`${origin}/inbox`);
      const bar = p.getByRole("toolbar", { name: "Filters" });
      await bar.getByRole("button", { name: "Lark", exact: true }).click();
      await bar.getByRole("button", { name: "Mail", exact: true }).click();
      const empty = p.locator("[data-inbox-filtered-empty]");
      await empty.waitFor();
      assert.equal(await p.locator("main .bg-green-tint").count(), 0, "no green check under filters");
      const bulk = (await p.locator("[data-clean-all]").textContent()).trim();
      assert.ok(!/Nothing to do/.test(bulk), `Clean up all claims nothing is left: ${bulk}`);
      results[`15-${theme}`] = { empty: (await empty.textContent()).trim(), bulk };
      await shot(p, `15-filtered-empty-${theme}`);
      await empty.getByRole("button", { name: "Clear filters" }).click();
      await p.locator("main [data-item]").first().waitFor();
      assert.equal(await empty.count(), 0);
      // the button went with the empty state: the keyboard is on All, the chip that now says what the list shows
      assert.equal(await active(p), "BUTTON||All");
      await context.close();
    }
    // audit item 84: a failed read says nothing about what is left, and its line has its own Retry
    {
      const { context, p } = await page(theme, { setup: (pg) => pg.route(/\/api\/v1\/inbox(\?|$)/, (r) => r.fulfill({ status: 500, contentType: "application/json", body: '{"error":"boom"}' })) });
      await p.goto(`${origin}/inbox`);
      const line = p.locator("[data-inbox-read-failed], [data-inbox-failed]").first();
      await line.waitFor();
      assert.equal(await line.getByRole("button", { name: "Retry" }).count(), 1);
      assert.equal((await p.locator("[data-clean-all]").textContent()).trim(), "Clean up all");
      await shot(p, `84-read-failed-${theme}`);
      await context.close();
    }
    // audit item 60: the time follows where the item came from, not the line's far end
    {
      const { context, p } = await page(theme);
      await p.goto(`${origin}/inbox/${encodeURIComponent(lark)}`);
      const meta = p.locator("[data-detail-meta]");
      await meta.waitFor();
      const box = await meta.evaluate((el) => {
        const [words, time] = [...el.children].map((c) => c.getBoundingClientRect());
        return { wordsRight: Math.round(words.right), timeLeft: Math.round(time.left), timeRight: Math.round(time.right), lineRight: Math.round(el.getBoundingClientRect().right) };
      });
      assert.ok(Math.abs(box.timeLeft - box.wordsRight) <= 1, `time right after the words: ${JSON.stringify(box)}`);
      assert.ok(box.lineRight - box.timeRight > 40, `time not pushed to the line's end: ${JSON.stringify(box)}`);
      results[`60-${theme}`] = box;
      await shot(p, `60-detail-meta-${theme}`);
      await context.close();
    }
    // audit item 16: Edit puts the caret at the end of the draft
    {
      const { context, p } = await page(theme);
      await p.goto(`${origin}/inbox`);
      await p.locator(`[data-item="${lark}"]`).getByRole("button", { name: "Edit", exact: true }).click();
      const area = p.getByRole("dialog").locator("textarea");
      await area.waitFor();
      await p.waitForFunction(() => document.activeElement?.tagName === "TEXTAREA");
      const caret = await area.evaluate((el) => [el.selectionStart, el.value.length]);
      assert.equal(caret[0], caret[1], "caret at the end");
      await p.keyboard.type(" XYZ");
      assert.ok((await area.inputValue()).endsWith(" XYZ"));
      await shot(p, `16-draft-focus-${theme}`);
      await context.close();
    }
    // audit item 71: Select / Done keep the keyboard
    {
      const { context, p } = await page(theme);
      await p.goto(`${origin}/inbox`);
      await p.locator("main [data-item]").first().waitFor();
      await p.getByRole("button", { name: "Select", exact: true }).focus();
      await p.keyboard.press("Enter");
      assert.match(await active(p), /^BUTTON\|\|Done$/);
      await shot(p, `71-select-focus-${theme}`);
      await p.keyboard.press("Enter");
      assert.match(await active(p), /^BUTTON\|\|Select$/);
      await p.keyboard.press("Enter");
      await p.locator('main [role="checkbox"]').first().focus();
      await p.keyboard.press("Escape");
      assert.match(await active(p), /^BUTTON\|\|Select$/);
      results[`71-${theme}`] = "focus kept";
      await context.close();
    }
    // audit item 74 (the Inbox's part): a narrow page by keyboard
    {
      const { context, p } = await page(theme, { width: 390, height: 844 });
      await p.goto(`${origin}/inbox`);
      const link = p.locator(`[data-item="${lark}"] > a`);
      await link.waitFor();
      await link.focus();
      await p.keyboard.press("Enter");
      await p.waitForURL(/\/inbox\/lark/);
      await p.waitForTimeout(300);
      // the item's title, with its focus ring
      assert.match(await active(p), /^H2\|\|/);
      assert.equal(await p.evaluate(() => document.activeElement.matches(":focus-visible")), true, "the title shows its ring");
      await shot(p, `74-opened-390-${theme}`);
      await p.keyboard.press("Escape");
      await p.waitForURL(/\/inbox$/);
      await p.waitForTimeout(300);
      assert.equal(await p.evaluate((id) => document.activeElement === document.querySelector(`[data-item="${id}"] > a`), lark), true, "Esc: back on the row");
      await shot(p, `74-back-on-row-390-${theme}`);
      await p.keyboard.press("Enter");
      await p.waitForURL(/\/inbox\/lark/);
      await p.goBack();
      await p.waitForURL(/\/inbox$/);
      await p.waitForTimeout(300);
      assert.equal(await p.evaluate((id) => document.activeElement === document.querySelector(`[data-item="${id}"] > a`), lark), true, "Back: on the row");
      results[`74-${theme}`] = "focus follows";
      await context.close();
    }
  }
  // audit item 74, and what it must not do: Done in an opened item's header files the item under Done. With Done open the
  // list jumped there after it; now it stays where it was, and on a narrow page the focus lands on the row now in the
  // item's place (a wide page keeps the focus where it was before the narrow page's rule)
  for (const [theme, width] of [["light", 1280], ["light", 390], ["dark", 1280], ["dark", 390]]) {
    const { context, p } = await page(theme, { width, height: width > 400 ? 820 : 844 });
    await p.goto(`${origin}/inbox`);
    await p.waitForFunction(() => document.querySelectorAll("main [data-item]").length > 300, null, { timeout: 60_000 });
    await p.locator('[data-section="done"] button[aria-expanded="false"]').first().click();
    const open = p.locator('main [data-section]:not([data-section="done"]) [data-item]');
    const [id, next] = await Promise.all([open.nth(120).getAttribute("data-item"), open.nth(121).getAttribute("data-item")]);
    const link = p.locator(`main [data-item="${id}"] > a`);
    await link.scrollIntoViewIfNeeded();
    const scroller = p.locator("[data-inbox-list-scroll]");
    const before = await scroller.evaluate((el) => el.scrollTop);
    const done = p.locator('section[aria-label="Details"]').getByRole("button", { name: "Done", exact: true });
    if (width > 400) {
      await link.click();
      await done.click();
    } else {
      await link.focus();
      await p.keyboard.press("Enter");
      await done.focus();
      await p.keyboard.press("Enter");
    }
    await p.waitForURL(/\/inbox$/);
    await p.locator(`[data-section="done"] [data-item="${id}"]`).waitFor();
    await p.waitForTimeout(400);
    const after = await scroller.evaluate((el) => el.scrollTop);
    const at = await p.evaluate(() => ({ row: document.activeElement?.closest("[data-item]")?.getAttribute("data-item") ?? null, done: Boolean(document.activeElement?.closest('[data-section="done"]')) }));
    assert.ok(Math.abs(after - before) < 100, `the list stays where it was at ${width}: ${before} → ${after}`);
    assert.equal(at.done, false, `nothing in Done has the focus at ${width}`);
    if (width < 400) assert.equal(at.row, next, "a narrow page: the focus is on the row now in the item's place");
    results[`74-done-${width}-${theme}`] = { before, after, focus: at.row };
    await shot(p, `74-done-${width}-${theme}`);
    await context.close();
  }
  // audit item 34: a phone opening a long Inbox draws its first rows first, the rest after (CPU 4x)
  {
    const { context, p } = await page("dark", { width: 390, height: 844, touch: true });
    const cdp = await context.newCDPSession(p);
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });
    await p.goto(`${origin}/inbox`);
    await p.locator("main [data-item]").first().waitFor({ timeout: 60_000 });
    let rows = 0;
    for (let i = 0; i < 40; i++) {
      await p.waitForTimeout(250);
      const n = await p.locator("main [data-item]").count();
      if (n === rows && n > 0) break;
      rows = n;
    }
    const m = await p.evaluate(() => ({ firstRows: window.__firstRows, rows: document.querySelectorAll("main [data-item]").length, mainNodes: document.querySelectorAll("main *").length, longTasks: window.__long }));
    assert.ok(m.rows > 300, `every row comes in the end: ${m.rows}`);
    assert.ok(m.firstRows <= 100, `first drawn with ${m.firstRows} rows`);
    results["34-cold-390-cpu4"] = m;
    await shot(p, "34-long-list-390-dark");
    await context.close();
  }
  assert.deepEqual(errors, []);
  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2));
  console.log(JSON.stringify(results, null, 2));
  console.log("INBOX_KEYBOARD_LONG_LIST PASS");
} catch (error) {
  await writeFile(join(output, "results.json"), JSON.stringify({ results, errors, failure: String(error?.stack ?? error) }, null, 2)).catch(() => {});
  console.error(error);
  console.error("INBOX_KEYBOARD_LONG_LIST FAIL");
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser?.close();
}
