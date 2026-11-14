// The Machines page and Overview at desktop widths (audit items 3, 25, 30 and 76; docs/web/web.md "Overview" and
// "Machines and settings"). On a hub that pages its Agents list, each computer card counts that computer's agents (the
// hub's own list, read here too), its state and its versions keep a gap between them, and no metric's value is cut
// ("↓ 2.58 M…", nor the widest a rate can be, "↓ 1023 MB/s"), and its metrics are one row or two columns, never three
// with one left alone (four in a row at 1280 and 1920 px); on Overview an extension's lone card is one of the page's
// two columns from 1440 px, not the whole width, and the whole width at 1280 px. When the list Overview counts from (the
// paged list with one assistant, the complete list with several) fails, its summary says so with Retry and no computer
// says "0 agents"; Retry brings the counts back. Light and dark screenshots of each.
//
// Needs the web page against the fake hub with the paged list, and Chrome. Run it with one assistant and with the
// fake hub's default roster (several assistants), each on its own port:
//
//   MOCK_SESSIONS_LIST=1 [MOCK_MANAGERS=1] PORT=<port> python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort --host 127.0.0.1 &)
//   node web/e2e/overview-machines-layout.mjs http://127.0.0.1:<web port> <artifact directory> [--channel chrome]
//
// The overview card's place, list and view are answered in the browser (an example extension's "Today's usage": three
// figures and a seven-day bar chart), with the UI-slot features added to the hub's hello, so the run needs no extension
// set up on the fake hub. Nothing is written to the hub. Prints one line per check and a verdict; exits 1 on a failed
// check and 2 on its three-minute timeout.
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
const channel = flag("--channel", "chrome");
if (!base || !out || out.startsWith("--")) {
  console.error("usage: node web/e2e/overview-machines-layout.mjs <web URL> <artifact directory> [--channel chrome]");
  process.exit(2);
}
const origin = new URL(base);
if (!["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname)) {
  console.error("a loopback web server backed by the fake hub is required");
  process.exit(2);
}
mkdirSync(out, { recursive: true });
const watchdog = setTimeout(() => { console.log("VERDICT TIMEOUT overview and machines layout"); process.exit(2); }, 180_000);

const failures = [];
const check = (ok, what, detail = "") => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail ? `: ${detail}` : ""}`);
  if (!ok) failures.push(what);
};
const hubGet = async (path) => (await fetch(`${base}/api/v1${path}`, { headers: { Authorization: "Bearer mock" } })).json();

// What the hub says each online computer runs: its agents that are not offline, without the assistant's conversations
// and without the child sessions folded under another (roots=1), every page of the complete list.
const expected = new Map();
for (let cursor = ""; ;) {
  const page = await hubGet(`/sessions?summary=1&limit=200&roots=1${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`);
  for (const s of page.sessions ?? []) {
    if (s.assistant || s.state === "offline") continue;
    expected.set(s.machine_id, (expected.get(s.machine_id) ?? 0) + 1);
  }
  if (!page.has_more) break;
  cursor = page.next_cursor;
}
const machines = (await hubGet("/machines")).machines ?? [];
check([...expected.values()].some((n) => n > 0), "the fake hub has agents on a computer", JSON.stringify(Object.fromEntries(expected)));
// Several assistants: Overview counts the current one's agents from the complete list; one: from the paged list, whose
// first page is every agent only when it has no more
const roster = await hubGet("/managers").catch(() => null);
const several = (roster?.managers ?? []).filter((m) => !m.installing).length > 1;
const firstPage = await hubGet("/sessions?summary=1&limit=50&roots=1");
const whole = several || !firstPage.has_more;
console.log(`roster: ${several ? "several assistants" : "one assistant"}; Overview counts computers: ${whole ? "yes" : "no, the paged list has more"}`);
const tag = several ? "several" : "one";

// the widest a rate gets before the next unit ("↓ 1023 MB/s", "↑ 1000 KB/s"), on the first online computer's card
const WIDEST = { net_rx: 1023 * 1024 * 1024, net_tx: 1000 * 1024 };
async function widestRate(page) {
  await page.route((u) => new URL(u).pathname === "/api/v1/metrics", async (r) => {
    try {
      const res = await r.fetch();
      const body = await res.json();
      const first = (body.machines ?? []).find((m) => m.latest && m.online !== false);
      if (first) first.latest = { ...first.latest, ...WIDEST };
      await r.fulfill({ response: res, json: body });
    } catch {
      // the page closed while the read was out (the next size's page): nothing to answer
      await r.abort().catch(() => {});
    }
  });
}

const DAYS = ["10-29", "10-30", "10-31", "11-01", "11-02", "11-03", "11-04"];
const VALUES = [2.2, 3.1, 1.1, 0.7, 4.0, 3.5, 1.5];
const TODAY = { sections: [{ items: [
  { type: "stat_grid", stats: [{ label: "Today", value: "1.5M", detail: "-56% vs yesterday", tone: "positive" }, { label: "Last 7 days", value: "17.0M" }, { label: "Cost", value: "$36.20" }] },
  { type: "chart", kind: "bar", unit: "M tokens", series: [{ name: "tokens", points: DAYS.map((label, i) => ({ label, value: VALUES[i] })) }] },
] }] };
async function overviewCard(page) {
  await page.routeWebSocket(/\/api\/v1\/ws/, (ws) => {
    const server = ws.connectToServer();
    server.onMessage((m) => {
      try {
        const o = JSON.parse(m);
        if (o.type === "hello") return ws.send(JSON.stringify({ ...o, features: [...new Set([...(o.features ?? []), "extensions", "extension_ui_slots"])] }));
      } catch { /* not JSON: pass it on */ }
      ws.send(m);
    });
    ws.onMessage((m) => server.send(m));
  });
  await page.route((u) => new URL(u).pathname === "/api/v1/extensions/-/ui", (r) => r.fulfill({ contentType: "application/json", body: JSON.stringify({ ui: [{ extension: "example-gateway", extension_name: "Example gateway", id: "today", title: "Today's usage", placement: "overview_card", size: "compact" }] }) }));
  await page.route((u) => new URL(u).pathname === "/api/v1/extensions/example-gateway/-/ui/today", (r) => r.fulfill({ contentType: "application/json", body: JSON.stringify({ view: TODAY }) }));
}

const browser = await chromium.launch({ channel, headless: true });
async function open(theme, width, height, route, setup) {
  const context = await browser.newContext({ viewport: { width, height } });
  await context.addInitScript((t) => { localStorage.setItem("persona.theme", t); localStorage.setItem("persona.language", "en"); }, theme);
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  // sign in with the link's token first, then open the page (lessons: wait for the sidebar before navigating)
  await page.goto(`${base}/settings?token=mock`);
  await page.waitForSelector("aside", { timeout: 20_000 });
  if (setup) await setup(page);
  await page.goto(`${base}${route}`);
  return { context, page, errors };
}

// Overview when the list it counts from fails: the paged list's reads with one assistant, the complete list's
// (summary=0, not a chat's exact lookup) with several; the hub's counts and everything else still answer.
const counted = (u) => {
  const url = new URL(u);
  if (url.pathname !== "/api/v1/sessions" || url.searchParams.has("counts") || url.searchParams.has("session_id")) return false;
  return several ? url.searchParams.get("summary") === "0" : url.searchParams.get("summary") !== "0";
};
async function failedList(theme) {
  const fail = (r) => r.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "boom" }) });
  const { context, page, errors } = await open(theme, 1280, 820, "/overview", (p) => p.route(counted, fail));
  const lines = () => page.$$eval("a[href^='/machines/'] [data-machine-line]", (spans) => spans.map((s) => ({ id: decodeURIComponent(s.closest("a").getAttribute("href").split("/")[2]), line: s.textContent })));
  const summary = () => page.$eval("h1 + div > p", (p) => p.textContent);
  await page.waitForFunction(() => document.querySelector("h1 + div > p")?.textContent === "Couldn't load what your agents are doing.", null, { timeout: 20_000 }).catch(() => {});
  check(await summary() === "Couldn't load what your agents are doing.", `${theme} ${tag}: Overview says it couldn't read the agents`, await summary());
  const retry = await page.$("h1 + div button:has-text('Retry')");
  check(retry !== null, `${theme} ${tag}: with Retry`);
  const live = () => page.$eval("h1 + div > p", (p) => p.querySelector("[role=status]")?.textContent ?? null);
  check(await live() === "Couldn't load what your agents are doing.", `${theme} ${tag}: the failure is in the line's live region`, String(await live()));
  const before = await lines();
  check(before.length > 0 && before.every((l) => !/\d+ agents?\b/.test(l.line)), `${theme} ${tag}: no computer gets a count it doesn't have`, before.map((l) => l.line).join(" | "));
  check(!(await page.textContent("body")).includes("All set"), `${theme} ${tag}: and no All set`);
  await page.screenshot({ path: join(out, `overview-failed-${tag}-${theme}.png`) });
  await page.unroute(counted);
  if (!retry) return context.close();
  await retry.click();
  // Retry says "Loading…" until the answer
  await page.waitForFunction(() => !["Couldn't load what your agents are doing.", "Loading…"].includes(document.querySelector("h1 + div > p")?.textContent ?? ""), null, { timeout: 10_000 }).catch(() => {});
  const after = await summary();
  check(!/Couldn't load|Loading/.test(after) && after.trim() !== "。" && after.trim() !== ".", `${theme} ${tag}: Retry reads them again`, after);
  // the counts sentence stays out of the live region, which would read it out again at each agent's change
  check(await live() === "", `${theme} ${tag}: the counts are not in the live region`, String(await live()));
  await page.waitForTimeout(300);
  for (const { id, line } of await lines()) {
    const m = machines.find((x) => x.id === id);
    if (!m?.online) continue;
    const n = expected.get(id) ?? 0;
    const want = whole ? `${n} agent${n === 1 ? "" : "s"}` : "";
    check(whole ? line.startsWith(want) : !/\d+ agents?\b/.test(line), `${theme} ${tag}: ${m.name} ${whole ? `counts its ${n} agents` : "gets no count of the first page only"} after Retry`, line);
  }
  check(errors.length === 0, `${theme} ${tag}: no page errors on Overview`, errors.join("; "));
  await page.screenshot({ path: join(out, `overview-retried-${tag}-${theme}.png`) });
  await context.close();
}

try {
  for (const theme of ["light", "dark"]) {
    for (const [width, height] of [[768, 1024], [1024, 768], [1280, 820], [1920, 1080]]) {
      const { context, page, errors } = await open(theme, width, height, "/machines", widestRate);
      await page.waitForSelector('[data-testid="machine-metrics-summary"]', { timeout: 20_000 });
      // read (a first read that failed, e.g. the hub's list changed between its pages, is tried again 5 s on)
      await page.waitForFunction(() => document.querySelectorAll('[data-agents]').length > 0 && [...document.querySelectorAll("[data-agents]")].every((e) => e.getAttribute("data-agents") === "yes"), null, { timeout: 20_000 }).catch(() => {});
      await page.waitForTimeout(500);
      const cards = await page.$$eval("section", (sections) => sections.filter((s) => s.querySelector("[data-testid=machine-metrics-summary]")).map((s) => {
        const footer = s.lastElementChild;
        const [state, right] = [...footer.children];
        const versions = right?.firstElementChild;
        const shown = versions && getComputedStyle(versions).display !== "none";
        // the text's own width against its box: scrollWidth rounds, and Chrome puts in "…" for half a pixel too much
        const values = [...s.querySelectorAll("[data-testid=machine-metrics-summary] > div > div:nth-child(2)")].map((v) => {
          const range = document.createRange();
          range.selectNodeContents(v);
          return { text: v.textContent, cut: range.getBoundingClientRect().width > v.getBoundingClientRect().width + 0.01 };
        });
        const grid = s.querySelector("[data-testid=machine-metrics-summary]");
        return {
          name: s.querySelector("h2")?.textContent ?? "",
          cells: grid.children.length,
          columns: getComputedStyle(grid).gridTemplateColumns.split(" ").length,
          agents: state.getAttribute("data-agents"),
          footer: state.textContent,
          gap: shown ? versions.getBoundingClientRect().left - state.getBoundingClientRect().right : null,
          values,
        };
      }));
      check(cards.length > 0, `${theme} ${width}: online computer cards are drawn`, String(cards.length));
      for (const c of cards) {
        const id = machines.find((m) => m.name === c.name)?.id;
        const n = expected.get(id) ?? 0;
        check(c.agents === "yes" && (n ? c.footer.startsWith(`${n} agent`) : c.footer === "No agents running"), `${theme} ${width}: ${c.name} counts its ${n} agents`, c.footer);
        if (c.gap !== null) check(c.gap >= 8, `${theme} ${width}: ${c.name}'s state and versions keep a gap`, `${c.gap.toFixed(1)} px`);
        const cut = c.values.filter((v) => v.cut).map((v) => v.text);
        check(cut.length === 0, `${theme} ${width}: ${c.name} shows every value whole`, cut.join(", ") || c.values.map((v) => v.text).join(" | "));
        // one row, or two columns: never three with the fourth alone below them; four in a row at 1280 and 1920 px
        const row = width >= 1280;
        check(row ? c.columns === c.cells : c.columns === c.cells || c.columns === 2, `${theme} ${width}: ${c.name}'s metrics are ${row ? "one row" : "one row or two columns"}`, `${c.columns} columns for ${c.cells}`);
      }
      check(cards.some((c) => c.values.some((v) => v.text === "↓ 1023 MB/s" && !v.cut)), `${theme} ${width}: the widest rate, ↓ 1023 MB/s, is shown whole`);
      check(errors.length === 0, `${theme} ${width}: no page errors on Machines`, errors.join("; "));
      await page.screenshot({ path: join(out, `machines-${width}-${theme}.png`) });
      await context.close();
    }
    for (const [width, height, lone] of [[1280, 820, false], [1440, 900, true], [1680, 1050, true], [1920, 1080, true]]) {
      const { context, page, errors } = await open(theme, width, height, "/overview", overviewCard);
      const card = await page.waitForSelector("[data-extension-card]", { timeout: 20_000 });
      await page.waitForSelector("[data-extension-card] svg", { timeout: 20_000 });
      const { cardWidth, pageWidth } = await card.evaluate((el) => ({ cardWidth: el.getBoundingClientRect().width, pageWidth: el.parentElement.getBoundingClientRect().width })); // its row spans the page's column
      const share = cardWidth / pageWidth;
      check(lone ? share < 0.55 : share > 0.95, `${theme} ${width}: the overview card is ${lone ? "one of two columns" : "the page's width"}`, `${Math.round(cardWidth)} of ${Math.round(pageWidth)} px`);
      check(errors.length === 0, `${theme} ${width}: no page errors on Overview`, errors.join("; "));
      await page.screenshot({ path: join(out, `overview-${width}-${theme}.png`) });
      await context.close();
    }
    await failedList(theme);
  }
} catch (e) {
  check(false, "the run finished", String(e));
} finally {
  await browser.close();
  clearTimeout(watchdog);
}
console.log(failures.length ? `VERDICT FAIL overview and machines layout: ${failures.length} failed` : "VERDICT PASS overview and machines layout");
process.exit(failures.length ? 1 : 0);
