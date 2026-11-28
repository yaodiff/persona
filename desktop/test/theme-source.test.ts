import assert from "node:assert/strict";
import test from "node:test";
import { applyDesktopThemeSource, parseDesktopThemeSource } from "../src/theme-source.js";

test("parseDesktopThemeSource accepts system/light/dark and rejects untrusted values", () => {
  assert.equal(parseDesktopThemeSource("system"), "system");
  assert.equal(parseDesktopThemeSource("light"), "light");
  assert.equal(parseDesktopThemeSource("dark"), "dark");

  for (const invalid of ["", "auto", "DARK", null, undefined, 123, {}, []]) {
    assert.equal(parseDesktopThemeSource(invalid), undefined);
  }
});

test("applyDesktopThemeSource updates nativeTheme target and refreshes window surface", () => {
  const target: { themeSource: "system" | "light" | "dark" } = { themeSource: "system" };
  let refreshes = 0;

  assert.equal(applyDesktopThemeSource(target, "dark", () => { refreshes += 1; }), true);
  assert.equal(target.themeSource, "dark");
  assert.equal(refreshes, 1);

  assert.equal(applyDesktopThemeSource(target, "dark", () => { refreshes += 1; }), false);
  assert.equal(target.themeSource, "dark");
  assert.equal(refreshes, 2);

  assert.equal(applyDesktopThemeSource(target, "light", () => { refreshes += 1; }), true);
  assert.equal(target.themeSource, "light");
  assert.equal(refreshes, 3);
});
