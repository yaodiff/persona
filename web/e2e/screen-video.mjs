// The live screen's video channel in a real browser (#6558), against the mock hub's helper side (MOCK_SCREEN_VIDEO=1:
// the HEVC or H.264 fixture, picked from start.codecs) and its own Screen Sharing:
//   PORT=<port> MOCK_COMPUTER=ready MOCK_SCREEN=fake MOCK_SCREEN_VIDEO=1 python3 scripts/mock/mock_hub.py
//   (cd web && VITE_SCREEN_VIDEO=1 PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port>)
//   node web/e2e/screen-video.mjs <web URL> <mock hub URL> [--shots <dir>] [--lang en|zh-Hans|ja] [--executable <chromium>]
// Checks: after signing in, the video takes the picture (its canvas drawn, noVNC's clear, its update requests held) and
// a report goes back for its frames; Connection stats shows Video with the segments, the codec and the size; the Mac
// locking says so and VNC shows until it's back; the stream dropping says video stopped. In a browser with no video
// decoder, no Picture switcher and the panel says why; a decoder that fails on the stream: the panel says it couldn't
// start, and video isn't tried again on that page (no switcher, no line promising it). Data use without tiers: a desktop
// starts at 12 Mbit/s; on mobile data (navigator.connection says cellular) at 4, and Wi-Fi again sends an update for 12.
// The mobile-data card with tiers: web/e2e/screen-data-use.mjs.
// No page error. Ends with one line: PASS, FAIL or TIMEOUT.
import { mkdir } from "node:fs/promises";
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
  console.log("usage: node web/e2e/screen-video.mjs <web URL> <mock hub URL> [--shots <dir>] [--lang en|zh-Hans|ja] [--executable <chromium>]");
  process.exit(2);
}
if (shots) await mkdir(shots, { recursive: true });
const deadline = setTimeout(() => {
  console.log("TIMEOUT screen video: no verdict in 180 s");
  process.exit(1);
}, 180_000);
const fail = (why) => {
  throw new Error(why);
};
const mock = async (q = "") => (await fetch(`${hub}/mock/screen-video${q}`)).json();

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
const errors = [];
let current = null; // the page being checked, for a screenshot when it fails
try {
  const context = async (scheme = "light", init) => {
    const c = await browser.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: scheme, serviceWorkers: "block" });
    await c.addInitScript((l) => localStorage.setItem("persona.language", l), lang);
    if (init) await c.addInitScript(init);
    return c;
  };
  const open = async (c) => {
    const page = await c.newPage();
    current = page;
    page.setDefaultTimeout(20_000);
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${web}/screen/macbook#token=mock`);
    await page.fill("input[autocomplete=username]", "me");
    await page.fill("input[type=password]", "mock-pass");
    await page.click("button[type=submit]");
    await page.waitForSelector("[data-testid=screen-stats-toggle]");
    return page;
  };
  // The video canvas shown with something drawn on it, and noVNC's canvas cleared.
  const videoShows = (page) =>
    page.waitForFunction(() => {
      const v = document.querySelector("[data-testid=screen-video-picture]");
      if (!v || v.style.display === "none" || !v.width) return false;
      const vnc = [...document.querySelectorAll("[data-testid=screen] canvas")][0];
      const px = v.getContext("2d").getImageData(Math.floor(v.width / 2), Math.floor(v.height / 2), 1, 1).data;
      return vnc?.style.opacity === "0" && px[3] > 0 && px[0] + px[1] + px[2] > 0;
    });
  // noVNC's own picture drawn (VNC, the no-video cases): its canvas has something at the middle.
  const vncDrawn = (page) =>
    page.waitForFunction(() => {
      const vnc = [...document.querySelectorAll("[data-testid=screen] canvas")][0];
      if (!vnc?.width) return false;
      const px = vnc.getContext("2d").getImageData(Math.floor(vnc.width / 2), Math.floor(vnc.height / 2), 1, 1).data;
      return px[3] > 0 && vnc.style.opacity !== "0";
    });
  const videoGone = (page) =>
    page.waitForFunction(() => {
      const v = document.querySelector("[data-testid=screen-video-picture]");
      return !v || v.style.display === "none";
    });

  for (const scheme of ["light", "dark"]) {
    const c = await context(scheme);
    const page = await open(c);
    await videoShows(page);
    let sent = await mock();
    for (let i = 0; i < 25 && sent.reports < 5; i++) {
      await page.waitForTimeout(200);
      sent = await mock();
    }
    if (!sent.starts.length || !sent.starts.at(-1).codecs?.length) fail(`no start with codecs reached the helper: ${JSON.stringify(sent.starts)}`);
    if (sent.reports < 5) fail(`only ${sent.reports} reports reached the helper in 5 s`);
    if (sent.starts.at(-1).max_kbps !== 12_000) fail(`a desktop started at ${sent.starts.at(-1).max_kbps} kbps, not 12000`);
    await page.click("[data-testid=screen-stats-toggle]");
    const panel = await page.waitForFunction(() => {
      const p = document.querySelector("[data-testid=screen-stats]");
      const seg = document.querySelector("[data-testid=screen-stats-segments]");
      return p && seg && /960×600/.test(p.textContent ?? "") && /HEVC|H\.264/.test(p.textContent ?? "") && p.textContent;
    }).then((h) => h.jsonValue());
    console.log(`${scheme}: start ${JSON.stringify(sent.starts.at(-1).codecs)}, ${sent.reports} reports; panel: ${panel}`);
    if (shots) await page.screenshot({ path: `${shots}/video-stats-${scheme}.png` });

    if (scheme === "light") {
      await mock("?pause=locked");
      await videoGone(page);
      await page.waitForSelector("[data-testid=screen-fallback]");
      if (shots) await page.screenshot({ path: `${shots}/video-locked-${scheme}.png` });
      await mock("?resume=1");
      await videoShows(page);
      await page.waitForSelector("[data-testid=screen-fallback]", { state: "detached" });
      await mock("?drop=1");
      await videoGone(page);
      await page.waitForSelector("[data-testid=screen-fallback]");
      if (shots) await page.screenshot({ path: `${shots}/video-lost-${scheme}.png` });
    }
    await c.close();
  }

  // On mobile data without tiers: the iPhone's 4 Mbit/s, and Wi-Fi again sends 12 in an update.
  {
    const c = await context("light", () => {
      const net = Object.assign(new EventTarget(), { type: "cellular", saveData: false });
      Object.defineProperty(Navigator.prototype, "connection", { get: () => net, configurable: true });
      window.__net = net;
    });
    const page = await open(c);
    await videoShows(page);
    const before = (await mock()).updates.length;
    const start = (await mock()).starts.at(-1);
    if (start.max_kbps !== 4_000) fail(`mobile data without tiers started at ${start.max_kbps} kbps, not 4000`);
    await page.evaluate(() => {
      window.__net.type = "wifi";
      window.__net.dispatchEvent(new Event("change"));
    });
    let updates = [];
    for (let i = 0; i < 25 && !updates.some((u) => u.max_kbps === 12_000); i++) {
      await page.waitForTimeout(200);
      updates = (await mock()).updates.slice(before);
    }
    if (!updates.some((u) => u.max_kbps === 12_000)) fail(`no update for 12000 kbps after Wi-Fi: ${JSON.stringify(updates)}`);
    if (await page.$("[data-testid=screen-mobile-data]")) fail("a mobile-data card without tiers");
    await c.close();
  }

  // No video decoder in this browser: no Picture switcher, and the panel says why.
  {
    const c = await context("light", () => {
      delete window.VideoDecoder;
    });
    const page = await open(c);
    await page.click("[data-testid=screen-stats-toggle]");
    await page.waitForSelector("[data-testid=screen-stats-no-video]");
    await vncDrawn(page); // VNC is the picture
    if (await page.$("[data-testid=screen-video]")) fail("a Picture switcher in a browser that can't play video");
    if (shots) await page.screenshot({ path: `${shots}/video-unsupported-light.png` });
    await c.close();
  }
  // A page that isn't a secure address: the panel says to open the https address.
  {
    const c = await context("dark", () => {
      Object.defineProperty(window, "isSecureContext", { value: false });
    });
    const page = await open(c);
    await page.click("[data-testid=screen-stats-toggle]");
    const words = await page.waitForSelector("[data-testid=screen-stats-no-video]").then((e) => e.textContent());
    if (!/https/.test(words ?? "")) fail(`the insecure page's reason doesn't name https: ${words}`);
    await vncDrawn(page);
    if (shots) await page.screenshot({ path: `${shots}/video-insecure-dark.png` });
    await c.close();
  }
  // A decoder that fails on the real stream: the panel says it couldn't start; not tried again on this page.
  {
    const c = await context("light", () => {
      const Real = window.VideoDecoder;
      // only the page's stream (it carries a decoder configuration record); noVNC's own H.264 check at load has none
      window.VideoDecoder = class extends Real {
        configure(c) {
          if (c.description) throw new DOMException("not this stream", "NotSupportedError");
          return super.configure(c);
        }
      };
    });
    const page = await open(c);
    await page.click("[data-testid=screen-stats-toggle]");
    const words = await page.waitForSelector("[data-testid=screen-stats-no-video]").then((e) => e.textContent());
    // not tried again on this page: no Picture switcher, and no "for now" line promising a retry; VNC is the picture
    await vncDrawn(page);
    if (await page.$("[data-testid=screen-video]")) fail("a Picture switcher after the decoder failed");
    if (await page.$("[data-testid=screen-fallback]")) fail(`a fallback line after the decoder failed: ${words}`);
    if (shots) await page.screenshot({ path: `${shots}/video-failed-light.png` });
    await c.close();
  }

  if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
  console.log("PASS screen video: the picture is video with its stats in light and dark; locked, back, lost; 12 Mbit/s on a desktop, 4 on mobile data and 12 again on Wi-Fi; no decoder, an insecure page and a failing decoder each say why");
} catch (e) {
  console.log(`FAIL screen video: ${e instanceof Error ? e.message : e}`);
  if (shots && current) await current.screenshot({ path: `${shots}/failed.png` }).catch(() => {});
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
