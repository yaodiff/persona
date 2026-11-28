// @vitest-environment jsdom
// MachineDetail co-located managers (#14964): shows hosted manager name, online/offline state, deploy mode, version,
// pending_restart / version-lag hints, Redeploy co-located note, and quick link to Settings › Hub › Assistants.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetRoster, setRoster } from "../../lib/managers";
import type { Machine } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const fake = vi.hoisted(() => ({ machines: [] as Machine[] }));
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: true, machines: fake.machines }) };
});

const { MachineDetail } = await import("./MachineDetail");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
};

beforeEach(() => {
  resetRoster();
  history.replaceState(null, "", "/machines/box");
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404 }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetRoster();
  vi.unstubAllGlobals();
});

describe("MachineDetail hosted managers (#14964)", () => {
  it("displays hosted managers with deploy mode, version, pending_restart and version-lag hints, and links to Settings › Hub › Assistants", async () => {
    setRoster({
      default: "m-1",
      managers: [
        { id: "m-1", name: "主助手", online: true, machine_uid: "uid-box", host: "box-host", deploy: "service", version: "v1-202611201000", pending_restart: true },
        { id: "m-2", name: "容器助手", online: true, machine_uid: "uid-box", host: "box-host", deploy: "docker", version: "v1-202611201000" },
        { id: "m-3", name: "离线助手", online: false, machine_uid: "uid-box", host: "box-host", deploy: "child", version: "v1-202611201000" },
      ],
    });
    fake.machines = [
      { id: "box", name: "Dev Box", online: true, machine_uid: "uid-box", hostname: "box-host", node_version: "v1-202611241200", node_mismatch: true, can_deploy: true },
    ];
    act(() => root.render(<MachineDetail subview="management" mid="box" onToggleSidebar={() => {}} />));
    await flush();

    const section = host.querySelector('[data-testid="hosted-managers-detail"]')!;
    expect(section).not.toBeNull();
    expect(section.textContent).toContain("主助手");
    expect(section.textContent).toContain("服务");
    expect(section.textContent).toContain("待空闲重启");
    expect(section.querySelector('[data-testid="manager-pending-restart-hint"]')?.textContent).toContain("空闲后自动重启");

    expect(section.textContent).toContain("容器助手");
    expect(section.textContent).toContain("容器");
    expect(section.textContent).toContain("版本落后");
    expect(section.querySelector('[data-testid="manager-version-lag-hint"]')?.textContent).toContain("重启其管理服务");

    expect(section.textContent).toContain("离线助手");
    expect(section.textContent).toContain("hub 内置");
    expect(section.textContent).toContain("离线");
    expect(section.textContent).toContain("v1-202611201000");

    // MachineDeploy strip includes co-located restart note
    expect(host.querySelector('[data-testid="deploy-co-located-note"]')?.textContent).toContain("主助手");

    // Quick link navigates to Settings › Hub › Assistants
    const link = section.querySelector<HTMLAnchorElement>('[data-testid="hosted-managers-settings-link"]')!;
    expect(link.getAttribute("href")).toBe("/settings?tab=managers");
    act(() => void link.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })));
    expect(location.pathname + location.search).toBe("/settings?tab=managers");
  });

  it("omits the hosted managers block when no manager runs on the computer", async () => {
    setRoster({
      default: "m-1",
      managers: [{ id: "m-1", name: "别机助手", online: true, machine_uid: "uid-other", host: "other-host", deploy: "service" }],
    });
    fake.machines = [{ id: "box", name: "Dev Box", online: true, machine_uid: "uid-box", hostname: "box-host" }];
    act(() => root.render(<MachineDetail subview="management" mid="box" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector('[data-testid="hosted-managers-detail"]')).toBeNull();
    expect(host.querySelector('[data-testid="deploy-co-located-note"]')).toBeNull();
  });
});
