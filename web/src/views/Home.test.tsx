// The overview ("Overview") rendered statically (#2320): a waiting card is something to do, and "Mark all as seen" is there whenever
// an agent is unread, though its tab is filed under "Stuck, take a look" or "Working".
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Interaction, Machine, Session } from "../lib/types";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

vi.mock("react-dom", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-dom")>()),
  createPortal: (children: React.ReactNode) => children,
}));

const fake = vi.hoisted(() => ({ loaded: true, sessions: [] as Session[], interactions: [] as Interaction[], machines: [] as Machine[], todos: [] as Todo[], answered: {} as Record<string, unknown>, conn: undefined as "connecting" | "online" | "offline" | undefined, list: "loaded" as "loaded" | "loading" | "failed", partial: false }));

vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: fake.loaded, machines: fake.machines, interactions: fake.interactions, todos: fake.todos, answered: fake.answered as ReturnType<typeof real.getState>["answered"], ...(fake.conn ? { conn: fake.conn } : {}) }) };
});
vi.mock("../lib/sessions", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/sessions")>();
  return { ...real, useAgentSessions: () => fake.sessions, useCounts: () => ({ ...real.stateCounts(fake.sessions, fake.interactions), interactions: fake.interactions.length }), useAgentListStatus: () => ({ status: fake.list, whole: fake.list === "loaded" && !fake.partial }) };
});

// the extensions' overview cards are not this test's
// Overview draws its cards through OverviewBody, a row where the hub has cards (#10905): none unless a test gives some
const slots = vi.hoisted(() => ({ cards: [] as { extension: string; id: string; title: string; placement: string }[] }));
vi.mock("../lib/extension-slots", async (importOriginal) => ({ ...(await importOriginal<typeof import("../lib/extension-slots")>()), useExtensionSlot: (slot: string) => (slot === "overview_card" ? slots.cards : []) }));
vi.mock("./extensions/ExtensionSlots", () => ({ OverviewCards: () => <section data-overview-cards="" />, OverviewArrangeButton: () => null, OverviewArranging: () => null }));
// a card's answer stays on its way until the test lets it end
const op = vi.hoisted(() => ({ finish: (_: unknown) => {} }));
vi.mock("../lib/ops", async (importOriginal) => ({ ...(await importOriginal<typeof import("../lib/ops")>()), apiOp: () => new Promise((done) => { op.finish = done; }) }));

const { Home, offlineLine, rowTags } = await import("./Home");
const { answerCard } = await import("../lib/card-answer");
const { allowBody } = await import("../lib/quick-answers");
const { loadPages } = await import("../lib/lazy-page");
const { t } = await import("../i18n");

const mac: Machine = { id: "mac", name: "Mac", online: true };
let n = 0;
const agent = (slot: number, pane: number, state: Session["state"], o: Partial<Session> = {}): Session => ({
  machine_id: "mac",
  session_id: `s${++n}`,
  title: `agent ${n}`,
  cwd: "/code",
  status: "idle",
  state,
  space_kind: "herdr",
  space_id: "w1",
  space_name: "app",
  slot_id: `w1:t${slot}`,
  slot_index: slot,
  pane_id: `w1:p${pane}`,
  pane_index: pane,
  ...o,
});
// Preserve fake.loaded unless explicitly provided so tests can set the loading state themselves.
const render = (sessions: Session[], interactions: Interaction[] = [], { machines = [mac], loaded }: { machines?: Machine[]; loaded?: boolean } = {}) => {
  Object.assign(fake, { sessions, interactions, machines }, loaded === undefined ? {} : { loaded });
  return renderToStaticMarkup(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />);
};
const markAll = (html: string) => html.match(/<button [^>]*title="将 (\d+) 个完成未看的会话标为已看"[^>]*>/)?.[1];
// where a group's heading is in the page (-1: not there)
const heading = (html: string, title: string) => html.indexOf(`>${title}</h2>`);
const buttonLabels = (html: string) => (html.match(/<button\b[^>]*>[\s\S]*?<\/button>/g) ?? []).map((button) => button.replace(/<[^>]*>/g, ""));

describe("the overview", () => {
  it("restores green unread success badges without tinting Needs you rows", () => {
    const html = render([agent(1, 0, "unread"), agent(2, 0, "needs_you")]);
    expect(html).toMatch(/class="[^"]*bg-green-tint text-green" aria-label="完成未看"/);
    expect(html).toMatch(/class="[^"]*bg-field text-attention" aria-label="需要你"/);
    expect(html).not.toContain("bg-orange-tint");
    expect(html).not.toContain("bg-red-tint");
  });

  it("says what each waiting card asks, from whatever field has it (#3780)", () => {
    const a = agent(1, 0, "needs_you");
    const html = render([a], [
      { id: "q", kind: "question", machine_id: "mac", session_id: a.session_id, summary: "先做 A 还是 B？" } as Interaction,
      { id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash", tool_input: { command: "git status" } } as Interaction,
      { id: "l", kind: "plan", machine_id: "mac", session_id: a.session_id, plan: "# 计划" } as Interaction,
    ]);
    expect(html).toContain("先做 A 还是 B？");
    expect(html).toContain("git status");
    expect(html).toContain("计划已就绪，等待你的批准");
  });

  it("answers a permission or a one-choice question on its row, and sends a plan to its chat (#4090)", () => {
    const a = agent(1, 0, "needs_you");
    const buttons = (html: string) => [...html.matchAll(/<button [^>]*>(?:<[^>]+>)*([^<]+)/g)].map((m) => m[1]);
    const html = render([a], [
      { id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash", tool_input: { command: "git status", description: "运行测试" } } as Interaction,
      { id: "q", kind: "question", machine_id: "mac", session_id: a.session_id, questions: [{ question: "哪种格式？", options: [{ label: "JPEG" }, { label: "PNG" }] }] } as Interaction,
    ]);
    expect(buttons(html)).toEqual(expect.arrayContaining(["允许", "拒绝", "JPEG", "PNG"]));
    // what "Allow" allows, by tool and command, not the agent's description of it
    expect(html).toContain("Bash：git status");
    const plan = render([a], [{ id: "l", kind: "plan", machine_id: "mac", session_id: a.session_id, plan: "# 计划" } as Interaction]);
    expect(buttons(plan)).not.toContain("允许");
    expect(plan).toContain("计划已就绪，等待你的批准");
  });

  it("asks for a computer before offering to create an agent (#3729)", () => {
    const html = render([], [], { machines: [] });
    expect(html).toContain("暂无电脑");
    expect(html).toContain("连接一台电脑即可开始使用 Agent。");
    expect(html).toContain("将电脑连接到 hub 后，即可在这里新建 Agent 并随时跟进进展。");
    expect(buttonLabels(html)).toContain("打开机器页");
    expect(buttonLabels(html)).not.toContain("新建 Agent");
    expect(html).not.toContain("一切就绪");
    expect(html).not.toContain("所有 Agent 都在等待你的指令");
  });

  it("waits for the initial load before showing the no-computers guidance", () => {
    let html: string;
    try {
      html = render([], [], { machines: [], loaded: false });
    } finally {
      fake.loaded = true;
    }
    expect(html).toContain("正在连接 hub");
    expect(html).not.toContain("暂无电脑");
    expect(html).not.toContain("连接一台电脑");
    expect(buttonLabels(html)).not.toContain("打开机器页");
    expect(html).not.toContain("一切就绪");
    expect(html).not.toContain("所有 Agent 都在等待你的指令");
  });

  it("says it is loading, not connecting, once the hub is connected and its first answers are on their way (audit item 85)", () => {
    const at = (conn: "connecting" | "online" | "offline") => {
      Object.assign(fake, { conn });
      try {
        return render([], [], { loaded: false });
      } finally {
        Object.assign(fake, { conn: undefined, loaded: true });
      }
    };
    const connected = at("online");
    expect(connected).toContain("正在加载…");
    expect(connected).not.toContain("正在连接 hub");
    expect(at("connecting")).toContain("正在连接 hub…");
    expect(at("offline")).toContain("正在连接 hub…");
  });

  describe("when a paging hub's Agents list has not come back (audit item 30)", () => {
    const studio: Machine = { ...mac, system: { name: "macOS 26.1" } } as Machine;
    const ix = { id: "i1", kind: "permission", machine_id: "mac", session_id: "s-any", tool_name: "Bash" } as Interaction;
    const summaryLine = (html: string) => html.match(/<h1[^>]*>[^<]*<\/h1><div[^>]*>([\s\S]*?)<\/div><\/div>/)?.[1] ?? "";
    const as = (list: "loading" | "failed", run: () => string) => {
      fake.list = list;
      try {
        return run();
      } finally {
        fake.list = "loaded";
      }
    };

    it("says it couldn't read them, with Retry, instead of a lone full stop", () => {
      const html = as("failed", () => render([], [ix], { machines: [studio] }));
      const line = summaryLine(html);
      expect(line).toContain("无法加载 Agent 状态。");
      expect(buttonLabels(line)).toContain("重试");
      expect(line).not.toMatch(/>。<\/p>/);
      // the cards still wait below it
      expect(heading(html, "需要你")).toBeGreaterThan(-1);
    });

    it("gives no computer an agent count it doesn't have", () => {
      for (const list of ["failed", "loading"] as const) {
        const html = as(list, () => render([], [], { machines: [studio] }));
        const line = html.match(/data-machine-line="true">([^<]*)</)?.[1];
        expect(line).toBe("macOS 26.1");
        expect(html).not.toContain("0 个 Agent");
        // and does not say nothing needs you, or that all is set: nobody knows yet
        expect(html).not.toContain("一切就绪");
        expect(html).not.toContain("所有 Agent 都在等待你的指令");
      }
      const plain = as("failed", () => render([], [], { machines: [mac] }));
      expect(plain.match(/data-machine-line="true">([^<]*)</)?.[1]).toBe("在线");
      expect(render([], [], { machines: [studio] }).match(/data-machine-line="true">([^<]*)</)?.[1]).toBe("0 个 Agent · macOS 26.1");
    });

    it("gives no computer a count of only the list's first pages", () => {
      fake.partial = true;
      try {
        const html = render([agent(1, 0, "working")], [], { machines: [studio] });
        expect(html.match(/data-machine-line="true">([^<]*)</)?.[1]).toBe("macOS 26.1");
        // the summary is the hub's count of every agent: it stays
        expect(html).toContain("1 个工作中");
      } finally {
        fake.partial = false;
      }
    });

    it("says it is loading while the list is read", () => {
      const line = summaryLine(as("loading", () => render([], [ix], { machines: [studio] })));
      expect(line).toContain("正在加载…");
      expect(buttonLabels(line)).not.toContain("重试");
    });

    it("keeps asking for a computer first when there is none", () => {
      const html = as("failed", () => render([], [], { machines: [] }));
      expect(html).toContain("连接一台电脑即可开始使用 Agent。");
      expect(html).not.toContain("无法加载 Agent 状态");
    });
  });

  it("keeps the Computers row interaction style before and after the Rows view loads", async () => {
    const row = /<a [^>]*href="\/machines\/mac"[^>]*class="[^"]*\bcard-interactive\b/;
    expect(render([], [], { machines: [mac] })).toMatch(row);
    await loadPages();
    const { OVERVIEW_MACHINE_VIEW_KEY } = await import("./OverviewMachineView");
    localStorage.setItem(OVERVIEW_MACHINE_VIEW_KEY, "rows");
    try {
      const html = render([], [], { machines: [mac] });
      expect(html).toMatch(/role="radio" aria-checked="true" data-selected="true"[^>]*>行<\/button>/);
      expect(html).toMatch(row);
    } finally {
      localStorage.removeItem(OVERVIEW_MACHINE_VIEW_KEY);
    }
  });

  it("offers the Cards and Rows display switch on the Computers heading, with Cards selected by default", async () => {
    await loadPages();
    const html = render([], [], { machines: [mac] });
    expect(html).toContain('role="radiogroup" aria-label="显示方式"');
    expect(html).toMatch(/role="radio" aria-checked="true" data-selected="true"[^>]*>卡片<\/button>/);
    expect(html).toMatch(/role="radio" aria-checked="false" data-selected="false"[^>]*>行<\/button>/);
  });

  it("never builds an empty sentence: with only a card whose agent is not counted, it says how many requests wait (audit item 30)", () => {
    const other = { id: "q3", machine_id: "mac", session_id: "assistant-1", kind: "question" } as Interaction;
    const html = render([], [other]);
    expect(html).toContain("有 1 项请求待你处理。");
    expect(html).not.toMatch(/>。<\/p>/);
  });

  it("keeps the ready state with an existing online computer and no agents", () => {
    const html = render([], [], { machines: [mac] });
    expect(html).toContain("一切就绪");
    expect(html).toContain("所有 Agent 都在等待你的指令");
    expect(buttonLabels(html)).toContain("新建 Agent");
    expect(html).not.toContain("暂无电脑");
    expect(buttonLabels(html)).not.toContain("打开机器页");
  });

  it("makes an offline computer actionable instead of calling the page all set (#12561)", () => {
    const html = render([], [], { machines: [{ ...mac, online: false }] });
    expect(html).not.toContain("一切就绪");
    expect(html).toContain('data-actionable-computer="mac"');
    expect(html).toContain("有 1 项请求待你处理");
    expect(buttonLabels(html)).toContain("新建 Agent");
  });

  it("keeps an agent that only waits in the background out of 工作中, in its own section with the clock (#10938)", () => {
    const bg = { background_working: true, background_waiting: true, state_since: "2026-01-01T00:00:00Z" };
    const html = render([agent(1, 0, "working", { title: "busy one" }), agent(2, 0, "working", { title: "busy two" }), agent(3, 0, "working", { title: "watching CI", ...bg })]);
    expect(html).toContain("2 个工作中，1 个后台等待中。");
    const working = heading(html, "工作中"), waiting = heading(html, "后台等待中");
    expect(working).toBeGreaterThan(-1);
    expect(waiting).toBeGreaterThan(working);
    // each section's count is its rows'
    expect(html.slice(working, waiting)).toContain(">2</span>");
    expect(html.slice(working, waiting)).not.toContain("watching CI");
    const row = html.slice(waiting);
    expect(row).toContain("watching CI");
    expect(row).toContain('data-state="bg_waiting"');
    expect(row).not.toContain("busy one");
    // the spinner is Working's
    expect(html.slice(working, waiting).split("busy-motion").length - 1).toBe(2);
    expect(row).not.toContain("busy-motion");
  });

  it("is not all quiet with only an agent waiting in the background (#10938)", () => {
    const html = render([agent(1, 0, "working", { background_working: true, background_waiting: true })]);
    expect(html).toContain("1 个后台等待中。");
    expect(heading(html, "后台等待中")).toBeGreaterThan(-1);
    expect(heading(html, "工作中")).toBe(-1);
    expect(html).not.toContain("一切就绪");
  });

  it("marks the pane that only waits in a working tab's row (#10938)", () => {
    const html = render([agent(1, 0, "working", { state_since: "2026-01-01T00:00:00Z" }), agent(1, 1, "working", { background_working: true, background_waiting: true })]);
    expect(html).toContain("1 个工作中，1 个后台等待中。");
    expect(heading(html, "后台等待中")).toBe(-1);
    const row = html.slice(heading(html, "工作中"));
    expect(row.split('data-state="bg_waiting"').length - 1).toBe(1);
    expect(row).toContain("后台等待中");
  });

  it("puts the extensions' cards last, under the agents' sections and the computers (#11147)", () => {
    slots.cards = [{ extension: "example", id: "card", title: "Example", placement: "overview_card" }];
    const cards = (html: string) => html.indexOf("data-overview-cards");
    const asking = agent(1, 0, "needs_you");
    const bg = { background_working: true, background_waiting: true };
    const html = render([asking, agent(2, 0, "needs_you"), agent(3, 0, "working"), agent(5, 0, "working", bg), agent(4, 0, "unread")], [{ id: "i1", kind: "permission", machine_id: "mac", session_id: asking.session_id, tool_name: "Bash" } as Interaction]);
    const at = [heading(html, "需要你"), heading(html, "遇到阻塞，请查看"), heading(html, "工作中"), heading(html, "后台等待中"), heading(html, "完成未看"), heading(html, "电脑"), cards(html)];
    expect(at.every((x) => x > -1)).toBe(true);
    expect(at).toEqual([...at].sort((a, b) => a - b));
    // nothing to do: the ready card and the computers still come first
    const calm = render([]);
    expect(calm.indexOf("一切就绪")).toBeGreaterThan(-1);
    expect(calm.indexOf("一切就绪")).toBeLessThan(heading(calm, "电脑"));
    expect(heading(calm, "电脑")).toBeLessThan(cards(calm));
    // before the first snapshot: only 加载中, then the cards
    const early = render([agent(3, 0, "working")], [], { loaded: false });
    expect(early.indexOf("加载中")).toBeGreaterThan(-1);
    expect(early.indexOf("加载中")).toBeLessThan(cards(early));
    render([], [], { loaded: true });
    slots.cards = [];
  });

  it("does not say nothing needs you above a waiting card", () => {
    const asking = agent(1, 0, "needs_you");
    const html = render([asking, agent(2, 0, "idle")], [{ id: "i1", kind: "permission", machine_id: "mac", session_id: asking.session_id, tool_name: "Bash" } as Interaction]);
    expect(heading(html, "需要你")).toBeGreaterThan(-1);
    expect(html).not.toContain("暂无需要你处理的事项");
    expect(html).not.toContain("一切就绪");
    expect(render([agent(2, 0, "idle")])).toContain("暂无需要你处理的事项");
  });

  it("offers 全部标为已看 beside the summary when the unread agents' tabs are working or need you", () => {
    const html = render([agent(1, 0, "working"), agent(1, 1, "unread"), agent(2, 0, "needs_you"), agent(2, 1, "unread")]);
    expect(heading(html, "完成未看")).toBe(-1);
    expect(heading(html, "工作中")).toBeGreaterThan(-1);
    expect(html).toContain("遇到阻塞，请查看");
    expect(markAll(html)).toBe("2");
  });

  it("keeps it in 完成未看's header, for every unread agent", () => {
    const html = render([agent(1, 0, "working"), agent(1, 1, "unread"), agent(3, 0, "unread")]);
    const section = heading(html, "完成未看");
    expect(section).toBeGreaterThan(-1);
    expect(markAll(html)).toBe("2");
    expect(html.indexOf("全部标为已看")).toBeGreaterThan(section);
    expect(markAll(render([agent(1, 0, "working")]))).toBeUndefined();
  });

  it("says the headline counts agents and why the cards can be more (#2956)", () => {
    const a = agent(2, 0, "needs_you");
    const card = (id: string) => ({ id, machine_id: "mac", session_id: a.session_id, kind: "question" }) as Interaction;
    const two = render([a], [card("q1"), card("q2")]);
    expect(two).toContain("1 个 Agent 需要你");
    expect(two).toContain("同一个 Agent 可能会连续向你发起多项请求");
    // one card per agent: the numbers agree, nothing to explain
    expect(render([a], [card("q1")])).not.toContain("同一个 Agent 可能会连续向你发起多项请求");
    // two cards on one agent beside a stuck agent without a card: still one agent with several (#2956 review)
    const stuck = agent(3, 0, "needs_you");
    expect(render([a, stuck], [card("q1"), card("q2")])).toContain("同一个 Agent 可能会连续向你发起多项请求");
    // a card per agent, its agent not counted (an assistant session): nothing to explain
    const other = { id: "q3", machine_id: "mac", session_id: "assistant-1", kind: "question" } as Interaction;
    expect(render([a], [card("q1"), other])).not.toContain("同一个 Agent 可能会连续向你发起多项请求");
  });

  it("explains the agents that need you without a card (#2956)", () => {
    const html = render([agent(2, 0, "needs_you")]);
    expect(html).toContain("遇到阻塞，请查看");
    expect(html).toContain("这些 Agent 未发送待处理卡片，可能是遇到错误或已阻塞");
  });

  it("before the first snapshot shows only 加载中, not the agents and cards the socket has streamed in so far", () => {
    const asking = agent(1, 0, "needs_you");
    const ix = { id: "i1", kind: "permission", machine_id: "mac", session_id: asking.session_id, tool_name: "Bash" } as Interaction;
    fake.loaded = false;
    try {
      const early = render([asking, agent(2, 0, "working"), agent(3, 0, "unread")], [ix]);
      expect(early).toContain("加载中");
      for (const group of ["需要你", "遇到阻塞，请查看", "工作中", "完成未看", "电脑"]) expect(heading(early, group)).toBe(-1);
      expect(early).not.toContain("全部标为已看");
    } finally {
      fake.loaded = true;
    }
    const html = render([asking, agent(2, 0, "working"), agent(3, 0, "unread")], [ix]);
    expect(html).not.toContain("加载中");
    for (const group of ["需要你", "工作中", "完成未看", "电脑"]) expect(heading(html, group)).toBeGreaterThan(-1);
  });
});

describe("compact rows", () => {
  const tags = (html: string) => (html.match(/<span [^>]*data-row-tags[^>]*>([\s\S]*?)<\/span><\/span>/g) ?? []).map((t) => [...t.matchAll(/>([^<]+)<\/span>/g)].map((m) => m[1]));

  it("shows the title once: a tab named after it is not a tag, its machine and space are", () => {
    const title = "Migrate the shared storage client to the new region config";
    const html = render([agent(1, 0, "unread", { title, slot_name: title.slice(0, 30) + "…", last_text: "Pushed the branch." })]);
    expect(tags(html)).toEqual([["Mac", "app"]]);
    // (Continue's screen-reader label names the row too: not something on screen)
    expect(html.replace(/<span class="sr-only">[^<]*<\/span>/g, "").split(title).length - 1).toBe(2); // the title text and the row's tooltip
  });

  it("says a session's source once, as a tag, not again before its latest words", () => {
    const html = render([agent(1, 0, "unread", { source_kind: "runtime", agent_label: "DSH", capabilities: { read: true, send: true }, last_text: "Report ready." })]);
    expect(tags(html)[0]).toContain("DSH");
    expect(html).not.toContain("DSH · Report ready.");
    expect(html).toContain(">Report ready.<");
  });

  it("shows the latest words without their Markdown, a tab's row too (#10939)", () => {
    const words = "Done. **Result** is in `src/app.ts`:\n- see [the notes](https://example.com/notes)";
    const plain = "Done. Result is in src/app.ts: see the notes";
    const one = render([agent(1, 0, "unread", { last_text: words })]);
    expect(one).toContain(`>${plain}<`);
    expect(one).not.toContain("**Result**");
    const tab = render([agent(2, 0, "working", { last_text: words, state_since: "2026-01-01T00:00:00Z" }), agent(2, 1, "idle")]);
    expect(tab).toContain(`>${plain}<`);
    expect(tab).not.toContain("`src/app.ts`");
  });

  it("puts the rows of every agent section in columns on a wide page, cards with buttons later", () => {
    const a = agent(1, 0, "needs_you");
    const html = render([a, agent(2, 0, "unread"), agent(3, 0, "working")], [{ id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash", tool_input: { command: "ls" } } as Interaction]);
    expect(html.match(/@min-\[60rem\]:grid-cols-2 @min-\[100rem\]:grid-cols-3/g)?.length).toBe(2);
    // the cards with buttons only once each has room for its question and buttons
    expect(html.match(/@min-\[84rem\]:grid-cols-2 @min-\[120rem\]:grid-cols-3/g)?.length).toBe(1);
    expect(html).toContain("@container");
  });

  it("lets a section's only card fill a two-column row, capped at two of three in every grid", () => {
    const a = agent(1, 0, "needs_you");
    fake.todos = [{ id: "only", title: "Only task", created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z" }];
    let html: string;
    try {
      html = render([a, agent(2, 0, "working")], [{ id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash" } as Interaction]);
    } finally {
      fake.todos = [];
    }
    const grids = [...html.matchAll(/class="([^"]*\bgrid\b[^"]*)"/g)].map((m) => m[1]!.replaceAll("&amp;", "&").replaceAll("&gt;", ">"));
    const rows = grids.filter((c) => c.includes("@min-[60rem]:grid-cols-2") && c.includes("@min-[100rem]:grid-cols-3") && c.includes("items-start"));
    const tasks = grids.filter((c) => c.includes("@min-[60rem]:grid-cols-2") && c.includes("@min-[100rem]:grid-cols-3") && c.includes("items-stretch"));
    const cards = grids.filter((c) => c.includes("@min-[84rem]:grid-cols-2") && c.includes("@min-[120rem]:grid-cols-3"));
    expect([rows.length, tasks.length, cards.length]).toEqual([1, 1, 1]);
    expect(rows[0]).toContain("[&>:only-child]:col-span-full");
    expect(rows[0]).toContain("@min-[100rem]:[&>:only-child]:col-span-2");
    expect(tasks[0]).toContain("[&>:only-child]:col-span-full");
    expect(tasks[0]).toContain("@min-[100rem]:[&>:only-child]:col-span-2");
    expect(cards[0]).toContain("[&>:only-child]:col-span-full");
    expect(cards[0]).toContain("@min-[120rem]:[&>:only-child]:col-span-2");
  });

  it("keeps a short space name that only starts the title, and drops a repeat", () => {
    expect(rowTags("app store listing", ["Mac", "app", "#2"])).toEqual(["Mac", "app", "#2"]);
    expect(rowTags("Fix the login redirect loop", ["Mac", "web", "Fix the login redir…"])).toEqual(["Mac", "web"]);
    expect(rowTags("Untitled", ["Mac", "", undefined, false, "Mac"])).toEqual(["Mac"]);
  });
});

describe("an offline computer's line on the overview (#4038)", () => {
  const raw = "ssh: connect to host box port 22: Operation timed out";
  it("says why in plain words and keeps the raw error in the tooltip", () => {
    Object.assign(fake, { sessions: [], interactions: [], machines: [mac, { id: "box", name: "盒子", online: false, error: raw }] });
    const html = renderToStaticMarkup(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />);
    const line = html.match(/<span [^>]*data-machine-line[^>]*>([^<]*)<\/span>/g)?.find((s) => s.includes("Operation"));
    expect(line).toContain(`title="${raw}"`);
    expect(line).toContain(">连接超时<");
  });
  it("keeps the raw text when it does not say why, and 离线 without one", () => {
    expect(offlineLine({ id: "b", name: "b", online: false, error: "something odd" })).toBe("something odd");
    expect(offlineLine({ id: "b", name: "b", online: false })).toBe("离线");
  });
  it("reads it in the machines page's order: waiting to be confirmed, then the hub's error_code", () => {
    expect(offlineLine({ id: "b", name: "b", online: false, trust: "pending", error: "ssh: connect to host b port 22: Operation timed out" } as Machine)).toBe("从未连接");
    expect(offlineLine({ id: "b", name: "b", online: false, error_code: "node_service_down", error: "dial tcp: i/o timeout" } as Machine)).not.toBe("连接超时");
  });
});

// Tasks in progress (#6450): every open task with its stage, PR and held delivery, above the working agents; none, no section.
describe("tasks in progress", () => {
  const todo = (id: string, o: Partial<Todo> = {}): Todo => ({ id, title: `task ${id}`, created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z", ...o });
  it("lists open tasks with their stage, PR and delivery", () => {
    fake.todos = [
      todo("a", { stage: "review", pr_url: "https://example.com/o/r/pull/42", agents: [{ machine_id: "mac", session_id: "w", linked_at: "x" }] }),
      todo("b", { delivery: { state: "merged" } }),
      todo("c", { done_at: "2026-11-02T00:00:00Z" }),
    ];
    const html = render([agent(1, 1, "working", { session_id: "w" })]);
    fake.todos = [];
    expect(heading(html, "进行中的任务")).toBeGreaterThan(-1);
    expect(heading(html, "进行中的任务")).toBeLessThan(heading(html, "工作中"));
    expect(html).toContain('data-task="a"');
    expect(html).toContain('data-task="b"');
    expect(html).not.toContain('data-task="c"');
    expect(html).toContain('data-stage="review" data-state="current"');
    expect(html).toContain("https://example.com/o/r/pull/42");
    // the agent's computer (its row has no model or effort to name; Home.task-agents.test.tsx has the rest, #10904)
    expect(html.slice(html.indexOf('data-task="a"'), html.indexOf('data-task="b"'))).toContain('<span class="truncate">Mac</span>');
    expect(html).not.toContain("查看全部");
    expect(html).toContain("已合入，未上线");
  });
  it("labels a concern beside its existing status, without labelling an ordinary task", () => {
    fake.todos = [todo("concern", { kind: "concern" }), todo("ordinary")];
    const html = render([]);
    fake.todos = [];
    const concernCard = html.slice(html.indexOf('data-task="concern"'), html.indexOf('data-task="ordinary"'));
    expect(concernCard).toContain(`data-testid="todo-concern">${t("todos.concern")}<`);
    expect(concernCard).toContain("rounded-full");
    expect(html.match(/data-testid="todo-concern"/g)).toHaveLength(1);
  });
  it("stretches a row of task cards to one height with the agent line at the bottom", () => {
    fake.todos = [todo("a", { stage: "dev", agents: [{ machine_id: "mac", session_id: "w", linked_at: "x" }] }), todo("b", { agents: [{ machine_id: "mac", session_id: "v", linked_at: "x" }] })];
    const html = render([]);
    fake.todos = [];
    expect(html).toContain("items-stretch");
    for (const id of ["a", "b"]) expect(html.slice(html.indexOf(`data-task="${id}"`) - 120, html.indexOf(`data-task="${id}"`))).toContain("h-full");
    expect(html).toContain("mt-auto flex");
  });
  // #10905: Overview's own sections come in the user's arrangement, kept on the hub for every device; nothing arranged,
  // they are where they always were (the test above)
  it("In progress moved first comes right under the greeting, above Needs you; a hidden section is not drawn", async () => {
    const { adoptOverview } = await import("../lib/overview-cards");
    adoptOverview([{ section: "in_progress" }, { section: "needs_you" }, { section: "waiting_on_computer" }, { section: "working", hidden: true }, { section: "unread" }, { section: "computers" }]);
    fake.todos = [todo("a")];
    try {
      const html = render([agent(1, 0, "working"), agent(2, 0, "unread")], [{ id: "c1", machine_id: "mac", session_id: "s1", kind: "question", question: "ok?" } as Interaction]);
      expect(heading(html, "进行中的任务")).toBeGreaterThan(-1);
      expect(heading(html, "进行中的任务")).toBeLessThan(heading(html, "需要你"));
      expect(heading(html, "需要你")).toBeLessThan(heading(html, "完成未看"));
      expect(heading(html, "工作中")).toBe(-1);
      expect(html.indexOf("</h1>")).toBeLessThan(heading(html, "进行中的任务"));
    } finally {
      fake.todos = [];
      adoptOverview([]);
    }
    // put back: the default order again
    const html = render([agent(1, 0, "working")], [{ id: "c1", machine_id: "mac", session_id: "s1", kind: "question", question: "ok?" } as Interaction]);
    expect(heading(html, "需要你")).toBeLessThan(heading(html, "工作中"));
  });
  it("offers See all past six tasks", () => {
    fake.todos = Array.from({ length: 7 }, (_, i) => todo(`t${i}`));
    const html = render([]);
    fake.todos = [];
    expect(html.match(/data-task=/g)?.length).toBe(6);
    expect(html).toContain("查看全部");
  });
  it("moves a task whose card was answered here out of Needs you at once", () => {
    const s = agent(1, 1, "needs_you", { session_id: "w" });
    const ix = { id: "ix1", machine_id: "mac", session_id: "w", kind: "question" } as Interaction;
    fake.todos = [todo("a", { agents: [{ machine_id: "mac", session_id: "w", linked_at: "x" }] })];
    const before = render([s], [ix]);
    fake.answered = { ix1: { ix, resolution: {}, at: Date.now() } };
    const after = render([s], []);
    fake.todos = [];
    fake.answered = {};
    const pill = (html: string) => html.slice(html.indexOf('data-task="a"')).match(/rounded-full[^>]*>([^<]+)</)?.[1];
    expect(pill(before)).toBe("需要你");
    expect(pill(after)).not.toBe("需要你");
  });
  it("draws nothing without open tasks", () => {
    fake.todos = [todo("c", { done_at: "2026-11-02T00:00:00Z" })];
    const html = render([]);
    fake.todos = [];
    expect(heading(html, "进行中的任务")).toBe(-1);
  });
});

describe("a card the task's supervisor holds (lib/supervisor-held.ts)", () => {
  const dialog = { question: "Do you want to proceed?", detail: "rmdir build", options: [{ key: "1", label: "Yes" }, { key: "2", label: "No" }] };
  const screen = (sid: string, o: Partial<Interaction> = {}) => ({ id: `screen-${sid}`, machine_id: "mac", session_id: sid, kind: "screen", dialog, ...o }) as Interaction;
  const needsCount = (html: string) => html.match(/>需要你<\/h2><span[^>]*>(\d+)</)?.[1];

  it("is neither listed nor counted in Needs you, and offers no Yes / No; its agent is listed Working", () => {
    const a = agent(1, 0, "needs_you", { title: "清理构建目录" });
    const html = render([a], [screen(a.session_id, { delegating: true })]);
    expect(heading(html, "需要你")).toBe(-1);
    expect(html).not.toContain("rmdir build");
    expect(buttonLabels(html)).not.toContain("Yes");
    expect(buttonLabels(html)).not.toContain("No");
    expect(html).not.toContain("1 个 Agent 需要你");
    // waiting on the supervisor, not stuck on the user
    expect(heading(html, "遇到阻塞，请查看")).toBe(-1);
    expect(heading(html, "工作中")).toBeGreaterThan(-1);
    expect(html).toContain("清理构建目录");
  });

  it("leaves the agent's other card in Needs you, counted once", () => {
    const a = agent(1, 0, "needs_you");
    const html = render([a], [screen(a.session_id, { delegating: true }), { id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash", tool_input: { command: "git status" } } as Interaction]);
    expect(needsCount(html)).toBe("1");
    expect(html).toContain("git status");
    expect(html).not.toContain("rmdir build");
  });

  it("an unheld terminal dialog is listed, counted and answered on its row as before", () => {
    const a = agent(1, 0, "needs_you");
    const html = render([a], [screen(a.session_id)]);
    expect(needsCount(html)).toBe("1");
    expect(html).toContain("rmdir build");
    expect(buttonLabels(html)).toEqual(expect.arrayContaining(["Yes", "No"]));
  });
});

it("opens an explicitly sessionless request for review and offers a one-tap denial", () => {
  const html = render([], [{ id: "script-request", machine_id: "mac", session_id: "", kind: "permission", tool_name: "personad_ask", summary: "Publish the draft?" }]);
  expect(html).toContain('href="/requests/script-request"');
  expect(buttonLabels(html)).toEqual(expect.arrayContaining([t("approval-impact.review"), t("chat-interactions.deny")]));
  expect(buttonLabels(html)).not.toContain(t("chat-interactions.allow"));
  expect(html).not.toContain('href="/s/mac/"');
});

it("holds a row's answers while an answer to its card is on its way, given here or in its chat, and says which (#2793)", async () => {
  const card: Interaction = { id: "held-request", machine_id: "mac", session_id: "", kind: "permission", tool_name: "Bash", tool_input: { command: "git status" } };
  // Allow and Deny, each as disabled or not
  const held = (html: string) => (html.match(/<button\b[^>]*>[\s\S]*?<\/button>/g) ?? []).filter((b) => ["允许", "拒绝"].includes(b.replace(/<[^>]*>/g, ""))).map((b) => /\sdisabled=""/.test(b));
  // Allow given in the chat (or on this row before Overview was left) while the hub is away: this row is drawn afresh
  const sent = answerCard(card, allowBody());
  const html = render([], [card]);
  expect(held(html)).toEqual([true, true]);
  expect(html).toContain(t("chat-interactions.pending_offline", { answer: t("lib-answered.allowed") }));
  op.finish({});
  await sent;
  expect(held(render([], [card]))).toEqual([false, false]);
});

it("lets a card's kicker yield to its title on a phone, its words whole in its tooltip (#11442)", () => {
  const a = agent(1, 0, "needs_you");
  const html = render([a], [{ id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash", tool_input: { command: "git status" } } as Interaction]);
  const words = t("todos.kicker_permission");
  const m = new RegExp(`<span title="${words}" class="([^"]*)"><span class="truncate">${words}</span></span>`).exec(html);
  expect(m, "the kicker pill").toBeTruthy();
  const classes = m![1]!.split(" ");
  // on a phone it grows from an equal start with the title, only up to its words; wider, it stays its words' width
  expect(classes).toEqual(expect.arrayContaining(["shrink-0", "max-sm:min-w-0", "max-sm:flex-1", "max-sm:max-w-max"]));
  expect(classes.filter((c) => /^(?:flex-|grow|basis-|max-w-|min-w-)/.test(c))).toEqual([]);
});
