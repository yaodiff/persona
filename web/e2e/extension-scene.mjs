// UI version 11's scene in Chromium against the synthetic mock home. No writes or non-loopback requests.
// MOCK_EXTENSIONS=1 MOCK_EXTENSION_SCENE=1; node web/e2e/extension-scene.mjs <loopback URL> <artifact dir> [Chromium]
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, shots, executable] = process.argv.slice(2);
if (!base || !shots || !/^https?:\/\/(127\.0\.0.1|localhost)(:\d+)?\/?$/.test(base)) {
  console.error("usage: node web/e2e/extension-scene.mjs <loopback web URL> <artifact directory> [Chromium executable]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const root = base.replace(/\/$/, "");
const results = [];
const check = (name, ok, detail) => {
  results.push({ name, ok: Boolean(ok), detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
};
const watchdog = setTimeout(() => { console.error("RESULT: extension-scene timed out"); process.exit(124); }, 180_000);
const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });

async function openPage(theme, at = "/extension/example-home/home-scene", textSize, reducedMotion = "no-preference") {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: theme, reducedMotion, deviceScaleFactor: 2, serviceWorkers: "block" });
  await context.addInitScript(([theme, textSize]) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "en");
    if (textSize) localStorage.setItem("persona.text-size", textSize);
  }, [theme, textSize]);
  await context.route("**/*", (route) => new URL(route.request().url()).origin === new URL(root).origin ? route.continue() : route.abort());
  const page = await context.newPage();
  const errors = [];
  const sceneRequests = [];
  page.on("request", (request) => { if (/ViewBody11|scene-geometry/.test(request.url())) sceneRequests.push(request.url()); });
  page.on("pageerror", (error) => errors.push(String(error)));
  page.on("console", (message) => { if (message.type() === "error" && !message.text().startsWith("Failed to load resource:")) errors.push(message.text()); });
  await page.goto(`${root}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"));
  const coldSceneRequests = [...sceneRequests];
  await page.goto(root + at);
  await page.locator("[data-scene-stage]").first().waitFor({ timeout: 30_000 }).catch(async (error) => {
    console.error("PAGE:", (await page.locator("body").innerText()).slice(0, 4000), errors);
    await page.screenshot({ path: join(shots, "scene-load-failure.png") });
    throw error;
  });
  return { page, errors, coldSceneRequests };
}
const camera = (page) => page.locator("[data-scene-stage]").first().evaluate((e) => ({ yaw: Number(e.dataset.cameraYaw), pitch: Number(e.dataset.cameraPitch), zoom: Number(e.dataset.cameraZoom) }));

try {
  for (const theme of ["light", "dark"]) {
    const { page, errors, coldSceneRequests } = await openPage(theme);
    check(`${theme}: cold main chat loads no scene renderer`, coldSceneRequests.length === 0, coldSceneRequests);
    const order = await page.evaluate(() => [...document.querySelectorAll("[data-scene-alt],[data-scene-label],[data-scene-pin]")].map((e) => e.getAttribute("data-scene-pin") || e.textContent));
    check(`${theme}: alt, two labels and four pins in source order`, JSON.stringify(order) === JSON.stringify(["Two-room flat with a living room on the west and a bedroom on the east.", "Living room", "Bedroom", "living-lamp", "living-ac", "living-tv", "bedroom-lamp"]), order);
    const pixels = await page.locator("canvas[data-scene-canvas]").evaluate((canvas) => {
      const ctx = canvas.getContext("2d");
      const data = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
      const colors = new Set();
      for (let i = 0; i < data.length; i += 400) colors.add(`${data[i]},${data[i + 1]},${data[i + 2]},${data[i + 3]}`);
      return colors.size;
    });
    check(`${theme}: Canvas draws a nontrivial model`, pixels > 20, pixels);
    const contrasts = await page.locator("[data-scene-pin]").evaluateAll((pins) => pins.map((pin) => {
      const rgb = (value) => (value.match(/[\d.]+/g) || [0, 0, 0]).slice(0, 3).map(Number);
      const lum = (parts) => {
        const c = parts.map((v) => { const s = v / 255; return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4; });
        return c[0] * 0.2126 + c[1] * 0.7152 + c[2] * 0.0722;
      };
      const style = getComputedStyle(pin);
      const a = lum(rgb(style.color)), b = lum(rgb(style.backgroundColor));
      return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
    }));
    check(`${theme}: every pin has opaque text contrast >= 4.5`, contrasts.every((n) => n >= 4.5), contrasts);
    const before = await camera(page);
    check(`${theme}: default camera`, before.yaw === 225 && before.pitch === 35 && before.zoom === 1, before);
    await page.screenshot({ path: join(shots, `scene-1440-${theme}.png`) });
    await page.locator("[data-scene-stage]").focus();
    await page.keyboard.press("ArrowRight");
    check(`${theme}: keyboard orbit`, (await camera(page)).yaw === 230);
    await page.getByRole("button", { name: "Zoom in" }).click();
    check(`${theme}: zoom button`, (await camera(page)).zoom > 1);
    await page.getByRole("button", { name: "Reset" }).click();
    check(`${theme}: Reset restores the default`, JSON.stringify(await camera(page)) === JSON.stringify(before));
    await page.mouse.move(560, 650);
    await page.mouse.down();
    await page.mouse.move(630, 690, { steps: 4 });
    await page.mouse.up();
    const dragged = await camera(page);
    check(`${theme}: drag orbits`, dragged.yaw !== 225 && dragged.pitch !== 35, dragged);
    await page.mouse.wheel(0, -120);
    await page.waitForFunction((zoom) => Number(document.querySelector("[data-scene-stage]")?.getAttribute("data-camera-zoom")) > zoom, dragged.zoom);
    check(`${theme}: wheel zooms`, (await camera(page)).zoom > dragged.zoom);
    const pinchBefore = (await camera(page)).zoom;
    const touch = await page.context().newCDPSession(page);
    await touch.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 2 });
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 560, y: 650, id: 1 }, { x: 640, y: 650, id: 2 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: 530, y: 650, id: 1 }, { x: 670, y: 650, id: 2 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await page.waitForFunction((zoom) => Number(document.querySelector("[data-scene-stage]")?.getAttribute("data-camera-zoom")) > zoom, pinchBefore);
    check(`${theme}: two-finger pinch zooms`, (await camera(page)).zoom > pinchBefore);
    await touch.send("Emulation.setTouchEmulationEnabled", { enabled: false });
    await page.locator('[data-scene-pin="living-lamp"]').click();
    await page.waitForFunction(() => document.body.textContent.includes("This page stands for a device's own page"));
    check(`${theme}: pin opens its device page`, true);
    await page.goBack();
    await page.locator("[data-scene-stage]").waitFor({ timeout: 15_000 });
    const redrawn = await camera(page);
    check(`${theme}: redraw keeps the touched camera`, redrawn.yaw === dragged.yaw && redrawn.pitch === dragged.pitch, { dragged, redrawn });
    await page.getByRole("button", { name: "Full screen" }).click();
    check(`${theme}: Full screen opens`, await page.locator("[role='dialog']").isVisible());
    const fullPins = await page.locator("[role='dialog'] [data-scene-pin]").evaluateAll((pins) => {
      const stage = document.querySelector("[role='dialog'] [data-scene-stage]").getBoundingClientRect();
      return pins.map((pin) => {
        const r = pin.getBoundingClientRect();
        return { id: pin.getAttribute("data-scene-pin"), visible: r.left >= stage.left && r.right <= stage.right && r.top >= stage.top && r.bottom <= stage.bottom && pin.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) };
      });
    });
    check(`${theme}: every pin stays inside Full screen`, fullPins.every((p) => p.visible), fullPins);
    await page.keyboard.press("Escape");
    check(`${theme}: Escape closes Full screen`, !(await page.$("[role='dialog']")));
    check(`${theme}: no browser page errors`, errors.length === 0, errors);
    await page.context().close();
  }
  for (const theme of ["light", "dark"]) {
    const { page, errors, coldSceneRequests } = await openPage(theme, "/overview");
    check(`${theme} compact: cold main chat loads no scene renderer`, coldSceneRequests.length === 0, coldSceneRequests);
    const preview = page.locator("button.graph-preview").first();
    await preview.waitFor({ timeout: 15_000 });
    const height = await preview.evaluate((e) => e.getBoundingClientRect().height);
    check(`${theme} compact: still preview is 200 px`, Math.abs(height - 200) < 4, height);
    check(`${theme} compact: no nested interactive pins or controls`, await preview.locator("button").count() === 0);
    await preview.scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(shots, `scene-card-${theme}.png`) });
    await preview.click();
    await page.locator("[role='dialog']").waitFor();
    check(`${theme} compact: preview opens interactive Full screen`, await page.locator("[role='dialog'] [data-scene-pin='living-lamp']").isVisible());
    const compactFullPins = await page.locator("[role='dialog'] [data-scene-pin]").evaluateAll((pins) => pins.map((pin) => {
      const r = pin.getBoundingClientRect();
      return { id: pin.getAttribute("data-scene-pin"), visible: r.top >= 0 && r.bottom <= innerHeight && r.left >= 0 && r.right <= innerWidth && pin.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) };
    }));
    check(`${theme} compact: every pin stays inside Full screen`, compactFullPins.every((p) => p.visible), compactFullPins);
    await page.screenshot({ path: join(shots, `scene-card-${theme}-full.png`) });
    await page.keyboard.press("Escape");
    check(`${theme} compact: focus returns to the preview`, await preview.evaluate((e) => document.activeElement === e));
    check(`${theme} compact: no browser page errors`, errors.length === 0, errors);
    await page.context().close();
  }
  for (const theme of ["light", "dark"]) {
    const { page, errors } = await openPage(theme, "/extension/example-home/home-scene", "large", "reduce");
    check(`${theme}: Large text is active`, await page.evaluate(() => document.documentElement.dataset.textSize === "large"));
    await page.screenshot({ path: join(shots, `scene-1440-large-${theme}.png`) });
    await page.goto(`${root}/overview`);
    const preview = page.locator("button.graph-preview").first();
    await preview.waitFor({ timeout: 15_000 });
    await preview.scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(shots, `scene-card-large-${theme}.png`) });
    await preview.click();
    await page.locator("[role='dialog']").waitFor();
    const pins = await page.locator("[role='dialog'] [data-scene-pin]").evaluateAll((items) => items.map((pin) => {
      const r = pin.getBoundingClientRect();
      return { id: pin.getAttribute("data-scene-pin"), visible: r.top >= 0 && r.bottom <= innerHeight && r.left >= 0 && r.right <= innerWidth && pin.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) };
    }));
    check(`${theme}: Large text keeps every pin reachable`, pins.every((p) => p.visible), pins);
    await page.screenshot({ path: join(shots, `scene-card-large-${theme}-full.png`) });
    await page.locator("[role='dialog'] [data-scene-stage]").focus();
    await page.keyboard.press("ArrowRight");
    await page.waitForFunction(() => Number(document.querySelector("[role='dialog'] [data-scene-stage]")?.getAttribute("data-camera-yaw")) === 230);
    await page.waitForFunction(() => {
      const stage = document.querySelector("[role='dialog'] [data-scene-stage]");
      return stage && stage.getAnimations({ subtree: true }).every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity);
    });
    const motion = await page.locator("[role='dialog'] [data-scene-stage]").evaluate((stage) => ({
      animations: stage.getAnimations({ subtree: true }).map((a) => ({ name: a.animationName, playState: a.playState, iterations: a.effect?.getTiming().iterations, duration: a.effect?.getTiming().duration })),
    }));
    check(`${theme}: Reduce Motion has immediate camera input and no running scene animation`, motion.animations.every((a) => a.playState !== "running"), motion);
    await page.keyboard.press("Escape");
    await page.locator("[role='dialog']").waitFor({ state: "detached" });
    check(`${theme}: Large text and Reduce Motion have no browser page errors`, errors.length === 0, errors);
    await page.context().close();
  }
} catch (error) {
  check("extension-scene driver", false, String(error));
} finally {
  clearTimeout(watchdog);
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`RESULT: ${results.length - failed.length}/${results.length} checks passed`);
if (failed.length) process.exit(1);
