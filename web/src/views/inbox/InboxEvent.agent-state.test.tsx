// @vitest-environment jsdom
// D3 "Agent 行的实时状态" (spec/fixtures/inbox/timeline.json `live_state`): only the newest Agent / Agent-stopped step
// linking a session shows that session's live state, in the Agents list's words; an older step for the same session,
// a processing-conversation step, and a session the app does not have show nothing. Written from the rule, not from the
// implementation: the newest step is decided on the whole chain, so folding a chain never moves the state to another row.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: () => Promise.resolve({}) }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined, translatesChat: () => false, translatesMail: () => false, languageName: () => "" }));

import type { InboxItem } from "../../lib/inbox";
import { resetInboxForTests } from "../../lib/inbox";
import type { ChainEntry } from "../../lib/inbox-events";
import { resetMailForTests } from "../../lib/mail";
import { settleRosterDefault } from "../../lib/manager-choice";
import { getState } from "../../lib/store";
import { stateName } from "../../lib/state-labels";
import type { Session } from "../../lib/types";
import { EventBody } from "./InboxEvent";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const session = (sessionId: string, state: string): Session => ({ machine_id: "m1", session_id: sessionId, title: sessionId, cwd: "/work", status: "idle", state } as Session);
const link = (machine: string, sessionId: string) => ({ type: "session", machine_id: machine, session_id: sessionId });
const step = (id: string, at: string, kind: string, via: string, sid?: string): ChainEntry =>
  ({ id, at, kind, via, text: `${id} ${kind}`, link: sid ? link("m1", sid) : { type: "source" } }) as ChainEntry;

const item = (chain: ChainEntry[]): InboxItem => ({
  id: "agent-states", source: "chat", kind: "group", account: "chat", account_name: "Team chat", title: "A matter",
  preview: "Latest line", count: 1, started_at: "2026-01-01T11:00:00Z", updated_at: "2026-01-01T11:30:00Z", chain,
});

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  resetInboxForTests();
  resetMailForTests();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const stateOf = (id: string) => host.querySelector(`[data-chain-id="${id}"] [data-chain-agent-state]`)?.textContent ?? null;
const press = async (text: string) => {
  const b = [...host.querySelectorAll("button")].find((x) => x.textContent?.trim() === text);
  if (!b) throw new Error(`no button ${text}`);
  await act(async () => b.click());
};
const sessions = (rows: Session[]) => Object.assign(getState(), { sessions: { m1: rows } });

describe("链条里 Agent 的实时状态", () => {
  it("shows it on the newest step for the session only, in the Agents list's words", async () => {
    sessions([session("s-a", "unread")]);
    act(() => root.render(<EventBody item={item([
      step("c1", "2026-01-01T11:01:00Z", "agent", "agent", "s-a"),
      step("c2", "2026-01-01T11:02:00Z", "agent_stopped", "agent", "s-a"),
    ])} onSource={() => {}} />));
    expect(stateOf("c2")).toBe(stateName("unread"));
    expect(stateOf("c1")).toBeNull();
  });

  it("decides the newest on the whole chain, so folding does not move the state to an older visible row", async () => {
    sessions([session("s-a", "working")]);
    const chain = [
      step("c1", "2026-01-01T11:01:00Z", "agent", "agent", "s-a"),
      step("c2", "2026-01-01T11:02:00Z", "agent_stopped", "agent", "s-a"),
      step("c3", "2026-01-01T11:03:00Z", "reported", "assistant"),
      step("c4", "2026-01-01T11:04:00Z", "waiting", "assistant"),
      step("c5", "2026-01-01T11:05:00Z", "received", "chat"),
    ];
    act(() => root.render(<EventBody item={item(chain)} onSource={() => {}} />));
    // folded: c1 and c2 are off screen, and the visible rows carry no state
    expect([...host.querySelectorAll("[data-chain-id]")].map((e) => e.getAttribute("data-chain-id"))).toEqual(["c3", "c4", "c5"]);
    for (const id of ["c3", "c4", "c5"]) expect(stateOf(id), id).toBeNull();
    await press("展开全部（5）");
    expect(stateOf("c2")).toBe(stateName("working"));
    for (const id of ["c1", "c3", "c4", "c5"]) expect(stateOf(id), id).toBeNull();
  });

  it("gives each session its own newest step", () => {
    sessions([session("s-a", "needs_you"), session("s-b", "offline")]);
    act(() => root.render(<EventBody item={item([
      step("c1", "2026-01-01T11:01:00Z", "agent", "agent", "s-a"),
      step("c2", "2026-01-01T11:02:00Z", "agent", "agent", "s-b"),
      step("c3", "2026-01-01T11:03:00Z", "agent_stopped", "agent", "s-a"),
      step("c4", "2026-01-01T11:04:00Z", "agent_stopped", "agent", "s-b"),
    ])} onSource={() => {}} />));
    expect(stateOf("c3")).toBe(stateName("needs_you"));
    expect(stateOf("c4")).toBe(stateName("offline"));
    expect(stateOf("c1")).toBeNull();
    expect(stateOf("c2")).toBeNull();
  });

  it("shows nothing for a session the app does not have, even on its newest step", () => {
    sessions([session("s-other", "working")]);
    act(() => root.render(<EventBody item={item([
      step("c1", "2026-01-01T11:01:00Z", "agent", "agent", "s-gone"),
      step("c2", "2026-01-01T11:02:00Z", "agent_stopped", "agent", "s-gone"),
    ])} onSource={() => {}} />));
    expect(stateOf("c1")).toBeNull();
    expect(stateOf("c2")).toBeNull();
  });

  it("keeps the processing-conversation step out of it, even when it links the same session", () => {
    sessions([session("s-a", "working")]);
    act(() => root.render(<EventBody item={item([
      step("c1", "2026-01-01T11:01:00Z", "agent", "agent", "s-a"),
      step("c2", "2026-01-01T11:02:00Z", "handling", "assistant", "s-a"),
    ])} onSource={() => {}} />));
    // the handling step is not an Agent step: the newest Agent step keeps the state
    expect(stateOf("c1")).toBe(stateName("working"));
    expect(stateOf("c2")).toBeNull();
  });
});
