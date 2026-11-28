// @vitest-environment jsdom
// A failed knowledge search shows its error and a retry, never the previous query's results under the new text.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { KBEntry } from "../../lib/kb";
import { lists, searches } from "./cache";
import { ListPane } from "./KBList";

type Request = { path: string; resolve: (value: unknown) => void; reject: (error: Error) => void };
const requests: Request[] = [];
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init: RequestInit = {}) => new Promise((resolve, reject) => {
    requests.push({ path, resolve, reject });
    init.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
  }),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const older: KBEntry = { kind: "docs", id: "older", name: "Older match", relevance: 0 };
const newer: KBEntry = { kind: "docs", id: "newer", name: "Newer match", relevance: 0 };
const input = () => host.querySelector<HTMLInputElement>('input[aria-label="搜索知识库"]')!;
const retryButton = () => [...host.querySelectorAll("button")].find((b) => b.textContent === "重试");
const last = () => requests[requests.length - 1];
const render = () => act(() => root.render(
  <ListPane kind="docs" detailOpen={false} reloadKey={0} onToggleSidebar={() => {}} onOpen={() => {}} />,
));
const answer = async (request: Request, value: unknown) => act(async () => request.resolve(value));
const fail = async (request: Request) => act(async () => request.reject(new TypeError("Failed to fetch")));
const type = (value: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input(), value);
  input().dispatchEvent(new Event("input", { bubbles: true }));
});
const debounce = () => act(async () => vi.advanceTimersByTime(300));

/** Searches `value` and returns its request once the debounce has sent it. */
const search = async (value: string) => {
  type(value);
  await debounce();
  const r = last();
  expect(r.path).toBe(`/kb-search?q=${value}&results=1`);
  return r;
};

beforeEach(async () => {
  vi.useFakeTimers();
  lists.clear();
  searches.clear();
  requests.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  render();
  await answer(requests[0], { items: [], tags: [] });
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.useRealTimers();
  lists.clear();
  searches.clear();
});

describe("a failed knowledge search", () => {
  it("shows the error instead of the previous query's results, and retry finds the new query", async () => {
    await answer(await search("ab"), { results: [{ entry: older, count: 1 }] });
    expect(host.textContent).toContain(older.name);

    // While the new query runs, the old results stay dimmed.
    type("abc");
    expect(host.textContent).toContain(older.name);
    await debounce();
    await fail(last());
    expect(host.textContent).toContain("无法连接 hub");
    expect(host.textContent).not.toContain(older.name);

    act(() => retryButton()!.click());
    await debounce();
    expect(last().path).toBe("/kb-search?q=abc&results=1");
    await answer(last(), { results: [{ entry: newer, count: 1 }] });
    expect(host.textContent).toContain(newer.name);
    expect(host.textContent).not.toContain(older.name);
    expect(host.textContent).not.toContain("无法连接 hub");
  });

  it("shows the error instead of the previous query's empty result", async () => {
    await answer(await search("ab"), { results: [] });
    expect(host.textContent).toContain("未找到与「ab」相关的结果");

    await fail(await search("abc"));
    expect(host.textContent).toContain("无法连接 hub");
    expect(host.textContent).not.toContain("未找到与");
    expect(retryButton()).toBeDefined();
  });

  it("offers retry on a first search that fails", async () => {
    await fail(await search("ab"));
    expect(host.textContent).toContain("无法连接 hub");
    act(() => retryButton()!.click());
    await debounce();
    await answer(last(), { results: [{ entry: newer, count: 1 }] });
    expect(host.textContent).toContain(newer.name);
    expect(host.textContent).not.toContain("无法连接 hub");
  });
});
