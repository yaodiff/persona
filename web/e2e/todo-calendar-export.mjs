// With the web app served against the local mock hub:
// node web/e2e/todo-calendar-export.mjs http://127.0.0.1:<web-port>
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { chromium } from "playwright-core";

const base = new URL(process.argv[2]);
assert.equal(base.hostname, "127.0.0.1", "use a local mock");
const stamp = "2026-11-02T09:00:00Z";
const tasks = [
  { id: "one", title: "Pick up books /api/v1/machines/private-machine/file/download?path=private-book&token=synthetic-key", due_date: "2026-11-03" },
  { id: "two", title: "Attend rehearsal", due_at: "2026-11-04T16:00:00Z" },
  { id: "three", title: "Next week", due_date: "2026-11-10" },
].map(todo => ({ ...todo, created_at: stamp, updated_at: stamp, agents: [] }));

const browser = await chromium.launch({ channel: "chrome" });
try {
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 844 }, locale: "en-US", timezoneId: "America/Los_Angeles" });
    await context.addInitScript(() => localStorage.setItem("persona.language", "en"));
    await context.route(/\/api\/v1\/todos(?:\?.*)?$/, route => route.fulfill({ json: { todos: tasks } }));
    const page = await context.newPage();
    page.setDefaultTimeout(15000);
    await page.goto(new URL("/todos?view=calendar&mode=week&date=2026-11-03&token=mock", base).href);
    const mode = width === 390 ? "day" : "week";
    const calendar = page.locator(`[data-testid="todo-calendar"][data-mode="${mode}"]`);
    await calendar.getByText(tasks[0].title, { exact: true }).waitFor();
    await context.setOffline(true);
    const [file] = await Promise.all([
      page.waitForEvent("download"),
      calendar.getByRole("button", { name: "Download deadlines" }).click(),
    ]);
    assert.equal(file.suggestedFilename(), mode === "day" ? "deadlines-2026-11-03.ics" : "deadlines-2026-11-01.ics");
    const contents = await readFile(await file.path(), "utf8");
    const titles = [...contents.matchAll(/^SUMMARY:([^\r\n]+)/gm)].map(match => match[1]).sort();
    assert.deepEqual(titles, mode === "day" ? ["Pick up books …"] : ["Attend rehearsal", "Pick up books …"]);
    assert.doesNotMatch(contents, /private-machine|private-book|synthetic-key/);
    assert.equal(contents.match(/^DTSTAMP:20261102T090000Z\r$/gm)?.length, titles.length);
    console.log(`${width}px ${mode}: verified offline filename and ${titles.length} deadlines`);
    await context.close();
  }
} finally {
  await browser.close();
}
