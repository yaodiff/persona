// Serve web/ with Vite and PERSONA_HUB_URL pointing at an owned scripts/mock/mock_hub.py (MOCK_MANAGERS=1).
// node web/e2e/team-document-catalog.mjs <loopback Vite origin> <screenshot directory> [Chromium executable]
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [, , base, shots, executablePath] = process.argv;
if (!base || !shots || !["localhost", "127.0.0.1", "[::1]"].includes(new URL(base).hostname)) {
  throw new Error("Supply a loopback Vite origin and a screenshot directory");
}
const project = "garden";
const token = "example-project-reader";
const prefix = `/v1/projects/${project}/documents`;

function revision(document, number, content) {
  return { project, document, revision: number, author: "writer", approved_by: "reviewer",
    approved_at: `2026-04-${String(number).padStart(2, "0")}T09:00:00Z`, content,
    sha256: createHash("sha256").update(content).digest("hex") };
}

const agenda = revision("agenda", 2, "Meet at the garden gate.");
const latest = revision("field-guide", 8, "Collect the new seedlings.");
const earlier = revision("field-guide", 3, "Bring a watering can for each planting bed.");
function summary({ content, ...metadata }) { return metadata; }

async function check(browser, width, theme) {
  const context = await browser.newContext({ viewport: { width, height: width > 600 ? 960 : 844 }, locale: "en-US",
    colorScheme: theme, hasTouch: width <= 600, reducedMotion: "reduce", serviceWorkers: "block" });
  try {
    const page = await context.newPage();
    page.setDefaultTimeout(15_000);
    const problems = [];
    const reads = [];
    page.on("pageerror", error => problems.push(error.message));
    await page.route("**/v1/projects/**", async route => {
      const req = route.request();
      const { pathname, search } = new URL(req.url());
      const path = pathname.slice(prefix.length) + search;
      reads.push(path);
      const replies = new Map([
        ["", { items: [summary(agenda)], next: agenda.document }],
        ["?after=agenda", { items: [summary(latest)] }],
        ["/field-guide/approvals", { items: [summary(latest)], next: String(latest.revision) }],
        ["/field-guide/approvals?before=8", { items: [summary(earlier)] }],
        ["/field-guide/revisions/3", earlier],
        ["/field-guide/approved", latest],
      ]);
      if (!pathname.startsWith(prefix) || req.method() !== "GET" || req.headers().authorization !== `Bearer ${token}` || !replies.has(path)) {
        problems.push(`Unexpected project request: ${req.method()} ${pathname}${search}`);
        return route.fulfill({ status: 400, json: {} });
      }
      return route.fulfill({ json: replies.get(path) });
    });
    await page.addInitScript(theme => {
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.language", "en");
    }, theme);
    await page.goto(new URL("/agents?token=mock", base).href);
    await page.locator("main").waitFor();
    // Use the app's New agent command with the same preset as an existing folder's New agent action.
    await page.evaluate(async () => {
      const { openNewAgent } = await import("/src/lib/new-agent.ts");
      openNewAgent({ machineId: "server", cwd: "/home/me/code/weather", launcher: "claude" });
    });
    const dialog = page.getByRole("dialog");
    const button = name => dialog.getByRole("button", { name, exact: true });
    const open = async () => {
      await button("Use a project document").click();
      await dialog.getByLabel("Project ID", { exact: true }).fill(project);
      await dialog.getByLabel("Project access token", { exact: true }).fill(token);
    };
    await open();
    await button("Browse documents").click();
    await button("More documents").click();
    await button("Versions of field-guide").click();
    await button("Older versions").click();
    const preview = button("Preview field-guide, revision 3");
    await preview.scrollIntoViewIfNeeded();
    const bounds = await preview.boundingBox();
    assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width, "Older revision controls fit the window");
    await page.screenshot({ path: join(shots, `history-${width}-${theme}.png`) });
    assert.deepEqual(reads, ["", "?after=agenda", "/field-guide/approvals", "/field-guide/approvals?before=8"]);
    await preview.click();
    const body = dialog.getByLabel("Approved document preview", { exact: true });
    await body.waitFor();
    assert.equal(await body.textContent(), earlier.content);
    await body.scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(shots, `preview-${width}-${theme}.png`) });
    await button("Attach this revision").click();
    await dialog.getByText("garden / field-guide · Revision 3").waitFor();
    await button("Remove document").click();
    await open();
    await dialog.getByLabel("Document ID", { exact: true }).fill("field-guide");
    await button("Load approved revision").click();
    await body.waitFor();
    assert.equal(await body.textContent(), latest.content);
    assert.deepEqual(reads.slice(4), ["/field-guide/revisions/3", "/field-guide/approved"]);
    assert.equal(await dialog.getByLabel("Project access token", { exact: true }).inputValue(), "");
    assert.equal(await page.locator("vite-error-overlay").count(), 0);
    assert.deepEqual(problems, []);
    console.log(`PASS ${width}px ${theme}: catalog, older approved version, attachment and direct lookup`);
  } finally {
    await context.close();
  }
}

await mkdir(shots, { recursive: true });
const browser = await chromium.launch(executablePath ? { executablePath } : { channel: "chrome" });
try {
  for (const width of [1280, 390]) {
    for (const theme of ["light", "dark"]) await check(browser, width, theme);
  }
} finally {
  await browser.close();
}
