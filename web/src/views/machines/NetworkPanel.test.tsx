// @vitest-environment jsdom
// NetworkPanel: on-demand read-only network & proxy diagnostics panel (#12351).
// Verifies rendering of redacted proxy settings, allowlisted endpoint probes,
// non-loopback interfaces, manual Re-check, zero background polling, and error states.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../../lib/types";
import { NetworkPanel, type MachineNetworkReport } from "./NetworkPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (v: unknown, status = 200) =>
  new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
};

const sampleReport: MachineNetworkReport = {
  checked_at: Math.floor(Date.now() / 1000) - 5,
  interfaces: [
    { name: "eth0", up: true, addresses: ["192.0.2.10", "2001:db8::10"] },
  ],
  proxy: {
    http_proxy: "http://[redacted]@proxy.example.com:8080",
    https_proxy: "https://[redacted]@proxy.example.com:8443",
    no_proxy: "localhost,127.0.0.1",
    active: true,
  },
  probes: [
    {
      id: "github_https",
      name: "GitHub HTTPS",
      target: "https://github.com",
      protocol: "https",
      via_proxy: true,
      reachable: true,
      status_code: 200,
      latency_ms: 42,
    },
    {
      id: "github_ssh_443",
      name: "GitHub SSH (443)",
      target: "ssh.github.com:443",
      protocol: "tcp",
      via_proxy: false,
      reachable: false,
      latency_ms: 4001,
      error: "dial tcp: i/o timeout",
    },
  ],
};

beforeEach(() => {
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

describe("NetworkPanel", () => {
  it("loads diagnostics on demand, renders redacted proxy & probes, and re-checks manually without polling", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        calls.push(url);
        return Promise.resolve(json(sampleReport));
      }),
    );

    const machines: Machine[] = [
      { id: "m1", name: "Workstation", online: true },
      { id: "m2", name: "Offline Box", online: false },
    ];

    act(() => root.render(<NetworkPanel machines={machines} />));
    await flush();

    expect(calls).toEqual(["/api/v1/machines/m1/network"]);
    expect(host.textContent).toContain("GitHub HTTPS");
    expect(host.textContent).toContain("GitHub SSH (443)");
    expect(host.textContent).toContain("http://[redacted]@proxy.example.com:8080");
    expect(host.textContent).toContain("192.0.2.10");
    expect(host.textContent).toContain("42 ms");
    expect(host.textContent).toContain("dial tcp: i/o timeout");

    // Zero background polling: advancing timers does not trigger additional requests.
    await act(async () => {
      vi.advanceTimersByTime(120_000);
    });
    await flush();
    expect(calls).toHaveLength(1);

    // Manual Re-check triggers one fresh on-demand request.
    const recheckBtn = [...host.querySelectorAll("button")].find((b) => /重新检测|Re-check/.test(b.textContent ?? ""));
    expect(recheckBtn).toBeDefined();
    await act(async () => {
      recheckBtn!.click();
    });
    await flush();
    expect(calls).toEqual(["/api/v1/machines/m1/network", "/api/v1/machines/m1/network"]);

    // Once all computers are offline, the tool shows its offline state without fetching.
    act(() => root.render(<NetworkPanel machines={machines.map((m) => ({ ...m, online: false }))} />));
    await flush();
    expect(calls).toHaveLength(2);
    expect(host.textContent).toMatch(/离线|offline/i);
  });

  it("shows old-node message when node does not implement machine.network", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(json({ error: "unknown method machine.network" }, 502))),
    );
    const machines: Machine[] = [{ id: "m1", name: "Legacy Box", online: true }];
    act(() => root.render(<NetworkPanel machines={machines} />));
    await flush();
    expect(host.textContent).toMatch(/更新.*节点|Update this computer's node/);
  });

  it("wires NetworkPanel lazily from MachineViews.tsx via lazyPage", async () => {
    const fs = await import("node:fs");
    const path = await import("node:path");
    const source = fs.readFileSync(path.join(__dirname, "MachineViews.tsx"), "utf8");
    expect(source).toMatch(/const NetworkPanel = lazyPage\(\(\) => import\("\.\/NetworkPanel"\)\.then\(\(m\) => m\.NetworkPanel\)\)/);
  });
});
