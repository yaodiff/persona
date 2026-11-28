// The open conversation's row in the sidebar (#2283): marked for assistive tech, its own fill, its actions without hover.
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { t } from "../i18n";
import { Icon } from "../ui/icons";
import type { AgentSession } from "../lib/agents";
import type { Session } from "../lib/types";
import { setAgentDetails } from "../lib/context-usage";
import { AgentRow, PersonaRow, SessionRow } from "./Sidebar";
import { unreadShown } from "./SidebarRows";
import { conversationMenuItems } from "./ConversationMenu";

// the sidebar's modules read the colour scheme and this browser's settings when they load
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

// the avatar reads the persona store, which has no server snapshot
vi.mock("./assistant/Persona", async (importOriginal) => ({ ...(await importOriginal<typeof import("./assistant/Persona")>()), PersonaAvatar: () => null }));

const session: Session = { machine_id: "mac", session_id: "s1", title: "整理文档", cwd: "/code", status: "idle", state: "idle" };
const rowProps = { s: session, k: "s:mac/s1", reg: () => () => {}, hover: () => {}, to: { name: "session", mid: "mac", sid: "s1" } as const, go: () => () => {}, onMenu: () => {} };
const link = (html: string) => html.match(/<a [^>]*>/)![0];
const more = (html: string) => html.match(/<button [^>]*aria-label="Agent 操作"[^>]*>/)![0];

describe("unread shown in sidebar counts", () => {
  const unseen: Session["view"] = { phase: "done", reason: { kind: "unseen" }, rank: 30 };

  it.each<{ name: string; patch: Partial<Session>; counted: boolean }>([
    { name: "an unopened reply", patch: { view: unseen }, counted: true },
    { name: "an unopened question", patch: { view: { phase: "done", reason: { kind: "asks", unseen: true }, rank: 30 } }, counted: true },
    { name: "an opened question", patch: { view: { phase: "done", reason: { kind: "asks" }, rank: 31 } }, counted: false },
    { name: "a just-finished turn", patch: { view: { phase: "done", reason: { kind: "just_finished" }, rank: 32 } }, counted: false },
    { name: "a reply marked seen locally", patch: { view: unseen, locallySeen: true }, counted: false },
    { name: "a usage-limit stop", patch: { state: "unread", view: { phase: "error", reason: { kind: "usage_limit" }, rank: 10 } }, counted: false },
    { name: "a legacy unread reply", patch: { state: "unread", status: "done" }, counted: true },
    { name: "a legacy usage-limit stop", patch: { state: "unread", status: "done", turn_error_kind: "usage_limit", turn_error: "Usage limit reached" }, counted: false },
  ])("checks the unread count for $name", ({ patch, counted }) => {
    expect(unreadShown({ ...session, ...patch })).toBe(counted);
  });
});

describe("an agent's row", () => {
  it("is aria-current with the open row's fill when it is open", () => {
    const html = renderToStaticMarkup(<SessionRow {...rowProps} active />);
    expect(link(html)).toContain('aria-current="page"');
    expect(link(html)).toMatch(/class="[^"]*\bbg-hover-2\b/);
  });
  it("draws an error phase with the error mark, not the Seen dot its activity folds it into (#16873)", () => {
    const mark = (s: Session) => renderToStaticMarkup(<SessionRow {...rowProps} s={s} showState active={false} />).match(/class="status-mark[^"]*"[^>]*data-state="([^"]+)"/)?.[1];
    expect(mark({ ...session, view: { phase: "error", reason: { kind: "start_refused" }, rank: 10 } } as Session)).toBe("error");
    expect(mark({ ...session, view: { phase: "done", reason: { kind: "unseen" }, rank: 30 } } as Session)).toBe("unread");
    expect(mark(session)).toBe("idle");
  });
  it("is neither when it is not", () => {
    const html = renderToStaticMarkup(<SessionRow {...rowProps} active={false} />);
    expect(link(html)).not.toContain("aria-current");
    expect(link(html)).not.toContain("bg-hover-2");
  });
  it("keeps its ⋯ shown when open, on hover only otherwise", () => {
    expect(more(renderToStaticMarkup(<SessionRow {...rowProps} active />))).toMatch(/class="[^"]*\bflex\b/);
    expect(more(renderToStaticMarkup(<SessionRow {...rowProps} active={false} />))).toMatch(/class="[^"]*\bhidden\b/);
  });
});

describe("where an agent was opened, as a mark at its row's end (#2526)", () => {
  const render = (patch: Partial<Session>, extra = {}) => renderToStaticMarkup(<SessionRow {...rowProps} s={{ ...session, ...patch }} active={false} {...extra} />);
  const mark = (html: string) => html.match(/<span data-source="[^"]*"[^>]*>.*?<\/span>/)?.[0];
  it("draws a terminal pane's as the terminal icon, named but without words", () => {
    const html = render({ pane_id: "w1:p1" });
    expect(mark(html)).toMatch(/aria-label="在标签页中"/);
    expect(mark(html)).toMatch(/title="在标签页中"/);
    expect(mark(html)).toContain("text-ink-3");
    expect(mark(html)).toContain('<rect x="2" y="4" width="20" height="16" rx="2.5"');
    expect(mark(html)).toContain("m6 9 3 3-3 3M12 15h5");
    expect(mark(html)).not.toMatch(/>终端</); // no visible word any more
    expect(link(html)).not.toContain(" title=");
  });
  it("draws a desktop app's own chat as a window, in its own color", () => {
    for (const patch of [{ space_kind: "codex_app", source_kind: "codex_history" }, { host: "paseo", source_kind: "runtime", source_instance_id: "paseo" }]) {
      const html = render(patch);
      expect(mark(html)).toMatch(/aria-label="桌面端"/);
      expect(mark(html)).toContain("text-source-desktop");
      expect(mark(html)).not.toContain("m6 9 3 3-3 3M12 15h5");
    }
  });
  it("draws Lark's mark for a Lark group, a link to its topic when it has one", () => {
    expect(mark(render({ pane_id: "botmux:oc_1/t1" }))).toMatch(/^<span data-source="feishu" role="img"[^>]*aria-label="飞书群"/);
    const linked = mark(render({ pane_id: "botmux:oc_1/t1", external_url: "https://example.com/topic" }));
    expect(linked).toMatch(/^<span data-source="feishu" role="link" tabindex="0"[^>]*aria-label="飞书群 · 在飞书中打开该话题"/);
    expect(linked).toContain("<img");
    for (const bad of ["javascript:alert(1)", "http://example.com/topic", "https:/admin", "https://user:pw@evil.example/topic"]) {
      expect(mark(render({ pane_id: "botmux:oc_1/t1", external_url: bad })), bad).toMatch(/^<span data-source="feishu" role="img"/);
    }
  });
  it("keeps the source mark and pending count without trailing age", () => {
    const timed = render({ pane_id: "botmux:oc_1/t1", external_url: "https://example.com/topic",
      updated_at: "2026-11-20T08:00:00Z", pending_interactions: 2 });
    expect(timed).not.toContain("data-row-age");
    expect(timed).toMatch(/data-source="feishu"[\s\S]*role="link"[\s\S]*>2<\/span><\/a>/);
    expect(render({ pane_id: "w1:p1" })).not.toContain("data-row-age");
  });
  it("draws none for other sources, Persona's conversations and a tab with several panes", () => {
    expect(mark(render({}))).toBeUndefined();
    expect(mark(render({ source_kind: "runtime", source_instance_id: "dsh" }))).toBeUndefined();
    expect(mark(render({ pane_id: "w1:p1", assistant: true }))).toBeUndefined();
    expect(mark(render({ pane_id: "w1:p1" }, { endRoom: true }))).toBeUndefined();
    expect(link(render({ pane_id: "w1:p1" }, { endRoom: true }))).not.toMatch(/\n终端/);
  });
  it("steps aside for the hover ⋯ like the rest of the row's end, and stays on the open row", () => {
    expect(mark(render({ pane_id: "w1:p1" }))).toContain("group-hover/row:invisible");
    expect(mark(renderToStaticMarkup(<SessionRow {...rowProps} s={{ ...session, pane_id: "w1:p1" }} active />))).not.toContain("invisible");
  });
  it("tells a framed terminal prompt and a desktop window apart by shape", () => {
    const svg = (html: string) => mark(html)!.match(/<svg[^>]*>(.*?)<\/svg>/)![1];
    const terminal = svg(render({ pane_id: "w1:p1" }));
    const desktop = svg(render({ space_kind: "codex_app", source_kind: "codex_history" }));
    expect(terminal).toContain("<rect");
    expect(desktop).toContain("<rect");
    expect(terminal).not.toBe(desktop);
  });
  it("leaves a runtime's name out of the visible tail whether or not it can send", () => {
    const paseo = { host: "paseo", source_kind: "runtime", source_instance_id: "paseo", agent_label: "Paseo" };
    const writable = render({ ...paseo, capabilities: { read: true, send: true } });
    expect(writable).not.toMatch(/>Paseo</);
    expect(render({ ...paseo, capabilities: { read: true, send: false } })).not.toMatch(/>[^<]*Paseo[^<]*</);
  });
  it("gives the title the text column instead of reserving room for source words", () => {
    const html = render({ space_kind: "codex_app", source_kind: "codex_history", agent_name: "Codex App" });
    expect(html).toMatch(/<span class="[^"]*flex-1 truncate[^"]*min-w-0[^"]*">整理文档<\/span>/);
    expect(html).not.toMatch(/>[^<]*Codex App[^<]*</);
    expect(html).not.toContain("basis-auto");
    expect(link(html)).not.toContain(" title=");
  });
  it("keeps a Persona title's date, kind and topic, with unread styling", () => {
    const title = "1125 | Design | Sidebar review";
    const html = render({ title, assistant: true, state: "unread", agent_name: "Antigravity", source_kind: "runtime", updated_at: "2026-11-20T08:00:00Z" }, { showState: false, keepTrailing: true });
    expect(html).toMatch(/class="[^"]*font-medium text-ink[^"]*">1125 \| Design \| Sidebar review<\/span>/);
    expect(html).not.toMatch(/>[^<]*Antigravity[^<]*</);
    expect(html).not.toContain("data-row-age");
  });
});

describe("an agent's row with 「Agent 行显示详情」 (#2430)", () => {
  const detailed: Session = { ...session, model: "claude-opus-5-5", context_tokens: 78000, context_window: 200000 };
  const render = (s: Session, extra = {}) => renderToStaticMarkup(<SessionRow {...rowProps} s={s} active={false} {...extra} />);
  afterEach(() => {
    setAgentDetails(false);
  });
  it("is byte for byte the same as before while the setting is off", () => {
    expect(render(detailed)).toBe(render(session)); // the model and context change nothing
    expect(render(detailed)).not.toContain("opus 5.5");
  });
  it("adds a second line with folder, model and labelled context used when on", () => {
    setAgentDetails(true);
    const html = render(detailed);
    expect(html).toContain("opus 5.5");
    expect(html).toContain("上下文 39%");
    expect(html).toContain("code");
  });
  it("uses amber caution from 80% context used", () => {
    setAgentDetails(true);
    expect(render({ ...detailed, context_tokens: 170000 })).toContain("text-orange");
    const danger = render({ ...detailed, context_tokens: 185000 });
    expect(danger).toContain("text-orange");
    expect(danger).not.toContain("text-red");
  });
  it("renders the agy context bar and share only with a known window", () => {
    setAgentDetails(true);
    const agy = { ...session, agent: "agy", model: "gemini-4-carbon-high", context_tokens: 262_144 };
    const known = render({ ...agy, context_window: 1_048_576 });
    expect(known).toContain('role="progressbar"');
    expect(known).toContain('aria-valuenow="25"');
    expect(known).toContain(t("context.label", { percent: 25 }));
    const unknown = render({ ...agy, model: "custom", context_window: 0 });
    expect(unknown).not.toMatch(/progressbar|%/);
  });
  it("draws no second line for a session that says nothing, or for a conversation of the assistant", () => {
    setAgentDetails(true);
    const plain = { ...session, cwd: "" };
    expect(render(plain)).not.toContain("flex-col");
    expect(render(detailed, { showState: false })).not.toContain("opus 5.5");
  });
});

describe("a personal agent's row", () => {
  const s = { id: "a1", title: "周报助手", provider: "lark", unreadCount: 0, rankTimeMs: 0 } as AgentSession;
  const props = { s, reg: () => () => {}, hover: () => {}, go: () => () => {}, pinned: false, onMenu: () => {} };
  const pin = (html: string) => html.match(/<button [^>]*aria-label="置顶"[^>]*>/)![0];
  it("is aria-current with the open row's fill and its pin shown when open", () => {
    const html = renderToStaticMarkup(<AgentRow {...props} active />);
    expect(link(html)).toContain('aria-current="page"');
    expect(link(html)).toMatch(/class="[^"]*\bbg-hover-2\b/);
    expect(pin(html)).toMatch(/class="[^"]*\bopacity-100\b/);
  });
  it("shows its pin on hover only otherwise", () => {
    const html = renderToStaticMarkup(<AgentRow {...props} active={false} />);
    expect(link(html)).not.toContain("aria-current");
    expect(pin(html)).toMatch(/class="[^"]*\bopacity-0\b/);
  });
  it("keeps an unread badge and renders identical resting markup when only activity time changes", () => {
    const read = renderToStaticMarkup(<AgentRow {...props} active={false} />);
    const recent = { ...s, rankTimeMs: Date.now() };
    expect(renderToStaticMarkup(<AgentRow {...props} s={recent} active={false} />)).toBe(read);
    const unread = renderToStaticMarkup(<AgentRow {...props} s={{ ...recent, unreadCount: 3 }} active={false} />);
    expect(unread).toMatch(/font-medium text-ink[^>]*>周报助手<\/span>/);
    expect(unread).toMatch(/tabular-nums[^>]*>3<\/span>/);
  });
});

describe("Persona's main row", () => {
  const main: Session = { machine_id: "hub", session_id: "main", title: "Persona", cwd: "/", status: "idle", state: "idle", assistant: true };
  const props = { reg: () => () => {}, hover: () => {}, go: () => () => {}, main };
  it("uses the existing home symbol and one Main chat line, without the assistant name or face", () => {
    const html = renderToStaticMarkup(<PersonaRow {...props} active />);
    const home = renderToStaticMarkup(<Icon name="home" size={14} stroke={1.8} className="shrink-0 text-ink" />);
    expect(html).toContain(home);
    expect(html).toContain(t("sidebar.main_chat"));
    expect(html).not.toContain("flex-col\"><span class=\"truncate text-[13.5px] font-semibold text-ink\">Persona");
    expect(html.replace(/<[^>]+>/g, "").match(new RegExp(t("sidebar.main_chat"), "g"))).toHaveLength(1);
  });
  it("has a ⋯ for the conversation's menu when the main conversation is there", () => {
    const html = renderToStaticMarkup(<PersonaRow {...props} active={false} onMenu={() => {}} />);
    expect(html).toMatch(/<button [^>]*aria-label="对话操作"[^>]*aria-haspopup="menu"/);
  });
  it("draws no ⋯ without it", () => {
    expect(renderToStaticMarkup(<PersonaRow {...props} main={undefined} active={false} />)).not.toContain("对话操作");
  });
  it("whose menu has only 复制会话 ID and 结束…", () => {
    expect(conversationMenuItems(main, { main: true }).map((i) => i.key)).toEqual(["copy", "end"]);
  });
});

describe("a 常驻 lead row (#6214 keepTrailing)", () => {
  const lead: Session = { machine_id: "hub", session_id: "lead-a", title: "增长", cwd: "/", status: "idle", state: "idle", assistant: true };
  const props = { s: lead, k: "j:lead-a", reg: () => () => {}, hover: () => {}, to: { name: "session", mid: "hub", sid: "lead-a" } as const, go: () => () => {}, onMenu: () => {}, hint: "在做 1 · 等你 1", hintFull: true, showState: false, keepTrailing: true };
  it("keeps its counts visible on hover and gives the ⋯ an in-flow slot instead of an overlay", () => {
    const html = renderToStaticMarkup(<SessionRow {...props} active={false} />);
    // the direction's name stays in the row even with no second line; the menu never replaces it
    expect(html).toContain("增长");
    // the counts never carry the hover-hiding class
    const counts = html.match(/<span[^>]*>在做 1 · 等你 1<\/span>/)![0];
    expect(counts).not.toContain("group-hover/row:invisible");
    // the ⋯ is an in-flow fader, not an absolute overlay covering the words
    const button = html.match(/<button [^>]*aria-label="Agent 操作"[^>]*>/)![0];
    expect(button).toContain("opacity-0 group-hover/row:opacity-100");
    expect(button).not.toContain("absolute");
    // and the anchor reserves no overlay room of its own
    expect(link(html)).toMatch(/pr-1\b/);
  });
  it("draws its name once, with or without a second line (it read 「Per… Per…」 in a narrow sidebar)", () => {
    const named = { ...props, label: "Persona 产品" };
    expect(renderToStaticMarkup(<SessionRow {...named} active={false} />).match(/>Persona 产品</g)).toHaveLength(1);
    expect(renderToStaticMarkup(<SessionRow {...named} active />).match(/>Persona 产品</g)).toHaveLength(1);
    // a second line puts the name in the column, still once
    const busy = { ...named, showState: true, s: { ...lead, last_text: "整理了三条进展" } };
    expect(renderToStaticMarkup(<SessionRow {...busy} active={false} />).match(/>Persona 产品</g)).toHaveLength(1);
  });
  it("an ordinary row still overlays its ⋯ and hides its tail on hover", () => {
    const html = renderToStaticMarkup(<SessionRow {...rowProps} active={false} />);
    expect(more(html)).toMatch(/class="[^"]*\bhidden\b/);
    expect(more(html)).toContain("absolute");
  });
});

describe("a session's associated tab name (#7595)", () => {
  const named: Session = { ...session, slot_id: "w1:t2", slot_name: "Release Review", pane_id: "w1:p1", updated_at: "2026-11-14T09:00:00Z" };
  const render = (patch: Partial<Session>, extra = {}) => renderToStaticMarkup(<SessionRow {...rowProps} s={{ ...session, ...patch }} hint="Project" active={false} {...extra} />);
  const tabName = (label: string) => t("sidebar.tab_named", { label });

  it("stays visible on a terminal row and follows a rename", () => {
    const first = render(named);
    expect(first).toContain(tabName("Release Review"));
    expect(first).not.toMatch(/>[^<]*Project[^<]*</);

    const renamed = render({ ...named, slot_name: "Triage" });
    expect(renamed).toContain(tabName("Triage"));
    expect(renamed).not.toContain(tabName("Release Review"));
  });

  it("keeps the session title and the existing fallbacks", () => {
    const numeric = render({ ...named, slot_name: "2" });
    expect(numeric).toContain("整理文档");
    expect(numeric).toContain(tabName("#2"));

    const missing = render({ ...named, slot_id: undefined, slot_name: undefined, pane_id: undefined });
    expect(missing).not.toContain(tabName("Release Review"));
    expect(missing).toContain("整理文档");

    const unassociated = render({ ...named, slot_id: undefined, tab_id: undefined, slot_name: "Workspace name" });
    expect(unassociated).not.toContain(tabName("Workspace name"));
    expect(unassociated).toContain("整理文档");

    expect(render(named, { endRoom: true })).toContain(tabName("Release Review"));
    expect(render({ ...named, status: "offline", state: "offline" })).toContain(tabName("Release Review"));
  });

  it("keeps the index-only fallback with a real association", () => {
    const indexOnly = render({ ...named, slot_name: undefined, slot_index: 2 });
    expect(indexOnly).toContain(tabName("#2"));
    expect(indexOnly).toContain("整理文档");
  });

  it("does not mint a localized identity from an unassociated structural fallback", () => {
    const html = render({ ...named, slot_id: undefined, tab_id: undefined, slot_name: "Workspace name", updated_at: undefined }, { tab: true, hint: undefined });
    expect(html).toContain("Workspace name");
    expect(html).not.toContain(tabName("Workspace name"));
  });
});

describe("a logical conversation's undelivered input on its row (#17431)", () => {
  const at = "2026-11-27T12:00:00Z";
  it("says how many wait, or that an expired one needs the owner, and leaves other rows alone", () => {
    const waiting: Session = { ...session, assistant: true, state: "working", undelivered: [
      { id: "head", text: "a", at, state: "waiting", reason: "awaiting_receipt", deadline: at },
      { id: "q", text: "b", at, state: "waiting", reason: "behind_unconfirmed", blocked_by: "head" },
    ] };
    const html = renderToStaticMarkup(<SessionRow {...rowProps} s={waiting} active={false} />);
    expect(html).toContain('data-undelivered-line="waiting"');
    expect(html).toContain(t("sidebar.undelivered_waiting", { count: 2 }));
    const expired: Session = { ...waiting, state: "needs_you", undelivered: [...waiting.undelivered!, { id: "old", text: "c", at, state: "failed", reason: "receipt_expired" }] };
    const alert = renderToStaticMarkup(<SessionRow {...rowProps} s={expired} active={false} />);
    expect(alert).toContain('data-undelivered-line="expired"');
    expect(alert).toContain('role="alert"');
    expect(alert).toContain(t("sidebar.undelivered_expired", { count: 1 }));
    // #10194's kept messages say it in their bubbles alone; a row without any draws nothing new
    const kept: Session = { ...session, undelivered: [{ id: "k", text: "d", at, state: "failed" }] };
    expect(renderToStaticMarkup(<SessionRow {...rowProps} s={kept} active={false} />)).not.toContain("data-undelivered-line");
    expect(renderToStaticMarkup(<SessionRow {...rowProps} active={false} />)).not.toContain("data-undelivered-line");
  });
});
