// @vitest-environment jsdom
// Finished work held for the owner's OK in Needs you (#13191, #13541): its own words, not the permission card's "Waiting
// for your approval" (nothing waits to be approved on the row), an explicit Open task, the pull request when known, and
// a two-step Mark done with Undo.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { getState, refreshTodos, reset } from "../../lib/store";
import type { Todo } from "../../lib/todos";
import { Toasts } from "../../ui/atoms";
import WaitingTaskRow from "./WaitingTaskRow";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const at = "2026-11-10T08:00:00Z";
let host: HTMLDivElement;
let root: Root;
let todo: Todo;
let patches: { path: string; body: unknown }[];
let status: number;

beforeEach(async () => {
  reset();
  patches = [];
  status = 200;
  todo = {
    id: "todo_held", title: "Release the sample page", origin: "main", created_at: at, updated_at: at,
    pr_url: "https://github.com/example/demo/pull/42",
    delivery: { state: "pr", gate: "Approve the release", unblock: "user", at },
  } as unknown as Todo;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = String(url);
    if (init?.method === "PATCH") {
      const body = JSON.parse(String(init.body));
      patches.push({ path, body });
      if (status !== 200) return new Response(JSON.stringify({ error: "Not yours to close" }), { status, headers: { "Content-Type": "application/json" } });
      todo = { ...todo, done_at: body.done ? at : undefined };
      return new Response(JSON.stringify({ todo }), { headers: { "Content-Type": "application/json" } });
    }
    return new Response(JSON.stringify({ todos: [todo] }), { headers: { "Content-Type": "application/json" } });
  }));
  await act(async () => { await refreshTodos(); });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

const render = (unblock = "user", state = "pr") => act(() => {
  todo = { ...todo, delivery: { state, gate: "Approve the release", unblock, at } };
  root.render(<><WaitingTaskRow todo={todo} status="follow_up" countsOpenAttr={{}} /><Toasts /></>);
});
const closeButton = () => host.querySelector<HTMLButtonElement>("[data-close-task=todo_held]")!;
const prLink = () => host.querySelector<HTMLAnchorElement>("[data-pr-link=todo_held]");
const press = () => act(() => closeButton().click());

describe("a delivery held for the owner's OK in Needs you", () => {
  it("says what it is and offers Open task, the pull request and Mark done", () => {
    render();
    expect(host.textContent).toContain(t("assistant-tasks.delivery_ok"));
    expect(host.textContent).not.toContain(t("todos.kicker_permission"));
    expect(host.textContent).toContain("Approve the release");
    const open = host.querySelector<HTMLButtonElement>("[data-open-task=todo_held]");
    expect(open?.textContent).toBe(t("assistant-tasks.open_task"));
    expect(open?.className).toContain("whitespace-nowrap");
    const link = prLink()!;
    expect(link.textContent).toBe(t("assistant-tasks.progress_pr", { number: "42" }));
    expect(link.href).toBe("https://github.com/example/demo/pull/42");
    expect(link.className).toContain("whitespace-nowrap");
    expect(link.className).toContain("touch-hit");
    const close = closeButton();
    expect(close.textContent).toBe(t("assistant-tasks.close_task"));
    expect(close.className).toContain("whitespace-nowrap");
    expect(close.className).toContain("touch-hit");
    // the action bar wraps whole buttons instead of splitting their words
    expect(close.closest("[data-card-keys]")?.className).toContain("flex-wrap");
  });

  it("arms on the first press, says what closing means for its state, and disarms on Escape", () => {
    render();
    press();
    expect(patches).toHaveLength(0);
    const close = closeButton();
    expect(close.hasAttribute("data-close-armed")).toBe(true);
    expect(close.textContent).toBe(t("assistant-tasks.close_confirm_pr"));
    expect(close.className).toContain("text-attention");
    act(() => close.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(closeButton().hasAttribute("data-close-armed")).toBe(false);
    expect(closeButton().textContent).toBe(t("assistant-tasks.close_task"));
    // merged: the state decides the wording
    render("user", "merged");
    press();
    expect(closeButton().textContent).toBe(t("assistant-tasks.close_confirm"));
    expect(patches).toHaveLength(0);
  });

  it("marks the task done on the second press, taking it out of the list, and Undo reopens it", async () => {
    render();
    press();
    await act(async () => closeButton().click());
    expect(patches).toHaveLength(1);
    expect(patches[0].path).toMatch(/\/todos\/todo_held$/);
    expect(patches[0].body).toEqual({ done: true });
    expect(getState().todos.find((x) => x.id === todo_held())?.done_at).toBe(at);
    const undo = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("assistant-tasks.undo_close"))!;
    expect(undo).toBeDefined();
    expect(document.body.textContent).toContain(t("assistant-tasks.closed_task", { title: "Release the sample page" }));
    await act(async () => undo.click());
    expect(patches).toHaveLength(2);
    expect(patches[1].body).toEqual({ done: false });
    expect(getState().todos.find((x) => x.id === todo_held())?.done_at).toBeUndefined();
  });

  it("re-arms after a refused close and says why", async () => {
    status = 403;
    render();
    press();
    await act(async () => closeButton().click());
    expect(patches).toHaveLength(1);
    const close = closeButton();
    expect(close.hasAttribute("data-close-armed")).toBe(true);
    expect(close.disabled).toBe(false);
    expect(close.textContent).toBe(t("assistant-tasks.close_confirm_pr"));
    expect(document.body.textContent).toContain(t("assistant-tasks.close_failed", { error: "Not yours to close" }));
  });

  it("offers nothing for a hold its supervisor has not read", () => {
    render("supervisor");
    expect(host.querySelector("[data-open-task]")).toBeNull();
    expect(host.querySelector("[data-close-task]")).toBeNull();
    expect(host.querySelector("[data-pr-link]")).toBeNull();
    expect(host.textContent).not.toContain(t("assistant-tasks.delivery_ok"));
  });

  it("drops PR links carrying userinfo, backslash confusion, or trailing non-digit path suffixes", () => {
    for (const bad of [
      "https://user:pass@example.com/example/demo/pull/42",
      "https://evil.example\\@example.com/example/demo/pull/42",
      "https:/example/demo/pull/42",
      "https://example.com/example/demo/pull/42abc",
    ]) {
      todo = { ...todo, pr_url: bad };
      render();
      expect(prLink(), bad).toBeNull();
    }
  });
});

const todo_held = () => "todo_held";
