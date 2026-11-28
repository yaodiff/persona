// @vitest-environment jsdom
// The ultracode switch against the store's frame-batched pushes (lib/store.ts, #5905): the node's "queued" push can be
// in the state, but not yet on screen, when the switch's own request comes back. The switch must still say its own
// change waits ("Applies next turn") and stand where it was put, not take the queue for another device's (#3428).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { connect, reset, useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import { resetUltracodeAsks } from "../../lib/ultracode";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

const { UltracodeSwitchRow } = await import("./Ultracode");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  send() {}
  close() {
    this.readyState = 3;
  }
  push(msg: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }
}

// the animation frames: run by hand
let frames: (FrameRequestCallback | null)[] = [];
const runFrame = () =>
  act(() => {
    const due = frames;
    frames = [];
    due.forEach((f) => f?.(0));
  });
const flush = () => act(async () => { for (let i = 0; i < 4; i++) await Promise.resolve(); });

let host: HTMLDivElement;
let root: Root;
let answerModes: (r: Response) => void;

const busy = { machine_id: "mac", session_id: "s1", title: "小组件", cwd: "/code", status: "working", state: "working", agent: "claude", effort: "high", pane_id: "p1", ultracode: true } as Session;

/** The chat's chain in short: the session from the store, handed down to the row as a prop. */
function Chat() {
  const s = useStore((st) => (st.sessions.mac ?? []).find((x) => x.session_id === "s1"));
  return s ? <UltracodeSwitchRow session={s} /> : null;
}

beforeEach(() => {
  reset();
  resetUltracodeAsks();
  FakeSocket.all = [];
  frames = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("requestAnimationFrame", (f: FrameRequestCallback) => frames.push(f));
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    frames[id - 1] = null;
  });
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    if (String(input).endsWith("/modes")) return new Promise<Response>((resolve) => (answerModes = resolve));
    return Promise.resolve(new Response(JSON.stringify({ accounts: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

it("the node's queued push, in the state but not yet drawn when the request returns, reads as this page's change", async () => {
  connect();
  const sock = FakeSocket.all[FakeSocket.all.length - 1];
  sock.readyState = 1;
  sock.onopen?.();
  sock.push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 1, resumed: true, features: ["ultracode"] });
  sock.push({ type: "sessions", machine_id: "mac", sessions: [busy] });
  runFrame();
  act(() => root.render(<Chat />));
  await flush();
  const theSwitch = () => host.querySelector<HTMLButtonElement>('[role="switch"]')!;
  const line = () => host.querySelector("[data-ultracode-line]")!.textContent;
  expect(theSwitch().getAttribute("aria-checked")).toBe("true");

  // turned off on a working agent: the node queues it and pushes that before the request returns
  await act(async () => theSwitch().click());
  act(() => sock.push({ type: "sessions", machine_id: "mac", sessions: [{ ...busy, ultracode_pending: true, rev: 2 }] }));
  answerModes(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
  await flush();
  runFrame();
  await flush();
  expect(line()).toBe("下一轮生效");
  expect(theSwitch().getAttribute("aria-checked")).toBe("false");

  // more pushes while the turn runs: still this page's change
  act(() => sock.push({ type: "sessions", machine_id: "mac", sessions: [{ ...busy, ultracode_pending: true, rev: 3 }] }));
  runFrame();
  await flush();
  expect(line()).toBe("下一轮生效");
  expect(theSwitch().getAttribute("aria-checked")).toBe("false");

  // through: what the node saw
  act(() => sock.push({ type: "sessions", machine_id: "mac", sessions: [{ ...busy, ultracode: false, state: "idle", status: "idle", rev: 4 }] }));
  runFrame();
  await flush();
  expect(line()).toBe("每个任务都将以多 Agent 工作流运行 · 消耗较多额度 · 仅对当前会话生效");
  expect(theSwitch().getAttribute("aria-checked")).toBe("false");
});
