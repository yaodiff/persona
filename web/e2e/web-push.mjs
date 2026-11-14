// End-to-end check of Web Push (#7459, docs/web/web.md "Web Push") in a real Chrome, as the user does it: Settings ›
// Notifications › "Notify this browser when Persona is closed" subscribes the browser at its real push service (FCM)
// with the hub's key and hands the hub the subscription; Send Test; a push the service worker gets shows as the hub's
// message says, under the page's tag, opening the conversation; a pushed notification whose event is over goes once a
// page opens; turning it off unsubscribes. --live also has the Go sender (server/internal/webpush TestLiveDelivery)
// push one through FCM and waits for the service worker to show it.
//
// Needs the built web app (Vite's dev server registers no service worker), the fake hub with MOCK_WEB_PUSH=1, and
// Chrome (Edge with --channel msedge, any Chromium through --executable):
//
//   MOCK_WEB_PUSH=1 PORT=18799 python3 scripts/mock/mock_hub.py &
//   (cd web && npm run build && PERSONA_HUB_URL=http://127.0.0.1:18799 npx vite preview --host 127.0.0.1 --port 5799 --strictPort &)
//   node web/e2e/web-push.mjs http://127.0.0.1:5799 --hub http://127.0.0.1:18799 [--shots <dir>] [--live <server dir>]
//
// The browser runs in a profile of its own (a push subscription needs one: Chrome has none in an incognito context),
// in English, wide (1440×900) and at phone width (390×844), light and dark; --shots saves Settings off and on. Prints one
// line per check and a verdict; exits 1 on a failure and after six minutes. The page may reach only the two loopback
// servers; the browser itself talks to its push service, and --live's sender to FCM.
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
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
const live = flag("--live");
const executable = flag("--executable");
const channel = flag("--channel") ?? "chrome";
if (!base || !hub) {
  console.error("usage: node web/e2e/web-push.mjs <web URL> --hub <mock hub URL> [--shots <dir>] [--live <server dir>] [--channel msedge | --executable <chromium>]");
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
let cleanup = () => {};
function fail(msg) {
  console.log(`FAIL ${msg}`);
  cleanup();
  process.exit(1);
}

// RFC 8291 Appendix A's application server key pair, a published example: the mock hub's VAPID key (web_push_mock.py).
const VAPID = { private: "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw", public: "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" };
const hubGet = async (path) => (await fetch(`${hub}${path}`, { headers: { Authorization: "Bearer mock" }, signal: AbortSignal.timeout(5000) })).json();
const mockState = await hubGet("/mock/web_push").catch(() => undefined);
if (mockState?.key !== VAPID.public) fail("the fake hub does not answer Web Push with the example key: start it with MOCK_WEB_PUSH=1");
const pendingCard = ((await hubGet("/api/v1/interactions")).interactions ?? [])[0]?.id;

const profile = mkdtempSync(join(tmpdir(), "persona-web-push-"));
const ctx = await chromium.launchPersistentContext(profile, {
  ...(executable ? { executablePath: executable } : { channel }),
  headless: true,
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
  locale: "en-US",
  // Chrome's push service needs its background connection; the rest of Playwright's switches stay.
  ignoreDefaultArgs: ["--disable-background-networking", "--disable-component-update", "--disable-sync", "--disable-default-apps"],
});
cleanup = () => {
  void ctx.close().catch(() => {});
  rmSync(profile, { recursive: true, force: true });
};
await ctx.grantPermissions(["notifications"], { origin: new URL(base).origin });
await ctx.route("**/*", (route) => {
  const origin = new URL(route.request().url()).origin;
  return [new URL(base).origin, new URL(hub).origin].includes(origin) ? route.continue() : route.abort();
});
await ctx.addInitScript(() => localStorage.setItem("persona.language", "en"));
const page = ctx.pages()[0] ?? (await ctx.newPage());
page.setDefaultTimeout(20000);
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));

const theme = async (scheme) => {
  await page.emulateMedia({ colorScheme: scheme });
  await page.evaluate((s) => localStorage.setItem("persona.theme", s), scheme);
};
const shot = async (name) => {
  if (!shots) return;
  await page.waitForTimeout(400);
  const group = page.locator('[data-settings-group="web-push"]');
  if (await group.count()) await group.scrollIntoViewIfNeeded();
  await page.screenshot({ path: join(shots, `${name}.png`) });
};
const toggle = () => page.getByRole("switch", { name: "Notify this browser" });
const hint = (text) => page.getByText(text, { exact: false }).first().waitFor({ state: "visible" });
const notifications = () =>
  page.evaluate(async () =>
    (await (await navigator.serviceWorker.ready).getNotifications()).map((n) => ({ title: n.title, body: n.body, tag: n.tag, data: n.data, silent: n.silent })),
  );
async function waitFor(what, test, ms = 15000) {
  const until = Date.now() + ms;
  for (;;) {
    const got = await test();
    if (got) return got;
    if (Date.now() > until) fail(`waited ${ms / 1000} s for ${what}`);
    await page.waitForTimeout(250);
  }
}

await page.goto(`${base}/?token=mock`);
await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 20000 });
await theme("light");
await page.goto(`${base}/settings?tab=notifications`);
await page.waitForFunction(() => navigator.serviceWorker?.controller, null, { timeout: 20000 }).catch(() => page.reload());
await page.waitForFunction(() => navigator.serviceWorker?.controller, null, { timeout: 20000 }).catch(() => fail("the service worker never took the page (is this the built app, not Vite's dev server?)"));
await toggle().waitFor({ state: "visible" });
await hint("Cards, finished turns and alerts reach this browser even when no Persona page is open.");
if ((await toggle().getAttribute("aria-checked")) !== "false") fail("Web Push is on before it was turned on");
ok("Settings › Notifications offers Web Push, off");
await shot("wide-light-1-off");

// On: the browser subscribes at its push service with the hub's key, and the hub gets it.
await toggle().click();
await hint("On, through FCM.");
const device = await page.evaluate(() => localStorage.getItem("persona.notifyDevice"));
const sub = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).pushManager.getSubscription())?.toJSON());
const kept = (await hubGet("/mock/web_push")).subscriptions?.[device];
if (!sub?.endpoint?.startsWith("https://fcm.googleapis.com/")) fail(`the browser has no FCM subscription: ${JSON.stringify(sub)}`);
if (!kept || kept.endpoint !== sub.endpoint || kept.key !== VAPID.public || kept.locale !== "en" || !kept.features?.includes("elicitation") || !/^Chrome · /.test(kept.browser))
  fail(`the hub got ${JSON.stringify(kept)} for ${device}`);
ok(`turning it on subscribes at FCM with the hub's key; the hub has it (${kept.browser}, en)`);

await page.getByRole("button", { name: "Send Test" }).click();
await page.getByText("Test sent. It should appear in a moment.").waitFor({ state: "visible" });
await hint("Last delivered");
ok("Send Test says it went, and the row says when it was last delivered");
await shot("wide-light-2-on");

// A push, as the browser hands it to the worker: shown with the hub's words, under the page's tag, opening the agent.
const cdp = await ctx.newCDPSession(page);
const registration = new Promise((resolve) =>
  cdp.on("ServiceWorker.workerRegistrationUpdated", ({ registrations }) => {
    const r = registrations.find((x) => !x.isDeleted && x.scopeURL.startsWith(base));
    if (r) resolve(r.registrationId);
  }),
);
await cdp.send("ServiceWorker.enable");
const registrationId = await Promise.race([registration, new Promise((r) => setTimeout(() => r(undefined), 10000))]);
if (!registrationId) fail("DevTools names no service worker registration for the page");
const deliver = (msg) => cdp.send("ServiceWorker.deliverPushMessage", { origin: new URL(base).origin, registrationId, data: JSON.stringify(msg) });
await deliver({ v: 1, kind: "interaction", title: "Needs your permission", subtitle: "Fix the login · Laptop", body: "Bash: npm test", machine_id: "laptop", session_id: "s-e2e", interaction_id: "ix-e2e" });
const card = await waitFor("the pushed card's notification", async () => (await notifications()).find((n) => n.tag === "laptop/s-e2e#needs"));
if (card.title !== "Needs your permission" || card.body !== "Fix the login · Laptop\nBash: npm test" || !card.data?.pushed || !card.data.url.endsWith("/s/laptop/s-e2e"))
  fail(`the card's notification ${JSON.stringify(card)}`);
await deliver({ v: 1, kind: "done", title: "Fix the login", subtitle: "Laptop", body: "Done", machine_id: "laptop", session_id: "s-quiet", quiet: true });
const quiet = await waitFor("the quiet finished turn", async () => (await notifications()).find((n) => n.tag === "laptop/s-quiet#finished"));
if (!quiet.silent) fail("a push inside quiet hours rang");
ok("a pushed card and a finished turn show with the hub's words, the page's tags, the agent's link, quiet hours silent");

// Opening a page takes back what is over: an agent this hub has no "Needs you" for, a finished turn nobody has to see,
// a card no longer pending; a card still pending stays.
if (pendingCard) await deliver({ v: 1, kind: "interaction", title: "Claude has a question", interaction_id: pendingCard });
await deliver({ v: 1, kind: "interaction", title: "Claude has a question", interaction_id: "ix-answered-elsewhere" });
await waitFor("the two card notifications", async () => (await notifications()).filter((n) => n.tag?.startsWith("card:")).length === (pendingCard ? 2 : 1));
await page.reload();
await toggle().waitFor({ state: "visible" });
const left = await waitFor("the notifications that are over to go", async () => {
  const tags = (await notifications()).map((n) => n.tag);
  return !tags.includes("laptop/s-e2e#needs") && !tags.includes("laptop/s-quiet#finished") && !tags.includes("card:ix-answered-elsewhere#needs") ? tags : undefined;
});
if (pendingCard && !left.includes(`card:${pendingCard}#needs`)) fail(`a pending card's notification went: ${left}`);
ok(`opening a page takes back the pushed notifications whose event is over${pendingCard ? ", and keeps a pending card's" : ""}`);

if (live) {
  // FCM now and then answers 410 for a subscription a fresh headless profile made moments ago: then the browser
  // subscribes again (off and on) and the sender tries once more.
  const send = async (s) => {
    const file = join(profile, "live.json");
    writeFileSync(file, JSON.stringify({ vapid: VAPID, subscription: { endpoint: s.endpoint, p256dh: s.keys.p256dh, auth: s.keys.auth },
      message: JSON.stringify({ v: 1, kind: "alert", id: "live-e2e", title: "Persona", body: "Delivered by FCM from the Go sender" }) }));
    try {
      return execFileSync("go", ["test", "./internal/webpush/", "-run", "TestLiveDelivery", "-count=1", "-v"], { cwd: live, env: { ...process.env, PERSONA_WEBPUSH_LIVE: file }, encoding: "utf8" });
    } catch (e) {
      return String(e.stdout ?? e);
    }
  };
  let out = await send(sub);
  if (/answered 410/.test(out)) {
    console.log("    FCM said the new subscription is gone (410); subscribing again");
    await toggle().click();
    await hint("Cards, finished turns and alerts reach this browser even when no Persona page is open.");
    await toggle().click();
    await hint("On, through FCM.");
    const again = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).pushManager.getSubscription())?.toJSON());
    out = await send(again);
  }
  if (!/took it: 201/.test(out)) fail(`the Go sender: ${out}`);
  const got = await waitFor("FCM to deliver the Go sender's push", async () => (await notifications()).find((n) => n.tag === "alert:live-e2e"), 120000);
  if (got.body !== "Delivered by FCM from the Go sender") fail(`the live push ${JSON.stringify(got)}`);
  ok("--live: FCM took the Go sender's push (201) and the service worker showed it");
}

await page.setViewportSize({ width: 390, height: 844 });
await theme("dark");
await page.reload();
await hint("On, through FCM.");
await shot("phone-dark-1-on");
await page.setViewportSize({ width: 1440, height: 900 });
await page.reload();
await hint("On, through FCM.");
await shot("wide-dark-1-on");

// Off: the hub forgets it, and the browser unsubscribes.
await toggle().click();
await hint("Cards, finished turns and alerts reach this browser even when no Persona page is open.");
const after = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).pushManager.getSubscription())?.endpoint ?? null);
if (after !== null || (await hubGet("/mock/web_push")).subscriptions?.[device]) fail(`turned off, the browser has ${after}, the hub ${JSON.stringify((await hubGet("/mock/web_push")).subscriptions?.[device])}`);
ok("turning it off unsubscribes the browser and the hub forgets it");

if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
cleanup();
console.log(`PASS web push: every check passed${live ? " (with a live push through FCM)" : ""}`);
process.exit(0);
