// Loopback-only production preview; all accounts, messages, and approvals are synthetic.
// BASE_URL=http://127.0.0.1:51796 PERSONA_E2E_CHROMIUM=/usr/bin/chromium SHOTS_DIR=/tmp/outbound-shots node web/e2e/outbound-messages.mjs
import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:51796");
assert(base.protocol === "http:" && ["127.0.0.1", "localhost", "[::1]"].includes(base.hostname));
assert(!base.username && !base.password && base.pathname === "/");
const output = resolve(process.env.SHOTS_DIR || "/tmp/outbound-shots");
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ executablePath: process.env.PERSONA_E2E_CHROMIUM, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("TIMEOUT outward messages"); process.exit(2); }, 180_000);
const results = [];
try {
  for (const theme of ["light", "dark"]) {
    for (const [width, lang] of [[1440, "en"], [390, "zh-Hans"], [320, "ja"]]) {
      const strings = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/settings-outbound.json`, import.meta.url), "utf8"));
      const standing = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/settings-standing.json`, import.meta.url), "utf8"));
      const context = await browser.newContext({ viewport: { width, height: 1100 }, colorScheme: theme, reducedMotion: "reduce" });
      await context.addInitScript(({ theme, lang }) => {
        localStorage.setItem("persona.token", "synthetic-token");
        localStorage.setItem("persona.theme", theme);
        localStorage.setItem("persona.language", lang);
      }, { theme, lang });
      let grants = [], status, requested;
      const errors = [];
      await context.route("**/*", route => new URL(route.request().url()).origin === base.origin ? route.continue() : route.abort());
      await context.route("**/api/v1/**", async route => {
        const req = route.request(), path = new URL(req.url()).pathname.replace(/^\/api\/v1/, "");
        if (path === "/assistant/outbound-messages") {
          assert.equal(req.headers()["x-persona-manager"], "assistant");
          if (req.method() === "POST") {
            requested = req.postDataJSON();
            status = "pending";
            return route.fulfill({ status: 202, json: { approval_id: "ix_example", status } });
          }
          if (new URL(req.url()).searchParams.get("approval")) return route.fulfill({ json: { status } });
          return route.fulfill({ json: { identity: "bot", approval_id: status === "pending" ? "ix_example" : undefined } });
        }
        if (path.startsWith("/assistant/standing-authorizations")) {
          if (req.method() === "DELETE") { grants = []; return route.fulfill({ json: { ok: true, revision: 2 } }); }
          return route.fulfill({ json: { revision: grants.length, authorizations: grants } });
        }
        assert(["GET", "HEAD"].includes(req.method()), `Unexpected mutation: ${path}`);
        const replies = {
          "/assistant": { manager: "assistant", conversations: [], default_permission_mode: "default" },
          "/assistant/memory": { files: [] }, "/managers": { managers: [{ id: "assistant", label: "Persona" }] },
          "/machines": [], "/sessions": [], "/personas": { personas: [] }, "/assistant/personas": { personas: [] },
          "/extensions": { extensions: [] }, "/workspaces": { workspaces: [] },
        };
        return route.fulfill({ json: replies[path] ?? {} });
      });
      await context.routeWebSocket(/\/api\/v1\/ws(?:\?|$)/, ws => {
        ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", features: ["managers"], hub_name: "Example hub" }));
        ws.onMessage(message => { const event = JSON.parse(String(message)); if (event.type === "app_ping") ws.send(JSON.stringify({ type: "pong", id: event.id })); });
      });
      const page = await context.newPage();
      page.setDefaultTimeout(15000);
      page.on("pageerror", error => errors.push(error.message));
      await page.goto(new URL("/settings?tab=persona", base).href);
      const group = page.locator('[data-settings-group="outbound-messages"]');
      await group.getByLabel(strings.identity).waitFor();
      await group.scrollIntoViewIfNeeded();
      assert.equal(await group.getByLabel(strings.identity).inputValue(), "bot");
      assert(await group.getByRole("button", { name: strings.request, exact: true }).isDisabled());
      await group.getByLabel(strings.target, { exact: true }).fill("oc_example_group");
      await group.getByLabel(strings.scenario, { exact: true }).selectOption("assistant_message");
      await group.getByRole("button", { name: strings.request, exact: true }).click();
      await group.getByText(strings.pending, { exact: true }).waitFor();
      assert.deepEqual(requested, { revision: 0, scope: { channel: "larkcli", target: "oc_example_group", scenario: "assistant_message", identity: "bot" } });
      assert.equal(grants.length, 0, "No grant before synthetic owner approval");
      await page.reload();
      await group.getByText(strings.pending, { exact: true }).waitFor();
      await group.scrollIntoViewIfNeeded();
      await page.screenshot({ path: join(output, `${theme}-${lang}-${width}-pending.png`) });
      grants = [{
        id: "sa_example", machine_id: "", directory: "", actions: ["send"], conditions: [],
        quote: "Automatic messages in the example group.", source: "settings", granted_at: "2026-11-17T00:00:00Z",
        reversible: true, messages: requested.scope,
      }];
      status = "approved";
      const registry = page.locator('[data-settings-group="standing-authorizations"]');
      await registry.locator("article").waitFor();
      await registry.scrollIntoViewIfNeeded();
      for (const word of ["oc_example_group", strings.assistant_message, strings.bot]) assert((await registry.textContent()).includes(word));
      assert(await group.evaluate(el => el.scrollWidth <= el.clientWidth), "Settings group has no horizontal overflow");
      assert(await group.locator("button").evaluateAll(buttons => buttons.every(button => {
        const box = button.getBoundingClientRect();
        return box.height >= 44 && getComputedStyle(button).whiteSpace === "nowrap";
      })), "One-line controls retain touch targets");
      await page.screenshot({ path: join(output, `${theme}-${lang}-${width}-approved.png`) });
      await registry.getByRole("button", { name: standing.revoke, exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.waitFor();
      assert((await dialog.textContent()).includes("oc_example_group"));
      await dialog.getByRole("button", { name: standing.confirm_revoke, exact: true }).click();
      await registry.locator("article").waitFor({ state: "detached" });
      assert.deepEqual(errors, []);
      results.push({ theme, lang, width, approval: "pending-then-approved", revoked: true, pageErrors: errors.length });
      await context.close();
    }
  }
  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2) + "\n");
  console.log(`PASS outward messages: ${results.length} viewport/theme/language flows, synthetic approval and revoke, no external traffic`);
} finally {
  clearTimeout(deadline);
  await browser.close();
}
