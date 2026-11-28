// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import { navigate } from "../../lib/router";
import { t } from "../../i18n";
import { NewTerminalDialog } from "./NewTerminalDialog";
import type { PanesResponse } from "../../lib/terminals";
const env = vi.hoisted(() => ({ listing: {} as Record<string, { data: PanesResponse; error?: string }>, features: ["pane_new_space"] }));
vi.mock("../../lib/current-assistant", () => ({ useScopedMachines: () => [{ id: "one", name: "Computer one", online: true }, { id: "offline", name: "Offline", online: false }] }));
vi.mock("../../lib/terminal-panes", () => ({ usePanes: () => ({ listing: env.listing, busy: false, refresh: vi.fn() }) }));
vi.mock("../../lib/store", () => ({ useStore: (read: (s: typeof env) => unknown) => read(env) }));
vi.mock("../../lib/api", async original => ({ ...await original<typeof import("../../lib/api")>(), api: vi.fn() }));
vi.mock("../../lib/router", async original => ({ ...await original<typeof import("../../lib/router")>(), navigate: vi.fn() }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
const close = vi.fn();
const paneHost = (workspaces: { id: string; label: string }[] = []) => ({ kind: "herdr", control: true, stream: true, new_tab: true, workspaces, tabs: [], panes: [] });
beforeEach(() => { vi.clearAllMocks(); env.features = ["pane_new_space"]; env.listing = { one: { data: { hosts: [paneHost([{ id: "workspace", label: "Example" }])] } } }; host = document.createElement("div"); document.body.append(host); root = createRoot(host); });
afterEach(() => { act(() => root.unmount()); host.remove(); });
async function render() { await act(async () => root.render(<NewTerminalDialog open onClose={close} />)); }
const button = (text: string) => [...document.querySelectorAll("button")].find(el => el.textContent === text)!;
describe("Agents + New terminal", () => {
  it("opens the selected computer/workspace with the existing plain-shell protocol", async () => {
    vi.mocked(api).mockResolvedValue({ terminal: "pane:new" }); await render();
    await act(async () => button(t("terminal-terminalpage.new_in_space", { name: "Example" })).click());
    expect(api).toHaveBeenCalledWith("/machines/one/panes", { method: "POST", json: { kind: "herdr", workspace_id: "workspace" } });
    expect(navigate).toHaveBeenCalledWith({ name: "terminal", mid: "one", id: "pane:new" }); expect(close).toHaveBeenCalledOnce();
  });
  it("keeps the picker and error after failure, and can retry the same choice", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("Not connected")).mockResolvedValueOnce({ terminal: "pane:retry" }); await render();
    const label = t("terminal-terminalpage.new_in_space", { name: "Example" }); await act(async () => button(label).click());
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("Not connected"); expect(close).not.toHaveBeenCalled();
    await act(async () => button(label).click()); expect(close).toHaveBeenCalledOnce();
  });
  it("opens a new home workspace only when the advertised capability allows it", async () => {
    env.listing = { one: { data: { hosts: [paneHost()] } } }; vi.mocked(api).mockResolvedValue({ terminal: "pane:home" }); await render();
    await act(async () => button(t("terminal-terminalpage.new_terminal")).click());
    expect(api).toHaveBeenCalledWith("/machines/one/panes", { method: "POST", json: { kind: "herdr", cwd: "~", new_space: true } });
    env.features = []; await render(); expect([...document.querySelectorAll("button")].some(b => b.textContent === t("terminal-terminalpage.new_terminal"))).toBe(false);
  });
});
