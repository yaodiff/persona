// End-to-end check of the direction tree on the web's pages (#4352, docs/web/web.md "Persona chats"): the To-dos page
// groups by 常驻 with what each direction has going as the sidebar shows it (the working arc while anything under it runs,
// the hub's `running`, and 等你 N; never 在做 N, #6626), the secretary's group 书记官 last; the Agents list has no 常驻
// grouping (#6662: the sidebar's 常驻 rows unfold to the same tree); the default 状态 grouping and the sidebar's 常驻
// rows keep #4610's look, each with a disclosure (folded by default) that opens to its tasks (state
// words) and a task to its agents; no 书记官 row in the sidebar.
//
// Needs the web page against the fake hub with leaders, and Chrome (or any Chromium through --executable):
//
//   MOCK_LEADERS=1 PORT=18990 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18990 npx vite --port 5990 --strictPort &)
//   node web/e2e/direction-tree.mjs http://127.0.0.1:5990 [--shots <dir>] [--executable <chromium>]
//
// Runs in Chinese, light and dark, wide (1280) and phone width (390); --shots saves each. Prints one line per check and a
// verdict; exits 1 on a failure and after three minutes.
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
if (!base) {
  console.error("usage: node web/e2e/direction-tree.mjs <web URL> [--shots <dir>] [--executable <chromium>]");
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

const hub = (path) => fetch(`${base}/api/v1${path}`, { headers: { Authorization: "Bearer mock" }, signal: AbortSignal.timeout(5000) }).then((r) => r.json());
const tree = (await hub("/assistant/directions/tree")).directions ?? [];
const leaders = tree.filter((d) => !d.secretary);
if (leaders.length < 2) fail("the fake hub has no leaders (start it with MOCK_LEADERS=1)");
// the sidebar's 常驻 rows' words (sidebar.leader_needs_you): what waits on you; what runs is the arc alone (#6626)
const countsText = (c) => (c.needs_you > 0 ? `等你 ${c.needs_you}` : "");
if (!leaders.some((d) => d.running) || !leaders.some((d) => !d.running)) fail("the fake hub needs a 常驻 with something running and one without");
// each lead's own conversation state: its own turn spins its row too, whatever runs under it
const ownState = new Map(((await hub("/assistant")).conversations ?? []).map((c) => [c.session_id, c.state]));

async function open(browser, theme, width, prefs = {}, path = "/overview") {
  const ctx = await browser.newContext({ viewport: { width, height: width > 600 ? 900 : 844 }, deviceScaleFactor: 2, colorScheme: theme, locale: "zh-CN", hasTouch: width <= 600 });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
  await page.evaluate(([t, p]) => {
    localStorage.setItem("persona.theme", t);
    localStorage.setItem("persona.language", "zh-Hans");
    for (const [k, v] of Object.entries(p)) localStorage.setItem(k, v);
  }, [theme, prefs]);
  await page.goto(`${base}${path}`);
  await page.waitForSelector("main", { timeout: 15000 });
  if (width <= 600 && path !== "/agents") {
    await page.locator("main").getByRole("button", { name: "侧边栏" }).first().click();
    await page.waitForTimeout(500); // the drawer slides in
  }
  return { ctx, page };
}

const first = leaders.find((d) => d.tasks.some((t) => t.agents.length > 0));
if (!first) fail("no leader with a task an agent is on");
const task = first.tasks.find((t) => t.agents.length > 0);
const row = (page, id) => page.locator(`aside [data-direction="${id}"]`).first();
const disclosure = (page, id) => page.locator(`aside [data-direction="${id}"] > div > button[aria-expanded]`).first();

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
try {
  // the sidebar's 常驻 rows: folded (#4610's row and counts, a chevron beside), then one opened to its tasks and a task to
  // its agents
  for (const [theme, width] of [["light", 1280], ["dark", 1280], ["light", 390]]) {
    const where = `${theme} ${width}px`;
    const { ctx, page } = await open(browser, theme, width);
    await row(page, first.id).waitFor({ state: "visible", timeout: 10000 }).catch(() => fail(`${where}: no 常驻 row for ${first.name}`));
    for (const d of leaders) {
      const text = await row(page, d.id).textContent();
      if (!text.includes(d.name) || !text.includes(countsText(d.counts))) fail(`${where}: ${d.name} row says ${text}`);
      // the lead's own row (its tasks folded under it say their state words): 等你 N, never 在做 N, and one activity mark
      // that spins while anything under the row runs or its own conversation does (#6626)
      const lead = row(page, d.id).locator(`a[href^="/persona/${d.id}"]`).first();
      if ((await lead.textContent()).includes("在做")) fail(`${where}: ${d.name}'s row says 在做: ${await lead.textContent()}`);
      const spins = (await lead.locator('[data-shape="arc"][data-state="working"]').count()) > 0;
      const own = ownState.get(d.id);
      const want = own === "needs_you" || own === "unread" ? own === "working" : d.running || own === "working";
      if (spins !== want) fail(`${where}: ${d.name} row ${spins ? "spins" : "does not spin"} (running ${d.running}, own ${own})`);
    }
    if (await page.locator('aside [data-direction="secretary"]').count()) fail(`${where}: the sidebar has a 书记官 row`);
    if ((await disclosure(page, first.id).getAttribute("aria-expanded")) !== "false") fail(`${where}: a 常驻 row does not start folded`);
    // the chevron is small (the name keeps its width); on a touch screen its hit area (touch-hit) is 44×44: each point 21px
    // from its centre lands on it
    const hit = width <= 600 ? await disclosure(page, first.id).evaluate((b) => {
      const r = b.getBoundingClientRect();
      const x = r.left + r.width / 2, y = r.top + r.height / 2;
      return [[-21, 0], [21, 0], [0, -21], [0, 21]].filter(([dx, dy]) => !b.contains(document.elementFromPoint(x + dx, y + dy))).map((d) => d.join(","));
    }) : [];
    if (hit.length) fail(`${where}: the disclosure's hit area misses ${hit.join(" ")} (under 44×44)`);
    ok(`${where}: ${leaders.length} 常驻 rows folded, each spinning exactly when something under it runs, 等你 N in words, no 书记官 row${width <= 600 ? ", disclosure hit area 44×44" : ""}`);
    if (shots) await page.screenshot({ path: join(shots, `web-sidebar-folded-${width > 600 ? "wide" : "narrow"}-${theme}.png`) });
    await disclosure(page, first.id).click();
    await page.locator(`aside [data-direction="${first.id}"] a[href="/todos?id=${task.id}"]`).waitFor({ state: "visible", timeout: 5000 }).catch(() => fail(`${where}: the task ${task.title} does not show`));
    const words = await row(page, first.id).locator("[data-task-state]").allTextContents();
    if (words.length !== first.tasks.length) fail(`${where}: ${words.length} state words for ${first.tasks.length} tasks`);
    const taskButton = row(page, first.id).locator(`button[aria-label="${task.title}的 Agent"]`);
    if ((await taskButton.getAttribute("aria-expanded")) !== "false") fail(`${where}: a task does not start folded`);
    await taskButton.click();
    const agent = task.agents[0];
    await page.locator(`aside [data-direction="${first.id}"] a[href="/s/${agent.machine_id}/${agent.session_id}"]`).waitFor({ state: "visible", timeout: 5000 }).catch(() => fail(`${where}: the agent ${agent.title} does not show`));
    await page.waitForTimeout(400); // the folds finish opening
    ok(`${where}: ${first.name} opens to ${words.join(" / ")}, ${task.title} to its agents`);
    if (shots) await page.screenshot({ path: join(shots, `web-sidebar-open-${width > 600 ? "wide" : "narrow"}-${theme}.png`) });
    // the fold is kept on this device
    await page.reload();
    await page.waitForSelector("main", { timeout: 15000 });
    if (width <= 600) {
      await page.locator("main").getByRole("button", { name: "侧边栏" }).first().click();
      await page.waitForTimeout(500);
    }
    await row(page, first.id).waitFor({ state: "visible", timeout: 10000 });
    if ((await disclosure(page, first.id).getAttribute("aria-expanded")) !== "true") fail(`${where}: the opened 常驻 row is folded again after a reload`);
    ok(`${where}: the fold is kept after a reload`);
    await ctx.close();
  }

  // the To-dos page by 常驻
  for (const theme of ["light", "dark"]) {
    const { ctx, page } = await open(browser, theme, 1280, { "persona.todos.grouping": "direction" }, "/todos");
    const groups = page.locator('[data-testid="todo-group"][data-group^="direction:"]');
    await groups.first().waitFor({ state: "visible", timeout: 10000 }).catch(() => fail(`${theme}: the To-dos page is not grouped by 常驻`));
    const shown = await groups.evaluateAll((gs) => gs.map((g) => g.getAttribute("data-group")));
    const expected = tree.filter((d) => d.tasks.length > 0).map((d) => `direction:${d.secretary ? "secretary" : d.id}`);
    if (JSON.stringify(shown) !== JSON.stringify(expected)) fail(`${theme}: To-dos groups ${shown.join(",")} != ${expected.join(",")}`);
    if (!(await page.getByRole("radio", { name: "常驻" }).count()) && !(await page.getByText("常驻", { exact: true }).count())) fail(`${theme}: no 常驻 grouping option`);
    // each section's end as the sidebar shows it: the arc exactly while something under it runs, then 等你 N
    const said = await groups.evaluateAll((gs) => gs.map((g) => {
      const end = g.querySelector("[data-direction-counts]");
      return [end?.getAttribute("title") ?? "", !!end?.querySelector('[data-shape="arc"][data-state="working"]')];
    }));
    const want = tree.filter((d) => d.tasks.length > 0).map((d) => [countsText(d.counts), d.running]);
    if (JSON.stringify(said) !== JSON.stringify(want)) fail(`${theme}: To-dos section ends ${JSON.stringify(said)} != ${JSON.stringify(want)}`);
    ok(`${theme}: To-dos grouped by 常驻 in the tree's order (${shown.length} groups), each end as the sidebar shows it`);
    if (shots) await page.screenshot({ path: join(shots, `web-direction-todos-${theme}.png`) });
    await ctx.close();
  }

  // the To-dos page's default grouping (状态), as before: no 常驻 section
  {
    const { ctx, page } = await open(browser, "light", 1280, {}, "/todos");
    await page.locator('[data-testid="todo-section"]').first().waitFor({ state: "visible", timeout: 10000 }).catch(() => fail("the To-dos page shows no status sections"));
    if (await page.locator('[data-testid="todo-group"][data-group^="direction:"]').count()) fail("the default To-dos grouping shows 常驻 sections");
    ok("To-dos by 状态 by default");
    if (shots) await page.screenshot({ path: join(shots, "web-direction-todos-default-light.png") });
    await ctx.close();
  }

  // the Agents list has no 常驻 grouping (#6662): a choice saved before it went shows By to-do, the 常驻 rows above hold the tree
  for (const [theme, width] of [["light", 1280], ["light", 390]]) {
    const where = `${theme} ${width}px`;
    const { ctx, page } = await open(browser, theme, width, { "persona.agentGrouping": "direction", "persona.agentChatList": "0" }, "/agents");
    const bar = page.locator('main [role="radiogroup"][aria-label="Agent 分组"]').first();
    await bar.waitFor({ state: "visible", timeout: 10000 }).catch(() => fail(`${where}: no grouping bar on the Agents page`));
    const segments = await bar.locator('[role="radio"]').allInnerTexts();
    if (segments.includes("常驻")) fail(`${where}: the Agents page still offers 常驻: ${segments.join(" / ")}`);
    if ((await bar.locator('[role="radio"][aria-checked="true"]').innerText()) !== "待办") fail(`${where}: a saved 常驻 choice does not read as By to-do`);
    ok(`${where}: the Agents page offers ${segments.join(" / ")}, a saved 常驻 choice read as 待办`);
    await ctx.close();
  }
  console.log("PASS direction tree");
} finally {
  await browser.close();
}
