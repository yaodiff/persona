import { describe, expect, it } from "vitest";
import type { Session } from "../../lib/types";
import { arrange, conversationTitle, filterConversations, groupByDay, mergeConversations, splitTitle, type Conversation } from "./conversations";

const now = new Date(2026, 10, 2, 10, 0);
const at = (d: number, h = 9) => new Date(2026, 10, d, h).toISOString();
const conv = (sid: string, p: Partial<Conversation> = {}): Conversation => ({
  machine_id: "server", session_id: sid, title: sid, cwd: "/home/me/.persona/assistant", status: "idle", assistant: true, ...p,
});

describe("titles", () => {
  it("splits the hub's titles", () => {
    expect(splitTitle("1101 | 修复 | 搜索框和顶栏重叠")).toEqual({ head: "1101 | 修复 | ", topic: "搜索框和顶栏重叠" });
    expect(splitTitle("早报 · 周三")).toBeNull();
    expect(splitTitle("1101 | 修复")).toBeNull();
  });

  it("calls an untitled one 新对话", () => {
    expect(conversationTitle({ title: "" })).toBe("新对话");
    expect(conversationTitle({ title: "Claude Code" })).toBe("新对话");
    expect(conversationTitle({ title: " 早报 " })).toBe("早报");
  });

  it("reads a chat titled by its <persona-context> tag: what was typed after it, else the tag's title, else 新对话", () => {
    expect(conversationTitle({ title: '<persona-context kind="context" title="飞书 · 发布群 &quot;周五&quot;" url="">' })).toBe('飞书 · 发布群 "周五"');
    expect(conversationTitle({ title: '<persona-context kind="context" title="A &amp; B &lt;x&gt;" url="">\n正文' })).toBe("A & B <x>");
    expect(conversationTitle({ title: '<persona-context kind="context" title="群" url="">\n正文\n</persona-context>\n\n帮我总结一下\n第二行' })).toBe("帮我总结一下");
    expect(conversationTitle({ title: '<persona-context kind="context" title="" url="">' })).toBe("新对话");
    expect(conversationTitle({ title: "<persona-context kind=" })).toBe("新对话");
    // a title that only mentions the tag further on is the user's own words
    expect(conversationTitle({ title: "什么是 <persona-context> 标签" })).toBe("什么是 <persona-context> 标签");
  });

  it("calls the main conversation Persona, whatever its first message was (#1313)", () => {
    expect(conversationTitle({ title: "我的邮箱里最近有哪些需要我处理或回复的邮件？挑重要的说。" }, true)).toBe("Persona");
    expect(conversationTitle({ title: "" }, true)).toBe("Persona");
  });
});

describe("search", () => {
  const list = [conv("a", { title: "约周五吃饭", last_text: "已经在日历里加好了。" }), conv("b", { title: "Weekly Report", last_text: "" }), conv("c", { title: "" })];
  it("matches the title or the last message, ignoring case and width", () => {
    expect(filterConversations(list, "日历").map((s) => s.session_id)).toEqual(["a"]);
    expect(filterConversations(list, "ｗｅｅｋｌｙ").map((s) => s.session_id)).toEqual(["b"]);
    expect(filterConversations(list, "新对话").map((s) => s.session_id)).toEqual(["c"]);
    expect(filterConversations(list, "  ")).toHaveLength(3);
  });
});

describe("arrange", () => {
  it("groups by day, newest first", () => {
    const g = groupByDay([conv("a", { updated_at: at(1) }), conv("b", { updated_at: at(2) }), conv("c", { updated_at: new Date(2026, 9, 10).toISOString() }), conv("d"), conv("e", { updated_at: new Date(2026, 9, 30).toISOString() })], now);
    expect(g.map((x) => [x.title, x.items.map((s) => s.session_id)])).toEqual([["今天", ["b"]], ["昨天", ["a"]], ["最近 7 天", ["e"]], ["更早", ["c", "d"]]]);
  });

  it("puts the main one first and the archived apart", () => {
    const list = [conv("main", { updated_at: at(1) }), conv("x", { updated_at: at(2) }), conv("old", { updated_at: at(2, 8), archived: true })];
    const a = arrange(list, "main", now);
    expect(a.main?.session_id).toBe("main");
    expect(a.days.flatMap((d) => d.items.map((s) => s.session_id))).toEqual(["x"]);
    expect(a.archived.map((s) => s.session_id)).toEqual(["old"]);
    expect(arrange(list, undefined, now).main).toBeUndefined();
  });

  it("merges what was fetched with the store and the live sessions", () => {
    const fetched = [conv("a", { archived: true, title: "旧标题" }), conv("b")];
    const listed: Session[] = [conv("b", { title: "新的 b" }), conv("n", { title: "刚开的" })];
    const live: Session[] = [conv("a", { title: "新标题", state: "unread", archived: true }), conv("other", { assistant: false })];
    const m = mergeConversations(fetched, listed, live);
    expect(m.map((s) => [s.session_id, s.title, s.archived, s.state])).toEqual([
      ["a", "新标题", true, "unread"],
      ["b", "新的 b", undefined, undefined],
      ["n", "刚开的", undefined, undefined],
    ]);
  });

  it("takes the archive mark from the live sessions both ways: one brought back elsewhere leaves 已归档", () => {
    // the hub marks every assistant session it pushes (archived only when it is, omitempty): one unarchived on another
    // device, by a message sent to it or by Persona comes without the mark
    const fetched = [conv("back", { archived: true }), conv("gone")];
    const live: Session[] = [conv("back"), conv("gone", { archived: true })];
    const m = mergeConversations(fetched, [], live);
    expect(m.map((s) => [s.session_id, !!s.archived])).toEqual([
      ["back", false],
      ["gone", true],
    ]);
    expect(arrange(m, undefined, now).archived.map((s) => s.session_id)).toEqual(["gone"]);
  });
});
