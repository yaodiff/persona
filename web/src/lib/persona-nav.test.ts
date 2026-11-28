import { describe, expect, it } from "vitest";
import { firstAndCurrent, NAV_PAGES, extensionNavKey, sidebarActiveKey, sidebarRoute } from "./persona-nav";
import { href, parse, routePath } from "./router";

describe("Persona is the front door (#1239)", () => {
  it("opens the main conversation at / and at the old /persona and /persona links", () => {
    expect(parse("/")).toEqual({ name: "persona" });
    expect(parse("/persona")).toEqual({ name: "persona", sid: undefined });
    expect(parse("/persona")).toEqual({ name: "persona", sid: undefined });
    expect(href({ name: "persona" })).toBe("/");
  });

  it("keeps one conversation's link and moves the one-off new conversation to /persona/new", () => {
    expect(parse("/persona/abc")).toEqual({ name: "persona", sid: "abc" });
    expect(href({ name: "persona", sid: "abc" })).toBe("/persona/abc");
    expect(parse("/persona/new")).toEqual({ name: "persona-new" });
    expect(parse("/persona/new")).toEqual({ name: "persona-new" });
    expect(href({ name: "persona-new" })).toBe("/persona/new");
  });

  it("moves 总览 to /overview; an unknown path says it is no page (web usability audit item 42)", () => {
    expect(parse("/overview")).toEqual({ name: "home" });
    expect(href({ name: "home" })).toBe("/overview");
    expect(parse("/no-such-page")).toEqual({ name: "not-found", path: "/no-such-page" });
  });

  it("keeps the other old links", () => {
    expect(parse("/s/mac/abc")).toEqual({ name: "session", mid: "mac", sid: "abc" });
    expect(parse("/history")).toEqual({ name: "history" });
    expect(parse("/history/inbox")).toEqual({ name: "history", origin: "inbox" });
    expect(routePath({ name: "history", origin: "task" })).toBe("/history/task");
    expect(parse("/inbox/x")).toEqual({ name: "inbox", id: "x" });
    expect(parse("/machines/mac")).toEqual({ name: "machine", mid: "mac" });
  });

  it("lights the Persona row for the main conversation however it is reached", () => {
    expect(sidebarActiveKey({ name: "persona" }, "main")).toBe("persona");
    expect(sidebarActiveKey({ name: "persona", sid: "main" }, "main")).toBe("persona");
    expect(sidebarActiveKey({ name: "persona", sid: "other" }, "main")).toBe("j:other");
    expect(sidebarActiveKey({ name: "persona" }, undefined)).toBe("persona");
    expect(sidebarActiveKey({ name: "persona-new" }, "main")).toBe("j:new");
    expect(sidebarActiveKey({ name: "history" }, "main")).toBe("history");
    expect(sidebarActiveKey({ name: "history", origin: "inbox" }, "main")).toBe("auto:inbox");
    expect(sidebarActiveKey({ name: "home" }, "main")).toBe("home");
    expect(sidebarActiveKey({ name: "machine", mid: "mac" }, "main")).toBe("machines");
    expect(sidebarActiveKey({ name: "session", mid: "mac", sid: "s" }, "main")).toBe("s:mac/s");
    expect(sidebarActiveKey({ name: "terminal" }, "main")).toBe("terminal");
    expect(sidebarActiveKey({ name: "terminal", mid: "mac", id: "pane:w1:p2" }, "main")).toBe("term:mac/pane:w1:p2");
  });

  it("keeps the row of the page an Agent's page was opened from, a drill-down (#12055, #9971)", () => {
    const agent = { name: "session", mid: "mac", sid: "s" } as const;
    // a to-do's Agent rows and hand-off, a knowledge project's Agents and a meeting's hand-off, the workflow board
    expect(sidebarActiveKey(agent, "main", "/todos?id=t1")).toBe("todos");
    expect(sidebarActiveKey(agent, "main", "/todos?view=calendar&mode=week&id=t1")).toBe("todos");
    expect(sidebarActiveKey(agent, "main", "/kb/projects/p1")).toBe("kb");
    expect(sidebarActiveKey(agent, "main", "/kb/recordings/r1")).toBe("kb");
    expect(sidebarActiveKey(agent, "main", "/workspace-board")).toBe("workspace-board");
    // a Persona conversation's card with How Agents open = Full page: the conversation's own row
    expect(sidebarActiveKey(agent, "main", "/")).toBe("persona");
    expect(sidebarActiveKey(agent, "main", "/persona/main")).toBe("persona");
    expect(sidebarActiveKey(agent, "main", "/persona/c2?machine=mac")).toBe("j:c2");
    // an Agent opened from another Agent's page keeps that Agent's row
    expect(sidebarActiveKey(agent, "main", "/s/mac/first")).toBe("s:mac/first");
    // a tab's page follows where it came from too
    expect(sidebarActiveKey({ name: "tab", mid: "mac", space: "w", slot: "1", sid: "s" }, "main", "/todos?id=t1")).toBe("todos");
    expect(sidebarRoute(agent, "/todos?id=t1")).toEqual({ name: "todos", id: "t1" });
  });

  it("lights an Agent's own row when nothing opened it, and every other page its own whatever it carries", () => {
    const agent = { name: "session", mid: "mac", sid: "s" } as const;
    expect(sidebarActiveKey(agent, "main")).toBe("s:mac/s");
    expect(sidebarActiveKey(agent, "main", undefined)).toBe("s:mac/s");
    expect(sidebarRoute(agent)).toBe(agent);
    // only an Agent's page is a drill-down here: a to-do opened from a conversation lights To-dos, not the conversation
    expect(sidebarActiveKey({ name: "todos", id: "t1" }, "main", "/persona/c2")).toBe("todos");
    expect(sidebarActiveKey({ name: "persona", sid: "c2" }, "main", "/todos")).toBe("j:c2");
  });

  it("shortens a list to its first n and the open item after them (#2283)", () => {
    const isFour = (x: number) => x === 4;
    expect(firstAndCurrent([1, 2, 3, 4, 5], 2, isFour)).toEqual([1, 2, 4]);
    expect(firstAndCurrent([1, 2, 3, 4, 5], 5, isFour)).toEqual([1, 2, 3, 4, 5]);
    expect(firstAndCurrent([1, 2, 3], 2, isFour)).toEqual([1, 2]);
  });

  it("orders the pages under Persona like the app", () => {
    // the feature modules' pages (the health module's after Inbox) are placed by the sidebar (SidebarParts.tsx)
    expect(NAV_PAGES).toEqual(["home", "inbox", "starred", "workspace-board", "todos", "routines", "kb", "machines", "terminal", "settings"]);
  });
});

describe("an extension's page (docs/extensions/extensions.md「UI slots」 nav_page)", () => {
  it("has its own link and lights its own sidebar row", () => {
    expect(parse("/extension/example-gateway/usage")).toEqual({ name: "extension", extension: "example-gateway", ui: "usage" });
    expect(href({ name: "extension", extension: "a b", ui: "c/d" })).toBe("/extension/a%20b/c%2Fd");
    expect(parse(href({ name: "extension", extension: "a b", ui: "c/d" }))).toEqual({ name: "extension", extension: "a b", ui: "c/d" });
    expect(parse("/extension/only-one")).toEqual({ name: "not-found", path: "/extension/only-one" });
    expect(sidebarActiveKey({ name: "extension", extension: "g", ui: "usage" })).toBe(extensionNavKey("g", "usage"));
    expect(extensionNavKey("g", "usage")).toBe("p:g/usage");
  });
});
