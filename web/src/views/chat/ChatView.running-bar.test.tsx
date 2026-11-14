// @vitest-environment jsdom
// While the main Persona conversation runs a foreground turn, its state is said in exactly one place for the whole turn:
// the message stream (sending/preparing feedback, then the live work line). The dock's running status bar never repeats
// it, including across transcript loads and state pushes; it returns only for work that outlives the turn (background
// workflow / wait / subagent). Agent chats keep the full dock bar (#6230, rework after live verification).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";

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
// Exactly one in-stream activity indicator: the live work line once the hub accepts the turn, or the sending/preparing
// feedback while the message is still on its way.
const inStreamIndicator = () => liveWorkLine() ?? sendFeedback();

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

    // a transcript reload while the turn runs must not let the dock bar leak in (the previous gate used the live line's
    // mount, which drops during loading)
    act(() => { transcript.loading = true; });
    await flush();
    expect(dockBar()).toBeNull();
    expect(inStreamIndicator()).not.toBeNull();
    act(() => { transcript.loading = false; });
    await flush();
    expect(dockBar()).toBeNull();
    expect(liveWorkLine()).not.toBeNull();

    // streaming the answer: still one place, never the dock bar
    act(() => { transcript.streaming = "The answer so far"; });
    await flush();
    expect(dockBar()).toBeNull();
    expect(inStreamIndicator()).not.toBeNull();
    act(() => { transcript.streaming = ""; });
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
  });

  it("still draws the dock bar throughout in an Agent chat", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "working", state: "working", state_since: "2026-11-13T01:00:01Z" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(dockBar()).not.toBeNull();
    act(() => { transcript.loading = true; });
    await flush();
    expect(dockBar()).not.toBeNull();
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

    // the same transcript reload that let the bar leak in before the phase-based gate must not bring it back here either
    act(() => { transcript.loading = true; });
    await flush();
    expect(dockBar()).toBeNull();
    expect(inStreamIndicator()).not.toBeNull();
    act(() => { transcript.loading = false; });
    await flush();
    expect(dockBar()).toBeNull();
    expect(liveWorkLine()).not.toBeNull();

    // streaming the answer: still one place, never the dock bar
    act(() => { transcript.streaming = "The answer so far"; });
    await flush();
    expect(dockBar()).toBeNull();
    expect(inStreamIndicator()).not.toBeNull();
    act(() => { transcript.streaming = ""; });
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
