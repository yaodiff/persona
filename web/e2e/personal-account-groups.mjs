// Personal Agents by account (#16248), light and dark, in English: the sidebar's personal list and the Agents page (/a)
// grouped by account show each account's name after its provider's mark, never the raw account id the hub groups by,
// and the account pill's menu names them the same way. /agents and /agents/sessions are answered here with made-up
// accounts (Grok Bot, Dots, Claude Duet with a blank name, Muse, and an account /agents does not list).
//
//   MOCK_SESSIONS_LIST=1 PORT=18961 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18961 npx vite --port 5961 --strictPort &)
//   node web/e2e/personal-account-groups.mjs http://127.0.0.1:5961 <artifact directory> [--channel chrome]
//
// Prints one line per check and a verdict; exits 1 on a failed check and 2 on its three-minute timeout.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name, fallback) => { const i = args.indexOf(name); return i >= 0 ? args[i + 1] : fallback; };
const base = (args[0] ?? "").replace(/\/$/, "");
const out = args[1];
const channel = flag("--channel", "");
if (!base || !out || out.startsWith("--")) { console.error("usage: node web/e2e/personal-account-groups.mjs <web URL> <artifact directory> [--channel chrome]"); process.exit(2); }
mkdirSync(out, { recursive: true });
const watchdog = setTimeout(() => { console.log("VERDICT TIMEOUT personal account groups"); process.exit(2); }, 180_000);
const failures = [];
const check = (ok, what, detail = "") => { console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail ? `: ${detail}` : ""}`); if (!ok) failures.push(what); };

const now = Date.now();
const row = (id, accountId, provider, title, hours, unread = 0) => ({ id: `example:${id}`, source: "example", sessionKey: id, accountId, provider, title, canSend: true, unreadCount: unread, rankTimeMs: now - hours * 3_600_000 });
const SESSIONS = [
  row("trip", "acct-grok", "grok_bot", "Trip planning", 0.2, 1), row("gym", "acct-grok", "grok_bot", "Workout log", 30),
  row("report", "acct-dots", "dots", "Weekly report", 1), row("review", "acct-duet", "claude_duet", "Code review", 3),
  row("notes", "acct-muse", "muse2api", "Reading notes", 5), row("recipes", "acct-groq", "groq", "Recipes", 50),
];
const SOURCES = [{ source: "example", enabled: true, accounts: [
  { accountId: "acct-grok", provider: "grok_bot", displayName: "Grok Bot", status: "ok", sessionCount: 2 },
  { accountId: "acct-dots", provider: "dots", displayName: "Dots · Example User", status: "ok", sessionCount: 1 },
  { accountId: "acct-duet", provider: "claude_duet", displayName: "", status: "ok", sessionCount: 1 },
  { accountId: "acct-muse", provider: "muse2api", displayName: "Muse", status: "ok", sessionCount: 1 },
] }];
const NAMES = ["Grok Bot", "Dots · Example User", "Claude Duet", "Muse", "Groq"];
const tally = (rows, key) => rows.reduce((m, r) => ({ ...m, [key(r)]: (m[key(r)] ?? 0) + 1 }), {});

const browser = await chromium.launch({ ...(channel ? { channel } : {}), ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}), headless: true });
try {
  for (const theme of ["light", "dark"]) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: theme });
    await context.addInitScript((value) => {
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", value);
      localStorage.setItem("persona.personalBrowseBy", "group");
    }, theme);
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/api/v1/agents", (route) => route.fulfill({ json: { sources: SOURCES } }));
    await page.route("**/api/v1/agents/hidden*", (route) => route.fulfill({ json: { sessions: [] } }));
    await page.route("**/api/v1/agents/sessions?*", (route) => {
      const q = new URL(route.request().url()).searchParams;
      const rows = SESSIONS.filter((r) => !q.get("group") || q.get("group") === r.accountId);
      return route.fulfill({ json: q.has("counts")
        ? { total: rows.length, revision: "r1", group: tally(SESSIONS, (r) => r.accountId), state: { idle: rows.length }, machine: {}, project: {} }
        : { sessions: rows, remove: [], has_more: false, total: rows.length, revision: "r1" } });
    });
    await page.goto(`${base}/a?token=mock`, { waitUntil: "domcontentloaded", timeout: 30_000 });
    const main = page.locator("main");
    await main.locator("[data-session-group]").nth(4).waitFor({ timeout: 30_000 });
    const headings = async (scope) => scope.locator("[data-session-group] button[aria-expanded]").evaluateAll((els) => els.map((el) => ({
      text: el.textContent ?? "", name: el.querySelector("[title]")?.textContent ?? "", tip: el.querySelector("[title]")?.getAttribute("title") ?? "", mark: el.querySelector("[data-agent-mark]")?.getAttribute("data-agent-mark") ?? "" })));
    const page1 = await headings(main);
    check(NAMES.every((n) => page1.some((h) => h.name === n)), `${theme}: Agents page names every account`, page1.map((h) => h.name).join(" | "));
    check(page1.every((h) => !h.text.includes("acct-") && h.tip.startsWith("acct-")), `${theme}: Agents page keeps the ids to the tooltips`);
    check(JSON.stringify(page1.map((h) => h.mark).sort()) === JSON.stringify(["claude", "codex", "grok", "letter", "letter"]), `${theme}: Agents page draws the providers' marks`, page1.map((h) => h.mark).join(","));
    await page.screenshot({ path: join(out, `agents-page-${theme}.png`) });

    const sidebar = page.locator("aside");
    const side = await headings(sidebar).catch(() => []);
    check(side.length >= 5 && side.every((h) => !h.text.includes("acct-")), `${theme}: sidebar names the accounts`, side.map((h) => h.text).join(" | "));
    await sidebar.locator("[data-session-group]").last().scrollIntoViewIfNeeded().catch(() => {});
    await sidebar.first().screenshot({ path: join(out, `sidebar-${theme}.png`) });

    const pill = main.locator('button[aria-haspopup="menu"]').filter({ hasText: /All accounts/ }).first();
    if (await pill.count()) {
      await pill.click();
      await page.locator('[role="menuitemradio"]').last().waitFor({ timeout: 10_000 });
      const items = await page.locator('[role="menuitemradio"]').allTextContents();
      check(NAMES.every((n) => items.some((x) => x.includes(n))) && items.every((x) => !x.includes("acct-")), `${theme}: the account pill's menu names the accounts`, items.join(" | "));
      const menu = page.locator('[role="menu"]').filter({ has: page.locator('[role="menuitemradio"]') }).last();
      await page.waitForTimeout(400); // the menu fades in
      const menuBox = await menu.boundingBox();
      const pillBox = await pill.boundingBox();
      if (menuBox && pillBox) {
        const x = Math.min(menuBox.x, pillBox.x) - 16, y = Math.min(menuBox.y, pillBox.y) - 16;
        await page.screenshot({ path: join(out, `account-menu-${theme}.png`), clip: { x, y, width: Math.max(menuBox.x + menuBox.width, pillBox.x + pillBox.width) - x + 16, height: Math.max(menuBox.y + menuBox.height, pillBox.y + pillBox.height) - y + 16 } });
      } else check(false, `${theme}: the account menu is on screen`);
      await page.keyboard.press("Escape");
    } else check(false, `${theme}: the account pill is shown`);
    check(!errors.length, `${theme}: no page errors`, errors.join("; "));
    await context.close();
  }
} finally {
  await browser.close();
  clearTimeout(watchdog);
}
console.log(failures.length ? `VERDICT FAIL personal account groups (${failures.length}): ${failures.join("; ")}` : "VERDICT PASS personal account groups");
process.exit(failures.length ? 1 : 0);
