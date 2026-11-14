import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import { rowMeta } from "../../lib/inbox-now";
import { sameMatterCandidates, SameMatterList } from "./InboxSameMatter";

const item = (id: string, title: string, over: Partial<InboxItem> & { where?: string } = {}): InboxItem => ({
  id, source: "examplechat", kind: "group", account: "examplechat", account_name: "Example Chat", title, preview: "", count: 1, ...over,
});

describe("收件箱：是同一件事…", () => {
  const current = item("self", "本件", { manager: "m-1" });
  const child = item("child", "自己的下级", { manager: "m-1" });
  const older = item("older", "Release checklist", { manager: "m-1", updated_at: "2026-01-01T00:00:05.1Z", where: "发布群", sender: "小甲" });
  const newer = item("newer", "另一件事", { manager: "m-1", updated_at: "2026-01-01T00:00:05.12Z", where: "设计评审" });
  const items = [
    newer,
    current,
    child,
    item("done", "已处理", { manager: "m-1", done_at: "2026-01-01T00:00:00Z" }),
    item("other-manager", "别的管理者", { manager: "m-2" }),
    older,
  ];

  it("offers only open items of this manager, excluding itself and its children, newest first", () => {
    expect(sameMatterCandidates(items, current, [child]).map((candidate) => candidate.id)).toEqual(["newer", "older"]);
  });

  it("searches title and where, not unrelated fields", () => {
    expect(sameMatterCandidates(items, current, [child], "release").map((candidate) => candidate.id)).toEqual(["older"]);
    expect(sameMatterCandidates(items, current, [child], "发布群").map((candidate) => candidate.id)).toEqual(["older"]);
    expect(sameMatterCandidates(items, current, [child], "小甲")).toEqual([]);
  });

  it("excludes every descendant, not only the direct child in the tree", () => {
    const a = item("a", "A", { manager: "m-1" });
    const b = item("b", "B", { manager: "m-1", parent: { kind: "inbox", id: "a" } });
    const c = item("c", "C", { manager: "m-1", parent: { kind: "inbox", id: "b" } });
    const d = item("d", "D", { manager: "m-1", parent: { kind: "inbox", id: "c" } });
    const chain = [a, b, c, d];

    expect(sameMatterCandidates(chain, a, [b])).toEqual([]);
    expect(sameMatterCandidates(chain, b, [c]).map((candidate) => candidate.id)).toEqual(["a"]);
  });

  it("stops walking cyclic parent data", () => {
    const a = item("cycle-a", "Cycle A", { manager: "m-1", parent: { kind: "inbox", id: "cycle-b" } });
    const b = item("cycle-b", "Cycle B", { manager: "m-1", parent: { kind: "inbox", id: "cycle-a" } });

    expect(sameMatterCandidates([current, a, b], current, []).map((candidate) => candidate.id).sort()).toEqual(["cycle-a", "cycle-b"]);
  });

  it("keeps parent lookup within this manager when another manager reuses an id", () => {
    const shared = item("shared", "Shared", { manager: "m-1" });
    const candidate = item("candidate", "Candidate", { manager: "m-1", parent: { kind: "inbox", id: "shared" } });
    const otherManagerShared = item("shared", "Other manager shared", {
      manager: "m-2",
      parent: { kind: "inbox", id: current.id },
    });

    expect(sameMatterCandidates([current, shared, candidate, otherManagerShared], current, []).map((choice) => choice.id).sort())
      .toEqual(["candidate", "shared"]);
  });

  it("draws the title with rowMeta underneath and keeps each choice a one-line-title button", () => {
    const html = renderToStaticMarkup(<SameMatterList items={[older]} busy={false} onPick={() => {}} />);
    const meta = rowMeta(older);
    expect(meta).not.toBe("");
    expect(html).toContain("Release checklist");
    expect(html).toContain(meta);
    expect(html.indexOf("Release checklist")).toBeLessThan(html.indexOf(meta));
    expect(html).toContain('data-same-matter="older"');
    expect(html).toContain("truncate");
  });
});
