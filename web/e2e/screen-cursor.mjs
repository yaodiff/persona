// The live screen's cursor visibility and capture-overlay cleanup in a real browser (#17828), against mock_hub.py's
// macOS-like Screen Sharing (no Cursor pseudo-encoding) and hardware video channel:
//   PORT=<port> MOCK_COMPUTER=ready MOCK_SCREEN=fake MOCK_SCREEN_VIDEO=1 python3 scripts/mock/mock_hub.py
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port>)
//   node web/e2e/screen-cursor.mjs <web URL> <mock hub URL> [--shots <dir>] [--lang en|zh-Hans|ja] [--executable <chromium>] [--cursor-web <url>]
// Checks, in light and dark:
//   - View only (Video & VNC): screen target does not hide the browser cursor (computed cursor !== "none") and clicks
//     do not open #noVNC_mouse_capture_elem;
//   - Take control (Video & VNC): computed cursor on the RFB canvas is never "none" and is larger than noVNC's 3x3 dot;
//   - Touch-capable device (noVNC useFallback): canvas and #noVNC_mouse_capture_elem still never have cursor "none";
//   - Mode switches, mouse leave/re-enter, resize, and disconnect/reconnect preserve a visible cursor;
//   - Capture overlay (#noVNC_mouse_capture_elem) is released on blur, visibilitychange (hidden), View-only switch, and
//     disconnect while holding the mouse button;
//   - Optional --cursor-web (MOCK_SCREEN_BG=terminal MOCK_SCREEN_CURSOR=1): a real remote PseudoCursor (-239) is kept.
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
const cursorWeb = flag("--cursor-web");
const [web, hub] = args;
if (!web || !hub) {
  console.log("usage: node web/e2e/screen-cursor.mjs <web URL> <mock hub URL> [--shots <dir>] [--lang en|zh-Hans|ja] [--executable <chromium>] [--cursor-web <url>]");
  process.exit(2);
}
if (shots) await mkdir(shots, { recursive: true });
const deadline = setTimeout(() => {
  console.log("TIMEOUT screen cursor: no verdict in 180 s");
  process.exit(1);
}, 180_000);
const fail = (why) => {
  throw new Error(why);
};

function pngCursorSize(cursor) {
  const m = /data:image\/png;base64,([A-Za-z0-9+/=]+)/.exec(cursor);
  if (!m) return null;
  const buf = Buffer.from(m[1].slice(0, 36), "base64");
  if (buf.length < 24) return null;
  return { w: buf.readUInt32BE(16), h: buf.readUInt32BE(20) };
}

const browser = await chromium.launch(
  executable
    ? { executablePath: executable, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] }
    : { channel: "chrome" },
);
const errors = [];
let current = null;
try {
  const context = async (scheme = "light", opts = {}, init) => {
    const c = await browser.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: scheme, serviceWorkers: "block", ...opts });
    await c.addInitScript((l) => localStorage.setItem("persona.language", l), lang);
    if (init) await c.addInitScript(init);
    return c;
  };
  const open = async (c, baseUrl = web) => {
    const page = await c.newPage();
    current = page;
    page.setDefaultTimeout(20_000);
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${baseUrl}/screen/macbook#token=mock`);
    await page.fill("input[autocomplete=username]", "me");
    await page.fill("input[type=password]", "mock-pass");
    await page.click("button[type=submit]");
    await page.waitForSelector("[data-testid=screen-stats-toggle]");
    return page;
  };
  const videoShows = (page) =>
    page.waitForFunction(() => {
      const v = document.querySelector("[data-testid=screen-video-picture]");
      return Boolean(v && v.style.display !== "none" && v.width > 0);
    });
  const inspect = (page) =>
    page.evaluate(() => {
      const vnc = document.querySelector("[data-testid=screen] canvas");
      const rect = vnc?.getBoundingClientRect();
      const cx = rect ? Math.round(rect.left + rect.width / 2) : 640;
      const cy = rect ? Math.round(rect.top + rect.height / 2) : 400;
      const center = document.elementFromPoint(cx, cy);
      const header = document.elementFromPoint(120, 20);
      const proxy = document.getElementById("noVNC_mouse_capture_elem");
      return {
        cx,
        cy,
        vncCursor: vnc ? getComputedStyle(vnc).cursor : null,
        centerTag: center?.tagName ?? null,
        centerId: center?.id ?? null,
        centerCursor: center ? getComputedStyle(center).cursor : null,
        headerTag: header?.tagName ?? null,
        headerId: header?.id ?? null,
        headerCursor: header ? getComputedStyle(header).cursor : null,
        proxyVisible: Boolean(proxy && proxy.style.display !== "none"),
        proxyCursor: proxy ? getComputedStyle(proxy).cursor : null,
      };
    });
  const assertCursorVisible = (cursor, label, { allowDefault = true } = {}) => {
    if (!cursor || cursor === "none") fail(`${label}: cursor is ${JSON.stringify(cursor)}`);
    const sz = pngCursorSize(cursor);
    if (sz && Math.max(sz.w, sz.h) <= 4) fail(`${label}: cursor is tiny ${sz.w}x${sz.h} dot (${cursor.slice(0, 60)})`);
    if (!allowDefault && cursor === "auto") fail(`${label}: unexpected auto cursor`);
  };

  for (const scheme of ["light", "dark"]) {
    const c = await context(scheme);
    const page = await open(c);
    await videoShows(page);

    // 1. Default View only in Video mode: normal browser cursor, clicks do not open capture proxy.
    const s1 = await inspect(page);
    assertCursorVisible(s1.centerCursor, `${scheme} video view-only center`);
    assertCursorVisible(s1.vncCursor, `${scheme} video view-only vnc canvas`);
    if (s1.centerTag === "CANVAS") fail(`${scheme} view-only: pointer hit fell onto RFB canvas instead of being ignored`);
    await page.mouse.move(s1.cx, s1.cy);
    await page.mouse.down();
    const s1Down = await inspect(page);
    await page.mouse.up();
    if (s1Down.proxyVisible) fail(`${scheme} view-only: mousedown opened #noVNC_mouse_capture_elem`);
    if (shots) await page.screenshot({ path: `${shots}/cursor-viewonly-${scheme}.png` });

    // 2. Switch Video -> VNC -> Video in View only.
    const videoMenu = await page.$("[data-testid=screen-video]");
    if (videoMenu) {
      await videoMenu.click();
      await page.getByRole("menuitemradio", { name: /VNC/i }).click();
      await page.waitForFunction(() => document.querySelector("[data-testid=screen-video-picture]")?.style.display === "none");
      const sVnc = await inspect(page);
      assertCursorVisible(sVnc.centerCursor, `${scheme} VNC view-only center`);
      await page.click("[data-testid=screen-video]");
      await page.getByRole("menuitemradio", { name: /Video/i }).click();
      await videoShows(page);
    }

    // 3. Take control: RFB canvas takes hits with a clear visible cursor (never none, never 3x3 dot).
    await page.getByRole("button", { name: /Take control/i }).click();
    const sCtrl = await inspect(page);
    if (sCtrl.centerTag !== "CANVAS") fail(`${scheme} take-control: expected CANVAS hit, got ${sCtrl.centerTag}`);
    assertCursorVisible(sCtrl.centerCursor, `${scheme} take-control center`);
    assertCursorVisible(sCtrl.vncCursor, `${scheme} take-control vnc canvas`);
    if (shots) await page.screenshot({ path: `${shots}/cursor-control-${scheme}.png` });

    // 4. Mouse leave to header and re-enter, plus viewport resize.
    await page.mouse.move(120, 20);
    await page.mouse.move(sCtrl.cx, sCtrl.cy);
    await page.setViewportSize({ width: 1440, height: 900 });
    const sResized = await inspect(page);
    assertCursorVisible(sResized.centerCursor, `${scheme} after leave/re-enter and resize`);

    if (scheme === "light") {
      // 5. Capture proxy cleanup on blur, visibilitychange(hidden), View-only switch, and disconnect.
      const startHold = async () => {
        const cur = await inspect(page);
        await page.mouse.move(cur.cx, cur.cy);
        await page.mouse.down();
        await page.waitForFunction(() => {
          const p = document.getElementById("noVNC_mouse_capture_elem");
          return Boolean(p && p.style.display !== "none");
        });
        const held = await inspect(page);
        assertCursorVisible(held.proxyCursor, "capture proxy cursor during drag");
      };

      await startHold();
      await page.evaluate(() => window.dispatchEvent(new Event("blur")));
      const afterBlur = await inspect(page);
      await page.mouse.up();
      if (afterBlur.proxyVisible) fail("capture proxy stayed visible after window blur");

      await startHold();
      await page.evaluate(() => {
        Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
        document.dispatchEvent(new Event("visibilitychange"));
        Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
        document.dispatchEvent(new Event("visibilitychange"));
      });
      const afterHidden = await inspect(page);
      await page.mouse.up();
      if (afterHidden.proxyVisible) fail("capture proxy stayed visible after visibilitychange(hidden)");

      await startHold();
      // Programmatically switch to View only (the proxy intercepts pointer clicks while held).
      await page.evaluate(() => document.querySelectorAll('header [role="group"] button')[0]?.click());
      const afterViewOnly = await inspect(page);
      await page.mouse.up();
      if (afterViewOnly.proxyVisible) fail("capture proxy stayed visible after switching to View only");

      // Re-enter Take control, hold mouse button, and drop the RFB connection mid-drag.
      await page.getByRole("button", { name: /Take control/i }).click();
      await startHold();
      await fetch(`${hub}/mock/screen-password?down=1`);
      await page.getByRole("button", { name: /Reconnect/i }).waitFor();
      const sDrop = await inspect(page);
      if (sDrop.proxyVisible) fail("capture proxy stayed visible over the page after disconnect while holding mouse");
      if (sDrop.headerId === "noVNC_mouse_capture_elem") fail("capture proxy blocked header after disconnect");
      assertCursorVisible(sDrop.headerCursor, "header cursor after disconnect");
      if (shots) await page.screenshot({ path: `${shots}/cursor-disconnect-${scheme}.png` });
      await page.mouse.up();

      // Reconnect and verify cursor stays visible.
      await fetch(`${hub}/mock/screen-password?down=0`);
      await page.getByRole("button", { name: /Reconnect/i }).click();
      await page.fill("input[autocomplete=username]", "me");
      await page.fill("input[type=password]", "mock-pass");
      await page.click("button[type=submit]");
      await page.waitForSelector("[data-testid=screen-stats-toggle]");
      await videoShows(page);
      const sReconn = await inspect(page);
      assertCursorVisible(sReconn.centerCursor, "after reconnect view-only");
      assertCursorVisible(sReconn.vncCursor, "after reconnect vnc canvas");
    }

    await c.close();
  }

  // 6. Touch-capable device (noVNC useFallback = true because hasTouch / maxTouchPoints > 0).
  {
    const c = await context("light", { hasTouch: true }, () => {
      Object.defineProperty(navigator, "maxTouchPoints", { value: 5, configurable: true });
    });
    const page = await open(c);
    await videoShows(page);
    await page.getByRole("button", { name: /Take control/i }).click();
    const sTouch = await inspect(page);
    assertCursorVisible(sTouch.vncCursor, "touch useFallback take-control vnc canvas");
    await page.mouse.move(sTouch.cx, sTouch.cy);
    await page.mouse.down();
    const sTouchDrag = await inspect(page);
    await page.mouse.up();
    if (sTouchDrag.proxyVisible) assertCursorVisible(sTouchDrag.proxyCursor, "touch useFallback capture proxy");
    await c.close();
  }

  // 7. Optional contrast: server with PseudoCursor (-239) preserves the remote cursor bitmap once the first FBU arrives.
  if (cursorWeb) {
    const c = await context("light");
    const page = await open(c, cursorWeb);
    await page.getByRole("button", { name: /Take control/i }).click();
    await page.waitForFunction(() => {
      const vnc = document.querySelector("[data-testid=screen] canvas");
      const cur = vnc ? getComputedStyle(vnc).cursor : "";
      const m = /data:image\/png;base64,([A-Za-z0-9+/=]+)/.exec(cur);
      if (!m) return false;
      const bin = atob(m[1].slice(0, 32));
      if (bin.length < 24) return false;
      const u32 = (i) => ((bin.charCodeAt(i) << 24) | (bin.charCodeAt(i + 1) << 16) | (bin.charCodeAt(i + 2) << 8) | bin.charCodeAt(i + 3)) >>> 0;
      return u32(16) === 10 && u32(20) === 17;
    });
    const sRemote = await inspect(page);
    const sz = pngCursorSize(sRemote.vncCursor ?? "");
    if (!sz || sz.w !== 10 || sz.h !== 17) fail(`remote PseudoCursor was not preserved: ${sRemote.vncCursor}`);
    await c.close();
  }

  if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
  console.log("PASS screen cursor: visible cursor in View only and Take control (Video/VNC/light/dark/touch useFallback/reconnect), capture proxy released on blur/hidden/view-only/disconnect");
} catch (e) {
  console.log(`FAIL screen cursor: ${e instanceof Error ? e.message : e}`);
  if (shots && current) await current.screenshot({ path: `${shots}/failed.png` }).catch(() => {});
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
