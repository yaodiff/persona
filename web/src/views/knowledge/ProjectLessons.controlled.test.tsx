// @vitest-environment jsdom
// Controlled ProjectLessons bypasses default toast/errorText/api/wall-clock singletons, exports pure
// ProjectLessonsView, EditLessonDialogView, and DeleteLessonDialogView, caches fresh-object getter snapshots via
// sameValue, preserves render purity across abandoned concurrent transitions, fences cross-scope / deps-switch /
// ABA / unmount races with AbortController and monotonic generations, preserves 409 lesson_changed conflict
// semantics, recovers via try/finally, and keeps 100% backward-compatible default behavior.
import { act, startTransition, StrictMode, Suspense, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import type { ProjectLesson, ProjectLessonsDeps, ProjectLessonsList, ProjectLessonsViewProps } from "./ProjectLessons";

const mocks = vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
  })) as typeof window.matchMedia;
  return {
    api: vi.fn(),
    errorText: vi.fn((err: unknown) => `default-err:${err instanceof Error ? err.message : String(err)}`),
    toast: vi.fn(),
    ago: vi.fn(),
  };
});

vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (...args: unknown[]) => mocks.api(...args),
  errorText: (err: unknown) => mocks.errorText(err),
}));

vi.mock("../../lib/format", async (original) => {
  const real = await original<typeof import("../../lib/format")>();
  return {
    ...real,
    ago: (iso?: string, now?: number, style?: Intl.RelativeTimeFormatStyle) => {
      mocks.ago(iso, now, style);
      return real.ago(iso, now, style);
    },
  };
});

vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: (message: string, tone?: string) => mocks.toast(message, tone),
}));

const { ApiError } = await import("../../lib/api");
const {
  DeleteLessonDialogView,
  EditLessonDialogView,
  LESSON_TEXT_MAX,
  ProjectLessons,
  ProjectLessonsView,
  projectLessonsDeps,
} = await import("./ProjectLessons");

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const FIXED_NOW = Date.UTC(2026, 10, 26, 12, 0, 0);
const TWO_HOURS_AGO_ISO = new Date(FIXED_NOW - 2 * 3600_000).toISOString();

const makeLesson = (patch: Partial<ProjectLesson> = {}): ProjectLesson => ({
  id: "lesson-a",
  kind: "missing_context",
  text: "Use the stock simulator for layout bugs.",
  seen: 3,
  last_seen_at: TWO_HOURS_AGO_ISO,
  stale: false,
  edited: false,
  sources: [{ todo_id: "todo_1", title: "Fix layout bug" }],
  ...patch,
});

const makeList = (lessons: ProjectLesson[], enabled = true): ProjectLessonsList => ({ enabled, lessons });
const cloneList = (list: ProjectLessonsList): ProjectLessonsList => ({
  enabled: list.enabled,
  lessons: list.lessons.map((l) => ({ ...l, sources: l.sources.map((s) => ({ ...s })) })),
});

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = (label: string, container: ParentNode = document) =>
  [...container.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.trim() === label);
const activeDialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const setTextarea = async (el: HTMLTextAreaElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const expectNoDefaultSingletons = () => {
  expect(mocks.api).not.toHaveBeenCalled();
  expect(mocks.errorText).not.toHaveBeenCalled();
  expect(mocks.toast).not.toHaveBeenCalled();
};

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await initI18n("en");
  mocks.api.mockReset();
  mocks.errorText.mockReset().mockImplementation((err: unknown) => `default-err:${err instanceof Error ? err.message : String(err)}`);
  mocks.toast.mockReset();
  mocks.ago.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("Pure presentational ProjectLessons views", () => {
  it("renders ProjectLessonsView, EditLessonDialogView, and DeleteLessonDialogView cleanly with custom formatAgo/fixed now and zero singleton calls", async () => {
    const lessonA = makeLesson();
    const lessonB = makeLesson({ id: "lesson-b", kind: "wasted_step", text: "Skip full rebuild.", seen: 1, stale: true, edited: true, sources: [{ todo_id: "todo_2" }] });
    const many = Array.from({ length: 6 }, (_, i) => makeLesson({ id: `l-${i}`, text: `Lesson ${i}` }));
    const onToggleAll = vi.fn();
    const onOpenEdit = vi.fn();
    const onOpenDelete = vi.fn();
    const onSaveEdit = vi.fn();
    const onConfirmDelete = vi.fn();

    // Empty list renders nothing.
    await act(async () => root.render(
      <ProjectLessonsView
        list={makeList([])}
        showAll={false}
        editing={null}
        editDraft=""
        deleting={null}
        onToggleAll={onToggleAll}
        onOpenEdit={onOpenEdit}
        onCloseEdit={() => {}}
        onEditDraftChange={() => {}}
        onSaveEdit={onSaveEdit}
        onOpenDelete={onOpenDelete}
        onCloseDelete={() => {}}
        onConfirmDelete={onConfirmDelete}
      />,
    ));
    expect(host.innerHTML).toBe("");

    // Custom formatAgo completely bypasses ago().
    await act(async () => root.render(
      <ProjectLessonsView
        list={makeList([lessonA, lessonB], false)}
        showAll={false}
        editing={null}
        editDraft=""
        deleting={null}
        formatAgo={() => "custom-ago-label"}
        onToggleAll={onToggleAll}
        onOpenEdit={onOpenEdit}
        onCloseEdit={() => {}}
        onEditDraftChange={() => {}}
        onSaveEdit={onSaveEdit}
        onOpenDelete={onOpenDelete}
        onCloseDelete={() => {}}
        onConfirmDelete={onConfirmDelete}
      />,
    ));
    expect(mocks.ago).not.toHaveBeenCalled();
    expect(host.textContent).toContain("custom-ago-label");
    expect(host.textContent).toContain(t("knowledge-projectlessons.footer_off"));
    await act(async () => { buttons(t("knowledge-projectlessons.edit"), host)[0].click(); });
    expect(onOpenEdit).toHaveBeenCalledWith(lessonA);

    // Pagination row when more than 5 lessons exist, plus fixed now bypassing Date.now().
    const nowSpy = vi.spyOn(Date, "now");
    await act(async () => root.render(
      <ProjectLessonsView
        list={makeList(many)}
        showAll={false}
        editing={null}
        editDraft=""
        deleting={null}
        now={FIXED_NOW}
        onToggleAll={onToggleAll}
        onOpenEdit={onOpenEdit}
        onCloseEdit={() => {}}
        onEditDraftChange={() => {}}
        onSaveEdit={onSaveEdit}
        onOpenDelete={onOpenDelete}
        onCloseDelete={() => {}}
        onConfirmDelete={onConfirmDelete}
      />,
    ));
    expect(nowSpy).not.toHaveBeenCalled();
    expect(mocks.ago).toHaveBeenCalledWith(TWO_HOURS_AGO_ISO, FIXED_NOW, undefined);
    expect(host.textContent).toContain("2 hours ago");
    expect(host.textContent).not.toContain("Lesson 5");
    await act(async () => { buttons(t("knowledge-projectlessons.show_all", { count: 6 }), host)[0].click(); });
    expect(onToggleAll).toHaveBeenCalledTimes(1);

    // Pure EditLessonDialogView: length guard, error alert, Cmd+Enter submit vs plain Enter.
    await act(async () => root.render(
      <EditLessonDialogView
        lesson={lessonA}
        draft={"x".repeat(LESSON_TEXT_MAX + 1)}
        error="Edit failed"
        onDraftChange={() => {}}
        onSave={onSaveEdit}
        onClose={() => {}}
      />,
    ));
    expect(activeDialog()!.textContent).toContain(t("knowledge-projectlessons.too_long", { max: LESSON_TEXT_MAX }));
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toContain("Edit failed");
    expect(buttons(t("common.save"), activeDialog()!)[0].disabled).toBe(true);

    await act(async () => root.render(
      <EditLessonDialogView
        lesson={lessonA}
        draft="Changed lesson text."
        onDraftChange={() => {}}
        onSave={onSaveEdit}
        onClose={() => {}}
      />,
    ));
    const area = activeDialog()!.querySelector<HTMLTextAreaElement>("textarea")!;
    await act(async () => {
      area.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(onSaveEdit).not.toHaveBeenCalled();
    await act(async () => {
      area.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }));
    });
    expect(onSaveEdit).toHaveBeenCalledTimes(1);

    // Pure DeleteLessonDialogView renders danger button and error alert.
    await act(async () => root.render(
      <DeleteLessonDialogView
        lesson={lessonA}
        error="Delete refused"
        onConfirm={onConfirmDelete}
        onClose={() => {}}
      />,
    ));
    expect(activeDialog()!.textContent).toContain(lessonA.text);
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toContain("Delete refused");
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), activeDialog()!).at(-1)!.click(); });
    expect(onConfirmDelete).toHaveBeenCalledTimes(1);
    expectNoDefaultSingletons();
  });
});

describe("Controlled ProjectLessons data bypass, snapshot caching, deterministic clock, and bridge stability", () => {
  it("bypasses list() when controlled data is supplied, caches fresh-clone getters with sameValue without subscription churn, updates deterministically with deps.now/formatAgo, and handles removed-lesson ABA", async () => {
    const listSpy = vi.fn(async () => makeList([]));
    const editSpy = vi.fn(async () => makeList([]));
    const removeSpy = vi.fn(async () => makeList([]));
    const dataListeners = new Set<() => void>();
    const scopeListeners = new Set<() => void>();
    const subscribeData = vi.fn((l: () => void) => { dataListeners.add(l); return () => { dataListeners.delete(l); }; });
    const subscribeScopeKey = vi.fn((l: () => void) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; });

    const lessonA = makeLesson();
    const lessonB = makeLesson({ id: "lesson-b", text: "Second lesson." });
    let currentData: ProjectLessonsList | null = null;
    let clock = FIXED_NOW;
    let dataReads = 0;

    // 1) Controlled data: null bypasses initial list() completely.
    await act(async () => root.render(
      <StrictMode>
        <ProjectLessons
          current={() => true}
          deps={{
            scopeKey: "proj-1",
            data: null,
            now: FIXED_NOW,
            list: listSpy,
            edit: editSpy,
            remove: removeSpy,
          }}
        />
      </StrictMode>,
    ));
    await flush();
    expect(host.innerHTML).toBe("");
    expect(listSpy).not.toHaveBeenCalled();

    // 2) Direct data -> open Edit modal & type draft -> switch bridge to fresh-clone getter + subscribe on same scope.
    currentData = makeList([lessonA, lessonB]);
    await act(async () => root.render(
      <StrictMode>
        <ProjectLessons
          current={() => true}
          deps={{
            scopeKey: "proj-1",
            data: currentData,
            now: () => clock,
            list: listSpy,
            edit: editSpy,
            remove: removeSpy,
          }}
        />
      </StrictMode>,
    ));
    expect(host.textContent).toContain("2 hours ago");
    await act(async () => { buttons(t("knowledge-projectlessons.edit"), host)[0].click(); });
    await setTextarea(activeDialog()!.querySelector("textarea")!, "Draft kept across bridge switch");

    const renderSubscribed = () => root.render(
      <StrictMode>
        <ProjectLessons
          current={() => true}
          deps={{
            scopeKey: () => "proj-1",
            subscribeScopeKey,
            data: () => {
              dataReads += 1;
              return currentData ? cloneList(currentData) : null;
            },
            subscribeData,
            now: () => clock,
            list: listSpy,
            edit: editSpy,
            remove: removeSpy,
          }}
        />
      </StrictMode>,
    );
    await act(async () => renderSubscribed());
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("Draft kept across bridge switch");
    expect(dataReads).toBeLessThan(20);
    expect(listSpy).not.toHaveBeenCalled();

    // Re-rendering with inline arrow getters & emitting semantically unchanged clones causes zero churn or loops.
    const dataSubCount = subscribeData.mock.calls.length;
    const scopeSubCount = subscribeScopeKey.mock.calls.length;
    await act(async () => renderSubscribed());
    await act(async () => renderSubscribed());
    await act(async () => { dataListeners.forEach((l) => l()); });
    expect(subscribeData).toHaveBeenCalledTimes(dataSubCount);
    expect(subscribeScopeKey).toHaveBeenCalledTimes(scopeSubCount);
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("Draft kept across bridge switch");

    // Advancing injected clock updates relative time deterministically without wall-clock Date.now().
    const nowSpy = vi.spyOn(Date, "now");
    clock = FIXED_NOW + 3600_000;
    await act(async () => renderSubscribed());
    expect(host.textContent).toContain("3 hours ago");
    expect(nowSpy).not.toHaveBeenCalled();

    // Removing the currently edited lesson from controlled data closes the modal, and re-adding it (ABA) does not reopen it.
    currentData = makeList([lessonB]);
    await act(async () => { dataListeners.forEach((l) => l()); });
    expect(activeDialog()).toBeNull();
    currentData = makeList([lessonA, lessonB]);
    await act(async () => { dataListeners.forEach((l) => l()); });
    expect(activeDialog()).toBeNull();

    // Custom formatAgo + renderView work cleanly.
    mocks.ago.mockReset();
    let seenView: ProjectLessonsViewProps | null = null;
    await act(async () => root.render(
      <ProjectLessons
        current={() => true}
        deps={{
          scopeKey: "proj-1",
          data: currentData,
          formatAgo: (iso) => `fmt:${iso.slice(0, 10)}`,
          list: listSpy,
          edit: editSpy,
          remove: removeSpy,
          renderView: (vp) => {
            seenView = vp;
            return <ProjectLessonsView {...vp} />;
          },
        }}
      />,
    ));
    expect(seenView!.list?.lessons).toHaveLength(2);
    expect(host.textContent).toContain("fmt:2026-11-26");
    expect(mocks.ago).not.toHaveBeenCalled();
    expect(listSpy).not.toHaveBeenCalled();
    expectNoDefaultSingletons();
  });
});

describe("ProjectLessons concurrency, ABA fencing, 409 conflicts, and try/finally recovery", () => {
  it("keeps committed edit dialog and in-flight edit intact when a concurrent transition render is abandoned", async () => {
    const suspendGate = deferred<void>();
    const editGate = deferred<ProjectLessonsList>();
    let capturedSignal: AbortSignal | null = null;
    const lessonA = makeLesson();
    const updated = makeLesson({ id: "lesson-a2", text: "Committed edit.", edited: true });
    const renderedScopes: string[] = [];

    function SuspendIf({ block }: { block: boolean }) {
      if (block) throw suspendGate.promise;
      return null;
    }

    let setHarness!: (next: { scope: string; block: boolean }) => void;
    function Harness() {
      const [state, setState] = useState({ scope: "p1", block: false });
      setHarness = setState;
      renderedScopes.push(state.scope);
      return (
        <Suspense fallback={<div data-fallback />}>
          <ProjectLessons
            current={() => true}
            deps={{
              scopeKey: state.scope,
              now: FIXED_NOW,
              list: async () => makeList([lessonA]),
              edit: (_id, _text, signal) => {
                capturedSignal = signal ?? null;
                return editGate.promise;
              },
              remove: async () => makeList([]),
            }}
          />
          <SuspendIf block={state.block} />
        </Suspense>
      );
    }

    await act(async () => root.render(<Harness />));
    await flush();
    await act(async () => { buttons(t("knowledge-projectlessons.edit"), host)[0].click(); });
    await setTextarea(activeDialog()!.querySelector("textarea")!, "Committed edit.");
    await act(async () => { buttons(t("common.save"), activeDialog()!)[0].click(); });
    expect(capturedSignal!.aborted).toBe(false);
    expect(buttons(t("common.save"), activeDialog()!)[0].getAttribute("aria-busy")).toBe("true");

    // Suspended concurrent transition to p2 must NOT abort committed p1 edit or close dialog.
    await act(async () => {
      startTransition(() => setHarness({ scope: "p2", block: true }));
    });
    expect(renderedScopes).toContain("p2");
    expect(capturedSignal!.aborted).toBe(false);
    expect(activeDialog()).not.toBeNull();

    // Abandon transition back to p1; resolving editGate updates list and closes dialog cleanly.
    await act(async () => setHarness({ scope: "p1", block: false }));
    expect(capturedSignal!.aborted).toBe(false);
    await act(async () => { editGate.resolve(makeList([updated])); });
    await flush();
    expect(activeDialog()).toBeNull();
    expect(host.textContent).toContain("Committed edit.");
    expectNoDefaultSingletons();
  });

  it("aborts in-flight list/edit/remove and resets dialogs across scopeKey switches, synchronous ABA bursts, deps switches (ABA), single-flight, and unmount", async () => {
    const scopeListeners = new Set<() => void>();
    let currentScope = "p1";
    const listCalls: Array<{ signal?: AbortSignal; gate: ReturnType<typeof deferred<ProjectLessonsList>> }> = [];
    const editCalls: Array<{ signal?: AbortSignal; gate: ReturnType<typeof deferred<ProjectLessonsList>> }> = [];
    const removeCalls: Array<{ signal?: AbortSignal; gate: ReturnType<typeof deferred<ProjectLessonsList>> }> = [];
    const notify = vi.fn();
    const onError = vi.fn();
    const lessonA = makeLesson();
    const lessonB = makeLesson({ id: "lesson-b", text: "Project B lesson." });

    const makeScopedDeps = (): ProjectLessonsDeps => ({
      scopeKey: () => currentScope,
      subscribeScopeKey: (l) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; },
      now: FIXED_NOW,
      list: (signal) => {
        const gate = deferred<ProjectLessonsList>();
        listCalls.push({ signal, gate });
        return gate.promise;
      },
      edit: (_id, _text, signal) => {
        const gate = deferred<ProjectLessonsList>();
        editCalls.push({ signal, gate });
        return gate.promise;
      },
      remove: (_id, signal) => {
        const gate = deferred<ProjectLessonsList>();
        removeCalls.push({ signal, gate });
        return gate.promise;
      },
      notify,
      formatError: (e) => `fmt:${e instanceof Error ? e.message : String(e)}`,
      onError,
    });

    await act(async () => root.render(<ProjectLessons deps={makeScopedDeps()} current={() => true} />));
    expect(listCalls).toHaveLength(1);
    await act(async () => { listCalls[0].gate.resolve(makeList([lessonA])); });
    await flush();
    expect(host.textContent).toContain(lessonA.text);

    // 1) Synchronous single-flight guard on edit + synchronous ABA burst on scopeKey (p1 -> p2 -> p1).
    await act(async () => { buttons(t("knowledge-projectlessons.edit"), host)[0].click(); });
    await setTextarea(activeDialog()!.querySelector("textarea")!, "Edited once");
    await act(async () => {
      const saveBtn = buttons(t("common.save"), activeDialog()!)[0];
      saveBtn.click();
      saveBtn.click();
    });
    expect(editCalls).toHaveLength(1);
    expect(editCalls[0].signal!.aborted).toBe(false);

    await act(async () => {
      currentScope = "p2";
      scopeListeners.forEach((l) => l());
      currentScope = "p1";
      scopeListeners.forEach((l) => l());
    });
    expect(activeDialog()).toBeNull();
    expect(editCalls[0].signal!.aborted).toBe(true);
    expect(listCalls).toHaveLength(2);
    await act(async () => { listCalls[1].gate.resolve(makeList([lessonA])); });
    await flush();

    // Late 409 conflict & rejection from stale visit-1 edit are completely ignored.
    await act(async () => {
      editCalls[0].gate.reject(new ApiError(409, "changed", { code: "lesson_changed", enabled: true, lessons: [lessonB] }));
    });
    await flush();
    expect(notify).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
    expect(host.textContent).toContain(lessonA.text);
    expect(host.textContent).not.toContain(lessonB.text);

    // 2) Implicit deps switch ABA (no scopeKey): depsA -> depsB -> depsA resets open delete dialog and aborts pending remove/list.
    const listA: Array<{ signal?: AbortSignal; gate: ReturnType<typeof deferred<ProjectLessonsList>> }> = [];
    const removeA: Array<{ signal?: AbortSignal; gate: ReturnType<typeof deferred<ProjectLessonsList>> }> = [];
    const listB: Array<{ signal?: AbortSignal; gate: ReturnType<typeof deferred<ProjectLessonsList>> }> = [];
    const depsA: ProjectLessonsDeps = {
      list: (signal?: AbortSignal) => { const gate = deferred<ProjectLessonsList>(); listA.push({ signal, gate }); return gate.promise; },
      edit: async (_id, _text, _signal) => makeList([]),
      remove: (_id: string, signal?: AbortSignal) => { const gate = deferred<ProjectLessonsList>(); removeA.push({ signal, gate }); return gate.promise; },
    };
    const depsB: ProjectLessonsDeps = {
      list: (signal?: AbortSignal) => { const gate = deferred<ProjectLessonsList>(); listB.push({ signal, gate }); return gate.promise; },
      edit: async (_id, _text, _signal) => makeList([]),
      remove: async (_id, _signal) => makeList([]),
    };

    await act(async () => root.render(<ProjectLessons deps={depsA} current={() => true} />));
    await act(async () => { listA[0].gate.resolve(makeList([lessonA])); });
    await flush();
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), host)[0].click(); });
    await act(async () => {
      const confirmBtn = buttons(t("knowledge-projectlessons.delete"), activeDialog()!).at(-1)!;
      confirmBtn.click();
      confirmBtn.click();
    });
    expect(removeA).toHaveLength(1);
    expect(removeA[0].signal!.aborted).toBe(false);

    // Switch depsA -> depsB -> depsA (ABA): aborts removeA[0] and listB[0], closes dialog, fetches fresh listA[1].
    await act(async () => root.render(<ProjectLessons deps={depsB} current={() => true} />));
    expect(removeA[0].signal!.aborted).toBe(true);
    expect(activeDialog()).toBeNull();
    expect(listB).toHaveLength(1);

    await act(async () => root.render(<ProjectLessons deps={depsA} current={() => true} />));
    expect(listB[0].signal!.aborted).toBe(true);
    expect(listA).toHaveLength(2);
    await act(async () => { listA[1].gate.resolve(makeList([lessonA])); });
    await flush();

    // Stale removeA[0] settling with empty list cannot overwrite fresh listA[1].
    await act(async () => { removeA[0].gate.resolve(makeList([])); });
    await flush();
    expect(host.textContent).toContain(lessonA.text);

    // Unmount aborts in-flight list/remove cleanly.
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), host)[0].click(); });
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), activeDialog()!).at(-1)!.click(); });
    expect(removeA).toHaveLength(2);
    await act(async () => root.render(null));
    expect(removeA[1].signal!.aborted).toBe(true);
    expectNoDefaultSingletons();
  });

  it("handles 409 lesson_changed conflicts and try/finally error recovery across edit and delete, tolerating throwing notify/formatError/onError", async () => {
    const lessonA = makeLesson();
    const lessonConflict = makeLesson({ id: "lesson-a3", seen: 5, text: "Updated on another device." });
    const lessonDeleted = makeLesson({ id: "lesson-c", text: "Remaining lesson." });
    let editMode: "sync-throw" | "reject" | "conflict" = "sync-throw";
    let removeMode: "reject" | "conflict" | "ok" = "reject";
    let throwFormat = true;
    let throwOnError = true;
    let throwNotify = true;

    const notify = vi.fn(() => {
      if (throwNotify) throw new Error("toast crashed");
    });
    const onError = vi.fn(() => {
      if (throwOnError) throw new Error("onError crashed");
    });

    await act(async () => root.render(
      <ProjectLessons
        current={() => true}
        deps={{
          scopeKey: "conflict-recovery",
          now: FIXED_NOW,
          list: async () => makeList([lessonA]),
          edit: () => {
            if (editMode === "sync-throw") throw new Error("sync edit boom");
            if (editMode === "reject") return Promise.reject(new Error("async edit boom"));
            return Promise.reject(new ApiError(409, "changed", { code: "lesson_changed", enabled: true, lessons: [lessonConflict] }));
          },
          remove: () => {
            if (removeMode === "reject") return Promise.reject(new Error("delete boom"));
            if (removeMode === "conflict") {
              return Promise.reject(new ApiError(409, "changed", { code: "lesson_changed", enabled: true, lessons: [lessonDeleted] }));
            }
            return Promise.resolve(makeList([]));
          },
          notify,
          formatError: (err) => {
            if (throwFormat) throw new Error("format crashed");
            return `custom:${err instanceof Error ? err.message : String(err)}`;
          },
          onError,
        }}
      />,
    ));
    await flush();

    // Edit sync throw + throwing formatError + throwing onError -> falls back to errorText, keeps draft, releases busy.
    await act(async () => { buttons(t("knowledge-projectlessons.edit"), host)[0].click(); });
    await setTextarea(activeDialog()!.querySelector("textarea")!, "Edited draft kept");
    await act(async () => { buttons(t("common.save"), activeDialog()!)[0].click(); });
    await flush();
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("Edited draft kept");
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toContain("default-err:sync edit boom");
    expect(buttons(t("common.save"), activeDialog()!)[0].disabled).toBe(false);

    // Edit async rejection with custom formatError -> keeps draft & shows custom error.
    throwFormat = false;
    throwOnError = false;
    editMode = "reject";
    await act(async () => { buttons(t("common.save"), activeDialog()!)[0].click(); });
    await flush();
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toContain("custom:async edit boom");
    expect(onError).toHaveBeenCalledWith("custom:async edit boom");

    // Edit 409 lesson_changed even when notify throws -> refreshes list, calls notify, closes dialog cleanly.
    editMode = "conflict";
    await act(async () => { buttons(t("common.save"), activeDialog()!)[0].click(); });
    await flush();
    expect(activeDialog()).toBeNull();
    expect(notify).toHaveBeenCalledWith(t("knowledge-projectlessons.changed_elsewhere"), "info");
    expect(host.textContent).toContain("Updated on another device.");
    expect(mocks.toast).not.toHaveBeenCalled();

    // Delete failure -> shows custom error & keeps dialog open -> 409 conflict refreshes list and closes dialog.
    throwNotify = false;
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), host)[0].click(); });
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), activeDialog()!).at(-1)!.click(); });
    await flush();
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toContain("custom:delete boom");
    expect(buttons(t("knowledge-projectlessons.delete"), activeDialog()!).at(-1)!.disabled).toBe(false);

    removeMode = "conflict";
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), activeDialog()!).at(-1)!.click(); });
    await flush();
    expect(activeDialog()).toBeNull();
    expect(notify).toHaveBeenCalledTimes(2);
    expect(host.textContent).toContain("Remaining lesson.");

    // Final delete succeeds and hides section.
    removeMode = "ok";
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), host)[0].click(); });
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), activeDialog()!).at(-1)!.click(); });
    await flush();
    expect(host.innerHTML).toBe("");
  });

  it("preserves backward-compatible default toast, errorText, relative ago(), and projectLessonsDeps(project, auth) signal forwarding", async () => {
    const auth = { token: "synthetic-token", tokenOwner: "credential-1", headers: { "X-Persona-Manager": "assistant" } };
    const relativeRecent = new Date(Date.now() - 60_000).toISOString();
    const liveLesson = makeLesson({ last_seen_at: relativeRecent });
    const conflictLesson = makeLesson({ id: "lesson-now", text: "Conflict refreshed.", last_seen_at: relativeRecent });

    mocks.api
      .mockResolvedValueOnce(makeList([liveLesson]))
      .mockRejectedValueOnce(new Error("temporary edit failure"))
      .mockRejectedValueOnce(new ApiError(409, "changed", { code: "lesson_changed", enabled: true, lessons: [conflictLesson] }))
      .mockResolvedValueOnce(makeList([]));

    const deps = projectLessonsDeps("proj/alpha", auth);
    await act(async () => root.render(<ProjectLessons deps={deps} current={() => true} />));
    await flush();

    // Default list call used encoded project path, auth, and AbortSignal.
    expect(mocks.api.mock.calls[0][0]).toBe("/kb/projects/proj%2Falpha/lessons");
    expect(mocks.api.mock.calls[0][1]).toMatchObject({ ...auth });
    expect(mocks.api.mock.calls[0][1].signal).toBeInstanceOf(AbortSignal);
    // Default relative ago() rendered without injected now.
    expect(mocks.ago).toHaveBeenCalledWith(relativeRecent, undefined, undefined);

    // Default edit failure uses api.errorText.
    await act(async () => { buttons(t("knowledge-projectlessons.edit"), host)[0].click(); });
    await setTextarea(activeDialog()!.querySelector("textarea")!, "Default edit text.");
    await act(async () => { buttons(t("common.save"), activeDialog()!)[0].click(); });
    await flush();
    expect(mocks.api.mock.calls[1][0]).toBe("/kb/projects/proj%2Falpha/lessons/lesson-a");
    expect(mocks.api.mock.calls[1][1]).toMatchObject({ method: "PUT", json: { text: "Default edit text." }, ...auth });
    expect(mocks.api.mock.calls[1][1].signal).toBeInstanceOf(AbortSignal);
    expect(mocks.errorText).toHaveBeenCalledTimes(1);
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toContain("default-err:temporary edit failure");

    // Default 409 uses atoms.toast.
    await act(async () => { buttons(t("common.save"), activeDialog()!)[0].click(); });
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith(t("knowledge-projectlessons.changed_elsewhere"), "info");
    expect(host.textContent).toContain("Conflict refreshed.");

    // Default delete forwards DELETE, auth, and AbortSignal.
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), host)[0].click(); });
    await act(async () => { buttons(t("knowledge-projectlessons.delete"), activeDialog()!).at(-1)!.click(); });
    await flush();
    expect(mocks.api.mock.calls[3][0]).toBe("/kb/projects/proj%2Falpha/lessons/lesson-now");
    expect(mocks.api.mock.calls[3][1]).toMatchObject({ method: "DELETE", ...auth });
    expect(mocks.api.mock.calls[3][1].signal).toBeInstanceOf(AbortSignal);
    expect(host.innerHTML).toBe("");
  });
});
