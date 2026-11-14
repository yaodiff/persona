// @vitest-environment jsdom
// Audit item 74: at a phone's width the list and a to-do's page take turns. A to-do opened from its row with Enter takes
// the focus to the page's Back, as a page pushed on iOS does; Esc goes back to the list as Back does, unless a field is
// being edited or a dialog or menu is over the page; back on the list the focus is on that to-do's row again, for a
// to-do opened by a link too. Side by side nothing moves. jsdom lays nothing out, so the list column reads as hidden
// (the phone's width) unless a case gives it a box. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: async () => ({}),
}));

const { getState } = await import("../lib/store");
const { useRoute } = await import("../lib/router");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
// a few turns of the event loop: history.back() lands as a popstate after a task
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (id: string, title: string): Todo => ({ id, title, created_at: iso(7_200_000), updated_at: iso(3_600_000), agents: [] }) as unknown as Todo;
const todos = [todo("a", "Book the dentist"), todo("b", "Renew the passport"), todo("c", "Water the plants")];
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

// the app's route to the page (App.tsx): the address's ?id= is the open to-do
function Page() {
  const r = useRoute();
  return <TodosView selectedId={r.name === "todos" ? r.id : undefined} />;
}

const mount = async (at: string) => {
  history.replaceState(null, "", at);
  await act(async () => root.render(<Page />));
  await settle();
};
const row = (id: string) => host.querySelector<HTMLElement>(`[data-todo="${id}"]`)!;
const back = () => host.querySelector<HTMLElement>("[data-testid=todos-back] button");
const key = async (el: Element, k: string) => {
  await act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
  await settle();
};
const openWithEnter = async (id: string) => {
  row(id).focus();
  await key(row(id), "Enter");
};

beforeEach(() => {
  localStorage.clear();
  seed({ todos, todosLoaded: true, todosError: null, sessions: {}, interactions: [], answered: {}, machines: [] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  history.replaceState(null, "", "/");
  seed(before);
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("a to-do's page at a phone's width", () => {
  it("takes the focus to Back when Enter opens it from its row", async () => {
    await mount("/todos");
    await openWithEnter("b");
    expect(location.search).toBe("?id=b");
    expect(back()).toBeTruthy();
    expect(back()!.textContent).toContain(t("todos.back_to_list"));
    expect(document.activeElement).toBe(back());
  });

  it("goes back on Esc, the focus on the row it was opened from", async () => {
    await mount("/todos");
    await openWithEnter("b");
    await key(document.activeElement!, "Escape");
    expect(location.search).toBe("");
    expect(back()).toBeNull();
    expect(document.activeElement).toBe(row("b"));
  });

  it("goes back by its Back button too, the focus on the row", async () => {
    await mount("/todos");
    await openWithEnter("c");
    await act(async () => back()!.click());
    await settle();
    expect(location.search).toBe("");
    expect(document.activeElement).toBe(row("c"));
  });

  it("stays on Esc in its title field, where Esc belongs to the edit", async () => {
    await mount("/todos");
    await openWithEnter("b");
    const title = host.querySelector<HTMLInputElement>("[data-testid=todos-detail-column] input[type=text]")!;
    title.focus();
    await key(title, "Escape");
    expect(location.search).toBe("?id=b");
    expect(back()).toBeTruthy();
  });

  it("stays on Esc while a dialog is over the page", async () => {
    await mount("/todos");
    await openWithEnter("b");
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    await key(document.body, "Escape");
    expect(location.search).toBe("?id=b");
    dialog.remove();
  });

  it("opened by a link, goes back on Esc with the focus on its row, not the page", async () => {
    await mount("/todos?id=c");
    expect(back()).toBeTruthy();
    await key(document.body, "Escape");
    expect(location.search).toBe("");
    expect(document.activeElement).toBe(row("c"));
  });
});

describe("a to-do's page beside the list", () => {
  it("leaves the focus on the row and Esc to the page", async () => {
    // the list column has a box: the page is wide enough for both
    vi.spyOn(HTMLElement.prototype, "getClientRects").mockImplementation(function (this: HTMLElement) {
      return (this.dataset.testid === "todos-list-column" ? [new DOMRect(0, 0, 380, 800)] : []) as unknown as DOMRectList;
    });
    await mount("/todos");
    await openWithEnter("b");
    expect(location.search).toBe("?id=b");
    expect(document.activeElement).toBe(row("b"));
    await key(row("b"), "Escape");
    expect(location.search).toBe("?id=b");
  });
});
