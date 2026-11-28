// @vitest-environment jsdom
// A reload shows the main chat's cached page at once, before the hub's newest page comes (useTranscript). That page can
// be older than the turn's reply: a hub that holds the read (a node connection stalled for 15 s at every main-chat turn
// end, #16214) kept it on screen through several reloads. Until the newest page lands, its unanswered last turn is not
// "No response returned", and Retry is not offered: pressing it sent the question again although the reply existed
// (#16286).
// The real page against a fake hub (sync/fake-hub.ts), as the sync scenarios run it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item } from "../../lib/types";
import { FakeSocket, SyncHub, type SyncCase } from "./sync/fake-hub";
import { project } from "./sync/project";

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
const { clearFeedCache, feedCacheScope, writeFeedCache } = await import("../../lib/feed-cache");
const { t } = await import("../../i18n");
const { ChatView } = await import("./ChatView");
const { resetStalePullsForTests } = await import("./useTranscript");
const { loadPages } = await import("../../lib/lazy-page");
await loadPages();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const MID = "m1";
const SID = "s1";
const question: Item = { id: "q#0", seq: 10, role: "user", kind: "text", text: "Which cloud credits do I get?", ts: "2026-01-02T03:00:00Z" };
const search: Item = { id: "t#0", seq: 11, role: "assistant", kind: "tool_use", tool_use_id: "toolu_1", tool_name: "WebSearch", ts: "2026-01-02T03:00:05Z" };
const found: Item = { id: "r#0", seq: 12, role: "user", kind: "tool_result", tool_use_id: "toolu_1", text: "Three results", ts: "2026-01-02T03:00:20Z" };
const reply: Item = { id: "a#0", seq: 13, role: "assistant", kind: "text", text: "You get a monthly cloud credit.", ts: "2026-01-02T03:00:52Z" };

const chat = (lines: Item[]): SyncCase => ({
  name: "cached_turn", description: "", chat: { machine_id: MID, session_id: SID, assistant: true }, devices: ["a"],
  start: { hub: { history: "h1", lines, to_seq: lines.at(-1)!.seq, interactions: [], session: { state: "idle" }, event_stream: "e1", event_seq: 0 } },
  steps: [], end: {},
});

let host: HTMLDivElement;
let root: Root;
let hub: SyncHub;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date", "requestAnimationFrame", "cancelAnimationFrame"] });
  vi.setSystemTime(new Date("2026-01-02T03:05:00Z"));
  vi.stubGlobal("WebSocket", FakeSocket);
  localStorage.clear();
  setToken("tok");
  clearFeedCache();
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

/** Opens the main chat on a reload: the cached page holds the question and its work, not the reply. */
async function reload(lines: Item[]) {
  hub = new SyncHub(chat(lines), "a");
  FakeSocket.hub = hub;
  vi.stubGlobal("fetch", hub.fetch);
  store.connect();
  await settle();
  const scope = feedCacheScope()!;
  expect(writeFeedCache(scope, { machineID: MID, sessionID: SID, items: [question, search, found], hasMore: false, history: "h1" })).toBe(true);
  hub.hold("/items"); // the hub answers the newest page only at the release
  root = createRoot(host);
  await act(async () => root.render(<ChatView mid={MID} sid={SID} assistant mainCache={{ scope }} />));
  await settle(100);
}

const noReply = () => host.textContent?.includes(t("chat-empty-response.no_response")) ?? false;
const retry = () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("chat-lines.retry"));
const shown = () => project(host, []).messages;

describe("a cached page before the hub's newest page", () => {
  it("does not say its last turn has no reply, nor offer Retry, while the newest page is on its way", async () => {
    await reload([question, search, found, reply]);
    expect(shown()).toEqual([{ from: "user", text: question.text, state: "shown" }]);
    expect(noReply()).toBe(false);
    expect(retry()).toBeUndefined();

    // the stall ends: the page brings the reply the cache did not have
    await settle(15_000);
    await act(async () => hub.release("/items"));
    await settle();
    expect(shown()).toEqual([{ from: "user", text: question.text, state: "shown" }, { from: "agent", text: reply.text }]);
    expect(noReply()).toBe(false);
    expect(hub.seen.filter((s) => s.post === "messages")).toEqual([]);
  });

  it("still offers Retry once the newest page shows the turn really ended without a reply", async () => {
    await reload([question, search, found]);
    expect(noReply()).toBe(false);
    await act(async () => hub.release("/items"));
    await settle();
    await act(async () => { await vi.dynamicImportSettled(); }); // the note is a lazy chunk
    expect(noReply()).toBe(true);
    expect(retry()).toBeDefined();
  });
});
