// Run against the isolated workspace-naming Manager fixture and Vite app.
// node web/e2e/workspace-naming-load.mjs <loopback origin> <shots directory> <Chromium executable>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [origin, shots, executablePath] = process.argv.slice(2);
assert(/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin), "Use an isolated loopback origin");
assert(shots && executablePath, "Provide screenshot output and the installed browser");
await mkdir(shots, { recursive: true });
const deadline = setTimeout(() => { console.error("FAIL naming import browser deadline"); process.exit(124); }, 240_000);
const browser = await chromium.launch({ executablePath, env: { ...process.env, TMPDIR: process.env.WORKSPACE_NAMING_BROWSER_TMP || process.env.TMPDIR }, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const results = [];
let activePage;
try {
  for (const scheme of ["light", "dark"]) for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 1200 }, colorScheme: scheme, locale: "en-US", reducedMotion: "reduce", ...(width === 390 ? { isMobile: true, hasTouch: true } : {}) });
    await context.addInitScript(({ scheme }) => {
      localStorage.setItem("persona.token", "synthetic-naming-token");
      localStorage.setItem("persona.theme", scheme);
      localStorage.setItem("persona.language", "en");
      // Exercise the persistent failure after PageBoundary's existing one-time
      // automatic reload. The recovery button below performs an actual reload.
      sessionStorage.setItem("persona.chunkReload", String(Date.now()));
    }, { scheme });
    const headers = { "X-Persona-Manager": "assistant", Authorization: "Bearer synthetic-naming-token" };
    const savedTemplate = "saved/{task-id}";
    const reset = await context.request.put(origin + "/api/v1/assistant", { headers, data: {
      notes: "Saved instructions", task_workspace_preference: "prefer-existing", workspace_rules: { task_directory_template: savedTemplate },
    } });
    assert(reset.ok(), "seed only the isolated fixture");
    let requested;
    const chunkRequested = new Promise((resolve) => { requested = resolve; });
    let release;
    const failChunk = new Promise((resolve) => { release = resolve; });
    let blocking = true;
    let imports = 0;
    const failedURLs = [];
    await context.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.origin !== origin) return route.abort();
      // Match both the Vite source module and its production chunk. Do not mock
      // lazyPage, PageBoundary or the component: the browser import really fails.
      if (/\/(?:src\/ui\/WorkspaceNaming\.tsx|assets\/WorkspaceNaming-[^/]+\.js)$/.test(url.pathname)) {
        imports++;
        if (blocking) { failedURLs.push(url.pathname); requested(); await failChunk; return route.abort("failed"); }
      }
      return route.continue();
    });
    const page = await context.newPage(); activePage = page;
    page.setDefaultTimeout(15_000);
    const pageErrors = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    await page.goto(origin + "/settings?tab=persona");
    const preference = page.getByLabel("When dispatching tasks, prefer", { exact: true });
    const notes = page.getByRole("textbox", { name: "About me", exact: true });
    await notes.waitFor();
    await chunkRequested;
    const draft = `Instructions kept through a failed import (${scheme}, ${width})`;
    await notes.fill(draft);
    assert.equal(await notes.inputValue(), draft, "pending import leaves other settings mounted");
    assert(await preference.isEnabled(), "settings remain usable while Suspense waits");
    release();
    await page.getByRole("alert").filter({ hasText: "This page didn't load" }).waitFor();
    const naming = page.locator('[data-settings-group="workspace-naming"]');
    assert.equal(await naming.getByRole("alert").count(), 1, "the import failure is confined to Workspace naming");
    assert.equal(await notes.inputValue(), draft, "the local failure preserves the other editor's draft");
    await naming.screenshot({ path: join(shots, `chunk-failed-${scheme}-${width}.png`) });
    if (width === 390) {
      await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "2"));
      assert(await naming.evaluate((el) => el.scrollWidth <= el.clientWidth), "the error and recovery action fit at 200% text");
      await naming.screenshot({ path: join(shots, `chunk-failed-${scheme}-${width}-large.png`) });
    }
    const notesGroup = notes.locator('xpath=ancestor::*[@data-settings-group][1]');
    const saved = page.waitForResponse((response) => response.url().endsWith("/api/v1/assistant") && response.request().method() === "PUT" && response.request().postDataJSON().notes === draft);
    await notesGroup.getByRole("button", { name: "Save", exact: true }).click();
    assert((await saved).ok(), "other settings can still save while the chunk is unavailable");
    assert.equal((await (await context.request.get(origin + "/api/v1/assistant", { headers })).json()).notes, draft);
    blocking = false;
    await Promise.all([page.waitForEvent("load"), naming.getByRole("button", { name: "Reload", exact: true }).click()]);
    await naming.getByRole("button", { name: "Workspace naming", exact: true }).click();
    const task = naming.getByLabel("Task folder template", { exact: true });
    assert.equal(await task.inputValue(), savedTemplate, "reload recovers the actual module and saved naming rules");
    assert.equal(await notes.inputValue(), draft, "the other setting persisted");
    assert.equal(await naming.getByRole("alert").count(), 0);
    await task.fill("draft/{task-id}");
    await notes.fill("Another instructions draft");
    assert.equal(await task.inputValue(), "draft/{task-id}", "ordinary parent updates preserve the naming draft");
    await naming.screenshot({ path: join(shots, `chunk-recovered-${scheme}-${width}.png`) });
    if (width === 390) {
      await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "2"));
      // The settings scroller uses content visibility. Give the full crop a
      // viewport so offscreen form rows are painted in the evidence image.
      await page.setViewportSize({ width, height: 6000 });
      assert(await naming.evaluate((el) => el.scrollWidth <= el.clientWidth), "recovered settings fit at 200% text");
      await naming.screenshot({ path: join(shots, `chunk-recovered-${scheme}-${width}-large.png`) });
    }
    assert.equal(pageErrors.length, 0, "the expected import error is caught by the local boundary");
    assert(imports >= 2 && failedURLs.length > 0, "a real module request failed and was fetched again on reload");
    results.push({ scheme, width, imports, failedURLs, pageErrors });
    await context.close();
    console.log(`PASS naming import failure and reload: ${scheme} ${width}px`);
  }
  await writeFile(join(shots, "results.json"), JSON.stringify(results, null, 2) + "\n");
  console.log("PASS real dynamic import isolation, sibling draft/save, reload recovery and normal naming draft");
} catch (error) {
  if (activePage && !activePage.isClosed()) {
    await activePage.screenshot({ path: join(shots, "failure.png"), fullPage: true });
    await writeFile(join(shots, "failure.txt"), await activePage.locator("body").innerText());
  }
  throw error;
} finally { clearTimeout(deadline); await browser.close(); }
