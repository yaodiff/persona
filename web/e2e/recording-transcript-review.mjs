// Serve web/ with Vite against an owned scripts/mock/mock_hub.py. The recording detail is an invented fixture.
// node web/e2e/recording-transcript-review.mjs <loopback web origin> <screenshot directory> [Chromium executable]
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [, , origin, output, executablePath] = process.argv;
if (!origin || !output || !["localhost", "127.0.0.1", "[::1]"].includes(new URL(origin).hostname)) {
  throw new Error("Supply a loopback web origin and a screenshot directory");
}
const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const path = `/kb/recordings/${id}`;
const first = "We can reserve the greenhouse on Friday.";
const second = "The greenhouse team can deliver the seedlings.";
const last = "Water each tray before noon.";
const fixture = {
  kind: "recordings", id, path: `recordings/${id}`,
  meta: { title: "Community garden planning", kind: "meeting", complete: true, duration: 180, revision: 1,
    started_at: "2026-04-15T09:00:00Z", bookmarks: [{ at: 12, label: "Supplies" }], minutes_state: "ready" },
  segments: [{ start: 12, text: first }, { start: 12, text: second }, { start: 30, text: "Hidden device echo", echo: true }, { start: 61, text: last }],
  minutes: { version: 1, revision: 1, title: "Community garden planning", markdown: "# Garden plan", decisions: [{ at: 61, text: "Water seedlings." }] },
};
const expectedText = [
  `09:00:12  ${first}`, `09:00:12  ${second}`, "09:00:12  Bookmark · Supplies", `09:01:01  ${last}`,
].join("\n");

async function review(browser, width, theme) {
  const context = await browser.newContext({ viewport: { width, height: width > 600 ? 900 : 844 }, timezoneId: "UTC",
    colorScheme: theme, locale: "en-US", reducedMotion: "reduce", serviceWorkers: "block" });
  try {
    await context.addInitScript(theme => {
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.token", "mock");
    }, theme);
    let expired = false;
    let keepBookmarks = true;
    await context.route(url => url.pathname === "/api/v1" + path, route => route.fulfill({
      json: { detail: expired
        ? { ...fixture, meta: { ...fixture.meta, trimmed_at: "2026-04-29T09:00:00Z", bookmarks: keepBookmarks ? fixture.meta.bookmarks : [] } }
        : fixture },
    }));
    const page = await context.newPage();
    page.setDefaultTimeout(15_000);
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.goto(new URL(path, origin).href);
    const button = name => page.getByRole("button", { name, exact: true });
    const open = button("Review transcript");
    await open.waitFor();
    const search = page.getByRole("searchbox", { name: "Find in transcript" });
    assert.equal(await search.count(), 0);
    await open.click();
    await search.fill("GREENHOUSE");
    const spoken = text => page.locator("li[id]").filter({ hasText: text });
    const one = spoken(first);
    const two = spoken(second);
    const firstID = await one.getAttribute("id");
    const secondID = await two.getAttribute("id");
    assert.ok(firstID && secondID && firstID !== secondID);
    const position = value => page.getByRole("status").filter({ hasText: value }).waitFor();
    await search.press("Enter");
    await position("1 of 2");
    assert.ok(await search.evaluate(el => el === document.activeElement));
    await search.press("Enter");
    await position("2 of 2");
    await search.press("Shift+Enter");
    await position("1 of 2");
    await button("Next match").click();
    await page.waitForFunction(id => document.activeElement?.id === id, secondID);
    await button("Previous match").click();
    await page.waitForFunction(id => document.activeElement?.id === id, firstID);
    await button("Bookmarks").click();
    const mark = page.getByRole("list", { name: "Transcript bookmarks" }).getByRole("button", { name: /Supplies/ });
    await mark.click();
    const bookmarkID = await spoken("Supplies").getAttribute("id");
    assert.ok(bookmarkID !== firstID && bookmarkID !== secondID);
    await page.waitForFunction(id => document.activeElement?.id === id, bookmarkID);
    await search.scrollIntoViewIfNeeded();
    await search.evaluate(el => el.scrollIntoView({ block: "center" }));
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "Page overflows horizontally");
    await page.screenshot({ path: join(output, `review-${width}-${theme}.png`) });

    // Search and the selected bookmark must not filter the downloaded recording.
    const checkDownload = async expected => {
      const downloaded = page.waitForEvent("download");
      await button("Download transcript").click();
      const download = await downloaded;
      assert.equal(download.suggestedFilename(), `transcript-${id}.txt`);
      const bytes = [];
      for await (const chunk of await download.createReadStream()) bytes.push(chunk);
      assert.deepEqual(Buffer.concat(bytes), Buffer.from(expected));
    };
    await checkDownload(expectedText);
    await search.fill("no matching sentence");
    assert.ok(await button("Next match").isDisabled());
    await search.press("Escape");
    await search.waitFor({ state: "hidden" });
    assert.ok(await open.evaluate(el => el === document.activeElement));

    expired = true;
    await page.reload();
    await page.getByText("The transcript has expired.", { exact: true }).waitFor();
    assert.equal(await spoken(first).count(), 0);
    await open.click();
    await checkDownload("09:00:12  Bookmark · Supplies");
    keepBookmarks = false;
    await page.reload();
    await page.getByText("The transcript has expired.", { exact: true }).waitFor();
    assert.equal(await open.count(), 0);
    assert.equal(await button("Download transcript").count(), 0);
    assert.deepEqual(errors, []);
    console.log(`PASS ${width}px ${theme}: repeated timestamps, keyboard, bookmark, complete download, expiry`);
  } finally {
    await context.close();
  }
}

await mkdir(output, { recursive: true });
const browser = await chromium.launch(executablePath ? { executablePath } : { channel: "chrome" });
try {
  for (const width of [1280, 390]) for (const theme of ["light", "dark"]) await review(browser, width, theme);
} finally {
  await browser.close();
}
