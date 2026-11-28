// @vitest-environment jsdom
// Controlled TaskCompletionCheck bypasses api/manager/check/i18n singletons and fences cross-todo/credential/manager and ABA races; default fallbacks stay pinned too.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TaskDoneCheck } from "../lib/task-completion-check";
import type { Todo } from "../lib/todos";
import type { TaskCompletionCheckDeps } from "./TaskCompletionCheck";

const mocks = vi.hoisted(() => ({
  currentLanguage: vi.fn(() => "en"),
  credentialOwner: vi.fn(() => "default-owner"),
  onCredentialChange: vi.fn(() => () => {}),
  subscribeCredentialOwner: vi.fn(() => () => {}),
  defaultManager: vi.fn(() => "assistant"),
  onDefaultManager: vi.fn(() => () => {}),
  useKnownDefault: vi.fn(() => "assistant"),
  checkTaskCompletion: vi.fn(),
  completionCheckError: vi.fn((e: unknown) => `default-err:${e instanceof Error ? e.message : String(e)}`),
}));

vi.mock("../i18n", async (orig) => ({
  ...(await orig<typeof import("../i18n")>()),
  currentLanguage: mocks.currentLanguage,
}));
vi.mock("../lib/api", () => ({
  credentialOwner: mocks.credentialOwner,
  onCredentialChange: mocks.onCredentialChange,
  subscribeCredentialOwner: mocks.subscribeCredentialOwner,
}));
vi.mock("../lib/manager-choice", () => ({
  defaultManager: mocks.defaultManager,
  onDefaultManager: mocks.onDefaultManager,
  useKnownDefault: mocks.useKnownDefault,
}));
vi.mock("../lib/task-completion-check", () => ({
  checkTaskCompletion: mocks.checkTaskCompletion,
  completionCheckError: mocks.completionCheckError,
}));

const { TaskCompletionCheck, TaskCompletionCheckView, CompletionCheckResult } = await import("./TaskCompletionCheck");
const { initI18n, t } = await import("../i18n");

const makeTodo = (patch: Partial<Todo> = {}): Todo => ({
  id: "todo-1",
  title: "Controlled task",
  origin: "conv-1",
  stage: "review",
  created_at: "2026-11-01T08:00:00Z",
  updated_at: "2026-11-01T09:00:00Z",
  ...patch,
});

const doneCheck = (patch: Partial<TaskDoneCheck> = {}): TaskDoneCheck => ({
  verdict: "done",
  reason: "All checks passed.",
  would_close: true,
  skip: "",
  applied: false,
  ...patch,
});

interface PendingCheck {
  id: string;
  manager: string;
  signal: AbortSignal;
  resolve: (value: TaskDoneCheck) => void;
  reject: (reason?: unknown) => void;
}

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const section = () => host.querySelector<HTMLElement>('[data-testid="task-completion-check"]')!;
const button = () => section().querySelector("button")!;
const result = () => host.querySelector<HTMLElement>('[data-testid="completion-check-result"]');
const alert = () => section().querySelector<HTMLElement>('[role="alert"]');

const expectNoDefaultSingletons = () => {
  expect(mocks.credentialOwner).not.toHaveBeenCalled();
  expect(mocks.onCredentialChange).not.toHaveBeenCalled();
  expect(mocks.subscribeCredentialOwner).not.toHaveBeenCalled();
  expect(mocks.defaultManager).not.toHaveBeenCalled();
  expect(mocks.onDefaultManager).not.toHaveBeenCalled();
  expect(mocks.useKnownDefault).not.toHaveBeenCalled();
  expect(mocks.checkTaskCompletion).not.toHaveBeenCalled();
  expect(mocks.completionCheckError).not.toHaveBeenCalled();
  expect(mocks.currentLanguage).not.toHaveBeenCalled();
};

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await initI18n("en");
  Object.values(mocks).forEach((m) => m.mockClear());
  mocks.currentLanguage.mockReturnValue("en");
  mocks.credentialOwner.mockReturnValue("default-owner");
  mocks.onCredentialChange.mockReturnValue(() => {});
  mocks.subscribeCredentialOwner.mockReturnValue(() => {});
  mocks.defaultManager.mockReturnValue("assistant");
  mocks.onDefaultManager.mockReturnValue(() => {});
  mocks.useKnownDefault.mockReturnValue("assistant");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("TaskCompletionCheck controlled deps, view separation, and race guards", () => {
  it("runs fully controlled via deps without invoking default singletons", async () => {
    const fixedDate = new Date("2026-11-26T00:00:00.000Z");
    const pending: PendingCheck[] = [];
    const checkFn = vi.fn((id: string, manager: string, signal: AbortSignal) => new Promise<TaskDoneCheck>((resolve, reject) => {
      pending.push({ id, manager, signal, resolve, reject });
    }));
    const deps: TaskCompletionCheckDeps = {
      checkTaskCompletion: checkFn,
      formatError: vi.fn((e) => `custom-error:${e instanceof Error ? e.message : String(e)}`),
      credentialOwner: "owner-1",
      defaultManager: "mgr-1",
      language: () => "ja",
      now: () => fixedDate,
    };

    await act(async () => root.render(<StrictMode><TaskCompletionCheck todo={makeTodo()} deps={deps} /></StrictMode>));
    await act(async () => { button().click(); button().click(); });
    expect(pending).toHaveLength(1);
    expect(pending[0]).toMatchObject({ id: "todo-1", manager: "mgr-1" });
    expect(button().disabled).toBe(true);

    await act(async () => pending[0].resolve(doneCheck({ reason: "Controlled verdict" })));
    await flush();
    expect(button().disabled).toBe(false);
    expect(result()?.textContent).toContain("Controlled verdict");
    expect(result()?.querySelector("time")?.textContent).toContain(fixedDate.toLocaleString("ja"));

    // Failure path uses controlled formatError.
    await act(async () => button().click());
    await act(async () => pending[1].reject(new Error("timeout")));
    await flush();
    expect(alert()?.textContent).toBe("custom-error:timeout");
    expectNoDefaultSingletons();
  });

  it.each(["resolve", "reject"] as const)(
    "aborts and fences stale %s across todo.id ABA (t1 -> t2 -> t1)",
    async (staleOutcome) => {
      const pending: PendingCheck[] = [];
      const deps: TaskCompletionCheckDeps = {
        checkTaskCompletion: (id, manager, signal) => new Promise((resolve, reject) => {
          pending.push({ id, manager, signal, resolve, reject });
        }),
        formatError: (e) => (e instanceof Error ? e.message : String(e)),
        credentialOwner: "owner-1",
        defaultManager: "mgr-1",
        language: "en",
      };

      // Visit 1 of todo-1.
      await act(async () => root.render(<TaskCompletionCheck todo={makeTodo({ id: "todo-1" })} deps={deps} />));
      await act(async () => button().click());
      expect(pending).toHaveLength(1);
      expect(button().disabled).toBe(true);

      // Switch to todo-2: req #1 aborted and UI reset.
      await act(async () => root.render(<TaskCompletionCheck todo={makeTodo({ id: "todo-2" })} deps={deps} />));
      expect(pending[0].signal.aborted).toBe(true);
      expect(button().disabled).toBe(false);

      // Switch back to todo-1 (ABA) and start req #2.
      await act(async () => root.render(<TaskCompletionCheck todo={makeTodo({ id: "todo-1" })} deps={deps} />));
      await act(async () => button().click());
      expect(pending).toHaveLength(2);
      expect(button().disabled).toBe(true);

      // Stale req #1 settles out of order (ignoring abort): must not overwrite or clear busy.
      await act(async () => {
        if (staleOutcome === "resolve") pending[0].resolve(doneCheck({ reason: "Stale visit 1" }));
        else pending[0].reject(new Error("Stale visit 1 error"));
      });
      await flush();
      expect(button().disabled).toBe(true);
      expect(result()).toBeNull();
      expect(alert()).toBeNull();

      // Current req #2 wins cleanly.
      await act(async () => pending[1].resolve(doneCheck({ reason: "Fresh visit 2" })));
      await flush();
      expect(button().disabled).toBe(false);
      expect(result()?.textContent).toContain("Fresh visit 2");
      expectNoDefaultSingletons();
    },
  );

  it("aborts and fences stale results on credentialOwner prop and imperative ABA changes", async () => {
    const pending: PendingCheck[] = [];
    let owner = "cred-a";
    const credListeners = new Set<() => void>();
    const emitCred = (next: string) => {
      owner = next;
      credListeners.forEach((l) => l());
    };
    const deps: TaskCompletionCheckDeps = {
      checkTaskCompletion: (id, manager, signal) => new Promise((resolve, reject) => {
        pending.push({ id, manager, signal, resolve, reject });
      }),
      formatError: (e) => (e instanceof Error ? e.message : String(e)),
      credentialOwner: () => owner,
      onCredentialChange: (l) => {
        credListeners.add(l);
        return () => { credListeners.delete(l); };
      },
      defaultManager: "mgr-1",
      language: "en",
    };

    await act(async () => root.render(<TaskCompletionCheck todo={makeTodo()} deps={deps} />));
    await act(async () => button().click());
    expect(pending).toHaveLength(1);

    // Imperative ABA within one synchronous batch (cred-a -> cred-b -> cred-a) aborts req #1 without wedging the button.
    await act(async () => {
      emitCred("cred-b");
      emitCred("cred-a");
    });
    await flush();
    expect(pending[0].signal.aborted).toBe(true);
    expect(button().disabled).toBe(false);

    // Start req #2; late req #1 cannot clobber req #2.
    await act(async () => button().click());
    expect(pending).toHaveLength(2);
    await act(async () => pending[0].resolve(doneCheck({ reason: "Stale cred-a #1" })));
    await flush();
    expect(result()).toBeNull();
    expect(button().disabled).toBe(true);

    await act(async () => pending[1].resolve(doneCheck({ reason: "Valid cred-a #2" })));
    await flush();
    expect(result()?.textContent).toContain("Valid cred-a #2");

    // Cross-owner switch clears completed result.
    await act(async () => emitCred("cred-b"));
    await flush();
    expect(result()).toBeNull();
    expectNoDefaultSingletons();
  });

  it("aborts on defaultManager switch when todo.manager is unset, and keeps explicit todo.manager untouched", async () => {
    const pending: PendingCheck[] = [];
    let mgr = "mgr-a";
    const mgrListeners = new Set<() => void>();
    const emitMgr = (next: string) => {
      mgr = next;
      mgrListeners.forEach((l) => l());
    };
    const deps: TaskCompletionCheckDeps = {
      checkTaskCompletion: (id, manager, signal) => new Promise((resolve, reject) => {
        pending.push({ id, manager, signal, resolve, reject });
      }),
      formatError: (e) => (e instanceof Error ? e.message : String(e)),
      credentialOwner: "owner-1",
      defaultManager: () => mgr,
      onDefaultManager: (l) => {
        mgrListeners.add(l);
        return () => { mgrListeners.delete(l); };
      },
      language: "en",
    };

    // Unset todo.manager follows defaultManager and aborts on switch.
    await act(async () => root.render(<TaskCompletionCheck todo={makeTodo({ manager: undefined })} deps={deps} />));
    await act(async () => button().click());
    expect(pending[0].manager).toBe("mgr-a");
    await act(async () => emitMgr("mgr-b"));
    await flush();
    expect(pending[0].signal.aborted).toBe(true);
    expect(button().disabled).toBe(false);

    await act(async () => button().click());
    expect(pending[1].manager).toBe("mgr-b");
    await act(async () => pending[0].resolve(doneCheck({ reason: "Stale mgr-a" })));
    await flush();
    expect(result()).toBeNull();
    expect(button().disabled).toBe(true);
    await act(async () => pending[1].resolve(doneCheck({ reason: "Current mgr-b" })));
    await flush();
    expect(result()?.textContent).toContain("Current mgr-b");

    // Explicit todo.manager ignores unrelated defaultManager changes.
    await act(async () => root.render(<TaskCompletionCheck todo={makeTodo({ id: "todo-pinned", manager: "pinned-mgr" })} deps={deps} />));
    await act(async () => button().click());
    expect(pending[2].manager).toBe("pinned-mgr");
    await act(async () => emitMgr("mgr-c"));
    await flush();
    expect(pending[2].signal.aborted).toBe(false);
    await act(async () => pending[2].resolve(doneCheck({ reason: "Pinned manager survived" })));
    await flush();
    expect(result()?.textContent).toContain("Pinned manager survived");

    // Unmount aborts an in-flight check.
    await act(async () => button().click());
    await act(async () => root.render(<div />));
    expect(pending[3].signal.aborted).toBe(true);
    expectNoDefaultSingletons();
  });

  it("renders pure TaskCompletionCheckView and CompletionCheckResult without touching default singletons", async () => {
    const fixedDate = new Date("2026-11-26T00:00:00.000Z");
    const onRun = vi.fn();
    await act(async () => root.render(
      <TaskCompletionCheckView
        supervised
        busy={false}
        result={{ check: doneCheck({ reason: "Pure view check", applied: true }), at: fixedDate }}
        language="zh-Hans"
        onRun={onRun}
      />,
    ));
    expect(section().textContent).toContain(t("todos.check_supervised"));
    expect(result()?.textContent).toContain("Pure view check");
    expect(result()?.textContent).toContain(t("todos.check_applied_review"));
    expect(result()?.querySelector("time")?.textContent).toContain(fixedDate.toLocaleString("zh-Hans"));
    await act(async () => button().click());
    expect(onRun).toHaveBeenCalledTimes(1);

    await act(async () => root.render(<CompletionCheckResult check={doneCheck({ reason: "Direct result" })} at={fixedDate} language="en" />));
    expect(result()?.textContent).toContain("Direct result");
    expectNoDefaultSingletons();
  });

  it("renders compact single-row TaskCompletionCheckView with InfoTip explanation and action button (#17938)", async () => {
    const onRun = vi.fn();
    await act(async () => root.render(
      <TaskCompletionCheckView
        supervised
        busy={false}
        compact
        language="en"
        onRun={onRun}
      />,
    ));
    expect(section().getAttribute("data-compact")).toBe("true");
    expect(section().textContent).toContain(t("todos.check_section"));
    expect(section().textContent).not.toContain(t("todos.check_explanation"));
    expect(section().textContent).not.toContain(t("todos.check_supervised"));

    const runBtn = section().querySelector<HTMLButtonElement>('[data-testid="task-completion-check-run"]')!;
    expect(runBtn).toBeTruthy();
    await act(async () => runBtn.click());
    expect(onRun).toHaveBeenCalledTimes(1);

    const infoBtn = section().querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-atoms.about", { name: t("todos.check_section") })}"]`)!;
    expect(infoBtn).toBeTruthy();
    await act(async () => infoBtn.click());
    for (let i = 0; i < 40 && !document.querySelector('[role="tooltip"]'); i++) {
      await act(async () => {
        await new Promise((r) => setTimeout(r, 10));
      });
    }
    const tipText = document.querySelector('[role="tooltip"]')?.textContent ?? "";
    expect(tipText).toContain(t("todos.check_explanation"));
    expect(tipText).toContain(t("todos.check_supervised"));
    expectNoDefaultSingletons();
  });

  it("preserves default api, manager-choice, task-completion-check, and i18n fallbacks when deps are omitted", async () => {
    mocks.checkTaskCompletion.mockResolvedValue(doneCheck({ reason: "Default path result" }));

    await act(async () => root.render(<TaskCompletionCheck todo={makeTodo()} />));
    await act(async () => button().click());
    await flush();

    expect(mocks.subscribeCredentialOwner).toHaveBeenCalled();
    expect(mocks.credentialOwner).toHaveBeenCalled();
    expect(mocks.onCredentialChange).toHaveBeenCalled();
    expect(mocks.useKnownDefault).toHaveBeenCalled();
    expect(mocks.defaultManager).toHaveBeenCalled();
    expect(mocks.onDefaultManager).toHaveBeenCalled();
    expect(mocks.checkTaskCompletion).toHaveBeenCalledWith("todo-1", "assistant", expect.any(AbortSignal));
    expect(mocks.currentLanguage).toHaveBeenCalled();
    expect(result()?.textContent).toContain("Default path result");
  });
});
