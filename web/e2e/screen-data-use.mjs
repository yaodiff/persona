// The live screen's data use with quality tiers in a real browser (#6558), against the mock's helper with tiers and its
// own Screen Sharing:
//   PORT=<port> MOCK_COMPUTER=ready MOCK_SCREEN=fake MOCK_SCREEN_VIDEO=1 MOCK_SCREEN_VIDEO_TIERS=1 python3 scripts/mock/mock_hub.py
//   (cd web && VITE_SCREEN_VIDEO=1 PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port>)
//   node web/e2e/screen-data-use.mjs <web URL> <mock hub URL> [--shots <dir>] [--lang en|zh-Hans|ja] [--executable <chromium>]
// Checks, on mobile data (navigator.connection says cellular), as on the iPhone: the stream starts at mobile data's
// 4 Mbit/s (tiers aren't known before the helper's hello), and once the hello lists them, the page's tiers pick a tier
// under Data Saver (one pixel per point, at most 1.5 Mbit/s) and send it in an update, and the card asks; Clearer
// sends a tier above 1.5 and at most 4 Mbit/s, is kept, and the card goes; ⋯ has the choice, and Data Saver there
// goes back under 1.5; the next page asks no more. The card closed unanswered keeps Data Saver. On a phone-sized screen
// the card fits. With the browser's Data Saver on: 1.5 and no card. On Wi-Fi: a start tier, the stats panel shows the
// tier and why, and Quality sends another tier and says so. No page error.
// Ends with one line: PASS, FAIL or TIMEOUT.
import { mkdir, readFile } from "node:fs/promises";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i < 0 ? undefined : args.splice(i, 2)[1];
};
const shots = flag("--shots");
const lang = flag("--lang") ?? "en";
const executable = flag("--executable");
const [web, hub] = args;
if (!web || !hub) {
  console.log("usage: node web/e2e/screen-data-use.mjs <web URL> <mock hub URL> [--shots <dir>] [--lang en|zh-Hans|ja] [--executable <chromium>]");
  process.exit(2);
}
if (shots) await mkdir(shots, { recursive: true });
const deadline = setTimeout(() => {
  console.log("TIMEOUT screen data use: no verdict in 180 s");
  process.exit(1);
}, 180_000);
const fail = (why) => {
  throw new Error(why);
};
const mock = async () => (await fetch(`${hub}/mock/screen-video`)).json();
const words = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/screen-video.json`, import.meta.url), "utf8"));
// What a tier update looks like under each choice: Data Saver one pixel per point and at most 1.5 Mbit/s; Clearer more.
const saver = (u) => u.scale !== undefined && u.scale <= 1 && u.max_kbps <= 1_500;
const clearer = (u) => u.scale !== undefined && u.max_kbps > 1_500 && u.max_kbps <= 4_000;

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
const errors = [];
let current = null;
try {
  // A browser on mobile data (or with its Data Saver on), as Chrome's navigator.connection says it.
  const context = async (scheme, { saveData = false, phone = false, type = "cellular" } = {}) => {
    const c = await browser.newContext({
      viewport: phone ? { width: 390, height: 844 } : { width: 1280, height: 800 },
      ...(phone ? { isMobile: true, hasTouch: true, deviceScaleFactor: 3 } : {}),
      colorScheme: scheme,
      serviceWorkers: "block",
    });
    await c.addInitScript((l) => localStorage.setItem("persona.language", l), lang);
    await c.addInitScript(([kind, save]) => {
      const net = Object.assign(new EventTarget(), { type: kind, saveData: save });
      Object.defineProperty(Navigator.prototype, "connection", { get: () => net, configurable: true });
    }, [type, saveData]);
    return c;
  };
  const open = async (c) => {
    const page = await c.newPage();
    current = page;
    page.setDefaultTimeout(20_000);
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${web}/screen/macbook#token=mock`);
    // the sign-in form, or (a kept sign-in) the live page
    await page.waitForSelector("input[type=password], [data-testid=screen-stats-toggle]");
    if (await page.$("input[type=password]")) {
      await page.fill("input[autocomplete=username]", "me");
      await page.fill("input[type=password]", "mock-pass");
      await page.click("button[type=submit]");
    }
    await page.waitForSelector("[data-testid=screen-stats-toggle]");
    return page;
  };
  const videoShows = (page) =>
    page.waitForFunction(() => {
      const v = document.querySelector("[data-testid=screen-video-picture]");
      if (!v || v.style.display === "none" || !v.width) return false;
      const px = v.getContext("2d").getImageData(Math.floor(v.width / 2), Math.floor(v.height / 2), 1, 1).data;
      return px[3] > 0 && px[0] + px[1] + px[2] > 0;
    });
  // An update that `fits` reached the helper after `from` updates.
  const updated = async (page, from, fits, what) => {
    let updates = [];
    for (let i = 0; i < 25; i++) {
      updates = (await mock()).updates.slice(from);
      if (updates.some(fits)) return updates.find(fits);
      await page.waitForTimeout(200);
    }
    fail(`no update for ${what}: ${JSON.stringify(updates)}`);
  };
  const lastStart = async () => (await mock()).starts.at(-1);

  for (const scheme of ["light", "dark"]) {
    const c = await context(scheme);
    const opened = (await mock()).updates.length;
    const page = await open(c);
    await videoShows(page);
    await page.waitForSelector("[data-testid=screen-mobile-data]");
    if ((await lastStart()).max_kbps !== 4_000) fail(`mobile data started at ${(await lastStart()).max_kbps} kbps, not 4000`);
    await updated(page, opened, saver, "Data Saver's tier"); // tiers in the hello: Data Saver until answered
    if (shots) await page.screenshot({ path: `${shots}/card-${scheme}.png` });
    if (scheme === "light") {
      const from = (await mock()).updates.length;
      await page.click("[data-testid=screen-mobile-data-clearer]");
      await updated(page, from, clearer, "Clearer's tier");
      await page.waitForSelector("[data-testid=screen-mobile-data]", { state: "detached" });
      if ((await page.evaluate(() => localStorage.getItem("persona.screen.mobileData"))) !== "clearer") fail("Clearer was not kept");
      // ⋯ has the choice; Data Saver from there sends 1.5 again
      await page.click("[data-testid=screen-video]");
      await page.waitForSelector("[role=menuitemradio]");
      await page.waitForTimeout(400); // the menu fades in
      if (shots) await page.screenshot({ path: `${shots}/menu-${scheme}.png` });
      const rows = await page.$$("[role=menuitemradio]");
      if (rows.length !== 7) fail(`⋯ has ${rows.length} choices, not Video, VNC, three qualities and two for mobile data`);
      if ((await rows[5].getAttribute("aria-checked")) !== "true") fail("Clearer isn't the checked mobile-data choice in ⋯");
      const again = (await mock()).updates.length;
      await rows[6].click();
      await updated(page, again, saver, "Data Saver's tier from ⋯");
      // the next page asks no more, and goes to the choice once the hello lists tiers
      const nextFrom = (await mock()).updates.length;
      const next = await open(c);
      await videoShows(next);
      await updated(next, nextFrom, saver, "Data Saver's tier on the next page");
      await next.waitForTimeout(500);
      if (await next.$("[data-testid=screen-mobile-data]")) fail("the card asked again after an answer");
    } else {
      // closed unanswered: the card goes, and the picture stays on Data Saver
      const from = (await mock()).updates.length;
      await page.click("[data-testid=screen-mobile-data-close]");
      await page.waitForSelector("[data-testid=screen-mobile-data]", { state: "detached" });
      await page.waitForTimeout(500);
      const updates = (await mock()).updates.slice(from);
      if (updates.some((u) => u.max_kbps > 1_500)) fail(`closing the card left Data Saver: ${JSON.stringify(updates)}`);
    }
    await c.close();
  }

  // A phone-sized screen: the card fits, each button on one line.
  {
    const c = await context("light", { phone: true });
    const page = await open(c);
    await videoShows(page);
    const card = await page.waitForSelector("[data-testid=screen-mobile-data]");
    // inside the window, and no button's words cut short (the touch targets may reach past the edge; they draw nothing)
    const fits = await card.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const cut = [...el.querySelectorAll("button span")].filter((s) => s.scrollWidth > s.clientWidth).map((s) => s.textContent);
      return r.left >= 0 && r.right <= window.innerWidth ? cut : [`card at ${r.left}–${r.right} of ${window.innerWidth}`];
    });
    if (fits.length) fail(`the card doesn't fit a phone: ${fits.join(", ")}`);
    if (shots) await page.screenshot({ path: `${shots}/card-phone-light.png` });
    await c.close();
  }

  // The browser's Data Saver on: always Data Saver, nothing asked.
  {
    const c = await context("light", { saveData: true });
    const page = await open(c);
    await videoShows(page);
    await page.waitForTimeout(1000);
    if (await page.$("[data-testid=screen-mobile-data]")) fail("a card with the browser's Data Saver on");
    if ((await lastStart()).max_kbps !== 1_500) fail(`the browser's Data Saver started at ${(await lastStart()).max_kbps} kbps, not 1500`);
    await c.close();
  }

  // On Wi-Fi with tiers: a start tier; the stats panel shows it and why; Quality sends another tier and says so.
  for (const scheme of ["light", "dark"]) {
    const c = await context(scheme, { type: "wifi" });
    const opened = (await mock()).updates.length;
    const page = await open(c);
    await videoShows(page);
    const first = await updated(page, opened, (u) => u.scale !== undefined, "a start tier");
    if (first.max_kbps > 12_000) fail(`the start tier is over Wi-Fi's 12 Mbit/s: ${JSON.stringify(first)}`);
    if (!(await page.$("[data-testid=screen-stats]"))) await page.click("[data-testid=screen-stats-toggle]");
    const tier = await page.waitForSelector("[data-testid=screen-stats-tier]");
    const reason = async () => (await page.textContent("[data-testid=screen-stats-tier-reason]")) ?? "";
    if (!(await tier.textContent())?.includes(words.quality_auto)) fail(`the tier line doesn't say Auto: ${await tier.textContent()}`);
    if (!(await reason()).startsWith(words.tier_start)) fail(`the first tier's reason isn't the start: ${await reason()}`);
    if (scheme === "light") {
      const from = (await mock()).updates.length;
      await page.click("[data-testid=screen-video]");
      await page.waitForSelector("[role=menuitemradio]");
      await (await page.$$("[role=menuitemradio]"))[3].click(); // Sharper
      await updated(page, from, (u) => u.scale !== undefined, "Sharper's tier");
      // the label follows the click at once; the reason once the stream's tier changed
      await page.waitForFunction((w) => document.querySelector("[data-testid=screen-stats-tier]")?.textContent?.includes(w), words.quality_sharper);
      const chose = await page
        .waitForFunction((w) => document.querySelector("[data-testid=screen-stats-tier-reason]")?.textContent?.startsWith(w), words.tier_choice, { timeout: 5_000 })
        .then(() => true, () => false);
      if (!chose) fail(`after Quality the reason isn't the choice: ${await reason()}; updates since: ${JSON.stringify((await mock()).updates.slice(from))}`);
    }
    await page.waitForTimeout(1200); // a reading: the panel's numbers
    if (shots) await page.screenshot({ path: `${shots}/stats-tier-${scheme}.png` });
    await c.close();
  }

  if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
  console.log("PASS screen data use: on mobile data Data Saver's tier first and the card asks; Clearer and Data Saver send tiers, are kept, and sit in ⋯; closed keeps Data Saver; fits a phone; the browser's Data Saver asks nothing; on Wi-Fi the stats show the tier, and Quality moves it");
} catch (e) {
  console.log(`FAIL screen data use: ${e instanceof Error ? e.message : e}`);
  if (shots && current) await current.screenshot({ path: `${shots}/failed.png` }).catch(() => {});
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
