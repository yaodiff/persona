// A nav_page's icon on the web (#16281): every name the schema's NavIcon lists (schema/extension/view.schema.json, read
// from the file, so the web's list cannot drift from it again) draws the glyph a row draws for it, not the extension's;
// an unknown name still draws as the extension's. The sidebar, its icon rail, the palette's "Go to" and Settings ›
// Sidebar pages all take a page's icon from navIcon (views/SidebarParts.tsx useNavPages).
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { UI_ICONS, uiIcon } from "../../lib/extension-ui";
import { navIcon } from "./ExtensionSlots";
import { ROW_ICON } from "./icon-map";

const schema = JSON.parse(readFileSync(new URL("../../../../schema/extension/view.schema.json", import.meta.url), "utf8")) as {
  $defs: { NavIcon: { enum: string[] } };
};
const NAV_ICONS = schema.$defs.NavIcon.enum;
/** The default and its old name: the only names drawn as the extension's icon. */
const DEFAULT = ["extension", "plugin"];

describe("a nav_page's icon", () => {
  it("reads the schema's whole set", () => {
    expect(NAV_ICONS).toEqual(expect.arrayContaining(["chat", "person", "folder", "settings", "commit", "file", "info", ...DEFAULT]));
  });

  it("knows every name the schema allows, and map", () => {
    expect(NAV_ICONS.filter((name) => uiIcon(name) !== name)).toEqual([]);
    expect([...UI_ICONS].sort()).toEqual([...NAV_ICONS, "map"].sort());
  });

  it("draws every name but the default with its own glyph, the one a row draws", () => {
    const named = NAV_ICONS.filter((name) => !DEFAULT.includes(name));
    expect(named.filter((name) => navIcon(name) === "extension")).toEqual([]);
    expect(named.filter((name) => navIcon(name) !== ROW_ICON[name as keyof typeof ROW_ICON])).toEqual([]);
    expect(DEFAULT.map(navIcon)).toEqual(["extension", "extension"]);
    expect(navIcon("chat")).toBe("chat");
    expect(navIcon("settings")).toBe("gear");
  });

  it("shares the rows' table: one glyph for every name the schema allows", () => {
    expect(Object.keys(ROW_ICON).sort()).toEqual([...NAV_ICONS].sort());
  });

  it("keeps map", () => {
    expect(navIcon("map")).toBe("map");
  });

  it("draws an unknown name, or none, as the extension's", () => {
    expect(["rocket", "Chat", "chat ", "", undefined].map(navIcon)).toEqual(["extension", "extension", "extension", "extension", "extension"]);
  });
});
