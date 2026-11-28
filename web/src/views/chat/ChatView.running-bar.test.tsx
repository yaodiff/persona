// @vitest-environment jsdom
// While the main Persona conversation runs a foreground turn, its state is said in exactly one place for the whole turn:
// the message stream (sending/preparing feedback, then the live work line). The dock's running status bar never repeats
// it, including across transcript loads and state pushes; it returns only for work that outlives the turn (background
// workflow / subagent / wait / working command). Agent chats keep the full dock bar (#6230, rework after live verification).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";
import { initI18n, t } from "../../i18n";

await initI18n("zh-Hans");

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

const captured = vi.hoisted(() => ({
  session: { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle", state: "idle" } as Session,
}));
const transcript = vi.hoisted(() => ({ items: [] as Item[], loading: false, error: "", hasMore: false, streaming: "" }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/store", async (importOriginal) => {
  const store = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...store,
    getSession: () => captured.session,
    useStore: <T,>(select: (s: ReturnType<typeof store.getState>) => T) =>
      select({ ...store.getState(), sessions: { hub: [captured.session] } }),
  };
});
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../../lib/router", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/router")>(), useRoute: () => ({ name: "session", mid: "hub", id: "c1" }) }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

const { ChatView } = await import("./ChatView");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const dockBar = () => host.querySelector("[data-running-status]");
const liveWorkLine = () => host.querySelector('[data-work-group="live"]');
const sendFeedback = () => host.querySelector("[data-message-feedback]");
// Activity stays in the stream: receipt feedback, the live work line, then the answer itself while it streams.
const inStreamIndicator = () => liveWorkLine() ?? sendFeedback() ?? host.querySelector('[data-answer="streaming"]');

beforeEach(async () => {
  localStorage.clear();
  const { setOutbox } = await import("../../lib/store");
  for (const key of Object.keys((await import("../../lib/store")).getState().outbox)) setOutbox(key, []);
  transcript.items = [{ id: "u1#0", seq: 1, role: "user", kind: "text", text: "What should I do today?", ts: "2026-11-13T01:00:00Z" }];
  transcript.loading = false;
  transcript.streaming = "";
  vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("the Persona conversation says its foreground turn exactly once (#6230)", () => {
  it("hides the dock bar across the whole working turn, keeping one in-stream indicator", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    expect(liveWorkLine()).not.toBeNull();
    expect(dockBar()).toBeNull();

    // Reloading with cached history keeps the live work line in place and leaves the dock hidden.
    act(() => { transcript.loading = true; root.render(<ChatView mid="hub" sid="c1" assistant />); });
    await flush();
    expect(dockBar()).toBeNull();
    expect(liveWorkLine()).not.toBeNull();
    expect(sendFeedback()).toBeNull();
    expect(host.querySelectorAll('[data-work-group="live"], [data-message-feedback]')).toHaveLength(1);

    // The streamed answer remains visible while the reload is still pending.
    act(() => { transcript.streaming = "The answer so far"; root.render(<ChatView mid="hub" sid="c1" assistant />); });
    await flush();
    expect(dockBar()).toBeNull();
    expect(sendFeedback()).toBeNull();
    expect(host.querySelector('[data-answer="streaming"]')?.textContent).toContain("The answer so far");
    act(() => { transcript.loading = false; transcript.streaming = ""; root.render(<ChatView mid="hub" sid="c1" assistant />); });
    await flush();
    expect(dockBar()).toBeNull();
    expect(liveWorkLine()).not.toBeNull();
  });

  it("shows Processing feedback on a brand-new working Persona conversation before its first transcript item arrives", async () => {
    const { noteSent } = await import("../../lib/outbox");
    transcript.items = [];
    noteSent("hub", "c1", { text: "Inspect the current workspace" });
    captured.session = { machine_id: "hub", session_id: "c1", title: "", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant starting />));
    await flush();
    expect(dockBar()).toBeNull();
    expect(sendFeedback()?.getAttribute("data-message-feedback")).toBe("processing");
    expect(inStreamIndicator()).not.toBeNull();
  });

  // The sending / awaiting window (an outbox message on its way, before the hub reports working) is covered end to end in
  // ChatView.failure.test.tsx, which drives the real message-send feedback and asserts the dock bar is null while the
  // in-stream "sending"/"processing" feedback is the one indicator.

  it("brings the dock bar back for background-only work the turn leaves behind", async () => {
    // state stays "working" but only background commands wait, so the turn is drawn finished (backgroundOnly): the stream
    // shows only quiet fold rows and the dock bar is allowed again.
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", background_waiting: true } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    expect(dockBar()).not.toBeNull();
    act(() => { transcript.loading = true; root.render(<ChatView mid="hub" sid="c1" assistant />); });
    await flush();
    expect(dockBar()).not.toBeNull();
    expect(sendFeedback()).toBeNull();
  });

  it("draws an ended turn with a working background command finished and brings the dock bar back", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", background_working: true,
      background: [{ id: "build", kind: "shell", description: "Build release", command: "make release", started_at: "2026-11-13T01:00:02Z" }] } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    expect(liveWorkLine()).toBeNull();
    expect(dockBar()).not.toBeNull();
    expect(dockBar()?.getAttribute("data-running-background")).toBe("command");
  });

  it("still draws the dock bar throughout in an Agent chat", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(dockBar()).not.toBeNull();
    act(() => { transcript.loading = true; root.render(<ChatView mid="hub" sid="c1" />); });
    await flush();
    expect(dockBar()).not.toBeNull();
  });

  it("keeps an Agent's existing wait and subagent bars while the answer streams", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", background_working: true, background_waiting: true,
      background: [{ id: "wait", kind: "shell", description: "Wait for CI", command: "gh pr checks --watch", started_at: "2026-11-13T01:00:02Z" }] } as Session;
    transcript.streaming = "The answer so far";
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(liveWorkLine()).not.toBeNull();
    expect(dockBar()?.getAttribute("data-running-background")).toBe("waiting");

    captured.session = { ...captured.session, background_waiting: false,
      subagents: [{ id: "a", session_id: "c1@a", running: true, background: true }] } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(liveWorkLine()).not.toBeNull();
    expect(dockBar()?.getAttribute("data-running-background")).toBe("subtasks");
  });
});

// A Persona conversation opened from the generic agent session route (/s/<mid>/<sid>) mounts ChatView without the
// `assistant` prop; only session.assistant marks it. The foreground turn still says its state once (#6230, follow-up:
// supervisor and other non-main Persona chats showed the line twice on that route).
describe("a Persona conversation on the generic session route says its foreground turn exactly once (#6230)", () => {
  it("hides the dock bar across the whole working turn without the assistant prop, keeping one in-stream indicator", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Supervisor", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", assistant: true } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(liveWorkLine()).not.toBeNull();
    expect(dockBar()).toBeNull();

    // The generic route still uses the cached work line during a reload.
    act(() => { transcript.loading = true; root.render(<ChatView mid="hub" sid="c1" />); });
    await flush();
    expect(dockBar()).toBeNull();
    expect(liveWorkLine()).not.toBeNull();
    expect(sendFeedback()).toBeNull();
    expect(host.querySelectorAll('[data-work-group="live"], [data-message-feedback]')).toHaveLength(1);

    // An empty transcript while loading renders only the single "Loading chat" loader, never a second "Processing…" spinner.
    const cached = transcript.items;
    act(() => { transcript.items = []; root.render(<ChatView mid="hub" sid="c1" />); });
    await flush();
    expect(dockBar()).toBeNull();
    expect(liveWorkLine()).toBeNull();
    expect(sendFeedback()).toBeNull();
    const statuses = [...host.querySelectorAll('[role="status"]')];
    expect(statuses).toHaveLength(1);
    expect(statuses[0]?.textContent).toContain("正在加载对话");
    expect(host.textContent).not.toContain("处理中…");
    act(() => { transcript.items = cached; transcript.loading = false; root.render(<ChatView mid="hub" sid="c1" />); });
    await flush();
    expect(dockBar()).toBeNull();
    expect(liveWorkLine()).not.toBeNull();

    // streaming the answer: still one place, never the dock bar
    act(() => { transcript.streaming = "The answer so far"; root.render(<ChatView mid="hub" sid="c1" />); });
    await flush();
    expect(dockBar()).toBeNull();
    expect(inStreamIndicator()).not.toBeNull();
    act(() => { transcript.streaming = ""; root.render(<ChatView mid="hub" sid="c1" />); });
  });

  it("brings the dock bar back for background-only work on the generic route", async () => {
    // state stays "working" but only a background wait remains, so the turn is drawn finished (backgroundOnly): the
    // stream shows only quiet fold rows and the dock bar is allowed again, keeping background work announced once.
    captured.session = { machine_id: "hub", session_id: "c1", title: "Supervisor", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", assistant: true, background_waiting: true } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(dockBar()).not.toBeNull();
  });

  it("keeps the dock bar for a plain Agent session on the generic route even when a session object carries no prop", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Agent", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", assistant: false } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(dockBar()).not.toBeNull();
  });
});

describe("Agent chats never render duplicate competing Working… timers (#12380)", () => {
  const workClocks = () => host.querySelectorAll("[data-work-elapsed]");
  const dockClocks = () => host.querySelectorAll("[data-running-elapsed]");

  it("renders only the dock status bar timer before any tool steps arrive after launch handoff", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Agent", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", assistant: false } as Session;
    transcript.items = [{ id: "u1#0", seq: 1, role: "user", kind: "text", text: "Fix the bug", ts: "2026-11-13T01:00:00Z" }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(dockBar()).not.toBeNull();
    expect(dockClocks()).toHaveLength(1);
    expect(liveWorkLine()).toBeNull();
    expect(workClocks()).toHaveLength(0);
  });

  it("keeps the expandable live work summary when tool steps exist without rendering a second elapsed timer", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Agent", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", assistant: false } as Session;
    transcript.items = [
      { id: "u1#0", seq: 1, role: "user", kind: "text", text: "Fix the bug", ts: "2026-11-13T01:00:00Z" },
      { id: "t1#0", seq: 2, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: "call-1", tool_input: { command: "git status" }, ts: "2026-11-13T01:00:01Z" },
      { id: "t1-r#0", seq: 3, role: "user", kind: "tool_result", tool_use_id: "call-1", text: "clean", ts: "2026-11-13T01:00:02Z" },
    ];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(dockBar()).not.toBeNull();
    expect(dockBar()?.querySelector("[data-running-verb]")?.textContent).toBe("正在工作…");
    expect(dockClocks()).toHaveLength(1);
    expect(liveWorkLine()).not.toBeNull();
    expect(liveWorkLine()?.querySelector<HTMLButtonElement>(":scope > button")?.getAttribute("aria-expanded")).toBe("false");
    expect(workClocks()).toHaveLength(0);
  });

  it("keeps the live work line above batched streamed text in an Agent chat with only one elapsed timer", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Agent", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", assistant: false } as Session;
    transcript.items = [{ id: "u1#0", seq: 1, role: "user", kind: "text", text: "Fix the bug", ts: "2026-11-13T01:00:00Z" }];
    transcript.streaming = "Investigating now.";
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(dockBar()).not.toBeNull();
    expect(dockClocks()).toHaveLength(1);
    expect(liveWorkLine()).not.toBeNull();
    expect(workClocks()).toHaveLength(0);
  });

  it("still shows the in-stream elapsed timer in Persona chats where the dock bar is hidden", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Persona", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", assistant: true } as Session;
    transcript.items = [{ id: "u1#0", seq: 1, role: "user", kind: "text", text: "What is next?", ts: "2026-11-13T01:00:00Z" }];
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    expect(dockBar()).toBeNull();
    expect(liveWorkLine()).not.toBeNull();
    expect(workClocks()).toHaveLength(1);
  });

  it("renders a single Loading chat spinner when entering a working Assistant chat whose history is still loading", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Persona", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z", assistant: true } as Session;
    transcript.items = [];
    transcript.loading = true;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    expect(dockBar()).toBeNull();
    expect(sendFeedback()).toBeNull();
    const loaders = [...host.querySelectorAll('[role="status"]')];
    expect(loaders).toHaveLength(1);
    expect(loaders[0]?.textContent).toContain("正在加载对话");
    expect(host.textContent).not.toContain("处理中…");
  });
});

describe("launch handoff pending message state (#16657)", () => {
  it("renders an accepted launch handoff bubble as sent, shows Processing only before working, and settles on client_msg_id", async () => {
    const { getState, putLaunch } = await import("../../lib/store");
    const launch = {
      id: "local-16657",
      machineId: "hub",
      cwd: "/code",
      launcher: "agy",
      text: "Check the broken view",
      attachments: [],
      phase: "ready" as const,
      session: { machine_id: "hub", session_id: "c1", title: "Agent", cwd: "/code", status: "idle" as const, state: "idle" as const, launcher: "agy" },
      outgoing: { text: "Check the broken view", images: [{ mime: "image/png", data: "iVBORw0KGgo=" }] },
      uploaded: {},
      startOpId: "start-16657",
      startPromptOpId: "start-prompt-16657",
      messageId: "msg-launch-16657",
      handoffAt: Date.now(),
    };
    putLaunch(launch);
    try {
      // Before the hub reports working and before the transcript echoes the first prompt:
      // POST /messages has already succeeded, so the handed-off bubble is "sent" (not dimmed "sending")
      // and the inline feedback says "Processing…" rather than "Sending…".
      captured.session = { machine_id: "hub", session_id: "c1", title: "Agent", cwd: "/code", status: "idle", state: "idle", launcher: "agy" } as Session;
      transcript.items = [];
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      const bubble = host.querySelector("[data-user-bubble]");
      expect(bubble?.getAttribute("data-send-state")).toBe("sent");
      expect(sendFeedback()?.getAttribute("data-message-feedback")).toBe("processing");

      // Once the session is already working (even before the transcript echoes the prompt):
      // never show a competing "Sending…" spinner underneath the live work state.
      captured.session = { ...captured.session, status: "working", state: "working", state_since: "2026-11-13T01:00:01Z" } as Session;
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(host.querySelector("[data-user-bubble]")?.getAttribute("data-send-state")).toBe("sent");
      expect(sendFeedback()).toBeNull();

      // Once the transcript item arrives with the matching client_msg_id (even without the attached image in the
      // transcript item), the pending launch bubble settles immediately with no duplicate bubble.
      transcript.items = [{
        id: "u1#0",
        seq: 1,
        role: "user",
        kind: "text",
        text: "Check the broken view",
        client_msg_id: "msg-launch-16657",
        ts: "2026-11-13T01:00:00Z",
      }];
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      const bubbles = host.querySelectorAll("[data-user-bubble]");
      expect(bubbles).toHaveLength(1);
      expect(bubbles[0]?.hasAttribute("data-send-state")).toBe(false);
      expect(sendFeedback()).toBeNull();
      expect(getState().launches[launch.id]?.outgoing).toBeUndefined();
    } finally {
      putLaunch({ ...launch, phase: "failed", outgoing: undefined, handoffAt: undefined });
    }
  });
});

describe("foreground Stop and Esc are hidden when only background work remains", () => {
  const stopBtn = () => host.querySelector<HTMLButtonElement>(`[data-testid="composer-stop"]`);
  const barInterruptBtn = () => dockBar()?.querySelector<HTMLButtonElement>(`button`);
  const composerInput = () => host.querySelector<HTMLTextAreaElement>("textarea");
  const escapeKey = () => act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
  });

  it("hides dock interrupt, composer Stop, queue placeholder, and Esc shortcut during background-only state", async () => {
    const fetchMock = vi.fn(async (_url: string | URL | Request, _init?: RequestInit) => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    captured.session = {
      machine_id: "hub",
      session_id: "c1",
      title: "Agent",
      agent: "agy",
      source_kind: "runtime",
      capabilities: { send: true, interrupt: true },
      cwd: "/code",
      status: "working",
      state: "working",
      state_since: "2026-11-13T01:00:01Z",
      background_working: true,
      background: [{ id: "hub/task-1", kind: "monitor", description: "Timer: 3m", started_at: "2026-11-13T01:00:02Z" }],
    } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();

    expect(dockBar()).not.toBeNull();
    expect(barInterruptBtn()).toBeNull();
    expect(stopBtn()).toBeNull();
    expect(composerInput()?.getAttribute("placeholder")).toBe(t("chat-chatview.agent_placeholder"));

    const callsBefore = fetchMock.mock.calls.length;
    escapeKey();
    await flush();
    expect(fetchMock.mock.calls.slice(callsBefore).some(([url]) => String(url).includes("/interrupt"))).toBe(false);
  });

  it("keeps dock interrupt, composer Stop, queue placeholder, and Esc shortcut during a foreground turn", async () => {
    const fetchMock = vi.fn(async (_url: string | URL | Request, _init?: RequestInit) => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    captured.session = {
      machine_id: "hub",
      session_id: "c1",
      title: "Agent",
      agent: "agy",
      source_kind: "runtime",
      capabilities: { send: true, interrupt: true },
      cwd: "/code",
      status: "working",
      state: "working",
      state_since: "2026-11-13T01:00:01Z",
      background_working: false,
    } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();

    expect(dockBar()).not.toBeNull();
    expect(barInterruptBtn()).not.toBeNull();
    expect(stopBtn()).not.toBeNull();
    expect(composerInput()?.getAttribute("placeholder")).toBe(t("chat-chatview.queue_placeholder"));

    escapeKey();
    await flush();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/interrupt"))).toBe(true);
  });

  it("also hides Stop and Esc for background-only runtime agents/workflows where backgroundTurn returns null", async () => {
    const fetchMock = vi.fn(async (_url: string | URL | Request, _init?: RequestInit) => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    captured.session = {
      machine_id: "hub",
      session_id: "c1",
      title: "Agent",
      agent: "agy",
      source_kind: "runtime",
      capabilities: { send: true, interrupt: true },
      cwd: "/code",
      status: "working",
      state: "working",
      state_since: "2026-11-13T01:00:01Z",
      background_working: true,
      background: [{ id: "hub/task-2", kind: "agent", description: "Subagent", started_at: "2026-11-13T01:00:02Z" }],
    } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();

    expect(barInterruptBtn()).toBeNull();
    expect(stopBtn()).toBeNull();
    expect(composerInput()?.getAttribute("placeholder")).toBe(t("chat-chatview.agent_placeholder"));

    const callsBefore = fetchMock.mock.calls.length;
    escapeKey();
    await flush();
    expect(fetchMock.mock.calls.slice(callsBefore).some(([url]) => String(url).includes("/interrupt"))).toBe(false);
  });

  it("clears stopping state immediately when interrupting transitions into background-only work", async () => {
    const fetchMock = vi.fn(async (_url: string | URL | Request, _init?: RequestInit) => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const fg = {
      machine_id: "hub",
      session_id: "c1",
      title: "Agent",
      agent: "agy",
      source_kind: "runtime",
      capabilities: { send: true, interrupt: true },
      cwd: "/code",
      status: "working",
      state: "working",
      state_since: "2026-11-13T01:00:01Z",
      background_working: false,
    } as Session;
    captured.session = fg;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();

    act(() => { stopBtn()!.click(); });
    await flush();

    // State remains "working" because background work continues; stopping lock must clear right away.
    captured.session = {
      ...fg,
      background_working: true,
      background: [{ id: "hub/task-2", kind: "agent", description: "Subagent", started_at: "2026-11-13T01:00:02Z" }],
    } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(stopBtn()).toBeNull();

    // Starting another foreground turn sees an enabled Stop button rather than staying disabled for STOP_SETTLE_MS.
    captured.session = fg;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(stopBtn()?.disabled).toBe(false);
  });

  it("does not route new sends into QueuePanel while only background work runs", async () => {
    let resolveSend!: (r: Response) => void;
    const fetchMock = vi.fn(() => new Promise<Response>((res) => { resolveSend = res; }));
    vi.stubGlobal("fetch", fetchMock);
    captured.session = {
      machine_id: "hub",
      session_id: "c1",
      title: "Agent",
      agent: "claude",
      capabilities: { send: true, interrupt: true, queue_edit: true },
      cwd: "/code",
      status: "working",
      state: "working",
      state_since: "2026-11-13T01:00:01Z",
      background_working: true,
      background: [{ id: "hub/task-2", kind: "agent", description: "Subagent", started_at: "2026-11-13T01:00:02Z" }],
    } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();

    const box = composerInput()!;
    const setValue = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    act(() => {
      setValue.call(box, "Start next turn");
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => {
      box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    await flush();

    expect(host.querySelector("[data-queue-panel]")).toBeNull();
    resolveSend(new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
    await flush();
  });
});
