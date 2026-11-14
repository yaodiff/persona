// node --test site/worker.test.mjs site/releases.test.mjs
import assert from "node:assert/strict";
import test from "node:test";
import { readFile, readdir } from "node:fs/promises";
import worker from "./worker.mjs";
import { RELEASES_API, FRESH_MS, classifyAsset, compareTags, slimReleases, summarizeNotes } from "./releases.mjs";
import {
  COMPONENTS, DEVICES, WEB_GUIDE, archLabel, areaLabel, componentsFor, countsLine, detectPlatform, deviceFor, devicesFor,
  downloadFor, formatAge, formatDate, formatSize, isDirectDownload, latestRelease, webGuideFor,
} from "./public/releases/model.js";
import { EN_COMPONENTS, EN_DEVICES, EN_WEB_GUIDE, PAGE_TEXT, pageText } from "./public/releases/text.js";

const DL = "https://github.com/nwverse/persona/releases/download/";
const asset = (tag, name, size = 1000, extra = {}) => ({ name, size, state: "uploaded", browser_download_url: `${DL}${tag}/${name}`, ...extra });
const release = (tag, extra = {}) => ({
  tag_name: tag, draft: false, prerelease: false, published_at: "2026-11-12T05:00:00Z",
  html_url: `https://github.com/nwverse/persona/releases/tag/${tag}`, body: "", assets: [], ...extra,
});
const full = (tag, v) => [
  asset(tag, `Persona-${v}-android.apk`, 21111552, { digest: "sha256:" + "a".repeat(64) }),
  asset(tag, `Persona-${v}-harmony.hap`, 32000000),
  asset(tag, `Persona-${v}-darwin-arm64.tar.gz`, 117300513),
  asset(tag, `Persona-${v}-darwin-amd64.tar.gz`),
  asset(tag, `Persona-${v}-linux-amd64.tar.gz`),
  asset(tag, `Persona-${v}-linux-arm64.tar.gz`),
  asset(tag, `Persona-${v}-windows-amd64.zip`),
  asset(tag, `Persona-${v}-windows-arm64.zip`),
  asset(tag, `Persona-${v}-web.tar.gz`),
  asset(tag, "personad-darwin-arm64.gz"),
  asset(tag, "SHA256SUMS"),
];

// ---------- metadata ----------

test("only published semver product releases are kept, newest first, latest by version", () => {
  const data = slimReleases([
    release("promo-v1", { assets: [asset("promo-v1", "persona-en.mp4")] }),
    release("v0.2.1", { published_at: "2026-11-11T08:00:00Z" }),
    release("v0.10.0", { published_at: "2026-11-10T08:00:00Z" }), // older date, higher version
    release("v0.3.0", { published_at: "2026-11-12T05:00:00Z" }),
    release("v0.4.0-rc.1", { published_at: "2026-11-13T05:00:00Z", prerelease: true }),
    release("v9.9.9", { draft: true }),
    release("v1.0.0", { html_url: "https://example.com/elsewhere" }),
    release("v0.3.0"), // duplicate
  ], new Date("2026-11-13T06:00:00Z"));
  assert.deepEqual(data.releases.map((r) => r.tag), ["v0.4.0-rc.1", "v0.3.0", "v0.2.1", "v0.10.0"]);
  assert.equal(data.latest, "v0.10.0");
  assert.equal(data.releases[0].prerelease, true);
  assert.equal(data.fetched_at, "2026-11-13T06:00:00.000Z");
  assert.equal(data.stale, false);
});

test("versions compare by semver, a prerelease before its release", () => {
  const sorted = ["v1.0.0", "v0.9.10", "v1.0.0-rc.2", "v0.9.9", "v1.0.0-rc.10", "v1.0.0-beta"].sort(compareTags);
  assert.deepEqual(sorted, ["v0.9.9", "v0.9.10", "v1.0.0-beta", "v1.0.0-rc.2", "v1.0.0-rc.10", "v1.0.0"]);
});

test("files are classified by name, with GitHub's checksum kept", () => {
  const data = slimReleases([release("v0.3.0", { assets: full("v0.3.0", "0.3.0") })]);
  const byName = Object.fromEntries(data.releases[0].assets.map((a) => [a.name, a]));
  assert.deepEqual(byName["Persona-0.3.0-android.apk"], {
    name: "Persona-0.3.0-android.apk", size: 21111552, url: `${DL}v0.3.0/Persona-0.3.0-android.apk`,
    kind: "app", platform: "android", sha256: "a".repeat(64),
  });
  assert.equal(byName["Persona-0.3.0-harmony.hap"].platform, "harmony");
  assert.equal(byName["Persona-0.3.0-harmony.hap"].kind, "app");
  assert.equal(byName["Persona-0.3.0-darwin-arm64.tar.gz"].arch, "arm64");
  assert.equal(byName["Persona-0.3.0-windows-amd64.zip"].kind, "computer");
  assert.equal(byName["Persona-0.3.0-web.tar.gz"].kind, "web");
  assert.deepEqual(classifyAsset("persona-node-linux-arm64.gz", "0.3.0"), { kind: "component", component: "persona-node", platform: "linux", arch: "arm64" });
  assert.equal(classifyAsset("SHA256SUMS", "0.3.0").kind, "checksum");
  assert.equal(classifyAsset("install.sh", "0.3.0").kind, "installer");
  assert.equal(classifyAsset("personad-wechat-linux-amd64.gz", "0.3.0").kind, "other");
  assert.equal(classifyAsset("Persona-0.2.0-android.apk", "0.3.0").kind, "other", "another version's file is not this one's app");
});

test("a file is dropped unless its address is exactly GitHub's download for this repository, tag and name", () => {
  const tag = "v0.3.0";
  const hostile = [
    { ...asset(tag, "a.apk"), browser_download_url: "https://evil.example/nwverse/persona/releases/download/v0.3.0/a.apk" },
    { ...asset(tag, "a.apk"), browser_download_url: `${DL}v0.2.0/a.apk` },
    { ...asset(tag, "b.apk"), browser_download_url: `${DL}v0.3.0/a.apk` },
    { ...asset(tag, "a.apk"), browser_download_url: `https://github.com/other/persona/releases/download/v0.3.0/a.apk` },
    { ...asset(tag, "a.apk"), browser_download_url: `${DL}v0.3.0/a.apk?x=1` },
    asset(tag, "../x.apk"),
    asset(tag, "<script>.apk"),
    asset(tag, "a.apk", -1),
    asset(tag, "a.apk", 1.5),
    asset(tag, "a.apk", 10, { state: "starter" }),
    null,
  ];
  const data = slimReleases([release(tag, { assets: [...hostile, asset(tag, "ok.apk")] })]);
  assert.deepEqual(data.releases[0].assets.map((a) => a.name), ["ok.apk"]);
});

test("the notes become counts and a few feature titles, without handles, links or PR numbers", () => {
  const body = [
    "## What's Changed",
    "* feat(android): a new board home by @someone in https://github.com/nwverse/persona/pull/1",
    "* feat(android): second android feature by @someone in https://github.com/nwverse/persona/pull/2",
    "* feat(web): select skills from the mention menu (#5473) by @x-y in https://github.com/nwverse/persona/pull/3",
    "* fix(ios): keep the list in place by @a in https://github.com/nwverse/persona/pull/4",
    "* fix: thanks @helper, see https://example.com/x by @a in https://github.com/nwverse/persona/pull/5",
    "* docs(site): words by @a in https://github.com/nwverse/persona/pull/6",
    "* plain line without a type by @dependabot[bot] in https://github.com/nwverse/persona/pull/7",
    "* feat(assistant): <b>bold</b> idea by @a in https://github.com/nwverse/persona/pull/8",
    "* feat(kb): one more by @a in https://github.com/nwverse/persona/pull/9",
    "* feat(tasks): and another by @a in https://github.com/nwverse/persona/pull/10",
    "## New Contributors",
    "* feat(never): counted by @new in https://github.com/nwverse/persona/pull/11",
    "**Full Changelog**: https://github.com/nwverse/persona/compare/v0.2.1...v0.3.0",
  ].join("\n");
  const s = summarizeNotes(body);
  assert.deepEqual(s.counts, { features: 6, fixes: 2, other: 2 });
  assert.deepEqual(s.highlights, [
    { scope: "android", title: "A new board home" },
    { scope: "web", title: "Select skills from the mention menu" },
    { scope: "assistant", title: "Bbold/b idea" },
    { scope: "kb", title: "One more" },
  ]);
  const text = JSON.stringify(s);
  assert.doesNotMatch(text, /@|https?:|#\d|</);
});

test("a release with no conventional features highlights its fixes; no notes give nothing", () => {
  assert.deepEqual(summarizeNotes("* fix(web): one\n* fix: two").highlights, [{ scope: "web", title: "One" }, { title: "Two" }]);
  assert.deepEqual(summarizeNotes(undefined), { counts: { features: 0, fixes: 0, other: 0 }, highlights: [] });
});

// ---------- the route ----------

const assets = { fetch: async (request) => new Response("asset " + new URL(request.url).pathname) };
const get = (path = "/releases/data.json", method = "GET") =>
  worker.fetch(new Request("https://site.example" + path, { method }), { ASSETS: assets }, { waitUntil() {} });

function fakeEdge(handler) {
  const store = new Map();
  const calls = [];
  const saved = { caches: globalThis.caches, fetch: globalThis.fetch };
  globalThis.caches = { default: {
    async match(key) { const r = store.get(String(key)); return r ? r.clone() : undefined; },
    async put(key, res) { store.set(String(key), res.clone()); },
  } };
  globalThis.fetch = async (url, init) => { calls.push({ url: String(url), init }); return handler(String(url), init); };
  return { calls, restore() { globalThis.caches = saved.caches; globalThis.fetch = saved.fetch; } };
}

test("/releases/data.json reads GitHub once while fresh and serves only metadata", async (t) => {
  const edge = fakeEdge(() => Response.json([release("v0.3.0", { assets: full("v0.3.0", "0.3.0"), body: "* feat(web): x by @a in https://x" })]));
  t.after(edge.restore);
  const first = await get();
  assert.equal(first.status, 200);
  assert.match(first.headers.get("cache-control"), /max-age=300/);
  const data = await first.json();
  assert.equal(data.latest, "v0.3.0");
  assert.equal(data.releases[0].body, undefined, "the notes themselves are not passed on");
  for (const a of data.releases[0].assets) assert.ok(a.url.startsWith(DL + "v0.3.0/"), a.url);
  await get();
  assert.equal(edge.calls.length, 1);
  assert.equal(edge.calls[0].url, RELEASES_API);
  assert.equal(edge.calls[0].init.headers["user-agent"], "persona-site");
});

test("a stale copy outlives GitHub, marked; with no copy the route answers 503", async (t) => {
  let up = true;
  const edge = fakeEdge(() => (up ? Response.json([release("v0.3.0")]) : new Response("down", { status: 502 })));
  t.after(edge.restore);
  const realNow = Date.now;
  t.after(() => { Date.now = realNow; });
  await get();
  up = false;
  Date.now = () => realNow() + FRESH_MS + 1000;
  const stale = await get();
  assert.equal(stale.status, 200);
  const data = await stale.json();
  assert.equal(data.stale, true);
  assert.equal(data.latest, "v0.3.0");
  assert.match(stale.headers.get("cache-control"), /max-age=60/);
  edge.restore();

  const empty = fakeEdge(() => { throw new Error("offline"); });
  t.after(empty.restore);
  const none = await get();
  assert.equal(none.status, 503);
  assert.equal(none.headers.get("cache-control"), "no-store");
  assert.equal((await none.json()).releases_page, "https://github.com/nwverse/persona/releases");
});

test("a new release replaces the old copy after it expires, without a site deployment", async (t) => {
  let list = [release("v0.3.0")];
  const edge = fakeEdge(() => Response.json(list));
  t.after(edge.restore);
  const realNow = Date.now;
  t.after(() => { Date.now = realNow; });
  assert.equal((await (await get()).json()).latest, "v0.3.0");
  list = [release("v0.4.0", { published_at: "2026-11-13T05:00:00Z" }), release("v0.3.0")];
  assert.equal((await (await get()).json()).latest, "v0.3.0", "still fresh");
  Date.now = () => realNow() + FRESH_MS + 1000;
  assert.equal((await (await get()).json()).latest, "v0.4.0");
});

test("the route is GET / HEAD only, and no package path is served by the Worker", async (t) => {
  const edge = fakeEdge(() => Response.json([release("v0.3.0")]));
  t.after(edge.restore);
  assert.equal((await get("/releases/data.json", "POST")).status, 405);
  const head = await get("/releases/data.json", "HEAD");
  assert.equal(head.status, 200);
  assert.equal(await head.text(), "");
  const before = edge.calls.length;
  // anything else under /releases/ is a static file, never a proxy to GitHub
  assert.equal(await (await get("/releases/download/v0.3.0/Persona-0.3.0-android.apk")).text(),
    "asset /releases/download/v0.3.0/Persona-0.3.0-android.apk");
  assert.equal(edge.calls.length, before);
  const wrangler = await readFile(new URL("./wrangler.jsonc", import.meta.url), "utf8");
  const first = /"run_worker_first":\s*\[([^\]]*)\]/.exec(wrangler)[1];
  // Localized 404s require every non-excluded path to reach the Worker. Static assets bypass it; metadata never does.
  assert.deepEqual(JSON.parse(`[${first}]`), [
    "/*", "!/img/*", "!/fonts/*", "!/favicon.png", "!/favicon-dark.png", "!/style.css",
    "!/releases/*.js", "!/install.sh",
  ]);
});

function excludedBy(patterns, path) {
  return patterns.some((pattern) => {
    if (!pattern.startsWith("!")) return false;
    const route = pattern.slice(1);
    if (route.endsWith("/*")) return path.startsWith(route.slice(0, -1));
    if (route.includes("*")) {
      const source = route.split("*").map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("[^/]*");
      return new RegExp(`^${source}$`).test(path);
    }
    return path === route;
  });
}

async function publicFiles(directory, prefix = "") {
  const found = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.isDirectory()) found.push(...await publicFiles(new URL(entry.name + "/", directory), `${prefix}${entry.name}/`));
    else found.push("/" + prefix + entry.name);
  }
  return found;
}

test("every static asset has a Worker exclusion, and no exclusion covers a page or dynamic route", async () => {
  const wrangler = await readFile(new URL("./wrangler.jsonc", import.meta.url), "utf8");
  const first = /"run_worker_first":\s*\[([^\]]*)\]/.exec(wrangler)[1];
  const patterns = JSON.parse(`[${first}]`);
  const dynamic = [/^\/dl\//, /^\/relay\//, /^\/extensions\/catalog\.json$/, /^\/extensions\/icons\//, /^\/releases\/data\.json$/];
  const files = await publicFiles(new URL("./public/", import.meta.url));
  assert.ok(files.length > 10);
  for (const path of files) {
    if (path.endsWith(".html") || dynamic.some((pattern) => pattern.test(path))) continue;
    assert.ok(excludedBy(patterns, path), `${path} must bypass the Worker; add its negative run_worker_first pattern`);
  }
  assert.ok(excludedBy(patterns, "/install.sh"), "publish-time install.sh bypasses the Worker");
  for (const path of [
    "/", "/en/", "/missing", "/releases/", "/en/releases/",
    "/dl/VERSION", "/dl/personad-linux-amd64.gz", "/dl/v1/manifest.json", "/relay/v1/health",
    "/extensions/catalog.json", "/extensions/icons/github", "/releases/data.json",
  ]) {
    assert.equal(excludedBy(patterns, path), false, `${path} must still reach the Worker`);
  }
});

// ---------- the page's logic ----------

const sample = () => slimReleases([
  release("v0.4.0", { published_at: "2026-11-13T05:00:00Z", assets: [] }),
  release("v0.3.0", { published_at: "2026-11-12T05:00:00Z", assets: full("v0.3.0", "0.3.0") }),
  release("v0.2.1", { published_at: "2026-11-11T05:00:00Z", assets: full("v0.2.1", "0.2.1") }),
  release("v0.5.0-rc.1", { published_at: "2026-11-14T05:00:00Z", prerelease: true, assets: full("v0.5.0-rc.1", "0.5.0-rc.1") }),
]);

test("each device gets the latest stable file, or the newest stable release that has one", () => {
  const data = sample();
  assert.equal(latestRelease(data).tag, "v0.4.0");
  const mac = downloadFor(data, "macos", "arm64");
  assert.equal(mac.tag, "v0.3.0", "v0.4.0 is still uploading; a prerelease is never offered");
  assert.equal(mac.current, false);
  assert.equal(mac.asset.name, "Persona-0.3.0-darwin-arm64.tar.gz");
  assert.equal(downloadFor(data, "android", "").asset.name, "Persona-0.3.0-android.apk");
  assert.equal(downloadFor(data, "harmony", "").asset.name, "Persona-0.3.0-harmony.hap");
  assert.equal(areaLabel("harmony"), "鸿蒙");
  assert.equal(downloadFor(data, "web", "").asset.kind, "web");
  assert.equal(downloadFor(data, "linux", "riscv64"), null);

  const done = slimReleases([release("v0.4.0", { assets: full("v0.4.0", "0.4.0") }), release("v0.3.0", { assets: full("v0.3.0", "0.3.0") })]);
  assert.deepEqual([downloadFor(done, "windows", "amd64").tag, downloadFor(done, "windows", "amd64").current], ["v0.4.0", true]);
});

test("the page links only to GitHub's own download addresses", () => {
  assert.ok(isDirectDownload({ name: "a b.zip", url: `${DL}v0.3.0/a%20b.zip` }));
  for (const url of [
    "https://personaside.com/dl/a.zip", `${DL}v0.3.0/a.zip/extra`, `${DL}../a.zip`, `${DL}v0.3.0/b.zip`,
    "javascript:alert(1)", `${DL}latest/a.zip`, `http://github.com/nwverse/persona/releases/download/v0.3.0/a.zip`,
  ]) assert.equal(isDirectDownload({ name: "a.zip", url }), false, url);
});

test("the visitor's system is read from the browser", () => {
  const mac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15";
  assert.deepEqual(detectPlatform({ userAgent: mac, maxTouchPoints: 0 }), { platform: "macos", arch: "arm64" });
  assert.equal(detectPlatform({ userAgent: mac, maxTouchPoints: 5 }).platform, "ios", "an iPad asking for the desktop site");
  assert.equal(detectPlatform({ userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X)" }).platform, "ios");
  assert.equal(detectPlatform({ userAgent: "Mozilla/5.0 (Linux; Android 15; Pixel 9)" }).platform, "android");
  for (const ua of [
    "Mozilla/5.0 (Phone; HarmonyOS 7.0; API 26)",
    "Mozilla/5.0 (Linux; HarmonyOS NEXT; ALN-AL00)",
    "Mozilla/5.0 (OpenHarmony 7.0)",
  ]) assert.equal(detectPlatform({ userAgent: ua }).platform, "harmony", ua);
  assert.equal(detectPlatform({ userAgentData: { platform: "HarmonyOS" } }).platform, "harmony");
  assert.deepEqual(detectPlatform({ userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)" }), { platform: "windows", arch: "amd64" });
  assert.deepEqual(detectPlatform({ userAgent: "Mozilla/5.0 (X11; Linux aarch64)" }), { platform: "linux", arch: "arm64" });
  assert.deepEqual(detectPlatform({ userAgent: "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0)" }), { platform: null, arch: "" });
  assert.deepEqual(detectPlatform({ userAgent: "" }), { platform: null, arch: "" });
});

test("sizes and counts read plainly", () => {
  assert.equal(formatSize(117300513), "112 MB");
  assert.equal(formatSize(8599801), "8.2 MB");
  assert.equal(formatSize(1306), "1 KB");
  assert.equal(formatSize(441), "441 B");
  assert.equal(countsLine({ features: 166, fixes: 223, other: 110 }), "166 项新功能 · 223 项修复");
  assert.equal(countsLine({ features: 0, fixes: 0, other: 3 }), "3 项改动");
});

test("dates, ages and counts have natural English forms and unchanged Chinese defaults", () => {
  const iso = new Date(2026, 10, 13, 12).toISOString();
  assert.equal(formatDate(iso), "2026 年 11 月 13 日");
  assert.equal(formatDate(iso, "zh"), "2026 年 11 月 13 日");
  assert.equal(formatDate(iso, "en"), "November 13, 2026");
  assert.equal(formatDate("not a date", "en"), "");

  const now = Date.parse("2026-11-13T06:00:00Z");
  const ago = (minutes) => new Date(now - minutes * 60000).toISOString();
  assert.equal(formatAge(ago(5), now), "5 分钟前");
  assert.equal(formatAge(ago(60), now), "1 小时前");
  assert.equal(formatAge(ago(1440), now), "24 小时前");
  assert.equal(formatAge(ago(2880), now), "2 天前");
  assert.equal(formatAge(ago(0), now, "en"), "just now");
  assert.equal(formatAge(ago(1), now, "en"), "1 minute ago");
  assert.equal(formatAge(ago(2), now, "en"), "2 minutes ago");
  assert.equal(formatAge(ago(60), now, "en"), "1 hour ago");
  assert.equal(formatAge(ago(120), now, "en"), "2 hours ago");
  assert.equal(formatAge(ago(1440), now, "en"), "24 hours ago");
  assert.equal(formatAge(ago(2880), now, "en"), "2 days ago");

  assert.equal(countsLine({ features: 1, fixes: 1, other: 4 }, "en"), "1 new feature · 1 fix");
  assert.equal(countsLine({ features: 2, fixes: 3, other: 4 }, "en"), "2 new features · 3 fixes");
  assert.equal(countsLine({ features: 0, fixes: 0, other: 1 }, "en"), "1 change");
  assert.equal(countsLine({ features: 0, fixes: 0, other: 3 }, "en"), "3 changes");
  assert.equal(countsLine(null, "en"), "");
});

test("device, guide, component, architecture and area copy is language-aware", () => {
  assert.equal(devicesFor(), DEVICES);
  assert.equal(devicesFor("zh"), DEVICES);
  assert.equal(devicesFor("en"), EN_DEVICES);
  assert.deepEqual(EN_DEVICES.map((device) => device.id), DEVICES.map((device) => device.id));
  assert.equal(deviceFor("web"), DEVICES.find((device) => device.id === "web"));
  assert.equal(deviceFor("web", "en").name, "Web app");
  assert.equal(deviceFor("harmony", "en").name, "HarmonyOS");
  assert.equal(deviceFor("ios", "en").sub, "Public TestFlight beta · Requires iOS 26");
  assert.equal(webGuideFor(), WEB_GUIDE);
  assert.equal(webGuideFor("en"), EN_WEB_GUIDE);
  assert.equal(componentsFor(), COMPONENTS);
  assert.equal(componentsFor("en"), EN_COMPONENTS);
  assert.equal(componentsFor("en").find((component) => component.id === "personad").what, "All in one: the hub, assistant services and computer runtime");

  assert.equal(archLabel("macos", "arm64"), "Apple 芯片");
  assert.equal(archLabel("macos", "amd64"), "Intel 芯片");
  assert.equal(archLabel("macos", "arm64", "en"), "Apple silicon");
  assert.equal(archLabel("macos", "amd64", "en"), "Intel");
  assert.equal(archLabel("linux", "arm64", "en"), "ARM64");
  assert.equal(archLabel("macos", "unknown", "en"), "");
  assert.equal(areaLabel("web"), "网页版");
  assert.equal(areaLabel("assistant"), "助手");
  assert.equal(areaLabel("WEB", "en"), "Web");
  assert.equal(areaLabel("knowledge", "en"), "Knowledge base");
  assert.equal(areaLabel("harmony", "en"), "HarmonyOS");
  assert.equal(areaLabel("unknown", "en"), "");
});

test("the page text catalogs have matching keys and preserve Chinese phrasing", () => {
  assert.deepEqual(Object.keys(PAGE_TEXT.en).sort(), Object.keys(PAGE_TEXT.zh).sort());
  for (const key of Object.keys(PAGE_TEXT.zh)) assert.equal(typeof PAGE_TEXT.en[key], typeof PAGE_TEXT.zh[key], key);
  assert.equal(pageText(), PAGE_TEXT.zh);
  assert.equal(pageText("zh"), PAGE_TEXT.zh);
  assert.equal(pageText("en-US"), PAGE_TEXT.zh);
  assert.equal(pageText("en"), PAGE_TEXT.en);

  const mac = { id: "macos", name: "Mac" };
  assert.equal(PAGE_TEXT.zh.releasedOn("2026 年 11 月 13 日"), "2026 年 11 月 13 日发布");
  assert.equal(PAGE_TEXT.zh.detected(mac), "看起来你在用 Mac");
  assert.equal(PAGE_TEXT.zh.downloadTitle(mac), "下载 Mac 版");
  assert.equal(PAGE_TEXT.zh.directMeta("Persona.tar.gz", "提示"), "Persona.tar.gz · 从 GitHub 直接下载\n提示");
  assert.equal(PAGE_TEXT.zh.fullNotes(1), "GitHub 上的完整说明和另外 1 个文件");
  assert.equal(PAGE_TEXT.zh.fullNotes(0), "GitHub 上的完整说明");
  assert.equal(PAGE_TEXT.zh.showEarlier(2), "显示更早的 2 个版本");
  assert.equal(PAGE_TEXT.zh.downloadHAP, "下载 HAP");
  assert.equal(PAGE_TEXT.zh.buildFromSource, "从源码构建");
  assert.equal(PAGE_TEXT.en.downloadHAP, "Download HAP");
  assert.equal(PAGE_TEXT.en.buildFromSource, "Build from source");
  assert.equal(PAGE_TEXT.en.fullNotes(1), "Full release notes and 1 more file on GitHub");
  assert.equal(PAGE_TEXT.en.fullNotes(2), "Full release notes and 2 more files on GitHub");
  assert.equal(PAGE_TEXT.en.fullNotes(0), "Full release notes on GitHub");
  assert.equal(PAGE_TEXT.en.showEarlier(1), "Show 1 earlier release");
  assert.equal(PAGE_TEXT.en.showEarlier(2), "Show 2 earlier releases");
});

test("the page draws data as text and never links packages through this site", async () => {
  const page = await readFile(new URL("./public/releases/page.js", import.meta.url), "utf8");
  assert.match(page, /document\.documentElement\.lang === "en"/, "English requires the exact html lang value");
  assert.doesNotMatch(page, /[㐀-鿿]/, "all Chinese presentation copy lives in the catalogs");
  for (const key of Object.keys(PAGE_TEXT.zh)) assert.ok(page.includes(`T.${key}`), `page uses catalog key ${key}`);
  assert.doesNotMatch(page, /\.innerHTML\s*=(?!\s*`<svg)/, "innerHTML only for the fixed icon markup");
  assert.doesNotMatch(page, /insertAdjacentHTML|outerHTML|document\.write/);
  assert.doesNotMatch(page, /\/dl\//);
  const html = await readFile(new URL("./public/releases/index.html", import.meta.url), "utf8");
  assert.match(html, /<noscript>[\s\S]*github\.com\/nwverse\/persona\/releases\/latest/);
  assert.doesNotMatch(html, /href="\/dl\//);
});

test("the home page offers every device's download, through the same model and never through this site", async () => {
  const home = await readFile(new URL("./public/index.html", import.meta.url), "utf8");
  const section = /<section class="block" id="download"[\s\S]*?<\/section>/.exec(home)[0];
  for (const id of ["ios", "android", "harmony", "macos", "windows", "linux", "web"]) assert.match(section, new RegExp(`data-id="${id}"`));
  for (const id of ["android", "harmony", "macos", "windows", "linux", "web"]) assert.match(section, new RegExp(`href="/releases/#dev-${id}"`), id);
  assert.doesNotMatch(section, /\/dl\//);
  assert.match(home, /import \{[^}]*downloadFor[^}]*\} from "\/releases\/model\.js"/);
  assert.match(home, /<a href="#download">下载<\/a>/);
  // the top bar's version is the release the download page shows, not the hub update channel's build
  assert.doesNotMatch(home, /\/dl\/VERSION/);
  assert.match(home, /<a class="ver" id="ver" href="\/releases\/" hidden>/);
  // the web app has a guide, not a download
  const webTile = /<div class="dl-tile" data-id="web">[\s\S]*?<\/div><\/div>\s*$/m.exec(section)[0];
  assert.doesNotMatch(webTile, /网页包|去下载|download/);
  assert.match(webTile, /扫码登录/);
});
