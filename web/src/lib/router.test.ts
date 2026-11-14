import { describe, expect, it } from "vitest";
import { parse, showsSession } from "./router";

describe("showsSession", () => {
  it("knows an agent's chat by its own address and by its tab's (#3405)", () => {
    expect(showsSession(parse("/s/mac/s1"), "mac", "s1")).toBe(true);
    expect(showsSession(parse("/t/mac/space/slot/s1"), "mac", "s1")).toBe(true);
    expect(showsSession(parse("/s/mac/s2"), "mac", "s1")).toBe(false);
    expect(showsSession(parse("/t/other/space/slot/s1"), "mac", "s1")).toBe(false);
    expect(showsSession(parse("/t/mac/space/slot"), "mac", "s1")).toBe(false);
    expect(showsSession(parse("/todos"), "mac", "s1")).toBe(false);
  });
});

it("opens a request by its own id without inventing a session", () => {
  expect(parse("/requests/request-one")).toEqual({ name: "request", id: "request-one" });
  expect(showsSession(parse("/requests/request-one"), "example-computer", "")).toBe(false);
});

describe("a Persona chat opened at a message (#6283)", () => {
  it("reads ?at= and writes it back", async () => {
    const { href } = await import("./router");
    const r = parse("/persona/c1", "?machine=server&at=i1%230");
    expect(r).toEqual({ name: "persona", sid: "c1", mid: "server", at: "i1#0" });
    expect(href(r)).toBe("/persona/c1?machine=server&at=i1%230");
    expect(parse("/persona/c1", "")).toEqual({ name: "persona", sid: "c1" });
  });
});

describe("an address that is no page (web usability audit item 42)", () => {
  it("is not found, keeping the address as it came, instead of Overview", async () => {
    const { routePath } = await import("./router");
    expect(parse("/foo-bar-unknown")).toEqual({ name: "not-found", path: "/foo-bar-unknown" });
    expect(parse("/foo", "?x=1")).toEqual({ name: "not-found", path: "/foo?x=1" });
    expect(parse("/foo", "x=1")).toEqual({ name: "not-found", path: "/foo?x=1" });
    // a link missing the part that names the thing
    for (const path of ["/s/mac", "/t/mac/space", "/requests", "/space/mac", "/extension/x"]) expect(parse(path).name).toBe("not-found");
    expect(routePath(parse("/foo-bar-unknown"))).toBe("/foo-bar-unknown");
  });

  it("leaves every page's own address alone", () => {
    expect(parse("/")).toEqual({ name: "persona" });
    expect(parse("/overview")).toEqual({ name: "home" });
    for (const path of ["/inbox", "/inbox/nope", "/kb", "/kb/x/y", "/todos", "/settings", "/machines", "/machines/nope", "/a", "/a/nope", "/agents", "/agents/archived", "/history", "/health", "/routines", "/messages", "/assistant", "/terminal", "/subscriptions", "/workspace-board", "/plugin/x/y", "/persona/new"]) {
      expect(parse(path).name, path).not.toBe("not-found");
    }
  });
});
