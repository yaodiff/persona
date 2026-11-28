// @vitest-environment jsdom
// Machines: a refresh that fails after a good one keeps the last cards up and says so in red; a hub with no
// machines shows the empty state, and one that cannot be reached at all says it cannot see them.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine, MachineMetrics } from "../lib/types";
import { chooseManager, resetManagerChoice } from "../lib/manager-choice";
import { currentRoster, resetRoster, setRoster } from "../lib/managers";
import { navigate } from "../lib/router";
import { getState } from "../lib/store";
import { t } from "../i18n";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  HTMLElement.prototype.scrollTo ??= () => {};
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const fake = vi.hoisted(() => ({ machines: [] as Machine[], features: [] as string[], loaded: true }));
vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: fake.loaded, machines: fake.machines, features: fake.features }) };
});

const { Machines } = await import("./Machines");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let replies: number[] = [];
let posts: string[] = [];
let metricsReplies: Record<string, MachineMetrics[] | number> = {};
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const path = String(input).split("?")[0]!;
  if (init?.method === "POST") posts.push(path);
  if (path.endsWith("/confirm") || path.endsWith("/reject")) return Promise.resolve(json({ ok: true }));
  if (path.endsWith("/reconnect")) return Promise.resolve(json({ ok: true }));
  if (path.endsWith("/deploy")) return Promise.resolve(json({ ok: true }));
  if (path === "/api/v1/managers" && currentRoster()) return Promise.resolve(json(currentRoster()));
  if (path !== "/api/v1/metrics") return Promise.resolve(json({ error: "not found" }, 404));
  const reply = metricsReplies[String(input)];
  const status = typeof reply === "number" ? reply : replies.length > 1 ? replies.shift()! : replies[0] ?? 200;
  return Promise.resolve(status === 200 ? json({ machines: Array.isArray(reply) ? reply : [] }) : json({ error: "unavailable" }, status));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const alert = () => host.querySelector('[role="alert"]');
const buttons = (label: string) => [...host.querySelectorAll("button")].filter((b) => b.textContent?.trim() === label);

beforeEach(() => {
  resetRoster();
  resetManagerChoice();
  metricsReplies = {};
  localStorage.removeItem("persona.machines.view");
  fake.machines = [];
  fake.features = [];
  fake.loaded = true;
  replies = [];
  posts = [];
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
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("Machines", () => {
  it("puts cards first, assistant resources last and fleet telemetry on the overview", async () => {
    fake.machines = ["a", "b"].map(id => ({ id, name: id, online: true }));
    metricsReplies["/api/v1/metrics"] = [{
      machine_id: "a", online: true, points: [], usage: { today: { in: 12 }, per_minute: 2, recent: [] },
    }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const views = [...host.querySelectorAll<HTMLButtonElement>('[role="radiogroup"] [role="radio"]')];
    const tokens = () => host.querySelector('[data-testid="machines-tokens"]');
    const hint = t("machines.refresh_interval", { seconds: 10 });
    expect(views[0]?.textContent).toBe(t("machines.view_cards"));
    expect(views.at(-1)?.textContent).toBe(t("resources.assistant_resources"));
    expect(tokens()).not.toBeNull();
    expect(host.textContent).toContain(hint);
    for (const view of views.slice(1)) {
      await act(async () => view.click());
      expect(Boolean(tokens())).toBe(view.textContent === t("machines.view_capabilities"));
      expect(host.textContent).not.toContain(hint);
    }
    await flush();
  });

  const link = (path: string) => host.querySelector<HTMLAnchorElement>(`a[href="${path}"]`);
  const show = async () => {
    await act(async () => root.render(<Machines onToggleSidebar={() => {}} />));
    await act(async () => { await vi.dynamicImportSettled(); });
  };
  const follow = async (path: string) => {
    expect(link(path)).toBeTruthy();
    await act(async () => link(path)!.click());
    await act(async () => { await vi.dynamicImportSettled(); });
    expect(location.pathname).toBe(path);
  };

  it("opens every tool on B, restores a bookmark and retains an offline target", async () => {
    fake.machines = ["a", "b"].map(id => ({ id, name: id, online: true }));
    await show();
    await follow("/machines/b");
    const rail = host.querySelector(`nav[aria-label="${t("machines.title")}"]`)!;
    expect(rail.querySelector('a[aria-current="page"]')?.getAttribute("href")).toBe("/machines/b");
    expect(rail.querySelector('a[href="/machines/a"]')).not.toBeNull();
    for (const view of ["files", "storage", "diagnostics", "services", "network", "toolchain", "procs"]) {
      await follow("/machines/b/" + view);
      expect(host.querySelector("h1")?.textContent).toBe("b");
    }
    await follow("/machines/a/procs");
    await act(async () => {
      const popped = new Promise(resolve => window.addEventListener("popstate", resolve, { once: true }));
      history.back();
      await popped;
    });
    expect(location.pathname).toBe("/machines/b/procs");
    expect(host.querySelector("h1")?.textContent).toBe("b");
    await act(async () => root.render(null));
    fake.machines[1] = { ...fake.machines[1]!, online: false };
    await show();
    expect(host.querySelector("h1")?.textContent).toBe("b");
    expect(host.querySelector("[data-offline-box]")).not.toBeNull();
    const reads = vi.mocked(fetch).mock.calls.map(([url]) => String(url));
    expect(reads).toContain("/api/v1/machines/b/storage");
    expect(reads).not.toContain("/api/v1/machines/a/storage");
  });

  it("keeps computer actions in the header on every route and distinguishes tool navigation", async () => {
    fake.machines = [{ id: "b", name: "Computer B", online: true }];
    fake.features = ["terminal_panes"];
    history.replaceState(null, "", "/machines/b");
    await show();
    const header = host.querySelector("section h2")!.closest("section")!;
    const actions = () => [...header.querySelectorAll("button")].map(button => button.textContent);
    for (const view of ["work", "monitoring", "files", "storage", "diagnostics", "services", "management"]) {
      const path = view === "work" ? "/machines/b" : "/machines/b/" + view;
      if (view !== "work") await follow(path);
      expect(actions()).toEqual([t("space-page.new_agent"), t("sidebar.nav_terminal")]);
      expect(header.querySelector('a[href="/machines/b/files"]')).toBeNull();
      const nav = host.querySelector(`nav[aria-label="${t("machines.view_label")}"]`)!;
      const active = nav.querySelector('a[aria-current="page"]')!;
      expect(active.getAttribute("href")).toBe(view === "services" ? "/machines/b/diagnostics" : path);
      expect(header.nextElementSibling?.contains(nav)).toBe(true);
      expect(nav.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
      const tools = host.querySelector(`nav[aria-label="${t("machines.view_diagnostics")}"]`);
      if (view === "services" || view === "diagnostics") {
        const pill = tools!.querySelector('a[aria-current="page"]')!;
        expect(pill.getAttribute("href")).toBe(path);
        expect(nav.compareDocumentPosition(tools!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(tools!.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
      } else expect(tools).toBeNull();
    }
    fake.features = [];
    await show();
    expect(actions()).toEqual([t("space-page.new_agent")]);
    fake.machines = [{ ...fake.machines[0]!, online: false }];
    await show();
    expect(actions()).toEqual([t("machines.reconnect")]);
  });

  it("restores the fleet tab and scroll offset when leaving detail through Back", async () => {
    fake.machines = ["a", "b"].map(id => ({ id, name: id, online: true }));
    await show();
    await act(async () => buttons(t("machines.view_capabilities"))[0]!.click());
    const scroller = () => host.querySelector<HTMLDivElement>(".overflow-y-auto")!;
    scroller().scrollTop = 175;
    scroller().dispatchEvent(new Event("scroll"));
    await follow("/machines/b");
    await act(async () => {
      const popped = new Promise(resolve => window.addEventListener("popstate", resolve, { once: true }));
      host.querySelector<HTMLAnchorElement>("header a")!.click();
      await popped;
    });
    expect(location.pathname).toBe("/machines");
    expect(scroller().scrollTop).toBe(175);
    expect(host.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe(t("machines.view_capabilities"));
  });

  it("keeps Overview as the previous page and restores an old engine bookmark", async () => {
    fake.machines = [{ id: "b", name: "Computer B", online: true }];
    history.replaceState(null, "", "/overview");
    navigate({ name: "machine", mid: "b" });
    await show();
    expect(host.querySelector("header a")?.getAttribute("href")).toBe("/overview");
    await act(async () => root.render(null));
    history.replaceState(null, "", "/machines/b#engine");
    await show();
    expect(location.pathname).toBe("/machines/b/management");
    expect(host.querySelector("#engine")).not.toBeNull();
  });

  it("waits for the directory and reports a refused tool request on the requested machine", async () => {
    fake.loaded = false;
    history.replaceState(null, "", "/machines/b/network");
    await show();
    expect(host.textContent).not.toContain(t("machines-machinedetail.not_found"));
    expect(host.querySelector('[data-testid="network-panel"]')).toBeNull();
    fake.loaded = true;
    fake.machines = [{ id: "b", name: "Computer B", online: true }];
    vi.mocked(fetch).mockImplementation(async input => String(input).endsWith("/b/network") ? json({ error: "Forbidden" }, 403) : hub(input));
    await show();
    expect(host.querySelector('[data-testid="network-panel"]')?.textContent).toContain(t("machines-network.forbidden"));
    expect(host.querySelector("h1")?.textContent).toBe("Computer B");
  });

  it("replaces a legacy reclaim entry with the selected computer's shareable URL", async () => {
    fake.machines = ["a", "b"].map(id => ({ id, name: id, online: true }));
    history.replaceState({ usage_focus: { mid: "b", mode: "reclaim" } }, "", "/machines");
    await show();
    expect(location.pathname + location.search).toBe("/machines/b/storage?mode=reclaim");
    expect(host.querySelector('[data-testid="storage-panel"]')).not.toBeNull();
    expect(history.state.usage_focus).toBeUndefined();
  });

  it("shows a single actionable card and keeps a missing direct target unavailable", async () => {
    fake.machines = [{ id: "a", name: "Computer A", online: true }];
    await show();
    expect(link("/machines/a")).toBeTruthy();
    expect(buttons(t("machines.view_capabilities"))).toHaveLength(0);
    const actions = buttons(t("space-page.new_agent"))[0]!.parentElement!;
    for (const view of ["files", "storage"]) {
      const shortcut = link("/machines/a/" + view)!;
      expect(shortcut.querySelector("svg")).not.toBeNull();
      expect(actions.contains(shortcut)).toBe(true);
      expect(shortcut.textContent).toBe(t(view === "files" ? "machines.view_files" : "machines.view_storage"));
    }
    await follow("/machines/a/files");
    expect(host.querySelector(`nav[aria-label="${t("machines.title")}"]`)).toBeNull();
    await act(async () => navigate({ name: "machine", mid: "missing", subview: "storage" }));
    await show();
    expect(host.textContent).toContain(t("machines-machinedetail.not_found"));
    expect(host.querySelector('[data-testid="storage-panel"]')).toBeNull();
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).includes("/missing/"))).toBe(false);
  });

  it("keeps the last cards and says so in red when a refresh fails", async () => {
    fake.machines = [{ id: "mac", name: "电脑 A", online: true }];
    replies = [200, 503];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(alert()).toBeNull();
    expect(host.textContent).toContain("电脑 A");
    await act(async () => { vi.advanceTimersByTime(10_000); });
    await flush();
    expect(alert()?.textContent).toContain("当前显示上次的数据");
    expect(host.textContent).toContain("电脑 A");
    expect(host.textContent).not.toContain("尚未添加电脑");
  });

  it("shows the empty state when the hub has no machines", async () => {
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).toContain("尚未添加电脑");
    expect(alert()).toBeNull();
  });

  // #5494: the grid has three columns from 2xl; md:col-span-2 alone left a full-row item two cells and put the next card beside it.
  const fullRow = (el: Element | null | undefined) => {
    expect(el?.classList.contains("col-span-full")).toBe(true);
    expect(el?.parentElement?.className).toContain("2xl:grid-cols-3");
  };

  it("spans every column with the error notice and the empty state", async () => {
    replies = [503];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    fullRow(alert());
    const empty = [...host.querySelectorAll("div")].find((d) => d.parentElement?.className.includes("2xl:grid-cols-3") && d.textContent?.includes("无法查看"));
    fullRow(empty);
    expect(host.innerHTML).not.toMatch(/(?<![\w:-])md:col-span-2/);
  });

  it("calls the offline card's button 重新连接, as the machine's page does (#3634)", async () => {
    fake.machines = [{ id: "mac", name: "电脑 A", online: false }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const labels = [...host.querySelectorAll("button")].map((b) => b.textContent?.trim());
    expect(labels).toContain("重新连接");
    expect(labels).not.toContain("重连");
  });

  // #3578: on a phone the card's "Details" link alone was a 48 × 17 target; a tap anywhere on the card opens the machine.
  const click = (el: Element) => act(() => void el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })));
  const card = () => host.querySelector("section")!;

  it("opens the machine from a tap on the card's blank space", async () => {
    fake.machines = [{ id: "mac", name: "电脑 A", online: true }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const cpu = [...card().querySelectorAll("div")].find((d) => d.textContent === "CPU")!;
    expect(cpu.closest("a, button")).toBeNull(); // really blank space, not inside a link
    click(cpu);
    expect(location.pathname).toBe("/machines/mac");
  });

  it("keeps 重新连接 its own: it reconnects and does not open the machine", async () => {
    fake.machines = [{ id: "mac", name: "电脑 A", online: false }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const button = [...card().querySelectorAll("button")].find((b) => b.textContent?.trim() === "重新连接")!;
    click(button);
    await flush();
    expect(location.pathname).toBe("/machines");
    expect(vi.mocked(fetch).mock.calls.map(([u]) => String(u))).toContain("/api/v1/machines/mac/reconnect");
  });

  it("names every active phase and disables Redeploy on that card until the update ends", async () => {
    const node: Machine = { id: "mac", name: "Example computer", online: true, can_deploy: true, node_mismatch: true };
    const show = (deploy_phase?: Machine["deploy_phase"]) => {
      fake.machines = [{ ...node, deploy_phase }];
      act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    };
    show();
    await vi.waitFor(async () => {
      await flush();
      expect(buttons(t("lib-machine-deploy.redeploy"))[0]?.disabled).toBe(false);
    });
    for (const phase of ["waiting_idle", "transferring", "draining", "restarting", "confirming"] as const) {
      show(phase); // phase alone is sufficient, including when an older hub omits `deploying`
      const [redeploy] = buttons(t(`lib-machine-deploy.${phase}`));
      expect(redeploy?.disabled).toBe(true);
      redeploy!.click();
      expect(posts).toEqual([]);
      expect(location.pathname).toBe("/machines");
    }
    show();
    expect(buttons(t("lib-machine-deploy.redeploy"))[0]?.disabled).toBe(false);
  });

  it("admits one Redeploy request even when two clicks arrive before the card renders", async () => {
    fake.machines = [{ id: "mac", name: "Example computer", online: true, node_mismatch: true, can_deploy: true }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const [redeploy] = buttons(t("lib-machine-deploy.redeploy"));
    expect(redeploy?.disabled).toBe(false);
    act(() => { redeploy!.click(); redeploy!.click(); });
    await flush();
    expect(posts).toEqual(["/api/v1/machines/mac/deploy"]);
  });

  it("checks the hub's current phase before posting from a card that still shows Redeploy", async () => {
    const node: Machine = { id: "mac", name: "Example computer", online: true, node_mismatch: true, can_deploy: true };
    fake.machines = [node];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const [redeploy] = buttons(t("lib-machine-deploy.redeploy"));
    expect(redeploy?.disabled).toBe(false);
    const before = getState().machines;
    try {
      getState().machines = [{ ...node, deploy_phase: "transferring" }];
      act(() => redeploy!.click());
      await flush();
      expect(posts).toEqual([]);
    } finally {
      getState().machines = before;
    }
  });

  it("does not open the machine while text on the card is being selected", async () => {
    fake.machines = [{ id: "mac", name: "电脑 A", online: true }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const range = document.createRange();
    range.selectNodeContents(card().querySelector("h2")!);
    getSelection()!.addRange(range);
    click(card());
    getSelection()!.removeAllRanges();
    expect(location.pathname).toBe("/machines");
  });

  it("leaves the card a plain region: keyboards reach the title link, and no control sits in another", async () => {
    fake.machines = [{ id: "mac", name: "电脑 A", online: false }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const c = card();
    expect(c.getAttribute("role")).toBeNull();
    expect(c.hasAttribute("tabindex")).toBe(false);
    expect(c.querySelector("a a, a button, button a, button button")).toBeNull();
    const first = c.querySelector<HTMLAnchorElement>("a[href]")!;
    expect(first.getAttribute("href")).toBe("/machines/mac");
    expect(first.textContent).toContain("电脑 A");
  });

  it("says it cannot see the machines when the first load fails", async () => {
    replies = [503];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).toContain("当前无法查看机器");
    expect(alert()).not.toBeNull();
    expect(alert()?.textContent).not.toContain("当前显示上次的数据");
  });

  it("offers 添加电脑 in the empty state, and in the header only when the hub can add one (machines_add)", async () => {
    // Old hubs: the empty state's button opens the how-to instead of the in-app flow.
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(buttons("添加电脑")).toHaveLength(1);
    expect(host.textContent).not.toContain("hub 连上后");

    fake.features = ["machine_trust", "machines_add"];
    act(() => root.render(<Machines onToggleSidebar={() => {}} key="new" />));
    await flush();
    expect(buttons("添加电脑")).toHaveLength(1); // the empty state's; the header has none while the page is empty
    expect(host.textContent).toContain("添加一台电脑后，Persona 就能在上面为你执行任务");

    fake.machines = [{ id: "mac", name: "电脑 A", online: true }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} key="one" />));
    await flush();
    expect(buttons("添加电脑")).toHaveLength(1); // the header's
  });

  it("asks about an online pending computer on its card and confirms it", async () => {
    fake.features = ["machine_trust", "machines_add"];
    fake.machines = [{ id: "home", name: "家里的服务器", online: true, trust: "pending", error: "等你在新版 App 或网页里确认这是你的电脑" }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).toContain("一台电脑请求加入：家里的服务器，这是你的电脑吗？");
    expect(host.textContent).not.toContain("等你在新版 App"); // the hub's words for old apps
    await act(async () => buttons("是我的电脑")[0]!.click());
    await flush();
    expect(posts).toEqual(["/api/v1/machines/home/confirm"]);
  });

  it("gives a pending computer that never connected a new link and no 重新连接", async () => {
    fake.features = ["machine_trust", "machines_add"];
    fake.machines = [{ id: "home", name: "家里的服务器", online: false, kind: "link", trust: "pending", error_code: "never_connected", error: "等你确认" }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).toContain("等待这台电脑通过配对链接接入");
    expect(buttons("重新生成链接")).toHaveLength(1);
    expect(buttons("移除")).toHaveLength(1);
    expect(buttons("重新连接")).toHaveLength(0);
  });

  it("says a machine's error_code in words instead of its raw error", async () => {
    fake.machines = [{ id: "mac", name: "电脑 A", online: false, kind: "link", error_code: "link_offline", error: "link closed: failed to get reader" }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector('[data-error-code="link_offline"]')?.textContent).toContain("Persona Link 未运行");
    expect(host.textContent).not.toContain("failed to get reader");
  });
});

describe("Machines metrics across managers", () => {
  const roster = {
    default: "assistant",
    managers: [
      { id: "m-1", name: "Other assistant", online: true },
      { id: "assistant", name: "Default assistant", online: true },
    ],
  };
  const first: MachineMetrics = { machine_id: "mac", online: true, points: [], latest: { t: 1, cpu: 11, mem_used: 25, mem_total: 100, net_rx: 1024, net_tx: 0 } };
  const second: MachineMetrics = { machine_id: "nas", online: true, points: [], latest: { t: 1, cpu: 62, mem_used: 50, mem_total: 100, net_rx: 2048, net_tx: 1024 } };
  const reads = () => vi.mocked(fetch).mock.calls.map(([input]) => String(input)).filter((url) => url.startsWith("/api/v1/metrics"));
  const showManager = async (id: string) => {
    act(() => chooseManager(id));
    await flush();
  };

  beforeEach(() => {
    setRoster(roster);
    fake.machines = [
      { id: "mac", name: "Computer A", online: true },
      { id: "nas", name: "Computer B", online: true, manager: "m-1" },
    ];
    metricsReplies = { "/api/v1/metrics": [first], "/api/v1/metrics?manager=m-1": [second] };
  });

  it("loads both managers' CPU, memory and network, keeping the first machine ID", async () => {
    metricsReplies["/api/v1/metrics?manager=m-1"] = [second, { ...first, latest: { t: 1, cpu: 99 } }];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    for (const value of ["11%", "25%", "↓ 1.00 KB/s"]) expect(host.textContent).toContain(value);
    expect(host.textContent).not.toContain("99%");
    await showManager("m-1");
    for (const value of ["62%", "50%", "↓ 2.00 KB/s"]) expect(host.textContent).toContain(value);
    expect(reads()).toEqual(["/api/v1/metrics", "/api/v1/metrics?manager=m-1"]);
    expect(alert()).toBeNull();
  });

  it.each(["assistant", "m-1"])("still loads the other manager when %s fails on the first load", async (failed) => {
    metricsReplies[failed === "assistant" ? "/api/v1/metrics" : "/api/v1/metrics?manager=m-1"] = 503;
    chooseManager(failed === "assistant" ? "m-1" : "assistant");
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    for (const value of failed === "assistant" ? ["62%", "50%", "↓ 2.00 KB/s"] : ["11%", "25%", "↓ 1.00 KB/s"]) expect(host.textContent).toContain(value);
    expect(alert()).toBeNull();
  });

  it("keeps a failed manager's numbers while refreshing the other, and reports only total failure", async () => {
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    metricsReplies["/api/v1/metrics"] = [{ ...first, latest: { ...first.latest!, cpu: 23 } }];
    metricsReplies["/api/v1/metrics?manager=m-1"] = 503;
    await act(async () => { vi.advanceTimersByTime(10_000); });
    await flush();
    expect(host.textContent).toContain("23%");
    await showManager("m-1");
    for (const value of ["62%", "50%", "↓ 2.00 KB/s"]) expect(host.textContent).toContain(value);
    expect(alert()).toBeNull();

    metricsReplies["/api/v1/metrics"] = 503;
    await act(async () => { vi.advanceTimersByTime(10_000); });
    await flush();
    expect(alert()?.textContent).toContain("当前显示上次的数据");
    expect(host.textContent).toContain("62%");
    await showManager("assistant");
    expect(host.textContent).toContain("23%");

    metricsReplies["/api/v1/metrics?manager=m-1"] = [];
    await act(async () => { vi.advanceTimersByTime(10_000); });
    await flush();
    expect(alert()).toBeNull();
    expect(host.textContent).toContain("23%");
    await showManager("m-1");
    expect(host.textContent).not.toContain("62%");
  });

  it("reads the latest roster on the next poll without polling on every roster push", async () => {
    setRoster({ ...roster, managers: roster.managers.map((m) => ({ ...m, online: m.id === "assistant" })) });
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(reads()).toEqual(["/api/v1/metrics"]);
    const next = { ...roster, managers: [...roster.managers, { id: "installing", name: "Installing", online: true, installing: "2099-01-01T00:00:00Z" }] };
    act(() => setRoster(next));
    act(() => setRoster({ ...next, managers: next.managers.map((m) => ({ ...m, inbox_needs_you: 1 })) }));
    await flush();
    expect(reads()).toHaveLength(1);
    await act(async () => { vi.advanceTimersByTime(10_000); });
    await flush();
    expect(reads()).toEqual(["/api/v1/metrics", "/api/v1/metrics", "/api/v1/metrics?manager=m-1"]);
    await showManager("m-1");
    expect(host.textContent).toContain("62%");
  });

  it("keeps a single bare request with one installed manager", async () => {
    setRoster({ default: "m-1", managers: [roster.managers[0], { id: "installing", name: "Installing", online: false, installing: "2099-01-01T00:00:00Z" }] });
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(reads()).toEqual(["/api/v1/metrics"]);
    expect(alert()).toBeNull();
  });

  it("does not request offline managers or report a failure when none is online", async () => {
    setRoster({ ...roster, managers: roster.managers.map((m) => ({ ...m, online: false })) });
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(reads()).toEqual([]);
    expect(alert()).toBeNull();
  });
});

// A hidden tab asks the hub nothing (the 10 s refresh of every card's numbers stops) and asks once on coming back.
describe("Machines in a hidden tab", () => {
  let visibility: DocumentVisibilityState = "visible";
  const show = (v: DocumentVisibilityState) => act(() => { visibility = v; document.dispatchEvent(new Event("visibilitychange")); });
  const metricsReads = () => vi.mocked(fetch).mock.calls.filter(([input]) => String(input).split("?")[0] === "/api/v1/metrics").length;

  beforeEach(() => {
    visibility = "visible";
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
  });
  afterEach(() => {
    delete (document as { visibilityState?: unknown }).visibilityState;
  });

  it("stops asking for /metrics while hidden and asks once when shown again", async () => {
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(metricsReads()).toBe(1);
    show("hidden");
    await act(async () => { vi.advanceTimersByTime(60_000); });
    await flush();
    expect(metricsReads()).toBe(1);
    show("visible");
    await flush();
    expect(metricsReads()).toBe(2);
    await act(async () => { vi.advanceTimersByTime(10_000); });
    await flush();
    expect(metricsReads()).toBe(3);
  });

  it("opened in a hidden tab, loads when the tab is shown", async () => {
    visibility = "hidden";
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await act(async () => { vi.advanceTimersByTime(60_000); });
    await flush();
    expect(metricsReads()).toBe(0);
    show("visible");
    await flush();
    expect(metricsReads()).toBe(1);
  });
});

describe("Machines today's tokens", () => {
  const usage = (machine_id: string, tokens: number): MachineMetrics => ({ machine_id, online: true, points: [], usage: { today: { in: tokens }, per_minute: 0, recent: [] } });
  // the share legend's rows, each its name and count, and the bar's segments
  const rows = () => [...host.querySelectorAll('[data-testid="machines-tokens"] .whitespace-nowrap')].map((r) => r.textContent);
  const segments = () => host.querySelector('[data-testid="machines-tokens"] div[aria-hidden]')!.children.length;

  it("keeps one row per computer when two share a name and the ranking changes", async () => {
    fake.machines = [
      { id: "a", name: "Mac", online: true },
      { id: "b", name: "Linux", online: true },
      { id: "c", name: "Mac", online: true },
    ];
    metricsReplies = { "/api/v1/metrics": [usage("a", 300), usage("b", 200), usage("c", 100)] };
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(rows()).toEqual(["Mac300", "Linux200", "Mac100"]);

    metricsReplies = { "/api/v1/metrics": [usage("a", 300), usage("b", 500), usage("c", 100)] };
    await act(async () => { vi.advanceTimersByTime(10_000); });
    await flush();
    expect(rows()).toEqual(["Linux500", "Mac300", "Mac100"]);
    expect(segments()).toBe(3);
  });
});

describe("Machines battery and temperature", () => {
  it("renders battery and temperature only when reported and omits placeholders when absent", async () => {
    fake.machines = [
      { id: "laptop", name: "Laptop", online: true },
      { id: "desktop", name: "Desktop", online: true },
    ];
    metricsReplies = {
      "/api/v1/metrics": [
        { machine_id: "laptop", online: true, points: [], latest: { t: 1000, battery_percent: 82, battery_charging: true, temp_c: 54.2 } },
        { machine_id: "desktop", online: true, points: [], latest: { t: 1000, cpu: 12 } },
      ],
    };
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const cards = [...host.querySelectorAll("section")];
    expect(cards.length).toBe(2);
    const laptopBattery = cards[0].querySelector('[data-testid="metric-battery"]');
    const laptopTemp = cards[0].querySelector('[data-testid="metric-temp"]');
    expect(laptopBattery?.textContent).toContain("82");
    expect(laptopBattery?.textContent).toContain("充电中");
    expect(laptopTemp?.textContent).toContain("54.2°C");
    expect(cards[1].querySelector('[data-testid="machine-power-thermal"]')).toBeNull();
    expect(cards[1].querySelector('[data-testid="metric-battery"]')).toBeNull();
    expect(cards[1].querySelector('[data-testid="metric-temp"]')).toBeNull();
  });
});

describe("Machines co-located managers (#14964)", () => {
  it("shows hosted manager status, pending_restart and version lag directly on cards while leaving other cards unchanged", async () => {
    setRoster({
      default: "m-1",
      managers: [
        { id: "m-1", name: "工作助手", online: true, machine_uid: "uid-a", host: "work-box", deploy: "service", version: "v1-202611241200" },
        { id: "m-2", name: "编译助手", online: true, machine_uid: "uid-b", host: "build-box", deploy: "service", version: "v1-202611201000", pending_restart: true },
        { id: "m-3", name: "旧版助手", online: true, host: "legacy-box", deploy: "service", version: "v1-202611201000" },
      ],
    });
    fake.machines = [
      { id: "work", name: "Work Box", online: true, machine_uid: "uid-a", hostname: "work-box", node_version: "v1-202611241200", manager: "m-1" },
      { id: "build", name: "Build Box", online: true, machine_uid: "uid-b", hostname: "build-box", node_version: "v1-202611241200", manager: "m-1" },
      { id: "legacy", name: "Legacy Box", online: true, hostname: "LEGACY-BOX", node_version: "v1-202611241200", node_mismatch: true, can_deploy: true, manager: "m-1" },
      { id: "plain", name: "Plain Node", online: true, machine_uid: "uid-plain", hostname: "plain-box", node_version: "v1-202611241200", manager: "m-1" },
    ];
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    const cardOf = (name: string) => [...host.querySelectorAll("section")].find((s) => s.querySelector("h2")?.textContent === name)!;

    const workBadge = cardOf("Work Box").querySelector('[data-hosted-manager="m-1"]');
    expect(workBadge?.getAttribute("data-manager-state")).toBe("online");
    expect(workBadge?.textContent).toContain("工作助手");
    expect(workBadge?.textContent).toContain("在线");

    const buildBadge = cardOf("Build Box").querySelector('[data-hosted-manager="m-2"]');
    expect(buildBadge?.getAttribute("data-manager-state")).toBe("pending_restart");
    expect(buildBadge?.textContent).toContain("编译助手");
    expect(buildBadge?.textContent).toContain("待空闲重启");

    const legacyBadge = cardOf("Legacy Box").querySelector('[data-hosted-manager="m-3"]');
    expect(legacyBadge?.getAttribute("data-manager-state")).toBe("version_lag");
    expect(legacyBadge?.textContent).toContain("旧版助手");
    expect(legacyBadge?.textContent).toContain("版本落后");
    expect(cardOf("Legacy Box").querySelector('[data-testid="deploy-co-located-note"]')?.textContent).toContain("旧版助手");

    // Card without hosted managers stays clean with no hosted-managers container
    expect(cardOf("Plain Node").querySelector('[data-testid="hosted-managers"]')).toBeNull();
  });
});
