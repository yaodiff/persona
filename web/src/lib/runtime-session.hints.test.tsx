// @vitest-environment jsdom
// An open chat on a paging hub reads its row again only for a sessions_list hint about it (#6685): every other
// session's change used to cost one exact lookup per hint.
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useRuntimeSession } from "./runtime-session";
import { connect, reset } from "./store";
import type { Session } from "./types";

class Socket {
  static OPEN = 1;
  static last: Socket;
  readyState = 1;
  onopen?: () => void;
  onmessage?: (event: { data: string }) => void;
  constructor(public url: string) { Socket.last = this; }
  send() {}
  close() { this.readyState = 3; }
  push(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }); }
}
const open: Session = { machine_id: "demo", session_id: "open", title: "Open chat", cwd: "/demo", status: "idle", state: "idle" };
let lookups: number;
let indexAt: string;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  lookups = 0;
  indexAt = "r1";
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("localStorage", { getItem: () => "demo-token", setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const url = new URL(path, "http://demo");
    if (url.pathname.endsWith("/api/v1/sessions") && url.searchParams.get("session_id") === "open") {
      lookups++;
      return Response.json({ sessions: [open], remove: [], total: 1, has_more: false, revision: indexAt });
    }
    return Response.json({});
  }));
});
afterEach(() => { reset(); vi.useRealTimers(); vi.unstubAllGlobals(); });
const settle = () => act(() => vi.advanceTimersByTimeAsync(200));

describe("an open chat and sessions_list hints", () => {
  it("asks for the changed rows, and reads its row only when a hint names it or cannot say", async () => {
    connect();
    expect(new URL(Socket.last.url).searchParams.get("features")?.split(",")).toContain("sessions_list_changes");
    Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup"] });
    await settle();
    const Probe = () => { useRuntimeSession("demo", "open"); return null; };
    const root = createRoot(document.createElement("div"));
    try {
      await act(async () => root.render(createElement(Probe)));
      await settle();
      expect(lookups).toBe(1);

      // ten other sessions change, one hint each, in an unbroken chain from the revision the answer stands at
      for (let i = 1; i <= 10; i++) {
        indexAt = `r${i + 1}`;
        Socket.last.push({ type: "sessions_list", revision: indexAt, since: `r${i}`, changed: [["demo", `other-${i}`]] });
        await settle();
      }
      expect(lookups).toBe(1);

      // this chat changes
      indexAt = "r12";
      Socket.last.push({ type: "sessions_list", revision: "r12", since: "r11", changed: [["demo", "other-11"], ["demo", "open"]] });
      await settle();
      expect(lookups).toBe(2);

      // a gap in the chain (a hint this page missed or a change no hint listed), then a hint without the fields
      indexAt = "r14";
      Socket.last.push({ type: "sessions_list", revision: "r14", since: "r13", changed: [["demo", "other-12"]] });
      await settle();
      expect(lookups).toBe(3);
      indexAt = "r15";
      Socket.last.push({ type: "sessions_list", revision: "r15" });
      await settle();
      expect(lookups).toBe(4);
      // and back in the chain
      Socket.last.push({ type: "sessions_list", revision: "r16", since: "r15", changed: [["other-machine", "open"]] });
      await settle();
      expect(lookups).toBe(4);
    } finally {
      act(() => root.unmount());
    }
  });
});
