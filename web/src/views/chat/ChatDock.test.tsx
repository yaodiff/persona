// The chat's header and what sits under the chat on a phone (#1996), as markup: the browser checks are
// scripts/test/web_mobile_input_e2e.py.
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Interaction, Item, Session } from "../../lib/types";
import { getState, type AnsweredCard } from "../../lib/store";
import { Header, spaceAgentsItem } from "./ChatView";
import { t } from "../../i18n";
import { HEADER_SLOT, HEADER_SLOT_END, HEADER_SLOT_START } from "./header";
import { Composer } from "./Composer";
import { PendingCards, permissionFrom } from "./Interactions";
import { restoreQueued } from "./queue-restore";
import { getComposerFiles, removeFile } from "../../lib/composer-files";
import { QueuePanel } from "./QueuePanel";
import { onDraftEvent, type DraftEvent } from "../../lib/drafts";
import { getContexts, removeContext, withContexts } from "../../lib/links";
import { parseQueued } from "../../lib/queue";
import { AttachmentTray } from "../../ui/AttachmentTray";
import { parseAPIError } from "../../lib/api-errors";
import { ErrorBanner } from "./Lines";
import { setSideFold } from "../../lib/side-fold";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

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
    // ⋯ sits once in the right-end slot at every width
    expect(html.match(/aria-label="更多操作"/g)).toHaveLength(1);
    expect(html).toMatch(/justify-end lg:contents"[^]*<span class="relative flex shrink-0"><button[^>]*aria-label="更多操作"/);
  });

  it("is as before without one", () => {
    const html = header();
    expect(html).not.toContain("justify-center lg:hidden");
    expect(html).not.toContain("max-lg:hidden");
    expect(html).not.toContain("max-lg:min-w-16");
    expect(html.match(/aria-label="更多操作"/g)).toHaveLength(1);
    expect(html).not.toContain("data-side-sessions");
  });

  it("has a Side sessions button with their count while the session has some, and the dot while one needs you (#7944)", () => {
    const quiet = header({ sides: { count: 2, open: false, needs: false, onToggle: () => {} } });
    expect(quiet).toMatch(/<button type="button" aria-label="分支会话（2）" title="分支会话（2）" aria-expanded="false" data-side-sessions="2"[^>]*>.*<span>2<\/span><\/button>/);
    expect(quiet).not.toContain("data-side-sessions-needs");
    const waiting = header({ sides: { count: 3, open: true, needs: true, onToggle: () => {} } });
    expect(waiting).toContain('aria-label="分支会话（3） · 有分支会话需要你处理"');
    expect(waiting).toContain('aria-expanded="true"');
    expect(waiting).toContain("data-side-sessions-needs");
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
      expect(html).toMatch(/title="Agent 运行于 MacBook，该电脑当前离线，重新上线后将继续执行。"[^>]*data-agent-device="offline"[^>]*opacity-75/);
    } finally {
      state().machines = machines;
    }
  });

  it("omits the duplicate branch badge from the header and places ⋯ at the right end with a hairline divider", () => {
    const html = bare(header({ session: { ...session, branch: "feat/net-tests" } as Session, view: "chat", onView: () => {} }));
    expect(html).not.toContain("data-chat-branch");
    expect(html).not.toContain("feat/net-tests");
    const files = html.indexOf('aria-label="文件"');
    const terminal = html.indexOf(`aria-label="${t("lib-terminals.terminal")}"`);
    const divider = html.indexOf("data-header-divider");
    const more = html.indexOf('aria-label="更多操作"');
    expect(files).toBeGreaterThan(-1);
    expect(terminal).toBeGreaterThan(files);
    expect(divider).toBeGreaterThan(terminal);
    expect(more).toBeGreaterThan(divider);
  });

  it("has no 同空间 button: the space's other agents are in ⋯ at every width, which carries their attention", () => {
    const html = bare(header());
    expect(html).not.toContain("同空间");
    expect(html).toMatch(/aria-label="更多操作"[^]*?<span aria-hidden="true" class="[^"]*bg-attention/);
    const calm = { ...sibling, state: "idle" } as Session;
    const quiet = { ...session, state: "idle" } as Session;
    expect(header({ session: quiet, siblings: [quiet, calm] })).not.toContain("bg-attention");
  });

  it("puts the loop mark before the Agent and reports background work on the overview toggle", () => {
    const looping = { ...session, loop: { kind: "interval", interval_seconds: 1800, prompt: "/loop check CI" } } as Session;
    const overview = { id: "overview", open: false, toggle: () => {} };
    const html = bare(header({ session: looping, overview }));
    const loop = html.indexOf("data-loop-mark");
    expect(loop).toBeGreaterThan(-1);
    expect(loop).toBeLessThan(html.indexOf("data-agent-device"));
    expect(html).toMatch(/aria-label="循环 · 每 30 分钟"/);
    expect(html).toMatch(/<button[^>]*aria-label="会话概览"/);
    expect(bare(header({ overview }))).toContain('aria-label="会话概览"');
    // a running background task: the button counts it
    const busy = { ...session, background: [{ id: "b1", kind: "bash", command: "npm test" }] } as Session;
    const counted = bare(header({ session: busy, overview }));
    expect(counted).toMatch(/aria-description="后台任务，1 个正在运行"/);
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

  it("puts what the panel gives beside the title (a task's ⌄ for its agents), with ⋯ in the right toolbar before close", () => {
    const html = header({ panel: { onClose: () => {}, title: "修登录崩溃", beside: <button aria-label="切换 Agent" /> } });
    expect(html).toMatch(/>修登录崩溃<\/h1><button aria-label="切换 Agent"><\/button>/);
    const beside = html.indexOf('aria-label="切换 Agent"');
    const more = html.indexOf('aria-label="更多操作"');
    const close = html.indexOf('aria-label="关闭面板"');
    expect(beside).toBeGreaterThan(-1);
    expect(more).toBeGreaterThan(beside);
    expect(close).toBeGreaterThan(more);
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
    expect(chat).toMatch(new RegExp(`<span class="relative flex shrink-0"><button[^>]*aria-label="${t("lib-terminals.terminal")}"`));
    expect(chat).not.toContain("radiogroup");
    const term = bare(header({ view: "term", onView: () => {} }));
    expect(term).toMatch(/<span class="relative flex shrink-0"><button[^>]*aria-label="对话"/);
  });
  it("is not there without a terminal to switch to", () => {
    expect(header()).not.toContain(`aria-label="${t("lib-terminals.terminal")}"`);
  });
  it("marks 对话 when the terminal shows and something wants an answer", () => {
    expect(header({ view: "term", onView: () => {}, viewDot: true })).toMatch(/aria-label="对话"(?:(?!<\/button>).)*<\/button><span aria-hidden="true" class="pointer-events-none absolute[^"]*bg-attention"/);
    expect(header({ view: "term", onView: () => {} })).not.toMatch(/aria-label="对话"(?:(?!<\/button>).)*<\/button><span aria-hidden/);
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
    const html = cards({ narrow: false, interactions: [], answered: [{ ix: perm, resolution: { allow: true }, at: Date.now() }] });
    expect(html).toContain("data-answered-about");
    expect(html).toContain("swift test");
    expect(html).toContain("允许");
    const q = ix("q", "question", { questions: [{ question: "用哪个框架？", options: [{ label: "A" }] }] as Interaction["questions"] });
    // a question's about line is the question itself (the answer is the pick)
    const qHtml = cards({ narrow: false, interactions: [], answered: [{ ix: q, resolution: { answers: { "用哪个框架？": "A" } }, at: Date.now() }] });
    expect(qHtml).toContain("data-answered-about");
    expect(qHtml).toContain("用哪个框架？");
    // a plan says its first line, not "Plan awaiting approval"; a bare one says nothing extra
    const plan = ix("l", "plan", { plan: "\n## 改用 **vitest**\n1. 换配置" });
    const planHtml = cards({ narrow: false, interactions: [], answered: [{ ix: plan, resolution: { approve: true }, at: Date.now() }] });
    expect(planHtml).toContain("改用 vitest");
    expect(planHtml).not.toContain("计划待审批");
    expect(cards({ narrow: false, interactions: [], answered: [{ ix: ix("l2", "plan"), resolution: { approve: true }, at: Date.now() }] })).not.toContain("data-answered-about");
    // a terminal dialog with nothing to say: no "Terminal needs you" above its Yes
    const dialog = ix("d", "screen", { dialog: { options: [{ key: "1", label: "Yes" }] } as Interaction["dialog"] });
    const dialogHtml = cards({ narrow: false, interactions: [], answered: [{ ix: dialog, resolution: { option: "1" }, at: Date.now() }] });
    expect(dialogHtml).not.toContain("data-answered-about");
    expect(dialogHtml).not.toContain("终端需要你处理");
    // handed back (#2450): nothing was decided, so no answer line repeats the head; the line says which request went back
    const handed = cards({ narrow: false, interactions: [], answered: [{ ix: perm, resolution: { dismiss: true }, at: Date.now() }] });
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

  it("names a side session's card as the side's, on its parent's page, once the hub folds sides (#7944)", () => {
    const card = { folded_under: "review", folded_as: "side" as const, session_title: "btw: can it merge" };
    expect(permissionFrom(card)).toBe("来自子 Agent · btw: can it merge");
    setSideFold(true);
    try {
      expect(permissionFrom(card)).toBe("来自分支会话 · btw: can it merge");
    } finally {
      setSideFold(false);
    }
  });

  it("shows only the first card on a wide screen, in no box of their own, with its position", () => {
    const html = cards({ narrow: false });
    expect(html).not.toContain("data-chat-cards=");
    expect(html).not.toContain("还有");
    expect(html).toContain("计划待审批");
    expect(html).not.toContain("用哪个框架？");
    expect(html).not.toContain("允许使用 Bash？");
    expect(html).toContain("data-chat-cards-position");
    expect(html).toContain("1 / 3");
  });

  it("scrolls them in a box of their own on a narrow one, the first open and its position chip for the rest", () => {
    const html = cards({ narrow: true });
    expect(html).toMatch(/data-chat-cards[^>]*overflow-y-auto/);
    expect(html).not.toContain("还有");
    expect(html).toContain("1 / 3");
    // the first card's fold is open, the others' closed (inert)
    const folds = [...html.matchAll(/<div class="grid [^"]*"([^>]*)>/g)].map((m) => m[1]);
    expect(folds).toHaveLength(2);
    expect(folds[0]).not.toContain("inert");
    expect(folds[1]).toContain("inert");
  });

  it("folds them into one line while the keyboard is up", () => {
    const html = cards({ narrow: true, keyboard: true });
    expect(html).toContain("3 项待处理");
    expect(html).not.toContain("还有");
    const folds = [...html.matchAll(/<div class="grid [^"]*"([^>]*)>/g)].map((m) => m[1]);
    expect(folds.every((f) => f.includes("inert"))).toBe(true);
  });

  it("is one line on a phone on its side", () => {
    const html = renderToStaticMarkup(<PendingCards interactions={three} answered={[]} narrow short keyboard={false} onDismissKeyboard={() => {}} />);
    expect(html).toContain("3 项待处理");
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
  const answered: AnsweredCard[] = [{ ix: ix("done", "question"), resolution: { answers: { q: "A" } }, at: Date.now() }];
  const other = ix("other", "question", { machine_id: "n", session_id: "t", session_title: "照片同步脚本支持 HEIC", questions: [{ question: "保存为哪种格式？", options: [] }] as Interaction["questions"] });
  // the store's cards: two of other chats, one of this chat (left out of the count)
  const seed = (list: Interaction[]) => Object.assign(getState(), { interactions: list, sessions: {} });
  afterEach(() => seed([]));

  it("says how many, which is next and what it asks under 已回答, linking to that chat", () => {
    seed([ix("mine", "permission"), other, ix("more", "permission", { machine_id: "n", session_id: "u" })]);
    for (const narrow of [false, true]) {
      const html = cards({ narrow, interactions: [], answered, offer: true });
      expect(html).toContain("已回答");
      expect(html).toContain("还有 2 项待你处理");
      expect(html).toContain("下一项：照片同步脚本支持 HEIC · 保存为哪种格式？");
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

  it("omits the attachment strip from an empty composer's dock", () => {
    vi.stubGlobal("localStorage", { getItem: () => null });
    const html = renderToStaticMarkup(<Composer draftKey="t" placeholder="…" onSend={async () => true} />);
    expect(html).not.toContain("data-composer-attachments");
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
    const html = cards({ narrow: false, interactions: [three[1]] });
    expect(hit(html, "交给电脑处理")).toBe(true);
    expect(html).toMatch(/<button[^>]*aria-pressed="false"[^>]*class="touch-hit /);
    expect(hit(html, "下一题") || hit(html, "发送回答")).toBe(true);
  });

  it("covers an attachment's remove button", () => {
    const html = renderToStaticMarkup(<AttachmentTray attachments={[{ id: "a", name: "a.png", url: "blob:a" }]} onRemove={() => {}} />);
    // grown out of the tile's corner, not centred on it: the picture's middle stays the picture's (ui/AttachmentTray.test.tsx)
    expect(html).toMatch(/<button[^>]*aria-label="移除 a.png"[^>]*class="touch-hit-corner /);
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
    expect(html).toMatch(/^<div role="region" aria-label="排队中的消息" data-queue-panel="true" class="w-full border-b border-line"/);
    expect(rows(html)).toHaveLength(2);
    expect(html.indexOf("顺便把截图换掉")).toBeLessThan(html.indexOf("跑一遍 UI 测试"));
    // the place is in the labels only
    expect(html).toContain('aria-label="第 1 条排队消息：顺便把截图换掉"');
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
    expect(claude).toContain('aria-label="立即将所有排队消息发送给 Claude，无需等待下一步"');
    expect(claude).toMatch(/title="立即将所有排队消息发送给 Claude，无需等待下一步\nClaude 将立即收到这些消息，无需等待下一步。/);
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
    expect(html).toContain("立即将所有排队消息发送给 Nova");
    // collapsed: no relayed row markup yet, just the summary line
    expect(html).not.toContain("data-queue-relayed-list");
    expect(html).toContain("1 条消息来自其他对话");
    expect(html).toContain("对话空闲后送达");
    // an own row sits above it, so the group has its hairline
    expect(html).toMatch(/data-queue-relayed-group="true" class="[^"]*border-t border-line/);
    expect(html).toContain("data-queue-relayed-summary");
    // relayed ones alone: nothing to send now
    const onlyRelayed = panel({ session: { ...persona, queued_relayed: 2 }, persona: "Nova" });
    expect(onlyRelayed).not.toContain("data-send-now");
    // no own rows: no own list and no hairline on the relayed group
    expect(onlyRelayed).not.toContain("px-1 pt-1.5");
    expect(onlyRelayed).not.toMatch(/data-queue-relayed-group="true" class="[^"]*border-t/);
  });

  it("marks the photos a Persona chat's queued message went with when this browser sent it (#4342)", () => {
    const persona = { ...queued, assistant: true, queued: ["请看图片", "顺便把截图换掉"] } as Session;
    const image = { mime: "image/png", data: "iVBORw0KGgo=" };
    const html = panel({ session: persona, persona: "Nova", pending: [{ id: "local", raw: persona.queued![0], before: [], images: [image, image] }] });
    expect(html.match(/data-queue-mark="photo"/g)).toHaveLength(1);
    expect(html).toContain('aria-label="第 1 条排队消息：2 张图片"');
    expect(html).not.toContain(">请看图片<");
  });

  // made-up words and paths, in the shape Claude Code queues a message sent with photos (#3032)
  const photo = (name: string) => `/Users/me/.persona/uploads/2026-11-10/${name}.jpg`;
  const withPhotos = `[Image #1] [Image #2]\n\n\n把这两张截图对一下\n<attached_images>\npath: ${photo("a1")}\npath: ${photo("b2")}\n</attached_images>`;

  it("shows a queued message's words and a mark per kind of attachment, never Claude's queue text (#3032)", () => {
    vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
    try {
      const photoOnly = `[Image #3] 请看图片\n\n<attached_images>\npath: ${photo("c3")}\n</attached_images>`;
      const html = panel({ session: { ...session, queued: [withPhotos, photoOnly] } as Session });
      expect(html).toContain("把这两张截图对一下");
      expect(html).not.toContain("[Image #");
      expect(html).not.toContain("attached_images");
      expect(html).not.toContain("path: ");
      expect(html).not.toContain("请看图片");
      expect(html.match(/data-queue-mark="photo"/g)).toHaveLength(2);
      expect(html.match(/data-queue-photo-thumb/g)).toHaveLength(3);
      expect(html).toContain('data-queue-header="true"');
      expect(html).toContain("已排队，尚未发送");
      // the label says what came with it
      expect(html).toContain('aria-label="第 1 条排队消息：把这两张截图对一下 · 2 张图片"');
      expect(html).toContain('aria-label="第 2 条排队消息：1 张图片"');
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe("restoreQueued (#3032)", () => {
  const photo = "/Users/me/.persona/uploads/2026-11-10/a1.jpg";
  const gone = "/Users/me/.persona/uploads/2026-11-10/gone.jpg";
  afterEach(() => {
    vi.unstubAllGlobals();
    for (const c of getContexts("rq")) removeContext("rq", c.id);
  });

  it("puts the words in the box, the photos in the tray, the files on their tiles, the contexts on their chips and a lost photo in as its path", async () => {
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
      expect(getComposerFiles("rq").map((f) => [f.state, f.file?.path])).toEqual([["ready", "/tmp/spec.pdf"]]); // #8361
      expect(events).toEqual([
        { type: "append", text: "Fix the header" },
        { type: "image", image: { name: "a1.jpg", mime: "image/jpeg", data: "AQID" } },
        { type: "append", text: gone },
      ]);
      expect(getContexts("rq").map(({ kind, title, text, attrs }) => ({ kind, title, text, attrs }))).toEqual([{ kind: "workspace", title: "web", text: "~/code/web", attrs: workspace.attrs }]);
    } finally {
      getComposerFiles("rq").forEach((f) => removeFile("rq", f.id));
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
