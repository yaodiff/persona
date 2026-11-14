import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";

const source = (name: string) => readFile(join(import.meta.dirname, "..", "..", "src", name), "utf8");

test("an explicit hub change closes the old window before making its own session and IPC", async () => {
  const main = await source("main.ts");
  const change = main.slice(main.indexOf("async function replaceMainWindow"), main.indexOf("async function showMain"));
  assert.match(change, /parseHubAddress\(previous\.webContents\.getURL\(\)\)\.url\.href === next\.url\.href/);
  assert.match(change, /const closed = new Promise<void>\(done => previous\.once\("closed", done\)\);/);
  assert.ok(change.indexOf("await closed;") < change.indexOf("mainWindow = undefined;"));
  assert.match(main, /await replaceMainWindow\(next, window => window\.destroy\(\)\);\s+hub = next;\s+setup\.close\(\);\s+await showMain\(\);/);
  assert.match(main, /hub: \(\) => \{\s+const url = current\.webContents\.getURL\(\);/);
  const ipc = await source("ipc.ts");
  assert.match(ipc, /inHub\(hub, event\.senderFrame\.url\)/);
});

test("local login-item IPC is separate from the surviving hub window's public bridge", async () => {
  const setup = await source("setup-ipc.ts");
  const preload = await source("setup-preload.cts");
  for (const suffix of ["get", "set"]) {
    assert.match(setup, new RegExp(`"desktop:setup-login-${suffix}"`));
    assert.match(preload, new RegExp(`"desktop:setup-login-${suffix}"`));
    assert.ok(!setup.includes(`"desktop:login-${suffix}"`), `setup must not own desktop:login-${suffix}`);
  }
  const hub = await source("ipc.ts");
  for (const name of ["desktop:login-get", "desktop:login-set"]) assert.match(hub, new RegExp(`"${name}"`));
});
