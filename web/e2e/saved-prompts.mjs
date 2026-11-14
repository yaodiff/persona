// End-to-end check of saved prompts (#7329, docs/web/web.md "Saved prompts"), as the user does it: Settings › Saved
// Prompts lists them in the "/" list's order; New Prompt says a rule before asking the hub and adds one; the main chat's
// "/" list offers them after its own commands, marked Saved, and picking one puts its text in the box without sending it;
// a message of yours (right click on a wide screen, a long press on a phone) › Save as Prompt… opens the editor filled
// with it; a row moves down and back, and is deleted once confirmed.
//
// Needs the web page against the fake hub with its three saved prompts, and Chrome (Edge with --channel msedge, any
// Chromium through --executable):
//
//   MOCK_SAVED_PROMPTS=1 PORT=18797 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18797 npx vite --host 127.0.0.1 --port 5797 --strictPort &)
//   node web/e2e/saved-prompts.mjs http://127.0.0.1:5797 --hub http://127.0.0.1:18797 [--shots <dir>] [--channel msedge | --executable <chromium>]
//
// Runs wide (1440×900) and phone width (390×844, touch), light and dark, in English; --shots saves the settings page, the
// editor, the "/" list and Save as Prompt of each. Prints one line per check and a verdict; exits 1 on a failure and after
// six minutes. Every request outside the two loopback servers is refused; what it adds it deletes again.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const hub = (flag("--hub") ?? "").replace(/\/$/, "");
const shots = flag("--shots");
const executable = flag("--executable");
const channel = flag("--channel") ?? "chrome";
if (!base || !hub) {
  console.error("usage: node web/e2e/saved-prompts.mjs <web URL> --hub <mock hub URL> [--shots <dir>] [--channel msedge | --executable <chromium>]");
  process.exit(2);
}
for (const url of [base, hub]) {
  if (!["127.0.0.1", "localhost"].includes(new URL(url).hostname)) {
    console.error(`only a loopback mock is allowed: ${url}`);
    process.exit(2);
  }
}
if (shots) mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 6 minutes");
  process.exit(1);
}, 360_000).unref();

const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}

const get = async (path) => (await fetch(`${hub}${path}`, { headers: { Authorization: "Bearer mock" }, signal: AbortSignal.timeout(5000) })).json();
const listed = async () => ((await get("/api/v1/assistant/saved-prompts")).prompts ?? []).map((p) => p.name);
const SEEDED = ["review", "release-notes", "standup"];
if (JSON.stringify(await listed()) !== JSON.stringify(SEEDED)) fail(`the fake hub does not have the three saved prompts (MOCK_SAVED_PROMPTS=1): ${await listed()}`);
// the mock hub's PR review on the MacBook, and the message of yours in it
const REVIEW = "a1f3c2d4-0000-4000-8000-000000000006";
const ASK = "看一下 PR #42"; // i18n-ignore: the mock hub's message of yours in that chat

async function open(browser, theme, phone) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    colorScheme: theme,
    locale: "en-US",
    hasTouch: phone,
    isMobile: phone,
  });
  await ctx.route("**/*", (route) => {
    const origin = new URL(route.request().url()).origin;
    return [new URL(base).origin, new URL(hub).origin].includes(origin) ? route.continue() : route.abort();
  });
  await ctx.addInitScript((theme) => {
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
  }, theme);
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
  return { ctx, page, errors };
}

/** Moves to an in-app path the way a link does (no reload: the page keeps what it holds). */
const go = (page, path) =>
  page.evaluate((path) => {
    history.pushState(null, "", path);
    dispatchEvent(new PopStateEvent("popstate"));
  }, path);

const shot = async (page, name) => {
  if (!shots) return;
  await page.waitForTimeout(350); // let the open animations finish
  await page.screenshot({ path: join(shots, `${name}.png`) });
};

const rowNames = (page) => page.locator("[data-saved-prompt]").evaluateAll((els) => els.map((e) => e.getAttribute("data-saved-prompt")));
async function expectRows(page, want, where) {
  await page.waitForFunction((want) => JSON.stringify([...document.querySelectorAll("[data-saved-prompt]")].map((e) => e.getAttribute("data-saved-prompt"))) === want, JSON.stringify(want)).catch(async () => fail(`${where}: the rows are ${await rowNames(page)}, not ${want}`));
}

async function settings(page, where, name) {
  await go(page, "/settings?tab=prompts");
  await page.locator('[data-saved-prompt="review"]').waitFor({ state: "visible" });
  await expectRows(page, SEEDED, where);
  ok(`${where}: Settings › Saved Prompts lists them in their order`);
  await shot(page, `${where}-1-settings`);

  await page.locator('[data-testid="saved-prompt-add"]').click();
  const dialog = page.locator('[role="dialog"]');
  await dialog.waitFor({ state: "visible" });
  if (!(await dialog.getByText("New Saved Prompt", { exact: true }).count())) fail(`${where}: the editor is not titled New Saved Prompt`);
  const field = dialog.locator('[data-testid="saved-prompt-name"]');
  await field.fill("two words");
  const note = dialog.locator(`[id="${await field.getAttribute("aria-describedby")}"]`);
  if (!(await note.textContent())?.startsWith("One word of letters")) fail(`${where}: a name with a space says no rule: ${await note.textContent()}`);
  await field.fill("Review");
  if ((await note.textContent()) !== "There is already a prompt called “/Review”.") fail(`${where}: a name taken says ${await note.textContent()}`);
  await field.fill(name);
  if ((await note.textContent()) !== `Type “/${name}” in any chat and pick it to use it.`) fail(`${where}: the hint says ${await note.textContent()}`);
  await dialog.locator('[data-testid="saved-prompt-text"]').fill("Summarize this conversation in five bullet points,\nthen list what is still open.");
  await shot(page, `${where}-2-editor`);
  await dialog.locator('[data-testid="saved-prompt-save"]').click();
  await dialog.waitFor({ state: "hidden" });
  await expectRows(page, [...SEEDED, name], where);
  if (JSON.stringify(await listed()) !== JSON.stringify([...SEEDED, name])) fail(`${where}: the hub has ${await listed()}`);
  ok(`${where}: New Prompt says its rules before asking, and adds /${name} at the end`);
}

async function slash(page, where, name) {
  await go(page, "/");
  const box = page.locator("main textarea").last();
  await box.waitFor({ state: "visible" });
  await box.click();
  await box.fill("/");
  const options = page.locator("[data-slash-palette] [role=option]");
  await options.first().waitFor({ state: "visible" });
  const texts = await options.allTextContents();
  const saved = texts.filter((t) => t.endsWith("Saved")).map((t) => t.slice(0, -"Saved".length));
  if (JSON.stringify(saved) !== JSON.stringify([...SEEDED, name])) fail(`${where}: the / list's saved rows are ${saved} (${texts})`);
  if (!texts.at(-1)?.endsWith("Saved") || texts.findIndex((t) => t.endsWith("Saved")) === 0) fail(`${where}: the saved prompts are not after the chat's own commands: ${texts}`);
  await box.fill("/rel");
  await page.waitForFunction(() => document.querySelector("[data-slash-palette] [role=option][aria-selected=true]")?.textContent === "release-notesSaved");
  await shot(page, `${where}-3-slash`);
  await box.press("Enter");
  if ((await box.inputValue()) !== "Write the release notes for the commits since the last tag: what changed for users first, then the fixes.") fail(`${where}: picking it typed ${await box.inputValue()}`);
  if (await page.locator("[data-slash-palette]").count()) fail(`${where}: the list stays open after a pick`);
  ok(`${where}: the main chat's / list offers them after its own commands, and a pick fills the box`);
  await box.fill("");
}

async function fromMessage(page, ctx, where, phone) {
  await go(page, `/s/macbook/${REVIEW}`);
  const mine = page.locator("main").getByText(ASK, { exact: true }).first();
  await mine.waitFor({ state: "visible" });
  await mine.scrollIntoViewIfNeeded();
  if (phone) {
    const box = await mine.boundingBox();
    const cdp = await ctx.newCDPSession(page);
    const at = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
    await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [at] });
    await page.waitForTimeout(700);
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  } else {
    await mine.click({ button: "right" });
  }
  const menu = page.locator('[role="menu"]').last();
  await menu.waitFor({ state: "visible" });
  const item = menu.getByRole("menuitem", { name: "Save as Prompt…" });
  if (!(await item.count())) fail(`${where}: no Save as Prompt… on your message`);
  await shot(page, `${where}-4-menu`);
  await item.click();
  const dialog = page.locator('[role="dialog"]');
  await dialog.waitFor({ state: "visible" });
  if (!(await dialog.getByText("Save as Prompt", { exact: true }).count())) fail(`${where}: the editor is not titled Save as Prompt`);
  if ((await dialog.locator('[data-testid="saved-prompt-text"]').inputValue()) !== ASK) fail(`${where}: the editor is not filled with the message`);
  if (!(await dialog.locator('[data-testid="saved-prompt-name"]').inputValue())) fail(`${where}: no name suggested`);
  await shot(page, `${where}-5-from-message`);
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });
  ok(`${where}: Save as Prompt… opens the editor filled with your message`);
}

async function tidy(page, where, name) {
  await go(page, "/settings?tab=prompts");
  await page.locator(`[data-saved-prompt="${name}"]`).waitFor({ state: "visible" });
  await page.getByRole("button", { name: "Move /review Down", exact: true }).click();
  await expectRows(page, ["release-notes", "review", "standup", name], where);
  await page.getByRole("button", { name: "Move /review Up", exact: true }).click();
  await expectRows(page, [...SEEDED, name], where);
  ok(`${where}: a row moves down and back`);
  const row = page.locator(`[data-saved-prompt="${name}"]`);
  await row.getByRole("button", { name: `Delete /${name}`, exact: true }).click();
  await row.getByRole("button", { name: "Delete", exact: true }).click();
  await expectRows(page, SEEDED, where);
  if (JSON.stringify(await listed()) !== JSON.stringify(SEEDED)) fail(`${where}: the hub still has ${await listed()}`);
  ok(`${where}: the row is deleted once confirmed`);
}

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel });
try {
  for (const phone of [false, true]) {
    for (const theme of ["light", "dark"]) {
      const where = `${phone ? "phone" : "wide"}-${theme}`;
      const name = `summary-${Date.now().toString(36).slice(-4)}`;
      const { ctx, page, errors } = await open(browser, theme, phone);
      await settings(page, where, name);
      await slash(page, where, name);
      await fromMessage(page, ctx, where, phone);
      await tidy(page, where, name);
      if (errors.length) fail(`${where}: page errors: ${errors.join(" | ")}`);
      await ctx.close();
    }
  }
} finally {
  await browser.close();
}
console.log("PASS saved prompts: settings, editor, the / list and Save as Prompt, wide and phone, light and dark");
process.exit(0);
