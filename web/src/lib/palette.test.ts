import { afterEach, describe, expect, it } from "vitest";
import {
  appCommands,
  closePalette,
  commandKeys,
  contextCommands,
  filterCommands,
  getPalette,
  IDLE_PANES,
  inboxHits,
  orderGroups,
  placeHits,
  type PlaceInput,
  openPalette,
  parseQuery,
  registerContextCommands,
  setPaletteQuery,
  terminalHits,
  togglePalette,
  type MachinePanes,
  type PaletteCommand,
} from "./palette";
import { bindingsOf, SHORTCUTS } from "./shortcuts";
import { buildTerminalTree, type PanesResponse } from "./terminals";
import type { Session } from "./types";

afterEach(closePalette);

describe("palette query", () => {
  it("is command mode after a `>`, spaces before it allowed", () => {
    expect(parseQuery(">")).toEqual({ mode: "command", text: "" });
    expect(parseQuery("> rename  ")).toEqual({ mode: "command", text: "rename" });
    expect(parseQuery("  >panel")).toEqual({ mode: "command", text: "panel" });
  });
  it("is a jump otherwise, with the words trimmed; a `>` later in the text is just text", () => {
    expect(parseQuery("")).toEqual({ mode: "jump", text: "" });
    expect(parseQuery("  build ")).toEqual({ mode: "jump", text: "build" });
    expect(parseQuery("a > b")).toEqual({ mode: "jump", text: "a > b" });
  });
});

describe("palette state", () => {
  it("opens in jump mode empty and in command mode with the `>` typed", () => {
    openPalette("jump");
    expect(getPalette()).toEqual({ open: true, query: "" });
    openPalette("command");
    expect(getPalette()).toEqual({ open: true, query: ">" });
    closePalette();
    expect(getPalette()).toEqual({ open: false, query: "" });
  });
  it("toggles: a mode's key opens it, closes it when it is the one showing, and switches from the other", () => {
    togglePalette("jump");
    expect(getPalette().open).toBe(true);
    togglePalette("jump");
    expect(getPalette().open).toBe(false);
    togglePalette("command");
    expect(getPalette()).toEqual({ open: true, query: ">" });
    togglePalette("jump");
    expect(getPalette()).toEqual({ open: true, query: "" });
    togglePalette("command");
    togglePalette("command");
    expect(getPalette().open).toBe(false);
  });
  it("treats a `>` typed by hand as command mode for the toggle", () => {
    openPalette("jump");
    setPaletteQuery(">fold");
    togglePalette("command");
    expect(getPalette().open).toBe(false);
  });
  it("starts empty each time it opens (what was typed is not kept)", () => {
    openPalette("jump");
    setPaletteQuery("build");
    closePalette();
    openPalette("jump");
    expect(getPalette().query).toBe("");
  });
});

describe("context commands (the hook other modules register into)", () => {
  const run = () => {};
  const cmd = (id: string, title = id): PaletteCommand => ({ id, title, run });

  it("collects what every provider offers for this context, in registration order", () => {
    const off1 = registerContextCommands((ctx) => (ctx.pane ? [cmd("a:rename", "重命名…"), cmd("a:close", "关闭")] : []));
    const off2 = registerContextCommands(() => [cmd("b:copy", "复制路径")]);
    const route = { name: "home" } as const;
    expect(contextCommands({ route, pane: { mid: "m", sid: "s" } }).map((c) => c.id)).toEqual(["a:rename", "a:close", "b:copy"]);
    expect(contextCommands({ route }).map((c) => c.id)).toEqual(["b:copy"]);
    off1();
    expect(contextCommands({ route }).map((c) => c.id)).toEqual(["b:copy"]);
    off2();
    expect(contextCommands({ route })).toEqual([]);
  });
  it("hands the provider where the user is", () => {
    let seen: unknown;
    const off = registerContextCommands((ctx) => ((seen = ctx), []));
    contextCommands({ route: { name: "session", mid: "m", sid: "s" }, pane: { mid: "m", sid: "s" } });
    expect(seen).toEqual({ route: { name: "session", mid: "m", sid: "s" }, pane: { mid: "m", sid: "s" } });
    off();
  });
  it("skips a provider that throws and a repeated id", () => {
    const offs = [
      registerContextCommands(() => {
        throw new Error("boom");
      }),
      registerContextCommands(() => [cmd("x", "first")]),
      registerContextCommands(() => [cmd("x", "second"), cmd("y")]),
    ];
    expect(contextCommands({ route: { name: "home" } }).map((c) => c.title)).toEqual(["first", "y"]);
    offs.forEach((f) => f());
  });
});

describe("registered commands", () => {
  const all = new Set(SHORTCUTS.map((s) => s.id));
  const base = { bindings: bindingsOf({}), available: (id: string) => all.has(id), run: () => {}, machines: [] as { id: string; name: string }[] };

  it("lists every available command with its chord, grouped by where it works", () => {
    const list = appCommands(base);
    expect(list.map((c) => c.id)).toContain("app:jump");
    const jump = list.find((c) => c.id === "app:jump")!;
    expect(jump).toMatchObject({ title: "前往会话或标签页", chord: "Mod+K", section: "全局" });
    expect(list.find((c) => c.id === "app:right-panel")).toMatchObject({ section: "会话页", chord: "Mod+B" });
    expect(list.find((c) => c.id === "app:collapse-tree")).toMatchObject({ section: "侧边栏", chord: "Mod+Alt+Shift+Up" });
  });
  it("leaves out commands nothing handles", () => {
    const list = appCommands({ ...base, available: (id) => id === "settings" });
    expect(list.map((c) => c.id)).toEqual(["app:settings"]);
  });
  it("shows the user's chord, not the default", () => {
    const list = appCommands({ ...base, bindings: bindingsOf({ jump: "Mod+Shift+O" }) });
    expect(list.find((c) => c.id === "app:jump")!.chord).toBe("Mod+Shift+O");
  });
  it("runs a command by id", () => {
    const ran: unknown[][] = [];
    const list = appCommands({ ...base, run: (...a) => void ran.push(a) });
    list.find((c) => c.id === "app:help")!.run();
    expect(ran).toEqual([["help"]]);
  });
  it("turns the machine command into one per machine, with that machine's digit", () => {
    const ran: unknown[][] = [];
    const list = appCommands({ ...base, machines: [{ id: "a", name: "Alpha" }, { id: "b", name: "Beta" }], run: (...a) => void ran.push(a) });
    const machines = list.filter((c) => c.id.startsWith("app:machine:"));
    expect(machines.map((c) => [c.title, c.chord])).toEqual([["切换到电脑：Alpha", "Mod+1"], ["切换到电脑：Beta", "Mod+2"]]);
    machines[1]!.run();
    expect(ran).toEqual([["machine", 2]]);
    expect(list.some((c) => c.id === "app:machine")).toBe(false);
  });
  it("puts commands without a shortcut after, as given", () => {
    const extra: PaletteCommand = { id: "app:new-agent", title: "新建 Agent", run: () => {} };
    const list = appCommands({ ...base, extra: [extra] });
    expect(list[list.length - 1]).toBe(extra);
  });
  it("draws the chord as keycaps per platform", () => {
    expect(commandKeys({ chord: "Mod+Shift+P" }, "other")).toEqual(["Ctrl", "Shift", "P"]);
    expect(commandKeys({ chord: "Mod+Shift+P" }, "mac")).toEqual(["⇧", "⌘", "P"]);
    expect(commandKeys({ chord: "Mod+Alt+Shift+Up" }, "other")).toEqual(["Ctrl", "Alt", "Shift", "↑"]);
    expect(commandKeys({})).toEqual([]);
  });
});

describe("filtering commands", () => {
  const c = (id: string, title: string, o: Partial<PaletteCommand> = {}): PaletteCommand => ({ id, title, run: () => {}, ...o });
  const list = [c("1", "显示 / 隐藏右侧面板", { section: "会话页" }), c("2", "命令面板", { section: "全局" }), c("3", "折叠侧边栏的全部分组", { section: "侧边栏" }), c("4", "重命名…", { section: "Agent", keywords: "rename" }), c("5", "面板布局", { section: "全局" })];

  it("keeps everything, in order, with nothing typed", () => {
    expect(filterCommands(list, "").map((x) => x.id)).toEqual(["1", "2", "3", "4", "5"]);
    expect(filterCommands(list, "   ").length).toBe(5);
  });
  it("finds by title, best match first: a start before a middle", () => {
    expect(filterCommands(list, "面板").map((x) => x.id)).toEqual(["5", "1", "2"]);
    expect(filterCommands(list, "命令").map((x) => x.id)).toEqual(["2"]);
  });
  it("finds by section and by keywords, every word needed", () => {
    expect(filterCommands(list, "侧边栏").map((x) => x.id)).toEqual(["3"]);
    expect(filterCommands(list, "rename").map((x) => x.id)).toEqual(["4"]);
    expect(filterCommands(list, "agent 重命名").map((x) => x.id)).toEqual(["4"]);
    expect(filterCommands(list, "面板 rename")).toEqual([]);
  });
  it("is case-insensitive and finds nothing for nonsense", () => {
    expect(filterCommands(list, "RENAME").map((x) => x.id)).toEqual(["4"]);
    expect(filterCommands(list, "zzzz")).toEqual([]);
  });
});

// ── terminal panes ───────────────────────────────────────

function pane(sid: string, o: Partial<Session> = {}): Session {
  return { machine_id: "m1", session_id: sid, title: sid, cwd: "/code/app", status: "idle", state: "idle", ...o } as Session;
}

const listing: PanesResponse = {
  hosts: [
    {
      kind: "herdr",
      stream: true,
      control: true,
      new_tab: true,
      workspaces: [{ id: "w1", label: "webapp", number: 1, branch: "main" }, { id: "w2", label: "infra", number: 2 }],
      tabs: [
        { id: "w1:t1", workspace_id: "w1", label: "shell", number: 1 },
        { id: "w1:t2", workspace_id: "w1", label: "build", number: 2 },
        { id: "w2:t1", workspace_id: "w2", label: "logs", number: 1 },
      ],
      panes: [
        { pane: "w1:p1", terminal: "pane:w1:p1", tab_id: "w1:t1", workspace_id: "w1", title: "zsh", cwd: "/code/webapp" },
        { pane: "w1:p2", terminal: "pane:w1:p2", tab_id: "w1:t2", workspace_id: "w1", title: "cargo watch", cwd: "/code/webapp" },
        { pane: "w1:p3", terminal: "pane:w1:p3", tab_id: "w1:t2", workspace_id: "w1", agent: "claude", status: "working", session_id: "agent-1", title: "fix the bug" },
        { pane: "w2:p1", terminal: "pane:w2:p1", tab_id: "w2:t1", workspace_id: "w2", title: "tail", cwd: "/srv/logs" },
      ],
    },
  ],
};

function machine(sessions: Session[] = [], mid = "m1", name = "Studio"): MachinePanes {
  return { mid, machine: name, spaces: buildTerminalTree(listing, sessions), sessionIds: new Set(sessions.map((s) => s.session_id)) };
}

describe("terminal panes in ⌘K", () => {
  it("finds a pane by what its row shows", () => {
    const hits = terminalHits([machine()], "cargo");
    expect(hits.map((h) => h.title)).toEqual(["cargo watch"]);
    expect(hits[0]).toMatchObject({ mid: "m1", id: "pane:w1:p2", to: { name: "terminal", mid: "m1", id: "pane:w1:p2" }, state: "shell" });
  });
  it("finds by its surroundings: workspace, tab, branch, directory, machine", () => {
    expect(terminalHits([machine()], "infra").map((h) => h.title)).toEqual(["tail"]);
    expect(terminalHits([machine()], "build").map((h) => h.title)).toEqual(["cargo watch", "fix the bug"]);
    expect(terminalHits([machine()], "main").length).toBe(3);
    expect(terminalHits([machine()], "srv logs").map((h) => h.title)).toEqual(["tail"]);
    expect(terminalHits([machine()], "studio").length).toBe(4);
  });
  it("needs every word, and ranks a title that starts with the text first", () => {
    expect(terminalHits([machine()], "webapp zzz")).toEqual([]);
    expect(terminalHits([machine()], "t").map((h) => h.title)[0]).toBe("tail");
  });
  it("leaves out an agent pane whose conversation ⌘K already lists", () => {
    const withSession = machine([pane("agent-1")]);
    expect(terminalHits([withSession], "").map((h) => h.id)).not.toContain("agent-1");
    expect(terminalHits([machine()], "").map((h) => h.id)).toContain("agent-1");
  });
  it("carries the place for the row's second line", () => {
    const hit = terminalHits([machine()], "cargo")[0]!;
    expect(hit.trail).toBe("Studio › webapp › build");
    expect(hit.detail).toContain("webapp");
  });
  it("lists a few with nothing typed, after the conversations (a low score)", () => {
    const many: MachinePanes[] = Array.from({ length: 5 }, (_, i) => machine([], `m${i}`, `Box ${i}`));
    const hits = terminalHits(many, "");
    expect(hits.length).toBe(IDLE_PANES);
    expect(hits.every((h) => h.score < 1)).toBe(true);
  });
  it("keeps machines apart: the same pane id on two machines is two hits", () => {
    const hits = terminalHits([machine([], "m1", "One"), machine([], "m2", "Two")], "cargo");
    expect(hits.map((h) => h.mid).sort()).toEqual(["m1", "m2"]);
  });
  it("finds nothing on a machine whose panes could not be read", () => {
    expect(terminalHits([], "anything")).toEqual([]);
  });
});

describe("⌘K's 收件箱 and 前往 (the Mac's quick open)", () => {
  const places: PlaceInput[] = [
    { key: "inbox", title: "收件箱", icon: "inbox", to: { name: "inbox" }, words: "inbox mail lark 邮件 飞书" },
    { key: "machines", title: "机器", icon: "machine", to: { name: "machines" }, words: "machines cpu 内存" },
    { key: "settings", title: "设置", icon: "gear", to: { name: "settings" }, words: "settings preferences" },
  ];

  it("finds a page by its name or by its other words", () => {
    expect(placeHits(places, "设置").map((h) => h.key)).toEqual(["p:settings"]);
    expect(placeHits(places, "mail").map((h) => h.key)).toEqual(["p:inbox"]);
    expect(placeHits(places, "飞书").map((h) => h.key)).toEqual(["p:inbox"]);
    expect(placeHits(places, "CPU").map((h) => h.key)).toEqual(["p:machines"]);
    expect(placeHits(places, "").length).toBe(3);
    expect(placeHits(places, "nothing")).toEqual([]);
  });

  it("finds inbox items by title, sender, preview or account, the best first, then the newest", () => {
    const items = [
      { id: "1", title: "周会纪要", sender: "Alice", preview: "下周的安排", account_name: "work", updated_at: "2026-11-01T00:00:00Z" },
      { id: "2", title: "发票", sender: "Bob", preview: "周会前请确认", account_name: "me@example.com", updated_at: "2026-11-03T00:00:00Z" },
      { id: "3", title: "Re: 周会", sender: "Carol", preview: "", updated_at: "2026-11-02T00:00:00Z" },
    ];
    expect(inboxHits(items, "周会").map((h) => h.key)).toEqual(["i:1", "i:3", "i:2"]);
    expect(inboxHits(items, "bob").map((h) => h.key)).toEqual(["i:2"]);
    expect(inboxHits(items, "example").map((h) => h.key)).toEqual(["i:2"]);
    expect(inboxHits(items, "", 2).map((h) => h.key)).toEqual(["i:2", "i:3"]);
  });

  it("orders groups by their best kind of match, ties in the order given, empty ones dropped", () => {
    const g = (name: string, ...scores: number[]) => scores.map((score) => ({ name, score }));
    expect(orderGroups([g("chats", 40), g("inbox"), g("places", 100)]).map((x) => x[0].name)).toEqual(["places", "chats"]);
    expect(orderGroups([g("chats", 85), g("inbox", 80), g("places", 95)]).map((x) => x[0].name)).toEqual(["chats", "inbox", "places"]);
    expect(orderGroups([g("chats", 1), g("inbox", 1), g("places", 1)]).map((x) => x[0].name)).toEqual(["chats", "inbox", "places"]);
  });
});
