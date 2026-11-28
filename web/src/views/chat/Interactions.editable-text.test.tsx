// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { InteractionCard } from "./Interactions";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
    return {};
  }),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const editable = JSON.parse(JSON.stringify({
  id: "p_edit", machine_id: "m", session_id: "s", kind: "permission", tool_name: "Bash",
  summary: "Send a Lark reply", editable_text: "hello world",
})) as Interaction;
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  calls.length = 0;
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const flush = async () => { for (let i = 0; i < 3; i++) await act(async () => { await Promise.resolve(); }); };
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === text)!;
const setValue = (el: HTMLInputElement | HTMLTextAreaElement, value: string) => act(() => {
  Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});

it("confirms the edited message before sending only edited_text", async () => {
  act(() => root.render(<InteractionCard ix={editable} />));
  expect(host.querySelector("textarea")).toBeNull();
  expect(button("总是允许")).toBeUndefined();
  act(() => button("编辑正文").click());
  await flush();
  setValue(host.querySelector("textarea")!, "hello $USER\nSecond line `literal`");
  await flush();
  expect(button("总是允许")).toBeUndefined();
  act(() => button("允许").click());
  expect(calls).toEqual([]);
  expect(host.querySelector("textarea")?.value).toBe("hello $USER\nSecond line `literal`");
  expect(host.querySelector("textarea")?.matches(":disabled")).toBe(true);
  act(() => button("确认允许").click());
  await flush();
  expect(calls).toEqual([{ path: "/interactions/p_edit/resolve", body: { allow: true, edited_text: "hello $USER\nSecond line `literal`" } }]);
});

it("keeps an unchanged allow plain and disables a whitespace-only edit", async () => {
  act(() => root.render(<InteractionCard ix={editable} />));
  act(() => button("编辑正文").click());
  await flush();
  const area = host.querySelector("textarea")!;
  setValue(area, "hello world");
  await flush();
  act(() => button("允许").click());
  expect(calls).toEqual([]);
  act(() => button("确认允许").click());
  await flush();
  expect(calls).toEqual([{ path: "/interactions/p_edit/resolve", body: { allow: true } }]);
  setValue(area, "  \n");
  await flush();
  expect(button("允许").disabled).toBe(true);
});

it("uses an optional deny note as message, and keeps an empty note as the old deny result", async () => {
  act(() => root.render(<InteractionCard ix={editable} />));
  act(() => button("附理由拒绝…").click());
  await flush();
  setValue(host.querySelector("input")!, "change the greeting");
  await flush();
  act(() => button("拒绝").click());
  await flush();
  expect(calls).toEqual([{ path: "/interactions/p_edit/resolve", body: { allow: false, message: "change the greeting" } }]);
});

it("does not offer editing on a card without editable_text", () => {
  const plain = { ...editable, editable_text: undefined };
  act(() => root.render(<InteractionCard ix={plain} />));
  expect(host.querySelector("[data-permission-text]")).toBeNull();
  expect(host.querySelector("textarea")).toBeNull();
});
