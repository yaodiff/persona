// Review captures for the Machines overview and machine-specific routes. Run against the loopback mock hub
// with MOCK_MANAGERS=0, MOCK_USAGE_MAP=1, two online computers named Computer A and Computer B, and a waiting session.
// node web/e2e/machine-tool-context.mjs <local web URL> <output directory> (CHROME_BIN overrides Chrome).
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [url, out] = process.argv.slice(2);
if (!out || !["localhost", "127.0.0.1", "[::1]"].includes(new URL(url).hostname)) {
  throw new Error("Supply the local mock web URL and a screenshot directory outside the repository.");
}
await mkdir(out, { recursive: true });
const browser = await chromium.launch(process.env.CHROME_BIN
  ? { executablePath: process.env.CHROME_BIN, args: ["--no-sandbox"] }
  : { channel: "chrome" });
const timeout = setTimeout(() => { console.error("TIMEOUT: Machines browser verification"); process.exit(2); }, 360_000);

async function verify(width, theme, textSize) {
  const height = width === 390 ? 844 : 900;
  const context = await browser.newContext({ viewport: { width, height }, hasTouch: width === 390, colorScheme: theme });
  try {
    await context.addInitScript(({ theme, textSize }) => {
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.text-size", textSize);
    }, { theme, textSize });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/api/v1/machines/studio/files/roots", r => r.fulfill({ json: { roots: [] } }));
    await page.route("**/api/v1/machines/studio/files/list*", r => {
      const requested = new URL(r.request().url()).searchParams.get("path");
      const path = requested === "~" ? "/projects" : requested;
      const name = path === "/projects" ? "Notes" : "Review.txt";
      return r.fulfill({ json: { path, canonical_path: path, parent: "/", total: 1,
        entries: [{ name, path: path + "/" + name, kind: path === "/projects" ? "dir" : "file", mode: "rw", size: 24 }] } });
    });
    await page.route("**/api/v1/machines/studio/storage", r => r.fulfill({ json: {
      supported: true, os: "darwin", collected_ms: 8, mounts: [{ mount: "/", device: "Local disk", fs_type: "apfs",
        total_bytes: 1000 * 2 ** 30, used_bytes: 930 * 2 ** 30, avail_bytes: 70 * 2 ** 30, used_percent: 93 }],
    } }));
    const cache = { id: "persona-logs", path: "~/logs", bytes: 8000000 };
    const previews = [];
    await page.route("**/api/v1/machines/studio/storage/hotspots", r => r.fulfill({ json: {
      duration_ms: 20, categories: [{ ...cache, status: "ok", cleanable: true, cleanup_target_id: cache.id }],
    } }));
    await page.route("**/api/v1/machines/studio/storage/cleanup", r => {
      previews.push(r.request().postDataJSON());
      return r.fulfill({ json: { estimated_bytes: cache.bytes, items: [{ ...cache, target_id: cache.id, status: "preview" }] } });
    });
    await page.route("**/api/v1/machines/*/network", (r) => r.fulfill({ json: {
      interfaces: [{ name: "Ethernet", up: true, addresses: [] }], proxy: { active: false },
      probes: [{ name: "Hub connection", target: "example.com", protocol: "https", reachable: true, latency_ms: 24, status_code: 200 }],
    } }));
    await page.goto(new URL("/settings?token=mock", url).href);
    await page.waitForSelector("aside, nav");
    await page.goto(new URL("/machines", url).href);

    const detail = page.getByRole("heading", { level: 1, name: "Computer B", exact: true });
    const nav = page.getByRole("navigation", { name: "View", exact: true });
    const rail = page.getByRole("navigation", { name: "Machines", exact: true });
    const capture = async view => {
      await page.evaluate(() => document.fonts.ready);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
      await page.screenshot({ path: join(out, `${width}-${theme}-${textSize}-${view}.png`), animations: "disabled" });
    };

    const computer = page.getByRole("link").filter({ has: page.getByRole("heading", { name: "Computer B", exact: true }) });
    await computer.waitFor();
    await capture("fleet");
    await computer.click();
    await detail.waitFor();
    const workPath = new URL(page.url()).pathname;
    assert.match(workPath, /^\/machines\/[^/]+$/);
    assert.equal(await rail.isVisible(), width === 1440);
    if (width === 1440) {
      await rail.getByRole("link", { name: /Computer A/ }).click();
      await page.getByRole("heading", { level: 1, name: "Computer A", exact: true }).waitFor();
      await rail.getByRole("link", { name: /Computer B/ }).click();
      await detail.waitFor();
    }
    await capture("work");
    for (const [view, label] of [["files", "Files"], ["storage", "Storage"], ["diagnostics", "Diagnostics"]]) {
      await nav.getByRole("link", { name: label, exact: true }).click();
      await page.waitForURL(u => u.pathname === workPath + "/" + view);
      await detail.waitFor();
      await nav.locator('[aria-current="page"]').filter({ hasText: label }).waitFor();
      if (view === "storage") await page.getByText("Local disk", { exact: false }).first().waitFor();
      if (view === "files") {
        await page.getByTestId("file-entry-Notes").click();
        await page.waitForURL(u => u.searchParams.get("path") === "/projects/Notes");
        await page.reload();
        await page.getByTestId("file-entry-Review.txt").waitFor();
        await detail.waitFor();
        await capture("folder-reload");
        await page.goBack();
        await page.getByTestId("file-entry-Notes").waitFor();
      }
      if (view === "storage") {
        assert.doesNotMatch(await page.getByTestId("storage-mounts-section").innerText(), /undefined/);
        await page.getByTestId("usage-start").click();
        await page.getByTestId("usage-crumbs").waitFor();
        if (width === 390) {
          await page.locator('[data-usage-side="cloud"]').waitFor();
          const cloud = await page.getByTestId("usage-cloud").boundingBox();
          const detail = await page.getByTestId("usage-detail").boundingBox();
          assert(cloud && detail && cloud.y + cloud.height <= detail.y, "Storage status must follow the stacked cloud map");
        }
        await page.getByTestId("usage-map").evaluate(el => el.scrollIntoView({ block: "start" }));
      }
      await capture(view);
    }
    await page.getByRole("link", { name: "Network", exact: true }).click();
    await page.getByText("Hub connection", { exact: true }).waitFor();
    await page.reload();
    await page.getByText("Hub connection", { exact: true }).waitFor();
    await detail.waitFor();
    assert.equal(new URL(page.url()).pathname, workPath + "/network");
    await capture("network");
    await page.getByRole("link", { name: "Back", exact: true }).click();
    await page.waitForURL(u => u.pathname === workPath + "/diagnostics");
    await page.goForward();
    await page.waitForURL(u => u.pathname === workPath + "/network");
    if (width === 390) {
      for (const path of ["/diagnostics", "/storage", "/files", ""]) {
        await page.getByRole("link", { name: "Back", exact: true }).click();
        await page.waitForURL(u => u.pathname === workPath + path);
      }
      await page.getByRole("link", { name: "Back", exact: true }).click();
      await page.waitForURL(u => u.pathname === "/machines");
      await computer.waitFor();
    }
    await page.goto(new URL(workPath + "/storage/cleanup", url).href);
    const suggestion = page.locator("[data-suggestion]").filter({ hasText: "Clean" }).first();
    await suggestion.waitFor();
    await detail.waitFor();
    await page.getByTestId("usage-reclaim").evaluate(el => el.scrollIntoView({ block: "start" }));
    await capture("reclaim");
    await suggestion.click();
    await page.getByTestId("usage-tray-clean").click();
    await page.getByTestId("usage-clean-list").waitFor();
    assert((await page.getByRole("dialog").innerText()).includes("Computer B"));
    await capture("cleanup-confirm");
    await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
    await page.getByTestId("storage-hotspots-section").locator("summary").click();
    await page.getByTestId("storage-scan-hotspots").click();
    await page.getByTestId("storage-preview-cleanup").click();
    await page.getByTestId("storage-preview-list").waitFor();
    assert.deepEqual(previews, [{ targets: [cache.id], dry_run: true }]);
    await capture("cleanup-preview");
    await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
    assert.equal(previews.length, 1);
    await nav.getByRole("link", { name: "Management", exact: true }).click();
    await page.waitForURL(u => u.pathname === workPath + "/management");
    await page.reload();
    await detail.waitFor();
    await page.locator("#engine").waitFor();
    await capture("management");
    await page.goBack();
    await suggestion.waitFor();
    await detail.waitFor();
    assert.deepEqual(errors, []);
    console.log(`PASS ${width}×${height} ${theme} ${textSize}: fleet, computer, Files, Storage, Diagnostics, Network, Management, folder reload, reclaim confirmation and history`);
  } finally {
    await context.close();
  }
}
try {
  for (const theme of ["light", "dark"]) {
    for (const width of [1440, 390]) {
      for (const textSize of ["standard", "large"]) await verify(width, theme, textSize);
    }
  }
  console.log("PASS: Machines browser verification");
} finally {
  await browser.close();
  clearTimeout(timeout);
}
