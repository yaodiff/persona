import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { InboxItem, InboxTree } from "../../lib/inbox";
import type { Session } from "../../lib/types";
import type { Todo } from "../../lib/todos";
import { lineRows, LineList } from "./InboxLine";

const apiCalls: { path: string; init?: unknown }[] = [];
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: unknown) => {
    apiCalls.push({ path, init });
    return Promise.resolve({});
  },
}));

const item = (id: string, title: string, over: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "examplechat", kind: "group", account: "examplechat", account_name: "Example Chat", title, preview: "", count: 1, ...over,
});
const todo = (id: string, title: string): Todo => ({ id, title, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" });
const session = (title: string): Session => ({ machine_id: "m1", session_id: "s1", title, cwd: "/work/example" } as Session);

describe("收件箱：线索", () => {
  it("lists the chain farthest first with local names, never raw todo or session ids", () => {
    const tree: InboxTree = {
      node: { kind: "inbox", id: "b" },
      item: item("b", "示例 B"),
      ancestors: [
        { kind: "inbox", id: "a", by: "continues", item: item("a", "示例 A") },
        { kind: "todo", id: "t1", by: "user" },
      ],
      children: [item("c", "示例 C")],
    };
    const rows = lineRows(tree, undefined, { todos: [todo("t1", "准备发布")] });
    expect(rows.map((r) => [r.label, r.title])).toEqual([["待办", "准备发布"], ["事件", "示例 A"], ["下一步", "示例 C"]]);
    expect(rows.map((r) => r.route)).toEqual([{ name: "todos", id: "t1" }, { name: "inbox", id: "a" }, { name: "inbox", id: "c" }]);
    expect(rows.map((r) => r.detach?.id)).toEqual(["a", "b", "c"]);
    expect(renderToStaticMarkup(<LineList rows={rows} />)).not.toContain("t1");
  });
  it("uses the session list's title and otherwise only the kind word", () => {
    const tree: InboxTree = {
      node: { kind: "inbox", id: "b" },
      item: item("b", "示例 B"),
      ancestors: [{ kind: "session", id: "m1/s1" }],
      children: [],
    };
    const named = lineRows(tree, undefined, { session: () => session("修复发布") });
    expect(named[0]).toMatchObject({ label: "Agent 会话", title: "修复发布", route: { name: "session", mid: "m1", sid: "s1" } });
    const unnamed = lineRows(tree);
    expect(unnamed[0].title).toBe("");
    const html = renderToStaticMarkup(<LineList rows={unnamed} />);
    expect(html).toContain("Agent 会话");
    expect(html).not.toContain("m1/s1");
    expect(html).not.toContain("s1");
  });
  it("pairs every ancestor with the item directly below it, and only offers detach for an existing item", () => {
    const rows = lineRows({
      node: { kind: "inbox", id: "b" },
      item: item("b", "示例 B"),
      ancestors: [
        { kind: "inbox", id: "a", item: item("a", "示例 A") },
        { kind: "inbox", id: "p", item: item("p", "示例 P") },
        { kind: "todo", id: "t1" },
      ],
      children: [item("c", "示例 C")],
    });
    expect(rows.map((r) => r.detach?.id)).toEqual(["p", "a", "b", "c"]);

    const missing = lineRows({
      node: { kind: "inbox", id: "b" },
      item: item("b", "示例 B"),
      ancestors: [{ kind: "inbox", id: "a" }, { kind: "todo", id: "t1" }],
      children: [],
    });
    expect(missing.map((r) => r.detach?.id)).toEqual([undefined, "b"]);
    const notAnItem = lineRows({ node: { kind: "todo", id: "t0" }, ancestors: [{ kind: "inbox", id: "a", item: item("a", "示例 A") }], children: [] });
    expect(notAnItem[0].detach).toBeUndefined();
  });
  it("opens a session at its machine and does not open an item that is gone", () => {
    const rows = lineRows({
      node: { kind: "inbox", id: "b" },
      ancestors: [{ kind: "inbox", id: "gone" }, { kind: "session", id: "m1/s1" }],
      children: [],
    });
    expect(rows[0]).toMatchObject({ label: "Agent 会话", route: { name: "session", mid: "m1", sid: "s1" } });
    expect(rows[1]).toMatchObject({ title: "已不在收件箱里", route: undefined });
  });
  it("draws nothing without a line, one-line relationship actions with one", () => {
    expect(renderToStaticMarkup(<LineList rows={[]} />)).toBe("");
    const html = renderToStaticMarkup(<LineList
      rows={[{ key: "k", label: "事件", title: "示例 A", route: { name: "inbox", id: "a" }, up: true, detach: { id: "b" } }]}
      onNotSameMatter={() => {}}
      onSameMatter={() => {}}
    />);
    expect(html).toContain("data-inbox-line");
    expect(html).toContain("线索");
    expect(html).toContain("whitespace-nowrap");
    expect(html).toContain("示例 A");
    expect(html).toContain("不是同一件事");
    expect(html).toContain("是同一件事…");
  });
  it("asks the tree route with the id kept readable and a session's slash kept", async () => {
    apiCalls.length = 0;
    const { fetchInboxTree, parentRoute } = await import("../../lib/inbox");
    await fetchInboxTree("inbox", "lark:F1:900");
    await fetchInboxTree("session", "m 1/s1");
    expect(apiCalls.map((c) => c.path)).toEqual(["/inbox/tree/inbox/lark:F1:900", "/inbox/tree/session/m%201/s1"]);
    expect(parentRoute({ kind: "session", id: "nosplit" })).toBeUndefined();
  });
});
