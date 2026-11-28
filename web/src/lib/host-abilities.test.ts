import { afterEach, describe, expect, it, vi } from "vitest";
import {
  abilitiesOf, fallbackAbilities, hostAbilities, hostAbilitiesVersion, orcaChatRenamesSpace, rememberHostAbilities, resetHostAbilities,
  subscribeHostAbilities,
} from "./host-abilities";
import { renamesRuntimeSpace } from "./worktree-actions-context";
import type { WorkspaceCatalogSnapshot } from "./workspace-catalog";
import type { PaneHostDTO } from "./terminals";

const host = (kind: string, abilities?: PaneHostDTO["abilities"]): PaneHostDTO =>
  ({ kind, stream: false, control: false, new_tab: true, workspaces: [], tabs: [], panes: [], ...(abilities ? { abilities } : {}) });

afterEach(() => resetHostAbilities());

// #17053: what a host can do comes from its node, never from its name; a node that predates `abilities` gets the table.
describe("host abilities", () => {
  it("an older node's hosts read from the table: herdr all of it, Orca rename / close pane / Git, Tern and Rex rename", () => {
    expect(fallbackAbilities(undefined)).toEqual({ rename: true, closePane: true, split: true, closeTab: true, git: true, runHere: true });
    expect(fallbackAbilities("orca")).toEqual({ rename: true, closePane: true, split: false, closeTab: false, git: true, runHere: false });
    expect(fallbackAbilities("tern")).toMatchObject({ rename: true, git: false, closePane: false });
    for (const kind of ["herdr-core", "botmux", "future"]) expect(Object.values(fallbackAbilities(kind)).some(Boolean)).toBe(false);
    expect(abilitiesOf(host("orca"))).toEqual(fallbackAbilities("orca"));
  });

  it("a node that reports them is taken at its word, missing ones as no", () => {
    expect(abilitiesOf(host("orca", { rename: true, split: true, close_tab: true }))).toEqual({
      rename: true, closePane: false, split: true, closeTab: true, git: false, runHere: false,
    });
    expect(abilitiesOf(host("herdr", {}))).toEqual({ rename: false, closePane: false, split: false, closeTab: false, git: false, runHere: false });
  });

  it("remembers each computer's report until a list no longer has the host, and tells whoever draws them", () => {
    const changed = vi.fn();
    const off = subscribeHostAbilities(changed);
    const before = hostAbilitiesVersion();
    rememberHostAbilities("m1", { hosts: [host("orca", { rename: true, close_pane: true, split: true, close_tab: true, git: true })] });
    expect(hostAbilities("m1", "orca").split).toBe(true);
    expect(hostAbilities("m2", "orca").split).toBe(false);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(hostAbilitiesVersion()).toBeGreaterThan(before);
    // the same report again draws nothing
    rememberHostAbilities("m1", { hosts: [host("orca", { rename: true, close_pane: true, split: true, close_tab: true, git: true })] });
    expect(changed).toHaveBeenCalledTimes(1);
    rememberHostAbilities("m1", { hosts: [] });
    expect(hostAbilities("m1", "orca")).toEqual(fallbackAbilities("orca"));
    expect(changed).toHaveBeenCalledTimes(2);
    off();
  });
});

describe("an Orca native chat's space", () => {
  const chat = { machine_id: "m1", session_id: "c1", space_id: "repo::/home/me/app", space_kind: "orca", source_kind: "runtime" } as const;
  const catalog = { owner: "o", manager: "assistant", workspaces: [] } as unknown as WorkspaceCatalogSnapshot;

  it("is renamed in Orca where the computer's Orca renames worktrees", () => {
    expect(orcaChatRenamesSpace(chat)).toBe(true);
    expect(renamesRuntimeSpace(chat as never, catalog)).toBe(true);
    rememberHostAbilities("m1", { hosts: [host("orca", { git: true })] });
    expect(orcaChatRenamesSpace(chat)).toBe(false);
    expect(renamesRuntimeSpace(chat as never, catalog)).toBe(false);
  });

  it("is not another runtime's, nor a chat without a space", () => {
    expect(orcaChatRenamesSpace({ ...chat, space_kind: "paseo" })).toBe(false);
    expect(orcaChatRenamesSpace({ ...chat, space_id: undefined })).toBe(false);
    expect(orcaChatRenamesSpace({ ...chat, source_kind: undefined })).toBe(false);
  });
});
