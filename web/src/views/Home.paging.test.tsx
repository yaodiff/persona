// @vitest-environment jsdom
// Overview on a hub that pages its Agents list (`sessions_list`, audit item 30), with the real store, list and complete
// list: no hook of theirs is stood in for. Such a hub pushes no sessions (docs/protocol/protocol.md "Bounded Agents
// lists"): Overview's agents and counts come from the list's first pages, or with several assistants from the complete
// list. Until the list they come from is read, or when its read failed, the page says so (with Retry) instead of "Nothing
// needs you" and "0 agents" on every computer; a computer's count comes only from a list that holds all its agents.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { connect, flushPushes, reset } from "../lib/store";
import { resetRoster, type ManagerList } from "../lib/managers";
import { resetManagerChoice } from "../lib/manager-choice";
import type { Machine, Session } from "../lib/types";
import { Home } from "./Home";

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

const machines: Machine[] = [{ id: "mac", name: "MacBook", online: true, system: { name: "macOS 26.1" } } as Machine];
const row = (session_id: string, state: Session["state"], patch: Partial<Session> = {}): Session => ({
  machine_id: "mac", session_id, title: `任务 ${session_id}`, cwd: "/code", status: state === "working" ? "working" : "idle", state, updated_at: "2026-11-14T00:00:00Z", ...patch,
});
// one working agent and one finished, seen: "1 working", and the computer runs 2
const rows: Session[] = [row("m1", "working"), row("m2", "idle")];
const counts = { total: rows.length, machine: { mac: 2 }, project: {}, group: {}, state: { working: 1, idle: 1 } as Record<string, number>, revision: "r1" };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const several: ManagerList = { default: "assistant", managers: [{ id: "assistant", name: "助手", online: true }, { id: "work", name: "工作", online: true }] } as ManagerList;

type Answer = "ok" | "fail" | "hold";
// how the hub answers the list's first page (summary=1) and the complete list (summary=0); `more`: the first page is
// not all of them
let page: Answer, full: Answer, more: boolean, roster: ManagerList | null;
let held: { what: "page" | "full"; go: (ok: boolean) => void }[];
let reads: { page: number; full: number };
function hub(input: RequestInfo | URL): Promise<Response> {
  const url = new URL(String(input), "http://hub.example.com");
  const path = url.pathname;
  if (path === "/api/v1/sessions") {
    if (url.searchParams.has("counts")) return Promise.resolve(json(counts));
    const what = url.searchParams.get("summary") === "0" ? "full" : "page";
    reads[what]++;
    const answer = what === "full" ? full : page;
    const body = what === "page" && more ? { sessions: rows.slice(0, 1), remove: [], has_more: true, next_cursor: "c2", revision: "r1", total: rows.length } : { sessions: rows, remove: [], has_more: false, revision: "r1", total: rows.length };
    if (answer === "hold") return new Promise((done) => held.push({ what, go: (ok) => done(ok ? json(body) : json({ error: "boom" }, 500)) }));
    return Promise.resolve(answer === "ok" ? json(body) : json({ error: "boom" }, 500));
  }
  if (path === "/api/v1/managers") return Promise.resolve(roster ? json(roster) : json({ error: "not found" }, 404));
  const answers: Record<string, unknown> = {
    "/api/v1/machines": { machines },
    "/api/v1/interactions": { interactions: [] },
    "/api/v1/todos": { todos: [] },
  };
  return Promise.resolve(path in answers ? json(answers[path]) : json({ error: "not found" }, 404));
}
const release = async (what: "page" | "full", ok: boolean) => {
  const out = held.filter((h) => h.what === what);
  held = held.filter((h) => h.what !== what);
  await act(async () => { out.forEach((h) => h.go(ok)); });
  await flush();
};

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 12; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
async function hello() {
  act(() => connect());
  act(() => {
    FakeSocket.last.readyState = 1;
    FakeSocket.last.onopen?.();
  });
  act(() => FakeSocket.last.onmessage?.({ data: JSON.stringify({ type: "hello", server_version: "v1", features: ["sessions_list", "sessions_lookup"] }) }));
  act(() => flushPushes());
  await flush();
  act(() => root.render(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />));
  await flush();
}
const summary = () => host.querySelector("h1")?.nextElementSibling;
const summaryText = () => summary()?.querySelector("p")?.textContent ?? "";
// the line's live region: its state while the agents are not known, empty once the counts sentence is shown
const liveRegion = () => summary()?.querySelector("[role=status]")?.textContent;
const retry = () => [...(summary()?.querySelectorAll("button") ?? [])].find((b) => b.textContent === "重试");
const machineLine = () => host.querySelector("[data-machine-line]")?.textContent;

beforeEach(() => {
  page = "ok";
  full = "ok";
  more = false;
  roster = null;
  held = [];
  reads = { page: 0, full: 0 };
  reset();
  resetRoster();
  resetManagerChoice();
  localStorage.clear();
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("fetch", vi.fn(hub));
  history.replaceState(null, "", "/overview");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset(); // also ends the complete list's read and its retry
  resetRoster();
  resetManagerChoice();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Overview on a hub that pages its Agents list, with one assistant", () => {
  it("counts the computer's agents once the list is read", async () => {
    await hello();
    expect(summaryText()).toBe("1 个工作中。");
    expect(machineLine()).toBe("2 个 Agent · macOS 26.1");
    expect(reads.full).toBe(0); // one assistant: Overview reads no complete list
    // the counts sentence is not in the live region, which a screen reader would read out at every agent's change
    expect(liveRegion()).toBe("");
  });

  it("puts only its state in the live region: loading, then nothing once the counts are shown", async () => {
    page = "hold";
    await hello();
    expect(liveRegion()).toBe("正在读取…");
    await release("page", true);
    expect(liveRegion()).toBe("");
    expect(summaryText()).toBe("1 个工作中。");
    // an agent finishes: the sentence changes, the live region (mounted throughout) stays quiet
    rows[0] = row("m1", "unread");
    const before = { ...counts.state };
    counts.state = { unread: 1, idle: 1 };
    try {
      act(() => FakeSocket.last.onmessage?.({ data: JSON.stringify({ type: "sessions_list", revision: "r2" }) }));
      await flush();
      expect(summaryText()).toBe("1 个完成未看。");
      expect(liveRegion()).toBe("");
    } finally {
      rows[0] = row("m1", "working");
      counts.state = before;
    }
  });

  it("says it couldn't read them, with Retry, and Retry brings the counts back", async () => {
    page = "fail";
    await hello();
    expect(summaryText()).toBe("没能读到 Agent 的状态。");
    expect(liveRegion()).toBe("没能读到 Agent 的状态。"); // a screen reader hears the failure
    expect(retry()).toBeDefined();
    expect(machineLine()).toBe("macOS 26.1"); // no "0 个 Agent"
    expect(host.textContent).not.toContain("一切就绪");
    expect(host.textContent).not.toContain("所有 Agent 都在等你");
    // an agent changes the list while the hub still fails: the read that starts keeps the failure and its Retry where
    // they are (they do not turn into "Loading…" under the pointer), and so does its failure
    page = "hold";
    let before = reads.page;
    act(() => FakeSocket.last.onmessage?.({ data: JSON.stringify({ type: "sessions_list", revision: "r2" }) }));
    await flush();
    expect(reads.page).toBe(before + 1);
    expect(summaryText()).toBe("没能读到 Agent 的状态。");
    expect(retry()).toBeDefined();
    await release("page", false);
    expect(summaryText()).toBe("没能读到 Agent 的状态。");
    expect(retry()).toBeDefined();
    // Retry reads at once and says it is loading until the answer, as the complete list's Retry does; failed again,
    // the failure line and Retry come back
    before = reads.page;
    await act(async () => { retry()!.click(); });
    await flush();
    expect(reads.page).toBe(before + 1);
    expect(summaryText()).toBe("正在读取…");
    expect(retry()).toBeUndefined();
    await release("page", false);
    expect(summaryText()).toBe("没能读到 Agent 的状态。");
    expect(retry()).toBeDefined();
    // and once the hub answers, the counts come back
    page = "ok";
    await act(async () => { retry()!.click(); });
    await flush();
    expect(summaryText()).toBe("1 个工作中。");
    expect(retry()).toBeUndefined();
    expect(machineLine()).toBe("2 个 Agent · macOS 26.1");
  });

  it("gives the computer no count while the list has more pages, which may hold more of its agents", async () => {
    more = true;
    await hello();
    expect(machineLine()).toBe("macOS 26.1");
    expect(host.textContent).not.toContain("1 个 Agent");
    // the summary is the hub's count of every agent, not of the page
    expect(summaryText()).toBe("1 个工作中。");
  });
});

describe("Overview on a hub that pages its Agents list, with several assistants", () => {
  beforeEach(() => {
    roster = several;
  });

  it("counts the current assistant's agents from the complete list", async () => {
    await hello();
    expect(reads.full).toBeGreaterThan(0);
    expect(summaryText()).toBe("1 个工作中。");
    expect(machineLine()).toBe("2 个 Agent · macOS 26.1");
  });

  it("says it is loading, not that nothing needs you, while the complete list is read, though the first page is in", async () => {
    full = "hold";
    await hello();
    expect(reads.page).toBeGreaterThan(0);
    expect(summaryText()).toBe("正在读取…");
    expect(machineLine()).toBe("macOS 26.1");
    expect(host.textContent).not.toContain("一切就绪");
    expect(host.textContent).not.toContain("0 个 Agent");
    await release("full", true);
    expect(summaryText()).toBe("1 个工作中。");
    expect(machineLine()).toBe("2 个 Agent · macOS 26.1");
  });

  it("says it couldn't read them when the complete list fails, and Retry reads it at once", async () => {
    full = "fail";
    await hello();
    expect(summaryText()).toBe("没能读到 Agent 的状态。");
    expect(retry()).toBeDefined();
    expect(machineLine()).toBe("macOS 26.1");
    expect(host.textContent).not.toContain("一切就绪");
    full = "hold";
    const before = reads.full;
    await act(async () => { retry()!.click(); });
    await flush(); // well before the complete list's own retry, five seconds on
    expect(reads.full).toBe(before + 1);
    expect(summaryText()).toBe("正在读取…"); // loading until the answer
    expect(machineLine()).toBe("macOS 26.1");
    await release("full", true);
    expect(summaryText()).toBe("1 个工作中。");
    expect(machineLine()).toBe("2 个 Agent · macOS 26.1");
  });
});
