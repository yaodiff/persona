import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { AdminWindow, isHubAdmin, keepNavigation, openNewWindowLink } from "../src/hub-links.js";
import type { AdminWindowLike } from "../src/hub-links.js";
import { parseHubAddress } from "../src/urls.js";

const source = (name: string) => readFile(join(import.meta.dirname, "..", "..", "src", name), "utf8");
const hub = parseHubAddress("https://hub.example");

/** A window as Electron's, recording what was asked of it. */
class FakeWindow implements AdminWindowLike {
  readonly loads: string[] = [];
  shows = 0;
  focuses = 0;
  destroyed = false;
  visible = false;
  url = "";
  fail: Error | undefined;
  #closed: (() => void) | undefined;
  readonly webContents = { getURL: () => this.url };
  isDestroyed() { return this.destroyed; }
  isVisible() { return this.visible; }
  show() { this.shows++; this.visible = true; }
  focus() { this.focuses++; }
  destroy() { this.destroyed = true; this.#closed?.(); }
  once(_event: "closed", listener: () => void) { this.#closed = listener; return this; }
  loadURL(url: string) {
    this.loads.push(url);
    if (this.fail) return Promise.reject(this.fail);
    this.url = url;
    return Promise.resolve();
  }
}

/** An Assistant window's links, as main.ts wires them: Hub Admin to its own window, the rest as before. */
function assistant() {
  const created: FakeWindow[] = [];
  const admin = new AdminWindow(() => { const w = new FakeWindow(); created.push(w); return w; });
  const here: string[] = [], browser: string[] = [];
  const targets = { admin: (url: string) => { admin.open(url); }, browser: (url: string) => { browser.push(url); } };
  return {
    created, admin, here, browser,
    open: (url: string) => openNewWindowLink(hub, url, { ...targets, here: page => { here.push(page); } }),
    navigate: (url: string) => keepNavigation(hub, url, targets),
  };
}

const settle = () => new Promise(done => setImmediate(done));

test("Hub Admin is the hub's own /admin pages, in any letter case, and nothing else", () => {
  for (const url of ["https://hub.example/admin", "https://hub.example/admin/", "https://hub.example/admin/machines?x=1", "https://hub.example/ADMIN/usage"]) {
    assert.equal(isHubAdmin(hub, url), true, url);
  }
  for (const url of ["https://hub.example/", "https://hub.example/administrator", "https://hub.example/api/v1/admin/state", "https://hub.example/settings?tab=admin", "https://other.example/admin", "http://hub.example/admin", "not a url"]) {
    assert.equal(isHubAdmin(hub, url), false, url);
  }
  const prefixed = parseHubAddress("https://hub.example/persona");
  assert.equal(isHubAdmin(prefixed, "https://hub.example/persona/admin/machines"), true);
  assert.equal(isHubAdmin(prefixed, "https://hub.example/admin"), false, "outside the prefix is not the hub");
});

test("a same-hub admin link opens a separate admin window and leaves the Assistant window's page alone", async () => {
  const a = assistant();
  a.open("https://hub.example/admin/machines");
  assert.equal(a.created.length, 1);
  assert.deepEqual(a.created[0]!.loads, ["https://hub.example/admin/machines"]);
  assert.deepEqual(a.here, [], "the Assistant window is not navigated");
  assert.deepEqual(a.browser, []);
  await settle();
  assert.equal(a.created[0]!.destroyed, false);
});

test("a second admin link focuses the open admin window, on the link's page when it is another one", () => {
  const a = assistant();
  a.open("https://hub.example/admin");
  const first = a.created[0]!;
  first.visible = true; // ready-to-show
  a.open("https://hub.example/admin");
  assert.equal(a.created.length, 1, "no second admin window");
  assert.deepEqual(first.loads, ["https://hub.example/admin"], "the same page is not reloaded");
  assert.equal(first.focuses, 1);
  a.open("https://hub.example/admin/usage");
  assert.equal(a.created.length, 1);
  assert.deepEqual(first.loads, ["https://hub.example/admin", "https://hub.example/admin/usage"]);
  assert.equal(first.focuses, 2);
  assert.deepEqual(a.here, []);
});

test("an admin window the person closed is opened again; closing the Assistant window closes it", () => {
  const a = assistant();
  a.open("https://hub.example/admin");
  a.created[0]!.destroy();
  assert.equal(a.admin.window, undefined);
  a.open("https://hub.example/admin");
  assert.equal(a.created.length, 2);
  a.admin.close();
  assert.equal(a.created[1]!.destroyed, true);
  assert.equal(a.admin.window, undefined);
});

test("an admin window whose first load failed is not left hidden, but a replaced load keeps it", async () => {
  const failing = new AdminWindow(() => Object.assign(new FakeWindow(), { fail: new Error("ERR_CONNECTION_REFUSED") }));
  const broken = failing.open("https://hub.example/admin");
  await settle();
  assert.equal(broken.destroyed, true);
  assert.equal(failing.window, undefined);
  const replaced = new AdminWindow(() => Object.assign(new FakeWindow(), { fail: Object.assign(new Error("ERR_ABORTED (-3)"), { code: "ERR_ABORTED" }) }));
  const kept = replaced.open("https://hub.example/admin");
  await settle();
  assert.equal(kept.destroyed, false);
  assert.equal(replaced.window, kept);
});

test("other same-hub links and off-hub links keep today's behaviour", () => {
  const a = assistant();
  a.open("https://hub.example/inbox");
  a.open("https://hub.example/settings?tab=admin");
  assert.deepEqual(a.here, ["https://hub.example/inbox", "https://hub.example/settings?tab=admin"], "a same-hub page loads in this window");
  a.open("https://example.com/elsewhere");
  a.open("https://hub.example.evil.test/admin");
  assert.deepEqual(a.browser, ["https://example.com/elsewhere", "https://hub.example.evil.test/admin"], "off-hub links go to the system browser");
  a.open("file:///etc/hosts");
  a.open("javascript:alert(1)");
  assert.equal(a.here.length, 2);
  assert.equal(a.browser.length, 2, "anything else opens nowhere");
  assert.equal(a.created.length, 0, "no admin window for any of them");
});

test("the Assistant window's own navigation to Hub Admin goes to the admin window; other navigation is unchanged", () => {
  const a = assistant();
  assert.equal(a.navigate("https://hub.example/admin/usage"), false, "the Assistant window keeps its page");
  assert.deepEqual(a.created[0]!.loads, ["https://hub.example/admin/usage"]);
  assert.equal(a.navigate("https://hub.example/inbox"), true);
  assert.equal(a.navigate("https://example.com/takeover"), false);
  assert.deepEqual(a.browser, ["https://example.com/takeover"]);
  assert.equal(a.created.length, 1);
});

test("in the admin window itself, admin links and navigation stay in it", () => {
  const here: string[] = [], browser: string[] = [];
  const targets = { browser: (url: string) => { browser.push(url); } };
  openNewWindowLink(hub, "https://hub.example/admin/machines", { ...targets, here: url => { here.push(url); } });
  assert.deepEqual(here, ["https://hub.example/admin/machines"]);
  assert.equal(keepNavigation(hub, "https://hub.example/admin/usage", targets), true);
  assert.equal(keepNavigation(hub, "https://example.com/", targets), false);
  assert.deepEqual(browser, ["https://example.com/"]);
  assert.equal(keepNavigation(undefined, "https://hub.example/admin", targets), false, "no hub: nothing is a hub page");
});

test("main.ts gives only the Assistant window an admin window, sharing its hub session without a preload", async () => {
  const main = await source("main.ts");
  const showMain = main.slice(main.indexOf("async function showMain"), main.indexOf("async function showSetup"));
  assert.match(showMain, /const admin = new AdminWindow\(\(\) => createAdminWindow\(owner\)\);\s*secureHubWindow\(current, url => \{ admin\.open\(url\); \}\);/);
  assert.match(showMain, /current\.once\("closed", \(\) => \{[^\n]*admin\.close\(\);/, "the admin window closes with its Assistant window");
  const create = main.slice(main.indexOf("function createAdminWindow"), main.indexOf("// A different hub needs a new window"));
  assert.ok(create.includes("secureHubWindow(admin);"), "the admin window keeps the hub link rules, without an admin target of its own");
  assert.ok(create.includes("partition: hubPartition(owner)"), "the admin window uses the hub's session (sign-in, TLS pin)");
  assert.ok(!create.includes("preload"), "the admin page gets no desktop bridge");
  assert.ok(!create.includes("titleBarStyle"), "the admin window keeps the standard title bar");
  const secure = main.slice(main.indexOf("function secureHubWindow"), main.indexOf("/** Hub Admin's window"));
  assert.ok(secure.includes("openNewWindowLink(hub, url,"));
  assert.equal(secure.match(/keepNavigation\(hub, url, targets\)/g)?.length, 2, "will-navigate and will-redirect");
});
