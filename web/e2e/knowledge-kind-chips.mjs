// The knowledge list's kind chips stay put (docs/web/web.md "Knowledge base"): picking another kind moves no chip, while
// its list loads and once it is drawn, at a wide window and on a phone, light and dark, in Chinese and English. A count
// on the selected chip used to change its width, so the row re-wrapped and the chips after it jumped.
//
// Needs the web page against the fake hub, and Chrome:
//
//   PORT=18794 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18794 npx vite --port 5294 --strictPort &)
//   node web/e2e/knowledge-kind-chips.mjs http://127.0.0.1:5294 <artifact directory> [--channel chrome]
//
// Only reads: the runner picks each kind in turn by clicking its chip, holds that kind's list read (so the kind is still
// loading, with Loading under the chips), measures every chip's box, lets the read through and measures again once the
// list is drawn. Prints one line per check and a verdict; exits 1 on a failed check and 2 on its three-minute timeout.
// Screenshots of the chip row with the first and the last kind picked, and of a kind while it loads, go to the artifact
// directory.
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
  console.error("usage: node web/e2e/knowledge-kind-chips.mjs <web URL> <artifact directory> [--channel chrome]");
  process.exit(2);
}
mkdirSync(out, { recursive: true });
const watchdog = setTimeout(() => { console.log("VERDICT TIMEOUT knowledge kind chips"); process.exit(2); }, 180_000);

const failures = [];
const check = (ok, what, detail = "") => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail ? `: ${detail}` : ""}`);
  if (!ok) failures.push(what);
};
const KINDS = ["projects", "topics", "chats", "people", "docs", "meetings", "events", "recordings"];
const NAMES = {
  "zh-Hans": ["项目", "主题", "会话", "人", "文档", "会议", "日程", "录音"],
  en: ["Projects", "Topics", "Chats", "People", "Docs", "Meetings", "Events", "Recordings"],
};
const LOADING = { "zh-Hans": "加载中", en: "Loading" };
const LAYOUTS = [
  { name: "wide", viewport: { width: 1280, height: 820 } },
  { name: "phone", viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true },
];

/** Every chip's text and box, rounded to the half pixel. */
const chips = (page) => page.$$eval('[role="tablist"] [role="tab"]', (els) => els.map((e) => {
  const r = e.getBoundingClientRect();
  return { text: e.textContent, box: [r.x, r.y, r.width, r.height].map((v) => Math.round(v * 2) / 2).join(",") };
}));

const browser = await chromium.launch({ channel, headless: true });
try {
  for (const lang of ["zh-Hans", "en"]) {
    for (const theme of ["light", "dark"]) {
      for (const layout of LAYOUTS) {
        if (lang === "en" && theme === "dark") continue; // English once, light: the widths are what it checks
        const label = `${lang} ${theme} ${layout.name}`;
        const context = await browser.newContext({ viewport: layout.viewport, hasTouch: layout.hasTouch, isMobile: layout.isMobile, deviceScaleFactor: 2 });
        await context.addInitScript(([th, lg]) => {
          localStorage.setItem("persona.theme", th);
          localStorage.setItem("persona.language", lg);
        }, [theme, lang]);
        const page = await context.newPage();
        await page.goto(`${base}/kb/projects?token=mock`);
        await page.waitForSelector('[role="tablist"] [role="tab"][aria-selected="true"]', { timeout: 20_000 });
        await page.waitForTimeout(800);
        const first = await chips(page);
        check(JSON.stringify(first.map((c) => c.text)) === JSON.stringify(NAMES[lang]), `${label}: each chip reads its kind's name alone`, first.map((c) => c.text).join(" "));
        const clip = async (name) => {
          // no chip row (a build that hides it while a kind loads): the top of the list pane, as wide as the window allows
          const r = (await (await page.$('[role="tablist"]'))?.boundingBox()) ?? { x: 12, y: 120, width: Math.min(layout.viewport.width, 420) - 60, height: 60 };
          await page.screenshot({ path: join(out, `kb-chips-${lang}-${theme}-${layout.name}-${name}.png`), clip: { x: Math.max(0, r.x - 12), y: Math.max(0, r.y - 60), width: Math.min(layout.viewport.width, r.width + 60), height: r.height + 220 } });
        };
        await clip("projects");
        const loading = page.getByRole("status").filter({ hasText: LOADING[lang] });
        const moved = [];
        const unheld = [];
        for (const kind of KINDS.slice(1)) {
          const i = KINDS.indexOf(kind);
          // Hold this kind's list read until its chips are measured: the mock answers in milliseconds, so without the
          // hold the list would usually be drawn before the first measure.
          let release;
          const held = new Promise((r) => (release = r));
          let seen = false;
          const list = new RegExp(`/api/v1/kb/${kind}(\\?.*)?$`);
          await page.route(list, async (route) => {
            seen = true;
            await held;
            await route.continue().catch(() => {});
          });
          await page.click(`[role="tablist"] [role="tab"]:nth-child(${i + 1})`);
          const picked = await page.waitForSelector(`[role="tablist"] [role="tab"]:nth-child(${i + 1})[aria-selected="true"]`, { timeout: 2_000 }).then(() => true, () => false);
          if (!picked) moved.push(`${kind} while it loads: its chip is not shown picked`);
          const shows = await loading.first().waitFor({ state: "visible", timeout: 5_000 }).then(() => true, () => false);
          // the read is held, so this waits safely: the pressed chip's scale and the colours finish their 150 ms change
          await page.mouse.move(0, 0);
          await page.waitForTimeout(250);
          const now = await chips(page);
          if (!seen || !shows) unheld.push(`${kind}${seen ? "" : " (read not held)"}${shows ? "" : " (no Loading)"}`);
          if (kind === "recordings") await clip("recordings-loading");
          release();
          await loading.first().waitFor({ state: "detached", timeout: 10_000 }).catch(() => unheld.push(`${kind} (still loading)`));
          await page.unroute(list);
          await page.waitForTimeout(300);
          const settled = await chips(page);
          for (const [when, boxes] of [["while it loads", now], ["once drawn", settled]]) {
            if (boxes.length !== first.length) moved.push(`${kind} ${when}: ${boxes.length} of ${first.length} chips shown`);
            boxes.forEach((c, j) => {
              if (c.box !== first[j].box || c.text !== first[j].text) moved.push(`${kind} ${when}: ${first[j].text} ${first[j].box} → ${c.text} ${c.box}`);
            });
          }
        }
        check(unheld.length === 0, `${label}: each kind loads with Loading under the chips while its list read is held`, unheld.join("; "));
        check(moved.length === 0, `${label}: no chip moves as each kind is picked, while it loads and once drawn`, moved.slice(0, 3).join("; "));
        await clip("recordings");
        await context.close();
      }
    }
  }
} catch (e) {
  check(false, "run", String(e));
} finally {
  clearTimeout(watchdog);
  await browser.close();
}
console.log(failures.length ? `VERDICT FAIL knowledge kind chips (${failures.length} failed)` : "VERDICT PASS knowledge kind chips");
process.exit(failures.length ? 1 : 0);
