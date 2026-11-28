// Real-app manual compaction regression with synthetic HTTP/WS only; never contacts a production hub.
// node web/e2e/assistant-compaction.mjs <loopback web URL> <screenshots> [Chromium executable]
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, shots, executable = "/usr/bin/chromium"] = process.argv.slice(2);
if (!base || !shots || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?\/?$/.test(base)) {
  console.error("usage: node web/e2e/assistant-compaction.mjs <loopback web URL> <screenshots> [Chromium executable]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => { console.error("TIMEOUT Assistant compaction verification (180s)"); process.exit(124); }, 180_000);
const AT = "2026-11-01T10:00:00Z";

async function verify(browser, kind, theme, width = 1280, zoom = 1) {
  const sid = `example-${kind}`;
  const assistant = kind !== "agent";
  const supported = kind !== "unsupported";
  let compaction;
  let busy = false;
  let socket;
  const calls = [];
  const session = () => ({
    machine_id: "example", session_id: sid, title: "Example conversation", agent: "claude",
    cwd: "/tmp/example-project", assistant, started_by_persona: assistant,
    state: busy ? "working" : "idle", status: busy ? "working" : "idle", running: true,
    updated_at: AT, compaction, capabilities: { read: true, send: true, interrupt: true },
  });
  // A smaller CSS viewport at a larger device scale exercises the reflow of 200% browser zoom without CSS-zoom hit-testing artifacts.
  const context = await browser.newContext({
    viewport: { width: Math.round(width / zoom), height: Math.round(900 / zoom) }, deviceScaleFactor: zoom,
    colorScheme: theme, reducedMotion: "reduce",
  });
  await context.addInitScript(({ theme }) => {
    localStorage.setItem("persona.token", "synthetic-example-token");
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
  }, { theme });
  await context.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api\/v1/, "");
    let body = {};
    let status = 200;
    if (path.endsWith("/compact")) {
      assert.equal(request.method(), "POST");
      assert.equal(path, `/assistant/conversations/${sid}/compact`);
      assert.deepEqual(request.postDataJSON(), { machine_id: "example" });
      calls.push(path);
      status = busy ? 409 : 200;
      body = busy ? { code: "busy", error: "Wait for the current reply to finish." } : { ok: true };
    } else {
      assert(["GET", "HEAD"].includes(request.method()) || /\/(seen|read|today)$/.test(path), `unexpected write: ${path}`);
      if (path === "/machines") body = { machines: [{ id: "example", name: "Example computer", online: true, os: "linux" }] };
      else if (path === "/sessions") body = { sessions: [session()] };
      else if (path === "/interactions") body = { interactions: [] };
      else if (path === "/assistant") body = {
        machine_id: "example", main: kind === "main" ? sid : "example-main",
        leaders: kind === "direction" ? [sid] : [], background_supervisor: kind === "supervisor" ? sid : undefined,
        conversations: assistant ? [session()] : [], tasks: [], memory: [],
      };
      else if (path === `/sessions/example/${sid}`) body = { session: session() };
      else if (path.endsWith("/items")) body = { items: [
        { id: "example-1", seq: 1, ts: AT, role: "user", kind: "text", text: "Continue the example plan." },
        { id: "example-2", seq: 2, ts: AT, role: "assistant", kind: "text", text: "The example conversation is ready to continue." },
      ], has_more: false };
      else if (path.endsWith("/feed")) body = { items: [] };
      else if (path === "/todos") body = { todos: [] };
      else if (path === "/extensions/ui") body = { slots: [] };
      else if (path === "/extensions") body = { extensions: [] };
      else if (path === "/managers") body = { managers: [] };
    }
    await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  });
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    socket = ws;
    ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub",
      features: supported ? ["assistant_compact", "assistant_main_clear", "assistant_main_new"] : [] }));
    ws.onMessage((raw) => { if (JSON.parse(String(raw)).type === "app_ping") ws.send(JSON.stringify({ type: "pong" })); });
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(String(error)));
  page.setDefaultTimeout(10_000);
  const path = kind === "main" ? "/" : assistant ? `/persona/${sid}` : `/s/example/${sid}`;
  await page.goto(base.replace(/\/$/, "") + path);
  await page.getByText("The example conversation is ready to continue.", { exact: true }).waitFor();
  const menu = async () => {
    await page.getByRole("button", { name: "More actions", exact: true }).filter({ visible: true }).first().click();
    await page.getByRole("menu").waitFor();
    if (!assistant) await page.getByRole("menuitem", { name: "More", exact: true }).click();
  };
  const shot = (state) => page.screenshot({ path: join(shots, `${kind}-${width}-${zoom}x-${theme}-${state}.png`), animations: "disabled" });
  await menu();
  const compact = page.getByRole("menuitem", { name: "Compact context", exact: true });
  if (assistant && supported) {
    await compact.waitFor();
    await shot("menu");
  }
  assert.equal(await compact.count(), assistant && supported ? 1 : 0);
  assert.equal(await page.getByRole("menuitem", { name: "Clear context", exact: true }).count(), kind === "main" ? 1 : 0);
  assert.equal(await page.getByRole("menuitem", { name: "New main chat…", exact: true }).count(), kind === "main" ? 1 : 0);
  if (kind === "direction") {
    await compact.click();
    await page.getByRole("status").filter({ hasText: "Compacting context…" }).waitFor();
    compaction = { state: "running", started_at: AT };
    socket.send(JSON.stringify({ type: "sessions", machine_id: "example", sessions: [session()] }));
    await menu();
    assert.equal(await page.getByRole("menuitem", { name: "Compacting context…", exact: true }).isDisabled(), true);
    await shot("running");
    await page.keyboard.press("Escape");
    compaction = { ...compaction, state: "done", finished_at: new Date().toISOString() };
    socket.send(JSON.stringify({ type: "sessions", machine_id: "example", sessions: [session()] }));
    await page.getByRole("status").filter({ hasText: "Context compacted" }).waitFor();
    await shot("done");
    busy = true;
    socket.send(JSON.stringify({ type: "sessions", machine_id: "example", sessions: [session()] }));
    await menu();
    await page.getByRole("menuitem", { name: "Compact context", exact: true }).click();
    await page.getByText("Couldn't start compacting: Wait for the current reply to finish.", { exact: true }).waitFor();
    assert.equal(calls.length, 2);
  } else {
    await page.keyboard.press("Escape");
  }
  assert.deepEqual(errors, [], "no browser exceptions");
  console.log(`PASS ${kind} ${theme} ${width}px ${zoom}x: menu eligibility, main-only clear/new, selected-session lifecycle/refusal`);
  await context.close();
}

let browser;
try {
  browser = await chromium.launch({ executablePath: executable, args: ["--no-sandbox"] });
  for (const theme of ["light", "dark"]) {
    for (const kind of ["main", "direction", "side", "supervisor", "agent", "unsupported"]) await verify(browser, kind, theme);
    await verify(browser, "direction", theme, 390);
    await verify(browser, "direction", theme, 1280, 2);
  }
  console.log("PASS Assistant compaction: 16 synthetic real-app scenarios; no production writes");
} catch (error) {
  console.error("FAIL Assistant compaction verification", error);
  process.exitCode = 1;
} finally {
  await browser?.close();
  clearTimeout(watchdog);
}
