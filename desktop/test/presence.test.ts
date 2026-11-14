import assert from "node:assert/strict";
import test from "node:test";
import { desktopPresence } from "../src/presence.js";

test("desktop presence requires recent input, an unlocked session and notifications", () => {
  assert.deepEqual(desktopPresence(20, false, true), { active: true, notifies: true });
  assert.deepEqual(desktopPresence(300, false, true), { active: false, notifies: false });
  assert.deepEqual(desktopPresence(20, true, true), { active: false, notifies: false });
  assert.deepEqual(desktopPresence(20, false, false), { active: false, notifies: false });
});
