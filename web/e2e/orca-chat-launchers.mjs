// Screenshots and checks of New Agent with the Orca bridge's chat launchers (#17047, docs/integrations/orca.md
// "Structured sessions"): on a computer whose Orca can start chats, the model panel has an "Orca chat · <provider>"
// group for each provider Orca has installed, and New Agent's default (its pill, its Start) is what it was. With
// --without (the fake hub run without MOCK_ORCA_CHAT_LAUNCHERS) it checks the panel has no such group: a computer
// without Orca sees no new entries.
//
//   MOCK_ORCA_CHAT_LAUNCHERS=1 PORT=18741 python3 scripts/mock/mock_hub.py &   (scripts/mock/orca_chat_launchers_mock.py)
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18741 npx vite --port 5201 --strictPort &)
//   node web/e2e/orca-chat-launchers.mjs http://127.0.0.1:5201 [--without] [--shots <dir>] [--channel chrome | --executable <browser>]
//
// Runs at 1280 wide, light and dark, in English; --shots saves web-orca-chat-<state>-<theme>[-without].png. Prints one
// line per check and a verdict; exits 1 on a failure.
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
const executablePath = flag("--executable"); // a browser of another Playwright build (a test host's)
const without = args.includes("--without");
if (!base) {
  console.error("usage: node web/e2e/orca-chat-launchers.mjs <web URL> [--without] [--shots <dir>] [--channel chrome | --executable <browser>]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });

const GROUPS = ["Orca chat · Claude Code", "Orca chat · Codex"];
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
    }, theme);
    await page.reload();
    await page.waitForSelector("main", { timeout: 15000 });
    const shot = async (state) => {
      if (!shots) return;
      await page.waitForTimeout(400);
      await page.screenshot({ path: join(shots, `web-orca-chat-${state}-${theme}${without ? "-without" : ""}.png`) });
    };

    await page.evaluate(() => import("/src/lib/new-agent.ts").then((m) => m.openNewAgent()));
    const dialog = page.getByRole("dialog", { name: "New agent", exact: true });
    await dialog.getByRole("button", { name: "Start", exact: true }).first().waitFor({ state: "visible", timeout: 10000 });
    const pill = page.locator('[data-testid="new-agent-model"] [data-model-pill]');
    await until(async () => (await pill.getAttribute("data-model-pill")) === "ready", `${theme}: the model pill never got ready`);
    const pillText = (await pill.textContent()) ?? "";
    if (pillText.includes("Orca")) fail(`${theme}: the default pick is an Orca chat launcher (${pillText})`);
    ok(`${theme}: the default pick stays ${JSON.stringify(pillText.trim())}`);
    await shot("new-agent");

    await pill.click();
    const panel = page.locator("[data-model-panel]");
    await panel.waitFor({ state: "visible", timeout: 8000 });
    await until(async () => (await panel.locator('[role="status"]').count()) === 0, `${theme}: the panel's lists did not load`);
    const text = (await panel.textContent()) ?? "";
    for (const g of GROUPS) {
      if (without && text.includes(g)) fail(`${theme}: ${g} shown on a computer without Orca chat launchers`);
      if (!without && !text.includes(g)) fail(`${theme}: no ${g} group in the model panel`);
    }
    ok(`${theme}: ${without ? "no Orca chat group" : "the Orca chat groups"} in the model panel`);
    await shot("model-panel");
    if (!without) {
      // the Codex chat launcher's own models, as the panel lists a launcher's when it is picked in its first column
      try {
        await panel.getByText(GROUPS[1], { exact: true }).first().click({ timeout: 8000 });
      } catch {
        fail(`${theme}: could not open the ${GROUPS[1]} group`);
      }
      await until(async () => ((await panel.textContent()) ?? "").includes("GPT-5.5"), `${theme}: ${GROUPS[1]} shows no models`);
      ok(`${theme}: ${GROUPS[1]} lists its models`);
      await shot("model-panel-codex");
    }
    await ctx.close();
  }
  console.log(`PASS orca chat launchers${without ? " (without)" : ""}`);
} finally {
  await browser.close();
}
