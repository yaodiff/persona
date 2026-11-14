// @vitest-environment jsdom
// The Trail must not depend on a tree answer that has both lists: a hub (or a page's fake) answering the tree route with
// `{}` leaves the section empty, as before, instead of throwing away the whole item page. And "Same matter as…" offers
// open items only, filtered as you type, and puts the choice on this item with the bare parent object.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { InboxItem, InboxTree } from "../../lib/inbox";

const holder = vi.hoisted(() => ({ items: [] as unknown[], requests: [] as { path: string; init?: { method?: string; json?: unknown; headers?: Record<string, string> } }[] }));

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown; headers?: Record<string, string> }) => {
    holder.requests.push({ path, init });
    // a tree answer without `ancestors` / `children`: what a page's fake hub gives
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/inbox", async (orig) => ({
  ...(await orig<typeof import("../../lib/inbox")>()),
  useInbox: <T,>(select: (s: { items: unknown[] }) => T) => select({ items: holder.items }),
}));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { InboxLine } from "./InboxLine";
import { InboxSameMatter } from "./InboxSameMatter";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string, title: string, over: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "examplechat", kind: "group", account: "examplechat", account_name: "Example Chat", title, preview: "", count: 1, ...over,
});
const self = item("self", "本件", { manager: "m-1", updated_at: "2026-01-01T00:00:00Z" });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  holder.items = [];
  holder.requests = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("收件箱：线索对树的形状", () => {
  it("draws nothing, and keeps the page, when the tree answer has no ancestors or children", async () => {
    act(() => root.render(<InboxLine item={self} />));
    await flush();
    expect(holder.requests.some((r) => r.path === "/inbox/tree/inbox/self")).toBe(true);
    expect(host.querySelector("[data-inbox-line]")).toBeNull();
  });
});

describe("收件箱：是同一件事…", () => {
  const tree: InboxTree = {
    node: { kind: "inbox", id: "self" },
    item: self,
    ancestors: [],
    children: [item("child", "自己的下级", { manager: "m-1" })],
  };
  const show = async () => {
    holder.items = [
      self,
      item("child", "自己的下级", { manager: "m-1" }),
      item("done", "已处理", { manager: "m-1", done_at: "2026-01-01T00:00:00Z" }),
      item("other", "别的管理者", { manager: "m-2" }),
    ];
    act(() => root.render(<InboxSameMatter open item={self} tree={tree} onClose={() => {}} />));
    await flush();
  };
  const rows = () => [...document.body.querySelectorAll<HTMLButtonElement>("[data-same-matter]")];
  const search = () => document.body.querySelector<HTMLInputElement>("input")!;

  it("offers this manager's open items but not itself or its children", async () => {
    await show();
    expect(rows()).toHaveLength(0);
  });

  it("filters as the user types, by title", async () => {
    holder.items = [self, item("a", "发布准备", { manager: "m-1" }), item("b", "设计评审", { manager: "m-1" })];
    act(() => root.render(<InboxSameMatter open item={self} tree={tree} onClose={() => {}} />));
    await flush();
    expect(rows().map((r) => r.dataset.sameMatter)).toEqual(["a", "b"]);
    act(() => {
      const field = search();
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(field, "评审");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush();
    expect(rows().map((r) => r.dataset.sameMatter)).toEqual(["b"]);
  });

  it("puts the pick on this item with the bare parent object, then closes", async () => {
    const onClose = vi.fn();
    holder.items = [self, item("a", "发布准备", { manager: "m-1" })];
    act(() => root.render(<InboxSameMatter open item={self} tree={tree} onClose={onClose} />));
    await flush();
    await act(async () => { rows()[0].click(); });
    await flush();
    const put = holder.requests.find((r) => r.init?.method === "PUT")!;
    expect(put.path).toBe("/inbox/self/parent");
    expect(JSON.stringify(put.init?.json)).toBe('{"kind":"inbox","id":"a"}');
    expect(put.init?.headers).toEqual({ "X-Persona-Manager": "m-1" });
    expect(onClose).toHaveBeenCalled();
  });
});
