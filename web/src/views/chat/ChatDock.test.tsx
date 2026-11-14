// The chat's header and what sits under the chat on a phone (#1996), as markup: the browser checks are
// scripts/test/web_mobile_input_e2e.py.
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Interaction, Item, Session } from "../../lib/types";
import { getState, type AnsweredCard } from "../../lib/store";
import { Header, spaceAgentsItem } from "./ChatView";
import { HEADER_SLOT, HEADER_SLOT_END, HEADER_SLOT_START } from "./header";
import { Composer } from "./Composer";
import { PendingCards, permissionFrom } from "./Interactions";
import { restoreQueued } from "./QueueStrip";
import { QueuePanel } from "./QueuePanel";
import { onDraftEvent, type DraftEvent } from "../../lib/drafts";
import { getContexts, removeContext, withContexts } from "../../lib/links";
import { parseQueued } from "../../lib/queue";
import { AttachmentTray } from "../../ui/AttachmentTray";
import { parseAPIError } from "../../lib/api-errors";
import { ErrorBanner } from "./Lines";

// stores without a server snapshot: renderToStaticMarkup needs one
vi.mock("../../lib/links", async (orig) => ({ ...(await orig<typeof import("../../lib/links")>()), useContexts: () => [] }));
vi.mock("../../lib/assistant-chat", async (orig) => ({ ...(await orig<typeof import("../../lib/assistant-chat")>()), usePickedLauncher: () => undefined }));

const session = {
  machine_id: "m",
  session_id: "s",
  title: "补齐网络层的单元测试",
  state: "needs_you",
  space_id: "sp",
  cwd: "/tmp/x",
} as unknown as Session;
const sibling = { ...session, session_id: "s2" } as Session;

const header = (extra: Partial<Parameters<typeof Header>[0]> = {}) =>
  renderToStaticMarkup(<Header session={session} title="补齐网络层的单元测试" machineName="MacBook" siblings={[session, sibling]} onInterrupt={() => {}} onToggleSidebar={() => {}} {...extra} />);
/** The header's markup with the brand marks' long inline pictures left out, so a failure prints what matters. */
const bare = (html: string) => html.replace(/<img src="data:[^"]*"/g, "<img");

describe("Header", () => {
  it("puts the switch it is given in the middle on a narrow screen, in place of the title", () => {
    const html = header({ topCenter: <span data-switch>助手 | Agents</span> });
    expect(html).toContain("data-switch");
    expect(html).toMatch(/class="flex min-w-0 flex-1 justify-center lg:hidden"><span data-switch/);
    expect(html).toMatch(/data-chat-title-row="true" class="flex min-w-0 flex-1 items-center gap-0.5 max-lg:hidden"/);
    // the buttons either side sit in slots of one least width, which grow to fit them (#5036)
    expect(html).toContain(`class="${HEADER_SLOT} ${HEADER_SLOT_START} lg:contents"`);
    expect(html).toContain(`class="${HEADER_SLOT} ${HEADER_SLOT_END} lg:contents"`);
    // the title is hidden there, so ⋯ goes to the right end on a narrow screen and stays after the title on a wide one
    expect(html).toMatch(/<span class="relative flex shrink-0 max-lg:hidden"><button[^>]*aria-label="更多操作"/);
    expect(html).toMatch(/<span class="relative flex shrink-0 lg:hidden"><button[^>]*aria-label="更多操作"/);
  });

  it("is as before without one", () => {
    const html = header();
    expect(html).not.toContain("justify-center lg:hidden");
    expect(html).not.toContain("max-lg:hidden");
    expect(html).not.toContain("max-lg:min-w-16");
    expect(html.match(/aria-label="更多操作"/g)).toHaveLength(1);
  });

  it("is cindy's one line: the Agent's mark, the title, then ⋯ (#5421)", () => {
    const html = bare(header());
    // spoken: which Agent and where it runs; on hover cindy's sentence
    const mark = html.indexOf('aria-label="Claude Code。Agent 在 MacBook 上运行" title="Agent 在 MacBook 上运行"');
    const title = html.indexOf("data-chat-title=");
    const more = html.indexOf('aria-label="更多操作"');
    expect(mark).toBeGreaterThan(-1);
    expect(mark).toBeLessThan(title);
    expect(title).toBeLessThan(more);
    // the mark carries the "on another computer" arc
    expect(html).toContain("data-agent-remote");
    // no state pill or dot, no second line, no model, no permission mode in the bar
    expect(html).not.toContain("需要你");
    expect(html).not.toContain("status-mark");
    expect(html).not.toContain("MacBook ›");
    expect(html).not.toContain("text-[11.5px]");
    // the title is cindy's 14 px medium
    expect(html).toMatch(/<h1[^>]*class="[^"]*text-\[14px\] font-medium/);
  });

  it("says the computer is offline in the mark's words, and dims it", async () => {
    const { getState: state } = await import("../../lib/store");
    const machines = state().machines;
    state().machines = [{ id: "m", name: "MacBook", online: false } as (typeof machines)[number]];
    try {
      const html = bare(header());
      expect(html).toMatch(/title="Agent 在 MacBook 上运行，那台电脑现在离线，连上后才能继续"[^>]*data-agent-device="offline"[^>]*opacity-75/);
    } finally {
      state().machines = machines;
    }
  });

  it("shows the branch after ⋯, wide only", () => {
    const html = bare(header({ session: { ...session, branch: "feat/net-tests" } as Session }));
    expect(html).toMatch(/aria-label="更多操作"[^]*data-chat-branch="true"[^>]*@max-\[800px\]:hidden[^>]*>[^]*feat\/net-tests/);
    expect(header()).not.toContain("data-chat-branch");
  });

  it("has no 同空间 button: the space's other agents are in ⋯ at every width, which carries their attention", () => {
    const html = bare(header());
    expect(html).not.toContain("同空间");
    expect(html).toMatch(/aria-label="更多操作"[^]*?<span aria-hidden="true" class="[^"]*bg-attention/);
    const calm = { ...sibling, state: "idle" } as Session;
    const quiet = { ...session, state: "idle" } as Session;
    expect(header({ session: quiet, siblings: [quiet, calm] })).not.toContain("bg-attention");
  });

  it("puts the loop mark before the Agent's mark and a Background tasks button at the right end (#5421)", () => {
    const looping = { ...session, loop: { kind: "interval", interval_seconds: 1800, prompt: "/loop check CI" } } as Session;
    const html = bare(header({ session: looping }));
    const loop = html.indexOf("data-loop-mark");
    expect(loop).toBeGreaterThan(-1);
    expect(loop).toBeLessThan(html.indexOf("data-agent-device"));
    expect(html).toMatch(/aria-label="循环 · 每 30 分钟"/);
    expect(html).toMatch(/<button[^>]*aria-label="后台任务"/);
    expect(bare(header())).not.toContain('aria-label="后台任务');
    // a running background task: the button counts it
    const busy = { ...session, background: [{ id: "b1", kind: "bash", command: "npm test" }] } as Session;
    const counted = bare(header({ session: busy }));
    expect(counted).toMatch(/aria-label="后台任务，1 个在运行"/);
    expect(counted).toMatch(/data-background-count="true"[^>]*>1</);
  });

  it("keeps the goal out of the bar (it is over the composer)", () => {
    const html = header({ session: { ...session, goal: { objective: "让测试全绿", status: "active" } } as Session });
    expect(html).not.toContain("让测试全绿");
  });
});

describe("Header in a task's or an Agent's side panel (#3333, #5424)", () => {
  const todoSession = { ...session, todo: { id: "t1", title: "代理的待办" } } as unknown as Session;

  it("is the panel's only header: the task's title (its state in the tooltip), a close button at the end", () => {
    const html = header({ session: todoSession, panel: { onClose: () => {}, title: "修登录崩溃", sub: "在做 · MacBook" } });
    expect(html).toMatch(/<h1 title="修登录崩溃 · 在做 · MacBook"[^>]*>修登录崩溃<\/h1>/);
    expect(html).toMatch(/aria-label="关闭面板"[^]*<\/header>$/);
    expect(html).not.toContain("代理的待办");
  });

  it("puts what the panel gives beside the title (a task's ⌄ for its agents), before ⋯", () => {
    const html = header({ panel: { onClose: () => {}, title: "修登录崩溃", beside: <button aria-label="切换 Agent" /> } });
    expect(html).toMatch(/>修登录崩溃<\/h1><button aria-label="切换 Agent"><\/button><span[^>]*><button[^>]*aria-label="更多操作"/);
  });

  it("is as before outside a panel", () => {
    const html = header();
    expect(html).not.toContain('aria-label="关闭面板"');
    expect(html).toMatch(/<h1 title="补齐网络层的单元测试"/);
  });
});

describe("spaceAgentsItem, ⋯'s 同空间 row (#3576, #5421)", () => {
  it("counts the others, opens the space's menu and marks attention when one needs you", () => {
    let opened = 0;
    const item = spaceAgentsItem([sibling], () => opened++);
    expect(item.label).toBe("同空间 1");
    expect(item.icon).toBe("layers");
    expect(item.state).toBe("needs_you");
    void item.onSelect();
    expect(opened).toBe(1);
    expect(spaceAgentsItem([{ ...sibling, state: "working" } as Session], () => {}).state).toBeUndefined();
  });
});

describe("Header, the terminal view (#2419, #5421)", () => {
  it("is one button to the other view at every width", () => {
    const chat = bare(header({ view: "chat", onView: () => {} }));
    expect(chat).toMatch(/<span class="relative flex shrink-0"><button[^>]*aria-label="终端"/);
    expect(chat).not.toContain("radiogroup");
    const term = bare(header({ view: "term", onView: () => {} }));
    expect(term).toMatch(/<span class="relative flex shrink-0"><button[^>]*aria-label="对话"/);
  });
  it("is not there without a terminal to switch to", () => {
    expect(header()).not.toContain('aria-label="终端"');
  });
  it("marks 对话 when the terminal shows and something wants an answer", () => {
    expect(header({ view: "term", onView: () => {}, viewDot: true })).toMatch(/aria-label="对话"[^]*?<\/button><span aria-hidden="true" class="pointer-events-none absolute[^"]*bg-attention"/);
    expect(header({ view: "term", onView: () => {} })).not.toMatch(/aria-label="对话"[^]*?<\/button><span aria-hidden/);
  });
});

const ix = (id: string, kind: Interaction["kind"], extra: Partial<Interaction> = {}) =>
  ({ id, machine_id: "m", session_id: "s", kind, tool_name: "Bash", ...extra }) as Interaction;
const three = [ix("a", "plan", { plan: "## 计划" }), ix("b", "question", { questions: [{ question: "用哪个框架？", options: [{ label: "A" }] }] as Interaction["questions"] }), ix("c", "permission")];
const cards = (o: { narrow: boolean; keyboard?: boolean; interactions?: Interaction[]; answered?: AnsweredCard[]; offer?: boolean }) =>
  renderToStaticMarkup(<PendingCards interactions={o.interactions ?? three} answered={o.answered ?? []} offerNext={o.offer ? { mid: "m", sid: "s" } : undefined} narrow={o.narrow} keyboard={Boolean(o.keyboard)} onDismissKeyboard={() => {}} />);

describe("PendingCards", () => {
  it("uses attention marks and neutral surfaces for questions, plans and permissions", () => {
    const html = cards({ narrow: false });
    expect(html).toContain("text-attention");
    expect(html).toContain("bg-surface");
    expect(html).not.toMatch(/\b(?:bg-red|bg-red-tint|text-red)\b/);
    expect(html).not.toContain("bg-danger-fill");
  });
  it("keeps the attention mark when the pending cards fold", () => {
    const html = cards({ narrow: true, keyboard: true });
    expect(html).toContain("bg-attention");
    expect(html).not.toContain("bg-red");
  });
  it("says what an answered card was about when its answer is one word (允许 / 批准)", () => {
    const perm = ix("p", "permission", { tool_input: { command: "swift test" } });
    const html = cards({ narrow: false, interactions: [], answered: [{ ix: perm, resolution: { allow: true } }] });
    expect(html).toContain("data-answered-about");
    expect(html).toContain("swift test");
    expect(html).toContain("允许");
    // a question's lines already carry what was asked
    const q = ix("q", "question", { questions: [{ question: "用哪个框架？", options: [{ label: "A" }] }] as Interaction["questions"] });
    expect(cards({ narrow: false, interactions: [], answered: [{ ix: q, resolution: { answers: { "用哪个框架？": "A" } } }] })).not.toContain("data-answered-about");
    // a plan says its first line, not "Plan awaiting approval"; a bare one says nothing extra
    const plan = ix("l", "plan", { plan: "\n## 改用 **vitest**\n1. 换配置" });
    const planHtml = cards({ narrow: false, interactions: [], answered: [{ ix: plan, resolution: { approve: true } }] });
    expect(planHtml).toContain("改用 vitest");
    expect(planHtml).not.toContain("计划等你审批");
    expect(cards({ narrow: false, interactions: [], answered: [{ ix: ix("l2", "plan"), resolution: { approve: true } }] })).not.toContain("data-answered-about");
    // a terminal dialog with nothing to say: no "Terminal needs you" above its Yes
    const dialog = ix("d", "screen", { dialog: { options: [{ key: "1", label: "Yes" }] } as Interaction["dialog"] });
    const dialogHtml = cards({ narrow: false, interactions: [], answered: [{ ix: dialog, resolution: { option: "1" } }] });
    expect(dialogHtml).not.toContain("data-answered-about");
    expect(dialogHtml).not.toContain("终端等你处理");
    // handed back (#2450): nothing was decided, so no answer line repeats the head; the line says which request went back
    const handed = cards({ narrow: false, interactions: [], answered: [{ ix: perm, resolution: { dismiss: true } }] });
    expect(handed).toContain("data-answered-about");
    expect(handed).not.toContain("交回电脑处理");
  });

  it("says which subagent asks for a permission", () => {
    const html = cards({ narrow: false, interactions: [ix("p", "permission", { agent_id: "a1", agent_type: "code-reviewer", tool_input: { command: "npm test" } })] });
    expect(html).toContain("data-permission-from");
    expect(html).toContain("来自子 Agent · code-reviewer");
    expect(cards({ narrow: false, interactions: [ix("p", "permission")] })).not.toContain("data-permission-from");
    // the main session's own card: `claude --agent reviewer` sets agent_type with no agent_id
    expect(cards({ narrow: false, interactions: [ix("p", "permission", { agent_type: "reviewer" })] })).not.toContain("data-permission-from");
    expect(cards({ narrow: false, interactions: [ix("p", "permission", { agent_id: "a1", agent_type: "main" })] })).not.toContain("data-permission-from");
  });

  it("keeps the subagent's type to one short line", () => {
    expect(permissionFrom({ agent_id: "a1", agent_type: "  code\n  reviewer  " })).toBe("来自子 Agent · code reviewer");
    const long = permissionFrom({ agent_id: "a1", agent_type: "x".repeat(80) });
    expect(long).toBe(`来自子 Agent · ${"x".repeat(47)}…`);
    const html = cards({ narrow: false, interactions: [ix("p", "permission", { agent_id: "a1", agent_type: "code-reviewer" })] });
    expect(html).toMatch(/data-permission-from="true" title="来自子 Agent · code-reviewer" class="truncate/);
  });

  it("names a child session's card by the child's title, on its parent's page (#5120)", () => {
    expect(permissionFrom({ folded_under: "lead", session_title: "  Write the\n tests " })).toBe("来自子 Agent · Write the tests");
    expect(permissionFrom({ folded_under: "lead" })).toBe("来自子 Agent · 子 Agent 任务");
    expect(permissionFrom({ folded_under: "lead", session_title: "y".repeat(80) })).toBe(`来自子 Agent · ${"y".repeat(47)}…`);
    // a question card says it too, at its top; the session's own cards say nothing
    const questions = [{ question: "用哪个框架？", options: [{ label: "A" }] }] as Interaction["questions"];
    const html = cards({ narrow: false, interactions: [ix("q", "question", { questions, folded_under: "lead", session_title: "Write the tests" })] });
    expect(html).toMatch(/data-permission-from="true" title="来自子 Agent · Write the tests"/);
    expect(cards({ narrow: false, interactions: [ix("q", "question", { questions })] })).not.toContain("data-permission-from");
  });

  it("names a lead's worker's card as the worker's, on the lead's page (#5180)", () => {
    expect(permissionFrom({ folded_under: "lead", folded_as: "worker", session_title: "Review the change" })).toBe("来自子 Agent · Review the change");
    const questions = [{ question: "用哪个框架？", options: [{ label: "A" }] }] as Interaction["questions"];
    const html = cards({ narrow: false, interactions: [ix("q", "question", { questions, folded_under: "lead", folded_as: "worker", session_title: "Review" })] });
    expect(html).toMatch(/data-permission-from="true" title="来自子 Agent · Review"/);
  });

  it("shows every card on a wide screen, in no box of their own", () => {
    const html = cards({ narrow: false });
    expect(html).not.toContain("data-chat-cards");
    expect(html).not.toContain("还有");
    expect(html).toContain("计划等你审批");
    expect(html).toContain("用哪个框架？");
    expect(html).toContain("允许使用 Bash？");
  });

  it("scrolls them in a box of their own on a narrow one, the first open and 还有 N 件 for the rest", () => {
    const html = cards({ narrow: true });
    expect(html).toMatch(/data-chat-cards[^>]*overflow-y-auto/);
    expect(html).toContain("还有 2 件");
    // the first card's fold is open, the others' closed (inert)
    const folds = [...html.matchAll(/<div class="grid [^"]*"([^>]*)>/g)].map((m) => m[1]);
    expect(folds).toHaveLength(2);
    expect(folds[0]).not.toContain("inert");
    expect(folds[1]).toContain("inert");
  });

  it("folds them into one line while the keyboard is up", () => {
    const html = cards({ narrow: true, keyboard: true });
    expect(html).toContain("3 件待处理");
    expect(html).not.toContain("还有");
    const folds = [...html.matchAll(/<div class="grid [^"]*"([^>]*)>/g)].map((m) => m[1]);
    expect(folds.every((f) => f.includes("inert"))).toBe(true);
  });

  it("is one line on a phone on its side", () => {
    const html = renderToStaticMarkup(<PendingCards interactions={three} answered={[]} narrow short keyboard={false} onDismissKeyboard={() => {}} />);
    expect(html).toContain("3 件待处理");
    expect(html).not.toContain("还有");
  });

  it("is nothing without cards", () => {
    expect(cards({ narrow: true, interactions: [] })).toBe("");
  });

  // #3112: on a phone and in a side panel the open cards sit in folds; a fold that still clipped once open was the box
  // a card's sticky buttons stuck in, so a card taller than the cards' box (a long command) had its Allow / Deny below
  // the fold, out of sight. The layout itself is checked in a browser (scripts/test/web_card_scroll_e2e.py).
  it("doesn't clip the open cards' folds on a narrow screen, so a tall card's buttons stick in view (#3112)", () => {
    const long = ix("p", "permission", { tool_input: { command: "python3 -c \"" + "print(1)\n".repeat(80) + "\"" } });
    const html = renderToStaticMarkup(<PendingCards interactions={[long, three[1]]} answered={[]} narrow keyboard={false} onDismissKeyboard={() => {}} />);
    const box = html.slice(html.indexOf("data-chat-cards"));
    // the first card's fold is open: its clip box is not a scroller (no overflow-hidden), the card's footer sticky
    const first = box.match(/<div class="grid [^"]*"[^>]*><div class="([^"]*)"/);
    expect(first?.[1]).toBeDefined();
    expect(first![1].split(/\s+/)).not.toContain("overflow-hidden");
    expect(box).toMatch(/data-card-actions="[^"]*" class="sticky -bottom-1 /);
    // the folded one (2 more) still clips: it is closed
    const folds = [...box.matchAll(/<div class="grid [^"]*"[^>]*><div class="([^"]*)"/g)].map((m) => m[1]);
    expect(folds).toHaveLength(2);
    expect(folds[1]).toContain("overflow-hidden");
  });
});

describe("PendingCards, the next card waiting elsewhere (#4091)", () => {
  const answered: AnsweredCard[] = [{ ix: ix("done", "question"), resolution: { answers: { q: "A" } } }];
  const other = ix("other", "question", { machine_id: "n", session_id: "t", session_title: "照片同步脚本支持 HEIC", questions: [{ question: "保存为哪种格式？", options: [] }] as Interaction["questions"] });
  // the store's cards: two of other chats, one of this chat (left out of the count)
  const seed = (list: Interaction[]) => Object.assign(getState(), { interactions: list, sessions: {} });
  afterEach(() => seed([]));

  it("says how many, which is next and what it asks under 已回答, linking to that chat", () => {
    seed([ix("mine", "permission"), other, ix("more", "permission", { machine_id: "n", session_id: "u" })]);
    for (const narrow of [false, true]) {
      const html = cards({ narrow, interactions: [], answered, offer: true });
      expect(html).toContain("已回答");
      expect(html).toContain("还有 2 件等你");
      expect(html).toContain("下一件：照片同步脚本支持 HEIC · 保存为哪种格式？");
      expect(html).toMatch(/<a href="[^"]*\/s\/n\/t"[^>]*data-chat-next-waiting/);
    }
  });
  it("is not there while this chat still has a card, before anything is answered here, when not offered, or with nothing elsewhere", () => {
    seed([other]);
    expect(cards({ narrow: true, interactions: [three[2]], answered, offer: true })).not.toContain("data-chat-next-waiting");
    expect(cards({ narrow: true, interactions: [], answered: [], offer: true })).toBe("");
    expect(cards({ narrow: false, interactions: [], answered })).not.toContain("data-chat-next-waiting");
    seed([ix("mine", "permission")]);
    expect(cards({ narrow: false, interactions: [], answered, offer: true })).not.toContain("data-chat-next-waiting");
  });
});

describe("Composer", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("caps the text box by the visible height", () => {
    vi.stubGlobal("localStorage", { getItem: () => null });
    const html = renderToStaticMarkup(<Composer draftKey="t" placeholder="给 Agent 发消息…" onSend={async () => true} />);
    expect(html).toMatch(/<textarea[^>]*style="max-height:min\(240px, calc\(var\(--kb-h\) \* 0.35\)\)"/);
  });
});

describe("tap areas", () => {
  afterEach(() => vi.unstubAllGlobals());
  const hit = (html: string, label: string) => new RegExp(`<button[^>]*aria-label="${label}"[^>]*class="touch-hit `).test(html) || new RegExp(`<button[^>]*class="touch-hit [^"]*"[^>]*aria-label="${label}"`).test(html);

  it("covers the composer's row: + and send", () => {
    vi.stubGlobal("localStorage", { getItem: () => null });
    const html = renderToStaticMarkup(<Composer draftKey="t" placeholder="…" machineId="m" mention={{ self: "t", files: () => [] }} onSend={async () => true} />);
    for (const label of ["添加文件、图片等", "发送"]) expect(hit(html, label), label).toBe(true);
  });

  it("covers a card's hand back, a question's options and its arrows", () => {
    const html = cards({ narrow: false });
    expect(hit(html, "交给电脑处理")).toBe(true);
    expect(html).toMatch(/<button[^>]*aria-pressed="false"[^>]*class="touch-hit /);
    expect(hit(html, "下一题") || hit(html, "发送回答")).toBe(true);
  });

  it("covers an attachment's remove button", () => {
    const html = renderToStaticMarkup(<AttachmentTray attachments={[{ id: "a", name: "a.png", url: "blob:a" }]} onRemove={() => {}} />);
    expect(hit(html, "移除 a.png")).toBe(true);
    // its tile does not clip the area
    expect(html).not.toMatch(/group\/att relative overflow-hidden/);
  });

  it("covers the error banner's ✕ and its buttons (#5313)", () => {
    const text = "API Error: 529 overloaded";
    const item = { id: "e1", seq: 1, role: "assistant", kind: "text", text } as Item;
    const html = renderToStaticMarkup(<ErrorBanner line={{ kind: "apiError", id: "err-e1", item, info: parseAPIError(text)!, count: 1 }} onRetry={() => {}} onDismiss={() => {}} />);
    expect(hit(html, "关闭错误提示")).toBe(true);
    // 28 px tall, as cindy's: a height in className would not win over the button's own 32 px
    const retry = html.match(/<button[^>]*class="([^"]*)"[^>]*>(?:(?!<\/button>).)*重试<\/button>/)![1].split(" ");
    expect(retry).toEqual(expect.arrayContaining(["touch-hit", "h-7"]));
    expect(retry).not.toContain("h-8");
  });
});

describe("QueuePanel (#5327, cindy's pending queue)", () => {
  const queued = { ...session, queued: ["顺便把截图换掉", "跑一遍 UI 测试"] } as Session;
  const panel = (props: Partial<Parameters<typeof QueuePanel>[0]> & { session: Session }) => renderToStaticMarkup(<QueuePanel draftKey="t" canEdit {...props} />);
  const rows = (html: string) => html.match(/data-queue-row="/g) ?? [];

  it("lists the messages in send order without numbers, one line each, a hairline under them", () => {
    const html = panel({ session: queued });
    expect(html).toMatch(/^<div role="region" aria-label="排队的消息" data-queue-panel="true" class="w-full border-b border-line"/);
    expect(rows(html)).toHaveLength(2);
    expect(html.indexOf("顺便把截图换掉")).toBeLessThan(html.indexOf("跑一遍 UI 测试"));
    // the place is in the labels only
    expect(html).toContain('aria-label="排队的第 1 条：顺便把截图换掉"');
    expect(html).not.toMatch(/>1</);
    expect(html).toMatch(/data-queue-row-body="true" class="[^"]*truncate text-\[13px\]/);
    expect(html).toMatch(/max-h-\[196px\][^"]*overflow-y-auto/);
  });

  it("is one line that opens the same rows on a phone on its side, beside the run's status", () => {
    const html = panel({ session: queued, compact: true });
    expect(html).toContain("data-queue-strip");
    expect(html).toContain("排队中 2 条");
    expect(html).toContain('aria-expanded="false"');
    expect(html).toMatch(/<div class="grid [^"]*"[^>]*inert/);
    expect(rows(html)).toHaveLength(2);
  });

  it("shows four in full, and from five the first three with N more", () => {
    const four = panel({ session: { ...queued, queued: ["一", "二", "三", "四"] } });
    expect(rows(four)).toHaveLength(4);
    expect(four).not.toContain("data-queue-fold");
    const six = panel({ session: { ...queued, queued: ["一", "二", "三", "四", "五", "六"] } });
    expect(rows(six)).toHaveLength(3);
    expect(six).toMatch(/data-queue-fold="true"[^>]*>还有 3 条/);
    expect(six).toContain('aria-expanded="false"');
  });

  it("gives every own row a drag handle, Edit and Delete; a Claude's Send now too, other agents none (#6088)", () => {
    const claude = panel({ session: { ...queued, agent: "claude" } });
    expect(claude.match(/data-queue-handle/g)).toHaveLength(2);
    expect(claude.match(/data-send-now/g)).toHaveLength(2);
    expect(claude.match(/data-queue-edit/g)).toHaveLength(2);
    expect(claude.match(/data-queue-delete/g)).toHaveLength(2);
    expect(claude).toContain('aria-label="拖动第 2 条排队消息调整顺序"');
    expect(claude).toContain('aria-label="编辑第 1 条排队消息"');
    expect(claude).toContain('aria-label="删除第 2 条排队消息"');
    // it hands over the whole queue at one press, and says so; its tooltip says what happens too, as the dialog did before #7155
    expect(claude).toContain('aria-label="现在就把排队的消息全部发给 Claude，不用等到下一步"');
    expect(claude).toMatch(/title="现在就把排队的消息全部发给 Claude，不用等到下一步\nClaude 会马上收到，不用等到下一步。/);
    const codex = panel({ session: { ...queued, agent: "codex" } });
    expect(codex).not.toContain("data-send-now");
    expect(codex.match(/data-queue-edit/g)).toHaveLength(2);
    // a lone message has nothing to move past
    expect(panel({ session: { ...queued, queued: ["只有一条"] } })).not.toContain("data-queue-handle");
    // a session this page can't send to shows them only
    const viewOnly = panel({ session: { ...queued, agent: "claude" }, canEdit: false });
    expect(rows(viewOnly)).toHaveLength(2);
    expect(viewOnly).not.toMatch(/data-queue-(handle|edit|delete)|data-send-now/);
  });

  it("in a Persona chat offers the user's own rows the same; one from another conversation is folded into one line (#4342, #6394)", () => {
    const persona = { ...queued, agent: undefined, assistant: true, queued: ["顺便把截图换掉", "来自「日报」：今天的汇总"], queued_relayed: 1 } as Session;
    const html = panel({ session: persona, persona: "Nova" });
    expect(html.match(/data-send-now/g)).toHaveLength(1);
    expect(html).toContain("现在就把排队的消息全部发给 Nova");
    // collapsed: no relayed row markup yet, just the summary line
    expect(html).not.toContain("data-queue-relayed-list");
    expect(html).toContain("还有 1 条别的对话在排队");
    expect(html).toContain("data-queue-relayed-summary");
    // relayed ones alone: nothing to send now
    expect(panel({ session: { ...persona, queued_relayed: 2 }, persona: "Nova" })).not.toContain("data-send-now");
  });

  it("marks the photos a Persona chat's queued message went with when this browser sent it (#4342)", () => {
    const persona = { ...queued, assistant: true, queued: ["请看图片", "顺便把截图换掉"] } as Session;
    const html = panel({ session: persona, persona: "Nova", photosOf: (q) => (q === "请看图片" ? 2 : 0) });
    expect(html.match(/data-queue-mark="photo"/g)).toHaveLength(1);
    expect(html).toContain('aria-label="排队的第 1 条：2 张图片"');
    expect(html).not.toContain(">请看图片<");
  });

  // made-up words and paths, in the shape Claude Code queues a message sent with photos (#3032)
  const photo = (name: string) => `/Users/me/.persona/uploads/2026-11-10/${name}.jpg`;
  const withPhotos = `[Image #1] [Image #2]\n\n\n把这两张截图对一下\n<attached_images>\npath: ${photo("a1")}\npath: ${photo("b2")}\n</attached_images>`;

  it("shows a queued message's words and a mark per kind of attachment, never Claude's queue text (#3032)", () => {
    const photoOnly = `[Image #3] 请看图片\n\n<attached_images>\npath: ${photo("c3")}\n</attached_images>`;
    const html = panel({ session: { ...session, queued: [withPhotos, photoOnly] } as Session });
    expect(html).toContain("把这两张截图对一下");
    expect(html).not.toContain("[Image #");
    expect(html).not.toContain("attached_images");
    expect(html).not.toContain("path: ");
    expect(html).not.toContain("请看图片");
    expect(html.match(/data-queue-mark="photo"/g)).toHaveLength(2);
    // the label says what came with it
    expect(html).toContain('aria-label="排队的第 1 条：把这两张截图对一下 · 2 张图片"');
    expect(html).toContain('aria-label="排队的第 2 条：1 张图片"');
  });
});

describe("restoreQueued (#3032)", () => {
  const photo = "/Users/me/.persona/uploads/2026-11-10/a1.jpg";
  const gone = "/Users/me/.persona/uploads/2026-11-10/gone.jpg";
  afterEach(() => {
    vi.unstubAllGlobals();
    for (const c of getContexts("rq")) removeContext("rq", c.id);
  });

  it("puts the words in the box, the photos in the tray, the contexts on their chips and a lost photo in as its path", async () => {
    const urls: string[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string) => {
      urls.push(url);
      return url.includes(encodeURIComponent(photo)) ? new Response(new Blob([new Uint8Array([1, 2, 3])], { type: "image/jpeg" })) : new Response("", { status: 404 });
    });
    const events: DraftEvent[] = [];
    const off = onDraftEvent("rq", (e) => void events.push(e));
    try {
      const workspace = { kind: "workspace" as const, title: "web", url: "", text: "~/code/web", attrs: { machine: "laptop", cwd: "/home/me/code/web" } };
      const raw = `${withContexts([workspace], "Fix the header", true)} <persona-file kind="file" path="/tmp/spec.pdf" name="spec.pdf"/>\n\n<attached_images>\npath: ${photo}\npath: ${gone}\n</attached_images>`;
      await restoreQueued("rq", session, parseQueued(raw));
      expect(urls).toHaveLength(2);
      expect(urls[0]).toContain("/machines/m/file/download?path=");
      expect(events).toEqual([
        { type: "append", text: "Fix the header /tmp/spec.pdf" },
        { type: "image", image: { name: "a1.jpg", mime: "image/jpeg", data: "AQID" } },
        { type: "append", text: gone },
      ]);
      expect(getContexts("rq").map(({ kind, title, text, attrs }) => ({ kind, title, text, attrs }))).toEqual([{ kind: "workspace", title: "web", text: "~/code/web", attrs: workspace.attrs }]);
    } finally {
      off();
    }
  });
});

describe("restoreQueued in a Persona chat (#4342)", () => {
  it("puts the photos that came back with the message in the tray, without the stand-in words", async () => {
    const events: DraftEvent[] = [];
    const off = onDraftEvent("rqp", (e) => void events.push(e));
    try {
      const images = [{ mime: "image/png", data: "AQID" }];
      await restoreQueued("rqp", session, parseQueued("请看图片", images.length), images);
      expect(events).toEqual([{ type: "image", image: { name: "photo-1.png", mime: "image/png", data: "AQID" } }]);
    } finally {
      off();
    }
  });
});

