import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, errorText } from "../../lib/api";
import type { InboxItem } from "../../lib/inbox";

const calls = vi.hoisted(() => ({ requests: [] as { path: string; init?: { method?: string; json?: unknown; headers?: Record<string, string> } }[], fail: null as unknown }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown; headers?: Record<string, string> }) => {
    calls.requests.push({ path, init });
    return calls.fail ? Promise.reject(calls.fail) : Promise.resolve({});
  },
}));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { toast } from "../../ui/atoms";
import { detachInboxParent, linkInboxParent, onInboxParentChanged } from "./InboxParent";

const item = (id: string, title: string, over: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "examplechat", kind: "group", account: "examplechat", account_name: "Example Chat", title, preview: "", count: 1, ...over,
});

beforeEach(() => {
  calls.requests = [];
  calls.fail = null;
  vi.mocked(toast).mockClear();
});

describe("线索的父子关系请求", () => {
  it("detaches the lower item with a literal null body, its manager header and the exact success toast", async () => {
    await expect(detachInboxParent({ id: "lark:F 1:900", manager: "m-1" })).resolves.toBe(true);
    expect(calls.requests).toHaveLength(1);
    const [{ path, init }] = calls.requests;
    expect(path).toBe("/inbox/lark:F%201:900/parent");
    expect(init?.method).toBe("PUT");
    expect(JSON.stringify(init?.json)).toBe("null");
    expect(init?.headers).toEqual({ "X-Persona-Manager": "m-1" });
    expect(toast).toHaveBeenCalledWith("已标为不是同一件事", "ok");
  });

  it("links this item with the bare parent object and names the chosen title", async () => {
    await expect(linkInboxParent({ id: "child", manager: "m-1" }, item("parent", "发布准备"))).resolves.toBe(true);
    const [{ path, init }] = calls.requests;
    expect(path).toBe("/inbox/child/parent");
    expect(init?.method).toBe("PUT");
    expect(JSON.stringify(init?.json)).toBe('{"kind":"inbox","id":"parent"}');
    expect(init?.headers).toEqual({ "X-Persona-Manager": "m-1" });
    expect(toast).toHaveBeenCalledWith("已连到「发布准备」", "ok");
  });

  it("notifies a visible Trail after a change from either entry point", async () => {
    const listener = vi.fn();
    const off = onInboxParentChanged(listener);
    try {
      await linkInboxParent({ id: "child" }, item("parent", "发布准备"));
      expect(listener).toHaveBeenCalledOnce();
    } finally {
      off();
    }
  });

  it.each([
    new ApiError(409, "that parent would make a cycle", { code: "conflict", error: "that parent would make a cycle" }),
    new ApiError(409, "HTTP 409", {}),
    new ApiError(409, "different wording", { code: "other", error: "different wording" }),
  ])("shows the localized conflict explanation for every 409", async (error) => {
    calls.fail = error;

    await expect(linkInboxParent({ id: "child" }, item("parent", "发布准备"))).resolves.toBe(false);
    expect(toast).toHaveBeenCalledExactlyOnceWith("这个关联不成立，请选择其他内容。", "error");
  });

  it.each([400, 404])("keeps the existing error text for HTTP %i", async (status) => {
    const error = new ApiError(status, `failure ${status}`, { code: "conflict", error: `failure ${status}` });
    calls.fail = error;

    await expect(linkInboxParent({ id: "child" }, item("parent", "发布准备"))).resolves.toBe(false);
    expect(toast).toHaveBeenCalledExactlyOnceWith(errorText(error), "error");
  });

  it.each(["detach", "link"] as const)("uses the existing error toast on %s failure and does not claim success", async (action) => {
    calls.fail = new Error("offline");
    const ok = action === "detach"
      ? await detachInboxParent({ id: "child" })
      : await linkInboxParent({ id: "child" }, item("parent", "发布准备"));
    expect(ok).toBe(false);
    expect(toast).toHaveBeenCalledOnce();
    expect(toast).toHaveBeenCalledWith("offline", "error");
  });
});
