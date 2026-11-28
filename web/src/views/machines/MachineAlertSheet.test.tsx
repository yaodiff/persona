// @vitest-environment jsdom
// A computer's alert sheet (#8037): what is wrong, why, what you can do, for each kind of alert; every action that
// changes something asks first and says what really happened; a hub or node without the diagnosis says so while the
// rest still works.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AlertKind, LauncherHealthRecord, MachineAlert } from "../../lib/machine-alerts";
import { resetRoster, setRoster } from "../../lib/managers";
import type { Machine, MachineMetrics, Session } from "../../lib/types";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const hub = vi.hoisted(() => ({ version: "v1-202611141020" }));
vi.mock("../../lib/store", async (load) => {
  const real = await load<typeof import("../../lib/store")>();
  return {
    ...real,
    refresh: () => Promise.resolve(),
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), version: hub.version }),
  };
});

const { MachineAlertSheet } = await import("./MachineAlertSheet");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const GB = 1024 ** 3;
type Call = { method: string; path: string; body?: unknown };
let calls: Call[] = [];
let routes: Record<string, (body: unknown) => Response | Promise<Response>> = {};
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

function fetcher(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input), "http://x");
  const method = init?.method ?? "GET";
  const path = url.pathname + url.search;
  const body = init?.body ? JSON.parse(String(init.body)) : undefined;
  calls.push({ method, path, body });
  const route = routes[`${method} ${url.pathname}`];
  return Promise.resolve(route ? route(body) : json({ error: "not found", code: "not_found" }, 404));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const $ = (sel: string) => document.body.querySelector(sel);
const $$ = (sel: string) => [...document.body.querySelectorAll(sel)];
const click = async (el: Element | null) => {
  expect(el).not.toBeNull();
  await act(async () => { (el as HTMLElement).click(); });
  await flush();
};
const posts = (part: string) => calls.filter((c) => c.method === "POST" && c.path.includes(part));

const laptop: Machine = { id: "laptop", name: "Travel Laptop", online: true, kind: "ssh", system: { cores: 10, memory: 48 * GB } };
const metrics: MachineMetrics = { machine_id: "laptop", online: true, points: [], latest: { t: 1, cpu: 40, load: 4, mem_used: 46 * GB, mem_total: 48 * GB, disk_used: 400 * GB, disk_total: 500 * GB } };
const agent = (sid: string, title: string, state: Session["state"], extra: Partial<Session> = {}): Session => ({
  machine_id: "laptop", session_id: sid, title, cwd: "/code", state, status: state === "needs_you" ? "blocked" : state === "unread" ? "done" : state ?? "idle", ...extra,
});
const agents = [
  agent("n1", "Answer the question", "needs_you"),
  agent("w1", "Build the widget", "working", { todo: { id: "t1", title: "Ship 1.3" } }),
  agent("i1", "Draft the email", "idle", { state_since: new Date(Date.now() - 95 * 60_000).toISOString() }),
  agent("u1", "Release notes", "unread"),
];

function render(over: { machine?: Machine; kind?: Exclude<AlertKind, "disk">; alerts?: MachineAlert[]; records?: LauncherHealthRecord[]; onReconnect?: (id: string) => void; onRecords?: (r: LauncherHealthRecord[]) => void; onReloadRecords?: () => void; onKind?: (k: AlertKind) => void; agents?: Session[] } = {}) {
  act(() => root.render(
    <MachineAlertSheet
      machine={over.machine ?? laptop}
      kind={over.kind ?? "memory"}
      alerts={over.alerts ?? [{ kind: over.kind ?? "memory", severity: "critical" }]}
      metrics={metrics}
      agents={over.agents ?? agents}
      agentsKnown="yes"
      machines={[over.machine ?? laptop]}
      allMetrics={[metrics]}
      records={over.records ?? []}
      launcherIssues={() => 0}
      onKind={over.onKind ?? (() => {})}
      onClose={() => {}}
      onReconnect={over.onReconnect ?? (() => {})}
      onRecords={over.onRecords ?? (() => {})}
      onReloadRecords={over.onReloadRecords}
    />,
  ));
}

const processes = { mem_total: 48 * GB, count: 300, groups: [
  { name: "claude", kind: "agent", count: 6, rss: 8 * GB, cpu: 14 },
  { name: "node", kind: "other", count: 14, rss: 6 * GB, cpu: 40 },
  { name: "herdr", kind: "engine", count: 1, rss: 1 * GB, cpu: 1 },
  { name: "personad", kind: "persona", count: 1, rss: 0.5 * GB },
] };

beforeEach(() => {
  calls = [];
  routes = {};
  vi.stubGlobal("fetch", vi.fn(fetcher));
  history.replaceState(null, "", "/machines");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  resetRoster();
  vi.unstubAllGlobals();
});

describe("the memory sheet", () => {
  it("shows what uses the memory and the Agents on the computer, in the three parts' order", async () => {
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => json({ machine_id: "laptop", at: 1, processes });
    render();
    await flush();
    expect(calls.find((c) => c.path.startsWith("/api/v1/machines/laptop/diagnosis"))?.path).toBe("/api/v1/machines/laptop/diagnosis?sections=processes");
    expect($$("[data-part]").map((p) => p.getAttribute("data-part"))).toEqual(["what", "why", "do"]);
    expect($$("[data-process]").map((p) => p.getAttribute("data-process"))).toEqual(["claude", "node", "herdr", "personad"]);
    expect($("[data-process=claude]")?.textContent).toContain("Agent");
    expect($("[data-process=herdr]")?.textContent).toContain("引擎");
    expect($("[data-group=needs_you]")?.textContent).toContain("Answer the question");
    expect($("[data-group=working]")?.textContent).toContain("Build the widget");
    expect($("[data-group=idle]")?.textContent).toContain("Draft the email");
    expect($("[data-group=idle]")?.textContent).toContain("空闲 1 小时 35 分");
  });

  it("sorts by CPU for a CPU alert", async () => {
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => json({ machine_id: "laptop", at: 1, processes });
    render({ kind: "cpu", alerts: [{ kind: "cpu", severity: "warning" }] });
    await flush();
    expect($$("[data-process]").map((p) => p.getAttribute("data-process"))).toEqual(["node", "claude", "herdr", "personad"]);
    expect($("[data-testid=load-definition]")).toBeNull(); // only the load alert explains load
  });

  it("says what load counts on a load alert, with the chip's per-core figure (#12519)", async () => {
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => json({ machine_id: "laptop", at: 1, processes });
    render({ kind: "load", alerts: [{ kind: "load", severity: "warning", value: 0.4 }] });
    await flush();
    // 4 over 10 cores: one decimal, as the chip and the card write it (the sheet said 0.40)
    expect($("[data-testid=pressure-figures]")?.textContent).toContain("每核 0.4");
    expect($("[data-testid=pressure-figures]")?.textContent).not.toContain("0.40");
    expect($("[data-testid=load-definition]")?.textContent).toMatch(/^负载指过去 1 分钟内正在运行及等待 CPU 的平均线程数/);
  });

  it("stops only the idle Agents, after a confirmation that lists them, one at a time, and says how it went", async () => {
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => json({ machine_id: "laptop", at: 1, processes });
    routes["POST /api/v1/sessions/laptop/i1/close"] = () => json({ ok: true });
    routes["POST /api/v1/sessions/laptop/u1/close"] = () => json({ error: "the node refused" }, 409);
    render();
    await flush();
    await click($("[data-testid=stop-idle]"));
    expect($$("[data-testid=stop-list] li")).toHaveLength(2);
    expect($("[data-testid=stop-list]")?.textContent).toContain("Draft the email");
    expect($("[data-testid=stop-list]")?.textContent).toContain("Release notes");
    expect($("[data-testid=stop-list]")?.textContent).not.toContain("Build the widget");
    expect($("[data-testid=stop-list]")?.textContent).not.toContain("Answer the question");
    expect(posts("/close")).toHaveLength(0);
    await click($("[data-testid=stop-confirm]"));
    expect(posts("/close").map((c) => c.path)).toEqual(["/api/v1/sessions/laptop/i1/close", "/api/v1/sessions/laptop/u1/close"]);
    const outcome = $("[data-testid=stop-outcome]")?.textContent ?? "";
    expect(outcome).toContain("已停止 1 / 2 个");
    expect(outcome).toContain("无法停止「Release notes」：the node refused");
  });

  it("says when the computer's Persona is too old to tell, offers Redeploy, and keeps the Agents and Stop working", async () => {
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => json({ error: "old", code: "node_outdated" }, 409);
    const kinds: AlertKind[] = [];
    render({ onKind: (k) => kinds.push(k) });
    await flush();
    const note = $("[data-diagnosis=node_outdated]");
    expect(note?.textContent).toContain("更新这台电脑上的 Persona");
    await click([...(note?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.includes("重新部署")) ?? null);
    expect(kinds).toEqual(["mismatch"]);
    expect($("[data-group=idle]")).not.toBeNull();
    expect(($("[data-testid=stop-idle]") as HTMLButtonElement).disabled).toBe(false);
  });

  it("says when the hub is too old, or the device isn't fully trusted", async () => {
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => new Response("404 page not found", { status: 404 });
    render();
    await flush();
    expect($("[data-diagnosis=hub_outdated]")?.textContent).toContain("更新 hub");
    expect($("[data-group=working]")).not.toBeNull();
    act(() => root.unmount());
    root = createRoot(host);
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => json({ error: "forbidden", code: "forbidden" }, 403);
    render();
    await flush();
    expect($("[data-diagnosis=forbidden]")?.textContent).toContain("完全信任的设备");
  });

  it("retries a failed read", async () => {
    let n = 0;
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => (n++ ? json({ machine_id: "laptop", at: 1, processes }) : json({ error: "deadline", code: "deadline" }, 504));
    render();
    await flush();
    await click([...($("[data-diagnosis=deadline]")?.querySelectorAll("button") ?? [])][0] ?? null);
    expect($$("[data-process]")).toHaveLength(4);
  });
});

describe("cache recovery on a computer without a usage map", () => {
  const disk = (caches: { id: string; path: string; bytes: number; in_use: boolean }[]) => ({
    machine_id: "laptop", at: 1,
    disk: { volumes: [{ mount: "/", used: 450 * GB, total: 500 * GB }], folders: [{ path: "~/code", bytes: 120 * GB }], partial: true, caches },
  });

  it("picks the caches nothing uses, can't pick one in use, cleans after a confirmation naming each, and shows what was freed", async () => {
    let reads = 0;
    routes["GET /api/v1/machines/laptop/diagnosis"] = () => {
      reads++;
      return json(disk(reads === 1 ? [
        { id: "go-build", path: "~/.cache/go-build", bytes: 6 * GB, in_use: true },
        { id: "npm-cache", path: "~/.npm/_cacache", bytes: 2 * GB, in_use: false },
        { id: "xcode-derived-data", path: "~/Library/Developer/Xcode/DerivedData", bytes: 3 * GB, in_use: false },
      ] : [{ id: "go-build", path: "~/.cache/go-build", bytes: 6 * GB, in_use: true }]));
    };
    routes["POST /api/v1/machines/laptop/cleanup"] = () => json({ freed_bytes: 4.8 * GB, items: [{ id: "npm-cache", bytes: 2 * GB, status: "cleaned" }, { id: "xcode-derived-data", bytes: 3 * GB, status: "in_use" }] });
    routes["GET /api/v1/machines/laptop/storage/usage"] = () => json({ code: "node_outdated" }, 409);
    const { UsageMap } = await import("./UsageMap");
    await act(async () => root.render(<UsageMap machine={laptop} focus={{ mode: "reclaim", seq: 1 }} />));
    await flush();
    expect($("[data-testid=folders]")?.textContent).toContain("~/code");
    expect($("[data-testid=folders-partial]")).not.toBeNull();
    const box = (id: string) => $(`[data-cache=${id}] input`) as HTMLInputElement;
    expect(box("go-build").disabled).toBe(true);
    expect(box("go-build").checked).toBe(false);
    expect(box("npm-cache").checked).toBe(true);
    expect($("[data-cache=xcode-derived-data]")?.textContent).toContain("Xcode 构建数据");
    expect($("[data-cache=xcode-derived-data]")?.textContent).toContain("~/Library/Developer/Xcode/DerivedData");
    await click($("[data-testid=clean-selected]"));
    const list = $("[data-testid=clean-list]")?.textContent ?? "";
    expect(list).toContain("npm 缓存");
    expect(list).toContain("~/.npm/_cacache");
    expect(list).not.toContain("go-build");
    expect($$("[role=dialog] h2").at(-1)?.textContent).toBe("清理这些缓存？");
    expect(posts("/cleanup")).toHaveLength(0);
    await click($("[data-testid=clean-confirm]"));
    expect(posts("/cleanup").map((c) => c.body)).toEqual([{ ids: ["npm-cache", "xcode-derived-data"] }]);
    const result = $("[data-testid=cleanup-result]")?.textContent ?? "";
    expect(result).toContain("已释放 4.80 GB 磁盘空间");
    expect($("[data-item=xcode-derived-data]")?.getAttribute("data-status")).toBe("in_use");
    expect(reads).toBe(2); // read again after the cleanup
  });
});

describe("the version sheet", () => {
  const studio: Machine = { id: "studio", name: "Studio Mac", online: true, kind: "ssh", can_deploy: true, node_mismatch: true, node_version: "v1-202611110815" };

  it("shows both builds and how far apart they are", async () => {
    render({ machine: studio, kind: "mismatch", alerts: [{ kind: "mismatch", severity: "info" }], agents: [agent("w1", "Build", "working", { machine_id: "studio" })] });
    await flush();
    expect($("[data-testid=builds]")?.textContent).toContain("v1-202611141020");
    expect($("[data-testid=builds]")?.textContent).toContain("v1-202611110815");
    expect($("[data-testid=build-gap]")?.textContent).toBe("这台电脑的版本比 hub 早 3 天。");
    expect($("[data-testid=working-count]")?.textContent).toContain("1 个 Agent 正在工作");
  });

  it("redeploys only after a confirmation, follows the pushed phases, and offers Try again after a failure", async () => {
    routes["POST /api/v1/machines/studio/deploy"] = () => json({ ok: true });
    const show = (m: Machine) => render({ machine: m, kind: "mismatch", alerts: [{ kind: "mismatch", severity: "info" }], agents: [] });
    show(studio);
    await flush();
    await click($("[data-testid=redeploy]"));
    expect($$("[role=dialog] h2").at(-1)?.textContent).toContain("重新部署 Studio Mac");
    expect(posts("/deploy")).toHaveLength(0);
    await click($("[data-testid=redeploy-confirm]"));
    expect(posts("/deploy")).toHaveLength(1);
    const state = (step: string) => $(`[data-testid=deploy-stepper] [data-step=${step}]`)?.getAttribute("data-state");
    show({ ...studio, deploying: true, deploy_phase: "waiting_idle" });
    await flush();
    expect(state("waiting_idle")).toBe("current");
    show({ ...studio, deploying: true, deploy_phase: "transferring" });
    await flush();
    expect(state("waiting_idle")).toBe("done");
    expect(state("transferring")).toBe("current");
    expect(state("restarting")).toBe("todo");
    show({ ...studio, deploy_error: "copy failed: no space left" });
    await flush();
    expect(state("transferring")).toBe("failed");
    expect($("[data-testid=deploy-failed]")?.textContent).toContain("copy failed: no space left");
    expect($("[data-testid=redeploy]")?.textContent).toBe("重试");
    await click($("[data-testid=redeploy]"));
    await click($("[data-testid=redeploy-confirm]"));
    expect(posts("/deploy")).toHaveLength(2);
    show({ ...studio, deploying: true, deploy_phase: "draining", deploy_error: undefined });
    await flush();
    expect(state("transferring")).toBe("done");
    expect(state("draining")).toBe("current");
    expect(state("restarting")).toBe("todo");
    expect($("[data-testid=deploy-draining-note]")?.textContent).toContain("正在等待当前任务完成后重启");
    show({ ...studio, deploying: true, deploy_phase: "confirming", deploy_error: undefined });
    await flush();
    expect(state("draining")).toBe("done");
    expect(state("confirming")).toBe("current");
    expect($("[data-testid=deploy-draining-note]")).toBeNull();
    show({ ...studio, node_mismatch: false, node_version: "v1-202611141020" });
    await flush();
    expect($("[data-testid=deploy-done]")?.textContent).toContain("v1-202611141020");
    expect(state("done")).toBe("done");
  });

  it("withdraws an open Redeploy confirmation when the computer starts updating or draining elsewhere", async () => {
    const show = (machine: Machine) => render({ machine, kind: "mismatch", agents: [] });
    for (const phase of ["draining", "restarting"] as const) {
      show(studio);
      await flush();
      await click($("[data-testid=redeploy]"));
      const confirm = $("[data-testid=redeploy-confirm]") as HTMLButtonElement;
      expect(confirm.disabled).toBe(false);

      show({ ...studio, deploy_phase: phase });
      await flush();
      expect($(`[data-step="${phase}"]`)?.getAttribute("data-state")).toBe("current");
      expect(confirm.isConnected && !confirm.disabled).toBe(false);
      await act(async () => { confirm.click(); });
      expect(posts("/deploy")).toHaveLength(0);
    }
  });

  it("gives the hub's reason when it can't update the computer", async () => {
    render({ machine: { ...studio, kind: "link", can_deploy: false, deploy_reason: "Update this computer's Persona Link first." }, kind: "mismatch", alerts: [] });
    await flush();
    expect($("[data-testid=deploy-reason]")?.textContent).toBe("Update this computer's Persona Link first.");
    expect($("[data-testid=redeploy]")).toBeNull();
  });

  it("says when the hub's build isn't known", async () => {
    hub.version = "";
    render({ machine: studio, kind: "mismatch", alerts: [] });
    await flush();
    hub.version = "v1-202611141020";
    expect($("[data-testid=build-gap]")?.textContent).toContain("无法获取 hub 版本");
  });

  it("shows co-located managers, restart notes in Redeploy confirmation, and pending_restart after deploy", async () => {
    const withUid: Machine = { ...studio, machine_uid: "uid-studio", hostname: "studio.local" };
    setRoster({
      default: "m-1",
      managers: [
        { id: "m-1", name: "Studio 助手", online: true, machine_uid: "uid-studio", host: "studio.local", deploy: "service", version: "v1-202611100800" },
      ],
    });
    routes["POST /api/v1/machines/studio/deploy"] = () => json({ ok: true });
    render({ machine: withUid, kind: "mismatch", alerts: [{ kind: "mismatch", severity: "info" }], agents: [] });
    await flush();

    expect($("[data-testid=deploy-hosted-managers]")?.textContent).toContain("Studio 助手");
    expect($("[data-testid=deploy-hosted-managers]")?.textContent).toContain("版本落后");
    expect($("[data-testid=deploy-hosted-why]")?.textContent).toContain("Studio 助手");

    await click($("[data-testid=redeploy]"));
    expect($("[data-testid=redeploy-confirm-managers]")?.textContent).toContain("Studio 助手");
    await click($("[data-testid=redeploy-confirm]"));

    act(() => setRoster({
      default: "m-1",
      managers: [
        { id: "m-1", name: "Studio 助手", online: true, machine_uid: "uid-studio", host: "studio.local", deploy: "service", version: "v1-202611100800", pending_restart: true },
      ],
    }));
    render({ machine: { ...withUid, node_mismatch: false, node_version: "v1-202611141020" }, kind: "mismatch", alerts: [], agents: [] });
    await flush();
    expect($("[data-testid=deploy-done-pending-restart]")?.textContent).toContain("Studio 助手");
  });
});

describe("the offline sheet", () => {
  it("says why with a numbered checklist, keeps the raw error under Details, and reconnects", async () => {
    const reconnect = vi.fn();
    const nas: Machine = { id: "nas", name: "Garage NAS", online: false, kind: "ssh", error: "ssh: connect to host nas port 22: Operation timed out", last_seen: new Date(Date.now() - 3 * 3600_000).toISOString() };
    render({ machine: nas, kind: "offline", alerts: [{ kind: "offline", severity: "critical", reason: "timeout" }], onReconnect: reconnect });
    await flush();
    expect($("[data-reason]")?.getAttribute("data-reason")).toBe("timeout");
    expect($("[data-reason]")?.textContent).toBe("连接超时");
    expect($$("[data-testid=offline-checklist] li")).toHaveLength(3);
    expect(document.body.textContent).toContain("最后在线：3 小时前");
    expect($("[data-testid=raw-error]")?.textContent).toContain("Operation timed out");
    expect($("[data-testid=raw-error]")?.closest("[inert]")).not.toBeNull(); // folded until Details
    await click($("[data-testid=reconnect]"));
    expect(reconnect).toHaveBeenCalledWith("nas");
  });

  it("offers Redeploy for a version mismatch the hub can fix", async () => {
    const kinds: AlertKind[] = [];
    const old: Machine = { id: "old", name: "Old", online: false, kind: "ssh", error: "protocol version mismatch", node_mismatch: true };
    render({ machine: old, kind: "offline", alerts: [], onKind: (k) => kinds.push(k) });
    await flush();
    expect($$("[data-testid=offline-checklist] li")[0]?.textContent).toContain("不兼容");
    await click($("[data-testid=offline-redeploy]"));
    expect(kinds).toEqual(["mismatch"]);
  });
});

describe("the launcher sheet", () => {
  const records: LauncherHealthRecord[] = [
    { machine: "laptop", launcher: "codex", state: "failing", kind: "usage_limit", error: "You've hit your usage limit.", checked_at: new Date().toISOString(), fails: 3, resets_at: new Date(Date.now() + 3600_000).toISOString() },
    { machine: "laptop", launcher: "team", state: "blocked", kind: "auth", error: "401 invalid x-api-key", checked_at: new Date().toISOString(), fails: 2 },
    { machine: "other", launcher: "claude", state: "blocked", kind: "auth" },
  ];

  it("lists each failing launcher, the ones that work, and tests one again", async () => {
    routes["GET /api/v1/machines/laptop/launchers"] = () => json({ launchers: [{ name: "claude" }, { name: "team", display_name: "Team relay" }, { name: "codex", agent: "codex" }] });
    routes["POST /api/v1/launchers/health/check"] = () => json({ records: [{ machine: "laptop", launcher: "team", state: "ok", checked_at: new Date().toISOString() }] });
    const got: LauncherHealthRecord[][] = [];
    render({ kind: "launcher", alerts: [{ kind: "launcher", severity: "warning", count: 2 }], records, onRecords: (r) => got.push(r) });
    await flush();
    expect($$("[data-testid=failing-launchers] [data-launcher]").map((l) => l.getAttribute("data-launcher"))).toEqual(["codex", "team"]);
    expect($("[data-launcher=codex]")?.textContent).toContain("已达到订阅用量上限");
    expect($("[data-launcher=codex]")?.textContent).toContain("连续失败 3 次");
    expect($("[data-launcher=team]")?.textContent).toContain("Team relay");
    expect($("[data-launcher=team]")?.textContent).toContain("401 invalid x-api-key");
    expect($("[data-testid=healthy-launchers]")?.textContent).toBe("Claude Code");
    await click($("[data-launcher=team] [data-testid=test-again]"));
    expect(posts("/launchers/health/check").map((c) => c.path)).toEqual(["/api/v1/launchers/health/check?machine=laptop&launcher=team"]);
    expect(got).toEqual([[expect.objectContaining({ launcher: "team", state: "ok" })]]);
    expect($("[data-launcher=team]")?.textContent).toContain("已恢复可用");
  });

  it("reads the records again when a check answers none for the launcher (#14534)", async () => {
    routes["GET /api/v1/machines/laptop/launchers"] = () => json({ launchers: [{ name: "claude" }] });
    routes["POST /api/v1/launchers/health/check"] = () => json({ records: [] });
    let reloads = 0;
    render({ kind: "launcher", alerts: [{ kind: "launcher", severity: "warning", count: 2 }], records, onReloadRecords: () => reloads++ });
    await flush();
    expect(reloads).toBe(0);
    await click($("[data-launcher=team] [data-testid=test-again]"));
    expect(reloads).toBe(1);
    expect($("[data-launcher=team]")?.textContent).toContain("本次检查未返回结果");
  });
});
