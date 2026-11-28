// @vitest-environment jsdom
/// <reference types="node" />
// MachineDetail renders machine state immediately and loads MachineDeploy via lazyOverlay only when the computer has a
// visible deployment status, error, or version mismatch (no idle preload), leaving MachineDeploy.tsx untouched.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { resetRoster } from "../../lib/managers";
import type { Machine } from "../../lib/types";

const deployRenders = vi.hoisted(() => vi.fn());

vi.mock("./MachineDeploy", async (importOriginal) => {
  const real = await importOriginal<typeof import("./MachineDeploy")>();
  return {
    ...real,
    MachineDeploy: (props: Parameters<typeof real.MachineDeploy>[0]) => {
      deployRenders(props.machine.id);
      return real.MachineDeploy(props);
    },
  };
});

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

import { MachineDetail } from "./MachineDetail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const staticMachineDeployImport = /^import\s+(?!type\b)[^;]*from\s+["']\.\/MachineDeploy["']/m;

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 12; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};

const waitFor = async (check: () => unknown) => {
  for (let i = 0; i < 80; i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 10)); });
    if (check()) return;
  }
};

beforeEach(async () => {
  await initI18n("en");
  resetRoster();
  fake.machines = [];
  deployRenders.mockClear();
  history.replaceState(null, "", "/machines/box");
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404 }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  resetRoster();
  vi.unstubAllGlobals();
  await initI18n("zh-Hans");
});

describe("MachineDetail MachineDeploy lazy boundary", () => {
  it("keeps MachineDeploy out of static imports and loads it via lazyOverlay without idle preloading", () => {
    const source = readFileSync(resolve(__dirname, "MachineDetail.tsx"), "utf8");
    expect(source).not.toMatch(staticMachineDeployImport);
    expect(source).toContain('import("./MachineDeploy")');
    expect(source).toContain("lazyOverlay");
    expect(source).not.toMatch(/import\(["']\.\/MachineDeploy["']\)[^;]*idle\s*:\s*true/);
  });

  it("omits MachineDeploy on a healthy machine, then renders it on demand when a version mismatch appears and unmounts when cleared", async () => {
    fake.machines = [{ id: "box", name: "Dev Box", online: true }];
    await act(async () => root.render(<MachineDetail mid="box" subview="management" onToggleSidebar={() => {}} />));
    await flush();

    expect(host.textContent).toContain("Dev Box");
    expect(deployRenders).not.toHaveBeenCalled();
    expect(host.textContent).not.toContain(t("lib-machine-deploy.mismatch"));

    fake.machines = [{ id: "box", name: "Dev Box", online: true, node_mismatch: true, can_deploy: true }];
    await act(async () => root.render(<MachineDetail mid="box" subview="management" onToggleSidebar={() => {}} />));
    await waitFor(() => host.textContent?.includes(t("lib-machine-deploy.redeploy")));

    expect(deployRenders).toHaveBeenCalledWith("box");
    expect(host.textContent).toContain(t("lib-machine-deploy.mismatch"));
    expect(host.textContent).toContain(t("lib-machine-deploy.redeploy"));

    // Redeploy inside the lazily mounted strip still posts to the hub's deploy endpoint.
    const redeploy = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("lib-machine-deploy.redeploy"));
    expect(redeploy).toBeDefined();
    await act(async () => redeploy!.click());
    await flush();
    expect(vi.mocked(fetch).mock.calls.some(([url, init]) => String(url) === "/api/v1/machines/box/deploy" && init?.method === "POST")).toBe(true);

    fake.machines = [{ id: "box", name: "Dev Box", online: true }];
    await act(async () => root.render(<MachineDetail mid="box" subview="management" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).not.toContain(t("lib-machine-deploy.mismatch"));
  });

  it("resolves the dynamic import to MachineDeploy", async () => {
    const mod = await import("./MachineDeploy");
    expect(typeof mod.MachineDeploy).toBe("function");
  });
});
