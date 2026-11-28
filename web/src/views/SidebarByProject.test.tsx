// @vitest-environment jsdom
// The sidebar's By project on a paging hub with the catalog, on two computers (#13522): a section per computer named as
// the app names it, with its online state, its spaces inside (same-named ones told apart by their parent folder), and
// Agent rows that leave out the computer their heading already says.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../i18n";
import { loadPages } from "../lib/lazy-page";
import { onOpenNewAgent, type NewAgentPreset } from "../lib/new-agent";
import { connect, reset } from "../lib/store";
import { SESSIONS_AGENTS_ONLY_FEATURE } from "../lib/runtime-list";
import { SESSIONS_CATALOG_FEATURE } from "../lib/session-groups";
import { listPair } from "../lib/session-list";
import type { Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  constructor() { FakeSocket.all.push(this); }
  send() {}
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  push(msg: Record<string, unknown>) { this.onmessage?.({ data: JSON.stringify(msg) }); }
}

const agent = (session_id: string, machine_id: string, cwd: string, space?: { id: string; name: string }): Session => ({
  machine_id, session_id, title: `Agent ${session_id}`, cwd, status: "idle", state: "idle", updated_at: "2026-01-01T00:00:00Z",
  ...(space ? { space_id: space.id, space_name: space.name, space_kind: "herdr" } : {}),
});
const ROWS: Session[] = [
  agent("d1", "laptop", "/Users/me/Desktop/app", { id: "w1", name: "app" }),
  agent("c1", "laptop", "/Users/me/code/app", { id: "w2", name: "app" }),
  agent("n1", "laptop", "/Users/me/scratch"),
  agent("s1", "server", "/srv/api", { id: "w1", name: "api" }),
];
const MACHINES = [{ id: "laptop", name: "Work laptop", online: true }, { id: "server", name: "Build server", online: false, last_seen: "2026-01-01T00:00:00Z" }];

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };
let host: HTMLDivElement, root: Root;

/** The rows a read selects by its space pairs, and the hub's answers: a page, the counts, the catalog with each space's folder. */
function answer(url: URL) {
  const q = url.searchParams;
  const spaces = q.getAll("space");
  const rows = ROWS.filter((s) => !spaces.length || spaces.includes(listPair(s.machine_id, s.space_id ?? "")));
  const tally = (key: (s: Session) => string) => rows.reduce<Record<string, number>>((out, s) => ({ ...out, [key(s)]: (out[key(s)] ?? 0) + 1 }), {});
  if (q.has("catalog")) {
    const spaced = new Map<string, { machine_id: string; id: string; name: string; path?: string; total: number }>();
    for (const s of rows) {
      const pair = listPair(s.machine_id, s.space_id ?? "");
      const at = spaced.get(pair) ?? { machine_id: s.machine_id, id: s.space_id ?? "", name: s.space_name ?? "", ...(s.space_id ? { path: s.cwd } : {}), total: 0 };
      at.total++;
      spaced.set(pair, at);
    }
    return json({ revision: "r1", total: rows.length, projects: [], spaces: [...spaced.values()] });
  }
  if (q.has("counts")) return json({ total: rows.length, revision: "r1", state: tally((s) => s.state ?? ""), machine: tally((s) => s.machine_id), group: tally((s) => s.space_id ?? ""), project: tally((s) => s.cwd) });
  return json({ sessions: rows, remove: [], has_more: false, revision: "r1", total: rows.length });
}

async function render() {
  localStorage.setItem("persona.agentGrouping", "space");
  const fetch = vi.fn((input: string) => {
    const url = new URL(input, "http://hub.example.com");
    if (url.pathname.endsWith("/sessions")) return answer(url);
    if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
    if (url.pathname.endsWith("/machines")) return json({ machines: MACHINES });
    if (url.pathname.endsWith("/todos")) return json({ todos: [] });
    return json({});
  });
  vi.stubGlobal("fetch", fetch);
  act(() => {
    connect();
    const socket = FakeSocket.all.at(-1)!;
    socket.open();
    socket.push({ type: "hello", features: ["sessions_list", "sessions_lookup", SESSIONS_AGENTS_ONLY_FEATURE, SESSIONS_CATALOG_FEATURE] });
  });
  await flush();
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
  return fetch;
}

// the sections are a chunk of their own (lazyPage): loaded before the tests draw them
beforeAll(() => loadPages(), 60_000);

beforeEach(() => {
  localStorage.clear();
  reset();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); reset(); vi.unstubAllGlobals();
});

describe("By project on two computers (#13522)", () => {
  const computers = () => [...host.querySelectorAll<HTMLElement>("[data-session-computer]")];
  const fold = (el: HTMLElement) => el.querySelector<HTMLButtonElement>("button[aria-expanded]")!;

  it("puts each computer's projects under its name and state, and its rows leave the computer out", async () => {
    await render();
    expect(computers().map((c) => c.dataset.sessionComputer)).toEqual(["laptop", "server"]);
    expect(computers().map((c) => fold(c).textContent)).toEqual([`Work laptop${t("sidebar.online")}`, `Build server${t("sidebar.offline")}`]);
    // the computer that is away starts folded
    expect(computers().map((c) => fold(c).getAttribute("aria-expanded"))).toEqual(["true", "false"]);
    const laptop = computers()[0];
    const spaces = [...laptop.querySelectorAll<HTMLElement>("[data-session-group]")];
    expect(spaces.map((s) => fold(s).textContent)).toEqual(["app · ~/Desktop", "app · ~/code", t("paged-agents.no_space")]);
    // under a computer's project an Agent row names neither the computer nor the project
    const rows = [...laptop.querySelectorAll<HTMLElement>("[data-session-row]")];
    expect(rows.map((r) => r.textContent?.includes("Agent "))).toEqual([true, true, true]);
    for (const r of rows) expect(r.textContent).not.toContain("Work laptop");
  });

  it("starts a new Agent from a project's + in its folder on its computer, through the one New agent sheet (#13523)", async () => {
    const opened: NewAgentPreset[] = [];
    const off = onOpenNewAgent((preset) => opened.push(preset));
    try {
      await render();
      const plus = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("agents-groupedsessions.new_agent_in", { name: "app · ~/Desktop" })}"]`)!;
      await act(async () => plus.click());
      expect(opened).toEqual([{ machineId: "laptop", cwd: "/Users/me/Desktop/app" }]);
      // one on each of the laptop's projects with a folder, none on its No project; the server is away
      expect(computers().map((c) => c.querySelectorAll("[data-space-add]").length)).toEqual([2, 0]);
    } finally {
      off();
    }
  });
});
