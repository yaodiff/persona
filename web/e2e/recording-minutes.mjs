// Synthetic Knowledge recording detail, no live hub. Run against an already running Vite server.
// BASE_URL=http://127.0.0.1:5791 PERSONA_E2E_CHROMIUM=<chrome> SHOTS_DIR=/tmp/mm/shots node web/e2e/recording-minutes.mjs
// Minutes without an overview (the legacy layout: minutes-<theme>.png at 1440, minutes-<theme>-<lang>-390.png) and,
// since #6563, with one (the summary card: overview-<lang>-<theme>-<width>[-large][-zoom200].png, in zh-Hans, en and ja
// at 1440 and 390, light and dark; at the app's Large text size, Settings › General › Appearance, in zh-Hans and en at
// 1440 and 390; and Large text in a 1440 window at 200% browser zoom, a 720 px page at twice the pixels). Every run checks
// overflow and single-line buttons, a timestamp jump, adding a to-do and regenerating; the summary card's also its
// heading levels, how many cards share each row (a card alone in its row takes all of it), that no line of a topic card
// runs past it, and the fold. E2E_ONLY=legacy runs only the legacy layout, to compare against a build of main. A
// 10-minute watchdog ends the run with a verdict.
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:5791");
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback Vite server");
assert.ok(["http:", "https:"].includes(base.protocol) && !base.username && !base.password && base.pathname === "/" && !base.search && !base.hash, "Use an origin without credentials or a route");
const executablePath = process.env.PERSONA_E2E_CHROMIUM || process.env.CHROME_BIN;
assert.ok(executablePath, "Set PERSONA_E2E_CHROMIUM or CHROME_BIN");
const output = resolve(process.env.SHOTS_DIR || "/tmp/mm/shots");
await mkdir(output, { recursive: true });
const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const detailPath = `/kb/recordings/${id}`;
const minutesPath = `/recordings/${id}/minutes`;
const fixture = () => ({
  kind: "recordings", id, path: `recordings/${id}`,
  meta: { title: "设计评审 · 示例录音", kind: "meeting", complete: true, started_at: "2026-11-10T14:00:00+08:00", duration: 1800, device: "phone", language: "zh-Hans", revision: 3, minutes_state: "ready", minutes_asked: true },
  summary: "讨论示例项目的导出限制、回归测试和后续安排。",
  segments: [
    { start: 0, end: 240, text: "Ada：今天讨论示例项目的导出体验，以及错误提示的改进。" },
    { start: 312, end: 480, text: "Ada：超过大小限制的条目需要明确提示，不应静默跳过。" },
    { start: 600, end: 780, text: "Bo：这一版保留限制，先补充清晰的提示。" },
    { start: 900, end: 1200, text: "Bo：周五前补充回归测试。Ada 会整理评审结论。" },
    { start: 1500, end: 1800, text: "Ada：限制按单个条目还是整份录音计算，留待下次讨论。" },
  ],
  minutes: {
    version: 2, revision: 3, generated_at: "2026-11-10T06:31:00Z", title: "示例项目设计评审", when: "2026-11-10 14:00–14:30", where: "示例会议室 3", attendees: ["Ada", "Bo"],
    points: [{ text: "导出遇到大小限制时，需要明确告知哪些条目未被包含。", who: "Ada", at: 312 }],
    decisions: [{ text: "本次发布保留大小限制，补充可理解的错误提示。", at: 600 }],
    actions: [
      { id: "a1", who: "Bo", what: "补充导出大小限制的回归测试，并确认提示文案。", by: "周五前", due_date: "2026-11-13", at: 900 },
      { id: "a2", who: "Ada", what: "整理评审结论并分享给示例项目组。", at: 930, todo_id: "example-todo" },
    ],
    open: [{ text: "大小限制应该按单个条目还是整份录音计算？", at: 1500 }],
    markdown: "# 示例项目设计评审\n\n- Bo：周五前补充回归测试。",
  },
});

// An attendee whose name is long (calendar names may run to 100 characters): the speaker label must wrap in a narrow card.
const LONG_NAME = "Alexandria Montgomery-Wellington";
// The summary card's words, in the minutes' own language (the hub writes them in one language, #5906).
const WORDS = {
  "zh-Hans": {
    title: "示例项目迭代评审", where: "示例会议室 3", by: "周五前",
    headline: "导出保留大小上限上线；发布日期取决于缓存问题能否在周三前修复",
    conclusions: [["导出保留 4 MB 上限，超限条目给出明确提示", "4 MB", 600], ["发布日期取决于周三前能否修复缓存问题", "周三前", 1500], ["压测目标提高到 40K QPS", "40K", 1200]],
    groups: [
      ["项目进展", [
        ["导出", "progress", [["导出会丢弃超过 4 MB 的条目", "4 MB", 312, "Ada"], ["超限提示文案已经完成", "已经完成", 480], ["导出保留上限，跳过条目时给出提示", "保留上限", 600, true]]],
        ["发布日期", "schedule", [["20 号的发布有风险", "有风险", 900], ["缓存问题周三前修不好，发布推迟一周", "推迟一周", 1500, true]]],
      ]],
      ["工具与性能", [
        ["压测", "performance", [["压测 QPS 提高到 40K", "40K", 1200, "Bo"]]],
        ["缓存问题", "quality", [["弱网下缓存问题复现了一次", "一次", 930], ["问题只在冷启动后出现", "冷启动", 960], ["日志显示缓存键被重复写入", "重复写入", 990], ["临时方案是关闭预取", "关闭预取", 1020], ["修复需要改动缓存层接口", "缓存层接口", 1050], ["Bo 周三前给出修复方案", "周三前", 1080]]],
        ["发布工具", "tooling", [["发布脚本新增回滚开关", "回滚开关", 1110, LONG_NAME], ["灰度比例降到 10%", "10%", 1140]]],
      ]],
    ],
    actions: [["Bo", "补充超限提示文案和回归测试", 900], ["Ada", "分享压测报告", 930]],
    open: [["大小限制按单个条目还是整份录音计算", 1500]],
  },
  en: {
    title: "Example project sprint review", where: "Example room 3", by: "Friday",
    headline: "Export ships with the size cap; the release date waits on the cache fix",
    conclusions: [["Export ships with the 4 MB cap and a warning", "4 MB", 600], ["The release date hangs on the cache fix by Wednesday", "Wednesday", 1500], ["Load target raised to 40K QPS", "40K", 1200]],
    groups: [
      ["Project progress", [
        ["Export", "progress", [["Export drops entries over 4 MB", "4 MB", 312, "Ada"], ["The export size-limit message is done", "done", 480], ["Export keeps the cap, with a warning when an entry is skipped", "keeps the cap", 600, true]]],
        ["Release date", "schedule", [["The release on the 20th is at risk", "at risk", 900], ["The release moves a week unless the cache bug is fixed by Wednesday", "a week", 1500, true]]],
      ]],
      ["Tooling and performance", [
        ["Load testing", "performance", [["Load-test QPS raised to 40K", "40K", 1200, "Bo"]]],
        ["Cache bug", "quality", [["The cache bug reproduced once on a weak network", "once", 930], ["It shows only after a cold start", "cold start", 960], ["Logs show the cache key written twice", "written twice", 990], ["The workaround turns prefetch off", "prefetch off", 1020], ["The fix changes the cache layer's interface", "interface", 1050], ["Bo proposes a fix by Wednesday", "Wednesday", 1080]]],
        ["Release tooling", "tooling", [["The release script gets a rollback switch", "rollback switch", 1110, LONG_NAME], ["The canary share drops to 10%", "10%", 1140]]],
      ]],
    ],
    actions: [["Bo", "Add the warning text and the regression test", 900], ["Ada", "Share the load-test report", 930]],
    open: [["Whether the cap is per entry or per recording", 1500]],
  },
  ja: {
    title: "サンプルプロジェクトのスプリントレビュー", where: "サンプル会議室 3", by: "金曜まで",
    headline: "エクスポートはサイズ上限付きで公開、リリース日はキャッシュ修正次第",
    conclusions: [["エクスポートは 4 MB の上限と警告付きで公開する", "4 MB", 600], ["リリース日は水曜までのキャッシュ修正次第", "水曜まで", 1500], ["負荷テストの目標を 40K QPS に引き上げ", "40K", 1200]],
    groups: [
      ["プロジェクトの進捗", [
        ["エクスポート", "progress", [["4 MB を超える項目はエクスポートされない", "4 MB", 312, "Ada"], ["上限超過のメッセージは完成", "完成", 480], ["上限は維持し、項目を飛ばすときは警告を出す", "上限は維持", 600, true]]],
        ["リリース日", "schedule", [["20 日のリリースは危うい", "危うい", 900], ["水曜までにキャッシュの不具合が直らなければリリースを 1 週間延期", "1 週間延期", 1500, true]]],
      ]],
      ["ツールと性能", [
        ["負荷テスト", "performance", [["負荷テストの QPS を 40K に引き上げ", "40K", 1200, "Bo"]]],
        ["キャッシュの不具合", "quality", [["弱い回線で不具合が 1 回再現した", "1 回", 930], ["コールドスタート後にだけ起きる", "コールドスタート", 960], ["ログではキャッシュキーが二重に書かれている", "二重", 990], ["暫定策はプリフェッチの停止", "プリフェッチの停止", 1020], ["修正にはキャッシュ層の変更が要る", "キャッシュ層", 1050], ["Bo が水曜までに修正案を出す", "水曜まで", 1080]]],
        ["リリースツール", "tooling", [["リリーススクリプトにロールバックのスイッチを追加", "ロールバック", 1110, LONG_NAME], ["カナリアの割合を 10% に下げる", "10%", 1140]]],
      ]],
    ],
    actions: [["Bo", "警告文と回帰テストを追加する", 900], ["Ada", "負荷テストのレポートを共有する", 930]],
    open: [["上限は項目ごとか録音全体か", 1500]],
  },
};

/** The detail with an overview whose bullets are the points and decisions, one to one, as the hub writes them. */
function overviewFixture(lang) {
  const w = WORDS[lang];
  const detail = fixture();
  detail.meta.title = w.title; detail.meta.language = lang; detail.summary = w.headline;
  const line = ([text, strong, at, extra]) => ({ text, strong: [strong], at, ...(typeof extra === "string" ? { who: extra } : {}), ...(extra === true ? { decision: true } : {}) });
  const groups = w.groups.map(([title, topics]) => ({ title, topics: topics.map(([topic, icon, bullets]) => ({ title: topic, icon, bullets: bullets.map(line) })) }));
  const all = groups.flatMap((g) => g.topics.flatMap((t) => t.bullets)).sort((a, b) => a.at - b.at);
  // The transcript in the same language: a line at every time the minutes cite, saying what they say of it.
  const said = new Map([[0, w.title], ...[...w.open, ...w.actions.map(([who, what, at]) => [`${who}: ${what}`, at]), ...all.map((b) => [b.who ? `${b.who}: ${b.text}` : b.text, b.at])].map(([text, at]) => [at, text])]);
  detail.segments = [...said].sort(([a], [b]) => a - b).map(([start, text]) => ({ start, end: start + 25, text }));
  const item = (b) => ({ text: b.text, at: b.at, ...(b.who ? { who: b.who } : {}) });
  Object.assign(detail.minutes, {
    title: w.title, where: w.where, language: lang, attendees: ["Ada", "Bo", LONG_NAME],
    points: all.filter((b) => !b.decision).map(item), decisions: all.filter((b) => b.decision).map(item),
    actions: [
      { id: "a1", who: w.actions[0][0], what: w.actions[0][1], by: w.by, due_date: "2026-11-13", at: w.actions[0][2] },
      { id: "a2", who: w.actions[1][0], what: w.actions[1][1], at: w.actions[1][2], todo_id: "example-todo" },
    ],
    open: w.open.map(([text, at]) => ({ text, at })),
    overview: { headline: w.headline, conclusions: w.conclusions.map(([text, strong, at]) => ({ text, strong: [strong], at })), groups },
    markdown: `# ${w.title}\n\n${w.headline}\n`,
  });
  return detail;
}

const browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("FAIL recording minutes timed out after 10 minutes"); process.exit(1); }, 600_000);
const shots = [];
let runs = 0;

/**
 * One browser run. `width` is the window's; `large` picks the app's Large text size (ui/look.ts TEXT_SIZES, the
 * largest it offers) and `zoom` the browser's page zoom, which shows a window `width / zoom` CSS pixels wide at `zoom`
 * times the pixels. `columns` is how many topic cards a full row holds.
 */
async function run(theme, lang = "zh-Hans", width = 1440, { overview = false, large = false, zoom = 1, columns = 1, shot = width === 1440 } = {}) {
  const s = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/knowledge-kbdetail.json`, import.meta.url), "utf8"));
  const cssWidth = width / zoom;
  const context = await browser.newContext({ viewport: { width: cssWidth, height: 1800 }, colorScheme: theme, timezoneId: "Asia/Shanghai", deviceScaleFactor: zoom, reducedMotion: "reduce" });
  await context.addInitScript(({ theme, lang, large }) => {
    localStorage.setItem("persona.token", "fixture-token");
    localStorage.setItem("persona.language", lang);
    localStorage.setItem("persona.theme", theme);
    if (large) localStorage.setItem("persona.text-size", "large");
  }, { theme, lang, large });
  let detail = overview ? overviewFixture(lang) : fixture();
  const writes = [], errors = [];
  await context.route("**/*", (route) => new URL(route.request().url()).origin === base.origin ? route.continue() : route.abort());
  await context.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(/^\/api\/v1/, "");
    if (path === detailPath) return route.fulfill({ json: { detail } });
    if (path === minutesPath || path === `${minutesPath}/todos`) {
      assert.equal(request.method(), "POST");
      assert.equal(request.headers()["x-persona-manager"], "assistant");
      if (path.endsWith("/todos")) {
        const body = request.postDataJSON(); writes.push({ path, body });
        assert.deepEqual(body, { version: 2, actions: ["a1"] });
        detail.minutes.actions[0].todo_id = "example-added-todo";
        return route.fulfill({ json: { todos: [{ id: "example-added-todo" }], minutes: detail.minutes } });
      }
      assert.equal(request.postData(), null);
      writes.push({ path }); detail.meta.minutes_state = "pending";
      return route.fulfill({ status: 202, json: { state: "pending" } });
    }
    assert.ok(["GET", "HEAD"].includes(request.method()), `Unexpected write ${path}`);
    const data = {
      "/kb/recordings": { items: [{ kind: "recordings", id, name: detail.meta.title, relevance: 0 }], tags: [] },
      "/sessions": { sessions: [], total: 0, has_more: false, revision: "fixture" },
      "/machines": { machines: [] }, "/interactions": { interactions: [] },
      "/assistant": { conversations: [] }, "/todos": { todos: [] },
      "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
      "/agents/sessions": { sessions: [] }, "/agents/prefs": {},
      "/managers": { managers: [{ id: "assistant", name: "Persona", builtin: true, online: true }], default: "assistant" },
      "/personas": { personas: [] }, "/assistant/personas": { personas: [] }, "/workspaces": { workspaces: [] }, "/extensions": { extensions: [] },
    };
    return route.fulfill({ json: data[path] ?? {} });
  });
  await context.routeWebSocket(/\/api\/v1\/ws(?:\?|$)/, (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features: ["managers"], hub_name: "Example hub" }));
    ws.send(JSON.stringify({ type: "machines", machines: [] }));
    ws.onMessage((message) => { const event = JSON.parse(String(message)); if (event.type === "app_ping") ws.send(JSON.stringify({ type: "pong", id: event.id })); });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  page.on("pageerror", (err) => errors.push(err.message));
  await page.goto(new URL(detailPath, base).href);
  const minutes = page.getByRole("region", { name: s.minutes, exact: true });
  await minutes.waitFor();
  await minutes.getByRole("heading", { name: s.minutes_open, exact: true }).waitFor();
  if (large) assert.equal(await page.evaluate(() => document.documentElement.dataset.textSize), "large");
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(350);
  assert.equal(await page.locator("vite-error-overlay").count(), 0);
  const name = overview ? `overview-${lang}-${theme}-${width}${large ? "-large" : ""}${zoom !== 1 ? `-zoom${zoom * 100}` : ""}`
    : lang === "zh-Hans" && width === 1440 ? `minutes-${theme}` : `minutes-${theme}-${lang}-${width}`;
  const fits = async (label) => {
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${label}: no page overflow`);
    assert.ok(await minutes.evaluate((section) => section.scrollWidth <= section.clientWidth + 1), `${label}: no minutes overflow`);
    assert.ok(await minutes.locator("button").evaluateAll((buttons) => buttons.every((button) => {
      const range = document.createRange(); range.selectNodeContents(button);
      return getComputedStyle(button).whiteSpace === "nowrap" && range.getBoundingClientRect().width <= button.clientWidth + 1;
    })), `${label}: all button text fits on one line`);
    // The Card clips what runs past it (overflow-hidden), so each topic card and each line is checked on its own.
    if (overview) {
      const wide = await minutes.locator("[data-minutes-topic], [data-minutes-bullet] p, [data-minutes-conclusion] p, [data-minutes-overview] > p")
        .evaluateAll((els) => els.filter((el) => el.scrollWidth > el.clientWidth + 1).map((el) => el.textContent.slice(0, 40)));
      assert.deepEqual(wide, [], `${label}: no topic card or line runs past its box`);
    }
  };
  await fits(name);

  if (overview) {
    const w = WORDS[lang];
    // The contract's headings: the headline h3; conclusions, groups, action items and open questions h4; topics h5.
    const headings = await minutes.locator("h3, h4, h5").evaluateAll((hs) => hs.map((h) => `${h.tagName} ${h.textContent}`));
    assert.deepEqual(headings, [
      `H3 ${w.headline}`, `H4 ${s.minutes_conclusions}`,
      ...w.groups.flatMap(([group, topics]) => [`H4 ${group}`, ...topics.map(([topic]) => `H5 ${topic}`)]),
      `H4 ${s.minutes_actions}`, `H4 ${s.minutes_open}`,
    ], `${name}: headings`);
    assert.equal(await minutes.getByText(s.minutes_points, { exact: true }).count(), 0, `${name}: no Key points card`);
    // As many cards a row as cards of --minutes-card-min fit, never more than three (two in the page's column, at the
    // Large text size too, and one on a phone), and a card alone in its row takes all of it.
    const rows = await minutes.locator(".minutes-topic-grid").evaluateAll((grids) => grids.map((grid) => {
      const lines = new Map();
      for (const card of grid.children) {
        const box = card.getBoundingClientRect(), top = Math.round(box.top);
        lines.set(top, [...(lines.get(top) ?? []), box.width]);
      }
      return { width: grid.getBoundingClientRect().width, lines: [...lines.values()] };
    }));
    const expected = w.groups.map(([, topics]) => Array.from({ length: Math.ceil(topics.length / columns) }, (_, i) => Math.min(columns, topics.length - i * columns)));
    assert.deepEqual(rows.map((g) => g.lines.map((line) => line.length)), expected, `${name}: cards a row`);
    for (const g of rows) for (const line of g.lines) if (line.length === 1) assert.ok(Math.abs(line[0] - g.width) <= 1, `${name}: a card alone in its row takes all of it`);
    assert.ok(await minutes.locator("strong").count() >= 10, `${name}: bold places`);
    await minutes.getByText(s.minutes_ai_note, { exact: true }).waitFor();
  }
  // The whole page in one picture: the detail scrolls inside its own column, so the window grows to its height.
  const tall = await page.evaluate(() => {
    const scroller = document.querySelector("[data-recording-minutes]").closest(".overflow-y-auto");
    return Math.ceil(scroller.getBoundingClientRect().top + scroller.scrollHeight);
  });
  const shoot = async (file) => {
    await page.setViewportSize({ width: cssWidth, height: Math.min(Math.max(tall, 900), 16000) });
    await page.waitForTimeout(150);
    await page.screenshot({ path: join(output, `${file}.png`), fullPage: true });
    shots.push(`${file}.png`);
    await page.setViewportSize({ width: cssWidth, height: 1800 });
  };
  if (shot) await shoot(name);

  if (overview) {
    // The fold: a topic of six shows four, then "Show 2 more", which opens the rest in place, and "Show less".
    // Its name says which topic it opens.
    const folded = WORDS[lang].groups[1][1][1][0];
    const fold = { name: s.minutes_show_more_in_other.replace("{count}", "2").replace("{topic}", folded), exact: true };
    const topic = minutes.locator("article").filter({ has: page.getByRole("heading", { name: folded, exact: true }) });
    const more = topic.getByRole("button", fold);
    assert.equal(await more.textContent(), s.minutes_show_more_other.replace("{count}", "2"));
    assert.equal(await topic.locator("[data-minutes-bullet]").count(), 4);
    await more.click();
    assert.equal(await topic.locator("[data-minutes-bullet]").count(), 6);
    await topic.getByRole("button", { name: s.minutes_show_less_in.replace("{topic}", folded), exact: true }).waitFor();
    await fits(`${name} opened`);
    if (shot && !large && zoom === 1) {
      await topic.screenshot({ path: join(output, `${name}-opened-topic.png`) });
      shots.push(`${name}-opened-topic.png`);
    }
  }

  // Exercise the real app, not just its initial render: timestamp jump, add action, then regenerate.
  const timestamp = minutes.locator('[aria-controls="recording-line-900"]').first();
  await timestamp.click();
  await page.waitForFunction(() => document.activeElement?.id === "recording-line-900");
  const line = page.locator('[id="recording-line-900"]');
  assert.ok(await line.evaluate((element) => element.classList.contains("bg-accent-tint")));
  assert.ok(await line.evaluate((element) => { const rect = element.getBoundingClientRect(); return rect.top >= 0 && rect.bottom <= innerHeight; }), "Jump reveals the transcript line");
  await minutes.getByRole("button", { name: s.minutes_add_todo, exact: true }).click();
  await minutes.getByRole("button", { name: s.minutes_add_todo, exact: true }).waitFor({ state: "detached" });
  assert.equal(writes.filter((write) => write.path.endsWith("/todos")).length, 1);
  await minutes.getByRole("button", { name: s.minutes_regenerate, exact: true }).click();
  await minutes.getByText(s.minutes_updating, { exact: true }).waitFor();
  assert.equal(writes.filter((write) => write.path === minutesPath).length, 1);
  assert.deepEqual(errors, []);
  await context.close();
  runs++;
}

try {
  // The minutes as before (the legacy baseline), then the summary card: two cards a row in the 1440 window's column, at
  // the Large text size too and in that window at 200% zoom (the page, 720 px wide, drops the sidebar); one on a phone.
  for (const theme of ["light", "dark"]) await run(theme);
  for (const lang of ["en", "zh-Hans", "ja"]) await run("dark", lang, 390, { shot: true });
  if (process.env.E2E_ONLY !== "legacy") {
    for (const lang of ["zh-Hans", "en", "ja"]) {
      for (const theme of ["light", "dark"]) {
        for (const width of [1440, 390]) await run(theme, lang, width, { overview: true, columns: width === 1440 ? 2 : 1, shot: true });
      }
    }
    for (const lang of ["zh-Hans", "en"]) {
      for (const theme of ["light", "dark"]) {
        for (const width of [1440, 390]) await run(theme, lang, width, { overview: true, large: true, columns: width === 1440 ? 2 : 1, shot: true });
        await run(theme, lang, 1440, { overview: true, large: true, zoom: 2, columns: 2, shot: true });
      }
    }
  }
  console.log(`PASS recording minutes: ${runs} browser runs, ${shots.length} screenshots in ${output}; timestamps, mutations, overflow, single-line buttons, the summary card's headings, cards a row, line widths and fold checked`);
  for (const file of shots) console.log(`  ${file}`);
} catch (err) {
  console.error(`FAIL recording minutes: ${err?.stack ?? err}`);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
