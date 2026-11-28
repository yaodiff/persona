// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Session } from "../../../lib/types";
const fake = vi.hoisted(() => ({ session: undefined as Session | undefined, online: true, post: vi.fn(async () => ({})), copy: vi.fn(), notify: vi.fn(), close: vi.fn(), closeAgent: undefined as undefined | { onSelect: () => void; disabled?: boolean } }));
vi.mock("../../../lib/store", () => ({
  getSession: (_mid: string, sid: string) => sid === "s1" ? fake.session : undefined,
  useStore: (read: (s: unknown) => unknown) => read({ machines: [{ id: "m1", online: fake.online }], sessions: { m1: fake.session ? [fake.session] : [] } }),
  refresh: vi.fn(async () => {}),
}));
vi.mock("../../../lib/node-actions", async (original) => ({
  ...await original<typeof import("../../../lib/node-actions")>(),
  defaultNodeDeps: () => ({ post: fake.post, copy: fake.copy, notify: fake.notify, changed: vi.fn(), closeAgent: () => fake.closeAgent }),
}));
import { t } from "../../../i18n";
import { PaneActions } from "./PaneActions";
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
let onSplit: ReturnType<typeof vi.fn<() => void>>;
beforeEach(() => {
  fake.session = { machine_id: "m1", session_id: "s1", pane_id: "w1:p1", space_kind: "herdr", agent: "claude" } as Session;
  fake.online = true; fake.closeAgent = undefined;
  fake.post.mockReset().mockResolvedValue({}); fake.copy.mockClear(); fake.notify.mockClear(); fake.close.mockClear();
  host = document.body.appendChild(document.createElement("div")); root = createRoot(host); onSplit = vi.fn();
});
afterEach(() => { act(() => root.unmount()); document.body.innerHTML = ""; });
const button = (label: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;
const key = (el: HTMLElement, k: string) => act(() => el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
const render = (sid = "s1", fullSupported = true) => act(() => root.render(<PaneActions mid="m1" sid={sid} fullSupported={fullSupported} onSplit={onSplit} />));
const open = () => act(() => button(t("session-terminalpanel.pane_actions")).click());
const clickRow = async (label: string) => { const row = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((el) => el.textContent?.includes(label))!; expect(row).toBeTruthy(); await act(async () => row.click()); };
it.each(["right", "down"])("splits %s and reveals Full UI only after success", async (direction) => {
  render(); let resolve!: () => void; fake.post.mockImplementationOnce(() => new Promise((done) => { resolve = () => done({}); }));
  act(() => button(t(`lib-node-actions.split_${direction}`)).click());
  expect(fake.post).toHaveBeenCalledWith("m1", "panes/split", { pane: "w1:p1", direction });
  expect(onSplit).not.toHaveBeenCalled(); expect(button(t("lib-node-actions.split_right")).disabled).toBe(true);
  await act(async () => resolve()); expect(onSplit).toHaveBeenCalledOnce();
});
it("keeps Single pane and reports a failed split", async () => {
  render(); fake.post.mockRejectedValueOnce(new Error("offline"));
  await act(async () => button(t("lib-node-actions.split_right")).click());
  expect(onSplit).not.toHaveBeenCalled(); expect(button(t("lib-node-actions.split_right")).disabled).toBe(false);
});
it("requires shell close confirmation and Escape cancels", async () => {
  fake.session = undefined; render("pane:w1:p1"); open();
  await clickRow(t("session-terminalpanel.close_this_pane")); expect(fake.post).not.toHaveBeenCalled();
  key(button(t("session-terminalpanel.pane_actions")), "Escape"); expect(document.querySelector('[role="menu"]')).toBeNull();
  open(); await clickRow(t("session-terminalpanel.close_this_pane")); await clickRow(t("lib-node-actions.close_confirm"));
  expect(fake.post).toHaveBeenCalledWith("m1", "panes/close", { pane: "w1:p1" });
});
it("reuses the Agent close dialog through a raw pane address", async () => {
  fake.closeAgent = { onSelect: fake.close }; render("pane:w1:p1"); open();
  await clickRow(t("session-terminalpanel.close_this_pane")); expect(fake.close).toHaveBeenCalledOnce(); expect(fake.post).not.toHaveBeenCalled();
});
it.each(["Enter", " ", "ArrowDown"])("opens the toolbar menu with %j and restores focus on Escape", (activationKey) => {
  render();
  const trigger = button(t("session-terminalpanel.pane_actions"));
  trigger.focus();
  key(trigger, activationKey);
  expect(document.querySelector('[role="menu"]')).not.toBeNull();
  key(trigger, "Escape");
  expect(document.querySelector('[role="menu"]')).toBeNull();
  expect(document.activeElement).toBe(trigger);
});
it("takes N, I and Backspace only while the toolbar menu owns focus", async () => {
  render(); open(); const field = document.body.appendChild(document.createElement("input")); field.focus();
  for (const k of ["n", "i", "Backspace"]) key(field, k);
  expect(fake.copy).not.toHaveBeenCalled(); expect(fake.post).not.toHaveBeenCalled(); expect(document.querySelectorAll('[role="menu"]')).toHaveLength(1);
  const outside = document.body.appendChild(document.createElement("div")); outside.tabIndex = 0; outside.focus();
  for (const k of ["n", "i", "Backspace"]) key(outside, k);
  expect(fake.copy).not.toHaveBeenCalled(); expect(document.querySelectorAll('[role="menu"]')).toHaveLength(1);
  const trigger = button(t("session-terminalpanel.pane_actions")); trigger.focus(); key(trigger, "i");
  expect(fake.copy).toHaveBeenCalledWith("w1:p1");
  open(); key(trigger, "n"); expect(document.querySelectorAll('[role="menu"]')).toHaveLength(2);
  await clickRow(t("lib-node-actions.split_down")); expect(onSplit).toHaveBeenCalledOnce();
  open(); key(trigger, "Backspace"); expect(fake.post).toHaveBeenCalledTimes(1);
});
it("keeps offline panes and hosts that do neither read-only", () => {
  fake.online = false; render(); expect(button(t("lib-node-actions.split_right"))).toBeNull();
  fake.online = true; fake.session!.space_kind = "herdr-core"; fake.session!.pane_id = "herdr-core:p1"; render();
  expect(button(t("lib-node-actions.split_right"))).toBeNull(); open(); expect(document.body.textContent).not.toContain(t("session-terminalpanel.close_this_pane"));
});
// #17053: an Orca pane closes (the node does it for Orca) and splits once its computer says Orca does.
it("follows what the pane's host can do: Orca closes, and splits once the node reports it", async () => {
  fake.session!.space_kind = "orca"; fake.session!.pane_id = "orca:tab1:leaf1"; render();
  expect(button(t("lib-node-actions.split_right"))).toBeNull(); open(); expect(document.body.textContent).toContain(t("session-terminalpanel.close_this_pane"));
  key(button(t("session-terminalpanel.pane_actions")), "Escape");
  const { rememberHostAbilities, resetHostAbilities } = await import("../../../lib/host-abilities");
  try {
    act(() => rememberHostAbilities("m1", { hosts: [{ kind: "orca", stream: false, control: false, new_tab: true, workspaces: [], tabs: [], panes: [],
      abilities: { rename: true, close_pane: true, split: true, close_tab: false, git: true, run_here: false } }] }));
    expect(button(t("lib-node-actions.split_right"))).not.toBeNull();
  } finally {
    act(() => resetHostAbilities());
  }
});
it("hides missing-pane controls and unsupported splits", () => {
  fake.session!.pane_id = undefined; render(); expect(host.childElementCount).toBe(0);
  fake.session!.pane_id = "w1:p1"; render("s1", false); expect(button(t("lib-node-actions.split_right"))).toBeNull();
});
