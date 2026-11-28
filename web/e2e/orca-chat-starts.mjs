// Screenshots and checks of "Start new Agents as Orca chats" (#17650, docs/integrations/engines.md "Orca chats as New
// Agent's default"): on a computer with Orca in its engine set, its Engine section has the switch, off; a click turns it on
// (saved at once); New Agent then says the default start opens as an Orca chat, and the line goes once a launcher is picked.
// With --without (the fake hub run without MOCK_ORCA_CHAT_STARTS) it checks there is no switch and no line.
//
//   MOCK_MODEL_DEFAULTS=1 MOCK_ORCA_CHAT_LAUNCHERS=1 MOCK_ORCA_CHAT_STARTS=1 PORT=18742 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18742 npx vite --port 5202 --strictPort &)
//   node web/e2e/orca-chat-starts.mjs http://127.0.0.1:5202 [--without] [--shots <dir>] [--channel chrome | --executable <browser>]
//
// Runs at 1280 wide, light and dark, in English; --shots saves web-orca-chat-starts-<state>-<theme>[-without].png. Prints
// one line per check and a verdict; exits 1 on a failure.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const shots = flag("--shots");
const channel = flag("--channel");
const executablePath = flag("--executable");
const without = args.includes("--without");
if (!base) {
  console.error("usage: node web/e2e/orca-chat-starts.mjs <web URL> [--without] [--shots <dir>] [--channel chrome | --executable <browser>]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });

const TITLE = "Start new Agents as Orca chats";
const LINE = "Opens as Orca chat · Claude Code";
const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}

async function until(check, what, tries = 80) {
  for (let i = 0; i < tries; i++) {
    if (await check()) return;
    await new Promise((r) => setTimeout(r, 150));
  }
  fail(what);
}

const browser = await chromium.launch(executablePath ? { executablePath } : channel ? { channel } : {});
try {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 }, deviceScaleFactor: 2, colorScheme: theme, locale: "en-US" });
    const page = await ctx.newPage();
    await page.goto(`${base}/?token=mock`);
    await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
    await page.evaluate((t) => {
      localStorage.setItem("persona.theme", t);
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("new.machine", "server");
      localStorage.removeItem("new.mode.server");
    }, theme);
    await fetch(`${base}/mock/reset`).catch(() => {});
    const shot = async (state) => {
      if (!shots) return;
      await page.waitForTimeout(400);
      await page.screenshot({ path: join(shots, `web-orca-chat-starts-${state}-${theme}${without ? "-without" : ""}.png`) });
    };

    // The computer's Engine section.
    await page.goto(`${base}/machines/server#engine`);
    const section = page.locator('[data-testid="engine-section"]');
    await section.waitFor({ state: "visible", timeout: 15000 });
    await section.scrollIntoViewIfNeeded();
    const toggle = page.locator('[data-testid="orca-chat-starts"] [role="switch"]');
    if (without) {
      await page.waitForTimeout(800);
      if (await toggle.count()) fail(`${theme}: the switch shows on a computer without Orca in its set`);
      ok(`${theme}: no switch without Orca`);
      await shot("engine");
    } else {
      await toggle.waitFor({ state: "visible", timeout: 10000 });
      if ((await toggle.getAttribute("aria-label")) !== TITLE) fail(`${theme}: the switch is not named ${TITLE}`);
      if ((await toggle.getAttribute("aria-checked")) !== "false") fail(`${theme}: the switch is not off by default`);
      ok(`${theme}: the switch is there, off`);
      await shot("engine-off");
      await toggle.click();
      await until(async () => (await toggle.getAttribute("aria-checked")) === "true", `${theme}: the switch did not turn on`);
      await page.waitForTimeout(600); // the hub's answer and its machine push
      if ((await toggle.getAttribute("aria-checked")) !== "true") fail(`${theme}: the switch went back after the save`);
      ok(`${theme}: turned on and kept`);
      await shot("engine-on");
    }

    // New Agent.
    await page.goto(`${base}/`);
    await page.waitForSelector("main", { timeout: 15000 });
    await page.evaluate(() => import("/src/lib/new-agent.ts").then((m) => m.openNewAgent()));
    const dialog = page.getByRole("dialog", { name: "New agent", exact: true });
    await dialog.getByRole("button", { name: "Start", exact: true }).first().waitFor({ state: "visible", timeout: 10000 });
    const pill = page.locator('[data-testid="new-agent-model"] [data-model-pill]');
    await until(async () => (await pill.getAttribute("data-model-pill")) === "ready", `${theme}: the model pill never got ready`);
    const line = page.locator('[data-testid="new-agent-orca-chat"]');
    await page.locator('[data-testid="new-agent-model"]').scrollIntoViewIfNeeded(); // the box, its notes and Start in view
    if (without) {
      await page.waitForTimeout(600);
      if (await line.count()) fail(`${theme}: New Agent names an Orca chat without the switch`);
      ok(`${theme}: New Agent names no Orca chat`);
      await shot("new-agent");
    } else {
      await until(async () => (await line.count()) > 0 && ((await line.textContent()) ?? "").startsWith(LINE), `${theme}: New Agent does not say where the start goes`);
      ok(`${theme}: New Agent says ${JSON.stringify(((await line.textContent()) ?? "").trim())}`);
      await line.scrollIntoViewIfNeeded();
      await shot("new-agent");
    }

    if (!without) {
      // Turn it back off for the next run (the switch is the hub's, shared by both themes).
      await fetch(`${base}/api/v1/machines/server/orca-chat-starts`, { method: "PUT", headers: { "Content-Type": "application/json", Authorization: "Bearer mock" }, body: JSON.stringify({ on: false }) }).catch(() => {});
    }
    await ctx.close();
  }
  console.log(`PASS orca chat starts${without ? " (without)" : ""}`);
} finally {
  await browser.close();
}
