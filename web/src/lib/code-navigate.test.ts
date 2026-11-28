import { describe, expect, it } from "vitest";
import { columnAfter, navClick, navigateValues, navKey, wordAt } from "./code-navigate";

const key = (k: string, mods: Partial<{ metaKey: boolean; ctrlKey: boolean; altKey: boolean; shiftKey: boolean }> = {}) => ({ key: k, metaKey: false, ctrlKey: false, altKey: false, shiftKey: false, ...mods });

describe("code navigate", () => {
  it("finds the name a column is in or just after", () => {
    const line = "\tfmt.Println(util.Greet(\"you\"))";
    expect(wordAt(line, 19)).toBe("Greet");
    expect(wordAt(line, 23)).toBe("Greet");
    expect(wordAt(line, 24)).toBe("Greet");
    expect(wordAt(line, 2)).toBe("fmt");
    expect(wordAt(line, 1)).toBe("");
    expect(wordAt("名前 := 値", 1)).toBe("名前");
    expect(wordAt("", 1)).toBe("");
  });
  it("counts columns in characters", () => {
    expect(columnAfter("😀a")).toBe(3);
  });
  it("sends where, the name and the intent", () => {
    expect(navigateValues("main.go", "definition", { line: 7, column: 20, text: "\tfmt.Println(util.Greet(\"you\"))" })).toEqual({ path: "main.go", line: "7", column: "20", word: "Greet", intent: "definition" });
    expect(navigateValues("main.go", "back")).toEqual({ path: "main.go", intent: "back" });
  });
  it("reads IntelliJ's keys on each platform", () => {
    expect(navKey(key("b", { metaKey: true }), true)).toBe("definition");
    expect(navKey(key("b", { ctrlKey: true }), true)).toBeNull();
    expect(navKey(key("B", { ctrlKey: true }), false)).toBe("definition");
    expect(navKey(key("F7", { altKey: true }), false)).toBe("references");
    expect(navKey(key("[", { metaKey: true }), true)).toBe("back");
    expect(navKey(key("ArrowLeft", { ctrlKey: true, altKey: true }), false)).toBe("back");
    expect(navKey(key("b", { metaKey: true, shiftKey: true }), true)).toBeNull();
    expect(navClick({ metaKey: true, ctrlKey: false, altKey: false, shiftKey: false }, true)).toBe(true);
    expect(navClick({ metaKey: false, ctrlKey: true, altKey: false, shiftKey: false }, true)).toBe(false);
  });
});
