import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Item } from "../../lib/types";
import type { Step } from "../../lib/turns";
import type { ToolStep, WorkGroupNode } from "../../lib/work-groups";
import { ActionRow, argumentLines, computeIntralineDiff, DIFF_FOLD_LINES, DiffBlocks, diffStats, editDiffs, foldDiffs, formatUnifiedDiff, inlineInput, lineDiff, presentAction, truncateParam } from "./ActionRow";
import { inOpenedFold, MAX_EXPANDED_ENTRIES, openFoldRange } from "./expanded";
import { PlanCard } from "./PlanCard";
import { Answer, bubbleLines, SegmentView, UserBubble } from "./SegmentView";
import { SubagentCard } from "./SubagentCard";
import { LiveThinking } from "./ThinkingCard";
import { ThinkingRow } from "./ThinkingRow";
import { ClusterRow, clusterWorkChildren, summarizeWorkSteps, WorkGroup, type WorkProps } from "./WorkGroup";

const T0 = Date.parse("2026-11-04T10:00:00Z");
const at = (s: number) => new Date(T0 + s * 1000).toISOString();
const use = (id: string, name: string, input: Record<string, unknown>, ts = 0): Item => ({ id: `${id}#0`, seq: Number(id.replace(/\D/g, "")) || 1, role: "assistant", kind: "tool_use", tool_name: name, tool_use_id: `c-${id}`, tool_input: input, ts: at(ts) });
const res = (id: string, text = "ok", ts = 1): Item => ({ id: `${id}-r#0`, seq: 1, role: "user", kind: "tool_result", tool_use_id: `c-${id}`, text, ts: at(ts) });
const tool = (id: string, name: string, input: Record<string, unknown>, done = true): ToolStep => ({ kind: "tool", id: `${id}#0`, use: use(id, name, input), result: done ? res(id) : undefined, images: [] });
const props: WorkProps = { mid: "m", sid: "s", scope: "t:", expandFold: async () => {}, agentStatus: () => "completed" };
const group = (children: Step[], live: boolean, durationMs?: number): WorkGroupNode => ({ kind: "group", id: `work-${live}`, children: children.map((step) => ({ kind: "step", step })), live, durationMs, startedAt: T0 });

describe("WorkGroup", () => {
  it("its header, a subagent card's and a plan's are a finger tall on a touch screen (#1996)", () => {
    const coarse = "[@media(pointer:coarse)]:min-h-10";
    const head = renderToStaticMarkup(<WorkGroup g={group([tool("t1", "Grep", { pattern: "x" })], false, 3000)} p={props} />);
    // the margin gives back what the height adds, so the group does not move
    expect(head).toContain(`[@media(pointer:coarse)]:-my-[7.5px] ${coarse}`);
    expect(renderToStaticMarkup(<SubagentCard step={tool("a1", "Task", { description: "查", prompt: "p" })} status="completed" mid="m" />)).toContain(coarse);
    expect(renderToStaticMarkup(<PlanCard id="p" todos={[{ content: "读", status: "pending" }]} />)).toContain(coarse);
  });

  it("while live: only a collapsed summary, spinner and clock, with no preview", () => {
    const html = renderToStaticMarkup(<WorkGroup g={group([tool("t1", "Bash", { command: "ls", description: "看目录" }, false)], true)} p={props} />);
    expect(html).toContain('data-work-group="live"');
    expect(html).toContain(">正在工作…<");
    expect(html).toMatch(/font-mono text-\[12px\][^>]*>(\d+[smh]? ?)+<|font-mono text-\[12px\][^>]*>[\d天小时分 ]+</);
    expect(html).not.toContain("data-live-preview");
    expect(html).not.toContain(">看目录<");
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain("data-work-detail"); // a lazy Collapse: nothing until it opens
  });

  it("done: no leading layers icon; the completed summary's disclosure follows its label", () => {
    const html = renderToStaticMarkup(<WorkGroup g={group([tool("t1", "Bash", { command: "ls" })], false, 65_000)} p={props} />);
    expect(html).toContain(">已工作 1m 5s<");
    expect(html).toMatch(/>已工作 1m 5s<\/span><svg/);
    expect(html.match(/<svg/g)).toHaveLength(1);
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain("data-live-preview");
    // Only one svg in the completed top-level header: the disclosure chevron right after the heading (no leading layers icon, no flex-1 spacer)
    expect(html.match(/<svg /g)).toHaveLength(1);
    expect(html).not.toContain("flex-1");
    expect(html.indexOf("已工作 1m 5s")).toBeLessThan(html.indexOf("<svg "));
  });

  it("done: a top-level header keeps the elapsed heading without a tool-count suffix (#16777)", () => {
    const inner = group([tool("r1", "Read", { file_path: "/a.ts" }), tool("e1", "Edit", { file_path: "/a.ts", old_string: "x", new_string: "y" })], false, 2000);
    const outer: WorkGroupNode = { kind: "group", id: "work-outer", children: [inner, { kind: "step", step: tool("c1", "Bash", { command: "echo ok" }) }], live: false, durationMs: 5000 };
    const html = renderToStaticMarkup(<WorkGroup g={outer} p={props} />);
    expect(html).toContain(">已工作 5s<");
    expect(html).toMatch(/>已工作 5s<\/span><svg/);
    expect(html).not.toContain("data-work-summary");
    expect(html).not.toContain("读取 1 个文件、编辑 1 处、运行 1 条命令");

    const liveHtml = renderToStaticMarkup(<WorkGroup g={group([tool("t1", "Bash", { command: "ls" }, false), tool("t2", "Read", { file_path: "/a" })], true)} p={props} />);
    expect(liveHtml).toContain("已执行 2 步");
  });

  it("every nonempty live group can be manually opened and never previews tools", () => {
    const five = [1, 2, 3, 4, 5].map((n) => tool(`t${n}`, "Bash", { command: `echo ${n}` }));
    expect(renderToStaticMarkup(<WorkGroup g={group(five, true)} p={props} />)).not.toMatch(/<button[^>]*disabled=""/);
    const six = [...five, tool("t6", "Bash", { command: "echo 6" })];
    const html = renderToStaticMarkup(<WorkGroup g={group(six, true)} p={props} />);
    expect(html).not.toMatch(/^<div[^>]*><button[^>]*disabled/);
    expect(html).not.toContain("echo 1");
    expect(html).not.toContain("echo 6");
  });

  it("thinking-only nested group omits the fallback layers icon and keeps spinner when live (#14866)", () => {
    const thought: Step = { kind: "thinking", id: "th#0", item: { id: "th#0", seq: 1, role: "assistant", kind: "thinking", text: "Checking details" } };
    const doneHtml = renderToStaticMarkup(<WorkGroup g={group([thought], false, 1000)} p={{ ...props, nested: true }} />);
    expect(doneHtml).toContain(">已工作 1s<");
    expect(doneHtml.match(/<svg\b/g)).toHaveLength(1); // chevron only, no leading layers icon
    expect(doneHtml).not.toContain("border-l-2");
    expect(doneHtml).toContain("pl-3");

    const liveHtml = renderToStaticMarkup(<WorkGroup g={group([thought], true)} p={{ ...props, nested: true }} />);
    expect(liveHtml).toContain("animation:spin");
  });
});

describe("ActionRow", () => {
  it("a command with a description is that sentence, no verb; the command is its title", () => {
    const p = presentAction(use("t1", "Bash", { command: "npm test", description: "跑单测" }));
    expect(p).toEqual({ param: { text: "npm test", title: "跑单测" } });
    const html = renderToStaticMarkup(<ActionRow step={tool("t1", "Bash", { command: "npm test", description: "跑单测" })} mid="m" sid="s" />);
    expect(html).toContain("py-[3px]");
    expect(html).toContain("rounded-[8px]");
    expect(html).not.toContain("运行命令");
  });

  it("one without: 运行命令 and the command on a quieter line under it", () => {
    const html = renderToStaticMarkup(<ActionRow step={tool("t1", "Bash", { command: "git status --short" })} mid="m" sid="s" />);
    expect(html).toContain(">git status --short<");
    expect(html).not.toContain("pl-[30px]");
  });

  it("a file is a chip with its name only; long parameters stop at 60 characters", () => {
    const html = renderToStaticMarkup(<ActionRow step={tool("t1", "Read", { file_path: "/Users/me/code/app/src/views/Home.tsx" })} mid="m" sid="s" />);
    expect(html).toContain(">读取<");
    expect(html).toContain("data-file-chip");
    expect(html).toContain(">Home.tsx<");
    expect(html).not.toContain(">/Users/me/code/app/src/views/Home.tsx<");
    expect(truncateParam("x".repeat(80))).toHaveLength(60);
    expect(truncateParam("x".repeat(80)).endsWith("...")).toBe(true);
    expect(presentAction(use("g", "Grep", { pattern: "y".repeat(100) })).param!.text).toHaveLength(60);
  });

  it("verbs follow cindy's table", () => {
    const verb = (name: string, input: Record<string, unknown> = {}) => presentAction(use("v", name, input)).verb;
    expect([verb("Edit", { file_path: "/a" }), verb("Write", { file_path: "/a" }), verb("Grep", { pattern: "x" }), verb("WebFetch", { url: "u" }), verb("mcp__github__create_issue")]).toEqual(["编辑", "创建", "搜索", "访问", "调用"]);
    expect(presentAction(use("v", "mcp__github__create_issue", {})).param!.text).toBe("GitHub · Create issue");
    expect(presentAction(use("v", "mcp__github__create_issue", { title: "Crash" })).param!.text).toBe("GitHub · Create issue · Crash");
    expect(presentAction(use("v", "mcp__persona__unknown_tool", {})).param).toEqual({ text: "Persona · unknown tool", title: "mcp__persona__unknown_tool" });
  });

  it("Claude Code's newer tools say what they did, then what about (#2986)", () => {
    expect(presentAction(use("n", "TaskCreate", { subject: "Write tests", description: "…" }))).toEqual({ verb: "添加待办", param: { text: "Write tests", title: "Write tests" } });
    expect(presentAction(use("n", "ListAgents", {}))).toEqual({ verb: "查看 Agent 列表" });
    expect(presentAction(use("n", "Monitor", { command: "tail -f x", description: "errors in x" })).verb).toBe("监视");
    expect(presentAction(use("n", "PowerShell", { command: "Get-ChildItem" }))).toEqual({ param: { text: "Get-ChildItem", title: "Get-ChildItem" } });
    expect(inlineInput(use("n", "Monitor", { command: "tail -f x", description: "d" }))).toBe("tail -f x");
  });

  it("a tool named like one of Object's methods reads as any other unknown tool", () => {
    expect(presentAction(use("n", "constructor", {}))).toEqual({ verb: "调用", param: { text: "constructor", title: undefined } });
    expect(presentAction(use("n", "hasOwnProperty", { title: "T" })).param!.text).toBe("hasOwnProperty · T");
    expect(presentAction(use("n", "mcp__foundry__constructor_args", {})).param!.text).toBe("Foundry · Constructor args");
  });

  it("any other call opens to its arguments as key: value lines, not JSON", () => {
    expect(argumentLines({ repo: "persona", title: "Crash", labels: ["bug", "ios"], meta: { a: 1 }, blank: " ", none: [] })).toBe(
      "title: Crash\nlabels: bug, ios\nmeta: {\"a\":1}\nrepo: persona",
    );
    expect(inlineInput(use("n", "mcp__github__create_issue", { title: "Crash", body: "Steps" }))).toBe("title: Crash\nbody: Steps");
  });

  it("Persona's own tools read as their verb, then what they were given", () => {
    expect(presentAction(use("p", "mcp__persona__agent_read", {}))).toEqual({ verb: "读取 Agent 对话" });
    expect(presentAction(use("p", "mcp__persona__kb_search", { query: "周报" }))).toEqual({ verb: "搜索知识库", param: { text: "周报", title: "mcp__persona__kb_search" } });
    expect(presentAction(use("p", "mcp__persona__phone_contacts_search", { query: "Alice" }))).toEqual({ verb: "iPhone", param: { text: "contacts search · Alice", title: "mcp__persona__phone_contacts_search" } });
    expect(presentAction(use("p", "mcp__github__create_issue", {})).verb).toBe("调用");
  });

  it("an edit shows its +N -N from a line diff (cindy's ASCII minus); a running call a spinner in its 16 px slot", () => {
    const edit = tool("e1", "Edit", { file_path: "/a.ts", old_string: "a\nb\nc", new_string: "a\nB\nc\nd" });
    expect(diffStats(editDiffs(edit.use)!)).toEqual({ add: 2, del: 1 });
    const html = renderToStaticMarkup(<ActionRow step={edit} mid="m" sid="s" />);
    expect(html).toContain(">+2<");
    expect(html).toContain(">-1<");
    expect(renderToStaticMarkup(<ActionRow step={tool("r", "Bash", { command: "sleep 9" }, false)} running mid="m" sid="s" />)).toMatch(/h-\[18px\] w-4[^>]*><span[^>]*animation:spin/);
  });

  it("lineDiff keeps what did not change and pairs what did", () => {
    const sign = (l: { kind: string; text: string }) => ({ same: " ", del: "-", add: "+" })[l.kind] + l.text;
    expect(lineDiff("a\nb\nc", "a\nx\nc").map(sign)).toEqual([" a", "-b", "+x", " c"]);
    expect(lineDiff("", "n1\nn2").map((l) => l.kind)).toEqual(["add", "add"]);
    expect(lineDiff("o", "").map((l) => l.kind)).toEqual(["del"]);
  });

  it("a long diff shows its first 40 lines on a phone, cut inside the change they end in (#1996)", () => {
    const lines = (n: number) => lineDiff("", Array.from({ length: n }, (_, i) => `l${i}`).join("\n"));
    const short = [lines(10), lines(30)];
    expect(foldDiffs(short)).toEqual({ shown: short, hidden: 0 });
    const { shown, hidden } = foldDiffs([lines(25), lines(25), lines(5)]);
    expect(shown.map((d) => d.length)).toEqual([25, 15]);
    expect(hidden).toBe(15);
    expect(DIFF_FOLD_LINES).toBe(40);
  });

  it("is a finger tall on a touch screen, in the chat's secondary size (#1996)", () => {
    const html = renderToStaticMarkup(<ActionRow step={tool("t1", "Grep", { pattern: "x" })} mid="m" sid="s" />);
    expect(html).toContain("[@media(pointer:coarse)]:min-h-10");
    expect(html).toContain("text-[length:var(--chat-secondary)]");
  });

  it("shows a real multi-second span as a quiet suffix, and nothing for a sub-second or unfinished call (#8163)", () => {
    const span = (id: string, startS: number, endS?: number): ToolStep => {
      const u = use(id, "Bash", { command: "npm test" }, startS);
      const r = endS === undefined ? undefined : res(id, "ok", endS);
      return { kind: "tool", id: u.id, use: u, ...(r ? { result: r } : {}), images: [] };
    };
    const done = renderToStaticMarkup(<ActionRow step={span("t1", 0, 30)} mid="m" sid="s" />);
    expect(done).toContain('data-tool-duration');
    expect(done).toMatch(/data-tool-duration[^>]*>30s</);
    // tabular, quiet and hidden only on compact layouts; the diff-stats layout is untouched
    expect(done).toContain("max-lg:hidden shrink-0 font-mono text-label text-tool-ink tabular-nums");
    expect(renderToStaticMarkup(<ActionRow step={span("t2", 0, 0.4)} mid="m" sid="s" />)).not.toContain("data-tool-duration");
    expect(renderToStaticMarkup(<ActionRow step={span("t3", 0)} running mid="m" sid="s" />)).not.toContain("data-tool-duration");
  });
});

describe("thought rows (cindy's ThinkingActivityRow, #5155)", () => {
  const thought = (text: string): Item => ({ id: "th#1", seq: 1, role: "assistant", kind: "thinking", text });

  it("a thought without words is no row at all", () => {
    expect(renderToStaticMarkup(<ThinkingRow item={thought("")} />)).toBe("");
    expect(renderToStaticMarkup(<ThinkingRow item={thought("  \n ")} />)).toBe("");
  });

  it("one with words is one row: the sparkle in the 16 px slot, its words italic on one line, the whole as its title", () => {
    const html = renderToStaticMarkup(<ThinkingRow item={thought("先看测试，  再看实现")} />);
    expect(html).toContain('data-thinking="words"');
    expect(html).toMatch(/h-\[18px\] w-4[^>]*><svg/);
    expect(html).toMatch(/class="[^"]*truncate[^"]*"[^>]*>先看测试， 再看实现</);
    expect(html).toContain('title="先看测试， 再看实现"');
    expect(html).toContain("text-thinking-ink italic");
    expect(html).toContain("text-[length:var(--chat-secondary)]");
    // the tool rows' chrome: the same radius, padding and chevron slot
    expect(html).toContain("rounded-[8px] px-2 py-[3px]");
    expect(html).toContain("size-[18px]");
    // it fits: nothing to open (measured once drawn), so no chevron and no toggle
    expect(html).not.toContain('role="button"');
    expect(html).not.toContain("aria-expanded");
    expect(html).toContain("cursor-default");
  });

  it("a thought with a line break opens in place", () => {
    const html = renderToStaticMarkup(<ThinkingRow item={thought("先看测试\n再看实现")} />);
    expect(html).toContain('aria-expanded="false"');
    // a div with the button role, not a <button>: opened, its words can be selected
    expect(html).toMatch(/<div role="button" tabindex="0" aria-expanded="false"/);
    expect(html).not.toContain("<button");
    expect(html).toContain(">先看测试 再看实现<");
  });

  it("reads **strong** and backtick code as cindy does, on the row and opened, and nothing else", () => {
    const html = renderToStaticMarkup(<ThinkingRow item={thought("**Inspecting files** with `git status` and [docs](https://example.com)")} />);
    expect(html).toMatch(/<span class="font-medium" data-thinking-markup="strong">Inspecting files<\/span>/);
    expect(html).toMatch(/<code class="font-mono not-italic" data-thinking-markup="code">git status<\/code>/);
    expect(html).not.toContain("**");
    expect(html).not.toContain("<a ");
    expect(html).toContain("[docs](https://example.com)");
    // the hover text is the words without the markers
    expect(html).toContain('title="Inspecting files with git status and [docs](https://example.com)"');
  });

  it("live: Thinking with three dots and a clock", () => {
    const html = renderToStaticMarkup(<LiveThinking />);
    expect(html).toContain(">正在思考<");
    expect(html.match(/size-1 rounded-full/g)).toHaveLength(3);
    expect(html).toMatch(/tabular-nums">1s</);
  });
});

describe("the user's bubble and the answer", () => {
  const msg = (text: string) => ({ id: "u#0", text, images: [], contexts: [] });

  it("on the right, fits its column up to 488 wide, 12 px corners", () => {
    const html = renderToStaticMarkup(<UserBubble msg={msg("你好")} mid="m" sid="s" />);
    expect(html).toContain("ml-auto");
    expect(html).toContain("max-w-[min(100%,488px)]");
    expect(html).toContain("rounded-[12px]");
    expect(html).not.toContain("展开");
    expect(html).not.toContain("data-fold-scroll");
  });

  it("past 14 lines it folds to 10 that scroll inside, faded at the bottom, with 展开", () => {
    const long = Array.from({ length: 20 }, (_, i) => `第 ${i} 行`).join("\n");
    expect(bubbleLines(long)).toBe(20);
    const html = renderToStaticMarkup(<UserBubble msg={msg(long)} mid="m" sid="s" />);
    expect(html).toContain("max-h-[10lh] overflow-y-auto");
    expect(html).toContain("data-fold-scroll");
    expect(html).toContain("mask-image:linear-gradient(180deg");
    expect(html).not.toContain("line-clamp");
    expect(html).not.toContain("overscroll");
    // the fade is on the words, not the bubble behind them
    expect(html).toMatch(/bg-user-bubble[^>]*>\s*<div[^>]*data-fold-scroll/);
    expect(html).toContain(">展开<");
    expect(bubbleLines("x".repeat(31))).toBe(2);
  });

  it("past 14 lines it folds while it is still being sent too, dimmed (#1996)", () => {
    const long = Array.from({ length: 20 }, (_, i) => `第 ${i} 行`).join("\n");
    const html = renderToStaticMarkup(<UserBubble msg={msg(long)} mid="m" sid="s" pending />);
    expect(html).toContain("max-h-[10lh] overflow-y-auto");
    expect(html).toContain(">展开<");
    expect(html).toContain("opacity-60");
    // Sending is the chat's status line under the last message (ChatView), not a second line under the bubble
    expect(html).not.toContain(">发送中…<");
  });

  it("leaves 12% on its left like a merged bubble, so it never runs the whole width (#1996)", () => {
    expect(renderToStaticMarkup(<UserBubble msg={msg("你好")} mid="m" sid="s" />)).toContain("pl-[12%]");
  });

  it("a streaming answer has the message menu too, at the chat's body size (#1996)", () => {
    const html = renderToStaticMarkup(<Answer streaming="写到一半" mid="m" sid="s" />);
    expect(html).toContain("group/msg");
    expect(html).toContain('data-answer="streaming"');
    expect(html).toContain("[&amp;_.prose-j]:text-[length:var(--chat-body)]!");
  });

  it("an exchange: the answer without a bubble at 15 / 1.6, the work before it folded", () => {
    const user: Item = { id: "u1#0", seq: 1, role: "user", kind: "text", text: "看一下", ts: at(0) };
    const seg = { id: "s", user: { id: "u1#0", text: "看一下", images: [], contexts: [], ts: at(0) }, steps: [tool("t2", "Read", { file_path: "/a.ts" })], answer: { id: "a3#0", seq: 3, role: "assistant", kind: "text", text: "看完了。", ts: at(9) } as Item, tools: 1, thoughts: 0, replies: 0, folds: [], start: user.ts };
    const html = renderToStaticMarkup(<SegmentView seg={seg} live={false} mid="m" sid="s" expandFold={async () => {}} />);
    expect(html).toContain("[&amp;_.prose-j]:text-[length:var(--chat-body)]!");
    expect(html).toContain(">已工作 9s · 读取 1 个文件<");
    expect(html.indexOf("已工作")).toBeLessThan(html.indexOf("看完了。"));
  });

  it("live words occupy the same slot after the folded work on every conversation (#2602)", () => {
    const user: Item = { id: "u1#0", seq: 1, role: "user", kind: "text", text: "跑一遍测试", ts: at(0) };
    const seg = { id: "s", user: { id: "u1#0", text: "跑一遍测试", images: [], contexts: [], ts: at(0) }, steps: [], tools: 0, thoughts: 0, replies: 0, folds: [], start: user.ts };
    const live = (batchedWords?: boolean) =>
      renderToStaticMarkup(<SegmentView seg={seg} live mid="m" sid="s" streaming="接下来跑一遍单元测试。" batchedWords={batchedWords} expandFold={async () => {}} />);
    expect(live()).not.toContain('data-work-group="live"');
    const agent = live(true);
    expect(agent).toContain('data-work-group="live"');
    expect(agent.indexOf('data-work-group="live"')).toBeLessThan(agent.indexOf('data-answer="streaming"'));
    // no live words yet: the row as before
    const waiting = renderToStaticMarkup(<SegmentView seg={seg} live mid="m" sid="s" batchedWords expandFold={async () => {}} />);
    expect(waiting).toContain('data-work-group="live"');
  });
});

describe("summarizeWorkSteps", () => {
  it("aggregates reads, edits, commands, searches, subagents, and other tools into Chinese summary", () => {
    const steps: Step[] = [
      tool("r1", "Read", { file_path: "/a.ts" }),
      tool("r2", "NotebookRead", { notebook_path: "/n.ipynb" }),
      tool("e1", "Edit", { file_path: "/a.ts", old_string: "a", new_string: "b" }),
      tool("e2", "Write", { file_path: "/b.ts", content: "hi" }),
      tool("e3", "MultiEdit", { file_path: "/c.ts", edits: [] }),
      tool("c1", "Bash", { command: "npm test" }),
      tool("c2", "Shell", { command: "ls" }),
      tool("s1", "Grep", { pattern: "foo" }),
      tool("s2", "WebSearch", { query: "bar" }),
      tool("a1", "Agent", { description: "sub" }),
      tool("o1", "CustomTool", {}),
    ];
    expect(summarizeWorkSteps(steps)).toBe("读取 2 个文件、编辑 3 处、运行 2 条命令、搜索 2 次、1 个子 Agent、1 项操作");
    expect(summarizeWorkSteps([])).toBe("");
  });

  it("recursively collects nested tools and keeps the live step count", () => {
    const inner = group([tool("r1", "Read", { file_path: "/a.ts" }), tool("e1", "Edit", { file_path: "/a.ts", old_string: "x", new_string: "y" })], false, 2000);
    const outer: WorkGroupNode = { kind: "group", id: "work-outer", children: [inner, { kind: "step", step: tool("c1", "Bash", { command: "echo ok" }) }], live: false, durationMs: 5000 };
    expect(summarizeWorkSteps(outer.children)).toBe("读取 1 个文件、编辑 1 处、运行 1 条命令");

    const liveHtml = renderToStaticMarkup(<WorkGroup g={group([tool("t1", "Bash", { command: "ls" }, false), tool("t2", "Read", { file_path: "/a" })], true)} p={props} />);
    expect(liveHtml).toContain(">正在工作…<");
    expect(liveHtml).toContain("已执行 2 步");
  });
});

describe("DiffBlocks & formatUnifiedDiff", () => {
  it("renders dual line-number gutters and ⋯ hunk gap separators", () => {
    const same = Array.from({ length: 12 }, (_, i) => `line ${i + 1}`);
    const oldText = ["old-top", ...same, "old-bot"].join("\n");
    const newText = ["new-top", ...same, "new-bot"].join("\n");
    const diff = lineDiff(oldText, newText);
    const html = renderToStaticMarkup(<DiffBlocks diffs={[diff]} />);
    expect(html).toContain("tabular-nums");
    expect(html).toContain("bg-diff-add-bg");
    expect(html).toContain("bg-diff-del-bg");
    expect(html).toContain("data-diff-gap");
    expect(html).toContain("⋯");
  });

  it("separates multi-edit blocks with a ⋯ gap and formats unified diff text", () => {
    const d1 = lineDiff("a", "b");
    const d2 = lineDiff("c", "d");
    const html = renderToStaticMarkup(<DiffBlocks diffs={[d1, d2]} />);
    expect(html).toContain("data-diff-gap");
    expect(formatUnifiedDiff([d1, d2])).toBe("-a\n+b\n@@\n-c\n+d");
  });
});

describe("expanded.ts memory cap", () => {
  it("caps foldRanges at 500 entries by evicting oldest scopes", () => {
    expect(MAX_EXPANDED_ENTRIES).toBe(500);
    const prefix = `cap-test-${Date.now()}:`;
    for (let i = 0; i < MAX_EXPANDED_ENTRIES + 10; i++) {
      openFoldRange(`${prefix}${i}`, 1, 5);
    }
    expect(inOpenedFold(`${prefix}0`, 3)).toBe(false);
    expect(inOpenedFold(`${prefix}9`, 3)).toBe(false);
    expect(inOpenedFold(`${prefix}10`, 3)).toBe(true);
    expect(inOpenedFold(`${prefix}${MAX_EXPANDED_ENTRIES + 9}`, 3)).toBe(true);
  });
});

describe("computeIntralineDiff & DiffBlocks intraline highlight", () => {
  it("highlights a single word change while preserving unchanged prefix and suffix", () => {
    const { oldParts, newParts } = computeIntralineDiff("const foo = 1;", "const bar = 1;");
    expect(oldParts).toEqual([
      { text: "const ", changed: false },
      { text: "foo", changed: true },
      { text: " = 1;", changed: false },
    ]);
    expect(newParts).toEqual([
      { text: "const ", changed: false },
      { text: "bar", changed: true },
      { text: " = 1;", changed: false },
    ]);
  });

  it("handles identical lines without marking anything changed", () => {
    expect(computeIntralineDiff("exact same", "exact same")).toEqual({
      oldParts: [{ text: "exact same", changed: false }],
      newParts: [{ text: "exact same", changed: false }],
    });
    expect(computeIntralineDiff("", "")).toEqual({ oldParts: [], newParts: [] });
  });

  it("marks completely different lines as changed", () => {
    expect(computeIntralineDiff("alpha beta", "12345")).toEqual({
      oldParts: [{ text: "alpha beta", changed: true }],
      newParts: [{ text: "12345", changed: true }],
    });
  });

  it("falls back cleanly on lines longer than 500 chars without O(N^2) blowup", () => {
    const longOld = "a".repeat(600);
    const longNew = `${"a".repeat(300)}b${"a".repeat(299)}`;
    const res = computeIntralineDiff(longOld, longNew);
    expect(res.oldParts).toEqual([{ text: longOld, changed: false }]);
    expect(res.newParts).toEqual([{ text: longNew, changed: false }]);
  });

  it("renders intraline emphasis spans inside DiffBlocks for paired del/add lines", () => {
    const diff = lineDiff("const foo = 1;\nconst a = 2;", "const bar = 1;\nconst b = 2;");
    const html = renderToStaticMarkup(<DiffBlocks diffs={[diff]} />);
    expect(html).toContain("bg-diff-del-fg/20");
    expect(html).toContain("bg-diff-add-fg/20");
    expect(html).toContain("data-diff-intraline");
    expect(html).toContain(">foo<");
    expect(html).toContain(">bar<");
  });
});

describe("clusterWorkChildren & ClusterRow", () => {
  const stepNode = (s: Step) => ({ kind: "step" as const, step: s });
  const errTool = (id: string, name: string, input: Record<string, unknown>): ToolStep => ({
    kind: "tool",
    id: `${id}#0`,
    use: use(id, name, input),
    result: { ...res(id, "boom"), is_error: true },
    images: [],
  });

  it("clusters 3+ consecutive reads and leaves runs under 3 unclustered", () => {
    const threeReads = [
      stepNode(tool("r1", "Read", { file_path: "/a.ts" })),
      stepNode(tool("r2", "Read", { file_path: "/b.ts" })),
      stepNode(tool("r3", "NotebookRead", { notebook_path: "/n.ipynb" })),
    ];
    const clustered = clusterWorkChildren(threeReads);
    expect(clustered).toHaveLength(1);
    expect(clustered[0]).toMatchObject({
      kind: "cluster",
      category: "read",
      label: "连续读取 3 个文件",
      hasError: false,
      hasRunning: false,
    });
    if (clustered[0].kind === "cluster") expect(clustered[0].children).toHaveLength(3);

    const twoReads = threeReads.slice(0, 2);
    expect(clusterWorkChildren(twoReads)).toEqual(twoReads);
  });

  it("splits mixed categories cleanly and formats search and command labels", () => {
    const nodes = [
      stepNode(tool("s1", "Grep", { pattern: "a" })),
      stepNode(tool("s2", "Glob", { pattern: "*.ts" })),
      stepNode(tool("s3", "WebSearch", { query: "q" })),
      stepNode(tool("e1", "Edit", { file_path: "/a.ts", old_string: "a", new_string: "b" })),
      stepNode(tool("c1", "Bash", { command: "ls" })),
      stepNode(tool("c2", "Shell", { command: "pwd" })),
      stepNode(tool("c3", "Bash", { command: "git status" })),
      stepNode(tool("c4", "Bash", { command: "npm test" })),
    ];
    const clustered = clusterWorkChildren(nodes);
    expect(clustered).toHaveLength(3);
    expect(clustered[0]).toMatchObject({ kind: "cluster", category: "search", label: "连续搜索 3 次" });
    expect(clustered[1]).toMatchObject({ kind: "step" });
    expect(clustered[2]).toMatchObject({ kind: "cluster", category: "command", label: "连续运行 4 条命令" });
  });

  it("tracks error/running state, collapses settled clusters by default, and auto-expands error or running clusters", () => {
    const settledNodes = [
      stepNode(tool("r1", "Read", { file_path: "/a.ts" })),
      stepNode(tool("r2", "Read", { file_path: "/b.ts" })),
      stepNode(tool("r3", "Read", { file_path: "/c.ts" })),
    ];
    const [settledCluster] = clusterWorkChildren(settledNodes);
    expect(settledCluster).toMatchObject({ kind: "cluster", category: "read", hasError: false, hasRunning: false });
    if (settledCluster.kind === "cluster") {
      const settledHtml = renderToStaticMarkup(<ClusterRow cluster={settledCluster} p={props} live={false} />);
      expect(settledHtml).toContain('data-work-cluster="read"');
      expect(settledHtml).toContain(">连续读取 3 个文件<");
      expect(settledHtml).toContain('aria-expanded="false"');
      expect(settledHtml).not.toContain("data-cluster-children");
    }

    const errorNodes = [
      stepNode(tool("c1", "Bash", { command: "echo 1" })),
      stepNode(errTool("c2", "Bash", { command: "false" })),
      stepNode(tool("c3", "Bash", { command: "echo 3" })),
    ];
    const [errCluster] = clusterWorkChildren(errorNodes);
    expect(errCluster).toMatchObject({ kind: "cluster", hasError: true, hasRunning: false });
    if (errCluster.kind === "cluster") {
      const errHtml = renderToStaticMarkup(<ClusterRow cluster={errCluster} p={props} live={false} />);
      expect(errHtml).toContain('aria-expanded="true"');
      expect(errHtml).toContain('data-cluster-badge="error"');
      expect(errHtml).toContain("data-cluster-children");
    }

    const runningNodes = [
      stepNode(tool("s1", "Grep", { pattern: "a" })),
      stepNode(tool("s2", "Grep", { pattern: "b" })),
      stepNode(tool("s3", "Grep", { pattern: "c" }, false)),
    ];
    const [runCluster] = clusterWorkChildren(runningNodes);
    expect(runCluster).toMatchObject({ kind: "cluster", hasError: false, hasRunning: true });
    if (runCluster.kind === "cluster") {
      const runHtml = renderToStaticMarkup(<ClusterRow cluster={runCluster} p={props} live={true} />);
      expect(runHtml).toContain('aria-expanded="true"');
      expect(runHtml).toContain('data-cluster-badge="running"');
      expect(runHtml).toContain("data-cluster-children");
    }
  });
});
