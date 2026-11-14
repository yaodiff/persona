// @vitest-environment jsdom
// The machine cards and a machine's page on a hub that pages its Agents list (`sessions_list`, audit item 3). Such a hub
// pushes no sessions over the socket (docs/protocol/protocol.md "Bounded Agents lists"), so the store holds none of a
// computer's agents: the pages read them as the Agents list does (lib/sessions.ts useFullAgentSessions) and, until that
// read is in, say so rather than "No agents running" / "Agent 0". An older hub's pushed lists count as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { connect, flushPushes, getState, reset } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Machines } from "./Machines";
import { MachineDetail } from "./machines/MachineDetail";
import { SpacePage } from "./SpacePage";

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

const machines: Machine[] = [
  { id: "mac", name: "MacBook", online: true },
  { id: "nas", name: "NAS", online: true },
];
const row = (machine_id: string, session_id: string, state: Session["state"], patch: Partial<Session> = {}): Session => ({
  machine_id, session_id, title: `任务 ${session_id}`, cwd: "/code", status: state === "working" ? "working" : "idle", state, updated_at: "2026-11-14T00:00:00Z", ...patch,
});
// MacBook: two agents in the space "weather", one working, one stopped (not counted) and a Persona conversation (no
// agent); NAS: one waiting
const weather = { space_kind: "herdr", space_id: "w1", space_name: "weather" } as const;
const rows: Session[] = [
  row("mac", "m1", "idle", weather),
  row("mac", "m2", "working", weather),
  row("mac", "m3", "offline"),
  row("mac", "p1", "idle", { assistant: true }),
  row("nas", "n1", "needs_you"),
];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const page = (sessions: Session[]) => ({ sessions, remove: [], has_more: false, revision: "r1", total: sessions.length });
const counts = { total: rows.length, machine: { mac: 4, nas: 1 }, project: {}, group: {}, state: { idle: 2, working: 1, offline: 1, needs_you: 1 }, revision: "r1" };

// the complete list's read (summary=0): answered, failed, or held until the test lets it fail
let full: "ok" | "fail" | "hold";
let held: (() => void)[];
let fullReads: number;
function hub(input: RequestInfo | URL): Promise<Response> {
  const url = new URL(String(input), "http://hub.example.com");
  const path = url.pathname;
  if (path === "/api/v1/sessions") {
    if (!url.search) return Promise.resolve(json({ sessions: rows })); // an older hub's whole list
    if (url.searchParams.has("counts")) return Promise.resolve(json(counts));
    if (url.searchParams.get("summary") === "0") {
      fullReads++;
      if (full === "fail") return Promise.resolve(json({ error: "boom" }, 500));
      if (full === "hold") return new Promise((done) => held.push(() => done(json({ error: "boom" }, 500))));
      return Promise.resolve(json(page(rows)));
    }
    // the bounded first page: only the newest row, as a busy hub's first page holds only some of them
    return Promise.resolve(json(page(rows.slice(-1))));
  }
  const answers: Record<string, unknown> = {
    "/api/v1/machines": { machines },
    "/api/v1/metrics": { machines: [] },
    "/api/v1/interactions": { interactions: [] },
    "/api/v1/machines/mac/metrics": { metrics: { points: [] } },
    "/api/v1/machines/mac/launchers": { launchers: [] },
  };
  return Promise.resolve(path in answers ? json(answers[path]) : json({ error: "not found" }, 404));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 12; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const push = async (message: Record<string, unknown>) => {
  act(() => FakeSocket.last.onmessage?.({ data: JSON.stringify(message) }));
  act(() => flushPushes());
  await flush();
};
const card = (name: string) => [...host.querySelectorAll("section")].find((s) => s.querySelector("h2")?.textContent === name);
const footer = (name: string) => card(name)?.querySelector("[data-agents]");
const agentsBox = () => host.querySelector("p[data-agents]");
const agentsHeading = () => [...host.querySelectorAll("h2")].find((h) => h.textContent === "Agent")?.parentElement?.textContent ?? "";

async function hello(features: string[]) {
  act(() => connect());
  act(() => {
    FakeSocket.last.readyState = 1;
    FakeSocket.last.onopen?.();
  });
  await push({ type: "hello", server_version: "v1", features });
  await flush();
}

beforeEach(() => {
  full = "ok";
  held = [];
  fullReads = 0;
  reset();
  localStorage.clear();
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("fetch", vi.fn(hub));
  history.replaceState(null, "", "/machines");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset(); // also ends the complete list's read and its retry: a new test starts with none
  vi.unstubAllGlobals();
});

describe("on a hub that pages its Agents list", () => {
  it("counts each computer's agents, which the socket never pushed", async () => {
    await hello(["sessions_list", "sessions_lookup"]);
    expect(getState().loaded).toBe(true);
    expect(getState().sessions).toEqual({}); // nothing pushed: what the cards used to read
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(footer("MacBook")?.textContent).toBe("2 个 Agent，1 个在工作");
    expect(footer("NAS")?.textContent).toBe("1 个 Agent");
    expect(fullReads).toBe(1);
  });

  it("says the agents are being read, then that they could not be, never that none run", async () => {
    full = "hold";
    await hello(["sessions_list", "sessions_lookup"]);
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(footer("MacBook")?.textContent).toBe("正在读取 Agent…");
    expect(footer("MacBook")?.getAttribute("data-agents")).toBe("loading");
    await act(async () => { held.forEach((go) => go()); });
    await flush();
    expect(footer("MacBook")?.textContent).toBe("没能读到 Agent");
    expect(footer("NAS")?.getAttribute("data-agents")).toBe("failed");
    expect(host.textContent).not.toContain("没有在跑的 Agent");
  });

  it("lists a computer's agents on its page, by its count", async () => {
    await hello(["sessions_list", "sessions_lookup"]);
    history.replaceState(null, "", "/machines/mac");
    act(() => root.render(<MachineDetail mid="mac" onToggleSidebar={() => {}} />));
    await flush();
    expect(agentsHeading()).toBe("Agent2");
    expect(host.textContent).toContain("任务 m1");
    expect(host.textContent).toContain("任务 m2");
    expect(host.textContent).not.toContain("任务 m3"); // stopped
    expect(host.textContent).not.toContain("任务 p1"); // a Persona conversation
    expect(agentsBox()).toBeNull();
  });

  it("gives a computer's page no count, and no 'No agents running', while they can't be read", async () => {
    full = "fail";
    await hello(["sessions_list", "sessions_lookup"]);
    act(() => root.render(<MachineDetail mid="mac" onToggleSidebar={() => {}} />));
    await flush();
    expect(agentsHeading()).toBe("Agent");
    expect(agentsBox()?.getAttribute("data-agents")).toBe("failed");
    expect(agentsBox()?.textContent).toBe("没能读到这台电脑的 Agent，稍后会再试。");
  });
});

describe("a space's page on a hub that pages its Agents list", () => {
  const space = () => act(() => root.render(<SpacePage mid="mac" space="weather" onToggleSidebar={() => {}} />));

  it("lists the space's agents, which the computer's page links to", async () => {
    await hello(["sessions_list", "sessions_lookup"]);
    history.replaceState(null, "", "/machines/mac");
    act(() => root.render(<MachineDetail mid="mac" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector('a[href="/space/mac/weather"]')).not.toBeNull();
    history.replaceState(null, "", "/space/mac/weather");
    space();
    await flush();
    expect(host.textContent).toContain("任务 m1");
    expect(host.textContent).toContain("任务 m2");
    expect(host.textContent).not.toContain("找不到这个空间");
  });

  it("is not 'not found' while the agents are read, nor when they can't be: it says so, with Retry", async () => {
    full = "hold";
    await hello(["sessions_list", "sessions_lookup"]);
    space();
    await flush();
    expect(host.textContent).not.toContain("找不到这个空间");
    expect(host.querySelector("[data-agents]")).toBeNull(); // loading
    await act(async () => { held.forEach((go) => go()); });
    await flush();
    expect(host.querySelector("[data-agents]")?.getAttribute("data-agents")).toBe("failed");
    expect(host.textContent).toContain("没能读到这个空间的 Agent");
    expect(host.textContent).not.toContain("找不到这个空间");
    // Retry reads them at once, not at the complete list's next try
    full = "ok";
    const before = fullReads;
    const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === "重试");
    await act(async () => { retry!.click(); });
    await flush();
    expect(fullReads).toBe(before + 1);
    expect(host.textContent).toContain("任务 m2");
  });

  it("still says a space with no agents is not found, once they are read", async () => {
    await hello(["sessions_list", "sessions_lookup"]);
    act(() => root.render(<SpacePage mid="mac" space="gone" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).toContain("找不到这个空间");
  });
});

describe("on a hub that pushes its sessions", () => {
  it("counts them as before, with no read of its own", async () => {
    await hello([]);
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(footer("MacBook")?.textContent).toBe("2 个 Agent，1 个在工作");
    expect(footer("NAS")?.textContent).toBe("1 个 Agent");
    expect(fullReads).toBe(0);
    act(() => root.render(<MachineDetail mid="mac" onToggleSidebar={() => {}} />));
    await flush();
    expect(agentsHeading()).toBe("Agent2");
  });
});
