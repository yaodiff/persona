// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";
import type { Machine, Session } from "../lib/types";

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

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

const toastSpy = vi.fn();
vi.mock("../ui/atoms", async (orig) => ({
  ...(await orig<typeof import("../ui/atoms")>()),
  toast: (...args: unknown[]) => toastSpy(...args),
}));

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const button = (label: string, scope: ParentNode = document) =>
  [...scope.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (id: string, title: string, patch: Partial<Todo> = {}): Todo => ({
  id,
  title,
  created_at: iso(7_200_000),
  updated_at: iso(600_000),
  ...patch,
});

const machines: Machine[] = [{ id: "box-1", name: "Build Box", online: true }];
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

const open = async (id: string) => {
  await act(async () => root.render(<TodosView selectedId={id} />));
  await flush();
};

beforeEach(() => {
  api.mockReset();
  toastSpy.mockReset();
  api.mockImplementation(async (path: string, init?: unknown) => {
    if (path.endsWith("/launchers")) return { launchers: [] };
    if (path.endsWith("/decisions?all=1")) return { decisions: [] };
    if (path === "/todos" && !init) return { todos: getState().todos };
    if (path.startsWith("/todos/") && (init as { method?: string } | undefined)?.method === "PATCH") {
      const id = path.slice("/todos/".length);
      const current = getState().todos.find((x) => x.id === id);
      return { todo: current ? { ...current, responsible: undefined } : { id } };
    }
    return {};
  });
  seed({
    todos: [
      todo("t-session", "Watch canary rollout", {
        responsible: { kind: "session", name: "deploy-watch", machine_id: "box-1", session_id: "tmux-9" },
      }),
      todo("t-person", "Confirm legal sign-off", {
        responsible: { kind: "person", name: "Alex" },
      }),
      todo("t-plain", "Plain unassigned to-do"),
    ],
    todosLoaded: true,
    machines,
    sessions: {},
    features: [],
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
  vi.restoreAllMocks();
});

describe("a to-do's responsible party (#12712)", () => {
  it("shows a session responsible party in list row and detail with resolved machine hint and hides not-handed text", async () => {
    await open("t-session");

    const row = document.querySelector<HTMLElement>("[data-todo=t-session]");
    expect(row).toBeTruthy();
    const rowResp = row!.querySelector<HTMLElement>("[data-testid=todo-responsible]");
    expect(rowResp?.textContent).toContain("deploy-watch");
    expect(rowResp?.dataset.kind).toBe("session");
    expect(row!.textContent).not.toContain("尚未交给 Agent");

    const detailResp = document.querySelector<HTMLElement>("[data-testid=todo-detail-responsible]");
    expect(detailResp?.textContent).toContain("deploy-watch");

    const section = document.querySelector<HTMLElement>("[data-testid=todo-responsible-section]");
    expect(section).toBeTruthy();
    expect(section!.querySelector("[data-testid=todo-responsible-name]")?.textContent).toBe("deploy-watch");
    expect(section!.querySelector("[data-testid=todo-responsible-kind]")?.textContent).toBe("会话");
    expect(section!.querySelector("[data-testid=todo-responsible-hint]")?.textContent).toBe("Build Box · tmux-9");
  });

  it("shows a person responsible party without extra hint when optional hints are absent", async () => {
    await open("t-person");

    const row = document.querySelector<HTMLElement>("[data-todo=t-person]");
    const rowResp = row!.querySelector<HTMLElement>("[data-testid=todo-responsible]");
    expect(rowResp?.textContent).toContain("Alex");
    expect(rowResp?.dataset.kind).toBe("person");

    const section = document.querySelector<HTMLElement>("[data-testid=todo-responsible-section]");
    expect(section!.querySelector("[data-testid=todo-responsible-name]")?.textContent).toBe("Alex");
    expect(section!.querySelector("[data-testid=todo-responsible-kind]")?.textContent).toBe("人员");
    expect(section!.querySelector("[data-testid=todo-responsible-hint]")).toBeNull();
  });

  it("renders no responsible markup when responsible is unset", async () => {
    await open("t-plain");

    const row = document.querySelector<HTMLElement>("[data-todo=t-plain]");
    expect(row!.querySelector("[data-testid=todo-responsible]")).toBeNull();
    expect(row!.textContent).toContain("尚未交给 Agent");
    expect(document.querySelector("[data-testid=todo-detail-responsible]")).toBeNull();
    expect(document.querySelector("[data-testid=todo-responsible-section]")).toBeNull();
  });

  it("clears responsible via PATCH /todos/{id} with { responsible: null }", async () => {
    await open("t-session");
    const section = document.querySelector<HTMLElement>("[data-testid=todo-responsible-section]");
    expect(section).toBeTruthy();

    await click(button("清除", section!));

    const patches = api.mock.calls.filter(
      ([path, init]) => path === "/todos/t-session" && (init as { method?: string } | undefined)?.method === "PATCH",
    );
    expect(patches).toHaveLength(1);
    expect((patches[0][1] as { json?: unknown }).json).toEqual({ responsible: null });
    expect(toastSpy).toHaveBeenCalledWith("已清除负责方", "ok");
  });

  it("keeps linked Persona agent status precedence when both linked agent and responsible are present", async () => {
    const workingSession: Session = {
      machine_id: "box-1",
      session_id: "agent-1",
      title: "Linked Worker",
      cwd: "/code/app",
      status: "working",
      state: "working",
      updated_at: iso(10_000),
    };
    seed({
      todos: [
        todo("t-both", "Both linked agent and responsible", {
          responsible: { kind: "person", name: "Alex" },
          agents: [{ machine_id: "box-1", session_id: "agent-1", linked_at: iso(60_000) }],
        }),
      ],
      sessions: { "box-1": [workingSession] },
    });

    await open("t-both");
    const row = document.querySelector<HTMLElement>("[data-todo=t-both]");
    expect(row!.querySelector("[data-testid=todo-status]")?.textContent).toBe("工作中");
    expect(row!.querySelector("[data-testid=todo-responsible]")?.textContent).toContain("Alex");
  });
});
