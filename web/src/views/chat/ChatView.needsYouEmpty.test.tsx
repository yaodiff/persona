// @vitest-environment jsdom
// When a session pauses in needs_you (or carries an open pending interaction card for this session)
// before producing final answer text, the latest user turn is still awaiting the user — it must not
// show the "No response returned" fallback or a Retry button. Once the session settles back to idle
// with no pending interactions and genuinely no answer text, the empty-response fallback and Retry
// button must appear as usual.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Interaction, Item, Session } from "../../lib/types";
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
const question: Item = { id: "q#0", seq: 10, role: "user", kind: "text", text: "Update the workspace setting", ts: "2026-01-02T03:00:00Z" };
const toolCall: Item = { id: "t#0", seq: 11, role: "assistant", kind: "tool_use", tool_use_id: "toolu_1", tool_name: "persona_settings", ts: "2026-01-02T03:00:03Z" };

const approvalCard: Interaction = {
  id: "ix_approval_1",
  machine_id: MID,
  session_id: SID,
  kind: "permission",
  tool_name: "persona_settings",
  tool_input: { key: "theme", value: "dark" },
  created_at: "2026-01-02T03:00:04Z",
};

const questionCard: Interaction = {
  id: "ix_question_1",
  machine_id: MID,
  session_id: SID,
  kind: "question",
  questions: [{ question: "Proceed with this change?", header: "Confirm", options: [{ label: "Yes" }, { label: "No" }] }],
  created_at: "2026-01-02T03:00:04Z",
};

function makeCase(session: Partial<Session>, interactions: Interaction[] = [], assistant = true): SyncCase {
  return {
    name: "needs_you_empty",
    description: "",
    chat: { machine_id: MID, session_id: SID, assistant },
    devices: ["a"],
    start: {
      hub: {
        history: "h1",
        lines: [question, toolCall],
        to_seq: toolCall.seq,
        interactions,
        session: { state: "needs_you", status: "blocked", ...session },
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

async function mount(session: Partial<Session>, interactions: Interaction[] = [], assistant = true) {
  hub = new SyncHub(makeCase(session, interactions, assistant), "a");
  FakeSocket.hub = hub;
  vi.stubGlobal("fetch", hub.fetch);
  store.connect();
  await settle();
  root = createRoot(host);
  await act(async () => root.render(<ChatView mid={MID} sid={SID} assistant={assistant} />));
  await settle(100);
  await act(async () => { await vi.dynamicImportSettled(); });
}

const noReply = () => host.textContent?.includes(t("chat-empty-response.no_response")) ?? false;
const retryButton = () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("chat-lines.retry"));
const stopButton = () => [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === t("chat-composer.stop") || b.textContent === t("chat-composer.stop"));

describe("ChatView empty-response guard while waiting for user input", () => {
  it("does not show No response returned or Retry while state is needs_you in an assistant chat, and keeps composer/stop semantics intact", async () => {
    await mount({ state: "needs_you", status: "blocked", pending_interactions: 1 }, [approvalCard], true);

    expect(noReply()).toBe(false);
    expect(retryButton()).toBeUndefined();
    // Composer stays in needsYou queue mode without mis-showing a Stop button.
    expect(stopButton()).toBeUndefined();
    const input = host.querySelector("[data-composer-input]");
    expect(input).not.toBeNull();
    expect(input?.getAttribute("placeholder") ?? input?.getAttribute("aria-placeholder") ?? "").toContain(t("chat-chatview.queue_placeholder"));
  });

  it("keeps approval and question cards visible without showing No response returned when pending_interactions is open", async () => {
    await mount({ state: "idle", status: "idle", pending_interactions: 1 }, [questionCard], false);

    expect(noReply()).toBe(false);
    expect(retryButton()).toBeUndefined();
    expect(stopButton()).toBeUndefined();
    expect(host.textContent).toContain("Proceed with this change?");
  });

  it("shows No response returned and Retry once the session returns to idle with no pending interactions and no reply", async () => {
    await mount({ state: "needs_you", status: "blocked", pending_interactions: 1 }, [approvalCard], true);
    expect(noReply()).toBe(false);
    expect(retryButton()).toBeUndefined();

    // Clear the pending interaction and transition the session to idle via hub push.
    const current = store.getSession(MID, SID)!;
    await act(async () => {
      hub.push({ type: "interaction_resolved", id: approvalCard.id });
      hub.push({ type: "sessions", machine_id: MID, sessions: [{ ...current, state: "idle", status: "idle", pending_interactions: 0 }] });
    });
    await settle(100);
    await act(async () => { await vi.dynamicImportSettled(); });

    expect(noReply()).toBe(true);
    expect(retryButton()).toBeDefined();
  });
});
