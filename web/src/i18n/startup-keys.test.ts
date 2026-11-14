import { describe, expect, it } from "vitest";
import { startupMatcher } from "./startup-keys";

describe("startupMatcher", () => {
  const inStartup = startupMatcher(["common", "settings.avatar", "todos.stage_"]);

  it("takes a whole namespace", () => {
    expect(inStartup("common.ok")).toBe(true);
    expect(inStartup("commons.ok")).toBe(false);
  });

  it("takes every key that begins with a listed one: its plural forms and suffixes too", () => {
    for (const key of ["settings.avatar", "settings.avatar_one", "settings.avatar_hint", "todos.stage_plan", "todos.stage_pr"]) expect(inStartup(key), key).toBe(true);
    for (const key of ["settings.theme", "settings.avata", "todos.stage", "todos.kicker_plan", "other.avatar"]) expect(inStartup(key), key).toBe(false);
  });

  it("takes nothing from an empty list", () => {
    expect(startupMatcher([])("common.ok")).toBe(false);
  });
});
