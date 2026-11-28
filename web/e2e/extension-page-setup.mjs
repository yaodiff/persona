// node web/e2e/extension-page-setup.mjs <loopback web origin> <screenshot directory> [Chromium executable]
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, directory, executablePath] = process.argv.slice(2);
const { origin, hostname } = new URL(base);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(hostname), "use a local web server");
await mkdir(directory, { recursive: true });
const extension = "example-reading";
const pagePath = "/extension/" + extension + "/shelf";
const drawPath = "/api/v1/extensions/" + extension + "/-/ui/shelf";
const secretTitle = "Library access key";
const readyText = "Your reading list is ready.";

async function configureFromPage(browser, viewport, theme) {
  const context = await browser.newContext({ viewport, colorScheme: theme, locale: "en-US", reducedMotion: "reduce", serviceWorkers: "block" });
  await context.addInitScript((theme) => {
    localStorage.setItem("persona.token", "fixture-token");
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
  }, theme);

  let saves = 0;
  const installation = () => ({
    id: extension, name: "Reading List", version: "1.0.0", kind: "process", source: "installed", enabled: true,
    status: saves ? "running" : "needs_setup", needs_setup: saves ? [] : [secretTitle],
    config: {}, secrets: [{ name: "key", title: secretTitle, set: saves > 0 }],
  });
  const empty = { machines: [], sessions: [], agents: [], groups: [], items: [], interactions: [], todos: [], workflows: [],
    extensions: [], conversations: [], providers: [], channels: [], personas: [], count: 0, total: 0 };
  await context.route("**/*", async route => {
    const request = route.request();
    const endpoint = new URL(request.url());
    if (endpoint.origin !== origin) return route.abort();
    if (!endpoint.pathname.startsWith("/api/v1/")) return route.continue();
    const path = endpoint.pathname.replace("/api/v1/plugins", "/api/v1/extensions");
    const send = (json, status = 200) => route.fulfill({ status, json });
    if (path === drawPath) {
      assert.equal(request.method(), "POST");
      if (!saves) return send({ error: "Extension not found" }, 404);
      return send({ view: { title: "Reading List", sections: [{ items: [{ type: "text", text: readyText }] }] } });
    }
    if (path === "/api/v1/extensions/" + extension + "/-/secrets/key") {
      assert.equal(request.method(), "PUT");
      assert.deepEqual(request.postDataJSON(), { value: "fixture-library-key" });
      saves++;
      return send(installation());
    }
    if (path === "/api/v1/extensions/" + extension + "/-/") return send(installation());
    if (path === "/api/v1/extensions") return send({ extensions: [installation()] });
    if (path === "/api/v1/extensions/ui") return send({ slots: [] });
    if (path === "/api/v1/managers") return send({ default: "assistant", managers: [{ id: "assistant", name: "Persona", online: true, builtin: true }] });
    return send(empty);
  });
  await context.routeWebSocket("**/api/v1/ws**", socket => socket.onMessage(raw => {
    const message = JSON.parse(String(raw));
    if (message.type === "client_hello") {
      socket.send(JSON.stringify({ type: "hello", server_version: "fixture", hub_name: "Reading room", features: ["extensions", "managers"] }));
      socket.send(JSON.stringify({ type: "machines", machines: [] }));
      socket.send(JSON.stringify({ type: "sessions", sessions: [] }));
    } else if (message.type === "app_ping") {
      socket.send(JSON.stringify({ type: "pong" }));
    }
  }));

  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  page.on("console", message => {
    if (message.type() !== "error") return;
    const path = message.location().url.replace("/api/v1/plugins", "/api/v1/extensions");
    // The missing extension process returns 404 before the host shows setup instructions.
    if (path !== origin + drawPath || !message.text().includes("404")) errors.push(message.text());
  });
  try {
    await page.goto(origin + pagePath);
    const panel = page.locator("[data-extension-page-container]");
    await panel.getByRole("heading", { name: "Set up Reading List" }).waitFor();
    await panel.getByText(secretTitle, { exact: true }).waitFor();
    assert.equal(await panel.getByText("Extension didn't load", { exact: true }).count(), 0);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false);
    await page.screenshot({ path: join(directory, theme + "-setup.png"), animations: "disabled" });

    await panel.getByRole("button", { name: "Open extension settings" }).click();
    await page.locator('[data-extension-settings="' + extension + '"]').waitFor();
    assert.equal(new URL(page.url()).searchParams.get("extension"), extension + "@assistant");
    await page.locator("[data-extension-needs-setup]").click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel(secretTitle, { exact: true }).fill("fixture-library-key");
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    assert.equal(saves, 1);

    await page.goBack();
    await panel.getByText(readyText, { exact: true }).waitFor();
    assert.equal(new URL(page.url()).pathname, pagePath);
    assert.deepEqual(errors, []);
    console.log("PASS", theme, viewport.width + "x" + viewport.height);
  } finally {
    await context.close();
  }
}

const browser = await chromium.launch(executablePath ? { executablePath } : { channel: "chrome" });
try {
  await configureFromPage(browser, { width: 1280, height: 800 }, "light");
  await configureFromPage(browser, { width: 390, height: 844 }, "dark");
} finally {
  await browser.close();
}
