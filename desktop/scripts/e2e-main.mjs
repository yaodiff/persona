import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { app, BrowserWindow, ipcMain, nativeTheme, Notification, powerMonitor, shell, Tray } from "electron";

/**
 * Development-only end-to-end acceptance smoke for the desktop shell (#6770).
 *
 * This file is Electron's entry point instead of dist/main.js: it puts observers on the main-process boundary, then
 * loads the unmodified production main module and drives the real window. It is never packaged (electron-builder
 * ships dist/ only), refuses to run packaged or without its explicit switch, exposes nothing to the hub renderer and
 * adds no IPC channel: the renderer keeps exactly the production preload.
 *
 * What is replaced, and why a headless runner needs it:
 * - `Notification.prototype.show` / `close` record the request instead of asking the OS notification center, which a
 *   headless runner cannot show or click. Everything up to that call is production code (web → preload → IPC →
 *   DesktopNotifications), and a click is simulated by emitting the notification's own `click` event.
 * - `Notification.isSupported` and `powerMonitor.getSystemIdleTime` answer "supported" and "just used": a runner has
 *   no notification service and nobody at the keyboard. Lock and unlock are the power monitor's own events, emitted.
 * - `shell.openExternal` records the address instead of starting a browser.
 */
const output = process.env.PERSONA_DESKTOP_E2E ? resolve(process.env.PERSONA_DESKTOP_E2E) : "";
const scenario = process.env.PERSONA_DESKTOP_E2E_SCENARIO ?? "";
const hubURL = process.env.PERSONA_HUB_URL ?? "";
const otherHubURL = process.env.PERSONA_DESKTOP_E2E_OTHER_HUB_URL ?? "";
const mock = process.env.PERSONA_DESKTOP_E2E_MOCK ?? "";
const otherMock = process.env.PERSONA_DESKTOP_E2E_OTHER_MOCK ?? "";
const token = process.env.PERSONA_DESKTOP_E2E_TOKEN ?? ""; // a temporary pairing code, as `personad hub pair` makes
const otherToken = process.env.PERSONA_DESKTOP_E2E_OTHER_TOKEN ?? "";
let card; // a pending permission or question card of the synthetic hub, chosen by the caller
try { card = JSON.parse(process.env.PERSONA_DESKTOP_E2E_CARD ?? "null"); } catch { card = undefined; }
const local = value => /^http:\/\/127\.0\.0\.1:\d+\//.test(value);
const otherHub = otherHubURL ? new URL(otherHubURL) : undefined;
const headless = process.platform === "linux" && !process.env.DISPLAY && !process.env.WAYLAND_DISPLAY;
if (app.isPackaged || !output || !process.argv.includes("--desktop-e2e") || !["root", "subpath", "hub-change", "hub-prefix-parent"].includes(scenario) || !local(hubURL) || !local(`${mock}/`) || !token || ((scenario === "hub-change" || scenario === "hub-prefix-parent") && (!otherHub || !otherToken || !local(`${otherMock}/`)))) {
  console.error("desktop e2e: refused (development-only; needs --desktop-e2e, an output directory and a local synthetic hub)");
  process.exit(2);
}

const hub = new URL(hubURL);
const prefix = hub.pathname.replace(/\/$/, ""); // "" at the root; unchanged for the whole scenario
const DEVICE = "Persona desktop";
const shown = []; // notifications that reached Notification.show()
const closed = [];
const external = []; // addresses handed to the system browser
const notifyRequests = []; // what the renderer asked for over the production IPC
const badges = []; // every count the renderer sent
const localSettings = []; // the shell-owned options window, observed without exposing it to the hub page
app.on("browser-window-created", (_event, window) => {
  const record = url => {
    if (url.endsWith("/setup.html") && !localSettings.includes(window)) {
      localSettings.push(window);
      // Chromium's headless platform gives new native windows a 1x1 off-screen frame. Exercise the same request the
      // person made by making that test window visible; on native runners production show()/focus() already did this.
      if (headless) {
        window.setBounds({ x: 0, y: 0, width: 720, height: 640 });
        window.show();
      }
    }
    if (headless && !url.endsWith("/setup.html") && !url.startsWith("devtools:") && window.getBounds().width < 600) {
      // A replacement hub window has the same headless sizing issue as the first one.
      window.setBounds({ x: 0, y: 0, width: 1280, height: 840 });
      window.show();
      window.focus();
    }
  };
  window.webContents.on("did-start-navigation", (_event, url) => record(url));
  record(window.webContents.getURL());
});
Notification.isSupported = () => true;
Notification.prototype.show = function show() { shown.push({ title: this.title, body: this.body, notification: this }); };
Notification.prototype.close = function close() { closed.push(this.title); };
powerMonitor.getSystemIdleTime = () => 0;
shell.openExternal = async url => { external.push(String(url)); };
// Linux is not a shipping platform, and GTK cannot build the tray's menu without a display server: on a headless
// build machine the tray keeps its icon and click, and its menu is left out. macOS and Windows run it unchanged.
if (headless) Tray.prototype.setContextMenu = () => {};

const steps = [];
const shots = [];
let win;
const sleep = ms => new Promise(done => setTimeout(done, ms));
const page = code => win.webContents.executeJavaScript(code, true);

/** Polls a real condition (DOM, main-process state or the synthetic hub); never a fixed wait. */
async function until(label, probe, timeout = 30_000) {
  const deadline = Date.now() + timeout;
  let last;
  for (;;) {
    try { last = await probe(); if (last) return last; } catch (error) { last = String(error); }
    if (Date.now() > deadline) throw new Error(`${label}: not reached in ${timeout} ms (last: ${JSON.stringify(last)?.slice(0, 300)})`);
    await sleep(100);
  }
}

async function step(name, run) {
  const started = Date.now();
  try {
    const detail = await run();
    steps.push({ name, ok: true, ms: Date.now() - started, ...(detail ? { detail } : {}) });
  } catch (error) {
    steps.push({ name, ok: false, ms: Date.now() - started, error: error instanceof Error ? error.message : String(error) });
    throw error;
  }
}

/** The synthetic hub's own control endpoints (/mock/…), which take no credential; this process never holds a device token. */
async function hubGet(path, address = mock) {
  const response = await fetch(`${address}${path}`, { signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error(`${path.split("?")[0]} answered ${response.status}`);
  return response.json();
}

/** The settled frame of the screen: fonts in, no entrance transition still running. */
async function shot(name) {
  await until(`${name} settled`, () => page("document.fonts.status === 'loaded' && document.getAnimations().every(a => a.playState !== 'running' || a.effect?.getComputedTiming().iterations === Infinity)"), 15_000);
  const image = await win.webContents.capturePage();
  const size = image.getSize();
  if (size.width < 600 || size.height < 400) throw new Error(`${name}: the captured page is ${size.width}x${size.height}`);
  await writeFile(join(output, `${name}.png`), image.toPNG());
  // What the page held when it was captured, so a verdict can be checked against the picture.
  const dom = await page("({title: document.title, heading: document.querySelector('header h1, main h1, h1')?.textContent?.trim().slice(0, 80) ?? '', testids: [...new Set([...document.querySelectorAll('[data-testid]')].map(e => e.dataset.testid))].slice(0, 40), links: document.querySelectorAll('a[href]').length, buttons: document.querySelectorAll('button').length})");
  shots.push({ name: `${name}.png`, ...size, ...dom });
}

/** In-app navigation as the router sees a link: the address changes, the page does not reload. */
const go = route => page(`history.pushState({}, "", ${JSON.stringify(prefix + route)}); window.dispatchEvent(new PopStateEvent("popstate")); true`);
const text = () => page("document.body.innerText");
const path = () => page("location.pathname");
/** The Needs-you count the web shows: the "(n)" its tab title starts with (lib/attention.ts tabTitle). */
const webCount = () => page("Number(/^\\((\\d+)\\)/.exec(document.title)?.[1] ?? 0)");
const lastBadge = () => badges[badges.length - 1];
let activeMock = mock;
const desktops = () => hubGet("/mock/desktops", activeMock);
const signedIn = () => page("!!window.personaDesktop && !location.search.includes('token') && !document.querySelector('input[type=password]') && !!document.querySelector('nav a[href], aside a[href]')");

async function signIn() {
  await step("signed out: the hub's own sign-in page", async () => {
    await until("sign-in page", () => page("!!document.querySelector('input[type=\"password\"]') || document.body.innerText.includes('Let this browser in from a device')"));
    if (!(await path()).startsWith(`${prefix}/`)) throw new Error(`the sign-in page is at ${await path()}`);
    await shot("01-sign-in");
  });
  await step("sign-in through the pairing deep link", async () => {
    // The production path of persona://pair: second-instance → navigateFromDeepLink → the hub's own sign-in exchange.
    app.emit("second-instance", {}, [`persona://pair?url=${encodeURIComponent(hub.href)}&token=${encodeURIComponent(token)}`]);
    await until("signed-in page", signedIn);
    const bridge = await page("({version: window.personaDesktop.version, platform: window.personaDesktop.platform, deviceName: window.personaDesktop.deviceName, keys: Object.keys(window.personaDesktop).sort()})");
    if (bridge.version !== 1 || bridge.deviceName !== DEVICE) throw new Error(`unexpected bridge ${JSON.stringify(bridge)}`);
    if (await page("typeof require !== 'undefined' || typeof process !== 'undefined'")) throw new Error("the hub page can reach Node");
    return { bridge: bridge.keys, platform: bridge.platform };
  });
}

async function screen(name, route, ready, file) {
  await step(name, async () => {
    await go(route);
    await until(`${name} DOM`, async () => (await path()) === prefix + route.split("?")[0] && page(ready));
    if (file) await shot(file);
    return { path: await path() };
  });
}

/** The switch in Settings › Notifications is this page's own storage flag (lib/notify.ts); set it the way the page reads it. */
async function enableNotifications() {
  await step("register_desktop on the authenticated socket", async () => {
    await page("localStorage.setItem('persona.systemNotify', '1'); window.dispatchEvent(new StorageEvent('storage', {key: 'persona.systemNotify'})); true");
    const state = await until("registration at the hub", async () => { const s = await desktops(); return s.registered.includes(DEVICE) && s; });
    if (await page("window.personaDesktop.notificationPermission()") !== "granted") throw new Error("the bridge does not report notifications as granted");
    return { registered: state.registered };
  });
}

async function lockWithdraws(expected) {
  await step(`lock withdraws the registration (${expected})`, async () => {
    const before = (await desktops()).log.length;
    powerMonitor.emit("lock-screen");
    const state = await until("withdrawal at the hub", async () => { const s = await desktops(); return s.registered.length === 0 && s; });
    const events = state.log.slice(before).map(entry => entry.event);
    if (!events.includes(expected)) throw new Error(`withdrawn by ${JSON.stringify(events)}, expected ${expected}`);
    const presence = await page("window.personaDesktop.desktopPresence()");
    if (presence.active || presence.notifies) throw new Error(`presence while locked: ${JSON.stringify(presence)}`);
    return { events };
  });
  await step("unlock registers again", async () => {
    powerMonitor.emit("unlock-screen");
    const state = await until("registration after unlock", async () => { const s = await desktops(); return s.registered.includes(DEVICE) && s; });
    return { events: state.log.map(entry => entry.event) };
  });
}

async function chatAndAgents() {
  // The conversation the hub holds is on the page (a message of the person's and a reply) and the composer can send.
  const chat = "!!document.querySelector('main [data-chat-content] [data-user-bubble]') && !!document.querySelector('main [data-chat-content] [data-answer]') && !!document.querySelector('[data-testid=\"composer-send\"]')";
  await step("main chat (Persona), light", async () => {
    nativeTheme.themeSource = "light";
    await go("/");
    await until("chat DOM", async () => (await path()) === `${prefix}/` && page(chat));
    await until("light theme", () => page("!matchMedia('(prefers-color-scheme: dark)').matches"));
    await shot("02-chat-light");
  });
  await step("main chat (Persona), dark", async () => {
    nativeTheme.themeSource = "dark";
    await until("dark theme", () => page("matchMedia('(prefers-color-scheme: dark)').matches"));
    await shot("03-chat-dark");
    nativeTheme.themeSource = "light";
    await until("light theme", () => page("!matchMedia('(prefers-color-scheme: dark)').matches"));
  });
  // The hub's agents are listed in the page itself (not only the sidebar), each row a link to its session.
  await screen("Agents list", "/agents", "!!document.querySelector('main [data-tree-branch-row]') && document.querySelectorAll('main a[href*=\"/s/\"], main a[href*=\"/t/\"]').length > 0", "04-agents");
}

async function rootScenario() {
  await signIn();
  await enableNotifications();
  await chatAndAgents();
  await step("Agent session with a waiting card", async () => {
    if (!card?.session_id) throw new Error("the synthetic hub has no pending permission or question card");
    const route = `/s/${card.machine_id}/${card.session_id}`;
    await go(route);
    // The router may show the session inside its terminal tab's address; the session id stays the last segment.
    const there = async () => (await path()).startsWith(`${prefix}/`) && (await path()).endsWith(`/${card.session_id}`);
    await until("session card DOM", async () => await there() && page(`!!document.querySelector('main [data-card=${JSON.stringify(card.id)}] button') && !!document.querySelector('main [data-card-actions]')`));
    await shot("05-session-card");
    return { kind: card.kind, path: await path() };
  });
  await screen("Inbox", "/inbox", "document.querySelectorAll('main [data-inbox-list-scroll] [data-item]').length > 0", "06-inbox");
  await screen("To-dos", "/todos", "document.querySelectorAll('main [data-testid=\"todo-title\"]').length > 0 && !!document.querySelector('main [data-testid=\"todos-detail-column\"]')", "07-todos");
  await step("signed-in desktop options keep the hub session and Link separate", async () => {
    await go("/settings");
    await until("desktop options entry", () => page("!!document.querySelector('[data-settings-group=\"desktop-options\"] button')"));
    const before = localSettings.length;
    const entry = "document.querySelector('[data-settings-group=\"desktop-options\"]')";
    await page(`${entry}.scrollIntoView({block: 'center'}); true`);
    await until("settings entry in the captured viewport", () => page(`(() => { const r = ${entry}.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight; })()`));
    await shot("08-settings-light");
    nativeTheme.themeSource = "dark";
    await page("[...document.querySelectorAll('button')].find(b => b.getAttribute('aria-pressed') !== null && b.textContent.trim() === 'Dark').click(); true");
    await until("the app's Dark choice is selected", () => page("document.documentElement.classList.contains('dark') && [...document.querySelectorAll('button')].some(b => b.getAttribute('aria-pressed') === 'true' && b.textContent.trim() === 'Dark')"));
    await shot("09-settings-dark");
    nativeTheme.themeSource = "light";
    await page("[...document.querySelectorAll('button')].find(b => b.getAttribute('aria-pressed') !== null && b.textContent.trim() === 'Light').click(); true");
    await until("the app's Light choice is selected", () => page("!document.documentElement.classList.contains('dark') && [...document.querySelectorAll('button')].some(b => b.getAttribute('aria-pressed') === 'true' && b.textContent.trim() === 'Light')"));
    await page("document.querySelector('[data-settings-group=\"desktop-options\"] button').click(); true");
    const options = await until("local options window", () => localSettings[before]);
    options.show();
    await until("visible settled local options", async () => options.isVisible() && options.getBounds().width >= 600 && await options.webContents.executeJavaScript("document.fonts.status === 'loaded' && (document.querySelector('details').open = true)"));
    await until("absent-Link explanation", () => options.webContents.executeJavaScript("document.querySelector('#link-state').textContent.includes('not installed') && !!document.querySelector('#link-install-guide') && !document.querySelector('#link-install-guide').hidden"));
    const image = await options.webContents.capturePage();
    if (image.getSize().width < 600 || image.getSize().height < 400) throw new Error(`local options capture ${JSON.stringify(image.getSize())}`);
    await writeFile(join(output, "10-local-options.png"), image.toPNG());
    const boundary = await options.webContents.executeJavaScript("({node: typeof require !== 'undefined', hub: !!window.personaDesktop, commands: Object.keys(window.desktopSetup).sort()})");
    if (boundary.node || boundary.hub) throw new Error(`local window boundary ${JSON.stringify(boundary)}`);
    options.close();
    await until("hub window stays signed in", async () => !win.isDestroyed() && await signedIn() && (await path()) === `${prefix}/settings`);
    const login = await page("window.personaDesktop.getLaunchAtLogin()");
    if (typeof login.enabled !== "boolean") throw new Error(`the hub window lost its login-item IPC: ${JSON.stringify(login)}`);
    const setLogin = await page(`window.personaDesktop.setLaunchAtLogin(${JSON.stringify(login.enabled)})`);
    if (typeof setLogin.enabled !== "boolean") throw new Error(`the hub window lost its login-item setter: ${JSON.stringify(setLogin)}`);
    return { localWindows: localSettings.length, boundary, hubPath: await path() };
  });

  let target;

  await step("window closed to the tray", async () => {
    await go("/");
    await until("chat again", async () => (await path()) === `${prefix}/`);
    await until("badge matches the web", async () => lastBadge() !== undefined && lastBadge() === await webCount());
    win.close();
    await until("hidden window", () => !win.isDestroyed() && !win.isVisible());
    return { visible: win.isVisible(), destroyed: win.isDestroyed(), badge: lastBadge() };
  });
  await step("a new Needs-you item reaches the main process as a notification", async () => {
    const before = { badge: lastBadge(), notify: notifyRequests.length, shown: shown.length };
    const bump = await hubGet("/mock/bump?what=screen", activeMock);
    const route = `${prefix}/s/${bump.key}`;
    const request = await until("notification request", () => notifyRequests.slice(before.notify).find(n => n.route === route));
    if (!request.title || !request.tag.includes(bump.key)) throw new Error(`unexpected request ${JSON.stringify(request)}`);
    target = await until("Notification.show", () => shown.slice(before.shown).find(n => n.title === request.title));
    await until("badge after the new item", async () => lastBadge() === before.badge + 1 && lastBadge() === await webCount());
    const dock = process.platform === "darwin" ? app.getBadgeCount() : undefined;
    if (dock !== undefined && dock !== lastBadge()) throw new Error(`the Dock badge is ${dock}, the web shows ${lastBadge()}`);
    if (win.isVisible()) throw new Error("the window came forward by itself");
    target.session = bump.key.split("/")[1];
    return { title: request.title, body: request.body, tag: request.tag, route: request.route, badgeBefore: before.badge, badge: lastBadge(), webCount: await webCount(), ...(dock !== undefined ? { dockBadge: dock } : {}) };
  });
  await step("clicking the notification shows the window on that session", async () => {
    target.notification.emit("click");
    await until("visible window", () => win.isVisible());
    // The page opens the route the notification carried; its router may then show the session in its tab's address.
    await until("session route", async () => (await path()).startsWith(`${prefix}/`) && (await path()).endsWith(`/${target.session}`));
    await shot("08-after-notification-click");
    return { visible: win.isVisible(), path: await path() };
  });
  await lockWithdraws("unregister");
}

async function hubChangeScenario() {
  await signIn();
  await step("changing the hub replaces its window, session and IPC authorization together", async () => {
    if (!otherHub || !otherToken) throw new Error("the second synthetic hub is missing");
    await page("window.personaDesktop.setLaunchAtLogin(true)");
    // Linux has no login-item service; the production getter truthfully reports false there. The round trip must
    // work on the shipping platforms, while the hub-change assertion here is about IPC surviving, not a Linux autostart.
    const replacedLogin = await page("window.personaDesktop.getLaunchAtLogin()");
    if (typeof replacedLogin.enabled !== "boolean" || (process.platform !== "linux" && !replacedLogin.enabled)) throw new Error(`the login setting did not round-trip before the hub change: ${JSON.stringify(replacedLogin)}`);
    await go("/settings");
    await until("desktop options entry for hub change", () => page("!!document.querySelector('[data-settings-group=\"desktop-options\"] button')"));
    await page("document.querySelector('[data-settings-group=\"desktop-options\"] button').click(); true");
    const options = await until("local options for hub change", () => localSettings.at(-1));
    await until("local hub form", async () => !options.isDestroyed() && options.webContents.executeJavaScript("document.querySelector('#hub').value.length > 0 && !!document.querySelector('#connect button')"));
    const previousSession = win.webContents.session.storagePath;
    const saved = options.webContents.executeJavaScript(`(() => { const form = document.querySelector('#connect'); document.querySelector('#hub').value = ${JSON.stringify(otherHub.href)}; form.requestSubmit(); return true; })()`);
    if (!await saved) throw new Error("the local Continue form did not submit");
    const replacement = await until("a new hub window", () => BrowserWindow.getAllWindows().find(candidate => candidate !== win && candidate !== options && !candidate.isDestroyed() && candidate.webContents.getURL().startsWith(otherHub.href)), 45_000);
    if (!win.isDestroyed()) throw new Error("the old hub window is still alive after the hub changed");
    win = replacement;
    activeMock = otherMock;
    const targetURL = win.webContents.getURL();
    const targetSession = win.webContents.session.storagePath;
    if (targetURL !== otherHub.href || targetSession === previousSession) throw new Error(`wrong replacement window or session: ${JSON.stringify({ targetURL, expected: otherHub.href, previousSession, targetSession })}`);
    if (!win.webContents.getURL().startsWith(otherHub.href)) throw new Error("the replacement still shows the previous hub");
    await until("second hub window is shown", () => !win.isDestroyed() && win.isVisible() && !win.webContents.isLoading());
    await until("second hub sign-in page", () => page("!!document.querySelector('input[type=password]') || document.body.innerText.includes('Let this browser in from a device')"));
    // This call returning either platform value proves the new window's production IPC is reachable. macOS may
    // report Notification.isSupported() false before sign-in; the notify() assertion below is the authorization check.
    const permission = await page("window.personaDesktop.notificationPermission()");
    if (permission !== "granted" && permission !== "denied") throw new Error(`the second hub's permission IPC is not reachable: ${permission}`);
    const loginItemState = await page("window.personaDesktop.getLaunchAtLogin().then(state => ({ok: true, state}), error => ({ok: false, error: String(error)}))");
    if (!loginItemState.ok || typeof loginItemState.state?.enabled !== "boolean") throw new Error(`the second hub lost its public login-item IPC: ${JSON.stringify(loginItemState)}`);
    const loginItemSet = await page(`window.personaDesktop.setLaunchAtLogin(${JSON.stringify(loginItemState.state.enabled)}).then(state => ({ok: true, state}), error => ({ok: false, error: String(error)}))`);
    if (!loginItemSet.ok || typeof loginItemSet.state?.enabled !== "boolean") throw new Error(`the second hub lost its public login-item setter: ${JSON.stringify(loginItemSet)}`);
    // A development Electron bundle may decline to persist a login item on macOS. The observable required here is
    // the surviving authorized get/set IPC and its truthful readback, not an unproven operating-system autostart.
    await until("second hub login item is still available", () => page(`window.personaDesktop.getLaunchAtLogin().then(state => typeof state.enabled === 'boolean')`));
    await page("window.personaDesktop.setLaunchAtLogin(false)");
    const presence = await page("window.personaDesktop.desktopPresence()");
    const windowState = { visible: win.isVisible(), focused: win.isFocused(), enabled: win.isEnabled(), loading: win.webContents.isLoading(), url: win.webContents.getURL() };
    if (!presence.active) throw new Error(`the second hub's bridge has stale presence: ${JSON.stringify({presence, permission, loginItemState, windowState})}`);
    app.emit("second-instance", {}, [`persona://pair?url=${encodeURIComponent(otherHub.href)}&token=${encodeURIComponent(otherToken)}`]);
    await until("second hub signed in", signedIn);
    if (!win.webContents.session.storagePath) throw new Error("the replacement window has no persisted origin-isolated session");
    await go("/");
    await until("second hub chat", () => page("!!document.querySelector('[data-testid=\"composer-send\"]')"));
    await enableNotifications();
    const original = await until("withdrawal at the first hub", async () => {
      const state = await hubGet("/mock/desktops", mock);
      return !state.registered.includes(DEVICE) && state;
    });
    const route = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000002";
    const accepted = await page(`window.personaDesktop.notify({title: 'Second hub notification', body: 'Bridge after the hub change', tag: 'second-hub-smoke', route: ${JSON.stringify(route)}})`);
    if (!accepted) throw new Error("the replacement hub's production notify IPC was refused");
    await until("second hub native notification", () => shown.find(n => n.title === "Second hub notification"));
    return { oldWindowDestroyed: true, permission, presence, nativeNotification: accepted, targetURL, sessionChanged: targetSession !== previousSession, loginItemState, previousHubWithdrawn: original.registered.length === 0 };
  });
}

async function subpathScenario() {
  if (!prefix) throw new Error("the sub-path scenario needs a prefixed hub address");
  await signIn();
  await chatAndAgents();
  const windows = () => BrowserWindow.getAllWindows().length;
  const click = (href, target) => page(`(() => { const a = document.createElement("a"); a.href = ${JSON.stringify(href)}; ${target ? 'a.target = "_blank"; a.rel = "noopener";' : ""} document.body.append(a); a.click(); a.remove(); return true; })()`);
  await step("an in-hub link in a new window stays in this window", async () => {
    const count = windows(), handed = external.length;
    await click(`${hub.origin}${prefix}/inbox`, true);
    await until("in-window navigation", async () => (await path()) === `${prefix}/inbox` && signedIn());
    if (windows() !== count || external.length !== handed) throw new Error("an in-hub link opened a window or the system browser");
    return { path: await path(), windows: windows() };
  });
  const refused = async (name, href, newWindow) => {
    await step(name, async () => {
      const count = windows(), handed = external.length, here = await page("location.href");
      await click(href, newWindow);
      await until("handed to the system browser", () => external.length === handed + 1);
      if (external[handed] !== href) throw new Error(`handed over ${external[handed]}`);
      // The refusal is synchronous in the main process; a page that had navigated would no longer answer here.
      if (windows() !== count || await page("location.href") !== here) throw new Error("the window left the hub");
      return { decision: "refused in the window, handed to the system browser", url: href };
    });
  };
  await refused("an off-hub link in a new window is refused", "https://example.com/elsewhere", true);
  await refused("an off-hub navigation of the window itself is refused", "https://example.com/takeover", false);
  await refused("the same origin outside the prefix is not the hub", `${hub.origin}/outside-the-prefix`, false);
  await enableNotifications();
  await lockWithdraws("close"); // a hub without desktop_unregister: only closing the socket withdraws (docs/desktop/desktop.md)
}

async function main() {
  await mkdir(output, { recursive: true });
  const created = new Promise(done => app.once("browser-window-created", (_event, window) => done(window)));
  await import(pathToFileURL(join(import.meta.dirname, "..", "dist", "main.js")).href);
  win = await created;
  win.webContents.on("console-message", event => { if (event.level === "error") consoleErrors.push(String(event.message).slice(0, 300)); });
  await until("first load", () => !win.webContents.isLoading() && win.webContents.getURL().startsWith(hub.href), 45_000);
  // installDesktopIPC clears its channels when it installs; these observers are added after it, beside the real handlers.
  ipcMain.on("desktop:notify", (_event, value) => { if (value && typeof value === "object") notifyRequests.push({ title: value.title, body: value.body, tag: value.tag, route: value.route }); });
  ipcMain.on("desktop:badge", (_event, value) => { badges.push(value); });
  // A headless Linux display gives every window 1x1; a real desktop keeps the size the shell restored.
  if (win.getBounds().width < 600) win.setBounds({ x: 0, y: 0, width: 1280, height: 840 });
  if (scenario === "root") await rootScenario();
  else if (scenario === "subpath") await subpathScenario();
  else await hubChangeScenario();
}

const consoleErrors = [];
const deadline = setTimeout(() => { void finish(new Error("the scenario did not finish in 150 s")); }, 150_000);
let finished = false;
async function finish(error) {
  if (finished) return;
  finished = true;
  clearTimeout(deadline);
  const ok = !error && steps.length > 0 && steps.every(s => s.ok);
  const result = {
    ok, scenario, platform: process.platform, electron: process.versions.electron, hub: prefix ? `prefix ${prefix}` : "root",
    ...(error ? { error: error instanceof Error ? error.message : String(error) } : {}),
    steps, screenshots: shots,
    boundary: { notifyRequests, shown: shown.map(n => ({ title: n.title, body: n.body })), closed: closed.length, badges, external },
    window: win && !win.isDestroyed() ? win.getBounds() : undefined,
    consoleErrors: consoleErrors.slice(0, 20),
  };
  try {
    if (!ok && win && !win.isDestroyed()) {
      result.failedAt = { url: win.webContents.getURL().split("?")[0], text: String(await text().catch(() => "")).slice(0, 1500) };
      const image = await win.webContents.capturePage().catch(() => undefined);
      if (image && image.getSize().width > 1) await writeFile(join(output, "failure.png"), image.toPNG());
    }
    await writeFile(join(output, "result.json"), `${JSON.stringify(result, null, 2)}\n`, "utf8");
  } catch (writeError) { console.error(writeError); }
  console.log(`desktop e2e ${scenario}: ${ok ? "PASS" : "FAIL"}${error ? ` (${result.error})` : ""}`);
  app.exit(ok ? 0 : 1);
}
main().then(() => finish(), error => finish(error));
