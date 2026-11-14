import { describe, expect, it } from "vitest";
import { conditionValues, dropConditions, toggleCondition } from "./agent-search";

describe("search condition aliases", () => {
  it("reads pin as the canonical pinned value", () => {
    expect(conditionValues("is:pin", "is")).toEqual(["pinned"]);
  });

  it.each([
    ["is:pin", "is", "pinned", false],
    ["host:box", "machine", "box", false],
    ["repo:/work", "project", "/work", false],
    ["status:working", "state", "working", false],
    ["after:7d", "since", "7d", true],
  ])("toggles %s off instead of duplicating its canonical operator", (query, key, value, only) => {
    expect(toggleCondition(query, key, value, only)).toBe("");
  });

  it("drops every key alias without removing other operators", () => {
    expect(dropConditions("repo:/work status:working after:7d", "project")).toBe("status:working after:7d");
    expect(dropConditions("host:box after:7d", "since")).toBe("host:box");
  });
});
