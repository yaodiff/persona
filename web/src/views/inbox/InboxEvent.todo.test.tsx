// @vitest-environment jsdom
// A to-do suggestion's card (#6480): the title and the day are fields; one tap on "加入待办" sends them as they stand.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const sent = vi.hoisted(() => ({ calls: [] as { path: string; json?: Record<string, unknown> }[] }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { json?: Record<string, unknown> }) => {
    sent.calls.push({ path, json: init?.json });
    return Promise.resolve({ id: "i1" });
  },
}));
import type { InboxItem } from "../../lib/inbox";
import { SuggestionCard } from "./InboxEvent";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  sent.calls = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const item = { id: "i1", source: "lark", title: "chat", suggestion: { version: 3, kind: "todo", text: "回复报价", due: "2026-11-20" } } as unknown as InboxItem;
const typeInto = (el: HTMLInputElement, v: string) => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, v);
  el.dispatchEvent(new Event("input", { bubbles: true }));
};

describe("to-do suggestion", () => {
  it("shows the suggested title and day, and confirms them as they stand", async () => {
    act(() => root.render(<SuggestionCard item={item} suggestion={item.suggestion!} />));
    expect(host.querySelector<HTMLInputElement>("[data-todo-title]")?.value).toBe("回复报价");
    expect(host.querySelector<HTMLInputElement>("[data-todo-day]")?.value).toBe("2026-11-20");
    await act(async () => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "加入待办")?.click());
    const body = sent.calls.find((c) => c.path.endsWith("/suggestion"))?.json;
    expect(body).toMatchObject({ version: 3, action: "confirm", due: "2026-11-20" });
    expect(body?.text).toBeUndefined();
  });
  it("sends an edited title and a cleared day as none", async () => {
    act(() => root.render(<SuggestionCard item={item} suggestion={item.suggestion!} />));
    act(() => typeInto(host.querySelector<HTMLInputElement>("[data-todo-title]")!, "回复新报价"));
    act(() => typeInto(host.querySelector<HTMLInputElement>("[data-todo-day]")!, ""));
    await act(async () => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "加入待办")?.click());
    const body = sent.calls.find((c) => c.path.endsWith("/suggestion"))?.json;
    expect(body).toMatchObject({ action: "confirm", text: "回复新报价" });
    expect(body?.due).toBeUndefined();
  });
  it("All day is a switch: on hides the time, off asks for one (9:00 to start with) and sends a moment", async () => {
    act(() => root.render(<SuggestionCard item={item} suggestion={item.suggestion!} />));
    const sw = () => host.querySelector<HTMLElement>('[role="switch"]')!;
    expect(sw().getAttribute("aria-checked")).toBe("true");
    expect(host.querySelector("[data-todo-time]")).toBeNull();
    await act(async () => sw().click());
    expect(host.querySelector<HTMLInputElement>("[data-todo-time]")?.value).toBe("09:00");
    await act(async () => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "加入待办")?.click());
    const body = sent.calls.find((c) => c.path.endsWith("/suggestion"))?.json;
    expect(body?.due).toBe(new Date(2026, 10, 20, 9, 0).toISOString());
  });
});
