// @vitest-environment jsdom
// Settings › General › Appearance › Auto-open Needs you (#17061): sidebar entry is opt-in, while the pending count
// and the action bar's manual capsule stay available.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { requestDirectWaiting, takeDirectWaitingRequest } from "../lib/direct-waiting";
import { needsYouAutoOpen, setNeedsYouAutoOpen } from "../lib/needs-you-auto-open";
import { refresh, reset } from "../lib/store";
import type { Session } from "../lib/types";
import { PersonaRow } from "./SidebarRows";
import { SidebarRail } from "./SidebarRail";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = {
    getItem: (k: string) => kept.get(k) ?? null,
    setItem: (k: string, v: string) => void kept.set(k, v),
    removeItem: (k: string) => void kept.delete(k),
    clear: () => void kept.clear(),
  };
});

const main: Session = { machine_id: "hub", session_id: "main", title: "Persona", cwd: "/", status: "idle", state: "idle", assistant: true };
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  takeDirectWaitingRequest("main");
  reset();
  vi.unstubAllGlobals();
});

describe("Needs you auto-open on Main chat entry (#17061)", () => {
  function renderExpandedEntry() {
    act(() => root.render(
      <PersonaRow
        reg={() => () => {}}
        hover={() => {}}
        active={false}
        go={() => (event) => event.preventDefault()}
        main={main}
        waiting={1}
      />,
    ));
    return host.querySelector<HTMLAnchorElement>("a")!;
  }

  it("expanded sidebar: a fresh browser keeps the request suppressed and the pending count visible", () => {
    const entry = renderExpandedEntry();
    expect(host.querySelector("[data-main-waiting]")?.textContent).toBe("1");

    act(() => entry.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));

    expect(takeDirectWaitingRequest("main")).toBe(false);
  });

  it("expanded sidebar: opting in preserves the automatic request", () => {
    localStorage.setItem("persona.needsYouAutoOpen", "1");
    const entry = renderExpandedEntry();

    act(() => entry.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));

    expect(takeDirectWaitingRequest("main")).toBe(true);
  });

  async function renderRailEntry() {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://hub.example.com").pathname;
      if (path === "/api/v1/assistant") {
        return Promise.resolve(Response.json({
          machine_id: "hub",
          main: "main",
          conversations: [{ ...main, pending_interactions: 1 }],
        }));
      }
      if (path === "/api/v1/assistant/conversations/main/feed") {
        return Promise.resolve(Response.json({ items: [], plans: {}, task_cards: [] }));
      }
      if (path === "/api/v1/machines") return Promise.resolve(Response.json({ machines: [{ id: "hub", name: "Hub", online: true }] }));
      if (path === "/api/v1/sessions") return Promise.resolve(Response.json({ sessions: [] }));
      if (path === "/api/v1/interactions") return Promise.resolve(Response.json({ interactions: [] }));
      if (path === "/api/v1/todos") return Promise.resolve(Response.json({ todos: [] }));
      return Promise.resolve(Response.json({}));
    }));
    await act(() => refresh());
    act(() => root.render(<SidebarRail route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await act(async () => { await vi.dynamicImportSettled(); });
    const entry = [...host.querySelectorAll<HTMLAnchorElement>("a")].find((a) => a.getAttribute("aria-label") === "Persona 1");
    expect(entry).toBeDefined();
    return entry!;
  }

  it("collapsed rail: a fresh browser suppresses the request and keeps the pending dot", async () => {
    const entry = await renderRailEntry();
    expect(entry.querySelector(".bg-attention")).not.toBeNull();

    act(() => entry.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));

    expect(takeDirectWaitingRequest("main")).toBe(false);
  });

  it("collapsed rail: opting in preserves the automatic request", async () => {
    localStorage.setItem("persona.needsYouAutoOpen", "1");
    const entry = await renderRailEntry();

    act(() => entry.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));

    expect(takeDirectWaitingRequest("main")).toBe(true);
  });

  it("explicit requests are not gated by the sidebar-entry preference", () => {
    expect(needsYouAutoOpen()).toBe(false);
    requestDirectWaiting("main");
    expect(takeDirectWaitingRequest("main")).toBe(true);
  });

  it("storage: invalid or unavailable storage keeps the default off without throwing", () => {
    localStorage.setItem("persona.needsYouAutoOpen", "not-a-boolean");
    expect(needsYouAutoOpen()).toBe(false);

    vi.stubGlobal("localStorage", {
      getItem: () => { throw new Error("blocked"); },
      setItem: () => { throw new Error("blocked"); },
      removeItem: () => { throw new Error("blocked"); },
    });
    expect(needsYouAutoOpen()).toBe(false);
    expect(() => setNeedsYouAutoOpen(true)).not.toThrow();
    expect(needsYouAutoOpen()).toBe(false);
  });
});
