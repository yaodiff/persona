// A real update, with no certificate and no published release.
//
//   node desktop/scripts/update-test.mjs [output directory]
//
// Builds the test-only package (electron-builder.update-test.yml) twice, as an old and a new version whose feed is a
// local directory served by this script, then runs the packaged old version and checks what it actually does:
//
//   Windows  installs the old NSIS package per user into a temporary directory, waits for the app to find, download
//            and verify the new one, lets it quit, and checks that the reopened app is the new version.
//   Linux    the same through an AppImage. The product ships no Linux package; this is evidence for the shared feed,
//            download and checksum code on a computer that has neither macOS nor Windows.
//   macOS    runs the old version from a ZIP with an ad-hoc signature (no certificate), checks that it finds and
//            downloads the new ZIP and that its checksum matches, then follows Squirrel.Mac's own verdict: when it
//            accepts the file, quitting must install it and the reopened app must be the new version; when it
//            refuses, the test records the refusal. It never reports an install it did not see. None of this is a
//            Developer ID signed, notarized release; that still needs the certificate.
//
// Every child process and temporary directory is owned by this script and removed at the end. One verdict line.
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { chmod, copyFile, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { basename, join, normalize, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { createCacheIdentity, createRunPaths, nsisGuid, prepareRunPaths, removeOwnedRunPaths, shipItJobLabel, testAppId, updaterCacheName, windowsRegistryKeys } from "./update-test-cache.mjs";
import { finishUpdateTest, packagedUpdateConfigPath, readPackagedUpdateConfig, readUpdateTestEvents, watchForNsisInstall, watchUpdateTestEvents as watch } from "./update-test-receipts.mjs";
import { inspectWindowsRegistryKeys, readWindowsInstallLocations, removeOwnedWindowsRegistryKeys as removeWindowsKeys, removeOwnedShipItJob as removeShipItJob } from "./update-test-native.mjs";

const OLD = "0.9.0";
const NEW = "0.9.1";
const PRODUCT = "Persona Update Test";
const root = fileURLToPath(new URL("..", import.meta.url));
const output = resolve(process.argv[2] ?? join(root, "release", "update-test", "result"));
const platform = process.platform;
const npm = platform === "win32" ? "npm.cmd" : "npm";
const steps = [];
const requests = [];
const owned = new Set();
let appLog = "";
let work;
let server;
let feed;
let runPaths;
let cacheIdentity;

const sleep = milliseconds => new Promise(done => setTimeout(done, milliseconds));
const step = (name, ok, detail) => { steps.push({ name, ok, ...(detail === undefined ? {} : { detail }) }); console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail === undefined ? "" : ` ${typeof detail === "string" ? detail : JSON.stringify(detail)}`}`); };
function need(name, ok, detail) { step(name, ok, detail); if (!ok) throw new Error(name); }

/** Runs a command to completion with a deadline; the whole process tree goes when it is exceeded. */
function run(command, args, { seconds, env, cwd = root, quiet = false, verbatim = false, shell = false }) {
  return new Promise((done, fail) => {
    const child = spawn(command, args, { cwd, env: env ?? process.env, stdio: ["ignore", "pipe", "pipe"], windowsVerbatimArguments: verbatim, shell, detached: platform !== "win32" });
    owned.add(child);
    let text = "";
    const keep = data => { text = (text + data).slice(-20_000); if (!quiet) process.stdout.write(data); };
    child.stdout.on("data", keep);
    child.stderr.on("data", keep);
    const timer = setTimeout(() => { stop(child); fail(new Error(`${basename(command)} timed out after ${seconds}s`)); }, seconds * 1000);
    child.once("error", error => { clearTimeout(timer); owned.delete(child); fail(error); });
    child.once("close", code => { clearTimeout(timer); owned.delete(child); done({ code, text }); });
  });
}

function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  try {
    if (platform === "win32") spawn("taskkill", ["/T", "/F", "/PID", String(child.pid)], { stdio: "ignore" });
    else process.kill(-child.pid, "SIGKILL");
  } catch { /* already gone */ }
}

const sha512 = file => new Promise((done, fail) => {
  const hash = createHash("sha512");
  createReadStream(file).on("data", data => hash.update(data)).on("error", fail).on("end", () => done(hash.digest("base64")));
});

const pipeRange = (file, start, end, response) => new Promise((done, fail) => {
  createReadStream(file, { start, end }).on("error", fail).on("end", done).pipe(response, { end: false });
});

/**
 * The feed: a plain directory over HTTP, like a release's assets. Byte ranges are honoured, including the
 * multipart/byteranges answer the updater's differential download asks a generic feed for.
 */
async function serve() {
  server = createServer((request, response) => {
    void (async () => {
      const path = decodeURIComponent(new URL(request.url ?? "/", "http://127.0.0.1").pathname);
      const file = feed ? normalize(join(feed, path)) : undefined;
      const entry = { method: request.method, path, range: request.headers.range };
      requests.push(entry);
      try {
        if (path === "/" && request.method === "GET") {
          // The page the app's main window opens, standing in for a hub: an installed app being updated already has one.
          entry.status = 200;
          response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" }).end("<!doctype html><title>Persona update test</title><p>Update test hub page</p>");
          return;
        }
        if (!file || !file.startsWith(feed + sep) || request.method !== "GET") throw new Error("not served");
        const info = await stat(file);
        if (!info.isFile()) throw new Error("not a file");
        const size = info.size;
        const header = request.headers.range;
        const ranges = header === undefined ? [[0, size - 1]] : /^bytes=\d+-\d*(?:,\s*\d+-\d*)*$/.test(header)
          ? header.slice(6).split(",").map(part => { const [from, to] = part.trim().split("-"); return [Number(from), to ? Number(to) : size - 1]; })
          : [];
        if (ranges.length === 0 || ranges.some(([start, end]) => start > end || end >= size)) throw new Error("bad range");
        const boundary = "persona-update-test-range";
        const parts = ranges.map(([start, end]) => ({ start, end, head: ranges.length > 1 ? `\r\n--${boundary}\r\nContent-Type: application/octet-stream\r\nContent-Range: bytes ${start}-${end}/${size}\r\n\r\n` : "" }));
        const tail = ranges.length > 1 ? `\r\n--${boundary}--\r\n` : "";
        const length = parts.reduce((total, part) => total + Buffer.byteLength(part.head) + part.end - part.start + 1, Buffer.byteLength(tail));
        entry.status = header === undefined ? 200 : 206;
        response.writeHead(entry.status, {
          "Content-Length": length, "Accept-Ranges": "bytes",
          ...(ranges.length > 1 ? { "Content-Type": `multipart/byteranges; boundary=${boundary}` } : { "Content-Type": "application/octet-stream" }),
          ...(header !== undefined && ranges.length === 1 ? { "Content-Range": `bytes ${ranges[0][0]}-${ranges[0][1]}/${size}` } : {}),
        });
        try {
          for (const part of parts) {
            if (part.head) response.write(part.head);
            await pipeRange(file, part.start, part.end, response);
          }
          response.end(tail);
          entry.sent = length;
        } catch { response.destroy(); }
        return;
      } catch {
        entry.status = 404;
        response.writeHead(404).end();
      }
    })();
  });
  await new Promise(done => server.listen(0, "127.0.0.1", done));
  return server.address().port;
}

async function build(version, port) {
  const directory = join(work, `build-${version}`);
  const target = platform === "win32" ? ["--win", "nsis", "--x64"] : platform === "darwin" ? ["--mac", "zip", `--${process.arch}`] : ["--linux", "AppImage", "--x64"];
  const { code, text } = await run(process.execPath, [
    join(root, "node_modules", "electron-builder", "cli.js"), "--config", "electron-builder.update-test.yml", ...target, "--publish", "never",
    `-c.publish.url=http://127.0.0.1:${port}/`, `-c.appId=${testAppId(runPaths.identity)}`, `-c.nsis.guid=${nsisGuid(runPaths.identity)}`,
    `-c.extraMetadata.version=${version}`, `-c.extraMetadata.name=${cacheIdentity}`, `-c.directories.output=${directory}`,
  ], { seconds: 1500, quiet: true, env: { ...process.env, PERSONA_DESKTOP_APP_ID: "com.example.persona.update-test", CSC_IDENTITY_AUTO_DISCOVERY: "false" } });
  if (code !== 0) console.log(text);
  need(`build ${version}`, code === 0, `exit ${code}`);
  const updateConfigPath = packagedUpdateConfigPath(directory, platform);
  const updateConfig = await readPackagedUpdateConfig(directory, platform);
  need(`${version} app-update.yml is in the packaged resources directory`, /^url: http:\/\/127\.0\.0\.1:/m.test(updateConfig), updateConfigPath);
  need(`${version} uses this run's updater cache identity`, new RegExp(`^updaterCacheDirName: ${cacheIdentity}$`, "m").test(updateConfig), cacheIdentity);
  return directory;
}

/** latest.yml / latest-mac.yml / latest-linux.yml: the three top-level fields the updater trusts. */
async function metadata(directory) {
  const name = platform === "win32" ? "latest.yml" : platform === "darwin" ? "latest-mac.yml" : "latest-linux.yml";
  const text = await readFile(join(directory, name), "utf8");
  const field = key => new RegExp(`^${key}: ['"]?([^'"\\r\\n]+)['"]?\\s*$`, "m").exec(text)?.[1];
  return { name, version: field("version"), path: field("path"), sha512: field("sha512") };
}

function events(profile) {
  return readUpdateTestEvents(profile);
}

function launch(executable, profile) {
  // An AppImage unpacks itself where FUSE is missing; keep that inside this test's own directory.
  const env = { ...process.env, ...runPaths.environment, PERSONA_UPDATE_TEST_EXECUTABLE_DIR: installed, ...(platform === "linux" ? { APPIMAGE_EXTRACT_AND_RUN: "1", TMPDIR: join(work, "tmp") } : {}) };
  for (const name of ["PERSONA_HUB_URL", "PERSONA_DESKTOP_DISABLE_UPDATE", "PERSONA_DESKTOP_SMOKE", "ELECTRON_RUN_AS_NODE"]) delete env[name];
  const args = [`--user-data-dir=${profile}`];
  if (platform === "linux") args.push("--no-sandbox", "--disable-gpu");
  const child = spawn(executable, args, { env, stdio: ["ignore", "pipe", "pipe"], detached: platform !== "win32" });
  appLog += `--- ${basename(executable)}\n`;
  // The AppImage runtime lists every file it unpacks; only the app's own lines are worth keeping.
  const keep = data => { appLog = (appLog + String(data).split("\n").filter(line => !line.includes("appimage_extracted") && !line.startsWith("File exists and file size matches")).join("\n")).slice(-200_000); };
  child.stdout.on("data", keep);
  child.stderr.on("data", keep);
  owned.add(child);
  child.once("close", () => owned.delete(child));
  child.once("error", () => owned.delete(child));
  return child;
}

async function exited(child, seconds) {
  const deadline = Date.now() + seconds * 1000;
  while (child.exitCode === null && child.signalCode === null && Date.now() < deadline) await sleep(250);
  return child.exitCode !== null || child.signalCode !== null;
}

async function windowsVersion(executable) {
  const { text } = await run("powershell", ["-NoProfile", "-NonInteractive", "-Command", `(Get-Item -LiteralPath '${executable.replaceAll("'", "''")}').VersionInfo.ProductVersion`], { seconds: 60, quiet: true });
  return text.trim();
}

async function macVersion(bundle) {
  const { text } = await run("plutil", ["-extract", "CFBundleShortVersionString", "raw", "-o", "-", join(bundle, "Contents", "Info.plist")], { seconds: 60, quiet: true });
  return text.trim();
}

async function windowsRunning(image) {
  const { text } = await run("tasklist", ["/FI", `IMAGENAME eq ${image}`, "/NH"], { seconds: 60, quiet: true });
  return text.toLowerCase().includes(image.toLowerCase());
}

/** Query, then remove only this run's exact random keys; never enumerate or reset the account's registry. */
async function removeOwnedWindowsRegistryKeys() {
  return removeWindowsKeys({ keys: windowsRegistryKeys(runPaths.identity), run });
}
async function windowsRegistryState() {
  return inspectWindowsRegistryKeys({ keys: windowsRegistryKeys(runPaths.identity), run });
}

/** ShipIt's launchd registration is separate from its owned files. Query and remove only this run's random label. */
async function removeOwnedShipItJob() {
  return removeShipItJob({ label: shipItJobLabel(runPaths.identity), uid: process.getuid(), run });
}

async function assertNativeStateAbsent() {
  if (platform === "win32") {
    const state = await windowsRegistryState();
    need("this run's NSIS registry keys do not already belong to another run", Object.values(state).every(entry => !entry.exists), state);
  } else if (platform === "darwin") {
    const label = shipItJobLabel(runPaths.identity);
    const result = await run("launchctl", ["print", `gui/${process.getuid()}/${label}`], { seconds: 30, quiet: true });
    need("this run's Squirrel launchd label is not already registered", result.code === 113, `query exit ${result.code}`);
  }
}

const cacheDirectory = () => runPaths.cache;

async function main() {
  await mkdir(output, { recursive: true });
  // The tray is drawn by GTK on Linux, which stops the whole app when there is no display (Xvfb is enough).
  if (platform === "linux") need("a display is available", !!(process.env.DISPLAY || process.env.WAYLAND_DISPLAY), "run under Xvfb on a computer without one");
  work = await mkdtemp(join(tmpdir(), "persona-update-test-"));
  runPaths = createRunPaths(work, createCacheIdentity());
  await prepareRunPaths(runPaths);
  cacheIdentity = updaterCacheName(runPaths.identity);
  await mkdir(join(work, "tmp"));
  await assertNativeStateAbsent();
  const { code } = await run(npm, ["run", "build"], { seconds: 600, quiet: true, shell: platform === "win32" });
  need("compile", code === 0);
  const port = await serve();
  const oldBuild = await build(OLD, port);
  const newBuild = await build(NEW, port);
  const previous = await metadata(oldBuild);
  const next = await metadata(newBuild);
  need("the new build's metadata names the new version", previous.version === OLD && next.version === NEW && !!next.path && !!next.sha512, next);
  const published = await sha512(join(newBuild, next.path));
  need("the metadata's sha512 is the package's sha512", published === next.sha512);
  feed = newBuild;

  // Put the old version where a person would have it.
  let executable;
  let installed;
  if (platform === "win32") {
    installed = join(work, "app");
    // NSIS takes /D last and unquoted; the temporary path has no spaces.
    const install = await run(join(oldBuild, previous.path), ["/S", `/D=${installed}`], { seconds: 600, verbatim: true });
    executable = join(installed, `${PRODUCT}.exe`);
    need("silent per-user install of the old version", install.code === 0 && await windowsVersion(executable).then(version => version.startsWith(OLD), () => false), `exit ${install.code}`);
  } else if (platform === "darwin") {
    installed = join(work, "app");
    await mkdir(installed);
    need("unpack the old version", (await run("ditto", ["-x", "-k", join(oldBuild, previous.path), installed], { seconds: 300 })).code === 0);
    const bundle = join(installed, `${PRODUCT}.app`);
    // Ad-hoc only (no certificate, no identity): Apple silicon refuses to start a binary with no signature at all.
    need("ad-hoc signature so the app can start", (await run("codesign", ["--force", "--deep", "--sign", "-", bundle], { seconds: 300, quiet: true })).code === 0);
    executable = join(bundle, "Contents", "MacOS", PRODUCT);
    const { text: bundleId } = await run("plutil", ["-extract", "CFBundleIdentifier", "raw", "-o", "-", join(bundle, "Contents", "Info.plist")], { seconds: 60, quiet: true });
    need("the packaged app owns this run's Squirrel identity", bundleId.trim() === testAppId(runPaths.identity), bundleId.trim());
  } else {
    installed = join(work, "app");
    await mkdir(installed);
    executable = join(installed, previous.path);
    await copyFile(join(oldBuild, previous.path), executable);
    await chmod(executable, 0o755);
    step("copy the old AppImage", true);
  }

  // The old version finds, downloads and verifies the new one by itself.
  const profile = join(work, "profile");
  // A saved hub, as on any computer where Persona is already set up; the first-run setup window is not under test here.
  await mkdir(profile, { recursive: true });
  await writeFile(join(profile, "desktop-state.json"), `${JSON.stringify({ hub: `http://127.0.0.1:${port}/` })}\n`, "utf8");
  const first = launch(executable, profile);
  // "Ready" is the moment the update can be installed. On macOS that is Squirrel.Mac's own verdict on the file,
  // which arrives after electron-updater's download event; a refusal arrives as an error instead.
  const ready = platform === "darwin" ? "native-downloaded" : "downloaded";
  const settled = await watch(profile, 300, seen => seen.find(entry => entry.event === ready) ?? (seen.some(entry => entry.event === "available") ? seen.find(entry => entry.event === "error") : undefined));
  const seen = await events(profile);
  need("the packaged old version started", seen[0]?.event === "start" && seen[0].version === OLD && seen[0].packaged === true, seen[0]);
  need("it found the new version in the feed", seen.find(entry => entry.event === "available")?.version === NEW, requests.filter(entry => entry.path.endsWith(".yml")).map(entry => `${entry.path} ${entry.status}`));
  need("its main window loaded the saved hub address", await watch(profile, 30, () => requests.some(entry => entry.path === "/" && entry.status === 200)), requests.filter(entry => entry.path === "/"));
  need("the feed served the package", requests.some(entry => entry.path === `/${next.path}` && (entry.status === 200 || entry.status === 206)));

  if (platform === "darwin") {
    // electron-updater checks the ZIP's sha512 before it hands the file to Squirrel.Mac and keeps the verified copy.
    const pending = join(cacheDirectory(), "pending");
    const files = (await readdir(pending).catch(() => [])).filter(name => name.endsWith(".zip"));
    const hashes = await Promise.all(files.map(name => sha512(join(pending, name))));
    need("the downloaded ZIP's sha512 matches the feed", hashes.includes(next.sha512), files);
  } else {
    need("it downloaded the new version", settled?.event === "downloaded" && settled.version === NEW, settled ?? seen.at(-1));
    // Read before the app quits: installing moves this file away.
    const downloaded = await sha512(settled.file).catch(() => undefined);
    need("the downloaded package's sha512 matches the feed", downloaded === next.sha512, basename(settled.file));
    if (platform === "win32") {
      const receipt = await watchForNsisInstall(profile, 180, installed);
      need("the real NSIS updater runs inside this run's temporary directory", receipt.matchesExpectedDirectory && receipt.event.executableInRunDirectory === true, receipt.event ?? { expectedExecutableDirectory: installed });
    }
  }

  let install;
  if (platform === "darwin" && settled?.event !== "native-downloaded") {
    // Nothing is installed, and the test says so rather than passing an install it did not see.
    need("Squirrel.Mac refused the unsigned build's code signature", settled?.event === "error" && /SQRLCodeSignatureErrorDomain|did not pass validation|code failed to satisfy/.test(settled?.message ?? ""), settled?.message?.slice(0, 400) ?? seen.at(-1));
    stop(first);
    await exited(first, 30);
    install = "not installed: Squirrel.Mac refused the update for this build";
  } else {
    if (platform === "darwin") step("Squirrel.Mac accepted the downloaded update", true, "ad-hoc signed test build");
    // Quitting installs. The harness does not install anything itself.
    if (platform === "linux") {
      // The AppImage updater replaces the file on quit and then starts the new one and waits for it, so the old
      // process does not exit by itself. The replaced file is the evidence; the test then stops what was started.
      executable = join(installed, next.path);
      const deadline = Date.now() + 120_000;
      let replaced = false;
      while (!replaced && Date.now() < deadline) {
        replaced = await sha512(executable).catch(() => undefined) === next.sha512;
        if (!replaced) await sleep(1000);
      }
      stop(first);
      await exited(first, 30);
      need("quitting replaced the AppImage with the new version", replaced);
    } else {
      const quit = await exited(first, 120);
      if (!quit) stop(first);
      need("the app quit on its own after the download", quit);
      const deadline = Date.now() + 300_000;
      let version = "";
      for (;;) {
        if (platform === "win32") {
          version = await windowsVersion(executable).catch(() => "");
          if (version.startsWith(NEW) && !await windowsRunning(basename(settled.file))) break;
        } else {
          version = await macVersion(join(installed, `${PRODUCT}.app`)).catch(() => "");
          if (version === NEW) { await sleep(3000); break; }
        }
        if (Date.now() > deadline) break;
        await sleep(2000);
      }
      need("quitting installed the new version in place", version.startsWith(NEW), version);
    }
    // The reopened app is the new version and finds nothing newer.
    const before = (await events(profile)).length;
    const second = launch(executable, profile);
    const again = await watch(profile, 180, all => { const fresh = all.slice(before); return fresh.some(entry => entry.event === "current" || entry.event === "error") ? fresh : undefined; });
    stop(second);
    await exited(second, 30);
    need("the reopened app reports the new version", again?.[0]?.event === "start" && again[0].version === NEW, again?.[0]);
    need("the new version finds nothing newer", again.some(entry => entry.event === "current" && entry.version === NEW), again.at(-1));
    if (platform === "win32") {
      const [installKey] = windowsRegistryKeys(runPaths.identity);
      const registry = await readWindowsInstallLocations({ keys: [installKey], run });
      const receipt = registry[installKey];
      // Read after the version and current-feed checks, so the actual registry receipt cannot be an early install read.
      need("NSIS's own GUID InstallLocation is this run's directory", receipt.exists === true && receipt.installLocation === installed, receipt);
    }
    install = "installed on quit; the reopened app reports the new version";
  }
  return { old: OLD, new: NEW, feed: next, install };
}

async function cleanup() {
  const failures = [];
  for (const child of owned) stop(child);
  await sleep(owned.size > 0 ? 2000 : 0);
  if (server) await new Promise(done => { server.closeAllConnections(); server.close(done); });
  if (!work) return failures;
  if (platform === "win32") {
    const uninstaller = join(work, "app", `Uninstall ${PRODUCT}.exe`);
    // _?= makes the uninstaller run in place and wait, so the directory can be removed afterwards.
    if (await stat(uninstaller).then(() => true, () => false)) {
      const result = await run(uninstaller, ["/S", `_?=${join(work, "app")}`], { seconds: 300, verbatim: true, quiet: true }).catch(error => ({ code: -1, error }));
      if (result.code !== 0) failures.push(`uninstall: ${result.error?.message ?? `exit ${result.code}`}`);
    }
    failures.push(...await removeOwnedWindowsRegistryKeys());
  }
  if (platform === "darwin") failures.push(...await removeOwnedShipItJob());
  if (runPaths) failures.push(...await removeOwnedRunPaths(runPaths));
  let removed = false;
  for (let attempt = 0; attempt < 5 && !removed; attempt++) {
    try { await rm(work, { recursive: true, force: true }); removed = true; }
    catch (error) { if (attempt === 4) failures.push(`temporary directory: ${error.message}`); else await sleep(2000); }
  }
  return failures;
}

let result;
let rejectTimeout;
const timedOut = new Promise((_, reject) => { rejectTimeout = reject; });
const limit = setTimeout(() => {
  for (const child of owned) stop(child);
  rejectTimeout(new Error("Desktop update test timed out after 75 minutes"));
}, 75 * 60 * 1000);
const verdict = await finishUpdateTest({
  runMain: () => Promise.race([main(), timedOut]),
  readEvents: () => work ? events(join(work, "profile")) : [],
  cleanup: async () => {
    let timer;
    try {
      const failures = await Promise.race([
        cleanup(),
        new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("cleanup timed out after 5 minutes")), 300_000); }),
      ]);
      step("cleanup of this run's owned temporary files", failures.length === 0, failures.join("; ") || undefined);
      return failures.join("; ") || undefined;
    } catch (error) {
      const failure = error instanceof Error ? error.message : String(error);
      step("cleanup of this run's owned temporary files", false, failure);
      return failure;
    } finally { clearTimeout(timer); }
  },
  message: error => error instanceof Error ? error.message : String(error),
  report: async ({ result: update, failure, cleanupFailure, profileEvents }) => {
    result = update;
    clearTimeout(limit);
    // Temporary and home paths are this computer's business, not the report's.
    let log = appLog;
    let report = JSON.stringify({ ok: !failure, platform, arch: process.arch, ...(failure ? { failure } : {}), ...(cleanupFailure ? { cleanup: "failed", cleanupFailure } : { cleanup: "succeeded" }), ...result, steps, events: profileEvents, requests }, null, 2);
    for (const [path, name] of [[work, "<temporary directory>"], [runPaths?.home, "<temporary home>"]]) {
      if (!path) continue;
      report = report.replaceAll(JSON.stringify(path).slice(1, -1), name).replaceAll(path, name);
      log = log.replaceAll(path, name);
    }
    await mkdir(output, { recursive: true });
    await writeFile(join(output, "app.log"), log, "utf8");
    await writeFile(join(output, "result.json"), `${report}\n`, "utf8");
  },
});
console.log(`Desktop update test (${platform}): ${verdict.failure ? `FAIL (${verdict.failure})` : `PASS (${OLD} -> ${NEW}; ${result.install})`}`);
process.exit(verdict.ok ? 0 : 1);
