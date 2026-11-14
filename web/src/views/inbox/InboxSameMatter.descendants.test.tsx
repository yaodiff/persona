// "Same matter as…" must not offer anything under this item, however deep the chain is: a parent that would make a loop
// is refused by the hub (409) and, without this, the picker would offer it over and over. The walk uses only the items the
// page already has (`parent`, kind inbox), never a request.
import { describe, expect, it } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import { sameMatterCandidates } from "./InboxSameMatter";

const item = (id: string, over: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "examplechat", kind: "group", account: "examplechat", account_name: "Example Chat", title: id, preview: "", count: 1, ...over,
});
const under = (id: string, parent: string, over: Partial<InboxItem> = {}) =>
  item(id, { manager: "m-1", parent: { kind: "inbox", id: parent }, ...over });
const ids = (list: InboxItem[]) => list.map((candidate) => candidate.id).sort();

describe("收件箱：是同一件事…排除所有下级", () => {
  // updated_at grows with the depth, so the order also shows the walk does not disturb sorting
  const at = (n: number) => `2026-01-01T00:00:0${n}Z`;
  const chain = [
    item("a", { manager: "m-1", updated_at: at(1) }),
    under("b", "a", { updated_at: at(2) }),
    under("c", "b", { updated_at: at(3) }),
    under("d", "c", { updated_at: at(4) }),
  ];
  const current = (id: string) => chain.find((it) => it.id === id)!;

  it.each([
    ["a", []],
    ["b", ["a"]],
    ["c", ["a", "b"]],
    ["d", ["a", "b", "c"]],
  ])("offers nothing under %s of a→b→c→d", (id, expected) => {
    expect(sameMatterCandidates(chain, current(id), []).map((candidate) => candidate.id).sort()).toEqual(expected);
  });

  it("finds a chain through a done item that is still in the list", () => {
    const items = [
      item("self", { manager: "m-1" }),
      under("done-mid", "self", { done_at: "2026-01-01T00:00:00Z" }),
      under("open-below", "done-mid"),
    ];
    expect(sameMatterCandidates(items, items[0], [])).toEqual([]);
  });

  it("does not read a parent id as another manager's item of the same id", () => {
    const items = [
      item("self", { manager: "m-1" }),
      // another manager's item whose id is what the m-1 candidate's parent names: not our ancestor
      item("mid", { manager: "m-2", parent: { kind: "inbox", id: "self" } }),
      under("candidate", "mid"),
    ];
    expect(ids(sameMatterCandidates(items, items[0], []))).toEqual(["candidate"]);
  });

  it("excludes a descendant even while searching for something it matches", () => {
    const items = [
      item("self", { manager: "m-1", updated_at: at(1) }),
      under("b", "self", { updated_at: at(2) }),
      under("c", "b", { title: "release", where: "发布群", updated_at: at(3) }),
      item("open", { manager: "m-1", title: "release elsewhere" }),
    ];
    expect(ids(sameMatterCandidates(items, items[0], [], "release"))).toEqual(["open"]);
  });

  it("excludes descendants that the tree does not list but parent links do", () => {
    const items = [
      item("self", { manager: "m-1" }),
      under("child", "self"),
      under("grandchild", "child"),
    ];
    // a stale tree naming no children must not bring the descendants back
    expect(sameMatterCandidates(items, items[0], [])).toEqual([]);
  });

  it("keeps its footing on a cycle that runs through this item", () => {
    const items = [
      item("self", { manager: "m-1", parent: { kind: "inbox", id: "b" } }),
      under("b", "a"),
      under("a", "self"),
      item("outside", { manager: "m-1" }),
    ];
    // a and b are both under self by the links; the walk must stop, not spin
    expect(ids(sameMatterCandidates(items, items[0], []))).toEqual(["outside"]);
  });

  it("keeps its footing on a self-parent and on a cycle outside this item", () => {
    const items = [
      item("self", { manager: "m-1" }),
      item("oops", { manager: "m-1", parent: { kind: "inbox", id: "oops" } }),
      item("x", { manager: "m-1", parent: { kind: "inbox", id: "y" } }),
      item("y", { manager: "m-1", parent: { kind: "inbox", id: "x" } }),
    ];
    expect(ids(sameMatterCandidates(items, items[0], []))).toEqual(["oops", "x", "y"]);
  });

  it("excludes a chain deeper than the hub would ever allow (its limit is 8)", () => {
    const items = [item("self", { manager: "m-1" })];
    for (let i = 0; i < 40; i++) items.push(under(`n${i}`, i === 0 ? "self" : `n${i - 1}`));
    expect(sameMatterCandidates(items, items[0], [])).toEqual([]);
  });

  it("ends a broken parent chain far longer than real data instead of walking forever", () => {
    const items = [item("self", { manager: "m-1" })];
    // a 2,000-long chain: impossible data, but it must not hang the picker
    for (let i = 0; i < 2000; i++) items.push(under(`n${i}`, i === 0 ? "self" : `n${i - 1}`));
    const started = Date.now();
    expect(Array.isArray(sameMatterCandidates(items, items[0], []))).toBe(true);
    expect(Date.now() - started).toBeLessThan(2000);
  });
});
