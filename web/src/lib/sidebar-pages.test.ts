// @vitest-environment jsdom
// The sidebar's pages as arranged in Settings › General › Sidebar pages: the order, the list and More, the defaults for new pages.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { arrangePages, EXTENSION_CAP, getSidebarPrefs, inList, movePage, ordered, parsePrefs, resetSidebarPrefs, setPageOrder, setPageShown, type SidebarPrefs } from "./sidebar-pages";

const NONE: SidebarPrefs = { order: [], shown: [], hidden: [] };
const pages = (...ks: string[]) => ks.map((k) => ({ k }));
const keys = (ps: { k: string }[]) => ps.map((p) => p.k);

beforeEach(() => localStorage.clear());
afterEach(() => {
  vi.restoreAllMocks();
  resetSidebarPrefs();
});

describe("ordered", () => {
  it("keeps the default order when nothing was chosen", () => {
    expect(ordered(["home", "inbox", "settings"], [])).toEqual(["home", "inbox", "settings"]);
  });
  it("follows the user's order and drops pages that are gone", () => {
    expect(ordered(["home", "inbox", "kb", "settings"], ["kb", "gone", "home"])).toEqual(["inbox", "kb", "home", "settings"]);
  });
  it("puts a page the order does not know right before the one that follows it by default", () => {
    // a new extension's page comes before Settings by default, wherever the user moved the others
    expect(ordered(["home", "kb", "p:new", "settings"], ["kb", "home", "settings"])).toEqual(["kb", "home", "p:new", "settings"]);
    expect(ordered(["fresh", "home", "settings"], ["settings", "home"])).toEqual(["settings", "fresh", "home"]);
    // last by default: last
    expect(ordered(["home", "settings", "late"], ["settings", "home"])).toEqual(["settings", "home", "late"]);
  });
});

describe("inList", () => {
  it("lists the built-in pages and the first extension pages, the rest wait under 更多", () => {
    const ext = Array.from({ length: EXTENSION_CAP + 2 }, (_, i) => `p:e${i}/page`);
    const listed = inList(["home", ...ext, "settings"], NONE);
    expect(listed.has("home") && listed.has("settings")).toBe(true);
    expect(ext.filter((k) => listed.has(k))).toEqual(ext.slice(0, EXTENSION_CAP));
  });
  it("follows the user's choices over the defaults, and the cap counts the extension pages shown by choice", () => {
    const ks = ["home", "p:a/x", "p:b/x", "p:c/x", "p:d/x", "settings"];
    const listed = inList(ks, { order: [], shown: ["p:d/x"], hidden: ["home", "p:a/x"] });
    // p:d is shown by choice (1), then p:b and p:c fill the cap of 3 by default
    expect([...listed]).toEqual(["p:b/x", "p:c/x", "p:d/x", "settings"]);
    expect(listed.has("home")).toBe(false);
    expect(listed.has("p:a/x")).toBe(false);
  });
  it("never puts 设置 away", () => {
    expect(inList(["home", "settings"], { order: [], shown: [], hidden: ["settings"] }).has("settings")).toBe(true);
  });
});

describe("arrangePages", () => {
  it("splits into the list and 更多, both in the user's order", () => {
    const { shown, more } = arrangePages(pages("home", "inbox", "health", "kb", "settings"), { order: ["kb", "home"], shown: [], hidden: ["health", "inbox"] });
    expect(keys(shown)).toEqual(["kb", "home", "settings"]);
    expect(keys(more)).toEqual(["inbox", "health"]);
  });
});

describe("the stored choices", () => {
  it("reads back what was set, and a broken value is none", () => {
    setPageShown("health", false);
    setPageShown("p:a/x", true);
    setPageOrder(["kb", "home"]);
    expect(getSidebarPrefs()).toEqual({ order: ["kb", "home"], shown: ["p:a/x"], hidden: ["health"] });
    setPageShown("health", true);
    expect(getSidebarPrefs().hidden).toEqual([]);
    expect(getSidebarPrefs().shown).toEqual(["p:a/x", "health"]);
    expect(parsePrefs("{not json")).toEqual(NONE);
    expect(parsePrefs(JSON.stringify({ order: [1, "home"], shown: "x" }))).toEqual({ order: ["home"], shown: [], hidden: [] });
  });
  it("ignores putting 设置 away", () => {
    setPageShown("settings", false);
    expect(getSidebarPrefs()).toEqual(NONE);
  });
  it("moves a page one place, and stops at the ends", () => {
    movePage(["home", "inbox", "kb"], "kb", -1);
    expect(getSidebarPrefs().order).toEqual(["home", "kb", "inbox"]);
    movePage(["home", "kb", "inbox"], "home", -1);
    expect(getSidebarPrefs().order).toEqual(["home", "kb", "inbox"]);
  });
  it("keeps hide, reorder, unhide, and reset in memory while storage writes fail", () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("storage blocked"); });
    const removeItem = vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => { throw new Error("storage blocked"); });
    setPageShown("health", false);
    expect(getSidebarPrefs().hidden).toEqual(["health"]);
    setPageOrder(["kb", "home"]);
    expect(getSidebarPrefs().order).toEqual(["kb", "home"]);
    setPageShown("health", true);
    expect(getSidebarPrefs().hidden).toEqual([]);
    expect(getSidebarPrefs().shown).toContain("health");
    resetSidebarPrefs();
    expect(getSidebarPrefs()).toEqual(NONE);

    setItem.mockRestore();
    removeItem.mockRestore();
    setPageShown("health", false);
    expect(getSidebarPrefs().hidden).toEqual(["health"]);
    expect(parsePrefs(localStorage.getItem("sidebar.pages")).hidden).toEqual(["health"]);
  });

  it("resets to the defaults", () => {
    setPageShown("health", false);
    resetSidebarPrefs();
    expect(getSidebarPrefs()).toEqual(NONE);
    expect(localStorage.getItem("sidebar.pages")).toBeNull();
  });
});
