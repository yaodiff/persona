// @vitest-environment jsdom
// The Machines page's alerts (#8037): a chip per alert on each card, worst first and the worst computers first; a chip
// opens the alert sheet on its alert without opening the computer; the hand-off sends one request to Persona's main
// chat; the Capabilities view lists every computer and is remembered.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine, MachineMetrics, Session } from "../lib/types";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const fake = vi.hoisted(() => ({ machines: [] as Machine[], sessions: [] as Session[] }));
vi.mock("../lib/store", async (load) => {
  const real = await load<typeof import("../lib/store")>();
  const state = () => ({ ...real.getState(), loaded: true, machines: fake.machines, version: "v1-202611141020", assistant: { machine_id: "hub", main: "main-chat", conversations: [] } });
  return { ...real, getState: state, refresh: () => Promise.resolve(), useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select(state()) };
});
vi.mock("../lib/sessions", async (load) => {
  const real = await load<typeof import("../lib/sessions")>();
  return { ...real, useFullAgentSessions: () => ({ sessions: fake.sessions, loaded: true, error: "" }) };
});
const sent = vi.hoisted(() => [] as { mid: string; sid: string; text: string }[]);
vi.mock("../lib/outbox", async (load) => {
  const real = await load<typeof import("../lib/outbox")>();
  return { ...real, sendToSession: (mid: string, sid: string, out: { text: string }) => { sent.push({ mid, sid, text: out.text }); return Promise.resolve(true); } };
});

const { Machines } = await import("./Machines");
const { resetTools } = await import("../lib/machine-diagnosis");
const { resetCapabilityCache } = await import("./machines/MachineCapabilities");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const GB = 1024 ** 3;
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
const metric = (mid: string, mem: number, disk = 0.3): MachineMetrics => ({ machine_id: mid, online: true, points: [], system: { cores: 8, memory: 32 * GB }, latest: { t: 1, cpu: 20, load: 2, mem_used: mem * 32 * GB, mem_total: 32 * GB, disk_used: disk * 1000, disk_total: 1000 } });
let metricsList: MachineMetrics[] = [];
let health: unknown[] = [];
const requests: string[] = [];

function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input), "http://x");
  requests.push(`${init?.method ?? "GET"} ${url.pathname}${url.search}`);
  if (url.pathname === "/api/v1/metrics") return Promise.resolve(json({ machines: metricsList }));
  if (url.pathname === "/api/v1/launchers/health") return Promise.resolve(json({ records: health }));
  if (url.pathname === "/api/v1/launchers/health/check") return Promise.resolve(json({ records: [] }));
  const m =/^\/api\/v1\/machines\/([^/]+)\/(diagnosis|launchers)$/.exec(url.pathname);
  if (m?.[2] === "diagnosis") return Promise.resolve(json({ machine_id: m[1], at: 1, tools: [{ id: "git", version: "2.47" }], processes: { mem_total: 32 * GB, count: 10, groups: [] } }));
  if (m?.[2] === "launchers") return Promise.resolve(json({ launchers: [{ name: "claude" }, { name: "codex", agent: "codex" }] }));
  return Promise.resolve(json({ error: "not found", code: "not_found" }, 404));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 8; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const $ = (sel: string) => document.body.querySelector(sel);
const $$ = (sel: string) => [...document.body.querySelectorAll(sel)];
const click = async (el: Element | null) => {
  expect(el).not.toBeNull();
  await act(async () => { (el as HTMLElement).dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })); });
  await flush();
};
const card = (name: string) => [...host.querySelectorAll("section")].find((s) => s.querySelector("h2")?.textContent === name) ?? null;

beforeEach(() => {
  localStorage.clear();
  resetTools();
  resetCapabilityCache();
  sent.length = 0;
  requests.length = 0;
  health = [];
  fake.machines = [
    { id: "ok", name: "Office Mini", online: true, os: "darwin", system: { cores: 8, memory: 32 * GB } },
    { id: "box", name: "Build Box", online: true, os: "linux", system: { cores: 8, memory: 32 * GB } },
    { id: "laptop", name: "Travel Laptop", online: true, os: "darwin", system: { cores: 8, memory: 32 * GB } },
    { id: "nas", name: "Garage NAS", online: false, error: "ssh: connect to host nas port 22: Operation timed out" },
  ];
  metricsList = [metric("ok", 0.3), metric("box", 0.4, 0.85), metric("laptop", 0.95)];
  fake.sessions = [
    { machine_id: "laptop", session_id: "w1", title: "Build the widget", cwd: "/code", status: "working", state: "working", todo: { id: "t1", title: "Ship 1.3" } },
    { machine_id: "laptop", session_id: "i1", title: "Draft the email", cwd: "/code", status: "idle", state: "idle" },
  ];
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  vi.stubGlobal("fetch", vi.fn(hub));
  history.replaceState(null, "", "/machines");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("alert chips on the Machines page", () => {
  it("puts a chip per alert on each card, and the worst computers first", async () => {
    health = [{ machine: "ok", launcher: "codex", state: "failing", kind: "usage_limit" }, { machine: "ok", launcher: "claude", state: "ok" }];
    await act(async () => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect([...host.querySelectorAll("section h2")].map((h) => h.textContent).filter((n) => n !== "今日 Token")).toEqual(["Garage NAS", "Travel Laptop", "Office Mini", "Build Box"]); // two warnings keep the hub's order
    const chips = (name: string) => [...(card(name)?.querySelectorAll("[data-alert]") ?? [])].map((b) => `${b.getAttribute("data-alert")}:${b.getAttribute("data-severity")}`);
    expect(chips("Travel Laptop")).toEqual(["memory:critical"]);
    expect(chips("Build Box")).toEqual(["disk:warning"]);
    expect(chips("Office Mini")).toEqual(["launcher:warning"]);
    expect(chips("Garage NAS")).toEqual(["offline:critical"]);
    const memory = card("Travel Laptop")!.querySelector("[data-alert=memory]")!;
    expect(memory.tagName).toBe("BUTTON");
    expect(memory.getAttribute("aria-label")).toBe("严重：内存占用 · 95%");
    expect(card("Garage NAS")!.querySelector("[data-alert]")?.textContent).toBe("离线 · 排查建议");
    // the offline card keeps its one red notice
    expect(card("Garage NAS")!.querySelectorAll(".bg-red-tint")).toHaveLength(1);
    // what load counts, once, under the cards (#12519)
    expect(host.querySelectorAll("[data-testid=load-definition]")).toHaveLength(1);
    expect(host.querySelector("[data-testid=load-definition]")?.textContent).toMatch(/^负载指/);
  });

  it("takes the disk chip to recovery on the affected computer without starting a cleanup", async () => {
    await act(async () => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    await click(card("Build Box")!.querySelector('[data-alert="disk"]'));
    expect(location.pathname + location.search).toBe("/machines/box/storage?mode=reclaim");
    expect($("[data-testid=alert-sheet]")).toBeNull();
    expect(requests.some(r => r.startsWith("POST") && r.includes("/cleanup"))).toBe(false);
  });

  // A check that answers no record for its launcher (#14534): the page reads the records again, so a record the hub
  // no longer holds leaves the sheet and the card instead of staying as it was.
  it("reads the launcher records again after a check with no result", async () => {
    health = [{ machine: "ok", launcher: "codex", state: "failing", kind: "cli_outdated", error: "Method not found: session/set_mode", checked_at: new Date().toISOString() }];
    await act(async () => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    await click(card("Office Mini")!.querySelector("[data-alert=launcher]"));
    expect($$("[data-testid=failing-launchers] [data-launcher]").map((l) => l.getAttribute("data-launcher"))).toEqual(["codex"]);
    const reads = () => requests.filter((r) => r === "GET /api/v1/launchers/health").length;
    const before = reads();
    health = [];
    await click($("[data-launcher=codex] [data-testid=test-again]"));
    expect(requests).toContain("POST /api/v1/launchers/health/check?machine=ok&launcher=codex");
    expect(reads()).toBe(before + 1);
    expect($$("[data-testid=failing-launchers] [data-launcher]")).toEqual([]);
    expect(card("Office Mini")!.querySelector("[data-alert=launcher]")).toBeNull();
  });

  it("opens the version sheet from the mismatch strip's Details", async () => {
    fake.machines = [{ id: "studio", name: "Studio Mac", online: true, kind: "ssh", node_mismatch: true, can_deploy: true, node_version: "v1-202611110815" }];
    metricsList = [];
    await act(async () => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(card("Studio Mac")!.querySelector("[data-alert]")).toBeNull(); // the strip says it, not a chip as well
    await click(card("Studio Mac")!.querySelector("[data-testid=deploy-details]"));
    expect($("[data-testid=alert-sheet]")?.getAttribute("data-kind")).toBe("mismatch");
    expect(location.pathname).toBe("/machines");
  });

  it("hands work to another computer through one message to Persona's main chat", async () => {
    await act(async () => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    await click(card("Travel Laptop")!.querySelector("[data-alert=memory]"));
    await click($("[data-testid=handoff]"));
    const target = (id: string) => $(`[data-testid=handoff-targets] [data-target=${id}]`);
    expect(target("nas")?.getAttribute("data-eligible")).toBe("false");
    expect(target("nas")?.textContent).toContain("离线");
    // Office Mini: same system, more free memory than Build Box, no alert
    expect(($("[data-testid=handoff-targets] [data-target]") as HTMLElement).getAttribute("data-target")).toBe("ok");
    expect((target("ok")?.querySelector("input") as HTMLInputElement).checked).toBe(true);
    // the to-do's Agent is picked, the loose idle one is not
    expect(($("[data-session=w1] input") as HTMLInputElement).checked).toBe(true);
    expect(($("[data-session=i1] input") as HTMLInputElement).checked).toBe(false);
    await click($("[data-testid=handoff-next]"));
    expect($("[data-testid=handoff-confirm-list]")?.textContent).toContain("Build the widget");
    expect(sent).toHaveLength(0);
    await click($("[data-testid=handoff-confirm]"));
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ mid: "hub", sid: "main-chat" });
    expect(sent[0].text).toContain("Travel Laptop（电脑 ID：laptop）");
    expect(sent[0].text).toContain("Office Mini（电脑 ID：ok）");
    expect(sent[0].text).toContain("会话 ID：w1");
    expect(sent[0].text).toContain("待办 ID：t1");
    expect(sent[0].text).not.toContain("i1");
  });
});

describe("the Capabilities view", () => {
  it("lists every computer with what it can run, reads tools and launchers lazily, and is remembered", async () => {
    await act(async () => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    await click([...host.querySelectorAll("[role=radio]")].find((b) => b.textContent === "能力") ?? null);
    await flush();
    const rows = $$("[data-testid=capabilities] [data-row]");
    expect(rows.map((r) => r.getAttribute("data-row"))).toEqual(["ok", "box", "laptop", "nas"]);
    const laptop = $("[data-row=laptop]")?.textContent ?? "";
    expect(laptop).toContain("8 核");
    expect(laptop).toContain("Claude Code");
    expect(laptop).toContain("Codex");
    expect(laptop).toContain("Git 2.47");
    expect($("[data-row=nas]")?.textContent).toContain("连接后显示");
    // only the online computers are asked, once each
    expect(requests.filter((r) => r.includes("/diagnosis?sections=tools")).sort()).toEqual(["GET /api/v1/machines/box/diagnosis?sections=tools", "GET /api/v1/machines/laptop/diagnosis?sections=tools", "GET /api/v1/machines/ok/diagnosis?sections=tools"]);
    expect(localStorage.getItem("persona.machines.view")).toBe("capabilities");
    expect(host.querySelector("[data-testid=machine-alerts]")).toBeNull(); // no cards in this view
  });
});
