// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import type { Machine } from "../../lib/types";
import { ToolchainPanel, type MachineToolchainReport } from "./ToolchainPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (v: unknown, status = 200) =>
  new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

const sampleReport: MachineToolchainReport = {
  collected_at: "2026-11-21T10:00:00Z",
  cached: true,
  cache_ttl_sec: 60,
  duration_ms: 42,
  os: "linux",
  arch: "amd64",
  tools: [
    { id: "git", category: "core", found: true, version: "2.47.1", path: "/usr/bin/git", resolved_path: "/usr/bin/git", status: "ready" },
    { id: "go", category: "core", found: true, version: "1.24.2", path: "/usr/local/go/bin/go", resolved_path: "/usr/local/go/bin/go", status: "ready" },
    { id: "node", category: "core", found: true, version: "22.14.0", path: "/home/dev/.nvm/versions/node/v22.14.0/bin/node", resolved_path: "/home/dev/.nvm/versions/node/v22.14.0/bin/node", status: "ready" },
    { id: "gh", category: "core", found: true, version: "2.67.0", path: "/usr/local/bin/gh", resolved_path: "/usr/local/bin/gh", status: "ready", capabilities: ["authenticated"] },
    { id: "docker", category: "core", found: true, version: "27.5.1", path: "/usr/bin/docker", resolved_path: "/usr/bin/docker", status: "ready", capabilities: ["daemon_accessible"] },
    { id: "pg_dump", category: "core", found: false, status: "missing" },
  ],
  agent_clis: [
    { id: "claude", category: "agent", found: true, version: "2.1.45", path: "/home/dev/.local/bin/claude", resolved_path: "/opt/claude/claude", status: "ready", capabilities: ["symlink"] },
    { id: "codex", category: "agent", found: true, version: "0.118.0", path: "/home/dev/.nvm/versions/node/v22.14.0/bin/codex", resolved_path: "/home/dev/.nvm/versions/node/v22.14.0/bin/codex", status: "ready" },
    { id: "agy", category: "agent", found: false, status: "missing" },
    { id: "omp", category: "agent", found: false, status: "missing" },
  ],
  docker: {
    installed: true,
    client_version: "27.5.1",
    server_version: "27.5.1",
    socket_path: "/var/run/docker.sock",
    socket_accessible: true,
    status: "ready",
  },
  github_cli: {
    installed: true,
    version: "2.67.0",
    authenticated: true,
    hosts: ["github.com"],
    status: "authenticated",
  },
  hardware: {
    gpus: [{ name: "NVIDIA GeForce RTX 4090", vendor: "nvidia", vram_mb: 24576, driver: "560.35.03" }],
    power: { has_battery: true, battery_percent: 82, charging: true, source: "ac", sleep_state: "deep" },
  },
  environment: {
    shell: "zsh",
    path_entries: ["/home/dev/.nvm/versions/node/v22.14.0/bin", "/usr/local/go/bin", "/usr/bin"],
    version_managers: ["nvm", "mise"],
    has_custom_go_path: true,
    proxy_configured: true,
  },
};

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
};

beforeEach(async () => {
  await initI18n("en");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("ToolchainPanel", () => {
  it("loads toolchain profile on demand and supports manual refresh", async () => {
    const requested: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        requested.push(url);
        const live = url.includes("refresh=1");
        return Promise.resolve(json({ ...sampleReport, cached: !live }));
      }),
    );

    const machines: Machine[] = [
      { id: "dev-box", name: "Dev Box", online: true },
      { id: "offline-box", name: "Offline Box", online: false },
    ];
    act(() => root.render(<ToolchainPanel machines={machines} />));
    await flush();

    expect(requested).toEqual(["/api/v1/machines/dev-box/toolchain"]);
    expect(host.textContent).toContain("Development toolchain & environment");
    expect(host.textContent).toContain("v2.47.1");
    expect(host.textContent).toContain("v22.14.0");
    expect(host.textContent).toContain("/var/run/docker.sock");
    expect(host.textContent).toContain("github.com");
    expect(host.textContent).toContain("NVIDIA GeForce RTX 4090");
    expect(host.textContent).toContain("24 GB VRAM");
    expect(host.textContent).toContain("82%");
    expect(host.textContent).toContain("Configured (redacted)");

    // Clicking Refresh sends ?refresh=1 without background polling.
    const refreshBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Refresh"));
    expect(refreshBtn).toBeTruthy();
    act(() => refreshBtn!.click());
    await flush();

    expect(requested).toEqual(["/api/v1/machines/dev-box/toolchain", "/api/v1/machines/dev-box/toolchain?refresh=1"]);
    expect(host.textContent).toContain("Fresh probe");

    // Once all computers are offline, the tool shows its offline state without fetching.
    act(() => root.render(<ToolchainPanel machines={machines.map((m) => ({ ...m, online: false }))} />));
    await flush();

    expect(requested).toHaveLength(2);
    expect(host.textContent).toContain("This computer is offline");
  });

  it("shows an update-node hint for node_outdated code and unknown method machine.toolchain", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(json({ error: "node too old", code: "node_outdated" }, 409))),
    );

    const machines: Machine[] = [{ id: "old-node", name: "Old Node", online: true }];
    act(() => root.render(<ToolchainPanel machines={machines} />));
    await flush();

    expect(host.textContent).toContain("This computer's node does not support toolchain profiling yet");

    act(() => root.unmount());
    root = createRoot(host);
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(json({ error: "unknown method machine.toolchain" }, 502))),
    );
    act(() => root.render(<ToolchainPanel machines={machines} />));
    await flush();

    expect(host.textContent).toContain("This computer's node does not support toolchain profiling yet");
  });

  it("distinguishes old-hub non-JSON 404 from generic 409 and JSON 404 errors", async () => {
    const machines: Machine[] = [{ id: "box-1", name: "Box 1", online: true }];

    // 1. Plain-text route-level 404 from an older hub -> update-hub message.
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response("404 page not found\n", { status: 404, headers: { "Content-Type": "text/plain" } }))),
    );
    act(() => root.render(<ToolchainPanel machines={machines} />));
    await flush();
    expect(host.textContent).toContain("This hub does not support toolchain profiling yet");
    expect(host.textContent).not.toContain("Update the node");

    // 2. Generic 409 without node_outdated code -> normal error state preserving the server message.
    act(() => root.unmount());
    root = createRoot(host);
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(json({ error: "probe rejected due to concurrent state conflict", code: "conflict" }, 409))),
    );
    act(() => root.render(<ToolchainPanel machines={machines} />));
    await flush();
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("probe rejected due to concurrent state conflict");
    expect(host.textContent).not.toContain("Update the node");
    expect(host.textContent).not.toContain("Update the hub");

    // 3. JSON 404 (e.g. unknown machine) -> normal error state, not old-hub or old-node.
    act(() => root.unmount());
    root = createRoot(host);
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(json({ error: "machine not found", code: "not_found" }, 404))),
    );
    act(() => root.render(<ToolchainPanel machines={machines} />));
    await flush();
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("machine not found");
    expect(host.textContent).not.toContain("Update the node");
    expect(host.textContent).not.toContain("Update the hub");
  });

  it("aborts pending manual refresh on machine switch and never leaks stale machine data or clears current busy state", async () => {
    const reportFor = (mid: string, gitPath: string, cached = true): MachineToolchainReport => ({
      ...sampleReport,
      cached,
      tools: [{ id: "git", category: "core", found: true, version: "2.47.1", path: gitPath, resolved_path: gitPath, status: "ready" }],
      agent_clis: [],
      environment: { shell: mid },
    });

    let resolveRefreshA!: (r: Response) => void;
    let resolveRefreshB!: (r: Response) => void;
    let signalRefreshA: AbortSignal | undefined;

    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/v1/machines/box-a/toolchain") {
          return Promise.resolve(json(reportFor("box-a", "/opt/box-a/bin/git", true)));
        }
        if (url === "/api/v1/machines/box-a/toolchain?refresh=1") {
          signalRefreshA = init?.signal ?? undefined;
          return new Promise<Response>((resolve) => {
            resolveRefreshA = resolve;
          });
        }
        if (url === "/api/v1/machines/box-b/toolchain") {
          return Promise.resolve(json(reportFor("box-b", "/opt/box-b/bin/git", true)));
        }
        if (url === "/api/v1/machines/box-b/toolchain?refresh=1") {
          return new Promise<Response>((resolve) => {
            resolveRefreshB = resolve;
          });
        }
        return Promise.reject(new Error(`unexpected url ${url}`));
      }),
    );

    const machines: Machine[] = [
      { id: "box-a", name: "Box A", online: true },
      { id: "box-b", name: "Box B", online: true },
    ];
    act(() => root.render(<ToolchainPanel machines={machines} />));
    await flush();
    expect(host.textContent).toContain("/opt/box-a/bin/git");

    // Start a manual refresh on Box A.
    const refreshBtn = () => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Refresh")) as HTMLButtonElement;
    act(() => refreshBtn().click());
    await flush();
    expect(signalRefreshA).toBeDefined();
    expect(signalRefreshA?.aborted).toBe(false);
    expect(refreshBtn().textContent).toContain("Refreshing");

    // Switch to Box B while Box A's refresh is still in flight: Box A's manual refresh must be aborted.
    const picker = host.querySelector("select")!;
    act(() => {
      picker.value = "box-b";
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    expect(signalRefreshA?.aborted).toBe(true);
    expect(host.textContent).toContain("/opt/box-b/bin/git");
    expect(host.textContent).not.toContain("/opt/box-a/bin/git");

    // Start a manual refresh on Box B, then resolve Box A's stale refresh promise out of order.
    act(() => refreshBtn().click());
    await flush();
    expect(refreshBtn().textContent).toContain("Refreshing");
    expect(refreshBtn().disabled).toBe(true);

    resolveRefreshA(json(reportFor("box-a", "/opt/box-a/bin/git-stale", false)));
    await flush();

    // Stale Box A response must NOT render under Box B and must NOT clear Box B's busy refresh state.
    expect(host.textContent).toContain("/opt/box-b/bin/git");
    expect(host.textContent).not.toContain("/opt/box-a/bin/git-stale");
    expect(refreshBtn().textContent).toContain("Refreshing");
    expect(refreshBtn().disabled).toBe(true);

    // Resolving Box B's refresh updates Box B cleanly and clears busy state.
    resolveRefreshB(json(reportFor("box-b", "/opt/box-b/bin/git-fresh", false)));
    await flush();
    expect(host.textContent).toContain("/opt/box-b/bin/git-fresh");
    expect(refreshBtn().textContent).not.toContain("Refreshing");
    expect(refreshBtn().disabled).toBe(false);
  });
});
