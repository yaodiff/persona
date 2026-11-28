// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { onOpenNewAgent } from "../../lib/new-agent";
import { href, parse } from "../../lib/router";
import type { Machine, Session } from "../../lib/types";
import { MachineDetail } from "./MachineDetail";
import { TerminalPage } from "../terminal/TerminalPage";
import { SpacePage } from "../SpacePage";

vi.hoisted(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) });
});
const machines: Machine[] = [{ id: "a", name: "Computer A", online: true }, { id: "b", name: "Computer B", online: true }];
const sessions: Session[] = [{ machine_id: "b", session_id: "session", title: "Review changes", cwd: "/projects/active", space_id: "active",
  space_name: "Active", space_kind: "herdr", state: "needs_you", status: "idle", todo: { id: "todo-a", title: "Review to-do" } }];
const read = { failedMachines: [] as string[] | null | undefined, paneError: false, folder: false };
const spaces = ["active", "quiet"].map(id => ({ id, source: "herdr", source_id: id, title: id === "active" ? "Active" : "Quiet", machine_id: "b", cwd: "/projects/" + id }));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: true, machines, features: ["terminal_panes", "pane_new_space", ...(read.folder ? ["pane_cwd"] : [])] }) };
});
vi.mock("../../lib/sessions", async (original) => ({ ...await original<typeof import("../../lib/sessions")>(), useFullAgentSessions: () => ({ sessions, loaded: true }) }));
vi.mock("../../lib/current-assistant", async (original) => ({ ...await original<typeof import("../../lib/current-assistant")>(), useScopedMachines: () => machines }));
vi.mock("../../lib/workspace-catalog", async (original) => ({ ...await original<typeof import("../../lib/workspace-catalog")>(), useWorkspaceCatalog: () => ({
  workspaces: [...spaces, { ...spaces[1], machine_id: "a", title: "Elsewhere" }], manager: "", owner: "", failedMachines: read.failedMachines,
}) }));

let host: HTMLDivElement, root: Root;
const posts: unknown[] = [];
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
const noOp = () => {};
const detail = () => act(async () => root.render(<MachineDetail mid="b" onToggleSidebar={noOp} />));
const find = (parent: Element, name: string) => [...parent.querySelectorAll<HTMLButtonElement>("button")].find(b => (b.getAttribute("aria-label") || b.textContent) === name)!;
const click = (el: HTMLElement) => act(async () => el.click());
const quiet = () => [...host.querySelectorAll("a")].find(a => a.textContent === "Quiet")!;
async function terminal() {
  const route = parse(location.pathname, location.search);
  expect(route).toEqual({ name: "terminal", view: "terminals" });
  if (route.name === "terminal") await act(async () => root.render(<TerminalPage route={route} onToggleSidebar={noOp} onNewAgent={noOp} />));
}
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/machines/b");
  posts.length = 0;
  Object.assign(read, { failedMachines: [], paneError: false, folder: false });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, opts?: RequestInit) => {
    const path = new URL(String(input), location.origin).pathname;
    if (path.endsWith("/metrics")) return json({ metrics: { machine_id: "b", online: true, latest: { t: 1, disk_used: 95, disk_total: 100 }, points: [] } });
    if (path.endsWith("/panes")) {
      if (read.paneError) return new Response("Pane inventory unavailable", { status: 503 });
      if (opts?.method === "POST") { posts.push([path, JSON.parse(String(opts.body))]); return json({ terminal: "pane:new" }); }
      return json({ hosts: [{ kind: "herdr", stream: true, new_tab: true, control: true,
        workspaces: [{ id: "active", cwd: "/projects/active" }], tabs: [],
        panes: [{ pane: "agent", terminal: "pane:agent", session_id: "session", cwd: "/projects/active", workspace_id: "active", tab_id: "agent" },
          { pane: "one", terminal: "pane:one", cwd: "/projects/active", workspace_id: "active", tab_id: "tab" }] }] });
    }
    return json({});
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });

it("shows waiting work once, links its to-do, and prefills machine and workspace Agent starts", async () => {
  const preset = vi.fn(), stop = onOpenNewAgent(preset);
  try {
    await detail();
    const waiting = [...host.querySelectorAll("section")].find(s => s.querySelector("h2")?.textContent === "Needs you")!;
    const todo = waiting.querySelector<HTMLAnchorElement>('a[href="/todos?id=todo-a"]')!;
    expect(waiting.textContent).toContain("Review changes");
    const management = host.querySelector('a[href="/machines/b/management"]')!;
    expect(management.compareDocumentPosition(waiting) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(management.compareDocumentPosition(quiet()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(host.querySelectorAll('a[href="/s/b/session"]')).toHaveLength(1);
    await click(find(host, t("space-page.new_agent")));
    expect(preset).toHaveBeenLastCalledWith({ machineId: "b" });
    await click(find(quiet().parentElement!, t("space-page.new_agent")));
    expect(preset).toHaveBeenLastCalledWith({ machineId: "b", cwd: "/projects/quiet" });
    expect(host.textContent).not.toContain("Elsewhere");
    expect(host.textContent!.indexOf("Active")).toBeLessThan(host.textContent!.indexOf("Quiet"));
    await click(todo);
    expect(location.pathname + location.search).toBe("/todos?id=todo-a");
  } finally { stop(); }
});

it("opens a known workspace without sessions by inventory identity", async () => {
  await detail(); await click(quiet());
  const route = parse(location.pathname, location.search);
  expect(route).toEqual({ name: "space", mid: "b", space: "Quiet", workspace: "quiet" });
  if (route.name === "space") await act(async () => root.render(<SpacePage {...route} workspaceId={route.workspace} onToggleSidebar={noOp} />));
  expect(host.textContent).toContain("/projects/quiet");
  expect(host.textContent).toContain(t("space-page.no_agents"));
});

it("opens an existing shell on this computer ahead of Agent panes and keeps Engine under Configuration", async () => {
  await detail();
  await act(async () => root.render(<MachineDetail mid="b" subview="management" onToggleSidebar={noOp} />));
  const engine = host.querySelector('[data-testid="engine-section"]')!;
  expect(engine.parentElement?.querySelector("h2")?.textContent).toBe("Configuration");
  await detail();
  await click(find(host, t("sidebar.nav_terminal"))); await terminal();
  expect(location.pathname + location.search).toBe(href({ name: "terminal", mid: "b", id: "pane:one", view: "terminals" }));
  expect(posts).toEqual([]);
});

it("offers a new terminal in a quiet workspace even when another workspace has a pane", async () => {
  await detail(); await click(find(quiet().parentElement!, t("sidebar.nav_terminal"))); await terminal();
  expect(host.textContent).toContain("/projects/quiet");
  expect(posts).toEqual([]);
  await click(find(host, t("terminal-terminalpage.new_terminal")));
  expect(posts).toEqual([["/api/v1/machines/b/panes", { kind: "herdr", cwd: "/projects/quiet", new_space: true }]]);
  expect(location.pathname).toBe("/terminal/b/pane%3Anew");
});

it("takes a disk warning straight to reclaim on the selected computer", async () => {
  await detail();
  await click([...host.querySelectorAll("button")].find(b => b.textContent?.includes(t("machines-usagemap.reclaim_open")))!);
  expect(location.pathname + location.search).toBe("/machines/b/storage?mode=reclaim");
});

it("distinguishes pending and failed workspace discovery while keeping known rows", async () => {
  read.failedMachines = undefined;
  await detail();
  expect(host.textContent).toContain(t("machines-machinedetail.workspaces_loading"));
  read.failedMachines = ["b"];
  await detail();
  expect(host.textContent).toContain(t("machines-machinedetail.workspaces_failed"));
  expect(quiet()).toBeTruthy();
});

it("retries a failed pane read before offering creation, and uses the folder-capable hub", async () => {
  read.paneError = true;
  read.folder = true;
  await detail();
  await click(find(quiet().parentElement!, t("sidebar.nav_terminal"))); await terminal();
  expect(host.querySelector('[role="alert"]')).toBeTruthy();
  expect(find(host, t("terminal-terminalpage.new_terminal"))).toBeUndefined();
  read.paneError = false;
  await click(find(host, t("terminal-terminalpage.retry")));
  await click(find(host, t("terminal-terminalpage.new_terminal")));
  expect(posts).toEqual([["/api/v1/machines/b/panes", { kind: "herdr", cwd: "/projects/quiet" }]]);
});


it("opens the quiet workspace folder on its own computer", async () => {
  await detail();
  await click(find(quiet().parentElement!, t("machines-machinedetail.files")));
  expect(parse(location.pathname, location.search)).toEqual({ name: "machine", mid: "b", subview: "files", path: "/projects/quiet" });
});
