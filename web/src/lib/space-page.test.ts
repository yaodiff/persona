import { describe, expect, it } from "vitest";
import { href, parse, type Route } from "./router";
import { spaceTodos, spaceView } from "./space-page";
import type { Todo } from "./todos";
import type { Session } from "./types";

let n = 0;
const agent = (state: Session["state"], o: Partial<Session> = {}): Session => ({
  machine_id: "mac",
  session_id: `s${++n}`,
  title: `agent ${n}`,
  cwd: "/code/app",
  status: "idle",
  state,
  space_id: "w1",
  space_name: "app",
  updated_at: `2026-11-05T10:0${n % 10}:00Z`,
  ...o,
});
const todo = (id: string, o: Partial<Todo> = {}): Todo => ({ id, title: `todo ${id}`, created_at: "", updated_at: "", ...o });

describe("space route (/space/<mid>/<space>)", () => {
  it("round-trips and encodes both segments", () => {
    const routes: Route[] = [
      { name: "space", mid: "mac", space: "app" },
      { name: "space", mid: "my mac", space: "dir:/a b/c" },
    ];
    for (const r of routes) expect(parse(href(r))).toEqual(r);
    expect(href({ name: "space", mid: "m", space: "a/b" })).toBe("/space/m/a%2Fb");
  });

  it("needs the machine and the space", () => {
    expect(parse("/space/mac").name).toBe("not-found");
  });
});

describe("spaceView", () => {
  it("lists the space's running agents by name on the machine, most urgent first", () => {
    const idle = agent("idle");
    const needs = agent("needs_you");
    const working = agent("working");
    const other = agent("needs_you", { space_id: "w2", space_name: "lib" });
    const elsewhere = agent("needs_you", { machine_id: "pc" });
    const assistant = agent("needs_you", { assistant: true });
    const gone = agent("offline");
    const v = spaceView([idle, needs, working, other, elsewhere, assistant, gone], "mac", "app");
    expect(v.live).toEqual([needs, working, idle]);
    expect(v.live).not.toContain(other);
    expect(v.live).not.toContain(elsewhere);
    expect(v.live).not.toContain(assistant);
    expect(v.recent).toEqual([gone]);
    expect(v.counts).toEqual({ needs: 1, working: 1, unread: 0 });
    expect(v.first).toBe(needs);
    expect(v.cwd).toBe("/code/app");
  });

  it("finds a space without a name by its id, and nothing outside a space", () => {
    const s = agent("idle", { space_name: undefined, space_id: "w9" });
    const loose = agent("idle", { space_id: undefined, space_name: "app" });
    expect(spaceView([s, loose], "mac", "w9").live).toEqual([s]);
    expect(spaceView([s, loose], "mac", "app").live).toEqual([]);
  });

  it("is empty for a space that is gone", () => {
    const v = spaceView([agent("idle")], "mac", "nope");
    expect(v.first).toBeUndefined();
    expect(v.live).toEqual([]);
  });
});

describe("spaceTodos", () => {
  it("puts the space's own todo first, then its agents', each once", () => {
    const a = agent("working", { todo: { id: "t2", title: "old title" } });
    const b = agent("idle", { todo: { id: "t2", title: "old title" } });
    const c = agent("idle", { todo: { id: "t1", title: "x" } });
    const todos = [todo("t1", { spaces: [{ machine_id: "mac", space: "app", linked_at: "" }] }), todo("t2")];
    const out = spaceTodos(todos, [a, b, c], "mac", "app");
    expect(out.map((x) => [x.id, x.via, x.title])).toEqual([
      ["t1", "space", "todo t1"],
      ["t2", "agent", "todo t2"],
    ]);
  });

  it("skips a finished todo of the space and keeps an agent's todo the list doesn't have", () => {
    const todos = [todo("t1", { done_at: "2026-11-01", spaces: [{ machine_id: "mac", space: "app", linked_at: "" }] })];
    const out = spaceTodos(todos, [agent("idle", { todo: { id: "t9", title: "elsewhere" } })], "mac", "app");
    expect(out).toEqual([{ id: "t9", title: "elsewhere", via: "agent", todo: undefined }]);
  });
});
