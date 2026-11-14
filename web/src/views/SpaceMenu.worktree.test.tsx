// @vitest-environment jsdom
import { beforeEach, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { credentialOwner, setToken } from "../lib/api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import type { SpaceLike } from "../lib/agent-filter";
import type { WorkspaceCatalogSnapshot } from "../lib/workspace-catalog";
import type { MenuItem } from "../ui/menu-model";
import { initI18n } from "../i18n";

const fake = vi.hoisted(() => ({
  state: {} as ReturnType<typeof import("../lib/store").getState>,
  initial: {} as ReturnType<typeof import("../lib/store").getState>,
  catalog: {} as WorkspaceCatalogSnapshot,
  items: [] as MenuItem[], node: undefined as { workspaceId?: string } | undefined, openChild: vi.fn(), refreshCatalog: vi.fn(),
}));
vi.mock("../lib/store", async (original) => {
  const real = await original<typeof import("../lib/store")>();
  fake.initial = real.getState();
  return { ...real, getState: () => fake.state, useStore: <T,>(select: (state: typeof fake.state) => T) => select(fake.state) };
});
vi.mock("../lib/workspace-catalog", async (original) => ({
  ...await original<typeof import("../lib/workspace-catalog")>(), useWorkspaceCatalog: () => fake.catalog, refreshWorkspaceCatalog: fake.refreshCatalog,
}));
vi.mock("../lib/worktree-creation", () => ({ openChildWorktree: fake.openChild }));
vi.mock("./NodeMenu", () => ({ NodeMenu: ({ before, node }: { before: MenuItem[]; node: { workspaceId?: string } }) => { fake.items = before; fake.node = node; return null; } }));
const { SpaceMenu } = await import("./SpaceMenu");
let space: SpaceLike;
const item = () => fake.items.find((item) => item.key === "new-worktree")!;
const render = () => renderToStaticMarkup(<SpaceMenu sp={space} anchor={document.body} onClose={() => {}} onRename={() => {}} onMarkSeen={() => {}} />);
beforeEach(async () => {
  await initI18n("en"); resetManagerChoice(); setRosterDefault("manager"); setToken("synthetic-space-menu");
  fake.openChild.mockClear(); fake.refreshCatalog.mockClear();
  fake.state = { ...fake.initial, machines: [{ id: "computer", name: "Computer", online: true, manager: "manager", engine_hosts: ["herdr", "orca"] }] };
  fake.catalog = { owner: credentialOwner(), manager: "manager", workspaces: [{ id: "native-row", machine_id: "computer", source: "herdr", source_id: "native-workspace", cwd: "/repo/sample", title: "Sample" }] };
  space = { key: "runtime-folder", machineId: "computer", name: "Sample", sessions: [{ machine_id: "computer", session_id: "agy-session", title: "Runtime Agent", status: "idle", source_kind: "runtime", space_kind: "agy", space_id: "/repo/sample", cwd: "/repo/sample" }] };
});

it("opens the existing composer with the actual native source from a runtime space menu", async () => {
  render(); expect(item().label).toBe("New child worktree…"); expect(item().disabled).toBe(false);
  expect(fake.openChild).not.toHaveBeenCalled();
  await item().onSelect();
  expect(fake.openChild).toHaveBeenCalledWith({ machineId: "computer", host: "herdr", workspaceId: "native-workspace", cwd: "/repo/sample", label: "Sample", owner: fake.catalog.owner, manager: "manager" }, expect.any(Object));
});
it("asks which native source to use when the runtime folder has several matches", async () => {
  fake.catalog.workspaces.push({ ...fake.catalog.workspaces[0], id: "second-row", source: "orca", source_id: "other-workspace", title: "Other source" });
  render(); await item().onSelect(); expect(fake.openChild).not.toHaveBeenCalled();
  const children = item().children as MenuItem[];
  expect(children).toHaveLength(2); await children[1].onSelect();
  expect(fake.openChild.mock.calls[0][0]).toMatchObject({ host: "orca", workspaceId: "other-workspace" });
});
it("keeps the entry visible with guidance while its native source is unavailable", () => {
  fake.catalog.workspaces = []; render();
  expect(item().disabled).toBe(true); expect(item().detail).toContain("Open this repository");
  expect(fake.openChild).not.toHaveBeenCalled();
});
it("rechecks machine and owner changes before opening a source", async () => {
  render(); fake.state.machines[0].online = false;
  await expect(item().onSelect()).rejects.toThrow(); expect(fake.openChild).not.toHaveBeenCalled();
  fake.state.machines[0].online = true; chooseManager("other");
  await expect(item().onSelect()).rejects.toThrow(); expect(fake.openChild).not.toHaveBeenCalled();
});
it("offers Rename space on a Paseo space the catalog has workspaces of, and on no other runtime's", () => {
  const paseo = { machine_id: "computer", session_id: "paseo-1", title: "Paseo Agent", status: "idle" as const, source_kind: "runtime" as const,
    space_kind: "paseo", host: "paseo", space_id: "prj_0000000000000001", workspace_id: "wks_0000000000000001", cwd: "/repo/sample" };
  space = { key: "paseo-space", machineId: "computer", name: "sample", sessions: [paseo] };
  fake.catalog.workspaces = [{ id: "paseo-row", machine_id: "computer", source: "paseo", source_id: "wks_0000000000000001", cwd: "/repo/sample", title: "main",
    project_id: "prj_0000000000000001", workspace_kind: "git", is_main: true }];
  render(); expect(fake.items.some((i) => i.key === "rename")).toBe(true);
  fake.catalog.workspaces = []; render(); expect(fake.items.some((i) => i.key === "rename")).toBe(false);
  space = { key: "runtime-folder", machineId: "computer", name: "Sample", sessions: [{ ...paseo, space_kind: "agy", host: "agy" }] };
  render(); expect(fake.items.some((i) => i.key === "rename")).toBe(false);
});
it("gives a runtime's space no terminal host's ID to copy, and a herdr space its workspace's", () => {
  render(); expect(fake.node?.workspaceId).toBeUndefined();
  space = { ...space, sessions: [{ machine_id: "computer", session_id: "herdr-session", title: "Agent", status: "idle", space_kind: "herdr", space_id: "w1", workspace_id: "w1", cwd: "/repo/sample" }] };
  render(); expect(fake.node?.workspaceId).toBe("w1");
});
