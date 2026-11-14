import assert from "node:assert/strict";
import test from "node:test";
import { createCacheIdentity, nsisGuid, windowsRegistryKeys, windowsRegistryPaths } from "./update-test-cache.mjs";
import { inspectWindowsRegistryKeys, parseWindowsInstallLocation, readWindowsInstallLocations, removeOwnedShipItJob, removeOwnedWindowsRegistryKeys } from "./update-test-native.mjs";

function double(replies) {
  const calls = [];
  const run = async (command, args, options) => {
    calls.push({ command, args, options });
    const key = `${command} ${args[0]} ${args[1]}`;
    const reply = replies.shift();
    assert.ok(reply, `unexpected command: ${key}`);
    if (reply.command) assert.equal(key, reply.command);
    if (reply.error) throw reply.error;
    return { code: reply.code ?? 0, text: reply.text ?? "" };
  };
  return { calls, run };
}

const identity = createCacheIdentity();
const keys = windowsRegistryKeys(identity);
const guid = nsisGuid(identity);
const label = `com.example.persona.update-test.${guid}.ShipIt`;

test("native preflight queries only this run's GUID keys and reads NSIS's InstallLocation", async () => {
  const entries = [
    { Name: keys[0], Exists: true, InstallLocation: "C:\\owned\\app" },
    { Name: keys[1], Exists: false, InstallLocation: null },
  ];
  const { calls, run } = double([{ command: "powershell -NoProfile -NonInteractive", text: JSON.stringify(entries) }]);
  assert.deepEqual(await inspectWindowsRegistryKeys({ keys, run }), {
    [keys[0]]: { exists: true, installLocation: "C:\\owned\\app" },
    [keys[1]]: { exists: false, installLocation: null },
  });
  assert.equal(calls.length, 1);
  const script = calls[0].args.at(-1);
  assert.ok(script.includes(guid));
  assert.ok(script.includes("InstallLocation"));
  for (const key of keys) assert.ok(script.includes(JSON.stringify(key)));
});

test("native preflight accepts one-key PowerShell output and rejects nonzero exits", async () => {
  const one = double([{ text: JSON.stringify({ Name: keys[0], Exists: false }) }]);
  assert.deepEqual(await inspectWindowsRegistryKeys({ keys: [keys[0]], run: one.run }), { [keys[0]]: { exists: false, installLocation: null } });
  const failed = double([{ code: 5 }]);
  await assert.rejects(inspectWindowsRegistryKeys({ keys, run: failed.run }), /exit 5/);
});

test("NSIS InstallLocation reg queries preserve this run's exact keys and parse REG_SZ", async () => {
  const text = `\r\nHKEY_CURRENT_USER\\Software\\${guid}\r\n    InstallLocation    REG_SZ    C:\\owned\\app\r\n`;
  assert.deepEqual(parseWindowsInstallLocation(text), { installLocation: "C:\\owned\\app" });
  const { calls, run } = double([
    { command: `reg query ${keys[0]}`, text },
    { command: `reg query ${keys[1]}`, code: 1, text: "ERROR: The system was unable to find the specified registry key or value." },
  ]);
  assert.deepEqual(await readWindowsInstallLocations({ keys, run }), {
    [keys[0]]: { exists: true, installLocation: "C:\\owned\\app" },
    [keys[1]]: { exists: false },
  });
  assert.deepEqual(calls.map(call => call.args), keys.map(key => ["query", key, "/v", "InstallLocation"]));
});

test("NSIS receipt failures and empty values are not reported as destination evidence", async () => {
  const failed = double([{ code: 2 }]);
  assert.deepEqual(await readWindowsInstallLocations({ keys: [keys[0]], run: failed.run }), { [keys[0]]: { error: "exit 2" } });
  const empty = double([{ text: "    InstallLocation    REG_SZ    \r\n" }]);
  assert.deepEqual(await readWindowsInstallLocations({ keys: [keys[0]], run: empty.run }), { [keys[0]]: { exists: true, installLocation: null } });
  assert.deepEqual(parseWindowsInstallLocation("OtherValue REG_SZ C:\\not-the-install"), { installLocation: null });
});

test("registry cleanup deletes and rechecks only this run's exact keys", async () => {
  const replies = [
    { code: 0 }, { code: 0 }, // query/delete install key
    { code: 1, text: "ERROR: The system was unable to find the specified registry key or value." }, // uninstall key absent: no delete
    { code: 1, text: "ERROR: The system was unable to find the specified registry key or value." },
    { code: 1, text: "ERROR: The system was unable to find the specified registry key or value." }, // both absent after cleanup
  ];
  const { calls, run } = double(replies);
  assert.deepEqual(await removeOwnedWindowsRegistryKeys({ keys, run }), []);
  assert.deepEqual(calls.map(call => call.args), [
    ["query", keys[0]], ["delete", keys[0], "/f"],
    ["query", keys[1]],
    ["query", keys[0]], ["query", keys[1]],
  ]);
  assert.ok(calls.every(call => call.command === "reg"));
  assert.deepEqual(Object.values(windowsRegistryPaths(identity)), keys);
});

test("registry cleanup preserves query/delete/recheck failures", async () => {
  const queryFailed = double([{ code: 5 }, { code: 1 }, { code: 1 }, { code: 1 }]);
  assert.match((await removeOwnedWindowsRegistryKeys({ keys, run: queryFailed.run })).join("\n"), /query failed.*exit 5/);
  const deleteFailed = double([{ code: 0 }, { code: 5 }, { code: 1 }, { code: 1 }, { code: 1 }]);
  assert.match((await removeOwnedWindowsRegistryKeys({ keys, run: deleteFailed.run })).join("\n"), /cleanup failed.*exit 5/);
  const remains = double([{ code: 1, text: "ERROR: The system was unable to find the specified registry key or value." }, { code: 1, text: "ERROR: The system was unable to find the specified registry key or value." }, { code: 0 }, { code: 1, text: "ERROR: The system was unable to find the specified registry key or value." }]);
  assert.match((await removeOwnedWindowsRegistryKeys({ keys, run: remains.run })).join("\n"), /remains after cleanup/);
});

test("registry access denial is never mistaken for absence", async () => {
  const denied = double([{ code: 1, text: "ERROR: Access is denied." }]);
  assert.match((await readWindowsInstallLocations({ keys: [keys[0]], run: denied.run }))[keys[0]].error, /exit 1.*Access is denied/);
  const cleanup = double([{ code: 1, text: "ERROR: Access is denied." }, { code: 1, text: "ERROR: The system was unable to find the specified registry key or value." }, { code: 1, text: "ERROR: Access is denied." }, { code: 1, text: "ERROR: The system was unable to find the specified registry key or value." }]);
  const failures = await removeOwnedWindowsRegistryKeys({ keys, run: cleanup.run });
  assert.equal(failures.length, 2);
  assert.match(failures.join("\n"), /query failed[\s\S]*Access is denied[\s\S]*verification failed[\s\S]*Access is denied/);
  assert.equal(cleanup.calls.filter(call => call.args[0] === "delete").length, 0);
});

test("ShipIt cleanup handles absence, removal, and failed recheck with only its run label", async () => {
  const absent = double([{ command: `launchctl print gui/12345/${label}`, code: 113 }]);
  assert.deepEqual(await removeOwnedShipItJob({ label, uid: 12345, run: absent.run }), []);
  assert.equal(absent.calls.length, 1);

  const removed = double([{ code: 0 }, { code: 0 }, { code: 113 }]);
  assert.deepEqual(await removeOwnedShipItJob({ label, uid: 12345, run: removed.run }), []);
  assert.deepEqual(removed.calls.map(call => call.args), [
    ["print", `gui/12345/${label}`], ["bootout", `gui/12345/${label}`], ["print", `gui/12345/${label}`],
  ]);

  const remains = double([{ code: 0 }, { code: 0 }, { code: 0 }]);
  assert.match((await removeOwnedShipItJob({ label, uid: 12345, run: remains.run })).join("\n"), /remains registered/);
  const inspectFailed = double([{ code: 1 }]);
  assert.match((await removeOwnedShipItJob({ label, uid: 12345, run: inspectFailed.run })).join("\n"), /could not inspect/);
});
