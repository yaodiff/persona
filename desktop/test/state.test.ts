import assert from "node:assert/strict";
import test from "node:test";
import { restoreWindow } from "../src/window-state.js";
import { notificationRoute } from "../src/contract.cjs";

const display = { x: 0, y: 0, width: 1920, height: 1080 };

test("window restore clamps sizes and rejects offscreen coordinates", () => {
  assert.deepEqual(restoreWindow({ x: 100, y: 100, width: 1200, height: 800, maximized: true }, [display]), { x: 100, y: 100, width: 1200, height: 800, maximized: true });
  assert.deepEqual(restoreWindow({ x: 5000, y: 100, width: 1200, height: 800 }, [display]), { width: 1200, height: 800, maximized: false });
  assert.equal(restoreWindow({ width: -1, height: Infinity }, [display]).width, 1280);
  assert.equal(restoreWindow(null, [display]).maximized, false);
});

test("notification routes cannot leave the hub", () => {
  assert.equal(notificationRoute("/s/computer/session?work=1"), "/s/computer/session?work=1");
  assert.equal(notificationRoute("https://evil.test"), undefined);
  assert.equal(notificationRoute("//evil.test"), undefined);
});
