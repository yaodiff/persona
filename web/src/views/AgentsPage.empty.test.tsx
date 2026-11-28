// @vitest-environment jsdom
// The Agents page with no computer (docs/assistant/onboarding.md 4.5, #4485): the machines page's "No computers yet" + "Add computer", not a
// bare "Agents by machine" title over nothing; with a computer the list is as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
  g.matchMedia = (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} });
});
import { getState } from "../lib/store";
import { AgentsList } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}"))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  Object.assign(getState(), { loaded: false, machines: [] });
  vi.unstubAllGlobals();
});

describe("Agents page with no computer", () => {
  it("shows 还没有电脑 with 添加电脑", () => {
    Object.assign(getState(), { loaded: true, machines: [] });
    act(() => root.render(<AgentsList route={{ name: "agent-list" }} onNewAgent={() => {}} />));
    const empty = host.querySelector('[data-testid="no-computers"]');
    expect(empty?.textContent).toContain("尚未添加电脑");
    expect([...empty!.querySelectorAll("button")].map((b) => b.textContent?.trim())).toContain("添加电脑");
  });

  it("lists as before once there is a computer", () => {
    Object.assign(getState(), { loaded: true, machines: [{ id: "m", name: "我的 MacBook", online: true }] });
    act(() => root.render(<AgentsList route={{ name: "agent-list" }} onNewAgent={() => {}} />));
    expect(host.querySelector('[data-testid="no-computers"]')).toBeNull();
    expect(host.textContent).toContain("Agents");
  });
});
