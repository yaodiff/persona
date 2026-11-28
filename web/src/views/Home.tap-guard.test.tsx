// @vitest-environment jsdom
// The mis-tap guard on the overview's one-tap answers (#8161): rows that are part of opening Overview answer at once;
// a row that appears after the open-grace swallows an early pointer tap and answers once armed; keyboard clicks never
// wait; replacing the card with one whose answers differ re-arms, while redrawing the same row does not.
import { act } from "react";
import serializedDialog from "../lib/testdata/terminal-approval.json";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../lib/ops", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../lib/ops")>()),
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
    return {};
  }),
}));

const fake = vi.hoisted(() => ({
  sessions: [] as import("../lib/types").Session[],
  interactions: [] as import("../lib/types").Interaction[],
}));
vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) =>
      select({ ...real.getState(), loaded: true, machines: [{ id: "mac", name: "Mac", online: true }], interactions: fake.interactions, todos: [], answered: {} }),
  };
});
vi.mock("../lib/current-assistant", () => ({
  useScopedMachines: () => [{ id: "mac", name: "Mac", online: true }],
  useScopedInteractions: () => fake.interactions,
  useScopedAgentSessions: () => fake.sessions,
  useScopedCounts: () => ({ needs: 0, working: 0, waiting: 0, unread: 0, interactions: fake.interactions.length }),
  useScopedAgentListStatus: () => ({ status: "loaded", whole: true }),
  useScopedTodos: () => [],
}));
vi.mock("./extensions/ExtensionSlots", () => ({ OverviewCards: () => null, OverviewArrangeButton: () => null, OverviewArranging: () => null }));

const { Home, resetOverviewTapGuardForTest } = await import("./Home");
const { getState } = await import("../lib/store");
const { setTapGuardClockForTest, TAP_GUARD_ATTR, TAP_GUARD_MS } = await import("../lib/tap-guard");
import type { Interaction, Session } from "../lib/types";

const session = { machine_id: "mac", session_id: "s1", title: "agent 1", cwd: "/code", status: "idle", state: "needs_you" } as Session;
const permission = { id: "p1", kind: "permission", machine_id: "mac", session_id: "s1", tool_name: "Bash", tool_input: { command: "git status" } } as Interaction;
const question = { id: "q1", kind: "question", machine_id: "mac", session_id: "s1", questions: [{ question: "which?", options: [{ label: "JPEG" }, { label: "PNG" }] }] } as Interaction;

let now = 0;
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const mount = async () => {
  await act(async () => root.render(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />));
};
const findButton = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(label))!;
/** A pointer or touch click (detail 1). */
const tap = (el: Element) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 })));

beforeEach(() => {
  now = 1000;
  setTapGuardClockForTest(() => now);
  resetOverviewTapGuardForTest();
  calls.length = 0;
  fake.sessions = [session];
  fake.interactions = [];
  // answerCard's permission check reads the real store through getSession, not the mocked useStore
  Object.assign(getState(), { sessions: { mac: [session] }, interactions: [], answered: {} });
  history.replaceState(null, "", "/overview");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setTapGuardClockForTest(null);
});

describe("the overview's quick-answer rows", () => {
  it("answers at once when the row is part of opening the overview", async () => {
    fake.interactions = [permission];
    await mount();
    const allow = findButton("允许");
    expect(allow.hasAttribute(TAP_GUARD_ATTR)).toBe(false);
    await tap(allow);
    await flush();
    expect(calls).toEqual([{ path: "/interactions/p1/resolve", body: { allow: true } }]);
  });

  it("offers review and sends one refusal for a serialized terminal request with no command field", async () => {
    fake.interactions = [{ ...permission, kind: "screen", dialog: serializedDialog }];
    await mount();
    expect(findButton("Yes")).toBeUndefined();
    expect(findButton("查看请求")).toBeTruthy();
    expect(calls).toEqual([]);
    await tap(findButton(serializedDialog.options[2].label));
    await flush();
    expect(calls).toEqual([{ path: "/interactions/p1/resolve", body: { option: "3" } }]);
  });

  it("swallows an early pointer tap on a row that appears later, then answers once armed", async () => {
    await mount(); // overview opens empty
    now += 1100; // past the open-grace
    fake.interactions = [permission];
    await mount();
    const allow = findButton("允许");
    expect(allow.hasAttribute(TAP_GUARD_ATTR)).toBe(true);
    await tap(allow);
    expect(calls).toEqual([]);
    now += TAP_GUARD_MS;
    await tap(allow);
    await flush();
    expect(calls).toEqual([{ path: "/interactions/p1/resolve", body: { allow: true } }]);
  });

  it("never blocks a keyboard click while the guard runs", async () => {
    await mount();
    now += 1100;
    fake.interactions = [permission];
    await mount();
    await act(async () => {
      findButton("允许").click(); // element.click() carries detail 0, as an Enter on a focused button does
    });
    await flush();
    expect(calls).toHaveLength(1);
  });

  it("re-arms when the answers change but not when the same row redraws", async () => {
    await mount();
    now += 1100;
    fake.interactions = [permission];
    await mount();
    now += 300;
    await mount(); // an unrelated re-render with the same row keeps the running window
    now += 200; // 500 ms after it appeared: armed
    await tap(findButton("允许"));
    await flush();
    expect(calls).toHaveLength(1);

    now += 100;
    fake.interactions = [question]; // a different card and option set: a fresh window
    await mount();
    const jpeg = findButton("JPEG");
    expect(jpeg.hasAttribute(TAP_GUARD_ATTR)).toBe(true);
    await tap(jpeg);
    expect(calls).toHaveLength(1);
    now += TAP_GUARD_MS;
    await tap(jpeg);
    await flush();
    expect(calls[1]).toEqual({ path: "/interactions/q1/resolve", body: { answers: { "which?": "JPEG" } } });
  });
});
