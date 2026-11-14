import assert from "node:assert/strict";
import test from "node:test";
import { appPairingURL } from "../src/pairing.js";
import { parseHubAddress } from "../src/urls.js";
import { NEEDS_YOU_OVERLAY } from "../src/badge-art.js";

const hub = parseHubAddress("https://hub.example/persona");

test("a same-hub app pairing link goes through the existing web sign-in", () => {
  const pairing = appPairingURL(hub, "persona://pair?url=https%3A%2F%2Fhub.example%3A443%2Fpersona&token=jp_example&fp=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA");
  assert.equal(pairing, "https://hub.example/persona/?token=jp_example");
});

test("an external pairing link cannot hand a token to a different origin", () => {
  assert.equal(appPairingURL(hub, "persona://pair?url=https%3A%2F%2Fother.example&token=jp_example"), undefined);
  assert.equal(appPairingURL(hub, "persona://pair?url=https%3A%2F%2Fhub.example&token="), undefined);
  assert.equal(appPairingURL(hub, "https://hub.example/?token=jp_example"), undefined);
});

test("the Windows overlay is a visible local asset", () => {
  assert.ok(NEEDS_YOU_OVERLAY.startsWith("data:image/svg+xml;base64,"));
  const svg = Buffer.from(NEEDS_YOU_OVERLAY.split(",")[1]!, "base64").toString("utf8");
  assert.match(svg, /<circle[^>]*fill="#df3f3f"/);
});
