// @vitest-environment jsdom
// Which list rows get a state line (#6611 E11, DECISIONS D3 "四端对比后统一": every row with a known `now` draws one) and
// which keep hiding it: an FYI row the AI filed without handling, a done row filed by the user, an older hub's row, and a
// key this app does not know (no empty line left behind).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const hub = vi.hoisted(() => ({ items: [] as unknown[] }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "a1", email: "me@example.com" }] });
    if (path.startsWith("/inbox")) return Promise.resolve({ items: structuredClone(hub.items) });
    if (path === "/sessions") return Promise.resolve({ sessions: [] });
    if (path === "/interactions") return Promise.resolve({ interactions: [] });
    if (path === "/machines") return Promise.resolve({ machines: [] });
    return Promise.resolve({});
  },
}));

import { reloadInbox, resetInboxForTests } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { refresh, reset } from "../../lib/store";
import { InboxList } from "./InboxList";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string, patch: Record<string, unknown> = {}) => ({
  id, source: "chat", kind: "group", account: "chat", account_name: "Team chat", title: id, preview: `${id} 的最新一句`,
  count: 1, tier: "action", updated_at: "2026-05-01T10:00:00Z", ...patch,
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const rowOf = (id: string) => host.querySelector(`[data-item="${id}"]`)!;

beforeEach(async () => {
  localStorage.clear();
  resetInboxForTests();
  resetMailForTests();
  // an FYI row the AI filed (no handling, no suggestion): its state line used to be hidden
  // a done row the user replied in the source: the manager's key says "你回过了"
  // an unknown key, and an older hub's plain row
  hub.items = [
    item("fyi", { tier: "fyi", now: "fyi" }),
    item("replied", { now: "done_replied", done_at: "2026-05-01T11:00:00Z", done_by: "replied" }),
    item("folded", { tier: "noise", now: "folded" }),
    item("future", { now: "a_key_from_a_newer_hub" }),
  ];
  await act(() => refresh());
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<InboxList />));
  await flush();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

describe("收件箱列表：有 now 的行都画状态行", () => {
  it("draws the FYI row's own state line, with no leading icon", async () => {
    const mark = rowOf("fyi").querySelector('[data-event-mark="now"]')!;
    expect(mark).not.toBeNull();
    expect(mark.textContent).toBe("知悉即可");
    expect(mark.getAttribute("data-event-icon")).toBe("none");
  });

  it("draws a done row filed in the source, not by the manager", async () => {
    // 已处理 folds by default: open it
    const toggle = [...host.querySelectorAll("button")].find((b) => (b.textContent ?? "").startsWith("已处理"));
    if (toggle) await act(async () => toggle.click());
    await act(async () => { void reloadInbox(); });
    await flush();
    const mark = rowOf("replied").querySelector('[data-event-mark="now"]')!;
    expect(mark.textContent).toBe("你回过了");
    expect(mark.getAttribute("data-event-icon")).toBe("check");
  });

  it("draws a folded row's state line too, with no leading icon", async () => {
    const toggle = [...host.querySelectorAll("button")].find((b) => (b.textContent ?? "").startsWith("已折叠"));
    if (toggle) await act(async () => toggle.click());
    const mark = rowOf("folded").querySelector('[data-event-mark="now"]')!;
    expect(mark.textContent).toBe("已折叠");
    expect(mark.getAttribute("data-event-icon")).toBe("none");
  });

  it("leaves an unknown key exactly as an older hub's row, with no empty line", async () => {
    expect(rowOf("future").querySelector("[data-event-mark]")).toBeNull();
    // and no leftovers from an empty state line: the row's own words are the only added lines
    expect(rowOf("future").querySelectorAll("p").length).toBe(0);
  });
});
