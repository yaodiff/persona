import { describe, expect, it } from "vitest";
import { autoConversations, autoOrigin, externalFrom, externalOpening, externalOrigin, matchesOrigin, openerOf, originMarkLabel, parseOriginFilter, recentConversations, unlistedConversations } from "./conversation-origins";
import type { Item, Session } from "./types";

const conv = (id: string, extra: Partial<Session> = {}) => ({ machine_id: "hub", session_id: id, title: id, cwd: "", status: "idle", ...extra }) as Session;

describe("conversation origins (docs/protocol/protocol.md「Origin of assistant chats」)", () => {
  it("counts none, user and channel as the user's own and anything else as automatic", () => {
    expect(autoOrigin(conv("a"))).toBeUndefined();
    expect(autoOrigin(conv("a", { origin: "user" }))).toBeUndefined();
    expect(autoOrigin(conv("a", { origin: "channel" }))).toBeUndefined();
    expect(autoOrigin(conv("a", { origin: "inbox" }))).toBe("inbox");
    expect(autoOrigin(conv("a", { origin: "lark_agent" }))).toBe("lark_agent");
    expect(autoOrigin(conv("a", { origin: "something_new" }))).toBe("other");
  });

  it("tallies the automatic ones by origin, in the sidebar's order, with what runs and waits", () => {
    const list = [
      conv("main", { origin: "task" }),
      conv("t1", { origin: "task", state: "working" }),
      conv("i1", { origin: "inbox" }),
      conv("i2", { origin: "inbox", state: "needs_you" }),
      conv("i3", { origin: "inbox", archived: true, state: "needs_you" }),
      conv("x", { origin: "future" }),
      conv("mine"),
    ];
    const { total, groups } = autoConversations(list, "main");
    expect(total).toEqual({ count: 4, working: 1, needsYou: 1 });
    expect(groups).toEqual([
      { origin: "inbox", count: 2, working: 0, needsYou: 1 },
      { origin: "task", count: 1, working: 1, needsYou: 0 },
      { origin: "other", count: 1, working: 0, needsYou: 0 },
    ]);
  });

  it("filters 全部对话 by an address's filter", () => {
    expect(parseOriginFilter(undefined)).toBe("all");
    expect(parseOriginFilter("bogus")).toBe("all");
    expect(parseOriginFilter("mine")).toBe("mine");
    expect(parseOriginFilter("inbox")).toBe("inbox");
    expect(matchesOrigin(conv("a", { origin: "inbox" }), "mine")).toBe(false);
    expect(matchesOrigin(conv("a", { origin: "channel" }), "mine")).toBe(true);
    expect(matchesOrigin(conv("a", { origin: "inbox" }), "inbox")).toBe(true);
    expect(matchesOrigin(conv("a", { origin: "task" }), "inbox")).toBe(false);
    expect(matchesOrigin(conv("a", { origin: "task" }), "all")).toBe(true);
  });

  it("filters to every chat an external source opened for the phone tab bar's External", () => {
    expect(parseOriginFilter("external")).toBe("external");
    expect(matchesOrigin(conv("a", { origin: "lark_agent" }), "external")).toBe(true);
    expect(matchesOrigin(conv("a", { origin: "extension" }), "external")).toBe(true);
    for (const origin of [undefined, "", "user", "channel", "inbox", "task", "spawned", "something_new"]) {
      expect(matchesOrigin(conv("a", { origin }), "external"), String(origin)).toBe(false);
    }
    // the background supervisor is with the scheduled tasks whatever its row says
    expect(matchesOrigin(conv("bg", { origin: "lark_agent" }), "external", "bg")).toBe(false);
  });

  it("counts only Lark /agent and other extensions as external, by the trimmed origin (#3758)", () => {
    expect(externalOrigin(conv("a", { origin: "lark_agent" }))).toBe("lark_agent");
    expect(externalOrigin(conv("a", { origin: " extension " }))).toBe("extension");
    for (const origin of [undefined, "", "user", "channel", "inbox", "task", "spawned", "other", "something_new", "Extension"]) {
      expect(externalOrigin(conv("a", { origin })), String(origin)).toBeUndefined();
    }
    expect(externalOrigin(undefined)).toBeUndefined();
  });

  it("says where an external conversation came from with the channel line's words (tests run in zh-Hans)", () => {
    expect(externalFrom("lark_agent")).toBe("来自 飞书 /agent");
    expect(externalFrom("extension")).toBe("来自扩展");
  });

  it("finds the message an external conversation was opened with only once the items reach its start", () => {
    const item = (id: string, role: string, kind: string, extra: Partial<Item> = {}) => ({ id, seq: 0, role, kind, ...extra }) as Item;
    const items = [item("n", "assistant", "notice"), item("tool-pic", "user", "image", { tool_use_id: "t1" }), item("u1#0", "user", "text"), item("a1", "assistant", "text"), item("u2#0", "user", "text")];
    const lark = conv("c", { origin: "lark_agent" });
    expect(externalOpening(lark, items, false)).toEqual({ id: "u1#0", origin: "lark_agent" });
    // earlier items still to load: the first one here is not the first
    expect(externalOpening(lark, items, true)).toBeUndefined();
    // a message that starts with a picture is opened by that picture's item, as lib/turns.ts groups it
    expect(externalOpening(conv("c", { origin: "extension" }), [item("p1#0", "user", "image"), item("p1#1", "user", "text")], false)).toEqual({ id: "p1#0", origin: "extension" });
    expect(externalOpening(lark, [item("a1", "assistant", "text")], false)).toBeUndefined();
    expect(externalOpening(conv("c", { origin: "channel" }), items, false)).toBeUndefined();
    expect(externalOpening(conv("c"), items, false)).toBeUndefined();
  });

  it("names the extension that opened an external conversation by opened_by, else origin_ref (#10358)", () => {
    expect(openerOf("lark_agent", { opened_by: "bridge", origin_ref: "bridge" })).toBe("bridge");
    expect(openerOf("extension", { opened_by: " notes " })).toBe("notes");
    // one opened before the hub noted opened_by: origin_ref is that extension too
    expect(openerOf("lark_agent", { origin_ref: "bridge" })).toBe("bridge");
    expect(openerOf("extension", { opened_by: "", origin_ref: "notes" })).toBe("notes");
    // the backfill knows no extension, and the other origins' refs are not extensions
    expect(openerOf("lark_agent", {})).toBeUndefined();
    expect(openerOf("lark_agent", undefined)).toBeUndefined();
    expect(openerOf("inbox", { origin_ref: "item-1" })).toBeUndefined();
    expect(openerOf("spawned", { origin_ref: "conv-1", opened_by: "x" })).toBeUndefined();
    expect(openerOf(undefined, { opened_by: "x" })).toBeUndefined();
  });
});

describe("the sidebar's Recent (#4873, #5195)", () => {
  const at = (h: number) => new Date(Date.UTC(2026, 10, 12, 12) - h * 3_600_000).toISOString();
  const list = [
    conv("main", { updated_at: at(0) }),
    conv("lead", { updated_at: at(0.5) }),
    conv("bg", { origin: "task", updated_at: at(0.05) }),
    conv("inbox", { origin: "inbox", updated_at: at(0.2) }),
    conv("gone", { archived: true, updated_at: at(0.1) }),
    conv("a", { origin: "user", updated_at: at(1) }),
    conv("lark", { origin: "lark_agent", updated_at: at(1.5) }),
    conv("b", { updated_at: at(2) }),
    conv("wx", { origin: "channel", updated_at: at(3) }),
    conv("task", { origin: "task", updated_at: at(4) }),
    conv("e2", { updated_at: at(5) }),
    conv("e1", { updated_at: at(5) }),
    conv("never"),
  ];

  it("is every chat newest first whoever opened it, never the main one, a leader, an archived one or the background supervisor", () => {
    expect(recentConversations(list, "main", ["lead"], "bg").map((c) => c.session_id)).toEqual(["inbox", "a", "lark", "b", "wx", "e1", "e2", "never"]);
  });

  it("applies the shared manual order after all existing Recent filters, keeping new rows visible", () => {
    expect(recentConversations(list, "main", ["lead"], "bg", false, ["b", "inbox", "not-loaded"]).map((c) => c.session_id)).toEqual(["b", "inbox", "a", "lark", "wx", "e1", "e2", "never"]);
    expect(recentConversations(list, "main", ["lead"], "bg", false, ["main", "lead", "gone"]).map((c) => c.session_id)).toEqual(["inbox", "a", "lark", "b", "wx", "e1", "e2", "never"]);
  });

  it("leaves the scheduled tasks' own chats out of Recent, however new they are (#6228): they keep their entry on Scheduled Tasks and All chats", () => {
    const taskFirst = [conv("main", { updated_at: at(0) }), conv("task", { origin: "task", updated_at: at(0.1) }), conv("mine", { origin: "user", updated_at: at(1) })];
    expect(recentConversations(taskFirst, "main", []).map((c) => c.session_id)).toEqual(["mine"]);
    // the background supervisor is a task-origin chat: a hub without its id still keeps it out of Recent, in All chats
    expect(recentConversations(list, "main", ["lead"]).map((c) => c.session_id)).not.toContain("bg");
  });

  it("brings the background supervisor and the tasks' chats back by time when the user shows them (#8324)", () => {
    expect(recentConversations(list, "main", ["lead"], "bg", true).map((c) => c.session_id)).toEqual(["bg", "inbox", "a", "lark", "b", "wx", "task", "e1", "e2", "never"]);
    // still never the main one, a leader or an archived one
    expect(recentConversations(list, "main", ["lead"], "bg", true).map((c) => c.session_id)).not.toContain("gone");
  });

  it("takes an ended leader back and is empty when nothing else is left", () => {
    expect(recentConversations(list, "main", [], "bg").map((c) => c.session_id).slice(0, 3)).toEqual(["inbox", "lead", "a"]);
    expect(recentConversations(list.slice(0, 3), "main", ["lead"], "bg")).toEqual([]);
  });

  it("names the assistant's conversations the held answer does not list, whoever made them, to read it again (#10190)", () => {
    const held = { main: "main", background_supervisor: "bg", conversations: [conv("listed", { assistant: true })] };
    const known = [
      conv("main", { assistant: true }), // the main one, which the answer names
      conv("bg", { assistant: true, origin: "task" }), // the background supervisor it names
      conv("listed", { assistant: true, state: "working" }), // listed: its change is laid over it, nothing is read
      conv("agent", { origin: "user" }), // an Agent's session, not the assistant's
      conv("old", { assistant: true, archived: true }), // the answer leaves archived ones out
      conv("gone", { assistant: true, deleted: true }),
      conv("health", { assistant: true, space: "health" }), // another space's
    ];
    const made = [
      conv("lark", { assistant: true, origin: "lark_agent" }), // an integration's POST /assistant/conversations
      conv("phone", { assistant: true, origin: "user" }), // started on the phone
      conv("morning", { assistant: true, origin: "task", origin_ref: "task_1" }), // a scheduled task's
    ];
    expect(unlistedConversations(held, known)).toEqual([]);
    expect(unlistedConversations(held, [...known, ...made])).toEqual(["lark", "phone", "morning"]);
    expect(unlistedConversations({ conversations: [] }, [conv("first", { assistant: true })])).toEqual(["first"]);
  });
});

describe("a row's origin mark (#5195)", () => {
  it("is named as All chats' filter for its group, an external one by where it came from", () => {
    expect(originMarkLabel("inbox")).toBe("收件箱");
    expect(originMarkLabel("task")).toBe("定时任务");
    expect(originMarkLabel("spawned")).toBe("对话派生");
    expect(originMarkLabel("other")).toBe("其他");
    expect(originMarkLabel("lark_agent")).toBe(externalFrom("lark_agent"));
    expect(originMarkLabel("extension")).toBe(externalFrom("extension"));
  });
});
