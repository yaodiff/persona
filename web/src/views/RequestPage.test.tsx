// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setToken } from "../lib/api";
import { getState, refresh, reset } from "../lib/store";
import serializedEdit from "../lib/testdata/codex-file-approval.json";
import { RequestPage } from "./RequestPage";
import type { Interaction } from "../lib/types";
import { loadPages } from "../lib/lazy-page";

await loadPages();

let cards: Interaction[];
let writes: { url: string; body: unknown }[];
let root: ReturnType<typeof createRoot>;
let host: HTMLDivElement;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setToken("fixture-token");
  reset();
  writes = [];
  cards = [{ id: "script-request", machine_id: "example-computer", session_id: "", kind: "permission", tool_name: "personad_ask", summary: "Publish the draft?" }];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://example.com").pathname;
    if (init?.method === "POST") {
      writes.push({ url: path, body: JSON.parse(String(init.body)) });
      return new Response("{}");
    }
    const value = path.endsWith("/interactions") ? { interactions: cards }
      : path.endsWith("/sessions") ? { sessions: [] } : path.endsWith("/machines") ? { machines: [] } : {};
    return new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await refresh();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

it("renders, answers and shows the receipt through the real store and API", async () => {
  act(() => root.render(<RequestPage id="script-request" onToggleSidebar={() => {}} />));
  await flush();
  expect(host.textContent).toContain("Publish the draft?");
  expect(host.querySelector("fieldset[disabled]")).toBeNull();
  const allow = [...host.querySelectorAll("button")].find((button) => button.textContent?.trim() === "允许");
  expect(allow).toBeDefined();
  act(() => allow!.click());
  expect(writes).toEqual([]);
  const confirm = [...host.querySelectorAll("button")].find(button => button.textContent === "确认允许");
  act(() => confirm!.click());
  await flush();
  expect(writes).toEqual([{ url: "/api/v1/interactions/script-request/resolve", body: { allow: true } }]);
  expect(getState().interactions).toEqual([]);
  expect(host.querySelector('[data-answered="script-request"]')).not.toBeNull();
});

it("shows an ended state for an unavailable id", async () => {
  act(() => root.render(<RequestPage id="no-longer-pending" onToggleSidebar={() => {}} />));
  await flush();
  expect(host.textContent).toContain("此请求已结束。");
  expect(writes).toEqual([]);
});

it("can cancel, opt out and restore confirmation for the same outward request", async () => {
  const { setConfirmationEnabled } = await import("../lib/approval-confirmation");
  const { t } = await import("../i18n");
  cards[0] = { ...cards[0], tool_name: "Bash", tool_input: { command: "git push" } };
  await refresh();
  const click = (key: string) => act(() => {
    [...host.querySelectorAll("button")].find(button => button.textContent === t(key))!.click();
  });
  try {
    act(() => root.render(<RequestPage id="script-request" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).toContain(t("approval-impact.push"));
    click("chat-interactions.allow");
    click("common.cancel");
    expect(writes).toEqual([]);
    act(() => setConfirmationEnabled(false));
    expect(host.textContent).not.toContain(t("approval-impact.push"));
    act(() => setConfirmationEnabled(true));
    expect(host.textContent).toContain(t("approval-impact.push"));
    act(() => setConfirmationEnabled(false));
    click("chat-interactions.allow");
    await flush();
    expect(writes.map(write => write.body)).toEqual([{ allow: true }]);
  } finally {
    act(() => setConfirmationEnabled(true));
  }
});

it("keeps the switch reversible on this page when the browser refuses storage writes", async () => {
  const { confirmationEnabled, setConfirmationEnabled } = await import("../lib/approval-confirmation");
  const refuse = () => { throw new DOMException("Unavailable", "SecurityError"); };
  const set = vi.spyOn(Storage.prototype, "setItem").mockImplementation(refuse);
  const remove = vi.spyOn(Storage.prototype, "removeItem").mockImplementation(refuse);
  try {
    setConfirmationEnabled(false);
    expect(confirmationEnabled()).toBe(false);
    setConfirmationEnabled(true);
    expect(confirmationEnabled()).toBe(true);
  } finally {
    set.mockRestore();
    remove.mockRestore();
    setConfirmationEnabled(true);
  }
});

it.each([
  { title: "absolute local write", fields: { tool_name: "Write", tool_input: { file_path: "/project/draft.txt" } }, review: false },
  { title: "shared write", fields: { tool_name: "Write", tool_input: { file_path: "//shared/project/draft.txt" } }, review: true },
  { title: "relative write in a shared directory", fields: { tool_name: "Write", cwd: "//shared/project", tool_input: { file_path: "draft.txt" } }, review: true },
  { title: "multi-file Codex edit", fields: serializedEdit, review: true },
  { title: "multi-file edit with scopes omitted", fields: { ...serializedEdit, always_keys: undefined }, review: true },
])("submits one approval for a $title", async ({ fields, review }) => {
  const { t } = await import("../i18n");
  cards[0] = { ...cards[0], ...fields };
  await refresh();
  act(() => root.render(<RequestPage id="script-request" onToggleSidebar={() => {}} />));
  await flush();
  const click = (key: string) => act(() => [...host.querySelectorAll("button")].find(b => b.textContent === t(key))!.click());
  click("chat-interactions.allow");
  await flush();
  if (review) {
    expect(writes).toEqual([]);
    click("approval-impact.confirm");
    await flush();
  }
  expect(writes).toEqual([{ url: "/api/v1/interactions/script-request/resolve", body: { allow: true } }]);
});
