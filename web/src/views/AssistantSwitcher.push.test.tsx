// @vitest-environment jsdom
// The switcher's counts follow the hub's `managers` push through the real store and socket (docs/assistant/managers.md
// "The current assistant"): the roster is read again and the count moves, also when the push comes while a read is
// still out (the read after it gives the hub's latest), and each count says what it counts to a screen reader.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { resetManagerChoice } from "../lib/manager-choice";
import { resetRoster } from "../lib/managers";
import { connect, flushPushes, refresh, reset } from "../lib/store";
import { AssistantSwitcher } from "./AssistantSwitcher";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
  static last: FakeSocket;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor() {
    FakeSocket.last = this;
  }
  send() {}
  close() {
    this.readyState = 3;
  }
}

// what the hub knows: 工作's inbox count; reads of the roster held back while `holding`
let inbox: number;
let reads: number;
let holding: boolean;
let held: (() => void)[];
const roster = () => ({ default: "assistant", managers: [{ id: "assistant", name: "助手", online: true }, { id: "m-1", name: "工作", online: true, inbox_needs_you: inbox }] });
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const push = async (message: Record<string, unknown>) => {
  act(() => FakeSocket.last.onmessage?.({ data: JSON.stringify(message) }));
  act(() => flushPushes());
  await flush();
};
const head = () => host.querySelector<HTMLButtonElement>("[data-assistant-switcher]")!;
const workRow = () => [...document.body.querySelectorAll<HTMLElement>("[role=menuitemradio]")].find((r) => r.textContent?.includes("工作"));
const badge = () => workRow()?.querySelector("[data-menu-badge]")?.textContent ?? "none";

beforeEach(async () => {
  inbox = 2;
  reads = 0;
  holding = false;
  held = [];
  reset();
  resetRoster();
  resetManagerChoice();
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    const path = new URL(url, "http://hub.example.com").pathname;
    if (path === "/api/v1/managers") {
      reads++;
      const body = roster(); // what the hub knows when the read reaches it
      return holding ? new Promise<Response>((done) => held.push(() => done(json(body)))) : Promise.resolve(json(body));
    }
    const answers: Record<string, unknown> = { "/api/v1/sessions": { sessions: [] }, "/api/v1/machines": { machines: [] } };
    return Promise.resolve(json(answers[path] ?? {}));
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(() => refresh());
  act(() => connect());
  act(() => {
    FakeSocket.last.readyState = 1;
    FakeSocket.last.onopen?.();
  });
  await push({ type: "hello", server_version: "v1", event_stream: "s", event_seq: 1, resumed: true, features: [] });
  act(() => root.render(<AssistantSwitcher phase="online" online={0} total={0} route={{ name: "persona" }} />));
  await flush();
  // the list stays open through the pushes below
  await act(async () => head().click());
  await flush();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  reset();
  resetRoster();
  resetManagerChoice();
  vi.unstubAllGlobals();
});

it("counts again on a managers push, and says what the count is to a screen reader", async () => {
  expect(badge()).toBe("2");
  // the number is drawn; a screen reader hears what it counts
  expect(workRow()!.querySelector("[data-menu-badge]")!.getAttribute("aria-hidden")).toBe("true");
  expect(workRow()!.querySelector(".sr-only")?.textContent).toBe("2 项等待你处理");
  inbox = 5;
  await push({ type: "managers" });
  expect(badge()).toBe("5");
  expect(workRow()!.querySelector(".sr-only")?.textContent).toBe("5 项等待你处理");
});

it("reads the roster again for a push that came while a read was out", async () => {
  expect(badge()).toBe("2");
  holding = true;
  const before = reads;
  inbox = 5;
  await push({ type: "managers" }); // read A goes out; the hub answers it with 5
  inbox = 9; // the count moves again and the hub pushes before A is back
  await push({ type: "managers" });
  expect(reads - before).toBe(1);
  holding = false;
  await act(async () => held.shift()!());
  await flush();
  // A's answer, then the read after it: the hub's latest
  expect(reads - before).toBe(2);
  expect(badge()).toBe("9");
});
