// Real-browser acceptance for remote copy / compress progress and Cancel (#6113).
// Start an exclusively owned mock and a Vite page backed by it:
//   MOCK_FILES=1 MOCK_FILE_OPS=1 MOCK_FILE_OP_DELAY=20 PORT=18961 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18961 npx vite --port 15961 --strictPort &)
//   node web/e2e/file-operations.mjs http://127.0.0.1:15961 --hub http://127.0.0.1:18961 --shots <artifact-directory>
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const hub = (flag("--hub") ?? base).replace(/\/$/, "");
const shots = flag("--shots");
const executable = flag("--executable");
const largeText = args.includes("--large-text");
if (!base || !shots) {
  console.error("usage: node web/e2e/file-operations.mjs <web URL> [--hub <mock URL>] --shots <artifact-directory> [--executable <chromium>] [--large-text]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 3 minutes");
  process.exit(1);
}, 180_000).unref();

const results = [];
const fail = (message) => {
  console.log(`FAIL ${message}`);
  process.exitCode = 1;
  throw new Error(message);
};
const mock = async (path) => {
  const response = await fetch(`${hub}${path}`, { headers: { Authorization: "Bearer mock" }, signal: AbortSignal.timeout(5000) });
  if (!response.ok) fail(`${path}: HTTP ${response.status}`);
  return response.json();
};
const waitFor = async (predicate, what) => {
  const deadline = Date.now() + 12_000;
  let last;
  while (Date.now() < deadline) {
    last = await predicate();
    if (last) return last;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  fail(`${what}; last=${JSON.stringify(last)}`);
};
const names = async () => new Set((await mock("/api/v1/machines/macbook/fs?path=~")).entries.map((entry) => entry.name));

async function open(browser, colorScheme, drill = false) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 2, colorScheme, locale: "zh-CN" });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(String(error)));
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15_000 });
  await page.evaluate(({ theme, large, drill }) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "zh-Hans");
    if (large) localStorage.setItem("persona.text-size", "large");
    if (drill) localStorage.setItem("persona.files.view", "drill");
  }, { theme: colorScheme, large: largeText, drill });
  await page.goto(`${base}/machines/macbook`);
  const files = page.getByRole("button", { name: "文件", exact: true });
  await files.waitFor({ state: "visible", timeout: 20_000 });
  await files.click();
  const more = page.getByRole("button", { name: "「notes.txt」的更多操作", exact: true });
  await more.waitFor({ state: "visible", timeout: 20_000 });
  await more.click();
  return { ctx, page, errors };
}

async function run(browser, colorScheme, kind) {
  const copying = kind === "copy";
  const { ctx, page, errors } = await open(browser, colorScheme);
  try {
    await page.getByText(copying ? "创建副本" : "压缩", { exact: true }).click();
    const progress = page.locator('[role="progressbar"][aria-label="notes.txt"]');
    await progress.waitFor({ state: "visible", timeout: 12_000 });
    await waitFor(async () => Number(await progress.getAttribute("aria-valuenow")) >= 30, `${colorScheme} ${kind}: no polled byte progress`);
    const detailText = await progress.evaluate((element) => element.nextElementSibling?.textContent ?? "");
    if (!detailText.includes(" / ")) fail(`${colorScheme} ${kind}: file / byte counts are not visible`);
    const cancelName = copying ? "取消复制「notes.txt」" : "取消压缩「notes.zip」";
    const cancel = page.getByRole("button", { name: cancelName, exact: true });
    await cancel.waitFor({ state: "visible" });
    await progress.scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(shots, `web-${kind}-progress-${colorScheme}${largeText ? "-large" : ""}.png`) });
    await cancel.click();
    await progress.waitFor({ state: "detached", timeout: 5000 });
    const operation = await waitFor(async () => {
      const operations = (await mock("/mock/file-operations")).operations;
      return operations.find((op) => op.kind === kind && op.state === "cancelled");
    }, `${colorScheme} ${kind}: the mock did not see Cancel`);
    const left = await names();
    const output = copying ? "notes 2.txt" : "notes.zip";
    if (left.has(output)) fail(`${colorScheme} ${kind}: ${output} remained after Cancel`);
    if (errors.length) fail(`${colorScheme} ${kind}: browser errors ${errors.join("; ")}`);
    results.push({ colorScheme, kind, operation: { id: operation.id, state: operation.state }, outputRemoved: true });
    console.log(`ok  ${colorScheme} ${kind}: progress, Cancel and no partial output`);
  } finally {
    await ctx.close();
  }
}

async function runReturn(browser, colorScheme) {
  const { ctx, page, errors } = await open(browser, colorScheme, true);
  try {
    await page.getByText("创建副本", { exact: true }).click();
    const progress = page.locator('[role="progressbar"][aria-label="notes.txt"]');
    await progress.waitFor({ state: "visible", timeout: 12_000 });
    await page.locator('button[title="/Users/me/code"]').click();
    await progress.waitFor({ state: "detached", timeout: 5000 });
    await page.getByRole("button", { name: "后退", exact: true }).click();
    await progress.waitFor({ state: "visible", timeout: 5000 });
    const cancel = page.getByRole("button", { name: "取消复制「notes.txt」", exact: true });
    await cancel.waitFor({ state: "visible" });
    await page.screenshot({ path: join(shots, `web-copy-returned-${colorScheme}${largeText ? "-large" : ""}.png`) });
    await cancel.click();
    await progress.waitFor({ state: "detached", timeout: 5000 });
    await waitFor(async () => {
      const operations = (await mock("/mock/file-operations")).operations;
      return operations.find((op) => op.kind === "copy" && op.state === "cancelled");
    }, `${colorScheme} return: the mock did not see Cancel after directory restoration`);
    if ((await names()).has("notes 2.txt")) fail(`${colorScheme} return: a partial copy remained after Cancel`);
    if (errors.length) fail(`${colorScheme} return: browser errors ${errors.join("; ")}`);
    results.push({ colorScheme, kind: "copy", scenario: "leave-return-cancel", outputRemoved: true });
    console.log(`ok  ${colorScheme} return: live progress and Cancel restored after directory navigation`);
  } finally {
    await ctx.close();
  }
}

async function runCompletionAway(browser, colorScheme) {
  const { ctx, page, errors } = await open(browser, colorScheme, true);
  try {
    await page.getByText("创建副本", { exact: true }).click();
    const progress = page.locator('[role="progressbar"][aria-label="notes.txt"]');
    await progress.waitFor({ state: "visible", timeout: 12_000 });
    await page.locator('button[title="/Users/me/code"]').click();
    await progress.waitFor({ state: "detached", timeout: 5000 });
    await waitFor(async () => {
      const operations = (await mock("/mock/file-operations")).operations;
      return operations.find((op) => op.kind === "copy" && op.state === "done");
    }, `${colorScheme} completion: the mock operation did not finish while away`);
    const notice = page.getByText("已为 1 项创建副本", { exact: true });
    await notice.waitFor({ state: "visible", timeout: 5000 });
    await page.getByRole("button", { name: "后退", exact: true }).click();
    await notice.waitFor({ state: "visible", timeout: 5000 });
    await page.screenshot({ path: join(shots, `web-copy-completed-return-${colorScheme}${largeText ? "-large" : ""}.png`) });
    if (!(await names()).has("notes 2.txt")) fail(`${colorScheme} completion: the completed copy is missing`);
    if (errors.length) fail(`${colorScheme} completion: browser errors ${errors.join("; ")}`);
    results.push({ colorScheme, kind: "copy", scenario: "leave-complete-return", completionNotice: true });
    console.log(`ok  ${colorScheme} completion: workspace notice survives completion away and return`);
  } finally {
    await ctx.close();
  }
}

const browser = await chromium.launch(executable ? { executablePath: executable, headless: true } : { channel: "chrome", headless: true });
try {
  await run(browser, "light", "copy");
  await run(browser, "dark", "zip");
  await runReturn(browser, "light");
  await runCompletionAway(browser, "dark");
  writeFileSync(join(shots, "results.json"), JSON.stringify(results, null, 2) + "\n");
  console.log("PASS web remote file operations");
} finally {
  await browser.close();
}
