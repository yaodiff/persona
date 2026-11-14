import assert from "node:assert/strict";
import test from "node:test";
import { readFile, readdir } from "node:fs/promises";
import { join } from "node:path";
import {
  CHECK_INTERVAL_MS, INITIAL_UPDATE_STATE, UPDATE_STRING_KEYS, UpdateController, reduceUpdate, updateMenu, updatesSupported,
} from "../src/update-state.js";
import type { UpdateEvent, UpdateState } from "../src/update-state.js";
import { trayTemplate } from "../src/tray-menu.js";
import { desktopLocale, loadStrings } from "../src/strings.js";

const locales = join(import.meta.dirname, "..", "..", "src", "locales");
const run = (events: UpdateEvent[], from: UpdateState = INITIAL_UPDATE_STATE): UpdateState => events.reduce(reduceUpdate, from);

/** A hand-driven clock and updater: no real timer, network or Electron. */
function harness(interval = CHECK_INTERVAL_MS) {
  const timers: { run: () => void; at: number; id: number }[] = [];
  const pending: { resolve: () => void; reject: (error: Error) => void }[] = [];
  const states: UpdateState[] = [];
  let now = 0;
  let nextTimer = 0;
  let installs = 0;
  const controller = new UpdateController({
    check: () => new Promise<void>((resolve, reject) => { pending.push({ resolve, reject }); }),
    install: () => { installs += 1; },
    setTimer: (callback, milliseconds) => { const id = nextTimer++; timers.push({ run: callback, at: now + milliseconds, id }); return id; },
    clearTimer: handle => { const index = timers.findIndex(timer => timer.id === handle); if (index >= 0) timers.splice(index, 1); },
    changed: state => { states.push(state); },
  }, interval);
  return {
    controller, states, timers,
    checks: () => pending.length,
    installs: () => installs,
    /** Lets promise callbacks run. */
    settle: async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); },
    advance: (milliseconds: number) => {
      now += milliseconds;
      for (;;) {
        const index = timers.findIndex(timer => timer.at <= now);
        if (index < 0) return;
        timers.splice(index, 1)[0]?.run();
      }
    },
    resolve: (index: number) => pending[index]?.resolve(),
    reject: (index: number) => pending[index]?.reject(new Error("offline")),
  };
}

test("a scheduled check that finds nothing or fails leaves no trace", () => {
  assert.deepEqual(run([{ type: "check", manual: false }]), { phase: "checking", manual: false });
  assert.deepEqual(run([{ type: "check", manual: false }, { type: "current" }]), { phase: "idle", manual: false });
  assert.deepEqual(run([{ type: "check", manual: false }, { type: "failed" }]), { phase: "idle", manual: false });
  assert.deepEqual(run([{ type: "check", manual: false }, { type: "available", version: "1.2.0" }, { type: "failed" }]), { phase: "idle", manual: false });
});

test("a check the person asked for keeps its outcome visible until the next check", () => {
  assert.deepEqual(run([{ type: "check", manual: true }, { type: "current" }]), { phase: "idle", manual: false, notice: "current" });
  assert.deepEqual(run([{ type: "check", manual: true }, { type: "failed" }]), { phase: "idle", manual: false, notice: "failed" });
  // Asking during a scheduled check adopts that check instead of starting a second one.
  assert.deepEqual(run([{ type: "check", manual: false }, { type: "check", manual: true }, { type: "failed" }]), { phase: "idle", manual: false, notice: "failed" });
  // The next check, scheduled or not, replaces a stale outcome.
  assert.deepEqual(run([{ type: "check", manual: true }, { type: "current" }, { type: "check", manual: false }, { type: "current" }]), { phase: "idle", manual: false });
});

test("a downloaded update stays offered whatever happens afterwards", () => {
  const ready = run([{ type: "check", manual: false }, { type: "available", version: "1.2.0" }, { type: "downloaded", version: "1.2.0" }]);
  assert.deepEqual(ready, { phase: "ready", version: "1.2.0", manual: false });
  for (const event of [{ type: "failed" }, { type: "current" }, { type: "check", manual: true }, { type: "available", version: "1.3.0" }] satisfies UpdateEvent[]) {
    assert.equal(reduceUpdate(ready, event), ready);
  }
});

test("duplicate and out-of-order events do not change state", () => {
  assert.equal(reduceUpdate(INITIAL_UPDATE_STATE, { type: "failed" }), INITIAL_UPDATE_STATE);
  assert.equal(reduceUpdate(INITIAL_UPDATE_STATE, { type: "current" }), INITIAL_UPDATE_STATE);
  const downloading = run([{ type: "check", manual: false }, { type: "available", version: "1.2.0" }]);
  assert.equal(reduceUpdate(downloading, { type: "current" }), downloading);
  assert.equal(reduceUpdate(downloading, { type: "check", manual: false }), downloading);
});

test("the tray shows one action and at most one status line, in every language", async () => {
  for (const locale of ["en", "zh-Hans", "ja"] as const) {
    const strings = await loadStrings(locales, locale);
    const labels = (state: UpdateState) => updateMenu(state, strings);
    assert.deepEqual(labels(INITIAL_UPDATE_STATE).map(item => item.action), ["check"]);
    assert.deepEqual(labels({ phase: "checking", manual: true }).map(item => item.action), [undefined]);
    assert.deepEqual(labels({ phase: "downloading", version: "1.2.0", manual: false }).map(item => item.action), [undefined]);
    assert.deepEqual(labels({ phase: "ready", version: "1.2.0", manual: false }).map(item => item.action), ["restart"]);
    assert.deepEqual(labels({ phase: "idle", manual: false, notice: "current" }).map(item => item.action), [undefined, "check"]);
    assert.deepEqual(labels({ phase: "idle", manual: false, notice: "failed" }).map(item => item.action), [undefined, "check"]);
    // The version is named wherever one exists, and no placeholder or raw key ever reaches the menu.
    assert.match(labels({ phase: "ready", version: "1.2.0", manual: false })[0]?.label ?? "", /1\.2\.0/);
    assert.match(labels({ phase: "downloading", version: "1.2.0", manual: false })[0]?.label ?? "", /1\.2\.0/);
    const everything = [INITIAL_UPDATE_STATE, { phase: "checking", manual: false }, { phase: "ready", version: "1.2.0", manual: false }, { phase: "idle", manual: false, notice: "failed" }, { phase: "idle", manual: false, notice: "current" }] satisfies UpdateState[];
    for (const item of everything.flatMap(labels)) assert.doesNotMatch(item.label, /\{|update_|^$/);
  }
  assert.equal((await loadStrings(locales, "en")).update_restart, "Restart to update to {version}");
});

test("every language has the same keys, including the tray and update strings", async () => {
  const read = async (name: string) => Object.keys(JSON.parse(await readFile(join(locales, `${name}.json`), "utf8")) as Record<string, string>).sort();
  const english = await read("en");
  assert.deepEqual(await read("zh-Hans"), english);
  assert.deepEqual(await read("ja"), english);
  for (const key of [...UPDATE_STRING_KEYS, "tray_open", "tray_quit"]) assert.ok(english.includes(key), key);
  assert.equal(desktopLocale("zh-CN"), "zh-Hans");
  assert.equal(desktopLocale("ja-JP"), "ja");
  assert.equal(desktopLocale("fr"), "en");
});

test("the tray menu keeps Open and Quit and adds the update section between them", () => {
  const calls: string[] = [];
  const actions = { open: () => { calls.push("open"); }, quit: () => { calls.push("quit"); }, update: (action: string) => { calls.push(action); } };
  const labels = { open: "Open Persona", quit: "Quit Persona" };
  assert.deepEqual(trayTemplate(labels, [], actions).map(item => item.label ?? item.type), ["Open Persona", "separator", "Quit Persona"]);
  const menu = trayTemplate(labels, [{ label: "Persona is up to date" }, { label: "Check for updates", action: "check" }], actions);
  assert.deepEqual(menu.map(item => item.label ?? item.type), ["Open Persona", "separator", "Persona is up to date", "Check for updates", "separator", "Quit Persona"]);
  assert.equal(menu[2]?.enabled, false);
  assert.equal(menu[2]?.click, undefined);
  (menu[3]?.click as (() => void) | undefined)?.();
  assert.deepEqual(calls, ["check"]);
});

test("updates exist only in the packaged macOS and Windows apps", () => {
  assert.equal(updatesSupported(true, "darwin", false), true);
  assert.equal(updatesSupported(true, "win32", false), true);
  assert.equal(updatesSupported(true, "linux", false), false);
  assert.equal(updatesSupported(false, "darwin", false), false);
  assert.equal(updatesSupported(true, "win32", true), false);
});

test("the controller checks at launch and then on a fixed four-hour interval", async () => {
  const h = harness();
  assert.equal(CHECK_INTERVAL_MS, 4 * 60 * 60 * 1000);
  h.controller.start();
  h.controller.start();
  await h.settle();
  assert.equal(h.checks(), 1);
  assert.deepEqual(h.timers.map(timer => timer.at), [CHECK_INTERVAL_MS]);
  h.controller.dispatch({ type: "current" });
  h.resolve(0);
  await h.settle();
  h.advance(CHECK_INTERVAL_MS - 1);
  await h.settle();
  assert.equal(h.checks(), 1);
  h.advance(1);
  await h.settle();
  assert.equal(h.checks(), 2);
  assert.deepEqual(h.timers.map(timer => timer.at), [2 * CHECK_INTERVAL_MS]);
});

test("a failed check is quiet and the next scheduled check retries", async () => {
  const h = harness();
  h.controller.start();
  await h.settle();
  h.reject(0);
  await h.settle();
  assert.deepEqual(h.controller.state, { phase: "idle", manual: false });
  h.advance(CHECK_INTERVAL_MS);
  await h.settle();
  assert.equal(h.checks(), 2);
  // A download that fails part-way is retried the same way.
  h.controller.dispatch({ type: "available", version: "1.2.0" });
  h.controller.dispatch({ type: "failed" });
  h.reject(1);
  await h.settle();
  assert.deepEqual(h.controller.state, { phase: "idle", manual: false });
  h.advance(CHECK_INTERVAL_MS);
  await h.settle();
  assert.equal(h.checks(), 3);
});

test("asking shows Checking at once, never runs two checks, and reports the outcome", async () => {
  const h = harness();
  h.controller.start();
  await h.settle();
  h.controller.dispatch({ type: "current" });
  h.resolve(0);
  await h.settle();
  h.controller.check();
  // Feedback is synchronous: the state changed before any promise or network work ran.
  assert.deepEqual(h.controller.state, { phase: "checking", manual: true });
  h.controller.check();
  await h.settle();
  assert.equal(h.checks(), 2);
  h.reject(1);
  await h.settle();
  assert.deepEqual(h.controller.state, { phase: "idle", manual: false, notice: "failed" });
  // A manual check does not move the schedule.
  assert.deepEqual(h.timers.map(timer => timer.at), [CHECK_INTERVAL_MS]);
});

test("a check that settles without an answer does not leave the menu stuck on Checking", async () => {
  const h = harness();
  h.controller.check();
  await h.settle();
  h.resolve(0);
  await h.settle();
  assert.deepEqual(h.controller.state, { phase: "idle", manual: false, notice: "failed" });
});

test("only the person's restart installs, and a waiting update stops further checks", async () => {
  const h = harness();
  h.controller.start();
  await h.settle();
  h.controller.restart();
  assert.equal(h.installs(), 0);
  h.controller.dispatch({ type: "available", version: "1.2.0" });
  h.controller.restart();
  assert.equal(h.installs(), 0);
  h.controller.dispatch({ type: "downloaded", version: "1.2.0" });
  h.resolve(0);
  await h.settle();
  assert.deepEqual(h.timers, []);
  h.advance(10 * CHECK_INTERVAL_MS);
  h.controller.check();
  await h.settle();
  assert.equal(h.checks(), 1);
  assert.equal(h.installs(), 0);
  h.controller.restart();
  assert.equal(h.installs(), 1);
  assert.deepEqual(h.states.at(-1), { phase: "ready", version: "1.2.0", manual: false });
});

test("stopping cancels the schedule", async () => {
  const h = harness();
  h.controller.start();
  await h.settle();
  h.controller.stop();
  assert.deepEqual(h.timers, []);
});

test("no run-time switch can point a packaged app at another update feed", async () => {
  const source = join(import.meta.dirname, "..", "..", "src");
  for (const name of (await readdir(source)).filter(file => /\.c?ts$/.test(file))) {
    const text = await readFile(join(source, name), "utf8");
    // The feed is whatever app-update.yml was packaged; these are the electron-updater APIs that would override it.
    assert.doesNotMatch(text, /setFeedURL|forceDevUpdateConfig|updateConfigPath|\.channel\s*=/, name);
  }
  const updater = await readFile(join(source, "updater.ts"), "utf8");
  assert.deepEqual([...updater.matchAll(/process\.env\.(\w+)/g)].map(match => match[1]), ["PERSONA_DESKTOP_DISABLE_UPDATE"]);
  // The release configuration publishes to GitHub Releases and never packages the test-only entry.
  const release = await readFile(join(source, "..", "electron-builder.yml"), "utf8");
  assert.match(release, /provider: github/);
  assert.doesNotMatch(release, /update-test|generic/);
  const testOnly = await readFile(join(source, "..", "electron-builder.update-test.yml"), "utf8");
  // Windows checkouts may carry CRLF line endings.
  assert.match(testOnly, /appId: com\.example\.persona\.update-test\r?\n/);
  assert.match(testOnly, /provider: generic\r?\n {2}url: http:\/\/127\.0\.0\.1:9\/\r?\n/);
});
