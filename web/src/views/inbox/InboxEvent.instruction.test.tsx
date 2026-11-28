// @vitest-environment jsdom
// "Say something" posts an instruction to /inbox/{id}/suggestion and keeps the draft on failure.
// "Try again" posts to /inbox/{id}/retry. A stale retry takes the authoritative item from its own manager,
// or reloads the list when none was returned. Suggestion cards keep their own instruction field.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});

type RequestOptions = { method?: string; json?: unknown; headers?: unknown };

const calls = vi.hoisted(() => ({
  list: [] as ({ path: string } & RequestOptions)[],
  rows: [] as InboxItem[],
  suggestion: undefined as (() => Promise<unknown>) | undefined,
  retry: undefined as (() => Promise<unknown>) | undefined,
}));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: RequestOptions) => {
    calls.list.push({ path, method: init?.method, json: init?.json, headers: init?.headers });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.rows });
    if (path.endsWith("/suggestion")) return calls.suggestion?.() ?? Promise.reject(new Error("offline"));
    if (path.endsWith("/retry")) return calls.retry?.() ?? Promise.reject(new Error("offline"));
    return Promise.resolve({});
  },
}));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined, translatesChat: () => false, translatesMail: () => false, languageName: () => "" }));

import { ApiError } from "../../lib/api";
import type { InboxItem } from "../../lib/inbox";
import { getInbox, reloadInbox, resetInboxForTests } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { getState } from "../../lib/store";
import { toast } from "../../ui/atoms";
import { EventBody } from "./InboxEvent";
import { settleRosterDefault } from "../../lib/manager-choice";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const fixture = JSON.parse(readFileSync(join(__dirname, "../../../../scripts/mock/inbox_events.json"), "utf8")) as { items: InboxItem[] };
const byId = (id: string) => fixture.items.find((x) => x.id === id)!;
// an item the AI could not finish, with no suggestion to answer (B-3's case)
const failed = byId("lark:oc_demo6:600");
// the same item on a hub that sends the state key
const failedNow = { ...byId("lark:oc_demo6:600"), now: "failed" } as InboxItem;
// an open item with no suggestion and no failure: "Say something", not "Try again"
const open = { ...byId("lark:oc_demo3:300") };
const withSuggestion = byId("lark:oc_demo1:100");

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll("button")].map((b) => b.textContent?.trim() ?? "");
const press = async (text: string) => {
  const b = [...host.querySelectorAll("button")].find((x) => x.textContent?.trim() === text);
  if (!b) throw new Error(`no button ${text}: ${buttons().join(" | ")}`);
  await act(async () => b.click());
};
const type = async (text: string) => {
  const input = host.querySelector<HTMLInputElement>("[data-event-say-input]")!;
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(input, text);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  return input;
};
const sent = () => calls.list.filter((c) => c.path.endsWith("/suggestion"));
const retries = () => calls.list.filter((c) => c.path.endsWith("/retry"));
const listReads = () => calls.list.filter((c) => c.path.startsWith("/inbox?sources=all")).length;

beforeEach(() => {
  resetInboxForTests();
  resetMailForTests();
  Object.assign(getState(), { sessions: {} });
  calls.list = [];
  calls.rows = [];
  calls.suggestion = undefined;
  calls.retry = undefined;
  vi.mocked(toast).mockClear();
  host = document.createElement("div");
  document.body.replaceChildren(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
});

describe("收件箱事件：说一句与再试一次", () => {
  it("says something to an open item without a suggestion at version 0, with its manager's header", async () => {
    calls.suggestion = () => Promise.resolve({ ...open, handling: { owner: "manager", state: "looking" } });
    await act(async () => root.render(<EventBody item={{ ...open, manager: "m-work" }} onSource={() => {}} />));
    await press("补充说明");
    expect(host.querySelector("[data-event-say]")).not.toBeNull();
    await type("接着看一下");
    await press("补充说明");
    await flush();
    expect(sent()).toHaveLength(1);
    expect(sent()[0].path).toBe("/inbox/lark:oc_demo3:300/suggestion");
    expect(sent()[0].json).toEqual({ version: 0, action: "instruct", text: "接着看一下", client_id: expect.any(String) });
    expect(sent()[0].headers).toEqual({ "X-Persona-Manager": "m-work" });
    expect(host.querySelector("[data-event-say]")).toBeNull();
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["已交给 AI，正在重新生成建议", "ok"]);
  });

  it("retries the default manager's item without sending instruction text", async () => {
    calls.rows = [failed];
    await act(async () => reloadInbox());
    calls.retry = () => Promise.resolve({ ...failed, handling: { owner: "manager", state: "looking" } });
    await act(async () => root.render(<EventBody item={failed} onSource={() => {}} />));
    await press("再试一次");
    await flush();
    expect(retries()).toEqual([{ path: "/inbox/lark:oc_demo6:600/retry", method: "POST", json: undefined, headers: undefined }]);
    expect(sent()).toHaveLength(0);
    expect(getInbox().items.find((item) => item.id === failed.id)?.handling?.state).toBe("looking");
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["已请 AI 重新处理", "ok"]);
  });

  it("keeps the words typed when the instruction fails", async () => {
    calls.suggestion = () => Promise.reject(new ApiError(502, "AI 暂时连不上", { code: "assistant_offline" }));
    await act(async () => root.render(<EventBody item={open} onSource={() => {}} />));
    await press("补充说明");
    await type("先问时间");
    await press("补充说明");
    await flush();
    expect(host.querySelector<HTMLInputElement>("[data-event-say-input]")?.value).toBe("先问时间");
    expect(host.querySelector("[data-event-say]")).not.toBeNull();
    expect(vi.mocked(toast).mock.calls.at(-1)?.[1]).toBe("error");
    expect(String(vi.mocked(toast).mock.calls.at(-1)?.[0])).toContain("处理建议失败：");
  });

  it("reconciles a stale retry without changing another manager's item of the same ID", async () => {
    const item = { ...failed, manager: "m-work" };
    calls.rows = [item, { ...failed, manager: "m-other" }];
    await act(async () => reloadInbox());
    const reads = listReads();
    const fresh = { ...failed, suggestion: { version: 3, kind: "reply", text: "好", at: "2026-01-01T09:00:00Z" } };
    calls.retry = () => Promise.reject(new ApiError(409, "建议已更新", { code: "inbox_retry_stale", item: fresh }));
    await act(async () => root.render(<EventBody item={item} onSource={() => {}} />));
    await press("再试一次");
    await flush();
    expect(retries()).toMatchObject([{ path: "/inbox/lark:oc_demo6:600/retry", method: "POST", headers: { "X-Persona-Manager": "m-work" } }]);
    expect(getInbox().items.find((row) => row.manager === "m-work")?.suggestion).toEqual(fresh.suggestion);
    expect(getInbox().items.find((row) => row.manager === "m-other")?.suggestion).toBeUndefined();
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["建议已更新", "info"]);
    expect(listReads()).toBe(reads);
  });

  it("reloads the inbox if a stale retry carries no authoritative item", async () => {
    calls.retry = () => Promise.reject(new ApiError(409, "建议已更新", { code: "inbox_retry_stale" }));
    await act(async () => root.render(<EventBody item={failed} onSource={() => {}} />));
    const reads = listReads();
    await press("再试一次");
    await flush();
    expect(retries()).toHaveLength(1);
    expect(listReads()).toBe(reads + 1);
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["建议已更新", "info"]);
  });

  it("shows neither button with a suggestion card, nor on a done item", async () => {
    await act(async () => root.render(<EventBody item={withSuggestion} onSource={() => {}} />));
    expect(buttons()).not.toContain("补充说明");
    expect(buttons()).not.toContain("再试一次");
    await act(async () => root.render(<EventBody item={{ ...withSuggestion, suggestion: undefined, done_at: "2026-01-01T10:00:00Z", done_by: "user" }} onSource={() => {}} />));
    expect(buttons()).not.toContain("补充说明");
    expect(buttons()).not.toContain("再试一次");
  });

  it("takes retry from the state key alone, and says a word from the handling state alone", async () => {
    await act(async () => root.render(<EventBody item={failedNow} onSource={() => {}} />));
    expect(buttons()).toContain("再试一次");
    expect(buttons()).not.toContain("补充说明");
    await act(async () => root.render(<EventBody item={{ ...failed, handling: undefined, now: "needs_you" }} onSource={() => {}} />));
    expect(buttons()).toContain("补充说明");
    expect(buttons()).not.toContain("再试一次");
  });
});
