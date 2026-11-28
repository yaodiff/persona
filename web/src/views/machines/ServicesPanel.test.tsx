// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../../lib/types";
import { ServicesPanel, type MachineServicesReport } from "./ServicesPanel";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

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

const click = (el: Element) =>
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("ServicesPanel", () => {
  const machines: Machine[] = [{ id: "linux-1", name: "Linux Box", online: true }];

  const sampleReport: MachineServicesReport = {
    at: 1700000000,
    os: "linux",
    managers: [
      { kind: "systemd", available: true },
      { kind: "docker", available: false, unavailable: "docker not found" },
    ],
    services: [
      {
        kind: "systemd",
        id: "redis.service",
        name: "redis.service",
        state: "running",
        sub_state: "running",
        status: "active (running)",
        description: "Redis server",
      },
      {
        kind: "systemd",
        id: "personad-node.service",
        name: "personad-node.service",
        state: "running",
        sub_state: "running",
        status: "active (running)",
        protected: true,
        protect_note: "Persona's own service cannot be stopped or restarted from here",
      },
    ],
    containers: [],
  };

  it("renders services, unavailable manager status, protected guardrails, logs, and confirmation-gated restart", async () => {
    const requests: { url: string; method: string; body?: string }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        requests.push({ url, method, body: typeof init?.body === "string" ? init.body : undefined });
        if (url === "/api/v1/machines/linux-1/services") {
          return json(sampleReport);
        }
        if (url.startsWith("/api/v1/machines/linux-1/services/logs")) {
          return json({
            kind: "systemd",
            id: "redis.service",
            lines: 100,
            truncated: true,
            source: "journalctl",
            content: "2026-11-21T10:00:00Z redis ready",
          });
        }
        if (url === "/api/v1/machines/linux-1/services/action" && method === "POST") {
          return json({ kind: "systemd", id: "redis.service", action: "restart", ok: true });
        }
        return json({ error: "not found" }, 404);
      }),
    );

    act(() => root.render(<ServicesPanel machines={machines} />));
    await flush();

    // Manager availability badges render gracefully.
    const dockerBadge = host.querySelector('[data-manager="docker"]');
    expect(dockerBadge?.getAttribute("data-available")).toBe("false");
    expect(dockerBadge?.textContent).toContain("docker not found");

    // Protected Persona service disables restart/stop buttons.
    const personaRow = host.querySelector('[data-service-row="systemd:personad-node.service"]')!;
    const personaButtons = [...personaRow.querySelectorAll("button")];
    const personaRestart = personaButtons.find((b) => b.textContent?.includes("重启"));
    expect(personaRestart?.disabled).toBe(true);

    // Clicking Logs opens bounded log viewer dialog.
    const redisRow = host.querySelector('[data-service-row="systemd:redis.service"]')!;
    const logsBtn = [...redisRow.querySelectorAll("button")].find((b) => b.textContent?.includes("日志"))!;
    click(logsBtn);
    await flush();
    const logDialog = document.body.querySelector('[data-testid="service-logs-dialog"]');
    expect(logDialog?.textContent).toContain("redis ready");
    expect(logDialog?.textContent).toContain("已限制输出长度");

    // Close log dialog.
    const closeBtn = [...document.body.querySelectorAll('[role="dialog"] button')].find((b) =>
      b.textContent?.includes("关闭"),
    )!;
    click(closeBtn);
    await flush();

    // Clicking Restart does NOT send POST until confirmed in dialog.
    const restartBtn = [...redisRow.querySelectorAll("button")].find((b) => b.textContent?.includes("重启"))!;
    click(restartBtn);
    await flush();
    expect(requests.filter((r) => r.method === "POST")).toHaveLength(0);

    // Confirming sends POST with confirmed: true and refreshes services list.
    const confirmBtn = document.body.querySelector('[data-testid="service-action-confirm"]')!;
    click(confirmBtn);
    await flush();
    const posts = requests.filter((r) => r.method === "POST");
    expect(posts).toHaveLength(1);
    expect(JSON.parse(posts[0].body!)).toEqual({
      kind: "systemd",
      id: "redis.service",
      action: "restart",
      confirmed: true,
    });
    expect(requests.filter((r) => r.url === "/api/v1/machines/linux-1/services").length).toBeGreaterThanOrEqual(2);
  });

  it("handles offline machines and old nodes gracefully", async () => {
    const fetchMock = vi.fn(async () => json({ error: "unknown method machine.services" }, 502));
    vi.stubGlobal("fetch", fetchMock);

    // Offline machine does not call API.
    act(() => root.render(<ServicesPanel machines={[{ id: "off", name: "Offline Box", online: false }]} />));
    await flush();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(host.textContent).toContain("离线");

    // Online old node shows update notice.
    act(() => root.render(<ServicesPanel machines={machines} />));
    await flush();
    expect(host.textContent).toContain("更新");
  });
});
