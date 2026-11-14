// The live screen's connection stats (#6558, docs/extensions/computer-use.md "Connection stats on the web"), in a real
// browser against the mock hub's animated Screen Sharing, so noVNC's frames, the RFB socket's bytes and the hub probe's
// Resource Timing are the browser's own:
//   PORT=<port> MOCK_COMPUTER=ready MOCK_SCREEN=fake MOCK_SCREEN_ANIMATE=1 python3 scripts/mock/mock_hub.py
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port>)
//   node web/e2e/screen-stats.mjs <web URL> [--shots <dir>] [--lang en|zh-Hans|ja] [--executable <chromium>]
// Checks, in light and dark: the bar's toggle is off and no panel shows; on, the panel shows a bitrate, a frame rate
// above 0 (the box moves) and the round trip to the hub on its own line; a new page for the Mac opens with it on; the
// panel's close turns it off. With the hub's health refused, the panel shows the rates and no round-trip line. No page
// error anywhere. Ends with one line: PASS, FAIL or TIMEOUT.
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
const [web] = args;
if (!web) {
  console.log("usage: node web/e2e/screen-stats.mjs <web URL> [--shots <dir>] [--lang en|zh-Hans|ja] [--executable <chromium>]");
  process.exit(2);
}
if (shots) await mkdir(shots, { recursive: true });

const deadline = setTimeout(() => {
  console.log("TIMEOUT screen stats: no verdict in 150 s");
  process.exit(1);
}, 150_000);
const fail = (why) => {
  throw new Error(why);
};

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
const errors = [];
try {
  const context = async (colorScheme, viewport = { width: 1280, height: 800 }) => {
    const c = await browser.newContext({ viewport, colorScheme, serviceWorkers: "block" });
    await c.addInitScript((l) => localStorage.setItem("persona.language", l), lang);
    return c;
  };
  const open = async (c) => {
    const page = await c.newPage();
    page.setDefaultTimeout(20_000);
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${web}/screen/macbook#token=mock`);
    await page.fill("input[autocomplete=username]", "me");
    await page.fill("input[type=password]", "mock-pass");
    await page.click("button[type=submit]");
    await page.waitForSelector("[data-testid=screen-stats-toggle]");
    await page.waitForTimeout(500); // the first frames drawn
    return page;
  };
  const pressed = (page) => page.getAttribute("[data-testid=screen-stats-toggle]", "aria-pressed");
  const panel = (page) => page.$("[data-testid=screen-stats]");
  // The panel once it has a frame rate above 0 and, unless told not to wait for it, the hub's round trip.
  const measured = (page, hub = true) =>
    page
      .waitForFunction((hub) => {
        const p = document.querySelector("[data-testid=screen-stats]");
        const lines = [...(p?.querySelectorAll("p") ?? [])].map((x) => x.textContent ?? "");
        const fps = Number(/(\d+) fps/.exec(lines[0] ?? "")?.[1] ?? "0");
        const mbps = Number(/([\d.]+) Mbps/.exec(lines[0] ?? "")?.[1] ?? "-1");
        const trip = document.querySelector("[data-testid=screen-stats-hub]")?.textContent ?? null;
        return fps > 0 && mbps >= 0 && (!hub || trip) && { lines, fps, mbps, trip };
      }, hub)
      .then((h) => h.jsonValue());

  for (const scheme of ["light", "dark"]) {
    const c = await context(scheme);
    const page = await open(c);
    if ((await pressed(page)) !== "false" || (await panel(page))) fail(`${scheme}: the stats are not off by default`);
    if (shots) await page.screenshot({ path: `${shots}/stats-off-${scheme}.png` });
    await page.click("[data-testid=screen-stats-toggle]");
    const got = await measured(page);
    if (/\d/.test((await page.textContent("[data-testid=screen-stats] > div")) ?? "")) fail(`${scheme}: a number on the panel's first line`);
    if (shots) await page.screenshot({ path: `${shots}/stats-on-${scheme}.png` });
    console.log(`${scheme}: ${JSON.stringify(got)}`);
    // kept for this Mac: a new page opens with it on
    await page.close();
    const again = await open(c);
    if ((await pressed(again)) !== "true" || !(await panel(again))) fail(`${scheme}: a new page did not keep the stats on`);
    await again.click("[data-testid=screen-stats-close]");
    if ((await pressed(again)) !== "false" || (await panel(again))) fail(`${scheme}: closing the panel did not turn the stats off`);
    await c.close();
  }

  // A hub whose health does not answer: the rates, and no round-trip line.
  {
    const c = await context("light");
    await c.route("**/api/v1/health*", (r) => r.abort());
    const page = await open(c);
    await page.click("[data-testid=screen-stats-toggle]");
    await measured(page, false);
    await page.waitForTimeout(5000); // a few probes' time
    if (await page.$("[data-testid=screen-stats-hub]")) fail("a round trip shown with the hub's health refused");
    if (shots) await page.screenshot({ path: `${shots}/stats-on-no-health-light.png` });
    await c.close();
  }

  // A narrow window (the toggle is its icon only), and the Large text size.
  if (shots) {
    for (const [name, scheme, viewport, size] of [["narrow-dark", "dark", { width: 430, height: 800 }, null], ["large-text-light", "light", undefined, "large"]]) {
      const c = await context(scheme, viewport);
      if (size) await c.addInitScript((s) => localStorage.setItem("persona.text-size", s), size);
      const page = await open(c);
      await page.click("[data-testid=screen-stats-toggle]");
      await measured(page);
      await page.screenshot({ path: `${shots}/stats-on-${name}.png` });
      await c.close();
    }
  }

  if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
  console.log("PASS screen stats: off by default, measured numbers in light and dark, kept per Mac, no round trip without the hub's health");
} catch (e) {
  console.log(`FAIL screen stats: ${e instanceof Error ? e.message : e}`);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
