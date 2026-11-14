// @vitest-environment jsdom
// The knowledge list's kind chips carry no count, so nothing changes their width: they paint at once, stay put while a
// kind loads and when another kind is picked (a count on the selected chip only re-wrapped the row and moved the chips
// after it). Cached lists and error recovery stay usable.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { KBEntry, KBKind, KBList } from "../../lib/kb";
import { lists, listKey, searches } from "./cache";
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
const entry: KBEntry = { kind: "docs", id: "guide", name: "Example guide", relevance: 0 };
const tablist = () => host.querySelector('[role="tablist"]');
const selectedTab = () => host.querySelector('[role="tab"][aria-selected="true"]');
const filter = () => host.querySelector('button[aria-label="按标签筛选"]');
const input = () => host.querySelector<HTMLInputElement>('input[aria-label="搜索知识库"]')!;
const render = (kind: KBKind = "docs", reloadKey = 0) => act(() => root.render(
  <ListPane kind={kind} detailOpen={false} reloadKey={reloadKey} onToggleSidebar={() => {}} onOpen={() => {}} />,
));
const answer = async (request: Request, value: unknown) => act(async () => request.resolve(value));
const fail = async (request: Request) => act(async () => request.reject(new TypeError("Failed to fetch")));
const search = (value: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input(), value);
  input().dispatchEvent(new Event("input", { bubbles: true }));
});

beforeEach(() => {
  lists.clear();
  searches.clear();
  requests.length = 0;
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
});

describe("the knowledge list's first paint", () => {
  it("paints the kind chips at once, and the rows under them once the list arrives", async () => {
    render();
    expect(requests[0].path).toBe("/kb/docs");
    expect(input()).not.toBeNull();
    expect(host.textContent).toContain("加载中");
    expect(tablist()).not.toBeNull();
    expect(selectedTab()?.textContent).toBe("文档");
    expect(filter()).not.toBeNull();
    expect(host.textContent).not.toContain("还没有文档");

    await answer(requests[0], { items: [entry], tags: ["example"] });
    expect(selectedTab()?.textContent).toBe("文档");
    expect(filter()).not.toBeNull();
    expect(host.textContent).toContain(entry.name);
    expect(host.textContent).not.toContain("加载中");
  });

  it("finishes loading an empty response", async () => {
    render();
    await answer(requests[0], { items: null, tags: null });
    expect(selectedTab()?.textContent).toBe("文档");
    expect(filter()).not.toBeNull();
    expect(host.textContent).toContain("还没有文档");
    expect(host.textContent).not.toContain("加载中");
  });

  it.each([{ name: "empty", items: [] }, { name: "populated", items: [entry] }])("shows a $name cached list immediately and keeps it during refresh", async ({ items }) => {
    const cached: KBList = { items, tags: [] };
    lists.set(listKey("docs"), cached);
    render();
    expect(selectedTab()?.textContent).toBe("文档");
    expect(host.textContent).toContain(items.length ? entry.name : "还没有文档");
    expect(host.textContent).not.toContain("加载中");
    const ready = host.innerHTML;

    render("docs", 1);
    expect(requests).toHaveLength(2);
    expect(host.innerHTML).toBe(ready);
    await fail(requests[1]);
    expect(host.innerHTML).toBe(ready);
  });

  it("keeps category links and retry available on failure, then paints the recovered list", async () => {
    render();
    await fail(requests[0]);
    expect(tablist()).not.toBeNull();
    expect(host.querySelector('[role="tab"][href="/kb/people"]')).not.toBeNull();
    expect(filter()).not.toBeNull();
    expect(host.textContent).toContain("连不上 hub");
    expect(host.textContent).not.toContain("还没有文档");

    const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === "重试")!;
    act(() => retry.click());
    expect(requests).toHaveLength(2);
    expect(tablist()).not.toBeNull();
    expect(host.textContent).toContain("加载中");
    await answer(requests[1], { items: [entry], tags: [] });
    expect(selectedTab()?.textContent).toBe("文档");
    expect(host.textContent).toContain(entry.name);
    expect(host.textContent).not.toContain("连不上 hub");
  });

  it("allows search before the list arrives and resumes loading when search is cleared", async () => {
    vi.useFakeTimers();
    render();
    search("guide");
    await act(async () => vi.advanceTimersByTime(300));
    expect(requests[1].path).toBe("/kb-search?q=guide&results=1");
    expect(host.textContent).toContain("搜索中");
    await answer(requests[1], { results: [{ entry, count: 1 }] });
    expect(host.textContent).toContain(entry.name);
    expect(tablist()).toBeNull();

    search("");
    expect(host.textContent).toContain("加载中");
    expect(host.textContent).not.toContain(entry.name);
    expect(tablist()).not.toBeNull();
    await answer(requests[0], { items: [entry], tags: [] });
    expect(selectedTab()?.textContent).toBe("文档");
    expect(host.textContent).toContain(entry.name);
  });
});

describe("the kind chips", () => {
  const chips = () => [...host.querySelectorAll('[role="tab"]')].map((a) => a.textContent);

  it("read only their kind, whichever is selected, and stay while the next kind loads", async () => {
    render("docs");
    const names = chips();
    expect(names).toEqual(["项目", "主题", "会话", "人", "文档", "会议", "日程", "录音"]);
    await answer(requests[0], { items: [entry, { ...entry, id: "two" }, { ...entry, id: "three" }], tags: [] });
    expect(chips()).toEqual(names);

    render("recordings");
    expect(selectedTab()?.textContent).toBe("录音");
    expect(chips()).toEqual(names); // the row is still there while recordings load, and no chip changed
    expect(host.textContent).toContain("加载中");
    await answer(requests[1], { items: [], tags: [] });
    expect(chips()).toEqual(names);
    expect(host.textContent).toContain("还没有录音");
  });
});
