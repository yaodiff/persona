// The sidebar's Agents groupings on a paging hub (#6662, docs/web/web.md "Paging hubs"): one bar, 待办 / 机器 / 仓库 /
// 项目 / 状态, and the same quick filters under it, whether the list is paged or complete. By to-do is paged as a section per open to-do, open, then 没挂待办的
// folded; By machine and By state stay paged; By repository and a pin take the complete list, under the same bar. The
// title row's ⋯ holds the same items either way (状态优先 / 最近更新 / the archived Agents).
// Checked against the fake hub's own answers, with light and dark screenshots.
//
// Needs the web page against the fake hub with the paged list, and Chrome:
//
//   MOCK_SESSIONS_LIST=1 MOCK_SESSIONS_CATALOG=1 MOCK_MANAGERS=1 PORT=18793 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18793 npx vite --port 5293 --strictPort &)
//   node web/e2e/agents-paged-groupings.mjs http://127.0.0.1:5293 <artifact directory> [--before] [--channel chrome]
//
// The fake hub pins a space by default, which keeps the sidebar on its complete list: the runner clears the pins, sets
// them again for the pinned case (and reloads: the fake hub does not push them), and puts them back at the end.
// --before takes the same screenshots of a build from before #6662, with no checks. Prints one line per check and a
// verdict; exits 1 on a failed check and 2 on its three-minute timeout.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : fallback;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const out = args[1];
const before = args.includes("--before");
const channel = flag("--channel", "chrome");
if (!base || !out || out.startsWith("--")) {
  console.error("usage: node web/e2e/agents-paged-groupings.mjs <web URL> <artifact directory> [--before] [--channel chrome]");
  process.exit(2);
}
mkdirSync(out, { recursive: true });
const watchdog = setTimeout(() => { console.log("VERDICT TIMEOUT agents paged groupings"); process.exit(2); }, 180_000);

const failures = [];
const check = (ok, what, detail = "") => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail ? `: ${detail}` : ""}`);
  if (!ok) failures.push(what);
};
const hubGet = async (path, init) => (await fetch(`${base}/api/v1${path}`, { ...init, headers: { Authorization: "Bearer mock", "Content-Type": "application/json", ...init?.headers } })).json();
const setPins = (pins) => hubGet("/pins", { method: "PUT", body: JSON.stringify(pins) });

// What the hub says, to hold the page to: its sessions by open to-do (child sessions folded, as the page asks), the
// to-dos' titles, and one session to pin.
const saved = await hubGet("/pins");
await setPins({ spaces: [], sessions: [] });
const counts = await hubGet("/sessions?counts=1&roots=1");
const todos = (await hubGet("/todos")).todos ?? [];
const first = (await hubGet("/sessions?limit=1&summary=1&roots=1")).sessions?.[0];
const titles = Object.entries(counts.todo ?? {}).filter(([id, n]) => id && n > 0).map(([id]) => todos.find((td) => td.id === id)?.title ?? id);
if (!before && !counts.todo) {
  console.log("VERDICT FAIL the fake hub counts no to-dos (start it with MOCK_SESSIONS_LIST=1)");
  process.exit(1);
}
const SEGMENTS = ["待办", "机器", "仓库", "项目", "状态"];
// the complete list's quick filters (lib/agent-filter.ts), the paged list's chips too on a hub with sessions_quick_filters
const QUICK = ["全部", "需要你", "完成未看", "工作中", "后台等待中", "飞书群", "终端"];

const browser = await chromium.launch({ channel, headless: true });
try {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 1600 }, deviceScaleFactor: 2, locale: "zh-CN", colorScheme: theme, reducedMotion: "reduce" });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${base}/?token=mock`);
    await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15_000 });
    await page.evaluate((theme) => {
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.language", "zh-Hans");
      localStorage.setItem("persona.sidebarWidth", "340");
      for (const key of Object.keys(localStorage)) if (key.startsWith("fold:") || key.endsWith("BrowseBy") || key === "persona.agentGrouping") localStorage.removeItem(key);
    }, theme);
    const paged = page.locator("[data-paged-sessions='runtime']").first();
    const bar = page.locator('aside [role="radiogroup"][aria-label="Agent 分组"]').first();
    const chipNames = async () => (await page.locator('aside [role="radiogroup"][aria-label="快速筛选"]').first().locator('[role="radio"]').evaluateAll((els) => els.map((el) => el.getAttribute("aria-label") ?? ""))).map((label) => label.replace(/ \d+$/, ""));
    // the Agents part of the sidebar: from its grouping control (or the paged list's) down
    const shot = async (name) => {
      await page.mouse.move(900, 900);
      await page.waitForTimeout(500);
      const side = await page.locator("aside").first().boundingBox();
      const anchor = (await bar.count()) ? bar : paged;
      // the Agents block at the top of the sidebar's scroller, clear of the usage meters at its foot
      await anchor.evaluate((el) => el.scrollIntoView({ block: "start" }));
      await page.waitForTimeout(300);
      const top = Math.max(0, (await anchor.boundingBox()).y - 48);
      await page.screenshot({ path: join(out, `${before ? "before" : "after"}-${name}-${theme}.png`), clip: { x: side.x, y: top, width: side.width, height: Math.min(1150, 1600 - top) }, animations: "disabled" });
    };
    // the title row's ⋯, opened, read and shot, then closed
    const listMenu = async (name) => {
      await page.locator('aside button[aria-label^="Agent 列表选项"]').first().click();
      const menu = page.locator('[role="menu"]').first();
      await menu.waitFor({ timeout: 5_000 });
      const items = await menu.locator('[role^="menuitem"]').evaluateAll((els) => els.map((el) => (el.textContent ?? "").trim()));
      const box = await menu.boundingBox();
      const side = await page.locator("aside").first().boundingBox();
      await page.screenshot({ path: join(out, `after-${name}-${theme}.png`), clip: { x: side.x, y: Math.max(0, box.y - 60), width: side.width, height: box.height + 120 }, animations: "disabled" });
      await page.keyboard.press("Escape");
      await page.waitForTimeout(200);
      return items;
    };
    const load = async () => {
      await page.goto(`${base}/`);
      await page.locator("aside").first().waitFor();
      await page.waitForTimeout(1500);
    };

    await load();
    if (before) {
      await paged.waitFor({ timeout: 20_000 });
      await shot("unpinned");
      await setPins({ spaces: [], sessions: [`${first.machine_id}/${first.session_id}`] });
      await load();
      await shot("pinned");
      await setPins({ spaces: [], sessions: [] });
      await ctx.close();
      continue;
    }

    // nothing pinned: paged, By to-do, under the complete list's bar
    await paged.waitFor({ timeout: 20_000 });
    check((await bar.locator('[role="radio"]').allInnerTexts()).join(" / ") === SEGMENTS.join(" / "), `${theme}: the bar offers ${SEGMENTS.join(" / ")}`);
    check((await bar.locator('[aria-checked="true"]').innerText()) === "待办", `${theme}: By to-do is the default`);
    const pagedChips = await chipNames();
    check(pagedChips[0] === "全部" && pagedChips.every((name) => QUICK.includes(name)), `${theme}: the paged list's chips are the complete list's quick filters`, pagedChips.join(" / "));
    const pagedMenu = await listMenu("paged-menu");
    check(pagedMenu[0] === "状态优先" && pagedMenu[1] === "最近更新", `${theme}: the paged list's ⋯ has the complete list's sorts`, pagedMenu.join(" / "));
    const sections = () => paged.locator("[data-session-group]").evaluateAll((els) => els.map((el) => ({
      name: el.querySelector("button[aria-expanded]")?.querySelector("span")?.textContent ?? el.querySelector("button[aria-expanded]")?.textContent ?? "",
      open: el.getAttribute("data-group-open") === "true",
      rows: el.querySelectorAll("[data-session-row]").length,
    })));
    await page.waitForTimeout(800);
    const todo = await sections();
    const named = (title) => todo.find((s) => s.name.startsWith(title));
    check(titles.length > 0 && titles.every((title) => named(title)?.open && named(title).rows > 0), `${theme}: a section per open to-do with Agents, open on them`, todo.map((s) => `${s.name}${s.open ? "" : " (folded)"}`).join(", "));
    const none = todo.at(-1);
    check(!(counts.todo?.[""] > 0) || (none?.name.startsWith("没挂待办的") && !none.open && none.rows === 0), `${theme}: Not on a to-do last, folded, nothing read`);
    await shot("paged-todo");

    // a pin: the complete list, under the same bar, still By to-do
    await setPins({ spaces: [], sessions: [`${first.machine_id}/${first.session_id}`] });
    await load();
    await bar.waitFor({ timeout: 20_000 });
    check(!(await paged.count()), `${theme}: a pin takes the complete list`);
    check((await bar.locator('[role="radio"]').allInnerTexts()).join(" / ") === SEGMENTS.join(" / ") && (await bar.locator('[aria-checked="true"]').innerText()) === "待办", `${theme}: the same bar, still By to-do`);
    const completeChips = await chipNames();
    check(completeChips.every((name) => QUICK.includes(name)) && pagedChips.every((name) => completeChips.includes(name)), `${theme}: the same quick filters`, completeChips.join(" / "));
    const completeMenu = await listMenu("pinned-menu");
    check(completeMenu.join(" / ") === pagedMenu.join(" / "), `${theme}: the same ⋯ menu`, completeMenu.join(" / "));
    await shot("pinned");
    await setPins({ spaces: [], sessions: [] });
    await load();
    await paged.waitFor({ timeout: 20_000 });
    check(true, `${theme}: unpinned, the paged list again`);

    // By machine and By state stay paged; By repository takes the complete list
    for (const [label, name] of [["机器", "paged-machine"], ["状态", "paged-state"]]) {
      await bar.getByRole("radio", { name: new RegExp(`^按${label}`) }).click();
      await page.waitForTimeout(1000);
      const groups = await sections();
      check(Boolean(await paged.count()) && groups.length > 0, `${theme}: By ${label} is paged, ${groups.length} sections`, groups.map((s) => s.name).join(", "));
      await shot(name);
    }
    await bar.getByRole("radio", { name: /^按仓库/ }).click();
    await page.waitForTimeout(1000);
    check(!(await paged.count()) && (await bar.locator('[aria-checked="true"]').innerText()) === "仓库", `${theme}: By repository takes the complete list, the bar unchanged`);
    await shot("complete-repo");
    check(errors.length === 0, `${theme}: no page errors`, errors.join(" | "));
    await ctx.close();
  }
} finally {
  await setPins(saved).catch(() => {});
  await browser.close();
  clearTimeout(watchdog);
}
console.log(before ? "VERDICT DONE screenshots only" : failures.length ? `VERDICT FAIL ${failures.length} checks` : "VERDICT PASS agents paged groupings (synthetic data only)");
process.exit(failures.length ? 1 : 0);
