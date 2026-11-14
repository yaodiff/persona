import assert from "node:assert/strict";
import test from "node:test";
import { externalBrowserURL, hubRoute, inHub, parseDeepLink, parseHubAddress } from "../src/urls.js";

test("hub root and reverse-proxy subpath are preserved", () => {
  assert.equal(parseHubAddress("https://hub.example/persona").url.href, "https://hub.example/persona/");
  assert.equal(parseHubAddress("hub.example/persona/").prefix, "/persona/");
  assert.equal(hubRoute(parseHubAddress("https://hub.example/persona"), "/s/computer/session"), "https://hub.example/persona/s/computer/session");
  assert.equal(inHub(parseHubAddress("https://hub.example/persona"), "https://hub.example/persona/api/v1/ws"), true);
  assert.equal(inHub(parseHubAddress("https://hub.example/persona"), "https://hub.example/other"), false);
  assert.equal(inHub(parseHubAddress("https://hub.example/persona"), "https://hub.example.evil.test/persona/"), false);
});

test("hub addresses reject credentials, fragments and arbitrary schemes", () => {
  for (const input of ["file:///tmp/a", "javascript:alert(1)", "https://u:p@hub.example/", "https://hub.example/#a", "https://hub.example/a b", ""]) {
    assert.throws(() => parseHubAddress(input), input);
  }
});

test("external links are restricted to HTTP(S) without credentials", () => {
  assert.equal(externalBrowserURL("https://example.com/a"), "https://example.com/a");
  assert.equal(externalBrowserURL("file:///tmp/a"), undefined);
  assert.equal(externalBrowserURL("javascript:alert(1)"), undefined);
  assert.equal(externalBrowserURL("https://u:p@example.com/"), undefined);
});

test("deep links never auto-pair or navigate to an arbitrary origin", () => {
  assert.deepEqual(parseDeepLink("persona://open?route=%2Fs%2Fcomputer%2Fsession"), { kind: "open", url: "persona://open?route=%2Fs%2Fcomputer%2Fsession", route: "/s/computer/session" });
  assert.equal(parseDeepLink("persona://link?url=https%3A%2F%2Fhub.example")?.kind, "pair-link");
  assert.equal(parseDeepLink("persona://pair?token=temporary")?.kind, "pair-app");
  assert.equal(parseDeepLink("persona://open?route=//evil.test"), undefined);
  assert.equal(parseDeepLink("persona://open?route=https://evil.test"), undefined);
  assert.equal(parseDeepLink("file:///tmp/a"), undefined);
});
