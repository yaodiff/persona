import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { createCacheIdentity, createRunPaths, nsisGuid, prepareRunPaths, removeOwnedRunPaths, shipItJobLabel, testAppId, updaterCacheName, windowsRegistryKeys } from "./update-test-cache.mjs";

test("test-only application and NSIS identities are stable in a run and unique across runs", () => {
  const identity = createCacheIdentity();
  const other = createCacheIdentity();
  assert.equal(testAppId(identity), testAppId(identity));
  assert.notEqual(testAppId(identity), testAppId(other));
  assert.match(testAppId(identity), /^com\.example\.persona\.update-test\.[a-f0-9-]{36}$/);
  assert.equal(nsisGuid(identity), identity.slice(-36));
  assert.match(nsisGuid(identity), /^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/);
  assert.notEqual(nsisGuid(identity), nsisGuid(other));
  assert.deepEqual(windowsRegistryKeys(identity), [
    `HKCU\\Software\\${nsisGuid(identity)}`,
    `HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\${nsisGuid(identity)}`,
  ]);
  assert.ok(windowsRegistryKeys(identity).every(key => !windowsRegistryKeys(other).includes(key)));
  assert.notEqual(shipItJobLabel(identity), shipItJobLabel(other));
});

const marker = async path => {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, `sentinel: ${path}\n`, { mode: 0o600 });
  return readFile(path, "utf8");
};

for (const platform of ["linux", "darwin", "win32"]) {
  test(`${platform}: cleanup owns one run's caches and cannot delete foreign HOME content`, async t => {
    const work = await mkdtemp(join(tmpdir(), "persona-update-test-cache-"));
    t.after(async () => { const { rm } = await import("node:fs/promises"); await rm(work, { recursive: true, force: true }); });
    const identity = createCacheIdentity();
    assert.notEqual(identity, createCacheIdentity());
    assert.match(updaterCacheName(identity), /^persona-desktop-update-test-[a-z0-9-]+-updater$/);
    const paths = createRunPaths(work, identity, platform);
    await prepareRunPaths(paths);
    const home = paths.home;
    const own = [paths.cache, paths.shipIt, paths.defaultProfile];
    const other = createRunPaths(work, createCacheIdentity(), platform);
    if (platform === "darwin") {
      assert.equal(paths.environment.CFFIXED_USER_HOME, home);
      assert.equal(paths.shipIt, join(home, "Library/Caches", `${testAppId(identity)}.ShipIt`));
      assert.notEqual(paths.shipIt, other.shipIt);
    }
    const protectedMarkers = [
      // The unsigned development identity is never a valid cleanup target.
      join(home, platform === "win32" ? "AppData/Local" : platform === "darwin" ? "Library/Caches" : ".cache", "com.example.persona.desktop.ShipIt", "sentinel.txt"),
      // A concurrent run has different random cache, bundle/ShipIt and default-profile identities.
      join(paths.cache, "..", `${updaterCacheName(other.identity)}`, "sentinel.txt"),
      join(other.shipIt, "sentinel.txt"),
      join(other.defaultProfile, "sentinel.txt"),
      join(home, platform === "darwin" ? "Documents" : "documents", "sentinel.txt"),
    ];
    if (platform === "linux") protectedMarkers.push(join(home, ".config", "unrelated-application", "sentinel.txt"));
    const ownMarkers = [];
    for (const directory of own) ownMarkers.push(await marker(join(directory, "sentinel.txt")));
    const expected = await Promise.all(protectedMarkers.map(marker));

    assert.deepEqual(await removeOwnedRunPaths(paths), []);
    for (const directory of own) await assert.rejects(readFile(join(directory, "sentinel.txt"), "utf8"), { code: "ENOENT" });
    assert.deepEqual(await Promise.all(protectedMarkers.map(path => readFile(path, "utf8"))), expected);
    assert.ok(ownMarkers.every(Boolean));
  });
}

test("cleanup refuses a foreign path and reports a failed removal separately", async t => {
  const work = await mkdtemp(join(tmpdir(), "persona-update-test-cache-"));
  t.after(async () => { const { rm } = await import("node:fs/promises"); await rm(work, { recursive: true, force: true }); });
  const paths = createRunPaths(work, createCacheIdentity(), "linux");
  await prepareRunPaths(paths);
  const foreign = join(work, "another-run", "sentinel.txt");
  await marker(foreign);
  paths.cache = join(work, "another-run");
  paths.shipIt = paths.home;
  assert.match((await removeOwnedRunPaths(paths)).join("\n"), /updater cache is outside the owned HOME[\s\S]*Squirrel cache is outside the owned HOME/);
  assert.equal(await readFile(foreign, "utf8"), `sentinel: ${foreign}\n`);

  const failing = createRunPaths(work, createCacheIdentity(), "linux");
  await mkdir(failing.home, { recursive: true });
  failing.cache = join(failing.home, ".cache", updaterCacheName(createCacheIdentity()), "sentinel", "descendant");
  await mkdir(dirname(dirname(failing.cache)), { recursive: true });
  await writeFile(dirname(failing.cache), "not a directory");
  const failures = await removeOwnedRunPaths(failing);
  assert.ok(failures.some(failure => failure.startsWith("updater cache:")), failures.join("\n"));
  assert.deepEqual(failures.filter(failure => failure.startsWith("Squirrel cache:")), []);
});

test("test-only configuration keeps disposable shortcuts disabled and production identities out", async () => {
  const config = await readFile(new URL("../electron-builder.update-test.yml", import.meta.url), "utf8");
  const script = await readFile(new URL("./update-test.mjs", import.meta.url), "utf8");
  assert.match(config, /createDesktopShortcut: false/);
  assert.match(config, /createStartMenuShortcut: false/);
  assert.match(script, /-c\.appId=\$\{testAppId\(runPaths\.identity\)\}/);
  assert.match(script, /-c\.nsis\.guid=\$\{nsisGuid\(runPaths\.identity\)\}/);
  assert.match(script, /updaterCacheDirName: \$\{cacheIdentity\}\$/);
  assert.doesNotMatch(script, /com\.example\.persona\.desktop\.ShipIt/);
  assert.doesNotMatch(script, /const CACHE =/);
});
