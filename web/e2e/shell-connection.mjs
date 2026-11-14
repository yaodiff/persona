// The app shell's connection states, dead links and keyboard focus (web usability audit items 23, 35, 38, 39, 42, 86, 87
// and 89), in Chrome against the fake hub, with light and dark screenshots:
//   23  folding the sidebar to its rail by the keyboard leaves the focus on the rail's expand button, and opening it
//       again on the sidebar's collapse button (not on the page's body)
//   35  an Agent's page names its browser tab after the session's title, not "Agent"
//   38  a credential the hub stops taking while the page is open goes to the sign-in page with "signed out" within
//       seconds (the fake hub's /mock/reject), not "can't reach the hub" for a minute
//   39  /a/<an id the hub doesn't have> says the conversation isn't there, with the way back and no box left loading
//   42  an address that is no page says so and keeps the address, instead of drawing Overview under it; a knowledge
//       entry the hub doesn't have says so in the shared empty state, not on the entry's page with its id
//   86  the Offline state's English line wraps between words ("make sure the" is never cut inside "the")
//   87  the reconnect pill covers none of the page's fields, buttons or titles (the fake hub's /mock/down), on a phone
//       and a wide window
//   89  with the alert sound off (the default) the page's first key press makes no AudioContext; on, it makes one
//
// Needs an exclusively owned fake hub (the runner takes it down and refuses this browser's credential for a while) and
// the web page against it, and Chrome:
//
//   MOCK_TASKS=1 MOCK_SESSIONS_LIST=1 PORT=<port> python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
//   node web/e2e/shell-connection.mjs http://127.0.0.1:<web port> http://127.0.0.1:<port> <artifact directory>
//
// Prints one line per check and a final verdict; exits 1 on a failed check and 2 on its five-minute timeout.
import { mkdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

const [webArg, mockArg, out] = process.argv.slice(2);
const web = (webArg ?? "").replace(/\/$/, "");
const mock = (mockArg ?? "").replace(/\/$/, "");
const loopback = (u) => /^http:\/\/(127\.0\.0\.1|localhost|\[::1\]):\d+$/.test(u);
if (!loopback(web) || !loopback(mock) || !out) {
  console.error("usage: node web/e2e/shell-connection.mjs <loopback web origin> <loopback fake hub origin> <artifact directory>");
  process.exit(2);
}
mkdirSync(out, { recursive: true });
const watchdog = setTimeout(() => { console.log("VERDICT TIMEOUT shell connection"); process.exit(2); }, 300_000);

const locales = join(dirname(fileURLToPath(import.meta.url)), "../src/i18n/locales");
const words = (lang, ns) => JSON.parse(readFileSync(join(locales, lang, `${ns}.json`), "utf8"));
const zh = { app: words("zh-Hans", "app"), sidebar: words("zh-Hans", "sidebar"), notFound: words("zh-Hans", "not-found"), login: words("zh-Hans", "login"), agents: words("zh-Hans", "agents-agentsview"), kbDetail: words("zh-Hans", "knowledge-kbdetail") };
const en = { app: words("en", "app") };

const failures = [];
const check = (ok, what, detail = "") => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail ? `: ${detail}` : ""}`);
  if (!ok) failures.push(what);
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const hub = (path) => fetch(`${mock}${path}`).then((r) => r.text());
const hubJSON = async (path) => (await fetch(`${mock}/api/v1${path}`, { headers: { Authorization: "Bearer mock" } })).json();

const browser = await chromium.launch({ channel: "chrome", headless: true });
async function page({ theme = "light", lang = "zh-Hans", w = 1280, h = 820, touch = false, sound = false, signedIn = true } = {}) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, hasTouch: touch, isMobile: touch, deviceScaleFactor: 2, colorScheme: theme });
  await ctx.addInitScript(([theme, lang, sound, signedIn]) => {
    if (sessionStorage.getItem("e2e.init")) return;
    sessionStorage.setItem("e2e.init", "1");
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", lang);
    localStorage.removeItem("persona.sidebarCollapsed");
    if (signedIn) localStorage.setItem("persona.token", "mock");
    if (sound) localStorage.setItem("persona.attentionSound", "1");
  }, [theme, lang, sound, signedIn]);
  // every AudioContext the page makes, counted
  await ctx.addInitScript(() => {
    window.__audioContexts = 0;
    const Real = window.AudioContext;
    if (Real) window.AudioContext = class extends Real { constructor(...a) { super(...a); window.__audioContexts++; } };
  });
  const p = await ctx.newPage();
  p.on("pageerror", (e) => console.log(`page error: ${e.message}`));
  return { ctx, p };
}
const online = (p) => p.waitForFunction(() => !document.querySelector("[data-connection-pill]") && document.body.innerText.length > 50, null, { timeout: 30_000 });
const shoot = (p, name) => p.screenshot({ path: join(out, `${name}.png`) });
/** One block of checks: a throw in it fails it and the run goes on to the next. */
async function section(name, run) {
  try {
    await run();
  } catch (e) {
    check(false, name, e.message.split("\n")[0]);
  }
}
const active = (p) => p.evaluate(() => document.activeElement === document.body ? "BODY" : (document.activeElement?.getAttribute("aria-label") ?? document.activeElement?.tagName));

try {
  await hub("/mock/down?s=0");
  await hub("/mock/reject?s=0");

  // 23: the focus across a fold of the sidebar, by the keyboard
  for (const theme of ["light", "dark"]) await section(`23 ${theme}`, async () => {
    const { ctx, p } = await page({ theme });
    await p.goto(`${web}/todos`);
    await p.waitForSelector(`button[aria-label="${zh.sidebar.collapse}"]`);
    await p.focus(`button[aria-label="${zh.sidebar.collapse}"]`);
    await p.keyboard.press("Enter");
    await p.waitForSelector(`button[aria-label="${zh.sidebar.expand}"]`);
    check(await active(p) === zh.sidebar.expand, `23 ${theme}: folded by Enter, the focus is on the rail's expand button`, await active(p));
    await shoot(p, `23-folded-focus-${theme}`);
    await p.focus(`button[aria-label="${zh.sidebar.expand}"]`); // where the keyboard should be (it is, unless the check above failed)
    await p.keyboard.press("Enter");
    await p.waitForSelector(`button[aria-label="${zh.sidebar.collapse}"]`);
    check(await active(p) === zh.sidebar.collapse, `23 ${theme}: opened by Enter, the focus is on the sidebar's collapse button`, await active(p));
    await shoot(p, `23-opened-focus-${theme}`);
    await ctx.close();
  });

  // 35: two Agents' pages, each tab named after its session
  await section("35", async () => {
    const rows = ((await hubJSON("/sessions?limit=50")).sessions ?? []).filter((s) => s.title?.trim() && !s.folded_under && !s.slot_id && s.kind !== "assistant");
    const { ctx, p } = await page();
    for (const s of rows.slice(0, 2)) {
      await p.goto(`${web}/s/${encodeURIComponent(s.machine_id)}/${encodeURIComponent(s.session_id)}`);
      await p.waitForFunction((title) => document.title.includes(title), s.title.trim(), { timeout: 15_000 }).catch(() => {});
      const title = await p.title();
      check(title.replace(/^\(\d+\) /, "") === `${s.title.trim()} · Persona`, `35: the tab of /s/…/${s.session_id.slice(0, 8)} is named after its session`, title);
    }
    check(rows.length >= 2, "35: the fake hub lists two titled sessions to open", String(rows.length));
    await ctx.close();
  });

  // 42 and 39: dead links
  for (const theme of ["light", "dark"]) await section(`42 and 39 ${theme}`, async () => {
    const { ctx, p } = await page({ theme });
    await p.goto(`${web}/foo-bar-unknown`);
    const shown = await p.waitForSelector(`text=${zh.notFound.page_title}`, { timeout: 15_000 }).then(() => true, () => false);
    check(shown, `42 ${theme}: an unknown address says the page isn't there`);
    check(new URL(p.url()).pathname === "/foo-bar-unknown", `42 ${theme}: and keeps the address`, p.url());
    await shoot(p, `42-unknown-${theme}`);
    await p.click(`main button:has-text("${zh.notFound.page_back}")`);
    check(await p.waitForURL((u) => u.pathname === "/overview", { timeout: 10_000 }).then(() => true, () => false), `42 ${theme}: with the way to Overview`, p.url());
    await p.goto(`${web}/kb/people/does-not-exist`);
    const kbGone = await p.waitForFunction((title) => document.querySelector("main [role=status]")?.textContent?.includes(title), zh.notFound.kb_title, { timeout: 15_000 }).then(() => true, () => false);
    check(kbGone, `42 ${theme}: a knowledge entry the hub doesn't have says it isn't there`);
    // the entry's side of the page (beside the list on a wide window)
    const kbPage = await p.evaluate(() => {
      const side = document.querySelector("main [role=status]")?.closest("section");
      return { h1: side?.querySelectorAll("h1").length, disabled: side?.querySelectorAll("button:disabled").length };
    });
    check(kbPage.h1 === 0 && kbPage.disabled === 0, `42 ${theme}: not the entry's page (no title made of its id, no buttons that can't be used)`, JSON.stringify(kbPage));
    await sleep(400); // the empty state fades in
    await shoot(p, `42-kb-gone-${theme}`);
    await p.goto(`${web}/a/does-not-exist`);
    const missing = await p.waitForFunction((title) => document.querySelector("main [role=status]")?.textContent?.includes(title), zh.notFound.agent_title, { timeout: 15_000 }).then(() => true, () => false);
    check(missing, `39 ${theme}: /a/<missing> says the conversation isn't there`);
    const agentPage = await p.evaluate((title) => ({ h1: document.querySelector("main h1")?.textContent, said: document.querySelector("main")?.innerText.split(title).length - 1 }), zh.notFound.agent_title);
    check(agentPage.h1 === zh.agents.title && agentPage.said === 1, `39 ${theme}: the header names the list, and the missing conversation is said once`, JSON.stringify(agentPage));
    check(await p.locator("main textarea").count() === 0, `39 ${theme}: no message box left loading`);
    await sleep(400); // the empty state fades in
    await shoot(p, `39-missing-agent-${theme}`);
    await p.click(`main button:has-text("${zh.notFound.agent_back}")`);
    check(await p.waitForURL((u) => u.pathname === "/a", { timeout: 10_000 }).then(() => true, () => false), `39 ${theme}: with the way back to the list`, p.url());
    await ctx.close();
  });

  // 89: the first key press with the alert sound off, then on
  for (const sound of [false, true]) await section(`89 sound ${sound ? "on" : "off"}`, async () => {
    const { ctx, p } = await page({ sound });
    await p.goto(`${web}/overview`);
    await online(p);
    await p.keyboard.press("Shift");
    await sleep(300);
    const made = await p.evaluate(() => window.__audioContexts);
    check(made === (sound ? 1 : 0), `89: alert sound ${sound ? "on" : "off"}, the first key press makes ${sound ? "one AudioContext" : "no AudioContext"}`, String(made));
    await ctx.close();
  });

  // 87: the reconnect pill over the to-dos on a phone and in a wide window
  for (const [theme, w, h, touch] of [["dark", 390, 844, true], ["light", 390, 844, true], ["light", 1280, 820, false], ["dark", 1280, 820, false]]) await section(`87 ${theme} ${w}px`, async () => {
    const { ctx, p } = await page({ theme, w, h, touch });
    await p.goto(`${web}/todos`);
    await online(p);
    await sleep(1500);
    await hub("/mock/down?s=60");
    await p.waitForSelector("[data-connection-pill]", { timeout: 45_000 });
    await sleep(700); // the strip has opened
    const covered = await p.evaluate(() => {
      const pill = document.querySelector("[data-connection-pill]").getBoundingClientRect();
      const hits = [];
      for (const el of document.querySelectorAll("main input, main button, main a, main [role=tab], main h1, main h2")) {
        if (el.closest("[data-connection-pill]")) continue;
        const r = el.getBoundingClientRect();
        if (!r.width || !r.height || getComputedStyle(el).visibility === "hidden") continue;
        if (r.left < pill.right && r.right > pill.left && r.top < pill.bottom && r.bottom > pill.top) hits.push(`${el.tagName} ${(el.getAttribute("aria-label") || el.getAttribute("placeholder") || el.textContent || "").trim().slice(0, 24)}`);
      }
      return hits;
    });
    check(covered.length === 0, `87 ${theme} ${w}px: the pill covers none of the page's fields, buttons or titles`, covered.join(", "));
    await shoot(p, `87-pill-todos-${w}-${theme}`);
    await p.click("[data-connection-pill]");
    await sleep(400);
    const details = await p.evaluate(() => {
      const d = document.querySelector("[data-connection-details]")?.getBoundingClientRect();
      if (!d) return "none";
      const top = document.elementFromPoint(d.left + d.width / 2, d.top + d.height / 2);
      return top?.closest("[data-connection-details]") ? "on top" : "under the page";
    });
    check(details === "on top", `87 ${theme} ${w}px: its details open over the page`, details);
    await shoot(p, `87-pill-details-${w}-${theme}`);
    await hub("/mock/down?s=0");
    await ctx.close();
  });

  // 86: the Offline state in English, the hub down from the start
  for (const theme of ["light", "dark"]) await section(`86 ${theme}`, async () => {
    await hub("/mock/down?s=30");
    const { ctx, p } = await page({ theme, lang: "en" });
    await p.goto(`${web}/overview`);
    await p.waitForSelector(`text=${en.app.offline_title}`, { timeout: 40_000 });
    const cut = await p.evaluate((text) => {
      const el = [...document.querySelectorAll("main div")].find((d) => d.childNodes.length === 1 && d.textContent === text);
      if (!el) return ["line not found"];
      const node = el.firstChild;
      const range = document.createRange();
      const cuts = [];
      let top = null;
      for (let i = 0; i < text.length; i++) {
        range.setStart(node, i);
        range.setEnd(node, i + 1);
        const r = range.getClientRects()[0];
        if (!r) continue;
        if (top !== null && r.top > top + 2 && text[i - 1] !== " " && text[i] !== " ") cuts.push(`${text.slice(Math.max(0, i - 8), i)}|${text.slice(i, i + 8)}`);
        top = r.top;
      }
      return cuts;
    }, en.app.offline_detail);
    check(cut.length === 0, `86 ${theme}: the Offline line wraps only between words`, cut.join(", "));
    await shoot(p, `86-offline-en-${theme}`);
    await ctx.close();
    await hub("/mock/down?s=0");
  });

  // 38: the credential refused while the page is open
  await section("38", async () => {
    await hub("/mock/down?s=0");
    const { ctx, p } = await page();
    await p.goto(`${web}/overview`);
    await online(p);
    await sleep(1000);
    await hub("/mock/reject?s=40");
    const t0 = Date.now();
    const signedOut = await p.waitForSelector(`text=${zh.app.signed_out}`, { timeout: 25_000 }).then(() => true, () => false);
    check(signedOut, "38: a refused credential goes to the sign-in page with “signed out” within 25 s", signedOut ? `${Math.round((Date.now() - t0) / 1000)} s` : "still on the page");
    await shoot(p, "38-signed-out-light");
    await ctx.close();
    await hub("/mock/reject?s=0");
  });
} catch (e) {
  check(false, "the run", e.message);
} finally {
  await hub("/mock/down?s=0").catch(() => {});
  await hub("/mock/reject?s=0").catch(() => {});
  await browser.close();
  clearTimeout(watchdog);
}
console.log(failures.length ? `VERDICT FAIL shell connection: ${failures.length} failed (${failures.join("; ")})` : "VERDICT PASS shell connection");
process.exit(failures.length ? 1 : 0);
