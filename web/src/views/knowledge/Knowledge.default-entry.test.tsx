// @vitest-environment jsdom
// The knowledge base beside its list (#16467): with no entry in the address, the detail pane shows the list's first
// entry, as To-dos does. It is a fallback only: a named entry wins, an empty list keeps the empty pane, and neither the
// address nor any read/seen state changes.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { setToken } from "../../lib/api";
import type { KBEntry } from "../../lib/kb";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { getState, reset } from "../../lib/store";
import { ONE_PANE } from "../../lib/viewport";
import { lists, searches } from "./cache";

type Call = { path: string; init: RequestInit };
const state = vi.hoisted(() => ({
  calls: [] as Call[],
  items: [] as KBEntry[],
  navigate: vi.fn(),
}));

vi.mock("../../lib/router", async (original) => ({
  ...(await original<typeof import("../../lib/router")>()),
  navigate: (...args: unknown[]) => state.navigate(...args),
}));
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string, init: RequestInit = {}) => {
    state.calls.push({ path, init });
    if (path === "/kb/docs") return Promise.resolve({ items: state.items, tags: [] });
    const detail = state.items.find((e) => path === `/kb/docs/${e.id}`);
    return Promise.resolve(detail ? { detail: { kind: detail.kind, id: detail.id, path: "", meta: { name: detail.name }, summary: `# ${detail.name}` } } : {});
  },
}));

import { Knowledge } from "./Knowledge";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const first: KBEntry = { kind: "docs", id: "first", name: "First guide", relevance: 0 };
const second: KBEntry = { kind: "docs", id: "second", name: "Second guide", relevance: 0 };

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 16; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (props: { kind?: string; id?: string }) => {
  await act(async () => root.render(<Knowledge {...props} onToggleSidebar={() => {}} />));
  await flush();
};
const detailPane = () => host.firstElementChild!.children[1] as HTMLElement;
const detailReads = () => state.calls.filter((c) => c.path.startsWith("/kb/docs/"));

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  reset();
  resetManagerChoice();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  state.calls.length = 0;
  state.items = [first, second];
  state.navigate.mockReset();
  lists.clear();
  searches.clear();
  history.replaceState(null, "", "/kb/docs");
  Element.prototype.scrollTo = vi.fn() as unknown as Element["scrollTo"];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  history.replaceState(null, "", "/");
  lists.clear();
  searches.clear();
  resetManagerChoice();
});

it("shows the first list entry when the address names none", async () => {
  await render({ kind: "docs" });
  expect(detailReads().map((c) => c.path)).toEqual(["/kb/docs/first"]);
  expect(detailPane().textContent).toContain("First guide");
  expect(detailPane().querySelector("[data-testid=kb-empty-detail-bar]")).toBeNull();
});

it("keeps the empty detail pane when the list has no entries", async () => {
  state.items = [];
  await render({ kind: "docs" });
  expect(detailReads()).toEqual([]);
  expect(detailPane().querySelector("[data-testid=kb-empty-detail-bar]")).not.toBeNull();
  expect(detailPane().textContent).toContain(t("knowledge-kbdetail.empty_title"));
});

it("keeps an entry named by the address in front of the list's first", async () => {
  await render({ kind: "docs", id: "second" });
  expect(detailReads().map((c) => c.path)).toEqual(["/kb/docs/second"]);
  expect(detailPane().textContent).toContain("Second guide");
  expect(detailPane().textContent).not.toContain("First guide");
});

it("changes neither the address nor read/seen state for the fallback", async () => {
  const before = location.href;
  const depth = history.length;
  const seen = getState().seen;
  await render({ kind: "docs" });
  expect(location.href).toBe(before);
  expect(history.length).toBe(depth);
  expect(state.navigate).not.toHaveBeenCalled();
  expect(getState().seen).toEqual(seen);
  expect(state.calls.every((c) => !c.init.method || c.init.method === "GET")).toBe(true);
  expect(state.calls.some((c) => /(?:^|\/)(?:read|seen)(?:\/|$)/.test(c.path))).toBe(false);
});

it("does not stand in on a narrow screen", async () => {
  const original = window.matchMedia;
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn((query: string) => ({ matches: query === ONE_PANE, addEventListener() {}, removeEventListener() {} })),
  });
  try {
    await render({ kind: "docs" });
    expect(detailReads()).toEqual([]);
    expect(detailPane().querySelector("[data-testid=kb-empty-detail-bar]")).not.toBeNull();
  } finally {
    Object.defineProperty(window, "matchMedia", { configurable: true, writable: true, value: original });
  }
});
