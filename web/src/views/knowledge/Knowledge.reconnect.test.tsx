// @vitest-environment jsdom
// The knowledge pages after the hub comes back: a list, search, map or entry that could not be read while it was away is
// read again on the next hello, as Retry would, and the error goes once that read works. Reads that went through are
// not repeated on a hello.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../../lib/api";
import type { KBEntry } from "../../lib/kb";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { lists, searches } from "./cache";

type Request = { path: string; resolve: (value: unknown) => void; reject: (error: Error) => void };
const hub = vi.hoisted(() => ({ requests: [] as Request[], hello: new Set<(msg: Record<string, unknown>) => void>() }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init: RequestInit = {}) => new Promise((resolve, reject) => {
    hub.requests.push({ path, resolve, reject });
    init.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
  }),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    if (type !== "hello") return () => {};
    hub.hello.add(l);
    return () => void hub.hello.delete(l);
  },
}));

import { EntryDetail } from "./KBDetail";
import { ListPane } from "./KBList";
import { KnowledgeMap } from "./KnowledgeMap";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const entry: KBEntry = { kind: "docs", id: "guide", name: "Example guide", relevance: 0 };
const pending = (path: string) => hub.requests.filter((r) => r.path === path);
const answer = async (r: Request, value: unknown) => act(async () => r.resolve(value));
const fail = async (r: Request) => act(async () => r.reject(new TypeError("Failed to fetch")));
const hello = async (msg: Record<string, unknown> = { type: "hello" }) => act(async () => hub.hello.forEach((l) => l(msg)));
const offline = () => host.textContent?.includes("连不上 hub") ?? false;

beforeEach(() => {
  lists.clear();
  searches.clear();
  hub.requests.length = 0;
  hub.hello.clear();
  localStorage.clear();
  resetManagerChoice();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  Element.prototype.scrollTo = vi.fn() as unknown as Element["scrollTo"];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.useRealTimers();
  lists.clear();
  searches.clear();
  resetManagerChoice();
});

describe("the knowledge list", () => {
  const render = () => act(() => root.render(<ListPane kind="docs" detailOpen={false} reloadKey={0} onToggleSidebar={() => {}} onOpen={() => {}} />));

  it("reads a list that failed while the hub was away again on the next hello, and drops the error", async () => {
    render();
    await fail(pending("/kb/docs")[0]);
    expect(offline()).toBe(true);

    await hello();
    expect(pending("/kb/docs")).toHaveLength(2);
    await answer(pending("/kb/docs")[1], { items: [entry], tags: [] });
    expect(offline()).toBe(false);
    expect(host.textContent).toContain(entry.name);

    // read: a later hello does not read it again
    await hello({ type: "hello", resumed: false });
    expect(pending("/kb/docs")).toHaveLength(2);
  });

  it("searches again on the next hello when a search failed", async () => {
    vi.useFakeTimers();
    render();
    await answer(pending("/kb/docs")[0], { items: [entry], tags: [] });
    const input = host.querySelector<HTMLInputElement>('input[aria-label="搜索知识库"]')!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "guide");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => vi.advanceTimersByTime(300));
    const path = "/kb-search?q=guide&results=1";
    await fail(pending(path)[0]);
    expect(offline()).toBe(true);

    await hello();
    await act(async () => vi.advanceTimersByTime(300));
    expect(pending(path)).toHaveLength(2);
    await answer(pending(path)[1], { results: [{ entry, count: 1 }] });
    expect(offline()).toBe(false);
    expect(host.textContent).toContain(entry.name);
    expect(pending("/kb/docs")).toHaveLength(1); // the list was read: the hello did not read it again
  });
});

describe("the knowledge map", () => {
  it("reads a map that failed while the hub was away again on the next hello", async () => {
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await fail(pending("/kb/topics")[0]);
    await answer(pending("/assistant")[0], {});
    expect(offline()).toBe(true);

    await hello();
    expect(pending("/kb/topics")).toHaveLength(2);
    await answer(pending("/kb/topics")[1], { items: [] });
    await answer(pending("/assistant")[1], { kb_topics: true });
    expect(offline()).toBe(false);
    expect(host.textContent).toContain("还没有");

    // drawn: a later hello does not read every entry again
    await hello();
    expect(pending("/kb/topics")).toHaveLength(2);
  });

  it("reads a map drawn without some entries again on the next hello, keeping it drawn meanwhile", async () => {
    const topics = { items: [{ kind: "topics", id: "alpha", name: "Alpha topic", relevance: 0 }, { kind: "topics", id: "beta", name: "Beta topic", relevance: 0 }] };
    const detail = (id: string) => ({ detail: { kind: "topics", id, path: "", meta: {}, summary: "" } });
    const missing = () => host.textContent?.includes("有 1 个主题没读到") ?? false;
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await answer(pending("/assistant")[0], {});
    await answer(pending("/kb/topics")[0], topics);
    await answer(pending("/kb/topics/alpha")[0], detail("alpha"));
    await fail(pending("/kb/topics/beta")[0]);
    expect(missing()).toBe(true);
    expect(host.textContent).toContain("Beta topic");

    await hello();
    expect(pending("/kb/topics")).toHaveLength(2);
    // reading: the map stays, with no Loading in its place
    expect(host.querySelector('[role="status"]')).toBeNull();
    expect(host.textContent).toContain("Alpha topic");
    // a read that fails again keeps the map as it was
    await fail(pending("/kb/topics")[1]);
    await answer(pending("/assistant")[1], {});
    expect(offline()).toBe(false);
    expect(missing()).toBe(true);

    await hello();
    expect(pending("/kb/topics")).toHaveLength(3);
    await answer(pending("/assistant")[2], {});
    await answer(pending("/kb/topics")[2], topics);
    await answer(pending("/kb/topics/alpha")[1], detail("alpha"));
    await answer(pending("/kb/topics/beta")[1], detail("beta"));
    expect(missing()).toBe(false);
    expect(host.textContent).toContain("Beta topic");

    // whole now: a later hello does not read every entry again
    await hello();
    expect(pending("/kb/topics")).toHaveLength(3);
  });
});

describe("an entry's page", () => {
  it("reads an entry that failed while the hub was away again on the next hello", async () => {
    const path = "/kb/people/example";
    act(() => root.render(<EntryDetail kind="people" id="example" reloadKey={0} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={() => {}} />));
    await fail(pending(path)[0]);
    expect(offline()).toBe(true);

    await hello();
    expect(pending(path)).toHaveLength(2);
    await answer(pending(path)[1], { detail: { kind: "people", id: "example", path: "", meta: { name: "Example person" }, summary: "# Example person" } });
    expect(offline()).toBe(false);
    expect(host.textContent).toContain("Example person");

    await hello();
    expect(pending(path)).toHaveLength(2);
  });
});
