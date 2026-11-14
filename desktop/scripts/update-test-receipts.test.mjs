import assert from "node:assert/strict";
import { appendFile, cp, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { finishUpdateTest, isNsisInstallAt, packagedUpdateConfigPath, readPackagedUpdateConfig, readUpdateTestEvents, watchForNsisInstall, watchUpdateTestEvents } from "./update-test-receipts.mjs";

async function fixture(t) {
  const directory = await mkdtemp(join(tmpdir(), "persona-update-test-receipts-"));
  t.after(async () => { await rm(directory, { recursive: true, force: true }); });
  return directory;
}

async function configFixture(t, platform, architecture = "x64") {
  const directory = await fixture(t);
  const path = packagedUpdateConfigPath(directory, platform, architecture);
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, "provider: generic\nurl: http://127.0.0.1:12345/\nupdaterCacheDirName: run-updater\n");
  return { directory, path };
}

const layouts = [
  ["win32", "x64", "win-unpacked/resources/app-update.yml"],
  ["darwin", "x64", "mac/Persona Update Test.app/Contents/Resources/app-update.yml"],
  ["darwin", "arm64", "mac-arm64/Persona Update Test.app/Contents/Resources/app-update.yml"],
  ["linux", "x64", "linux-unpacked/resources/app-update.yml"],
];

for (const [platform, architecture, relative] of layouts) {
  test(`${platform} ${architecture}: reads app-update.yml from the exact packaged resources path`, async t => {
    const { directory, path } = await configFixture(t, platform, architecture);
    assert.equal(path, join(directory, relative));
    assert.match(await readPackagedUpdateConfig(directory, platform, architecture), /^updaterCacheDirName: run-updater$/m);
  });
}

test("a decoy at the legacy macOS Resources/resources path does not satisfy the real path", async t => {
  const directory = await fixture(t);
  const decoy = join(directory, "mac-arm64/Persona Update Test.app/Contents/Resources/resources/app-update.yml");
  await mkdir(dirname(decoy), { recursive: true });
  await writeFile(decoy, "updaterCacheDirName: wrong-decoy\n");
  assert.equal(packagedUpdateConfigPath(directory, "darwin", "arm64"), join(directory, "mac-arm64/Persona Update Test.app/Contents/Resources/app-update.yml"));
  await assert.rejects(readPackagedUpdateConfig(directory, "darwin", "arm64"), { code: "ENOENT" });
  assert.equal(await readFile(decoy, "utf8"), "updaterCacheDirName: wrong-decoy\n");
});

for (const [platform, architecture, relative] of [
  ["win32", "x64", "win-unpacked/app-update.yml"],
  ["linux", "x64", "linux-unpacked/app-update.yml"],
  ["darwin", "x64", "mac-x64/Persona Update Test.app/Contents/Resources/resources/app-update.yml"],
]) {
  test(`${platform}: a config only above or below its resources directory is rejected`, async t => {
    const directory = await fixture(t);
    const decoy = join(directory, relative);
    await mkdir(dirname(decoy), { recursive: true });
    await writeFile(decoy, "updaterCacheDirName: wrong-decoy\n");
    await assert.rejects(readPackagedUpdateConfig(directory, platform, architecture), { code: "ENOENT" });
  });
}

const record = entry => `${JSON.stringify({ at: Date.now(), ...entry })}\n`;

test("watch awaits a genuinely delayed NSIS event and validates its real executable directory", async t => {
  const profile = await fixture(t);
  const installed = join(profile, "owned-app");
  const receipt = watchForNsisInstall(profile, 2, installed, { pollMilliseconds: 10 });
  // Leave the file absent for several polls, then create it asynchronously. No expected directory is assigned to the
  // updater field: its real locked implementation leaves that optional property null.
  const write = new Promise(resolve => setTimeout(resolve, 75)).then(() => writeFile(
    join(profile, "update-test.jsonl"),
    record({ event: "nsis-install", executableDirectory: installed, executableInRunDirectory: true, updaterInstallDirectory: null }),
  ));
  const result = await receipt;
  await write;
  assert.equal(result.matchesExpectedDirectory, true);
  assert.equal(result.event.executableDirectory, installed);
  assert.equal(result.event.updaterInstallDirectory, null);
  assert.equal(result.event.executableInRunDirectory, true);
});

test("watch returns undefined after a missing-event deadline, not a Promise treated as an event", async t => {
  const profile = await fixture(t);
  const started = Date.now();
  const result = await watchForNsisInstall(profile, 0.06, join(profile, "owned-app"), { pollMilliseconds: 10 });
  assert.equal(result.event, undefined);
  assert.equal(result.matchesExpectedDirectory, false);
  assert.ok(Date.now() - started >= 50, "the deadline must be a real asynchronous wait");
});

test("a valid NSIS event with the wrong executable directory cannot pass", async t => {
  const profile = await fixture(t);
  await writeFile(join(profile, "update-test.jsonl"), record({ event: "nsis-install", executableDirectory: join(profile, "other-app"), executableInRunDirectory: false, updaterInstallDirectory: null }));
  const result = await watchForNsisInstall(profile, 0, join(profile, "owned-app"), { pollMilliseconds: 1 });
  assert.equal(result.matchesExpectedDirectory, false);
  assert.equal(result.event, undefined);
});

test("watch ignores unrelated early events and accepts a later appended matching event", async t => {
  const profile = await fixture(t);
  const installed = join(profile, "owned-app");
  const file = join(profile, "update-test.jsonl");
  await writeFile(file, record({ event: "start", version: "0.9.0" }));
  const appended = watchUpdateTestEvents(profile, 2, events => events.find(entry => isNsisInstallAt(entry, installed)), { pollMilliseconds: 10 });
  await new Promise(resolve => setTimeout(resolve, 70));
  await appendFile(file, record({ event: "nsis-install", executableDirectory: installed }));
  assert.equal((await appended)?.executableDirectory, installed);
});

test("watch waits past an early wrong-directory event for a later real receipt", async t => {
  const profile = await fixture(t);
  const installed = join(profile, "owned-app");
  const file = join(profile, "update-test.jsonl");
  await writeFile(file, record({ event: "nsis-install", executableDirectory: join(profile, "wrong-app") }));
  const pending = watchForNsisInstall(profile, 2, installed, { pollMilliseconds: 10 });
  await new Promise(resolve => setTimeout(resolve, 70));
  await appendFile(file, record({ event: "nsis-install", executableDirectory: installed }));
  const result = await pending;
  assert.equal(result.matchesExpectedDirectory, true);
  assert.equal(result.event.executableDirectory, installed);
});

test("real event-file read failures propagate instead of becoming an empty receipt history", async t => {
  const profile = await fixture(t);
  await mkdir(join(profile, "update-test.jsonl"));
  await assert.rejects(watchForNsisInstall(profile, 0.1, join(profile, "owned-app"), { pollMilliseconds: 5 }), { code: "EISDIR" });
  await rm(join(profile, "update-test.jsonl"), { recursive: true });
  await writeFile(join(profile, "update-test.jsonl"), "not-json\n");
  await assert.rejects(watchForNsisInstall(profile, 0.1, join(profile, "owned-app"), { pollMilliseconds: 5 }), SyntaxError);
});

test("an incomplete final JSON line is retried after the real writer finishes it", async t => {
  const profile = await fixture(t);
  const installed = join(profile, "owned-app");
  const file = join(profile, "update-test.jsonl");
  const text = record({ event: "nsis-install", executableDirectory: installed });
  await writeFile(file, text.slice(0, -20));
  const pending = watchForNsisInstall(profile, 2, installed, { pollMilliseconds: 10 });
  await new Promise(resolve => setTimeout(resolve, 70));
  await appendFile(file, text.slice(-20));
  assert.equal((await pending).matchesExpectedDirectory, true);
});

test("a null optional updater field never substitutes for the executable receipt", () => {
  assert.equal(isNsisInstallAt({ event: "nsis-install", executableDirectory: "/owned/app", updaterInstallDirectory: null }, "/owned/app"), true);
  assert.equal(isNsisInstallAt({ event: "nsis-install", updaterInstallDirectory: "/owned/app" }, "/owned/app"), false);
  assert.equal(isNsisInstallAt(undefined, "/owned/app"), false);
});

test("the harness awaits watch before reading the NSIS event (legacy expression negative control)", async () => {
  const expected = "/new-owned-fixture/app";
  const watch = async () => ({ event: "nsis-install", executableDirectory: expected, updaterInstallDirectory: null });
  // This is the former expression, retained as a falsifiable control: property access on a pending Promise yields
  // undefined. The production helper must not use it. See watchForNsisInstall for the actual awaited read.
  const legacy = await watch()?.installDirectory === expected;
  const awaited = (await watch())?.installDirectory === expected;
  assert.equal(legacy, false);
  assert.equal(awaited, false);
  assert.equal(isNsisInstallAt(await watch(), expected), true);
});

test("test-only instrumentation forwards the real updater's full restart arguments", async t => {
  const directory = await fixture(t);
  const profile = join(directory, "profile");
  const installed = join(directory, "owned-app");
  const executable = join(installed, "Persona Update Test.exe");
  const received = [];
  const autoUpdater = {
    install(...args) { received.push(args); return "real updater return"; },
    on() {},
  };
  const electron = {
    app: {
      getPath(name) {
        if (name === "userData") return profile;
        if (name === "exe") return executable;
        throw new Error(`unexpected path ${name}`);
      },
      isPackaged: true,
      getVersion: () => "0.9.0",
      isDefaultProtocolClient: () => false,
      setAsDefaultProtocolClient: () => false,
      quit() {},
    },
    autoUpdater: { on() {} },
  };
  globalThis.__updateReceiptDoubles = { autoUpdater, electron };
  t.after(() => { delete globalThis.__updateReceiptDoubles; });
  await mkdir(join(directory, "update-test"), { recursive: true });
  await mkdir(join(directory, "dist"), { recursive: true });
  await mkdir(join(directory, "node_modules"), { recursive: true });
  await cp(new URL("../update-test/main.mjs", import.meta.url), join(directory, "update-test/main.mjs"));
  await writeFile(join(directory, "dist/main.js"), "export {};\n");
  await writeFile(join(directory, "package.json"), '{"type":"module"}\n');
  for (const name of ["electron", "electron-updater"]) {
    await mkdir(join(directory, "node_modules", name), { recursive: true });
    await writeFile(join(directory, "node_modules", name, "package.json"), '{"name":"test-double","type":"module","main":"index.js"}\n');
    await writeFile(join(directory, "node_modules", name, "index.js"), name === "electron"
      ? "const { app, autoUpdater } = globalThis.__updateReceiptDoubles.electron; export { app, autoUpdater };\n"
      : "const autoUpdater = globalThis.__updateReceiptDoubles.autoUpdater; export default { autoUpdater };\n");
  }
  process.env.PERSONA_UPDATE_TEST_EXECUTABLE_DIR = installed;
  t.after(() => { delete process.env.PERSONA_UPDATE_TEST_EXECUTABLE_DIR; });
  const platform = Object.getOwnPropertyDescriptor(process, "platform");
  Object.defineProperty(process, "platform", { value: "win32" });
  t.after(() => Object.defineProperty(process, "platform", platform));
  await import(join(directory, "update-test/main.mjs"));
  assert.equal(autoUpdater.install(true, true), "real updater return");
  assert.deepEqual(received, [[true, true]]);
  const receipt = (await readUpdateTestEvents(profile)).find(entry => entry.event === "nsis-install");
  assert.ok(receipt);
  assert.equal(receipt.executable, executable);
  assert.equal(receipt.executableDirectory, installed);
  assert.equal(receipt.updaterInstallDirectory, null);
  assert.equal(receipt.executableInRunDirectory, true);
  assert.deepEqual(receipt.forwardedArguments, [true, true]);
});

test("a failed final receipt snapshot still cleans up and writes a nonzero verdict", async () => {
  const calls = [];
  let written;
  const verdict = await finishUpdateTest({
    async runMain() { calls.push("main"); throw new SyntaxError("malformed complete event"); },
    async readEvents() { calls.push("snapshot"); throw new SyntaxError("malformed complete event"); },
    async cleanup() { calls.push("cleanup"); return undefined; },
    message: error => error.message,
    async report(value) { calls.push("report"); written = value; },
  });
  assert.deepEqual(calls, ["main", "snapshot", "cleanup", "report"]);
  assert.equal(verdict.ok, false);
  assert.match(verdict.failure, /malformed complete event/);
  assert.equal(written.ok, undefined);
  assert.match(written.failure, /could not read update-test event receipt/);
});

test("snapshot I/O errors remain distinct from a successful cleanup", async () => {
  let written;
  const verdict = await finishUpdateTest({
    async runMain() { return { install: "not installed: failure before update" }; },
    async readEvents() { throw Object.assign(new Error("is a directory"), { code: "EISDIR" }); },
    async cleanup() { return undefined; },
    message: error => error.message,
    async report(value) { written = value; },
  });
  assert.equal(verdict.ok, false);
  assert.match(written.failure, /could not read update-test event receipt: is a directory/);
  assert.equal(written.cleanupFailure, undefined);
});

test("cleanup errors remain a nonzero result after a valid final snapshot", async () => {
  let written;
  const verdict = await finishUpdateTest({
    async runMain() { return { install: "installed" }; },
    async readEvents() { return [{ event: "start" }]; },
    async cleanup() { return "owned removal failed"; },
    message: error => error.message,
    async report(value) { written = value; },
  });
  assert.equal(verdict.ok, false);
  assert.equal(verdict.failure, "owned removal failed");
  assert.equal(written.cleanupFailure, "owned removal failed");
});

test("a deadline rejection still runs cleanup and writes the timeout verdict", async () => {
  const calls = [];
  let written;
  const verdict = await finishUpdateTest({
    runMain: () => Promise.race([new Promise(() => {}), Promise.reject(new Error("deadline exceeded"))]),
    async readEvents() { calls.push("snapshot"); return []; },
    async cleanup() { calls.push("cleanup"); return undefined; },
    message: error => error.message,
    async report(value) { calls.push("report"); written = value; },
  });
  assert.deepEqual(calls, ["snapshot", "cleanup", "report"]);
  assert.equal(verdict.ok, false);
  assert.equal(verdict.failure, "deadline exceeded");
  assert.equal(written.failure, "deadline exceeded");
});

test("a result-report write failure returns a controlled nonzero verdict", async () => {
  const calls = [];
  const verdict = await finishUpdateTest({
    async runMain() { return { install: "installed" }; },
    async readEvents() { return []; },
    async cleanup() { calls.push("cleanup"); return undefined; },
    message: error => error.message,
    async report() { calls.push("report"); throw new Error("result path is a directory"); },
  });
  assert.deepEqual(calls, ["cleanup", "report"]);
  assert.equal(verdict.ok, false);
  assert.equal(verdict.failure, "could not write update-test result report: result path is a directory");
});

test("a cleanup rejection still reaches report and cannot become success", async () => {
  let written;
  const verdict = await finishUpdateTest({
    async runMain() { return { install: "installed" }; },
    async readEvents() { return []; },
    async cleanup() { throw new Error("cleanup deadline exceeded"); },
    message: error => error.message,
    async report(value) { written = value; },
  });
  assert.equal(verdict.ok, false);
  assert.equal(written.cleanupFailure, "cleanup failed: cleanup deadline exceeded");
});

test("unsupported platforms cannot silently fall back to the Linux layout", () => {
  assert.throws(() => packagedUpdateConfigPath("/new-fixture", "freebsd"), /unsupported update-test platform/);
});
