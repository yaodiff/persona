// UI version 10 as the web reads it (docs/extensions/extensions.md "UI version 10", #9727): a view's `graph`
// component — nodes, edges, folds and the node drawn as selected — from the schema's examples; what it cannot use is
// left out, and the component is then drawn as a graph without it. The layout itself is lib/graph-layout.test.ts.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { isGraphLink, readView, UI_VERSION, type UIGraphNode } from "./extension-ui";

const example = (name: string) => JSON.parse(readFileSync(new URL(`../../../schema/extension/examples/View.v10-${name}.json`, import.meta.url), "utf8"));
const graphOf = (raw: unknown) => readView({ sections: [{ items: [raw] }] }).sections[0]?.items[0] as
  { type: string; nodes?: UIGraphNode[]; edges?: { from: string; to: string }[]; folded?: string[]; layout?: string; direction?: string; selected?: string; key?: string };

describe("UI 10: reading a view's graph", () => {
  it("is the version the web sends", () => {
    expect(UI_VERSION).toBe(13);
  });

  it("keeps every node, edge, fold and selection of the examples", () => {
    const work = graphOf(example("work-tree").sections[0].items[1]);
    expect(work.type).toBe("graph");
    expect(work.key).toBe("work");
    expect(work.nodes).toHaveLength(12);
    expect(work.nodes![2]).toMatchObject({ id: "build", title: "Build", tone: "positive", badge: { text: "Done", tone: "positive" }, action: { id: "open", values: { id: "build" } } });
    expect(work.nodes![3]).toMatchObject({ id: "build.small", title: "Smallest change", group: "Two ways", color: "green" });
    expect(work.nodes![5]).toMatchObject({ id: "tests", subtitle: "2 of 3 suites", progress: 0.66, icon: "check" });
    expect(work.edges).toHaveLength(11);
    expect(work.folded).toEqual(["docs"]);
    expect(work.selected).toBe("tests");
    expect(work.layout).toBeUndefined(); // tree is the default: not carried
    const plan = graphOf(example("plan-layered").sections[0].items[0]);
    expect(plan.layout).toBe("layered");
    expect(plan.direction).toBe("down");
    expect(plan.nodes).toHaveLength(6);
  });

  it("leaves out what does not read: nodes without a title, edges naming an unknown node or itself, repeats", () => {
    const g = graphOf({ type: "graph", nodes: [
      { id: "a", title: "A" }, { id: "b", title: "B" }, { id: "c" }, { title: "no id" }, "junk",
      { id: "a", title: "A again" },
    ], edges: [
      { from: "a", to: "b" }, { from: "a", to: "b" }, { from: "a", to: "a" }, { from: "a", to: "nope" }, { from: "nope", to: "b" }, 7,
    ], selected: "nope", folded: ["nope", "a", "b"], direction: "sideways", layout: "circle" });
    expect(g.nodes?.map((n) => n.id)).toEqual(["a", "b"]);
    expect(g.edges).toEqual([{ from: "a", to: "b" }]);
    expect(g.selected).toBeUndefined();
    expect(g.folded).toEqual(["a", "b"]); // an id that is gone is dropped, the person's folds are kept
    expect(g.direction).toBeUndefined();
    expect(g.layout).toBeUndefined();
  });

  it("keeps a node's fields only as they read, and an unknown tone is no tone", () => {
    const g = graphOf({ type: "graph", nodes: [
      { id: "a", title: "A", tone: "positive" },
      { id: "b", title: "B", color: "teal", tone: "danger", progress: 1.5, icon: 3, badge: { text: "" }, subtitle: 9 },
      { id: "c", title: "C", tone: "sparkly", color: "not-a-color", progress: 0.4, group: "" },
    ] });
    expect(g.nodes).toEqual([
      { id: "a", title: "A", tone: "positive" },
      { id: "b", title: "B", color: "teal", tone: "danger", progress: 1.5 },
      { id: "c", title: "C", progress: 0.4 },
    ]);
  });

  it("a graph with no node that reads is left out of the view", () => {
    expect(readView({ sections: [{ items: [{ type: "graph", nodes: [] }, { type: "text", text: "hi" }] }] }).sections[0].items.map((i) => i.type)).toEqual(["text"]);
    expect(readView({ sections: [{ items: [{ type: "graph", nodes: [{ id: "a", title: "A" }] }] }] }).sections[0].items[0].nodes).toHaveLength(1);
  });
});

describe("UI 10: a node's inspector fields", () => {
  const one = (node: Record<string, unknown>) => graphOf({ type: "graph", nodes: [{ id: "a", title: "A" }, { ...node, id: "b" }] }).nodes![1];

  it("keeps a started and ended time, and an ended time is dropped before its start", () => {
    expect(one({ id: "b", title: "A", started_at: 10, ended_at: 20 })).toEqual({ id: "b", title: "A", started_at: 10, ended_at: 20 });
    expect(one({ id: "b", title: "A", ended_at: 20 })).toEqual({ id: "b", title: "A", ended_at: 20 }); // no start: nothing to compare
    expect(one({ id: "b", title: "A", started_at: 20, ended_at: 10 })).toEqual({ id: "b", title: "A", started_at: 20 }); // before its start
    expect(one({ id: "b", title: "A", started_at: -1, ended_at: Infinity })).toEqual({ id: "b", title: "A" });
    expect(one({ id: "b", title: "A", started_at: "5", ended_at: NaN })).toEqual({ id: "b", title: "A" });
  });

  it("keeps only facts with a value that reads, its label under 200 and the first eight", () => {
    expect(one({ id: "b", title: "A", facts: [{ label: "Owner", value: "me", icon: "user" }, { label: "x".repeat(201), value: "v" }, { value: "" }, "junk", { value: 7 }] })).toEqual({
      id: "b", title: "A", facts: [{ label: "Owner", value: "me", icon: "user" }, { value: "v" }],
    });
    const many = Array.from({ length: 12 }, (_, i) => ({ label: `L${i}`, value: `v${i}` }));
    expect(one({ id: "b", title: "A", facts: many }).facts).toHaveLength(8);
    expect(one({ id: "b", title: "A", facts: [].concat([]) }).facts).toBeUndefined();
    // a value of at most 2000 characters reads; one longer is dropped
    expect(one({ id: "b", title: "A", facts: [{ value: "v".repeat(2000) }] }).facts).toHaveLength(1);
    expect(one({ id: "b", title: "A", facts: [{ value: "v".repeat(2001) }] }).facts).toBeUndefined();
  });

  it("keeps a detail, cut to 2000, and drops an empty one", () => {
    expect(one({ id: "b", title: "A", detail: "hello" }).detail).toBe("hello");
    expect(one({ id: "b", title: "A", detail: "x".repeat(2500) }).detail).toHaveLength(2000);
    expect(one({ id: "b", title: "A", detail: "" }).detail).toBeUndefined();
    expect(one({ id: "b", title: "A", detail: 3 }).detail).toBeUndefined();
  });

  it("keeps credential-free https links only, first eight", () => {
    const url = (u: string) => ({ title: "Work", url: u });
    expect(one({ id: "b", title: "A", links: [url("https://example.com/s/1")] }).links).toEqual([{ title: "Work", url: "https://example.com/s/1" }]);
    // http, credentials, an empty title, a too-long title, a too-long url and junk are left out
    expect(one({ id: "b", title: "A", links: [
      url("http://example.com"), url("https://me:secret@example.com"), { title: "", url: "https://example.com" },
      { title: "t".repeat(81), url: "https://example.com" }, url(`https://example.com/${"p".repeat(2000)}`), "junk", { title: "no url" },
    ] }).links).toBeUndefined();
    const many = Array.from({ length: 12 }, (_, i) => ({ title: `T${i}`, url: `https://example.com/${i}` }));
    expect(one({ id: "b", title: "A", links: many }).links).toHaveLength(8);
    expect(one({ id: "b", title: "A", links: [] }).links).toBeUndefined();
  });

  it("isGraphLink accepts only a credential-free https url with a host", () => {
    expect(isGraphLink("https://example.com/a")).toBe(true);
    expect(isGraphLink("http://example.com")).toBe(false);
    expect(isGraphLink("https://me:pw@example.com")).toBe(false);
    expect(isGraphLink("https://")).toBe(false);
    expect(isGraphLink("not a url")).toBe(false);
    expect(isGraphLink(undefined)).toBe(false);
  });

  it("counts Unicode code points for facts, detail and link titles without splitting surrogates", () => {
    const emoji = "🚀"; // 1 code point, 2 UTF-16 code units
    const node = one({
      id: "b",
      title: "A",
      facts: [{ label: emoji.repeat(200), value: emoji.repeat(1500) }],
      detail: "a" + emoji.repeat(2000),
      links: [{ title: emoji.repeat(80), url: "https://example.com/s" }],
    });
    expect(node.facts).toHaveLength(1);
    expect(Array.from(node.detail!)).toHaveLength(2000);
    expect(node.detail!.endsWith("\uD83D")).toBe(false); // never cut mid-surrogate
    expect(node.links).toHaveLength(1);
  });

  it("keeps every rich field of the view's example node, together", () => {
    const node = one({ id: "b", title: "A", started_at: 1, ended_at: 2, facts: [{ label: "Mod", value: "small" }], detail: "why", links: [{ title: "Session", url: "https://example.com/s" }] });
    expect(node).toEqual({ id: "b", title: "A", started_at: 1, ended_at: 2, facts: [{ label: "Mod", value: "small" }], detail: "why", links: [{ title: "Session", url: "https://example.com/s" }] });
  });
});
