// @vitest-environment jsdom
// The sidebar's Expand all, from the Agents title row's button or the keyboard shortcut, opens every group of a list
// folded under its title (docs/web/web.md, DESIGN_SYSTEM.md 3.4), though a folded block holds none of its groups
// (#7440): they take the fold-all they missed when the block opens.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { resetFoldAll } from "../lib/fold-all";
import { setSidebarCollapsed } from "../lib/layout";
import { setAllFolded } from "../lib/sidebar-api";
import { refresh, reset } from "../lib/store";
import type { Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
// the block's title, a name in every language
const agentsTitle = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.getAttribute("aria-label") === "Agents" || b.textContent?.trim() === "Agents")!;
const groupHeads = () => [...host.querySelectorAll<HTMLButtonElement>("[data-sidebar-agents] [data-sidebar-group-heading] button[aria-expanded]")].filter((b) => !b.closest("[data-sidebar-agents-controls]"));
const opened = () => groupHeads().map((h) => h.getAttribute("aria-expanded"));

beforeEach(async () => {
  localStorage.clear();
  resetFoldAll();
  setSidebarCollapsed(false);
  history.replaceState(null, "", "/");
  localStorage.setItem("persona.agentGrouping", "state");
  const agents: Session[] = [
    { machine_id: "mac", session_id: "s1", title: "Release script", cwd: "/code/a", status: "working", state: "working" },
    { machine_id: "mac", session_id: "s2", title: "Read the logs", cwd: "/code/b", status: "idle", state: "idle" },
  ];
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    const path = new URL(url, "http://x").pathname;
    const body = path === "/api/v1/sessions" ? { sessions: agents } : path === "/api/v1/machines" ? { machines: [{ id: "mac", name: "Work Mac", online: true }] } : {};
    return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  }));
  await refresh();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("Expand all on a list folded under the Agents title", () => {
  for (const how of ["the title row's button", "the keyboard shortcut"] as const) {
    it(`opens every group in it, from ${how}`, async () => {
      act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
      await flush();
      expect(groupHeads().length).toBeGreaterThan(1);
      // every group folded by hand (Seen starts folded), then the list under its title
      for (const h of groupHeads()) if (h.getAttribute("aria-expanded") === "true") act(() => h.click());
      await flush();
      expect(opened().every((x) => x === "false")).toBe(true);
      act(() => agentsTitle().click());
      await flush();
      expect(agentsTitle().getAttribute("aria-expanded")).toBe("false");
      // past the fold's close: the block holds none of its groups now
      await act(async () => { await new Promise((r) => setTimeout(r, 600)); });
      await flush();
      expect(groupHeads()).toHaveLength(0);
      if (how === "the title row's button") act(() => host.querySelector<HTMLButtonElement>("[data-sidebar-agents-controls] [data-testid=agents-fold-all]")!.click());
      else {
        act(() => setAllFolded(false));
        act(() => agentsTitle().click());
      }
      await flush();
      expect(agentsTitle().getAttribute("aria-expanded")).toBe("true");
      expect(groupHeads().length).toBeGreaterThan(1);
      // before, the groups folded by hand stayed folded: they were not there to hear it
      expect(opened().every((x) => x === "true")).toBe(true);
    });
  }
});
