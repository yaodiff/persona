// Synthetic acceptance for #10239, using the real terminal and session menus.
// Start scripts/mock/mock_hub.py with MOCK_TERMINAL_PANES=1 MOCK_TERMINAL_STREAM=1
// MOCK_TERMINAL_CONTROL=1 MOCK_TERMINAL_FULL=1, then Vite with PERSONA_HUB_URL pointing at it.
// node web/e2e/terminal-pane-actions.mjs <web URL> <shots directory>
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";
const [base = "http://127.0.0.1:19362", shots = "work/pane-actions"] = process.argv.slice(2);
mkdirSync(shots, { recursive: true });
const browser = await chromium.launch({ channel: "chrome", headless: true });
const results = [];
try {
  for (const width of [1280, 390]) for (const theme of ["light", "dark"]) for (const large of [false, true]) {
    const tag = `${width}-${theme}-${large ? "large" : "default"}`;
    const context = await browser.newContext({ viewport: { width, height: 900 }, colorScheme: theme, hasTouch: width === 390, permissions: ["clipboard-read", "clipboard-write"] });
    await context.addInitScript(({ theme, large }) => {
      localStorage.setItem("persona.token", "mock"); localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme); localStorage.setItem("persona.terminal.defaultMode", "pane");
      if (large) localStorage.setItem("persona.text-size", "large");
    }, { theme, large });
    const page = await context.newPage();
    const errors = []; page.on("pageerror", (e) => errors.push(e.message));
    await page.addInitScript(() => {
      window.__paneFeedback = [];
      document.addEventListener("click", (e) => {
        const button = e.target.closest?.("button");
        if (!/^Split (right|down)$/.test(button?.getAttribute("aria-label") ?? "")) return;
        const start = performance.now();
        const observer = new MutationObserver(() => {
          if (button.disabled) {
            window.__paneFeedback.push({ disabledAfterMs: +(performance.now() - start).toFixed(1) });
            observer.disconnect();
          }
        });
        observer.observe(button, { attributes: true, attributeFilter: ["disabled"] });
      }, true);
    });
    const posts = [];
    page.on("request", (r) => { if (r.method() === "POST" && /\/panes\/(split|close)$/.test(new URL(r.url()).pathname)) posts.push({ url: r.url(), body: r.postDataJSON() }); });
    const shell = `${base}/terminal/server/pane%3Aw1%3Ap2`;
    await page.goto(shell);
    const toolbar = page.locator("[data-terminal-toolbar]");
    await toolbar.waitFor();
    const single = page.getByRole("radio", { name: "Single terminal", exact: true });
        await page.waitForFunction(() => [...document.querySelectorAll(".xterm-rows")].some((e) => e.textContent.includes("git status")));
    assert.equal(await single.getAttribute("aria-checked"), "true");
    await page.screenshot({ path: join(shots, `${tag}-toolbar.png`) });
    const actions = toolbar.getByRole("button", { name: "Terminal actions", exact: true });
    await actions.focus();
    await page.keyboard.press("Enter");
    await page.getByRole("menuitem", { name: /Close this terminal/ }).waitFor();
    await page.locator("[data-menu]").evaluateAll(async (els) => Promise.all(els.flatMap((el) => el.getAnimations()).map((a) => a.finished)));
    await page.screenshot({ path: join(shots, `${tag}-menu.png`) });
    // A real xterm textarea regaining focus must keep N, I and Backspace even with the menu open.
    const input = page.locator('[data-surface="shown"] textarea.xterm-helper-textarea').last();
    await input.focus(); await page.keyboard.press("n"); await page.keyboard.press("i"); await page.keyboard.press("Backspace");
    assert.equal(posts.length, 0); assert.equal(await page.getByRole("menu").count(), 1);
    await toolbar.getByRole("button", { name: "Terminal actions", exact: true }).focus();
    await page.keyboard.press("i");
    assert.equal(await page.evaluate(() => navigator.clipboard.readText()), "w1:p2");
    await toolbar.getByRole("button", { name: "Terminal actions", exact: true }).click();
    await page.keyboard.press("n");
    await page.getByRole("menuitem", { name: /Split down/ }).waitFor();
    await page.keyboard.press("Escape");
    await toolbar.getByRole("button", { name: "Terminal actions", exact: true }).click();
    await page.keyboard.press("Backspace");
    await page.getByRole("menuitem", { name: /^Close(?:\s|$)/ }).waitFor();
    assert.equal(posts.length, 0); await page.keyboard.press("Escape");
    // Failure does not select Full view; success does, for both directions.
    await page.route("**/panes/split", (route) => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Synthetic offline refusal" }) }));
    await toolbar.getByRole("button", { name: "Split right", exact: true }).click();
    await page.getByText(/Synthetic offline refusal/).waitFor();
    assert.equal(await single.getAttribute("aria-checked"), "true");
    await page.unroute("**/panes/split");
    const feedback = [];
    for (const direction of ["right", "down"]) {
      await single.click();
      await page.waitForFunction(() => document.querySelectorAll('[data-surface]').length === 1);
      const split = toolbar.getByRole("button", { name: `Split ${direction}`, exact: true });
      const start = performance.now();
      const response = page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/panes/split") && r.status() === 200);
      await split.click();
      const received = await response;
      await received.finished();
      const timing = received.request().timing();
      await page.waitForFunction(() => [...document.querySelectorAll('[role="radio"]')].some((e) => e.ariaLabel === "Full view" && e.getAttribute("aria-checked") === "true"));
      await page.waitForFunction(() => [...document.querySelectorAll('[data-surface="shown"] .xterm-rows')].some((e) => e.textContent.includes("1 main")));
      feedback.push({ direction, clickToRenderedFullMs: +(performance.now() - start).toFixed(1), requestStartToResponseEndMs: +(timing.responseEnd - timing.requestStart).toFixed(1) });
    }
    await page.screenshot({ path: join(shots, `${tag}-full.png`) });
    const geometry = await toolbar.evaluate((el) => ({ width: el.clientWidth, scroll: el.scrollWidth, right: el.getBoundingClientRect().right }));
    assert.ok(geometry.scroll <= geometry.width + 1 && geometry.right <= width + 1, JSON.stringify(geometry));
    assert.deepEqual(posts.slice(-2).map((p) => p.body), [{ pane: "w1:p2", direction: "right" }, { pane: "w1:p2", direction: "down" }]);
    const localFeedback = await page.evaluate(() => window.__paneFeedback);
    // History lists ended sessions, which have no terminal actions menu.
    await page.goto(`${base}/terminal`);
    const row = page.locator('[data-history-row] a[href^="/s/"]').first();
    await row.waitFor(); await row.click({ button: "right" });
    assert.equal(await page.getByRole("menu").count(), 0);
    await page.screenshot({ path: join(shots, `${tag}-history.png`) });
    assert.deepEqual(errors, []);
    results.push({ tag, geometry, feedback, localFeedback, checks: "split failure/success, scoped N/I/Backspace, clipboard, close cancel, session menu, no overflow" });
    console.log(`PASS ${tag}`);
    await context.close();
  }
} finally { await browser.close(); }
writeFileSync(join(shots, "results.json"), JSON.stringify(results, null, 2) + "\n");
