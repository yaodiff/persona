// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import { credentialOwner, setToken } from "../lib/api";
import { resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import type { Machine, Session } from "../lib/types";
import type { WorkspaceCatalogSnapshot } from "../lib/workspace-catalog";
import type { OwnedWorkspaceTarget } from "../lib/worktree-actions";
import { openWorktreeActions, worktreeActionProps } from "../lib/worktree-actions";
import { WorktreeActionsButton, WorktreeActionsHost } from "./WorktreeActions";

const mock = vi.hoisted(() => ({
  catalog: {} as WorkspaceCatalogSnapshot,
  state: { machines: [] as Machine[], sessions: {} as Record<string, Session[]>, sessionSummaries: [] as Session[] },
  refresh: vi.fn(), openAgent: vi.fn(), child: vi.fn(), navigate: vi.fn(), parent: vi.fn(),
}));
vi.mock("../lib/workspace-catalog", async (original) => ({ ...await original<typeof import("../lib/workspace-catalog")>(), useWorkspaceCatalog: () => mock.catalog, refreshWorkspaceCatalog: mock.refresh }));
vi.mock("../lib/store", () => ({ getState: () => mock.state, useStore: (select: (state: typeof mock.state) => unknown) => select(mock.state) }));
vi.mock("../lib/new-agent", () => ({ openNewAgent: mock.openAgent }));
vi.mock("../lib/worktree-creation", () => ({ openChildWorktree: mock.child }));
vi.mock("../lib/router", () => ({ navigate: mock.navigate }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let target: OwnedWorkspaceTarget;
let host: HTMLDivElement;
let root: Root;
let requests: { url: string; init: RequestInit; resolve: (value: Response) => void }[];
let deny = false;
let folder = false;
let dirty = 0;
let linked = true;
let ordered: Session[] | undefined;
const body = () => document.body.textContent || "";
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((el) => el.textContent === label)!;
const item = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((el) => el.textContent?.includes(label))!;
const click = async (element: HTMLElement) => {
  await act(async () => element.click());
  await act(async () => { await vi.dynamicImportSettled(); });
};
const reply = async (index: number, value: unknown, status = 200) => act(async () => requests[index].resolve(new Response(JSON.stringify(value), { status })));
const open = async () => {
  await act(async () => openWorktreeActions(target, document.getElementById("opener")!));
  await act(async () => { await vi.dynamicImportSettled(); });
};
const render = async (readOnly = false) => act(async () => root.render(<StrictMode><button id="opener" {...worktreeActionProps(target)}>Workspace</button><WorktreeActionsButton target={target} /><WorktreeActionsHost readOnly={readOnly} sessions={ordered} onOpenWorkspace={mock.parent} /></StrictMode>));

beforeEach(async () => {
  await initI18n("en"); resetManagerChoice(); setRosterDefault("manager"); setToken("synthetic-component");
  requests = []; deny = false; folder = false; dirty = 0; linked = true; ordered = undefined;
  Object.values(mock).forEach((value) => { if (typeof value === "function") value.mockClear(); });
  mock.catalog = { owner: credentialOwner(), manager: "manager", workspaces: [{ id: "discovery", machine_id: "computer", source: "orca", source_id: "work", title: "Readable worktree", cwd: "/repo/work", parent_source_id: "parent", available: true, online: true }, { id: "parent-row", machine_id: "computer", source: "orca", source_id: "parent", title: "Parent", cwd: "/repo", project_id: "other-project" }] };
  target = { owner: mock.catalog.owner, manager: "manager", machineId: "computer", host: "orca", workspaceId: "work", cwd: "/repo/work", label: "Readable worktree" };
  mock.state = { machines: [{ id: "computer", manager: "manager", name: "Studio computer", engine_hosts: ["orca", "herdr"], online: true }], sessions: {}, sessionSummaries: [] };
  vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
  vi.stubGlobal("matchMedia", (media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => {
    if (url.endsWith("/auth/tokens")) return Promise.resolve(new Response(JSON.stringify(deny ? { error: "read only" } : { current: "device", tokens: [{ id: "device", role: "full" }] }), { status: deny ? 403 : 200 }));
    const json = init.body ? JSON.parse(String(init.body)) : {};
    if (url.endsWith("/worktree/status")) return Promise.resolve(new Response(JSON.stringify({ worktree: linked, path: target.cwd }), { status: 200 }));
    if (json.preview) return Promise.resolve(new Response(JSON.stringify({ workspace_id: "work", kind: json.kind, host_id: "local", instance_id: json.kind === "orca" ? "private-instance" : "", preview_token: "private-token", path: "/repo/work", branch: json.close_only ? "" : "topic", folder: json.close_only || folder, dirty: json.close_only ? 0 : dirty, ...(json.close_only ? { close_only: true } : {}) }), { status: 200 }));
    return new Promise<Response>((resolve) => requests.push({ url, init, resolve }));
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host); await render();
});
afterEach(async () => { await act(async () => root.unmount()); host.remove(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it("hands off from the real menu to worktree rename, serializes Enter and restores the original opener", async () => {
  await open(); await click(item("Rename worktree"));
  const input = document.querySelector<HTMLInputElement>("[role=dialog] input")!;
  expect(document.activeElement).toBe(input); expect(body()).toContain("Agent names stay the same");
  await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "New workspace label"); input.dispatchEvent(new Event("input", { bubbles: true })); });
  await act(async () => document.querySelector("form")!.requestSubmit()); await act(async () => document.querySelector("form")!.requestSubmit());
  expect(requests).toHaveLength(1); expect(requests[0].url).toBe("/api/v1/machines/computer/worktree/rename");
  expect(JSON.parse(String(requests[0].init.body))).toEqual({ workspace_id: "work", kind: "orca", name: "New workspace label" });
  await reply(0, { error: "Rename refused" }, 500);
  expect(body()).toContain("Rename refused"); expect(mock.catalog.workspaces[0].title).toBe("Readable worktree"); expect(mock.refresh).not.toHaveBeenCalled();
  await click(button("Save")); await reply(1, {});
  expect(mock.refresh).toHaveBeenCalledTimes(1); expect(document.querySelector("[role=dialog]")).toBeNull(); expect(document.activeElement?.id).toBe("opener");
});
it("requires the real dirty checkbox and retains an uncertain deletion across close/reopen", async () => {
  dirty = 2; await open(); await click(item("Delete worktree"));
  expect(button("Delete worktree").disabled).toBe(true);
  const choices = document.querySelectorAll<HTMLInputElement>("input[type=checkbox]"); expect(choices).toHaveLength(2); expect(choices[1].checked).toBe(false);
  await click(choices[0]); await click(button("Delete worktree"));
  expect(JSON.parse(String(requests[0].init.body))).toEqual({ workspace_id: "work", kind: "orca", preview_token: "private-token", force: true });
  await reply(0, { workspace_id: "work", removed: false, state: "removing" });
  expect(mock.refresh).not.toHaveBeenCalled(); expect(body()).toContain("Removal is not confirmed"); expect(button("Check status").disabled).toBe(false);
  expect(body()).not.toContain("private-token"); expect(body()).not.toContain("private-instance");
  await click(button("Close")); await open(); await click(item("Check status")); await click(button("Check status"));
  expect(requests).toHaveLength(2); expect(JSON.parse(String(requests[1].init.body))).toEqual({ workspace_id: "work", kind: "orca", preview_token: "private-token", reconcile: true });
  await reply(1, { workspace_id: "work", removed: true, state: "removed" });
  expect(mock.refresh).toHaveBeenCalledTimes(1); expect(document.querySelector("[role=dialog]")).toBeNull();
});
it("clearly says folder unregister preserves the directory and shows no discard/branch choice", async () => {
  folder = true; dirty = 7; await open(); await click(item("Delete worktree"));
  expect(body()).toContain("Its directory and files are kept"); expect(document.querySelector("input[type=checkbox]")).toBeNull();
  await click(button("Remove entry")); expect(JSON.parse(String(requests[0].init.body))).toEqual({ workspace_id: "work", kind: "orca", preview_token: "private-token" });
});
it("uses keyboard context keys, Escape focus restoration, and the touch button for the same destination", async () => {
  const opener = document.getElementById("opener")!; opener.focus();
  await act(async () => opener.dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true, cancelable: true })));
  expect(item("New agent")).toBeTruthy();
  await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(document.activeElement).toBe(opener);
  await click(document.querySelector<HTMLButtonElement>('button[aria-label="Worktree actions"]')!); await click(item("New agent"));
  expect(mock.openAgent).toHaveBeenCalledExactlyOnceWith({ workspace: target });
});
it("opens only a direct ordered Agent including pins, and otherwise starts at the exact empty target", async () => {
  const agent = (id: string, workspace: string) => ({ machine_id: "computer", session_id: id, title: id, status: "idle", cwd: "/repo/work", host: "orca", workspace_id: workspace } as Session);
  mock.catalog.workspaces.push({ ...mock.catalog.workspaces[0], id: "child", source_id: "child", parent_source_id: "work", cwd: "/repo/work/child" });
  ordered = [agent("child-agent", "child"), agent("pinned-direct", "work"), agent("other-direct", "work")]; await render();
  await open(); await click(item("Open worktree")); expect(mock.navigate).toHaveBeenCalledExactlyOnceWith({ name: "session", mid: "computer", sid: "pinned-direct" });
  ordered = [agent("child-agent", "child")]; await render(); await open(); await click(item("Open worktree")); expect(mock.openAgent).toHaveBeenCalledExactlyOnceWith({ workspace: target });
});
it("passes a real same-host cross-project parent to the leader callback and calls frozen M4 export", async () => {
  await open(); await click(item("Open parent worktree"));
  expect(mock.parent).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ workspaceId: "parent", machineId: "computer", host: "orca", owner: target.owner, manager: target.manager }));
  await open(); await click(item("New child worktree")); expect(mock.child).toHaveBeenCalledWith(target, { changed: expect.any(Function) });
});
it("fails closed on restricted device roles and preserves copy navigation", async () => {
  deny = true; await open(); expect((item("New agent") as HTMLButtonElement).disabled).toBe(true);
  expect(body()).toContain("Full device access"); expect((item("Copy folder path") as HTMLButtonElement).disabled).toBe(false); expect(requests).toHaveLength(0);
});
it("does not show workspace removal for a generic runtime", async () => {
  target = { ...target, host: "future" }; mock.catalog.workspaces[0].source = "future"; await render(); await open();
  expect(item("Close workspace")).toBeUndefined(); expect(item("Delete worktree")).toBeUndefined();
});

it("closes a herdr main workspace through a file-preserving confirmation", async () => {
  target = { ...target, host: "herdr" }; mock.catalog.workspaces[0].source = "herdr"; linked = false;
  await render(); await open();
  expect(item("Close workspace")).toBeTruthy();
  expect((item("Delete worktree") as HTMLButtonElement).disabled).toBe(true);
  await click(item("Close workspace"));
  expect(body()).toContain("The directory, files and Git branches are kept");
  expect(document.querySelector("input[type=checkbox]")).toBeNull();
  await click(button("Close workspace"));
  expect(JSON.parse(String(requests[0].init.body))).toEqual({ workspace_id: "work", kind: "herdr", preview_token: "private-token", close_only: true });
  await reply(0, { workspace_id: "work", removed: true, state: "removed" });
  expect(mock.refresh).toHaveBeenCalledTimes(1);
  expect(document.querySelector("[role=dialog]")).toBeNull();
});

it("allows linked herdr checkout deletion and retains the branch by default", async () => {
  target = { ...target, host: "herdr" }; mock.catalog.workspaces[0].source = "herdr";
  await render(); await open(); await click(item("Delete worktree"));
  expect(body()).toContain("The checkout directory and its files will be deleted");
  await click(button("Delete worktree"));
  expect(JSON.parse(String(requests[0].init.body))).toEqual({ workspace_id: "work", kind: "herdr", preview_token: "private-token" });
});
it("invalidates a ready preview on machine availability or catalog target replacement", async () => {
  await open(); await click(item("Delete worktree")); mock.state.machines[0].online = false; await render();
  expect(body()).toContain("availability changed"); expect(button("Delete worktree").disabled).toBe(true); expect(requests).toHaveLength(0);
});
it("cannot refresh or close the next target after a stale rename response", async () => {
  await open(); await click(item("Rename worktree")); await click(button("Save"));
  const old = target; target = { ...target, workspaceId: "parent", cwd: "/repo", label: "Parent" }; await render(); await open();
  await reply(0, {}); expect(mock.refresh).not.toHaveBeenCalled(); expect(body()).toContain("Parent");
  target = old;
});
it("credential replacement closes and fences a pending deletion even in StrictMode", async () => {
  await open(); await click(item("Delete worktree")); await click(button("Delete worktree"));
  await act(async () => setToken("synthetic-next-owner")); await reply(0, { workspace_id: "work", removed: true, state: "removed" });
  expect(mock.refresh).not.toHaveBeenCalled(); expect(document.querySelector("[role=dialog]")).toBeNull();
});

it("keeps the original target and read-only reconciliation when a removing row vanishes from discovery", async () => {
  await open(); await click(item("Delete worktree")); await click(button("Delete worktree"));
  await reply(0, { workspace_id: "work", removed: false, state: "removing" });
  mock.catalog = { ...mock.catalog, workspaces: mock.catalog.workspaces.filter((w) => w.source_id !== "work") }; await render();
  expect(button("Check status").disabled).toBe(false); expect(body()).toContain("/repo/work");
  await click(button("Check status")); expect(JSON.parse(String(requests[1].init.body))).toEqual({ workspace_id: "work", kind: "orca", preview_token: "private-token", reconcile: true });
  await reply(1, { workspace_id: "work", removed: true, state: "removed" }); expect(mock.refresh).toHaveBeenCalledTimes(1);
});
