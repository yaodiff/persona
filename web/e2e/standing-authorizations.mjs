// Run against a loopback production preview. All API responses and credentials are synthetic.
// BASE_URL=http://127.0.0.1:51794 PERSONA_E2E_CHROMIUM=<chrome> SHOTS_DIR=<directory> node web/e2e/standing-authorizations.mjs
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:51794");
assert(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname) && base.protocol === "http:", "Use a loopback HTTP preview");
assert(!base.username && !base.password && base.pathname === "/" && !base.search && !base.hash, "Use an origin without credentials");
const executablePath = process.env.PERSONA_E2E_CHROMIUM;
assert(executablePath, "Set PERSONA_E2E_CHROMIUM");
const output = resolve(process.env.SHOTS_DIR || ".notes/standing-authorizations");
await mkdir(output, { recursive: true });
const fixture = () => [
  { id: "sa_example/one", machine_id: "example-computer", directory: "/code/example", actions: ["merge", "deploy-test"], conditions: ["ci_green", "no_conflicts"], quote: "Merge this repository after its checks pass without asking again.", source: "Main conversation", granted_at: "2026-11-11T07:36:00Z", reversible: true },
  { id: "sa_example_two", machine_id: "example-computer", directory: "/code/another-example", actions: ["push"], conditions: ["Only the reviewed branch"], quote: "Push reviewed changes to the example repository.", source: "Main conversation", granted_at: "2026-11-11T08:00:00Z", reversible: false },
];
const browser = await chromium.launch({ executablePath, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("FAIL standing authorizations timed out"); process.exit(1); }, 180_000);
try {
  for (const theme of ["light", "dark"]) {
    for (const [width, large, lang] of [[1440, false, "en"], [390, true, "ja"], [1440, true, "zh-Hans"]]) {
      const s = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/settings-standing.json`, import.meta.url), "utf8"));
      const common = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/common.json`, import.meta.url), "utf8"));
      const context = await browser.newContext({ viewport: { width, height: large ? 1200 : 1100 }, colorScheme: theme, reducedMotion: "reduce", timezoneId: "UTC" });
      await context.addInitScript(({ theme, lang, large }) => {
        localStorage.setItem("persona.token", "synthetic-token"); localStorage.setItem("persona.theme", theme);
        localStorage.setItem("persona.language", lang); localStorage.setItem("persona.text-size", large ? "large" : "standard");
      }, { theme, lang, large });
      let grants = fixture(), failDelete = true, failRead = false;
      const writes = [], errors = [];
      await context.route("**/*", route => new URL(route.request().url()).origin === base.origin ? route.continue() : route.abort());
      await context.route("**/api/v1/**", async route => {
        const request = route.request(), path = new URL(request.url()).pathname.replace(/^\/api\/v1/, "");
        if (path.startsWith("/assistant/standing-authorizations")) {
          assert.equal(request.headers()["x-persona-manager"], "assistant");
          if (request.method() === "DELETE") {
            writes.push(path); assert.equal(path, "/assistant/standing-authorizations/sa_example%2Fone");
            if (failDelete) { failDelete = false; return route.fulfill({ status: 503, json: { error: "Synthetic storage failure" } }); }
            grants = grants.filter(grant => grant.id !== "sa_example/one");
            return route.fulfill({ json: { ok: true, revision: 2 } });
          }
          if (failRead) return route.fulfill({ status: 403, json: { error: "Synthetic access restriction" } });
          return route.fulfill({ json: { revision: 1, authorizations: grants } });
        }
        assert(["GET", "HEAD"].includes(request.method()), `Unexpected mutation: ${path}`);
        const responses = {
          "/assistant": { manager: "assistant", conversations: [], default_permission_mode: "default" },
          "/assistant/memory": { files: [] }, "/assistant/tasks": { tasks: [] },
          "/sessions": { sessions: [], total: 0, has_more: false }, "/machines": { machines: [] },
          "/managers": { managers: [{ id: "assistant", name: "Persona", builtin: true, online: true }], default: "assistant" },
          "/interactions": { interactions: [] }, "/todos": { todos: [] }, "/agents/sessions": { sessions: [] },
          "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
          "/personas": { personas: [] }, "/assistant/personas": { personas: [] }, "/extensions": { extensions: [] }, "/workspaces": { workspaces: [] },
        };
        return route.fulfill({ json: responses[path] ?? {} });
      });
      await context.routeWebSocket(/\/api\/v1\/ws(?:\?|$)/, ws => {
        ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", features: ["managers"], hub_name: "Example hub" }));
        ws.onMessage(message => { const event = JSON.parse(String(message)); if (event.type === "app_ping") ws.send(JSON.stringify({ type: "pong", id: event.id })); });
      });
      const page = await context.newPage(); page.setDefaultTimeout(15000);
      page.on("pageerror", error => errors.push(error.message));
      await page.goto(new URL("/settings?tab=persona", base).href);
      const group = page.locator('[data-settings-group="standing-authorizations"]');
      await group.locator("article").first().waitFor(); await group.scrollIntoViewIfNeeded();
      await page.evaluate(() => document.fonts.ready);
      if (large) await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "2"));
      const suffix = `${theme}${large ? `-${lang}-large-${width}` : ""}`;
      assert.equal(await group.locator("article").count(), 2);
      assert.equal(await group.getByText(s.quote, { exact: true }).count(), 2);
      for (const [index, grant] of fixture().entries()) {
        const row = group.locator("article").nth(index);
        assert.equal(await row.locator("dt").count(), 8);
        for (const value of [grant.machine_id, grant.directory, grant.quote, s.source_main_chat]) assert((await row.textContent()).includes(value)); // the source as the page words it (#7181)
        assert.equal(await row.locator("time").getAttribute("datetime"), grant.granted_at);
      }
      assert(await group.evaluate(el => el.scrollWidth <= el.clientWidth), "Grant group fits its scroller");
      assert(await group.locator("button").evaluateAll(buttons => buttons.every(button => {
        const box = button.getBoundingClientRect(), range = document.createRange(); range.selectNodeContents(button);
        return box.height >= 44 && box.width >= 44 && getComputedStyle(button).whiteSpace === "nowrap" && range.getBoundingClientRect().width <= button.clientWidth;
      })), "Controls fit on one line with 44px targets");
      await page.screenshot({ path: join(output, `standing-authorizations-${suffix}.png`) });
      await group.getByRole("button", { name: s.revoke, exact: true }).first().click();
      const dialog = page.getByRole("dialog"); await dialog.waitFor();
      // Measure the dialog once it has opened: during its slide-up the panel is scaled to 0.985, and its 44 px buttons
      // measure 43.3 px (#7189). Under reduced motion the animation is 1 ms, but its first frame is still drawn.
      await dialog.evaluate(el => Promise.all(el.getAnimations({ subtree: true }).map(animation => animation.finished)));
      assert.equal(writes.length, 0);
      assert((await dialog.textContent()).includes("/code/example"));
      assert(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth), "Dialog has no horizontal overflow");
      assert(await dialog.locator("button").filter({ hasText: new RegExp(`${common.cancel}|${s.confirm_revoke}`) }).evaluateAll(buttons => buttons.every(button => {
        const box = button.getBoundingClientRect(), range = document.createRange(); range.selectNodeContents(button);
        return box.width >= 44 && box.height >= 44 && box.x >= 0 && box.right <= innerWidth && range.getBoundingClientRect().width <= button.clientWidth;
      })), "Confirmation controls fit with 44px targets");
      await page.screenshot({ path: join(output, `standing-authorizations-confirm-${suffix}.png`) });
      await dialog.getByRole("button", { name: common.cancel, exact: true }).click();
      await dialog.waitFor({ state: "detached" }); assert.equal(writes.length, 0);
      await group.getByRole("button", { name: s.revoke, exact: true }).first().click();
      await dialog.getByRole("button", { name: s.confirm_revoke, exact: true }).click();
      await dialog.getByRole("alert").waitFor(); assert.equal(await group.locator("article").count(), 2);
      await dialog.getByRole("button", { name: s.confirm_revoke, exact: true }).click();
      await dialog.waitFor({ state: "detached" }); assert.equal(await group.locator("article").count(), 1);
      assert.equal(writes.length, 2);
      grants = []; await group.getByRole("button", { name: s.refresh, exact: true }).click(); await group.getByText(s.empty, { exact: true }).waitFor();
      failRead = true; await group.getByRole("button", { name: s.refresh, exact: true }).click(); await group.getByRole("alert").waitFor();
      assert.equal(await group.getByText(s.empty, { exact: true }).count(), 0);
      assert.deepEqual(errors, []);
      console.log(`PASS ${theme} ${lang} ${width}px ${large ? "200% text" : "default text"}: fields, confirmation, cancel, DELETE 503/retry, empty, read 403, no overflow or page errors`);
      await context.close();
    }
  }
  console.log("PASS standing authorizations browser verification (synthetic API; no live hub)");
} finally { clearTimeout(deadline); await browser.close(); }
