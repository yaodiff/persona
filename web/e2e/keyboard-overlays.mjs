// End-to-end check of the keyboard around the web app's overlays (docs/web/web.md "Keyboard shortcuts", "Node menu"):
//
// - the session ⋯ menu uses the window's height: whole at 1280 × 820; at 1280 × 560, too short for it, it opens beside
//   its button, not over it, a fade at the cut edge shows there is more (at the top once scrolled), and End from the
//   keyboard scrolls the last row into view;
// - Escape anywhere in the ⌘K palette (after Tab moved the focus off its field) closes it and gives the focus back to
//   where it was opened from; ⌘/ over the palette replaces it with the shortcut help, and ⌘K over the help the reverse;
// - Tab in the shortcut help reaches "Change in Settings" after its scrolling list, and wraps to Close;
// - Escape out of a menu's Rename leaves the focus on the row the menu was opened on, the menu still open with its Rename
//   row highlighted, so Enter renames again instead of opening the row;
// - Escape in a search field (the To-dos page's) clears what was typed;
// - a theme, theme color or language picked in one tab is taken by the other tabs of the browser; the language's reload
//   waits while the other tab holds a draft, with a toast, and comes once the draft is gone.
//
// Needs the web page against the fake hub with its Recent conversations, and Chrome (or any Chromium through --executable):
//
//   MOCK_RECENT=1 PORT=18960 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18960 npx vite --port 5960 --strictPort &)
//   node web/e2e/keyboard-overlays.mjs http://127.0.0.1:5960 [--shots <dir>] [--executable <chromium>]
//
// Runs in Chinese, light and dark, each in a fresh browser context; nothing but the sign-in is written to the hub. Prints one line per
// check and a verdict; exits 1 on a failure and after three minutes.
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
const executable = flag("--executable");
if (!/^https?:\/\/(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$/.test(base)) {
  console.error("usage: node web/e2e/keyboard-overlays.mjs <loopback web origin> [--shots <dir>] [--executable <chromium>]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 3 minutes");
  process.exit(1);
}, 180_000).unref();

let failures = 0;
const check = (cond, msg, detail) => {
  console.log(`${cond ? "ok  " : "FAIL"} ${msg}${!cond && detail !== undefined ? ` (${JSON.stringify(detail)})` : ""}`);
  if (!cond) failures++;
};

const SESSION = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000002";
const settle = (page, ms = 300) => page.waitForTimeout(ms);
const shot = (page, name) => (shots ? page.screenshot({ path: join(shots, `${name}.png`) }) : undefined);
const modals = (page) => page.evaluate(() => [...document.querySelectorAll('[role=dialog][aria-modal="true"]')].map((d) => (d.querySelector("input") ? "palette" : d.textContent.includes("快捷键") ? "help" : "other")));
const focused = (page) =>
  page.evaluate(() => {
    const a = document.activeElement;
    return a ? { tag: a.tagName, opener: a.hasAttribute("data-e2e-opener"), text: (a.getAttribute("aria-label") || a.textContent || "").trim().slice(0, 40) } : null;
  });

async function context(browser, theme, width = 1280, height = 820) {
  const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: theme, locale: "zh-CN", reducedMotion: "reduce", serviceWorkers: "block" });
  // Every hub write but the sign-in (the link's token traded for this browser's own) is answered here and never reaches
  // the hub: the checks only read.
  await ctx.route("**/api/v1/**", (route) => {
    const r = route.request();
    if (r.method() === "GET" || new URL(r.url()).pathname.startsWith("/api/v1/auth/")) return route.continue();
    return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });
  const page = await ctx.newPage();
  page.on("pageerror", (e) => check(false, `no page error (${theme})`, String(e).slice(0, 200)));
  await page.goto(`${base}/?token=mock`);
  await page.waitForURL((u) => !u.search.includes("token"), { timeout: 15000 });
  await page.waitForLoadState("networkidle", { timeout: 15000 }).catch(() => {});
  await page.evaluate((t) => {
    localStorage.setItem("persona.theme", t);
    localStorage.setItem("persona.language", "zh-Hans");
  }, theme);
  return { ctx, page };
}

async function go(page, path) {
  await page.goto(`${base}${path}`);
  await page.waitForLoadState("networkidle", { timeout: 15000 }).catch(() => {});
  await settle(page, 600);
}

// The session ⋯ menu: its height and where it opens.
async function sessionMenu(browser, theme) {
  for (const height of [820, 560]) {
    const { ctx, page } = await context(browser, theme, 1280, height);
    await go(page, SESSION);
    const button = page.locator('button[aria-label="更多操作"]').first();
    await button.click();
    await page.locator("[role=menu]").first().waitFor({ timeout: 5000 });
    await settle(page);
    const m = await page.evaluate(() => {
      const el = document.querySelector("[role=menu]");
      const r = el.getBoundingClientRect();
      return { client: el.clientHeight, scroll: el.scrollHeight, top: r.top, bottom: r.bottom, down: Boolean(el.querySelector('[data-menu-more="down"]')), up: Boolean(el.querySelector('[data-menu-more="up"]')) };
    });
    const anchor = await button.boundingBox();
    await shot(page, `11-session-menu-1280x${height}-${theme}`);
    if (height === 820) {
      check(m.scroll <= m.client + 1 && m.bottom <= height - 8 && !m.down, `menu: every row shows at 1280 × 820 (${theme})`, m);
    } else {
      check(m.top >= anchor.y + anchor.height, `menu: opens below its button, not over it, at 1280 × 560 (${theme})`, { menuTop: m.top, anchorBottom: anchor.y + anchor.height });
      check(m.bottom <= height - 8 + 1, `menu: kept inside the window at 1280 × 560 (${theme})`, m);
      check(m.scroll > m.client && m.down && !m.up, `menu: a fade at the bottom says there is more (${theme})`, m);
      // End from the keyboard: the last row is scrolled into view, not highlighted out of sight
      await page.keyboard.press("End");
      await settle(page);
      const last = await page.evaluate(() => {
        const el = document.querySelector("[role=menu]");
        const rows = el.querySelectorAll("[role=menuitem]");
        const box = el.getBoundingClientRect();
        const r = rows[rows.length - 1].getBoundingClientRect();
        return { rowBottom: r.bottom, menuBottom: box.bottom, scrollTop: el.scrollTop };
      });
      await shot(page, `11-session-menu-1280x${height}-end-${theme}`);
      check(last.scrollTop > 0 && last.rowBottom <= last.menuBottom, `menu: End scrolls the last row into view (${theme})`, last);
      const end = await page.evaluate(() => {
        const el = document.querySelector("[role=menu]");
        return { down: Boolean(el.querySelector('[data-menu-more="down"]')), up: Boolean(el.querySelector('[data-menu-more="up"]')) };
      });
      await shot(page, `11-session-menu-1280x${height}-scrolled-${theme}`);
      check(end.up && !end.down, `menu: scrolled to the end, the fade moves to the top (${theme})`, end);
    }
    await ctx.close();
  }
}

// The palette and the shortcut help: Escape after Tab, and the two replacing each other.
async function paletteAndHelp(browser, theme) {
  const { ctx, page } = await context(browser, theme);
  await go(page, "/persona");
  const composer = page.locator("textarea").first();
  await composer.click();
  await composer.evaluate((el) => el.setAttribute("data-e2e-opener", ""));
  await page.keyboard.press("Meta+k");
  await page.locator('[role=dialog][aria-modal="true"] input').first().waitFor({ timeout: 5000 });
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await settle(page, 150);
  const inside = await page.evaluate(() => Boolean(document.activeElement?.closest('[role=dialog][aria-modal="true"]')) && document.activeElement?.tagName !== "INPUT");
  await page.keyboard.press("Escape");
  await settle(page);
  const after = { modals: await modals(page), focus: await focused(page) };
  await shot(page, `21-palette-esc-after-tab-${theme}`);
  check(inside && after.modals.length === 0, `palette: Escape off its field closes it (${theme})`, after);
  check(after.focus?.opener === true, `palette: the focus goes back to the composer it was opened from (${theme})`, after.focus);

  await page.keyboard.press("Meta+k");
  await settle(page);
  await page.keyboard.press("Meta+/");
  await settle(page, 500);
  const help = await modals(page);
  await shot(page, `21-palette-then-help-${theme}`);
  check(help.length === 1 && help[0] === "help", `palette: ⌘/ replaces it with the help, one dialog (${theme})`, help);
  await page.keyboard.press("Meta+k");
  await settle(page, 500);
  const back = await modals(page);
  check(back.length === 1 && back[0] === "palette", `help: ⌘K replaces it with the palette, one dialog (${theme})`, back);
  await page.keyboard.press("Escape");
  await settle(page);
  check((await modals(page)).length === 0, `palette: one Escape closes it after the swap (${theme})`);

  // Tab through the help: past its scrolling list to the button after it, then round to Close.
  await page.keyboard.press("Meta+/");
  await settle(page, 500);
  const seen = [];
  for (let i = 0; i < 5; i++) {
    await page.keyboard.press("Tab");
    await settle(page, 100);
    seen.push((await focused(page))?.text ?? "");
    if (seen.at(-1) === "在设置里改键") await shot(page, `22-help-tab-${theme}`);
  }
  const at = seen.indexOf("在设置里改键");
  check(at >= 0, `help: Tab reaches "在设置里改键" (${theme})`, seen);
  check(at >= 0 && seen[at + 1] === "关闭", `help: Tab after it wraps to Close (${theme})`, seen);
  await page.keyboard.press("Escape");
  await ctx.close();
}

// Escape out of a menu's Rename, and Escape in a search field.
async function renameAndSearch(browser, theme) {
  const { ctx, page } = await context(browser, theme);
  await go(page, "/persona");
  const row = page.locator('a:has-text("周末去杭州的行程")').first();
  await row.focus();
  await row.evaluate((el) => el.setAttribute("data-e2e-opener", ""));
  await page.keyboard.press("Shift+F10");
  await page.locator("[role=menu]").first().waitFor({ timeout: 5000 });
  await settle(page);
  await page.keyboard.press("r");
  const renaming = await page
    .waitForFunction(() => document.activeElement?.tagName === "INPUT" && Boolean(document.activeElement.closest("[role=menu]")), null, { timeout: 5000 })
    .then(() => true, () => false);
  await page.keyboard.press("Escape");
  await settle(page);
  const f = await focused(page);
  const menus = await page.locator("[role=menu]").count();
  await shot(page, `72-rename-esc-${theme}`);
  check(renaming, `rename: R in the row's menu opens its field (${theme})`);
  check(f?.opener === true && menus === 1, `rename: Escape leaves the focus on the row, the menu open (${theme})`, { focus: f, menus });
  const lit = await page.evaluate(() => {
    const hl = document.querySelector("[role=menu] span[aria-hidden]");
    const rows = [...document.querySelectorAll("[role=menu] [role=menuitem]")];
    const r = rows.find((b) => b.textContent.includes("重命名"));
    return hl && r ? { opacity: getComputedStyle(hl).opacity, at: Math.round(parseFloat(hl.style.top)), row: r.offsetTop } : null;
  });
  check(lit?.opacity === "1" && lit.at === lit.row, `rename: the Rename row stays highlighted (${theme})`, lit);
  // Enter is the menu's: the Rename field again, not the conversation of the row underneath
  const before = page.url();
  await page.keyboard.press("Enter");
  const again = await page
    .waitForFunction(() => document.activeElement?.tagName === "INPUT" && Boolean(document.activeElement.closest("[role=menu]")), null, { timeout: 3000 })
    .then(() => true, () => false);
  check(again && page.url() === before, `rename: Enter after it renames again and stays on the page (${theme})`, { again, url: page.url() });
  await page.keyboard.press("Escape");
  await settle(page);
  await page.keyboard.press("Escape");
  await settle(page);
  const closed = { menus: await page.locator("[role=menu]").count(), focus: await focused(page) };
  check(closed.menus === 0 && closed.focus?.opener === true, `rename: the next Escape closes the menu onto the row (${theme})`, closed);

  await go(page, "/todos");
  const box = page.getByRole("textbox", { name: "搜索待办" }).first();
  await box.click();
  await box.fill("example words");
  await page.keyboard.press("Escape");
  await settle(page);
  const value = await box.inputValue();
  await shot(page, `59-todo-search-esc-${theme}`);
  check(value === "" && new URL(page.url()).pathname === "/todos", `search: Escape clears the field and stays on the page (${theme})`, { value, url: page.url() });
  await ctx.close();
}

// Two tabs of one browser: the theme, theme color and language picked in one reach the other.
async function tabsFollow(browser) {
  const { ctx, page: a } = await context(browser, "light");
  await go(a, "/persona");
  const b = await ctx.newPage();
  await go(b, "/settings");
  check(!(await a.evaluate(() => document.documentElement.classList.contains("dark"))), "tabs: the first tab starts light");
  await b.getByRole("button", { name: "深色" }).first().click();
  await a.waitForFunction(() => document.documentElement.classList.contains("dark"), null, { timeout: 5000 }).catch(() => {});
  await shot(a, "78-other-tab-after-dark");
  check(await a.evaluate(() => document.documentElement.classList.contains("dark")), "tabs: the other tab turns dark with it");
  await b.getByRole("radio", { name: /珊瑚橙/ }).first().click();
  await a.waitForFunction(() => document.documentElement.dataset.accent === "coral", null, { timeout: 5000 }).catch(() => {});
  check((await a.evaluate(() => document.documentElement.dataset.accent)) === "coral", "tabs: the other tab takes the theme color with it");

  // The language: the first tab, in the background, holds a draft, so it waits and says so; once the draft is gone it reloads.
  const composer = a.locator("textarea").first();
  await composer.fill("还没发出去的话");
  await a.evaluate(() => Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" }));
  await b.locator("select").filter({ has: b.locator('option[value="en"]') }).first().selectOption("en");
  await settle(a, 1500);
  const waiting = await a.evaluate(() => ({ lang: document.documentElement.lang, toast: document.body.textContent.includes("另一个标签页换了语言") }));
  await shot(a, "78-other-tab-waits-over-draft");
  check(waiting.lang !== "en" && waiting.toast, "tabs: over a draft the other tab keeps its language and says it changed", waiting);
  await composer.fill("");
  await a.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await a.waitForFunction(() => document.documentElement.lang === "en", null, { timeout: 10000 }).catch(() => {});
  await settle(a, 600);
  await shot(a, "78-other-tab-after-english");
  check((await a.evaluate(() => document.documentElement.lang)) === "en", "tabs: with the draft gone the other tab reloads into English");
  await ctx.close();
}

const browser = await chromium.launch({ headless: true, ...(executable ? { executablePath: executable } : { channel: "chrome" }) });
try {
  for (const theme of ["light", "dark"]) {
    await sessionMenu(browser, theme);
    await paletteAndHelp(browser, theme);
    await renameAndSearch(browser, theme);
  }
  await tabsFollow(browser);
} catch (e) {
  check(false, "the run finished", String(e).split("\n")[0]);
} finally {
  await browser.close();
}
console.log(failures ? `FAIL ${failures} check(s) failed` : "PASS keyboard and overlays");
process.exit(failures ? 1 : 0);
