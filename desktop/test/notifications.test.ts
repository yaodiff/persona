import assert from "node:assert/strict";
import test from "node:test";
import { notificationRoute } from "../src/contract.cjs";
import { parseHubAddress } from "../src/urls.js";

test("native notification routes preserve the hub's reverse-proxy prefix", () => {
  const hub = parseHubAddress("https://hub.example/persona");
  const clean = notificationRoute("/persona/s/computer/session?view=chat");
  assert.ok(clean);
  const target = new URL(clean, hub.url);
  assert.equal(target.href, "https://hub.example/persona/s/computer/session?view=chat");
  assert.equal(notificationRoute("https://other.example/persona/s/computer/session"), undefined);
});
