// @vitest-environment jsdom
// When a session reports unknown state (for example Antigravity summary_behind while the summary DB
// lags the transcript), ChatView must not falsely mark the latest unanswered turn as "No response
// returned" or offer Retry. Once the session settles back to idle with genuinely no answer text, the
// empty-response fallback and Retry button must appear as usual.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";
import { FakeSocket, SyncHub, type SyncCase } from "./sync/fake-hub";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = (q: string) => ({ matches: /pointer: *fine|hover: *hover/.test(q), addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("../assistant/AssistantChat", () => ({
  AssistantFace: () => null,
  CompactionStrip: () => null,
  useAssistantMenu: () => ({ items: [], dialogs: null }),
}));
vi.mock("../assistant/Readiness", () => ({
  useReadinessGate: () => ({ readiness: null, step: null, blocked: undefined, dialog: null, help() {}, confirm() {} }),
  ReadinessCard: () => null,
  ChatOnlyNote: () => null,
  FailureCard: () => null,
}));
vi.mock("../assistant/Tasks", () => ({
  DayLine: () => null,
  FeedLineView: () => null,
  QuickResultCard: () => null,
  RouteChip: () => null,
  AssistantActionBar: () => null,
  TaskDock: () => null,
  TaskQuestions: () => null,
}));

const store = await import("../../lib/store");
const { setToken } = await import("../../lib/api");
const { t } = await import("../../i18n");
const { ChatView } = await import("./ChatView");
const { resetStalePullsForTests } = await import("./useTranscript");
const { loadPages } = await import("../../lib/lazy-page");
await loadPages();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const MID = "m1";
const SID = "s1";
const question: Item = { id: "q#0", seq: 10, role: "user", kind: "text", text: "Check the transcript", ts: "2026-01-02T03:00:00Z" };
const toolCall: Item = { id: "t#0", seq: 11, role: "assistant", kind: "tool_use", tool_use_id: "toolu_1", tool_name: "read_file", ts: "2026-01-02T03:00:01Z" };
const answer: Item = { id: "a#0", seq: 12, role: "assistant", kind: "text", text: "Transcript looks good.", ts: "2026-01-02T03:00:03Z" };

function makeCase(session: Partial<Session>): SyncCase {
  return {
    name: "unknown_state_empty",
    description: "",
    chat: { machine_id: MID, session_id: SID, assistant: false },
    devices: ["a"],
    start: {
      hub: {
        history: "h1",
        lines: [question, toolCall],
        to_seq: toolCall.seq,
        interactions: [],
        session: { state: "unknown", status: "unknown", ...session },
        event_stream: "e1",
        event_seq: 0,
      },
    },
    steps: [],
    end: {},
  };
}

let host: HTMLDivElement;
let root: Root;
let hub: SyncHub;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date", "requestAnimationFrame", "cancelAnimationFrame"] });
  vi.setSystemTime(new Date("2026-01-02T03:05:00Z"));
  vi.stubGlobal("WebSocket", FakeSocket);
  localStorage.clear();
  setToken("tok");
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  store.disconnect();
  store.reset();
  resetStalePullsForTests();
  FakeSocket.hub = null;
  vi.unstubAllGlobals();
  vi.useRealTimers();
  localStorage.clear();
});

async function settle(ms = 0) {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(i === 0 ? ms : 17);
    });
  }
}

async function mount(session: Partial<Session>) {
  hub = new SyncHub(makeCase(session), "a");
  FakeSocket.hub = hub;
  vi.stubGlobal("fetch", hub.fetch);
  store.connect();
  await settle();
  root = createRoot(host);
  await act(async () => root.render(<ChatView mid={MID} sid={SID} />));
  await settle(100);
  await act(async () => { await vi.dynamicImportSettled(); });
}

const noReply = () => host.textContent?.includes(t("chat-empty-response.no_response")) ?? false;
const retryButton = () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("chat-lines.retry"));
const stopButton = () => [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === t("chat-composer.stop") || b.textContent === t("chat-composer.stop"));

describe("ChatView empty-response guard while session state is unknown", () => {
  it("does not show No response returned or Retry when session view reports host_unknown", async () => {
    await mount({
      state: "unknown",
      status: "unknown",
      view: { phase: "idle", reason: { kind: "host_unknown" }, rank: 41 },
    });

    expect(noReply()).toBe(false);
    expect(retryButton()).toBeUndefined();
    expect(stopButton()).toBeUndefined();
  });

  it("renders the reply cleanly without flashing No response returned when unknown state settles with an answer", async () => {
    await mount({
      state: "unknown",
      status: "unknown",
      view: { phase: "idle", reason: { kind: "host_unknown" }, rank: 41 },
    });
    expect(noReply()).toBe(false);

    const current = store.getSession(MID, SID)!;
    await act(async () => {
      hub.push({ type: "items", machine_id: MID, session_id: SID, history: "h1", items: [answer], to_seq: answer.seq });
      hub.push({ type: "sessions", machine_id: MID, sessions: [{ ...current, state: "idle", status: "done", view: { phase: "done", reason: { kind: "seen" }, rank: 40 } }] });
    });
    await settle(100);
    await act(async () => { await vi.dynamicImportSettled(); });

    expect(host.textContent).toContain("Transcript looks good.");
    expect(noReply()).toBe(false);
    expect(retryButton()).toBeUndefined();
  });

  it("shows No response returned and Retry once unknown state settles to idle with genuinely no reply", async () => {
    await mount({
      state: "unknown",
      status: "unknown",
      view: { phase: "idle", reason: { kind: "host_unknown" }, rank: 41 },
    });
    expect(noReply()).toBe(false);
    expect(retryButton()).toBeUndefined();

    const current = store.getSession(MID, SID)!;
    await act(async () => {
      hub.push({ type: "sessions", machine_id: MID, sessions: [{ ...current, state: "idle", status: "idle", view: { phase: "idle", reason: { kind: "seen" }, rank: 40 } }] });
    });
    await settle(100);
    await act(async () => { await vi.dynamicImportSettled(); });

    expect(noReply()).toBe(true);
    expect(retryButton()).toBeDefined();
  });
});
