import { afterEach, describe, expect, it, vi } from "vitest";
import { applyDraft, onDraftEvent, parseQuote, replaceNeedsConfirm, sendDraft, splitQuote, storedQuotes, withAppended, withQuote, withQuoteBlock, withUnsent } from "./drafts";
import { chatFiles, displayTitle, mentionAt, replaceToken } from "./mentions";
import { browseTarget, byPrefix, nameMatches, otherSessions, pageSessions, relPath, sessionDetail, sessionQuote, workspaceContext, workspaceMatches, workspaceTitle } from "./mentions-panel";
import { contextXML } from "./links";
import type { Workspace } from "./workspace-board";
import { keepAllBut, moved, movedTo } from "./queue";
import { sameMessage } from "./echo";
import { boxRect, fitRect, keepMark } from "./image-edit";
import type { Item, Session } from "./types";

afterEach(() => vi.unstubAllGlobals());

describe("drafts", () => {
  it("quotes every line in front of the draft (ChatView.swift bindMenu)", () => {
    expect(withQuote("", "第一行\n第二行")).toBe("> 第一行\n> 第二行\n\n");
    expect(withQuote("我的看法", "原话")).toBe("> 原话\n\n我的看法");
  });

  it("sends a quote as the same > block in front of what was typed", () => {
    expect(withQuoteBlock({ text: "第一行\n\n第二行\n", from: "Persona" }, "我的看法")).toBe("> 第一行\n> \n> 第二行\n\n我的看法");
    expect(withQuoteBlock({ text: "原话" }, "  ")).toBe("> 原话");
    expect(withQuoteBlock(null, "只有字")).toBe("只有字");
    expect(withQuoteBlock({ text: "  " }, "只有字")).toBe("只有字");
  });

  it("sends a task's quote with its title and todo id on the first line (#2462)", () => {
    const task = { title: "补齐网络层的单元测试", id: "todo_7f3a" };
    const sent = withQuoteBlock({ text: "测试补好了，覆盖率 92%。\n\n还差离线的用例。", task }, "离线的什么时候补？");
    expect(sent).toBe("> 任务「补齐网络层的单元测试」（todo id: todo_7f3a）\n> \n> 测试补好了，覆盖率 92%。\n> \n> 还差离线的用例。\n\n离线的什么时候补？");
    // and back: the bubble and "Edit" see the task again
    expect(parseQuote(splitQuote(sent)!.quote)).toEqual({ text: "测试补好了，覆盖率 92%。\n\n还差离线的用例。", task });
    // a card with nothing said under it, or no todo id: the title alone
    expect(withQuoteBlock({ text: "", task: { title: "整理发布说明" } }, "")).toBe("> 任务「整理发布说明」");
    expect(parseQuote("任务「整理发布说明」")).toEqual({ text: "", task: { title: "整理发布说明" } });
    // an ordinary quote stays as it was
    expect(parseQuote("任务很多\n第二行")).toEqual({ text: "任务很多\n第二行" });
  });

  it("splits a leading > block off a message, the way it was sent or typed", () => {
    expect(splitQuote("> 第一行\n> \n> 第二行\n\n我的看法")).toEqual({ quote: "第一行\n\n第二行", rest: "我的看法" });
    expect(splitQuote(">原话")).toEqual({ quote: "原话", rest: "" });
    expect(splitQuote("\n  > 缩进的\n接着")).toEqual({ quote: "缩进的", rest: "接着" });
    expect(splitQuote("我说 > 不是引用")).toBeNull();
    expect(splitQuote("    > 代码块")).toBeNull();
    expect(splitQuote(">\n> \n正文")).toBeNull();
    // the sent shape comes back whole
    const q = { text: "多行\n原文" };
    expect(splitQuote(withQuoteBlock(q, "回复"))).toEqual({ quote: q.text, rest: "回复" });
  });

  it("puts a pulled-back queued message under the draft", () => {
    expect(withAppended("", "排队的")).toBe("排队的");
    expect(withAppended("先写的", "排队的")).toBe("先写的\n排队的");
  });

  it("applies text events and leaves the text alone for an image or a quote", () => {
    expect(applyDraft("x", { type: "quote", text: "q" })).toBe("x");
    expect(applyDraft("x", { type: "replace", text: "旧消息" })).toBe("旧消息");
    expect(applyDraft("x", { type: "append", text: "y" })).toBe("x\ny");
    expect(applyDraft("x", { type: "image", image: { name: "a.png", mime: "image/png", data: "" } })).toBe("x");
  });

  it("asks before replacing only a different draft", () => {
    expect(replaceNeedsConfirm("", "a")).toBe(false);
    expect(replaceNeedsConfirm("  a ", "a")).toBe(false);
    expect(replaceNeedsConfirm("b", "a")).toBe(true);
  });

  it("hands events to the mounted composer, else keeps text in the stored draft", () => {
    const store = new Map<string, string>([["draft:m/s", "typed"]]);
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => store.set(k, v),
      removeItem: (k: string) => store.delete(k),
    });
    expect(sendDraft("m/s", { type: "quote", text: "q", from: "Persona", ts: "2026-11-05T08:45:00Z" })).toBe(true);
    // the quote waits for the bar above the input; the typed text is left alone
    expect(store.get("draft:m/s")).toBe("typed");
    expect(storedQuotes("m/s")).toMatchObject([{ text: "q", from: "Persona", ts: "2026-11-05T08:45:00Z" }]);
    sendDraft("m/s", { type: "quote", text: "", task: { title: "整理发布说明", id: "todo_1" } });
    expect(storedQuotes("m/s")[0]?.task).toEqual({ title: "整理发布说明", id: "todo_1" });
    expect(sendDraft("m/s", { type: "image", image: { name: "a.png", mime: "image/png", data: "AA" } })).toBe(false);

    const got: string[] = [];
    const off = onDraftEvent("m/s", (e) => got.push(e.type));
    expect(sendDraft("m/s", { type: "replace", text: "r" })).toBe(true);
    expect(got).toEqual(["replace"]);
    expect(store.get("draft:m/s")).toBe("typed"); // the composer has it, not the store
    off();
    sendDraft("m/s", { type: "replace", text: "" });
    expect(store.has("draft:m/s")).toBe(false);
  });

  it("a replace while no composer is mounted puts a sent message's quote back on the bar, the words in the draft", () => {
    const store = new Map<string, string>();
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => store.set(k, v),
      removeItem: (k: string) => store.delete(k),
    });
    // a quote left on the bar from before: the message coming back has its own
    sendDraft("m/q", { type: "quote", text: "旧的引用" });
    // "Rewind to here" finishes after the reader went to another chat: the message, sent with a quote, comes back
    sendDraft("m/q", { type: "replace", text: withQuoteBlock({ text: "上周的结论" }, "照这个改") });
    expect(store.get("draft:m/q")).toBe("照这个改");
    expect(storedQuotes("m/q").map((q) => q.text)).toEqual(["上周的结论"]);
    // one sent without a quote clears the bar, as the mounted composer does
    sendDraft("m/q", { type: "replace", text: "只有字" });
    expect(store.get("draft:m/q")).toBe("只有字");
    expect(storedQuotes("m/q")).toEqual([]);
  });
});

describe("@ mentions", () => {
  it("finds the token before the caret, not inside an e-mail address", () => {
    expect(mentionAt("@", 1)).toEqual({ start: 0, end: 1, query: "" });
    expect(mentionAt("看看 @src/ma", 10)).toEqual({ start: 3, end: 10, query: "src/ma" });
    expect(mentionAt("me@example.com", 14)).toBeNull();
    expect(mentionAt("@a b", 4)).toBeNull();
    // the caret in the middle: only what is before it counts
    expect(mentionAt("x @ab y", 4)).toEqual({ start: 2, end: 4, query: "a" });
  });

  it("replaces the token and does not double the space after a picked file", () => {
    const t = mentionAt("看 @Rea 呢", 6)!;
    expect(replaceToken("看 @Rea 呢", t, "@README.md ")).toEqual({ text: "看 @README.md 呢", caret: 13 });
    const d = mentionAt("@sr", 3)!;
    expect(replaceToken("@sr", d, "@src/")).toEqual({ text: "@src/", caret: 5 });
  });

  it("lists the files the chat's tools touched, newest first and once each", () => {
    const tool = (seq: number, input: Record<string, unknown>): Item => ({ id: `t${seq}`, seq, role: "assistant", kind: "tool_use", tool_input: input });
    const items: Item[] = [
      tool(1, { file_path: "/w/a.ts" }),
      tool(2, { command: "ls" }),
      tool(3, { notebook_path: "/w/n.ipynb" }),
      tool(4, { file_path: "/w/a.ts" }),
      { id: "x", seq: 5, role: "assistant", kind: "text", text: "/w/b.ts" },
    ];
    expect(chatFiles(items)).toEqual(["/w/a.ts", "/w/n.ipynb"]);
    expect(chatFiles(items, 1)).toEqual(["/w/a.ts"]);
    expect(relPath("/w/src/a.ts", "/w")).toBe("src/a.ts");
    expect(relPath("/other/a.ts", "/w/")).toBe("/other/a.ts");
  });

  it("browses a folder once the query has a slash", () => {
    expect(browseTarget("src", "/w")).toBeNull();
    expect(browseTarget("src/ma", "/w/")).toEqual({ dir: "/w/src", typed: "src/", filter: "ma" });
    expect(browseTarget("/etc/ho", "/w")).toEqual({ dir: "/etc", typed: "/etc/", filter: "ho" });
    expect(browseTarget("/", "/w")).toEqual({ dir: "/", typed: "/", filter: "" });
    expect(browseTarget("~/", undefined)).toEqual({ dir: "~", typed: "~/", filter: "" });
    expect(browseTarget("src/", undefined)).toBeNull();
  });

  it("matches names anywhere, hidden ones only when asked, prefix matches first", () => {
    expect(nameMatches("README.md", "read")).toBe(true);
    expect(nameMatches(".env", "env")).toBe(false);
    expect(nameMatches(".env", ".e")).toBe(true);
    expect(byPrefix(["xmain", "main", "amain"], (s) => s, "ma")).toEqual(["main", "xmain", "amain"]);
  });

  const sess = (id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "m", session_id: id, title: `t-${id}`, cwd: `/code/${id}`, status: "idle", ...extra });

  it("offers other sessions, newest first, not assistants or this one", () => {
    const all = [
      sess("a", { updated_at: "2026-11-02T10:00:00Z" }),
      sess("b", { updated_at: "2026-11-02T12:00:00Z", agent: "codex" }),
      sess("c", { assistant: true, updated_at: "2026-11-02T13:00:00Z" }),
      sess("self", { updated_at: "2026-11-02T14:00:00Z" }),
    ];
    expect(otherSessions(all, "m/self", "").map((s) => s.session_id)).toEqual(["b", "a"]);
    expect(otherSessions(all, "m/self", "codex").map((s) => s.session_id)).toEqual(["b"]);
    expect(displayTitle({ title: "Claude Code", cwd: "/x/weather" })).toBe("weather");
  });

  it("holds the chats opened here and the Agents list's other rows, each session once (#12983)", () => {
    const opened = sess("a", { title: "Complete detail", queued: [] });
    const listed = [sess("a", { title: "List row", waiting: false }), sess("b", { waiting: false }), sess("a", { machine_id: "n", waiting: false })];
    const held = pageSessions({ m: [opened] }, listed);
    // the opened chat's complete row stands for its list row; the same session id on another computer is another session
    expect(held.map((s) => `${s.machine_id}/${s.session_id}`)).toEqual(["m/a", "m/b", "n/a"]);
    expect(held[0]).toBe(opened);
    expect(held[1]).toBe(listed[1]);
    // a hub that pushes complete rows and has no list: those rows as they are
    expect(pageSessions({ m: [opened] }, [])).toEqual([opened]);
    // an Agent the list has and nobody opened is offered once the panel reads both
    expect(otherSessions(held, "m/self", "").map((s) => `${s.machine_id}/${s.session_id}`)).toEqual(["m/a", "m/b", "n/a"]);
  });

  it("folds a child session into its listed parent, as every list does (#5120)", () => {
    const parent = sess("p", { waiting: false, children: { total: 1 } });
    const child = sess("c", { folded_under: "p" });
    expect(pageSessions({ m: [child] }, [parent]).map((s) => s.session_id)).toEqual(["p"]);
    // a child whose parent the page does not hold stays a row of its own
    expect(pageSessions({ m: [child] }, []).map((s) => s.session_id)).toEqual(["c"]);
  });

  it("names a Windows computer's folder by its own separator (#9901)", () => {
    const win = sess("w", { title: "", cwd: "C:\\Users\\me\\code\\app" });
    expect(displayTitle(win)).toBe("app");
    expect(sessionDetail(win)).toBe("Claude · app");
    expect(displayTitle({ title: "Claude Code", cwd: "C:\\Users\\me\\code\\app\\" })).toBe("app");
    expect(displayTitle({ title: "", cwd: "\\\\server\\share\\app" })).toBe("app");
    expect(workspaceTitle({ title: "", cwd: "D:\\work\\notes" })).toBe("notes");
    // elsewhere a backslash is part of a name; no folder, or the root, is still untitled
    expect(displayTitle({ title: "", cwd: "/code/a\\b" })).toBe("a\\b");
    expect(displayTitle({ title: "", cwd: "/" })).toBe(displayTitle({ title: "", cwd: "" }));
    expect(sessionDetail(sess("r", { cwd: "/" }))).toBe("Claude");
  });

  it("quotes a session with its folder and last words", () => {
    const q = sessionQuote(sess("a", { title: "修登录", cwd: "/code/app", last_text: "  改好了  " }));
    expect(q.url).toBe("persona://session/m/a");
    expect(q.title).toBe("修登录");
    expect(q.text).toBe("另一个会话：修登录（Claude）\n目录：/code/app\n最近回复：改好了");
  });

  it("keeps a TRAE CLI session's custom label in the mention menu and quote", () => {
    for (const agent of ["traex", "traecli", "trae-cli"]) {
      const s = sess("a", { title: "Import", cwd: "/code/app", agent, agent_label: "Team relay" });
      expect(sessionDetail(s)).toBe("Team relay · app");
      expect(sessionQuote(s).text).toContain("Import（Team relay）");
      expect(sessionDetail({ ...s, agent_label: "TRAEX" })).toBe("TRAE CLI · app");
      expect(sessionDetail({ ...s, agent_label: undefined })).toBe("TRAE CLI · app");
    }
  });
});

describe("queue", () => {
  it("re-queues every other message in order", () => {
    expect(keepAllBut(3, 1)).toEqual([0, 2]);
    expect(keepAllBut(1, 0)).toEqual([]);
  });

  it("moves one message up or down, the others in their order (#6088)", () => {
    expect(moved(3, 1, -1)).toEqual([1, 0, 2]);
    expect(moved(3, 1, 1)).toEqual([0, 2, 1]);
    expect(moved(3, 0, -1)).toBeNull();
    expect(moved(3, 2, 1)).toBeNull();
  });

  it("puts one message at any place, the others in their order (the drag handle, #5327)", () => {
    expect(movedTo(4, 3, 0)).toEqual([3, 0, 1, 2]);
    expect(movedTo(4, 0, 3)).toEqual([1, 2, 3, 0]);
    expect(movedTo(4, 1, 2)).toEqual([0, 2, 1, 3]);
    expect(movedTo(4, 2, 2)).toBeNull();
    expect(movedTo(4, 0, 4)).toBeNull();
    expect(movedTo(4, -1, 0)).toBeNull();
  });

  it("matches a sent message to its queued copy, short ones only exactly", () => {
    expect(sameMessage("好", "好的，先这样")).toBe(false);
    expect(sameMessage(" 好 ", "好")).toBe(true);
    expect(sameMessage("把测试也跑一遍再提交", "把测试也跑一遍再提交 [Image #1]")).toBe(true);
    expect(sameMessage("", "")).toBe(false);
  });
});

describe("image markup geometry", () => {
  const img = { width: 400, height: 200 };
  // the editor's turns, crops, widths and history: image-edit.test.ts
  it("fits the image in the box", () => {
    expect(fitRect(img, { width: 200, height: 200 })).toEqual({ x: 0, y: 50, width: 200, height: 100 });
    expect(fitRect({ width: 0, height: 10 }, { width: 10, height: 10 }).width).toBe(0);
  });

  it("keeps boxes dragged either way and drops taps", () => {
    expect(boxRect({ x: 10, y: 20 }, { x: 4, y: 2 })).toEqual({ x: 4, y: 2, width: 6, height: 18 });
    expect(keepMark({ tool: "box", ink: "red", width: 2, points: [{ x: 1, y: 1 }] })).toBe(false);
    expect(keepMark({ tool: "box", ink: "red", width: 2, points: [{ x: 1, y: 1 }, { x: 1.5, y: 1 }] })).toBe(false);
    expect(keepMark({ tool: "pen", ink: "red", width: 2, points: [{ x: 1, y: 1 }, { x: 1, y: 2 }] })).toBe(true);
  });
});

describe("发送失败放回输入框", () => {
  it("空着就原样放回；期间又打了字，接在放回的那段下面", () => {
    expect(withUnsent("", "删除所有测试设备")).toBe("删除所有测试设备");
    expect(withUnsent("还有一件事", "删除所有测试设备")).toBe("删除所有测试设备\n还有一件事");
    expect(withUnsent("还有一件事", "")).toBe("还有一件事");
  });
});

describe("@ workspaces (#5594)", () => {
  const ws = (id: string, title: string, cwd: string, extra: Partial<Workspace> = {}): Workspace => ({ id, machine_id: "box", source: "herdr", source_id: id, title, cwd, online: true, available: true, ...extra });
  const name = (id: string) => (id === "box" ? "Work laptop" : id);

  it("ranks title prefixes first and unavailable ones last, matching computer and folder too", () => {
    const all = [ws("a", "app-weather", "/c/aw"), ws("b", "weather", "/c/w", { available: false }), ws("c", "web", "/c/weather-web"), ws("d", "notes", "/n")];
    expect(workspaceMatches(all, "wea", name).map((w) => w.id)).toEqual(["a", "c", "b"]);
    expect(workspaceMatches(all, "laptop", name)).toHaveLength(4);
    expect(workspaceMatches([ws("e", "", "/c/untitled")], "", name).map(workspaceTitle)).toEqual(["untitled"]);
  });

  it("writes the computer, folder and branch as attributes, escaped", () => {
    const c = workspaceContext(ws("a", 'say "hi"', "/c/<x>", { branch: "main" }), name);
    const xml = contextXML({ kind: "workspace", ...c });
    expect(xml.split("\n")[0]).toBe('<persona-context kind="workspace" title="say &quot;hi&quot;" url="" machine="box" cwd="/c/&lt;x&gt;" branch="main">');
    expect(c.text).toContain("Work laptop");
  });
});
