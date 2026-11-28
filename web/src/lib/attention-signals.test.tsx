// @vitest-environment jsdom
// The system notifications of useAttentionSignals (#2971): "Needs you" at once, "Done" after the settle time and only for a
// long turn (Settings › Notifications "Long tasks only"), and only while the page is not in front.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentState, Interaction, Session } from "./types";

let sessions: Session[] = [];
let mutes: { spaces: string[]; sessions: string[]; allowed: string[] } = { spaces: [], sessions: [], allowed: [] };
let interactions: Interaction[] = [];
let hubPrefs: Record<string, unknown> = {};
// the hub's events this page listens to, and what its client_hello declares beyond the basics (#8145)
const hubListeners = new Map<string, Set<(msg: Record<string, unknown>) => void>>();
let helloExtras: () => string[] = () => [];
let hellos: string[][] = [];
let assistantRows: Session[] = [];
let pushGrading = false;
let badgeCount = 0;
vi.mock("./store", () => ({
  useStore: (select: (s: { loaded: boolean; interactions: Interaction[]; pushGrading: boolean }) => unknown) =>
    select({ loaded: true, interactions, pushGrading }),
  getState: () => ({ mutes, interactions, pushGrading, sessions: { server: assistantRows }, assistant: { machine_id: "server", main: "main", conversations: [] } }),
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    if (!hubListeners.has(type)) hubListeners.set(type, new Set());
    hubListeners.get(type)!.add(l);
    return () => void hubListeners.get(type)?.delete(l);
  },
  onHello: () => () => {},
  appSocketReady: () => false,
  send: () => {},
  connect: () => {},
  disconnect: () => {},
  DESKTOP_UNREGISTER_FEATURE: "desktop_unregister",
  setHelloExtras: (fn: () => string[]) => void (helloExtras = fn),
  sayHello: () => void hellos.push(helloExtras()),
}));
vi.mock("./sessions", () => ({ useCounts: () => ({ badge: badgeCount }), useAgentSessions: () => sessions }));
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  api: async () => ({ input: true, done: "long", long_turn_seconds: 60, ...hubPrefs }),
  enc: encodeURIComponent,
}));

const { preloadAttentionSignals, useAttentionSignals } = await import("./attention-signals");
await preloadAttentionSignals();
const { BATCH_MAX_MS, BATCH_QUIET_MS, SETTLE_MS } = await import("./attention");
const { _setTabId, markFront } = await import("./notify");

const T0 = Date.parse("2026-01-01T10:00:00Z");
const session = (id: string, state: AgentState, since?: string): Session => ({
  machine_id: "m", session_id: id, title: id, cwd: "/p", status: state, state, state_since: since, last_text: `${id} 的最后一句`,
});

const posted: { title: string; body?: string; tag?: string }[] = [];
const closed: string[] = [];
const notifications: FakeNotification[] = [];
class FakeNotification {
  tag?: string;
  static permission = "granted";
  static requestPermission = async () => "granted";
  onclick: (() => void) | null = null;
  title: string;
  constructor(title: string, opts?: { body?: string; tag?: string }) {
    this.title = title;
    this.tag = opts?.tag;
    posted.push({ title, body: opts?.body, tag: opts?.tag });
    notifications.push(this);
  }
  close() {
    closed.push(this.title);
  }
}

function Probe() {
  useAttentionSignals("Persona", undefined);
  return null;
}

let root: Root;
let focused = false;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  vi.setSystemTime(T0);
  vi.stubGlobal("Notification", FakeNotification);
  vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
  vi.spyOn(window, "focus").mockImplementation(() => {});
  history.replaceState(null, "", "/");
  localStorage.setItem("persona.systemNotify", "1");
  posted.length = 0;
  closed.length = 0;
  notifications.length = 0;
  interactions = [];
  focused = false;
  mutes = { spaces: [], sessions: [], allowed: [] };
  hellos.length = 0;
  assistantRows = [];
  hubPrefs = {};
  pushGrading = false;
  badgeCount = 0;
  _setTabId("tab-me");
  root = createRoot(document.createElement("div"));
});
afterEach(() => {
  act(() => root.unmount());
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

async function show(next: Session[], cards: Interaction[] = interactions) {
  sessions = next;
  interactions = cards;
  await act(async () => root.render(<Probe key="p" />));
}

describe("系统通知", () => {
  it("需要你马上发；做完的长任务等一会儿再发，短的不发", async () => {
    await show([session("long", "working", "2026-01-01T09:58:00Z"), session("short", "working", "2026-01-01T09:59:50Z"), session("ask", "working")]);
    await act(async () => {}); // the prefs load
    await show([session("long", "unread"), session("short", "unread"), session("ask", "needs_you")]);
    expect(posted).toEqual([{ title: "ask 需要你", body: "ask 的最后一句", tag: "m/ask#needs" }]);
    await act(async () => void vi.advanceTimersByTime(SETTLE_MS + BATCH_QUIET_MS + 10));
    expect(posted.map((p) => p.title)).toEqual(["ask 需要你", "long 已完成"]);
  });

  it("页面在前台时不发", async () => {
    focused = true;
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted).toEqual([]);
  });

  it("开关没开时不发", async () => {
    localStorage.removeItem("persona.systemNotify");
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted).toEqual([]);
  });

  it("屏蔽了的 Agent 需要你时不发（和手机推送、Mac 一样）", async () => {
    mutes = { spaces: [], sessions: ["m/quiet"], allowed: [] };
    await show([session("quiet", "working"), session("loud", "working")]);
    await show([session("quiet", "needs_you"), session("loud", "needs_you")]);
    expect(posted.map((p) => p.title)).toEqual(["loud 需要你"]);
  });

  it("另一个标签页在前台时不发", async () => {
    _setTabId("tab-other");
    markFront(true);
    _setTabId("tab-me");
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted).toEqual([]);
  });

  it("同一件事别的标签页刚发过就不再发", async () => {
    localStorage.setItem("persona.notified", JSON.stringify({ "m/a#needs": Date.now() }));
    await show([session("a", "working"), session("b", "working")]);
    await show([session("a", "needs_you"), session("b", "needs_you")]);
    expect(posted.map((p) => p.title)).toEqual(["b 需要你"]);
  });
});

describe("Always show system notifications (#2451)", () => {
  beforeEach(() => localStorage.setItem("persona.systemNotifyAlways", "1"));

  it("posts while this page is in front, and clicking opens the conversation", async () => {
    focused = true;
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted).toEqual([{ title: "a 需要你", body: "a 的最后一句", tag: "m/a#needs" }]);
    act(() => notifications[0].onclick?.());
    expect(location.pathname).toBe("/s/m/a");
  });

  it("posts while another tab is in front", async () => {
    _setTabId("tab-other");
    markFront(true);
    _setTabId("tab-me");
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted.map((p) => p.title)).toEqual(["a 需要你"]);
  });

  it("still once for all tabs: one another tab just posted is not posted again", async () => {
    focused = true;
    localStorage.setItem("persona.notified", JSON.stringify({ "m/a#needs": Date.now() }));
    await show([session("a", "working"), session("b", "working")]);
    await show([session("a", "needs_you"), session("b", "needs_you")]);
    expect(posted.map((p) => p.title)).toEqual(["b 需要你"]);
  });

  it("does nothing while the main switch is off", async () => {
    focused = true;
    localStorage.removeItem("persona.systemNotify");
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted).toEqual([]);
  });
});

describe("一批做完的合成一条（#4945）", () => {
  const long = "2026-01-01T09:58:00Z";
  const working = (...ids: string[]) => ids.map((id) => session(id, "working", long));
  const settle = (ms = SETTLE_MS + BATCH_QUIET_MS + 10) => act(async () => void vi.advanceTimersByTime(ms));
  beforeEach(() => localStorage.setItem("persona.notifyPrefs", JSON.stringify({ input: true, done: "always" })));

  it("几个先后做完：只弹一条，写几个、都是谁，点开去总览", async () => {
    await show(working("a", "b", "c"));
    await show([session("a", "unread"), ...working("b", "c")]);
    vi.advanceTimersByTime(500);
    await show([session("a", "unread"), session("b", "unread"), ...working("c")]);
    vi.advanceTimersByTime(500);
    await show([session("a", "unread"), session("b", "unread"), session("c", "unread")]);
    await settle();
    expect(posted).toEqual([{ title: "3 个任务已完成 · a、b、c", body: "点击查看概览", tag: "finished#batch" }]);
  });

  it("超过三个只列三个；一直有新的做完也最多等一小段", async () => {
    const ids = ["a", "b", "c", "d", "e", "f"];
    await show(working(...ids));
    for (let i = 1; i <= ids.length; i++) {
      await show([...ids.slice(0, i).map((id) => session(id, "unread")), ...working(...ids.slice(i))]);
      await act(async () => void vi.advanceTimersByTime(1200));
    }
    // the first five went out BATCH_MAX_MS after the first joined; the sixth joins them in the next one
    await settle();
    expect(BATCH_MAX_MS).toBeLessThan(5 * 1200);
    expect(posted.map((p) => p.title)).toEqual(["5 个任务已完成 · a、b、c 等", "6 个任务已完成 · a、b、c 等"]);
  });

  it("等的时候有几个已经看过了：剩一个就照原来的单条", async () => {
    await show(working("a", "b"));
    await show([session("a", "unread"), session("b", "unread")]);
    vi.advanceTimersByTime(SETTLE_MS + 100);
    await show([session("a", "idle"), session("b", "unread")]);
    await settle();
    expect(posted).toEqual([{ title: "b 已完成", body: "b 的最后一句", tag: "m/b#finished" }]);
  });

  it("需要你不合并，照样各弹一条", async () => {
    await show(working("a", "b", "c", "d"));
    await show([session("a", "needs_you"), session("b", "needs_you"), session("c", "unread"), session("d", "unread")]);
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["a 需要你", "b 需要你", "2 个任务已完成 · c、d"]);
  });

  it("下一批替换这一条，还没看的留在里面；都看过了就收回", async () => {
    // ids no other test posted for: the notify module keeps its notifications between tests
    const at = (...st: AgentState[]) => ["p", "q", "r", "s"].map((id, i) => session(id, st[i], long));
    await show(working("p", "q", "r", "s"));
    await show(at("unread", "unread", "working", "working"));
    await settle();
    await show(at("idle", "unread", "unread", "unread"));
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["2 个任务已完成 · p、q", "3 个任务已完成 · q、r、s"]);
    await show(at("idle", "idle", "idle", "unread"));
    expect(closed).toEqual([]);
    await show(at("idle", "idle", "idle", "idle"));
    expect(closed).toEqual(["3 个任务已完成 · q、r、s"]);
  });

  it("收回后同一批 Agent 很快又做完：照样弹", async () => {
    const at = (st: AgentState) => ["u", "v"].map((id) => session(id, st, long));
    await show(working("u", "v"));
    await show(at("unread"));
    await settle();
    await show(at("idle"));
    expect(closed).toEqual(["2 个任务已完成 · u、v"]);
    await show(working("u", "v"));
    await show(at("unread"));
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["2 个任务已完成 · u、v", "2 个任务已完成 · u、v"]);
  });

  it("别的标签页已经发了这一批：这页不再发，自己的单条收回", async () => {
    await show(working("g", "h"));
    await show([session("g", "unread", long), ...working("h")]);
    await settle();
    localStorage.setItem("persona.notified", JSON.stringify({ "finished#batch@m/g,m/h": Date.now() }));
    await show([session("g", "unread", long), session("h", "unread", long)]);
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["g 已完成"]);
    expect(closed).toEqual(["g 已完成"]);
  });

  it("先单独弹过的，并进下一批时收回单条", async () => {
    await show(working("x", "y", "z"));
    await show([session("x", "unread", long), ...working("y", "z")]);
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["x 已完成"]);
    await show([session("x", "unread", long), session("y", "unread", long), session("z", "unread", long)]);
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["x 已完成", "3 个任务已完成 · x、y、z"]);
    expect(closed).toEqual(["x 已完成"]);
  });
});

describe("已在需要你时又来一张卡（#4058）", () => {
  const ask: Interaction = { id: "q1", machine_id: "m", session_id: "a", kind: "question", created_at: "2026-01-01T10:00:00Z", questions: [{ question: "用哪个框架？" } as never] };
  const perm: Interaction = { id: "p1", machine_id: "m", session_id: "a", kind: "permission", created_at: "2026-01-01T10:00:05Z", tool_name: "Bash", tool_input: { command: "go test ./..." } };

  it("第二张卡也弹，写的是这张卡；同一张卡不重复弹", async () => {
    await show([session("a", "working")]);
    await show([session("a", "needs_you")], [ask]);
    expect(posted).toEqual([{ title: "a 向你提问", body: "用哪个框架？", tag: "m/a#needs" }]);
    await show([session("a", "needs_you")], [ask, perm]);
    expect(posted.at(-1)).toEqual({ title: "a 需要你的授权", body: "go test ./...", tag: "m/a#needs" });
    await show([session("a", "needs_you")], [perm, ask]); // a snapshot again, the same cards
    await show([{ ...session("a", "needs_you"), last_text: "新的一句" }]);
    expect(posted).toHaveLength(2);
  });

  it("通知说的那张卡答掉了就收回，会话还在等另一张也收回", async () => {
    await show([session("a", "needs_you")], [ask]);
    await show([session("a", "needs_you")], [ask, perm]);
    expect(posted.map((p) => p.title)).toEqual(["a 需要你的授权"]);
    await show([session("a", "needs_you")], [ask]);
    expect(closed).toEqual(["a 需要你的授权"]);
    await show([session("a", "needs_you")], [ask]);
    expect(closed).toHaveLength(1);
  });

  it("需要你先到、卡后到（Codex 授权）：通知改写成这张卡，不算第二次", async () => {
    await show([session("a", "working")]);
    await show([session("a", "needs_you")], []);
    expect(posted.map((p) => p.title)).toEqual(["a 需要你"]);
    vi.setSystemTime(T0 + 10_000);
    await show([session("a", "needs_you")], [perm]);
    expect(posted.at(-1)).toEqual({ title: "a 需要你的授权", body: "go test ./...", tag: "m/a#needs" });
    await show([session("a", "needs_you")], [perm, { ...ask, id: "q2" }]);
    expect(posted.at(-1)?.title).toBe("a 向你提问");
  });

  it("刚打开页面时已有的卡不弹", async () => {
    await show([session("a", "needs_you")], [ask, perm]);
    expect(posted).toEqual([]);
  });

  it("屏蔽了的 Agent 新卡不弹", async () => {
    mutes = { spaces: [], sessions: ["m/a"], allowed: [] };
    await show([session("a", "needs_you")], [ask]);
    await show([session("a", "needs_you")], [ask, perm]);
    expect(posted).toEqual([]);
  });

  describe("授权卡的通知带「允许 / 拒绝」（#4464）", () => {
    let shown: { title: string; ix?: string; created_at?: string }[];
    beforeEach(() => {
      shown = [];
      (FakeNotification as unknown as { maxActions: number }).maxActions = 2;
      vi.stubGlobal("matchMedia", () => ({ matches: true }));
      const reg = { showNotification: async (title: string, o: { data?: { ix?: string; created_at?: string } }) => void shown.push({ title, ix: o.data?.ix, created_at: o.data?.created_at }), getNotifications: async () => [] };
      vi.stubGlobal("navigator", { ...navigator, serviceWorker: { controller: {}, ready: Promise.resolve(reg), getRegistration: async () => reg, addEventListener() {}, removeEventListener() {} } });
    });
    afterEach(() => delete (FakeNotification as unknown as { maxActions?: number }).maxActions);

    it("能批的会话：经 service worker 弹，带卡片 occurrence", async () => {
      await show([session("a", "working")]);
      await show([session("a", "needs_you")], [perm]);
      await act(async () => {});
      expect(shown).toEqual([{ title: "a 需要你的授权", ix: "p1", created_at: perm.created_at }]);
      expect(posted).toEqual([]);
    });

    it("要先填内容的授权卡（fields）：不带按钮", async () => {
      await show([session("a", "working")]);
      await show([session("a", "needs_you")], [{ ...perm, fields: [{ key: "pw", label: "密码", secret: true }] }]);
      await act(async () => {});
      expect(shown).toEqual([]);
      expect(posted.map((p) => p.title)).toEqual(["a 需要你的授权"]);
    });

    it("不能批的会话（approve=false）：不带按钮，照旧弹普通通知", async () => {
      const locked = { ...session("a", "needs_you"), source_kind: "codex_history", capabilities: { approve: false } } as Session;
      await show([{ ...locked, state: "working", status: "working" }]);
      await show([locked], [perm]);
      await act(async () => {});
      expect(shown).toEqual([]);
      expect(posted.map((p) => p.title)).toEqual(["a 需要你的授权"]);
    });
  });
});

it("posts independent notifications for sessionless requests and closes only the settled one", async () => {
  await show([], []);
  const card = (id: string): Interaction => ({ id, machine_id: "example-computer", session_id: "", kind: "permission", tool_name: "personad_ask", agent_label: "Script", summary: "Publish the draft?" });
  await show([], [card("request-one"), card("request-two")]);
  expect(posted.map((p) => p.tag)).toEqual(["card:request-one#needs", "card:request-two#needs"]);
  notifications[1].onclick?.();
  expect(window.location.pathname).toBe("/requests/request-two");
  expect(closed).toHaveLength(1);
  closed.length = 0;
  await show([], [card("request-one"), card("request-two")]);
  expect(posted).toHaveLength(2);
  await show([], [card("request-two")]);
  expect(closed).toHaveLength(1);
});

it("keeps sessionless request notifications under the existing device switch", async () => {
  localStorage.removeItem("persona.systemNotify");
  await show([], []);
  await show([], [{ id: "quiet-request", machine_id: "example-computer", session_id: "", kind: "permission", summary: "Publish the draft?" }]);
  expect(posted).toEqual([]);
});

describe("the hub's notify events: the assistant's conversations (#8145)", () => {
  const notify = (msg: Record<string, unknown>) => act(() => hubListeners.get("notify")?.forEach((l) => l({ type: "notify", ...msg })));
  const askCard: Interaction = { id: "ask1", machine_id: "server", session_id: "main", session_title: "Persona", kind: "question", tool_name: "ask_user", questions: [{ question: "Move tomorrow's meeting?" } as never] };
  const askNotify = { kind: "interaction", machine_id: "server", session_id: "main", interaction_id: "ask1", title: "Persona is asking you", subtitle: "Persona · hub", body: "Move tomorrow's meeting?" };
  /** The page as it opens: its hello asks for the hub's events, and the few seconds of what the hub kept go by. */
  async function open(next: Session[] = [], cards: Interaction[] = []) {
    await show(next, cards);
    await act(async () => {}); // the prefs load
    await act(async () => void vi.advanceTimersByTime(3_001));
  }

  it("asks for them while the page can alert, and says so again when that changes", async () => {
    await open();
    expect(hellos).toEqual([["ws_notify"]]);
    localStorage.removeItem("persona.systemNotify");
    act(() => void window.dispatchEvent(new Event("storage")));
    expect(hellos.at(-1)).toEqual([]);
    localStorage.setItem("persona.attentionSound", "1");
    act(() => void window.dispatchEvent(new Event("storage")));
    expect(hellos.at(-1)).toEqual(["ws_notify"]);
    const before = hellos.length;
    act(() => void window.dispatchEvent(new Event("focus"))); // nothing changed: no hello
    expect(hellos).toHaveLength(before);
  });

  it("does not ask while both the sound and system notifications are off", async () => {
    localStorage.removeItem("persona.systemNotify");
    await open();
    expect(hellos).toEqual([]);
    expect(helloExtras()).toEqual([]);
  });

  it("a new ask_user card in the main chat posts the hub's words, and goes when the card is answered", async () => {
    await open([], [askCard]);
    await notify(askNotify);
    expect(posted).toEqual([{ title: "Persona is asking you", body: "Persona · hub\nMove tomorrow's meeting?", tag: "server/main#needs" }]);
    notifications[0].onclick?.();
    expect(window.location.pathname).toBe("/s/server/main");
    closed.length = 0; // the click closed it
    await show([], []);
    expect(closed).toEqual(["Persona is asking you"]);
  });

  it("drops a retained notification whose occurrence was replaced", async () => {
    const replacement: Interaction = { ...askCard, kind: "permission", created_at: "2026-01-02T03:04:05.000000002Z" };
    await open([], [replacement]);
    await notify({ ...askNotify, title: "Old request", body: "Answer the old request", interaction_created_at: "2026-01-02T03:04:05.000000001Z" });
    expect(posted).toEqual([]);
  });

  it("a leader's card and the assistant's notify_user post too, each once for every tab", async () => {
    await open();
    await notify({ ...askNotify, session_id: "leader", interaction_id: "ask2" });
    await notify({ kind: "assistant", machine_id: "server", session_id: "main", title: "Persona", body: "The report is ready for you to sign" });
    await notify({ kind: "assistant", machine_id: "server", session_id: "main", title: "Persona", body: "The report is ready for you to sign" });
    expect(posted.map((p) => p.tag)).toEqual(["server/leader#needs", expect.stringMatching(/^assistant:/)]);
    expect(posted[1].body).toBe("The report is ready for you to sign");
  });

  it("an Agent's card is its own: the hub's event for it posts nothing more", async () => {
    await open([session("a", "working")]);
    await show([session("a", "needs_you")], [{ id: "q1", machine_id: "m", session_id: "a", kind: "question", questions: [{ question: "Which one?" } as never] }]);
    await notify({ kind: "interaction", machine_id: "m", session_id: "a", interaction_id: "q1", title: "a", body: "Which one?" });
    await notify({ kind: "done", machine_id: "m", session_id: "a", title: "a", body: "done" });
    expect(posted.map((p) => p.tag)).toEqual(["m/a#needs"]);
  });

  it("what the hub sends right after the hello (kept while no page asked) is not announced", async () => {
    await show([], [askCard]);
    await act(async () => {});
    await notify(askNotify);
    expect(posted).toEqual([]);
  });

  it("nothing while the page is in front, unless Always show is on", async () => {
    await open();
    focused = true;
    await notify({ kind: "assistant", machine_id: "server", session_id: "main", title: "Persona", body: "one" });
    expect(posted).toEqual([]);
    localStorage.setItem("persona.systemNotifyAlways", "1");
    await notify({ kind: "assistant", machine_id: "server", session_id: "main", title: "Persona", body: "two" });
    expect(posted.map((p) => p.body)).toEqual(["two"]);
  });

  it("a manager this browser turned off says nothing here either", async () => {
    assistantRows = [{ ...session("work-chat", "idle"), machine_id: "server", assistant: true, manager: "work" }];
    hubPrefs = { muted_managers: ["work"] };
    await open();
    await notify({ kind: "assistant", machine_id: "server", session_id: "work-chat", title: "Work", body: "muted" });
    expect(posted).toEqual([]);
  });
});

describe("push grading open-page signals (#11771)", () => {
  it("suppresses P1 cards, finished (P2) turns, sound, and P1/P2 notify events when grading is on while P0 still interrupts", async () => {
    let chimeOscillators = 0;
    class FakeAudioContext {
      state = "running";
      currentTime = 0;
      destination = {};
      resume = async () => {};
      createGain = () => ({ gain: { setValueAtTime: () => {}, exponentialRampToValueAtTime: () => {} }, connect: () => this.destination });
      createOscillator = () => {
        chimeOscillators++;
        return { type: "sine", frequency: { value: 0 }, connect: (g: unknown) => g, start: () => {}, stop: () => {} };
      };
    }
    localStorage.setItem("persona.attentionSound", "1");
    vi.stubGlobal("AudioContext", FakeAudioContext);

    pushGrading = true;
    const setBadge = vi.fn();
    Object.defineProperty(window, "personaDesktop", { value: { setBadge, notify: () => false, closeNotification: () => {}, clearNotifications: () => {}, notificationPermission: () => "granted" }, configurable: true });
    const link = document.createElement("link");
    link.setAttribute("rel", "icon");
    link.setAttribute("href", "/icon.png");
    document.head.append(link);

    badgeCount = 0;
    await show([session("p1", "working"), session("p0", "working"), session("fin", "working", "2026-01-01T09:57:00Z")], []);
    await act(async () => {});
    // Trigger a user gesture so armAudio activates AudioContext.
    window.dispatchEvent(new Event("pointerdown"));

    // P1 escalated card + finished (P2) turn: silent (no notification, no sound, badge stays 0).
    await show(
      [session("p1", "needs_you"), session("p0", "working"), session("fin", "unread")],
      [{ id: "ix_p1", machine_id: "m", session_id: "p1", kind: "permission", summary: "run tests", push_level: "P1" }],
    );
    await act(async () => void vi.advanceTimersByTime(SETTLE_MS + BATCH_QUIET_MS + 10));
    expect(posted).toEqual([]);
    expect(chimeOscillators).toBe(0);
    expect(document.title).toBe("Persona");
    expect(setBadge).toHaveBeenLastCalledWith(0);

    // Defensive WS notify check: held P1/P2 notify does not pop or chime when grading is on; P0 still does.
    await act(async () => {
      hubListeners.get("notify")?.forEach((l) => l({ kind: "assistant", push_level: "P1", title: "Held", body: "briefing" }));
      hubListeners.get("notify")?.forEach((l) => l({ kind: "alert", push_level: "P2", title: "Silent", body: "bg" }));
    });
    expect(posted).toEqual([]);
    expect(chimeOscillators).toBe(0);

    // P0 needs_you + badgeCount=1: immediate popup, chime, tab title, and Mac Dock badge.
    badgeCount = 1;
    await show(
      [session("p1", "needs_you"), session("p0", "needs_you"), session("fin", "unread")],
      [
        { id: "ix_p1", machine_id: "m", session_id: "p1", kind: "permission", summary: "run tests", push_level: "P1" },
        { id: "ix_p0", machine_id: "m", session_id: "p0", kind: "question", summary: "Approve deploy?", push_level: "P0" },
      ],
    );
    expect(posted.map((p) => p.tag)).toEqual(["m/p0#needs"]);
    expect(chimeOscillators).toBe(2); // needs chime plays two notes
    expect(document.title).toBe("(1) Persona");
    expect(setBadge).toHaveBeenLastCalledWith(1);

    // New P1 card added to already-waiting p0 does not re-notify or chime; new P0 alert does.
    chimeOscillators = 0;
    vi.advanceTimersByTime(4000);
    await show(
      [session("p1", "needs_you"), session("p0", "needs_you"), session("fin", "unread")],
      [
        { id: "ix_p1", machine_id: "m", session_id: "p1", kind: "permission", summary: "run tests", push_level: "P1" },
        { id: "ix_p0", machine_id: "m", session_id: "p0", kind: "question", summary: "Approve deploy?", push_level: "P0" },
        { id: "ix_p0_held", machine_id: "m", session_id: "p0", kind: "permission", summary: "extra p1", push_level: "P1" },
      ],
    );
    expect(posted).toHaveLength(1);
    expect(chimeOscillators).toBe(0);

    // Turning grading OFF restores finished notifications and P1 card alerts.
    pushGrading = false;
    posted.length = 0;
    await show([session("done2", "working", "2026-01-01T09:57:00Z")], []);
    await show([session("done2", "unread")], []);
    await act(async () => void vi.advanceTimersByTime(SETTLE_MS + BATCH_QUIET_MS + 10));
    expect(posted.map((p) => p.tag)).toEqual(["m/done2#finished"]);
    link.remove();
    delete (window as { personaDesktop?: unknown }).personaDesktop;
  });
});
