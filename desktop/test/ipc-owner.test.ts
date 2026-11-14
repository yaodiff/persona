import assert from "node:assert/strict";
import test from "node:test";
import { DesktopIPCOwner } from "../src/ipc-owner.js";

test("old hub-window cleanup cannot remove a replacement's IPC ownership", () => {
  const owner = new DesktopIPCOwner();
  const oldCleanup = owner.install();
  const newCleanup = owner.install();
  oldCleanup();
  assert.equal(owner.active, true);
  newCleanup();
  assert.equal(owner.active, false);
  newCleanup();
  assert.equal(owner.active, false);
});
