// @vitest-environment jsdom
// The admin page's Machines row and a switch to Persona Link (#10662): it starts through the admin request helper (the
// session cookie and its CSRF token, not the app's bearer token), and only on a computer the list says something about.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { resetLinkSwitchAttempts } from "../../lib/link-switch";
import { setCsrf, setTransport } from "../api";
import type { AdminMachine } from "../types";
import { Machines } from "./Machines";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
let calls: { path: string; init: RequestInit }[];
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const button = (label: string) => [...document.body.querySelectorAll("button")].filter((b) => b.textContent?.trim() === label);
const machine = (id: string, extra: Partial<AdminMachine>): AdminMachine => ({ id, name: id, kind: "ssh", ssh_host: id, online: true, transport: "ssh", os: "linux", state: "active", ...extra });

beforeEach(() => {
  resetLinkSwitchAttempts();
  setCsrf("tok");
  calls = [];
  setTransport(async (path, init) => {
    calls.push({ path, init });
    const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
    if (path === "/api/v1/admin/machines")
      return reply({ machines: [machine("box", { link_switch: { available: true } }), machine("mac", { os: "darwin", link_switch: { available: false, reason: "unsupported_os" } }), machine("old", {})], tls: true, restart: { required: false, reasons: [] } });
    if (path.endsWith("/link-switch")) return reply({ link_switch: { available: false, reason: "in_progress", phase: "checking", at: "2026-11-19T08:00:00Z" } }, 202);
    return reply({});
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  setTransport((path, init) => fetch(path, init));
  setCsrf("");
});

describe("admin Machines: switching to Persona Link", () => {
  it("starts through the admin request helper, once, and only where the list offers it", async () => {
    await act(async () => root.render(<Machines />));
    await flush();
    expect(host.querySelectorAll("[data-link-switch]")).toHaveLength(2);
    expect(host.textContent).toContain("仅支持在此切换 Linux 电脑");
    const starts = button("切换到 Persona Link…");
    expect(starts).toHaveLength(1);
    await act(async () => starts[0]!.click());
    await act(async () => button("切换")[0]!.click());
    await flush();
    const posts = calls.filter((c) => c.init.method === "POST");
    expect(posts).toHaveLength(1);
    expect(posts[0]!.path).toBe("/api/v1/admin/machines/box/link-switch");
    expect(posts[0]!.init.credentials).toBe("same-origin");
    const headers = posts[0]!.init.headers as Record<string, string>;
    expect(headers["X-Persona-CSRF"]).toBe("tok");
    expect(headers.Authorization).toBeUndefined();
    expect(host.querySelector("[data-step=checking]")?.getAttribute("data-state")).toBe("current");
  });
});
