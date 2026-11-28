import { describe, expect, it } from "vitest";
import { PERSONA_SERVERS } from "./compatibility";
import { chatLines, freshWords, humanizeTool, insideFold, isLoopTick, mcpServerName, mergeItems, withoutFolded, parseUserText, pickSubject, segments, openRecap, personaToolVerb, supervisionTool, supervisionVisible, toolChip, toolDisplayName, toolLabel, formatDuration } from "./turns";
import type { Item } from "./types";

const u = (seq: number, text: string, extra: Partial<Item> = {}): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text, ...extra });
const a = (seq: number, text: string): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text });
const tool = (seq: number, id: string): Item => ({ id: `t${seq}#0`, seq, role: "assistant", kind: "tool_use", tool_use_id: id, tool_name: "Bash", tool_input: { command: "ls -la\npwd" } });
const res = (seq: number, id: string): Item => ({ id: `r${seq}#0`, seq, role: "user", kind: "tool_result", tool_use_id: id, text: "ok" });

describe("segments", () => {
  it("keeps every real user message amid repeated compaction and supervision", () => {
    const items: Item[] = [];
    for (let question = 1; question <= 3; question++) {
      items.push(u(question * 10, `Question ${question}`, { client_msg_id: `client-${question}` }), a(question * 10 + 1, "An answer"));
      if (question < 3) items.push({ id: `compact-${question}`, seq: question * 10 + 2, role: "system", kind: "notice", text: "Context compacted", compact: { trigger: "auto" } });
      for (let n = 0; n < 3; n++) items.push(u(question * 10 + 3 + n, "Automatic task supervision", {
        injected: { kind: "system", state: "supervise", line: "Review a synthetic task" },
      }));
    }
    const users = chatLines(items).flatMap((line) => line.kind === "segment" && line.seg.user ? [line.seg.user] : []);
    expect(users.filter((user) => user.client_msg_id).map((user) => user.text)).toEqual(["Question 1", "Question 2", "Question 3"]);
  });

  it("splits at user messages and takes the last words as the answer", () => {
    const s = segments([u(1, "hi"), a(2, "looking"), tool(3, "x"), res(4, "x"), a(5, "done"), u(6, "next"), a(7, "sure")]);
    expect(s).toHaveLength(2);
    expect(s[0].user?.text).toBe("hi");
    expect(s[0].answer?.text).toBe("done");
    expect(s[0].steps.map((x) => x.kind)).toEqual(["text", "tool"]);
    expect(s[0].tools).toBe(1);
    expect(s[0].replies).toBe(1);
    expect([s[0].tools, s[0].thoughts, s[0].replies]).toEqual([1, 0, 1]); // the counts the old work row read (workSummary is gone with it)
    const st = s[0].steps[1];
    expect(st.kind === "tool" && st.result?.text).toBe("ok");
    expect(s[1].answer?.text).toBe("sure");
  });

  it("starts with a segment without a message when the page begins mid-turn", () => {
    const s = segments([tool(1, "x"), res(2, "x"), a(3, "end")]);
    expect(s[0].user).toBeUndefined();
    expect(s[0].answer?.text).toBe("end");
  });

  it("keeps the blocks of one line (text + image) as one message", () => {
    const img: Item = { id: "u1#1", seq: 1, role: "user", kind: "image", image: { mime: "image/png", lazy: true } };
    const s = segments([u(1, "look [Image #1]"), img, a(2, "nice")]);
    expect(s).toHaveLength(1);
    expect(s[0].user?.text).toBe("look");
    expect(s[0].user?.images).toHaveLength(1);
  });

  it("counts folded work", () => {
    const f: Item = { id: "fold:t2#0", seq: 2, role: "assistant", kind: "fold", fold: { items: 10, tools: 5, thoughts: 2, through_seq: 9 } };
    const s = segments([u(1, "go"), f, a(10, "ok")]);
    expect(s[0].tools).toBe(5);
    expect(s[0].thoughts).toBe(2);
    expect(s[0].folds).toHaveLength(1);
  });
});

describe("freshWords", () => {
  it("is the agent's words in a push the chat did not have yet", () => {
    const have = [u(1, "hi"), a(2, "earlier")];
    const pushed = [a(2, "earlier"), tool(3, "x"), a(4, "new words"), u(5, "next")];
    expect(freshWords(have, pushed)).toEqual([pushed[2].id]);
    expect(freshWords(have, [])).toEqual([]);
  });
});

describe("mergeItems", () => {
  it("drops a fold once the steps of its range arrive", () => {
    const f: Item = { id: "fold:t3#0", seq: 3, role: "assistant", kind: "fold", fold: { items: 2, tools: 1, thoughts: 0, through_seq: 4 } };
    const have = mergeItems([], [u(1, "go"), f, a(5, "ok")]);
    expect(have.map((i) => i.id)).toEqual(["u1#0", "fold:t3#0", "a5#0"]);
    const next = mergeItems(have, [tool(3, "x"), res(4, "x")]);
    expect(next.map((i) => i.id)).toEqual(["u1#0", "t3#0", "r4#0", "a5#0"]);
  });

  it("opens a narration-only fold and clears its text when a folded page arrives again", () => {
    const f: Item = { id: "fold:a2#0", seq: 2, role: "assistant", kind: "fold", fold: { items: 3, tools: 0, thoughts: 0, through_seq: 4 } };
    const have = mergeItems([], [u(1, "go"), f, a(5, "done")]);
    const expanded = mergeItems(have, [a(2, "first"), a(3, "second"), a(4, "third")]);
    expect(expanded.map((i) => i.id)).toEqual(["u1#0", "a2#0", "a3#0", "a4#0", "a5#0"]);

    const refolded = mergeItems(withoutFolded(expanded, [f]), [f]);
    expect(refolded.map((i) => i.id)).toEqual(["u1#0", "fold:a2#0", "a5#0"]);
  });

  it("uses numeric block bounds for narration on the same line as the final answer", () => {
    const f: Item = { id: "fold:2#2", seq: 2, role: "assistant", kind: "fold", fold: { items: 2, tools: 0, thoughts: 0, through_seq: 2, through_id: "2#10" } };
    const narration: Item = { id: "2#3", seq: 2, role: "assistant", kind: "text", text: "progress" };
    const answer: Item = { id: "2#11", seq: 2, role: "assistant", kind: "text", text: "done" };
    const expanded = mergeItems([f, answer], [narration]);
    expect(expanded.map((i) => i.id)).toEqual(["2#3", "2#11"]);

    const refolded = mergeItems(withoutFolded(expanded, [f]), [f]);
    expect(refolded.map((i) => i.id)).toEqual(["fold:2#2", "2#11"]);
  });
});

describe("parseUserText", () => {
  it("drops the attached_images block and image markers", () => {
    expect(parseUserText("[Image #1] 像这样合并。\n<attached_images>\n/tmp/a.jpg\n</attached_images>").text).toBe("像这样合并。");
    expect(parseUserText("像这样合并。\n<attached_images>\n</attached_images>").text).toBe("像这样合并。");
  });

  it("keeps the attached photos' paths for the bubble's thumbnails (G026)", () => {
    const p = parseUserText("看这两张\n\n<attached_images>\n/tmp/persona/a.jpg\n /tmp/persona/b.png \n这不是路径\n/tmp/persona/a.jpg\n~/Pictures/c.heic\n</attached_images>");
    expect(p.text).toBe("看这两张");
    expect(p.photos).toEqual(["/tmp/persona/a.jpg", "/tmp/persona/b.png", "~/Pictures/c.heic"]);
    expect(parseUserText("没有图").photos).toEqual([]);
  });

  it("does not take the path: lines the node adds for Claude Code as photos (#32)", () => {
    const sent = parseUserText("看这个\n\n<attached_images>\n/tmp/persona/a.jpg\npath: /tmp/persona/a.jpg\n</attached_images>");
    expect(sent).toMatchObject({ text: "看这个", photos: ["/tmp/persona/a.jpg"] });
    // Claude's transcript: the bare path became the picture; only its path: line is left in the block
    const recorded = parseUserText("[Image #1]看这个\n<attached_images>\npath: /tmp/persona/a.jpg\n</attached_images>");
    expect(recorded).toMatchObject({ text: "看这个", photos: [] });
  });

  it("puts a message's photos on its user message", () => {
    const items: Item[] = [{ id: "u1#0", seq: 1, role: "user", kind: "text", text: "<attached_images>\n/tmp/persona/a.jpg\n</attached_images>" }];
    const [seg] = segments(items);
    expect(seg.user?.text).toBe("");
    expect(seg.user?.photos).toEqual(["/tmp/persona/a.jpg"]);
  });

  it("does not repeat a photo across the merged parts of one message", () => {
    const items: Item[] = [
      { id: "u1#0", seq: 1, role: "user", kind: "text", text: "<attached_images>\n/tmp/persona/a.jpg\n</attached_images>" },
      { id: "u1#1", seq: 2, role: "user", kind: "text", text: "再看\n<attached_images>\n/tmp/persona/a.jpg\n/tmp/persona/b.png\n</attached_images>" },
    ];
    const [seg] = segments(items);
    expect(seg.user?.photos).toEqual(["/tmp/persona/a.jpg", "/tmp/persona/b.png"]);
  });

  it("pulls out persona-context tags", () => {
    const p = parseUserText('<persona-context kind="edit" title="Doc &amp; co" url="https://x">sel</persona-context>fix it');
    expect(p.text).toBe("fix it");
    expect(p.contexts).toEqual([{ kind: "edit", title: "Doc & co", url: "https://x", text: "sel" }]);
  });

  it("never renders the hub's interject note as an owner-bubble quote (#6595)", () => {
    const interject =
      '<persona-context kind="context" title="插话" url="">\n用户在你这一轮还没做完时发来下面这条消息。\n</persona-context>\n先停一下，帮我查个东西';
    const p = parseUserText(interject);
    expect(p.text).toBe("先停一下，帮我查个东西");
    expect(p.contexts).toEqual([]);
    // a normal picked context right beside it still parses
    const mixed = `${interject}\n<persona-context kind="context" title="需求文档" url="https://x">摘要</persona-context>`;
    const q = parseUserText(mixed);
    expect(q.text).toBe("先停一下，帮我查个东西");
    expect(q.contexts.map((c) => c.title)).toEqual(["需求文档"]);
    // only the exact interject title is dropped; another context titled differently stays
    expect(parseUserText('<persona-context kind="context" title="插话记录" url="">x</persona-context>问').contexts).toHaveLength(1);
  });

  it("pulls out persona-context tags, alongside older persona-context ones (#3182)", () => {
    const p = parseUserText(
      '<persona-context kind="context" title="新" url="">a</persona-context>\n<persona-context kind="edit" title="旧">b</persona-context>\n问题',
    );
    expect(p.text).toBe("问题");
    expect(p.contexts.map((c) => [c.kind, c.title, c.text])).toEqual([["context", "新", "a"], ["edit", "旧", "b"]]);
    expect(parseUserText("<persona-context>\n快照\n</persona-context>\n你好").text).toBe("你好");
  });

  it("leaves only the request of a Feishu /agent prompt as the text", () => {
    const raw =
      '<persona-context kind="instructions" title="给 Agent 的说明">\n示例用户在飞书「发布群」里用 /agent 叫你。\n\n## 怎么做\n\n- 做完回复一次。\n</persona-context>\n' +
      '<persona-context kind="context" title="请求的位置">\n位置：群聊「发布群」\n</persona-context>\n' +
      '<persona-context kind="context" title="项目 · 发布">\n</persona-context>\n看下构建为什么挂了';
    const p = parseUserText(raw);
    expect(p.text).toBe("看下构建为什么挂了");
    expect(p.contexts.map((c) => [c.kind, c.title])).toEqual([["instructions", "给 Agent 的说明"], ["context", "请求的位置"], ["context", "项目 · 发布"]]);
    expect(p.contexts[2].text).toBe("");
  });
});

it("toolChip shows the first line of a command", () => {
  expect(toolChip(tool(1, "x"))).toBe("ls -la");
});

describe("chatLines", () => {
  const notice = (seq: number, text: string, extra: Partial<Item> = {}): Item => ({ id: `n${seq}#0`, seq, role: "system", kind: "notice", text, ...extra });
  const recap = (seq: number, text: string): Item => ({ id: `rc${seq}#0`, seq, role: "system", kind: "recap", text });
  const wake = (seq: number, title: string, status = "completed"): Item =>
    notice(seq, `后台命令完成 · ${title}`, { wake: true, task: { kind: "shell", status, title, task_id: `bk${seq}` } });
  const kinds = (ls: ReturnType<typeof chatLines>) => ls.map((l) => l.kind);

  it("draws one segment per message, the live one last", () => {
    const ls = chatLines([u(1, "hi"), a(2, "ok"), u(3, "next"), tool(4, "x")], { live: true });
    expect(kinds(ls)).toEqual(["segment", "segment"]);
    expect(ls.map((l) => l.kind === "segment" && l.live)).toEqual([false, true]);
  });

  it("draws a finished exchange of several messages message by message (#2512)", () => {
    const items = [u(1, "first"), tool(2, "x"), res(3, "x"), u(4, "also this", { queued: true }), a(5, "partial"), tool(6, "y"), res(7, "y"), a(8, "all done")];
    const ls = chatLines(items);
    expect(kinds(ls)).toEqual(["segment", "segment"]);
    const [one, two] = ls.map((l) => (l.kind === "segment" ? l.seg : null));
    expect(one?.user?.text).toBe("first");
    expect(one?.tools).toBe(1);
    expect(two?.user?.text).toBe("also this");
    expect(two?.answer?.text).toBe("all done");
    expect(two?.steps.map((s) => s.kind)).toEqual(["text", "tool"]);
    // the live exchange as well
    expect(kinds(chatLines(items, { live: true }))).toEqual(["segment", "segment"]);
  });

  it("keeps an interrupted notice after the exchange", () => {
    const ls = chatLines([u(1, "go"), tool(2, "x"), notice(3, "已中断")]);
    expect(kinds(ls)).toEqual(["segment", "notice"]);
  });

  it("classifies a settled whitespace reply without changing live, answered, or queued turns", () => {
    const first = u(1, "Check this");
    const thinking: Item = { id: "t2#0", seq: 2, role: "assistant", kind: "thinking", text: "Considering it" };
    const blank = a(3, " \n\t");
    const empty = (items: Item[], live = false) => {
      const [line] = chatLines(items, { live });
      return line.kind === "segment" && line.seg.emptyResponse === true;
    };
    expect(empty([first, thinking, blank])).toBe(true);
    expect(empty([first, thinking, blank], true)).toBe(false);
    expect(empty([first, thinking, a(3, "An answer")])).toBe(false);
    expect(empty([first, thinking, a(3, "API Error: 503 Unavailable")])).toBe(false);
    expect(empty([thinking, blank])).toBe(false);
    expect(empty([u(1, "Queued", { queued: true }), blank])).toBe(false);
    expect(empty([first, thinking, blank, notice(4, "Interrupted")])).toBe(false);
    expect(empty([first, thinking, blank, notice(4, "中断しました")])).toBe(false);
    expect(empty([first, thinking, blank, notice(4, "已中断")])).toBe(false);
  });

  it("calls no turn empty while the items may predate the transcript (a cached page before its newest read)", () => {
    const items = [u(1, "Check this"), tool(2, "search"), res(3, "search")];
    const empty = (opts: { stale?: boolean }) => {
      const [line] = chatLines(items, opts);
      return line.kind === "segment" && line.seg.emptyResponse === true;
    };
    expect(empty({})).toBe(true);
    expect(empty({ stale: true })).toBe(false);
  });

  it("omits the empty-response fallback on the latest turn while waiting on user input (needsYou)", () => {
    const firstTurn = [u(1, "Earlier question"), { id: "t2#0", seq: 2, role: "assistant", kind: "thinking", text: "Hmm" } as Item];
    const latestTurn = [u(3, "Need approval"), tool(4, "persona_settings")];
    const lines = chatLines([...firstTurn, ...latestTurn], { needsYou: true });
    expect(lines).toHaveLength(2);
    expect(lines[0].kind === "segment" && lines[0].seg.emptyResponse).toBe(true);
    expect(lines[1].kind === "segment" && lines[1].seg.emptyResponse).toBeUndefined();
    expect(lines[1].kind === "segment" && lines[1].live).toBe(false);

    const settled = chatLines([...firstTurn, ...latestTurn], { needsYou: false });
    expect(settled[1].kind === "segment" && settled[1].seg.emptyResponse).toBe(true);
  });

  it("omits the empty-response fallback on the latest turn while session state is unknown (statusUnknown)", () => {
    const firstTurn = [u(1, "Earlier question"), { id: "t2#0", seq: 2, role: "assistant", kind: "thinking", text: "Hmm" } as Item];
    const latestTurn = [u(3, "Latest question"), tool(4, "read_file")];
    const lines = chatLines([...firstTurn, ...latestTurn], { statusUnknown: true });
    expect(lines).toHaveLength(2);
    expect(lines[0].kind === "segment" && lines[0].seg.emptyResponse).toBe(true);
    expect(lines[1].kind === "segment" && lines[1].seg.emptyResponse).toBeUndefined();
    expect(lines[1].kind === "segment" && lines[1].live).toBe(false);

    const settled = chatLines([...firstTurn, ...latestTurn], { statusUnknown: false });
    expect(settled[1].kind === "segment" && settled[1].seg.emptyResponse).toBe(true);
  });

  it("omits the empty-response fallback after a silent supervision finish", () => {
    const finish = { ...tool(2, "finish"), tool_name: "mcp__persona__supervision_finish", tool_input: { visibility: "silent" } };
    const receipt = { ...res(3, "finish"), text: '{"visibility":"silent"}' };
    for (const tools of [[finish], [finish, receipt]]) {
      const [line] = chatLines([u(1, "Check this"), ...tools, a(4, "\n")]);
      expect(line.kind === "segment" && line.seg.emptyResponse).toBeUndefined();
    }
  });

  it("omits the empty-response fallback for /compact and notice-answered local slash commands", () => {
    const empty = (items: Item[]) => {
      const [line] = chatLines(items);
      return line.kind === "segment" && line.seg.emptyResponse === true;
    };
    const compactWithArgs = u(1, "/compact Write every compaction summary in English.");
    const compactedStdout = notice(2, "Compacted PreCompact completed successfully");
    expect(empty([compactWithArgs, compactedStdout])).toBe(false);
    expect(empty([compactWithArgs])).toBe(false);
    expect(empty([u(1, "/compact")])).toBe(false);
    expect(empty([u(1, "/model opus"), notice(2, "Set model to Opus")])).toBe(false);
    expect(empty([u(1, "/cost"), notice(2, "Total cost: $0.04")])).toBe(false);

    // Model-invoking slash commands, file paths, and ordinary messages still report an empty response.
    const thinking: Item = { id: "t2#0", seq: 2, role: "assistant", kind: "thinking", text: "Reviewing" };
    expect(empty([u(1, "/review")])).toBe(true);
    expect(empty([u(1, "/review"), thinking, a(3, " ")])).toBe(true);
    expect(empty([u(1, "/review"), notice(2, "API Error: 503 Unavailable")])).toBe(true);
    expect(empty([u(1, "/compact/notes.md")])).toBe(true);
    expect(empty([u(1, "/etc/hosts"), notice(2, "Checked")])).toBe(true);
    expect(empty([u(1, "Please run /compact")])).toBe(true);
  });

  // #3428: the node's notices for switching ultracode (transcript/ultracode.go) are ordinary notices
  it("keeps the ultracode on / off notices as notices", () => {
    for (const text of ["已开启 ultracode（只对本会话）", "已关闭 ultracode"]) {
      const [line] = chatLines([u(1, "go"), tool(2, "x"), notice(3, text), a(4, "ok")]);
      expect(line.kind === "segment" && line.seg.steps.map((st) => st.kind === "notice" ? st.item.text : st.kind)).toEqual(["tool", text]);
    }
  });

  it("folds recaps in a row into one line", () => {
    const ls = chatLines([u(1, "go"), a(2, "done"), recap(3, "one"), recap(4, "two"), u(5, "back"), a(6, "ok"), recap(7, "three")]);
    expect(kinds(ls)).toEqual(["segment", "recaps", "segment", "recaps"]);
    expect(ls[1].kind === "recaps" && ls[1].items.map((i) => i.text)).toEqual(["one", "two"]);
    expect(ls[1].id).toBe("rc3#0");
  });

  it("opens the newest recap as the chat's last line, in the flow", () => {
    const one = chatLines([u(1, "go"), a(2, "done"), recap(3, "one")]);
    const last = one[one.length - 1];
    const shown = last.kind === "recaps" ? last.items[0] : undefined;
    expect(openRecap(one, shown)).toEqual({ line: last.id, earlier: [], newest: shown });
    expect(openRecap(one, undefined)).toBeUndefined();
    // folded with older ones: the newest opens, the rest stay a chip in the same line
    const two = chatLines([u(1, "go"), a(2, "done"), recap(3, "one"), recap(4, "two")]);
    const l2 = two[two.length - 1];
    const open = openRecap(two, l2.kind === "recaps" ? l2.items[1] : undefined);
    expect(open?.line).toBe(l2.id);
    expect(open?.earlier.map((i) => i.text)).toEqual(["one"]);
    expect(open?.newest.text).toBe("two");
    expect(openRecap(two, l2.kind === "recaps" ? l2.items[0] : undefined)).toBeUndefined();
    // a recap that is not the newest thing is not open
    const mid = chatLines([u(1, "go"), a(2, "done"), recap(3, "one"), u(4, "back"), a(5, "ok")]);
    expect(openRecap(mid, mid[1].kind === "recaps" ? mid[1].items[0] : undefined)).toBeUndefined();
  });

  // #9140: a background task that wakes the idle agent starts an exchange of its own. The turn before it had ended, so
  // its last words stay its answer; the chat still ends on the newest words (#3783), now the woken exchange's.
  const answers = (ls: ReturnType<typeof chatLines>) => ls.flatMap((l) => (l.kind === "segment" && l.seg.answer ? [l.seg.answer.text] : []));
  const seg = (l: ReturnType<typeof chatLines>[number] | undefined) => (l?.kind === "segment" ? l.seg : undefined);

  it("keeps the answer to a newer question when a background task reports back after it (#9140)", () => {
    // the issue's timeline: A's background check is still running, B is asked and answered, then three notifications
    const items = [
      u(1, "Check which trains run on Saturday."),
      tool(2, "a"),
      res(3, "a"),
      a(4, "The timetable check is running in the background."),
      u(5, "What should I pack for two days?"),
      a(6, "Pack a rain jacket, two shirts and walking shoes."),
      wake(7, "Timetable check"),
      tool(8, "b"),
      res(9, "b"),
      a(10, "First result: the 08:10 runs on Saturday."),
      wake(11, "Timetable check"),
      a(12, "Second result: the 10:40 runs too."),
      wake(13, "Timetable check"),
      tool(14, "c"),
      res(15, "c"),
      a(16, "All done: three trains run on Saturday."),
      u(17, "Thanks. Book the 08:10."),
    ];
    const ls = chatLines(items);
    expect(kinds(ls)).toEqual(["segment", "segment", "notice", "segment", "notice", "segment", "notice", "segment", "segment"]);
    // four final texts between the two messages: each is the answer of its own exchange
    expect(answers(ls)).toEqual([
      "The timetable check is running in the background.",
      "Pack a rain jacket, two shirts and walking shoes.",
      "First result: the 08:10 runs on Saturday.",
      "Second result: the 10:40 runs too.",
      "All done: three trains run on Saturday.",
    ]);
    // the answer to B is no step of any work summary
    expect(ls.some((l) => seg(l)?.steps.some((s) => s.kind === "text" && s.item.text?.startsWith("Pack a rain jacket")))).toBe(false);
    // each woken exchange opens with its notification and holds only its own work
    expect(ls[2].kind === "notice" && ls[2].item.id).toBe("n7#0");
    expect(seg(ls[3])?.user).toBeUndefined();
    expect(seg(ls[3])?.steps.map((s) => s.id)).toEqual(["t8#0"]);
    expect(seg(ls[5])?.steps).toEqual([]);
    expect(seg(ls[7])?.steps.map((s) => s.id)).toEqual(["t14#0"]);
    expect(seg(ls[1])?.steps).toEqual([]);
  });

  it("answers a woken exchange with its own words and leaves the exchange before it alone", () => {
    const items = [
      u(1, "run the tests in the background"),
      a(2, "started, I will tell you when it ends — this is the long one"),
      wake(3, "tests"),
      tool(4, "x"),
      res(5, "x"),
      a(6, "all green"),
    ];
    const ls = chatLines(items);
    expect(kinds(ls)).toEqual(["segment", "notice", "segment"]);
    expect(answers(ls)).toEqual(["started, I will tell you when it ends — this is the long one", "all green"]);
    expect(seg(ls[2])?.steps.map((s) => s.id)).toEqual(["t4#0"]);
  });

  it("keeps a long-running agent's newest words at the bottom of a long history", () => {
    const long = "a full summary of everything so far. ".repeat(60);
    const items: Item[] = [u(1, "keep going until it is merged"), a(2, long)];
    let seq = 3;
    for (let n = 0; n < 8; n++) {
      items.push(wake(seq++, `wait ${n}`), tool(seq++, `t${n}`), res(seq++, `t${n}`), a(seq++, `update ${n}`));
    }
    const ls = chatLines(items);
    expect(seg(ls[ls.length - 1])?.answer?.text).toBe("update 7");
    // the summary stays the answer of the turn that wrote it, and every update the answer of its own
    expect(answers(ls)).toEqual([long, ...Array.from({ length: 8 }, (_, n) => `update ${n}`)]);
    expect(ls.filter((l) => l.kind === "notice")).toHaveLength(8);
  });

  it("shows the work a wake started as live under its notice, before and after its first step", () => {
    const before = [u(1, "watch the deploy"), a(2, "watching it"), wake(3, "deploy")];
    // woken, nothing of the new work here yet: a live work row of its own, the answer before it untouched
    const woken = chatLines(before, { live: true });
    expect(kinds(woken)).toEqual(["segment", "notice", "segment"]);
    expect(woken[0].kind === "segment" && woken[0].live).toBe(false);
    expect(seg(woken[0])?.answer?.text).toBe("watching it");
    expect(woken[2].kind === "segment" && woken[2].live).toBe(true);
    expect(woken.some((line) => line.id === "live")).toBe(false);
    // its row keeps its key as steps come and when its words land
    const working = chatLines([...before, tool(4, "x"), res(5, "x")], { live: true });
    expect(working[2].id).toBe(woken[2].id);
    expect(seg(working[2])?.id).toBe(seg(woken[2])?.id);
    const done = chatLines([...before, tool(4, "x"), res(5, "x"), a(6, "deployed")]);
    expect(done[2].id).toBe(woken[2].id);
    expect(answers(done)).toEqual(["watching it", "deployed"]);
  });

  it("puts work a wake started after the recap that came before it", () => {
    const ls = chatLines([u(1, "go"), a(2, "started the build, waiting for it"), recap(3, "waiting for the build"), wake(4, "build"), a(5, "ok")]);
    expect(kinds(ls)).toEqual(["segment", "recaps", "notice", "segment"]);
    expect(answers(ls)).toEqual(["started the build, waiting for it", "ok"]);
  });

  it("marks the woken exchange live when a wake arrives after a recap", () => {
    const ls = chatLines([u(1, "go"), a(2, "waiting"), recap(3, "context"), wake(4, "build"), tool(5, "x")], { live: true });
    expect(kinds(ls)).toEqual(["segment", "recaps", "notice", "segment"]);
    expect(ls[0].kind === "segment" && ls[0].live).toBe(false);
    expect(ls[3].kind === "segment" && ls[3].live).toBe(true);
    expect(ls.some((line) => line.id === "live")).toBe(false);
  });

  it("starts an exchange at a subagent's report that woke the agent, drawn under its call", () => {
    const launch: Item = { id: "t2#0", seq: 2, role: "assistant", kind: "tool_use", tool_use_id: "agent", tool_name: "Agent", tool_input: { description: "Review", run_in_background: true } };
    const report: Item = { id: "r5#0", seq: 5, role: "user", kind: "tool_result", tool_use_id: "agent", text: "Review finished.", wake: true, task: { kind: "agent", status: "completed" } };
    const ls = chatLines([u(1, "review it"), launch, { ...res(3, "agent"), text: "Async agent launched successfully" }, a(4, "The review is running."), report, a(6, "The review found nothing.")]);
    // no notice line: the report belongs to the call that launched it
    expect(kinds(ls)).toEqual(["segment", "segment"]);
    expect(answers(ls)).toEqual(["The review is running.", "The review found nothing."]);
  });

  it("keeps a message typed while the woken agent works in the woken exchange", () => {
    const ls = chatLines([u(1, "go"), a(2, "started"), wake(3, "build"), tool(4, "x"), res(5, "x"), u(6, "also check the logs", { queued: true }), a(7, "build is up, logs are clean")]);
    expect(kinds(ls)).toEqual(["segment", "notice", "segment", "segment"]);
    expect(answers(ls)).toEqual(["started", "build is up, logs are clean"]);
    expect(seg(ls[3])?.user?.text).toBe("also check the logs");
  });

  it("opens work a wake started on its own with the wake notice", () => {
    const ls = chatLines([wake(1, "build"), tool(2, "x"), res(3, "x"), a(4, "build is up")]);
    expect(kinds(ls)).toEqual(["notice", "segment"]);
    expect(ls[1].kind === "segment" && ls[1].seg.answer?.text).toBe("build is up");
  });

  it("orders a #-1 wake notice before non-numeric same-seq steps even when merged late", () => {
    const prior = [u(1, "run build"), a(2, "Complete first reply.")];
    const resumedThinking: Item = { id: "3@b#thinking", seq: 3, role: "assistant", kind: "thinking", text: "Checking build output" };
    const resumedText: Item = { id: "3@b#text", seq: 3, role: "assistant", kind: "text", text: "Checking next step..." };
    const lateWake: Item = { id: "2@a#-1", seq: 3, role: "system", kind: "notice", wake: true, text: "Background task updated" };
    // Simulate resumed non-numeric steps landing before the #-1 wake notice at the same seq.
    const merged = mergeItems([...prior, resumedThinking, resumedText], [lateWake]);
    expect(merged.map((i) => i.id)).toEqual(["u1#0", "a2#0", "2@a#-1", "3@b#thinking", "3@b#text"]);
    const ls = chatLines(merged, { live: true });
    expect(kinds(ls)).toEqual(["segment", "notice", "segment"]);
    expect(ls[0].kind === "segment" && ls[0].live).toBe(false);
    expect(seg(ls[0])?.answer?.text).toBe("Complete first reply.");
    expect(ls[2].kind === "segment" && ls[2].live).toBe(true);
    expect(seg(ls[2])?.answer?.text).toBe("Checking next step...");
    // A fold starting at the same seq with a non-numeric id must not claim the #-1 wake notice.
    const sameSeqFold: Item = { id: "fold:3@b#thinking", seq: 3, role: "assistant", kind: "fold", fold: { items: 2, tools: 1, thoughts: 1, through_seq: 3, through_id: "3@b#tool0" } };
    expect(insideFold(lateWake, sameSeqFold)).toBe(false);
  });

  it("turns API errors into cards and folds retries of the same one", () => {
    const err = "API Error: 502 upstream error, try again";
    const ls = chatLines([u(1, "plan the day"), a(2, err), u(3, "继续"), a(4, err), u(5, "继续"), a(6, err)]);
    expect(kinds(ls)).toEqual(["segment", "apiError"]);
    const e = ls[1].kind === "apiError" ? ls[1] : null;
    expect(e?.count).toBe(3);
    expect(e?.info.status).toBe(502);
    expect(e?.info.transient).toBe(true);
    // the message before it stays, without the error as its answer
    expect(ls[0].kind === "segment" && ls[0].seg.answer).toBeUndefined();
  });

  it("does not fold a different error, nor one after real work", () => {
    const ls = chatLines([u(1, "a"), a(2, "API Error: 502 bad gateway"), u(3, "继续"), a(4, "API Error: 401 authentication_error")]);
    expect(kinds(ls)).toEqual(["segment", "apiError", "segment", "apiError"]);
    const ls2 = chatLines([u(1, "a"), a(2, "API Error: 502 x"), u(3, "do something else"), tool(4, "x"), res(5, "x"), a(6, "API Error: 502 x")]);
    expect(kinds(ls2)).toEqual(["segment", "apiError", "segment", "apiError"]);
  });

  it("marks cross-session messages and loop ticks", () => {
    const ls = chatLines([u(1, "hello from the other side", { from: "build-bot" }), a(2, "ok"), u(3, "/loop check CI"), a(4, "CI is green")]);
    const segs = ls.flatMap((l) => (l.kind === "segment" ? [l.seg] : []));
    expect(segs[0].user?.from).toBe("build-bot");
    expect(isLoopTick(segs[1].user!)).toBe(true);
  });

  it("shows the work row while working with nothing of the turn yet", () => {
    const ls = chatLines([u(1, "go"), a(2, "done"), recap(3, "r")], { live: true });
    expect(kinds(ls)).toEqual(["segment", "recaps", "segment"]);
    expect(ls[2].kind === "segment" && ls[2].live).toBe(true);
  });
});

it("toolLabel names an MCP tool by its own name, whatever the server is called", () => {
  expect(toolLabel("mcp__github__create_issue")).toBe("create issue");
  expect(toolLabel("mcp__plugin_lark_docs__fetch_doc")).toBe("fetch doc");
  expect(toolLabel("mcp__x")).toBe("x");
  expect(toolLabel("Bash")).toBe("运行");
});

it("toolDisplayName shows Persona's own MCP server (still id `persona`) as Persona, other servers' tools in words", () => {
  expect(toolDisplayName("mcp__persona__agent_read")).toBe("Persona · agent read");
  expect(toolDisplayName("mcp__plugin_persona__agent_send")).toBe("Persona · agent send");
  expect(toolDisplayName("mcp__persona")).toBe("Persona");
  expect(toolDisplayName("mcp__github__create_issue")).toBe("GitHub · Create issue");
  expect(toolDisplayName("mcp__plugin_lark_docs__fetch_doc")).toBe("Lark Docs · Fetch doc");
  expect(toolDisplayName("mcp__persona_extra__x")).toBe("Persona Extra · X");
  expect(toolDisplayName("Bash")).toBe("Bash");
  expect(toolDisplayName(undefined)).toBe("工具");
});

it("names read as words, servers as names (iOS ToolNaming, #2986)", () => {
  expect(["create_issue", "createIssue", "list-pull-requests", "getPRComments", "HTTPServerStart", "search_github_repos", "ios_build", "x", ""].map(humanizeTool)).toEqual([
    "Create issue",
    "Create issue",
    "List pull requests",
    "Get PR comments",
    "HTTP server start",
    "Search GitHub repos",
    "iOS build",
    "X",
    "",
  ]);
  expect(["github", "chrome-devtools", "google_drive", "plugin_context7_context7", "claude_ai_Gmail", "ide"].map(mcpServerName)).toEqual([
    "GitHub",
    "Chrome DevTools",
    "Google Drive",
    "Context7",
    "Gmail",
    "IDE",
  ]);
});

it("an unknown tool's subject is its most telling field", () => {
  expect(pickSubject({ title: "T", description: "D" })).toBe("D");
  expect(pickSubject({ path: "/a/b/Store.swift", query: "q" })).toBe("Store.swift");
  expect(pickSubject({ zeta: "z", alpha: " a ", n: 3 })).toBe("a");
  expect(pickSubject({ zeta: "z", zz_persona_clipped: { note: "Omitted 8 array items" } })).toBe("z");
  expect(pickSubject({ values: [["short", 42]], zz_persona_clipped: { note: "Omitted 8 array items" } })).toBe("");
  expect(pickSubject({ n: 3 })).toBe("");
});

it("toolChip says what Claude Code's newer tools were about", () => {
  const call = (name: string, input: Record<string, unknown>): Item => ({ id: "c#0", seq: 1, role: "assistant", kind: "tool_use", tool_name: name, tool_input: input });
  expect(toolChip(call("TaskCreate", { subject: "Write tests", description: "…" }))).toBe("Write tests");
  expect(toolChip(call("TaskUpdate", { taskId: "3", status: "completed" }))).toBe("#3");
  expect(toolChip(call("TaskList", {}))).toBe("");
  expect(toolChip(call("SendMessage", { to: "researcher", summary: "assign task 1", message: "start" }))).toBe("researcher · assign task 1");
  expect(toolChip(call("ToolSearch", { query: "select:WebFetch,Monitor" }))).toBe("WebFetch, Monitor");
  expect(toolChip(call("LSP", { operation: "findReferences", filePath: "/a/ToolViews.swift", line: 120 }))).toBe("ToolViews.swift:120");
  expect(toolChip(call("Monitor", { ws: { url: "wss://events.example.com" } }))).toBe("wss://events.example.com");
  expect(toolChip(call("ReportFindings", { findings: [{ file: "a", summary: "x" }, { file: "b", summary: "y" }] }))).toBe("2 条发现");
  expect(toolChip(call("mcp__github__create_issue", { owner: "o", title: "Crash on launch" }))).toBe("Crash on launch");
});

it("names that match Object's own methods stay words (no table lookup reaches the prototype)", () => {
  const call = (name: string, input: Record<string, unknown>): Item => ({ id: "c#0", seq: 1, role: "assistant", kind: "tool_use", tool_name: name, tool_input: input });
  expect(["constructor_args", "encode_constructor", "toString", "__proto__"].map(humanizeTool)).toEqual(["Constructor args", "Encode constructor", "To string", "Proto"]);
  expect(mcpServerName("constructor")).toBe("Constructor");
  expect(toolDisplayName("mcp__constructor__x")).toBe("Constructor · X");
  expect(toolDisplayName("mcp__foundry__constructor_args")).toBe("Foundry · Constructor args");
  expect(toolLabel("constructor")).toBe("constructor");
  expect(toolChip(call("constructor", { title: "T" }))).toBe("T");
  expect(toolChip(call("valueOf", {}))).toBe("");
  expect(personaToolVerb("mcp__persona__constructor")).toBeNull();
});

describe("persona-file tags", () => {
  it("become files on the message, not text", () => {
    const p = parseUserText('上次的录屏，你看下按钮那里\n\n<persona-file kind="video" path="/tmp/录屏 1.mov" name="录屏.mov" size="25270000" duration="6.0"/>\n<persona-file kind="file" path="/tmp/a&amp;b.pdf"/>');
    expect(p.text).toBe("上次的录屏，你看下按钮那里");
    expect(p.files).toEqual([
      { kind: "video", path: "/tmp/录屏 1.mov", name: "录屏.mov", size: 25270000, duration: 6 },
      { kind: "file", path: "/tmp/a&b.pdf", name: "a&b.pdf", size: undefined, duration: undefined },
    ]);
    expect(parseUserText('<persona-file kind="video" name="x"/>').files).toEqual([]); // no path: nothing to play
  });

  it("formats durations", () => {
    expect([formatDuration(6), formatDuration(65.4), formatDuration(3723)]).toEqual(["0:06", "1:05", "1:02:03"]);
  });
});

describe("paths in answers", () => {
  it("picks absolute paths to media and documents, not code or prose", async () => {
    const { replyPaths, mediaKindOf } = await import("./turns");
    expect(
      replyPaths("视频已生成：`/Users/me/code/weather/media/preview.mp4`。报告在 ~/Desktop/季度 报告.pdf 和 (/tmp/a.png)，代码改了 /Users/me/x/main.swift，网址 https://example.com/x.pdf。"),
    ).toEqual(["/Users/me/code/weather/media/preview.mp4", "/tmp/a.png"]);
    expect(replyPaths("见 ~/notes/plan.docx, 以及 /data/clip.MOV")).toEqual(["~/notes/plan.docx", "/data/clip.MOV"]);
    expect([mediaKindOf("/a/b.MOV"), mediaKindOf("x.m4a"), mediaKindOf("p.heic"), mediaKindOf("r.pdf")]).toEqual(["video", "audio", "image", "file"]);
  });
});

describe("Persona's messages for the model (lib/injected.ts)", () => {
  const notice = "🔔 任务「跑测试」做完了（电脑）\n（这是 Persona 的任务通知，不是用户发的消息。……）\n\nAgent 最后说的：\n全过了。\n\n你来决定要不要告诉用户、怎么说……";
  it("shows the line, never the words for the model", () => {
    const s = segments([u(1, notice), a(2, "测试全过了。")]);
    expect(s[0].user?.injected?.kind).toBe("task_notice");
    expect(s[0].user?.text).toBe("任务「跑测试」做完了");
    expect(s[0].answer?.text).toBe("测试全过了。");
  });
  it("takes the hub's mark", () => {
    const s = segments([u(1, "whatever", { injected: { kind: "followup", state: "daily", line: "定时任务「早报」开始运行" } })]);
    expect(s[0].user?.text).toBe("定时任务「早报」开始运行");
  });
  it("is a line of its own in a finished exchange", () => {
    const lines = chatLines([u(1, notice), u(2, "顺便看看 CI", { queued: true }), a(3, "好")]);
    const segs = lines.filter((l) => l.kind === "segment");
    expect(segs.map((l) => (l.kind === "segment" ? l.seg.user?.injected?.kind ?? l.seg.user?.text : ""))).toEqual(["task_notice", "顺便看看 CI"]);
  });
  it("a message the app sent stays a bubble", () => {
    expect(segments([u(1, notice, { client_msg_id: "c1" })])[0].user?.injected).toBeUndefined();
  });
});

it("personaToolVerb names Persona's own tools by what they do, and nothing else", () => {
  expect(personaToolVerb("mcp__persona__agent_read")).toEqual({ verb: "读取 Agent 对话" });
  expect(personaToolVerb("mcp__plugin_persona__kb_update")).toEqual({ verb: "更新知识库" });
  expect(personaToolVerb("mcp__persona__ask_user")).toEqual({ verb: "向你提问" });
  expect(personaToolVerb("mcp__persona__send_to_agent")).toEqual({ verb: "给 Agent 发消息" });
  expect(personaToolVerb("mcp__persona__conversation_new")).toEqual({ verb: "新建对话" });
  expect(personaToolVerb("mcp__persona__computer_run")).toEqual({ verb: "操作电脑" });
  expect(personaToolVerb("mcp__persona__secret_run")).toEqual({ verb: "使用密码运行" });
  expect(personaToolVerb("mcp__persona__phone_contacts_search")).toEqual({ verb: "iPhone", what: "contacts search" });
  expect(personaToolVerb("mcp__persona__mail_send")).toEqual({ verb: "邮件", what: "send" });
  // no verb of its own: keeps "Called Persona · …"
  expect(personaToolVerb("mcp__persona__something_new")).toBeNull();
  // other servers, and the bare server
  expect(personaToolVerb("mcp__github__agent_read")).toBeNull();
  expect(personaToolVerb("mcp__persona_extra__agent_read")).toBeNull();
  expect(personaToolVerb("mcp__persona")).toBeNull();
  expect(personaToolVerb("agent_read")).toBeNull();
  expect(personaToolVerb(undefined)).toBeNull();
});

it("recognizes Antigravity single-underscore and scoped Persona MCP tool names", () => {
  for (const server of PERSONA_SERVERS) {
    for (const prefix of [`mcp_${server}`, `mcp_${server}_0c671287`, `mcp_${server}_55a1c59d`]) {
      expect(personaToolVerb(`${prefix}_notify_user`)).toEqual(personaToolVerb("mcp__persona__notify_user"));
      expect(toolDisplayName(`${prefix}_mail_send`)).toBe("Persona · mail send");
      expect(supervisionTool(`${prefix}_notify_user`)).toBe("notify_user");
      expect(supervisionTool(`${prefix}_ask_user`)).toBe("ask_user");
      expect(supervisionTool(`${prefix}_task_review`)).toBe("task_review");
      expect(supervisionTool(`${prefix}_supervision_finish`)).toBe("supervision_finish");
    }
  }
  expect(supervisionTool("mcp_persona_other_notify_user")).toBeUndefined();
  const call: Item = { id: "c1", seq: 2, role: "assistant", kind: "tool_use", tool_use_id: "call_1", tool_name: "mcp_persona_0c671287_notify_user", tool_input: { message: "Ready" } };
  const ok: Item = { id: "r1", seq: 3, role: "user", kind: "tool_result", tool_use_id: "call_1", text: "Notification sent." };
  expect(supervisionVisible([u(1, "Check"), call, ok], new Map([["call_1", ok]]), false)).toBe(true);
});
