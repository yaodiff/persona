// @vitest-environment jsdom
// Controlled ConfirmedFacts bypasses default api/errorText singletons, exports pure ConfirmedFactsView and
// FactsEditorView, fences cross-scope / target-switch / ABA / editor-reopen / unmount races with AbortController
// and monotonic generations, preserves render purity across abandoned concurrent transitions, recovers via
// try/finally (even with throwing observers), and keeps 100% backward-compatible default behavior.
import { act, startTransition, StrictMode, Suspense, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import type { ConfirmedFactsAuth, ConfirmedFactsDeps, ConfirmedFactsViewProps } from "./ConfirmedFacts";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  errorText: vi.fn((err: unknown) => `default-err:${err instanceof Error ? err.message : String(err)}`),
}));

vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (...args: unknown[]) => mocks.api(...args),
  errorText: (err: unknown) => mocks.errorText(err),
}));

const { ConfirmedFacts, ConfirmedFactsView, FactsEditorView } = await import("./ConfirmedFacts");

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const AUTH: ConfirmedFactsAuth = {
  token: "synthetic-token-a",
  tokenOwner: "credential-1",
  headers: { "X-Persona-Manager": "assistant" },
};

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const findButton = (label: string) =>
  [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const click = async (label: string) => {
  await act(async () => { findButton(label)!.click(); });
};
const textarea = () => host.querySelector<HTMLTextAreaElement>("textarea");
const setTextarea = async (value: string) => {
  const el = textarea()!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const submitForm = async () => {
  await act(async () => {
    host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
};

const expectNoDefaultSingletons = () => {
  expect(mocks.api).not.toHaveBeenCalled();
  expect(mocks.errorText).not.toHaveBeenCalled();
};

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await initI18n("en");
  mocks.api.mockReset();
  mocks.errorText.mockReset().mockImplementation((err: unknown) => `default-err:${err instanceof Error ? err.message : String(err)}`);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("Pure presentational ConfirmedFacts views", () => {
  it("renders ConfirmedFactsView and FactsEditorView across view, edit, saving, refreshing, save-error, and refresh-error states without touching singletons", async () => {
    const onStartEdit = vi.fn();
    const onDraftChange = vi.fn();
    const onSave = vi.fn();
    const onRetryRefresh = vi.fn();
    const onClose = vi.fn();

    // Empty read-only state offers Add button.
    await act(async () => root.render(
      <ConfirmedFactsView
        value=""
        editing={false}
        draft=""
        initial=""
        phase="edit"
        error=""
        onStartEdit={onStartEdit}
        onDraftChange={onDraftChange}
        onSave={onSave}
        onRetryRefresh={onRetryRefresh}
        onClose={onClose}
      />,
    ));
    expect(host.textContent).toContain(t("knowledge-confirmedfacts.empty"));
    expect(findButton(t("knowledge-confirmedfacts.add"))).toBeDefined();
    await click(t("knowledge-confirmedfacts.add"));
    expect(onStartEdit).toHaveBeenCalledTimes(1);

    // Populated read-only state offers Edit button.
    await act(async () => root.render(
      <ConfirmedFactsView
        value="Confirmed role."
        editing={false}
        draft="Confirmed role."
        initial="Confirmed role."
        phase="edit"
        error=""
        onStartEdit={onStartEdit}
        onDraftChange={onDraftChange}
        onSave={onSave}
        onRetryRefresh={onRetryRefresh}
        onClose={onClose}
      />,
    ));
    expect(host.textContent).toContain("Confirmed role.");
    expect(findButton(t("knowledge-confirmedfacts.edit"))).toBeDefined();

    // Pure FactsEditorView: Cmd+Enter submits while plain Enter does not.
    await act(async () => root.render(
      <FactsEditorView
        draft="Updated role."
        initial="Confirmed role."
        phase="edit"
        error=""
        onDraftChange={onDraftChange}
        onSave={onSave}
        onRetryRefresh={onRetryRefresh}
        onClose={onClose}
      />,
    ));
    expect(textarea()!.value).toBe("Updated role.");
    expect(textarea()!.disabled).toBe(false);
    expect(findButton(t("common.save"))!.disabled).toBe(false);
    await act(async () => {
      textarea()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(onSave).not.toHaveBeenCalled();
    await act(async () => {
      textarea()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }));
    });
    expect(onSave).toHaveBeenCalledTimes(1);

    // Save error renders alert and Retry saving label.
    await act(async () => root.render(
      <FactsEditorView
        draft="Updated role."
        initial="Confirmed role."
        phase="edit"
        error="Write rejected"
        onDraftChange={onDraftChange}
        onSave={onSave}
        onRetryRefresh={onRetryRefresh}
        onClose={onClose}
      />,
    ));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("Write rejected");
    expect(findButton(t("knowledge-confirmedfacts.retry_save"))).toBeDefined();

    // Refreshing status disables textarea and shows polite status.
    await act(async () => root.render(
      <FactsEditorView
        draft="Updated role."
        initial="Confirmed role."
        phase="refreshing"
        error=""
        onDraftChange={onDraftChange}
        onSave={onSave}
        onRetryRefresh={onRetryRefresh}
        onClose={onClose}
      />,
    ));
    expect(textarea()!.disabled).toBe(true);
    expect(host.querySelector('[role="status"]')?.textContent).toContain(t("knowledge-confirmedfacts.refreshing"));

    // Refresh-error renders Retry refresh action without Save/Cancel buttons.
    await act(async () => root.render(
      <FactsEditorView
        draft="Updated role."
        initial="Confirmed role."
        phase="refresh-error"
        error="Read timed out"
        onDraftChange={onDraftChange}
        onSave={onSave}
        onRetryRefresh={onRetryRefresh}
        onClose={onClose}
      />,
    ));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("Read timed out");
    expect(findButton(t("common.save"))).toBeUndefined();
    await click(t("knowledge-confirmedfacts.retry_refresh"));
    expect(onRetryRefresh).toHaveBeenCalledTimes(1);
    expectNoDefaultSingletons();
  });
});

describe("Controlled ConfirmedFacts deps, bridge stability, renderView, and single-flight", () => {
  it("runs savePinned and refresh fully controlled, enforces synchronous single-flight, preserves draft across scopeKey bridge switches without subscription churn, and supports renderView", async () => {
    const saveCalls: Array<{ kind: string; id: string; pinned: string; auth: ConfirmedFactsAuth; signal: AbortSignal; gate: ReturnType<typeof deferred<void>> }> = [];
    const refreshCalls: Array<{ signal?: AbortSignal; gate: ReturnType<typeof deferred<boolean>> }> = [];
    const onSaved = vi.fn();
    const scopeListeners = new Set<() => void>();
    const subscribeScopeKey = vi.fn((l: () => void) => {
      scopeListeners.add(l);
      return () => { scopeListeners.delete(l); };
    });

    const savePinned: NonNullable<ConfirmedFactsDeps["savePinned"]> = (kind, id, pinned, auth, signal) => {
      const gate = deferred<void>();
      saveCalls.push({ kind, id, pinned, auth, signal, gate });
      return gate.promise;
    };
    const refresh = vi.fn((signal?: AbortSignal) => {
      const gate = deferred<boolean>();
      refreshCalls.push({ signal, gate });
      return gate.promise;
    });

    // Start in direct scopeKey mode, open editor, and type a draft.
    await act(async () => root.render(
      <StrictMode>
        <ConfirmedFacts
          kind="people"
          id="example"
          value="Initial fact."
          auth={AUTH}
          current={() => true}
          refresh={refresh}
          onSaved={onSaved}
          deps={{ scopeKey: "scope-1", savePinned, formatError: (e) => `fmt:${String(e)}` }}
        />
      </StrictMode>,
    ));
    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("Draft preserved across bridge switch.");
    expect(textarea()!.value).toBe("Draft preserved across bridge switch.");

    // Switch bridge in-place to inline getter + subscribeScopeKey on the same scope value.
    const renderSubscribed = () => root.render(
      <StrictMode>
        <ConfirmedFacts
          kind="people"
          id="example"
          value="Initial fact."
          auth={AUTH}
          current={() => true}
          refresh={refresh}
          onSaved={onSaved}
          deps={{
            scopeKey: () => "scope-1",
            subscribeScopeKey,
            savePinned,
            formatError: (e) => `fmt:${String(e)}`,
          }}
        />
      </StrictMode>,
    );
    await act(async () => renderSubscribed());
    expect(textarea()!.value).toBe("Draft preserved across bridge switch.");

    // Re-rendering with fresh inline arrow getter must not churn subscribeScopeKey.
    const subCount = subscribeScopeKey.mock.calls.length;
    await act(async () => renderSubscribed());
    await act(async () => renderSubscribed());
    expect(subscribeScopeKey).toHaveBeenCalledTimes(subCount);

    // Synchronous double submit admits only one savePinned call.
    await act(async () => {
      const form = host.querySelector("form")!;
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(saveCalls).toHaveLength(1);
    expect(saveCalls[0]).toMatchObject({ kind: "people", id: "example", pinned: "Draft preserved across bridge switch.", auth: AUTH });
    expect(saveCalls[0].signal.aborted).toBe(false);

    // Resolve savePinned -> triggers refresh with AbortSignal; submitting again while refreshing is blocked.
    await act(async () => { saveCalls[0].gate.resolve(); });
    await flush();
    expect(refreshCalls).toHaveLength(1);
    expect(refreshCalls[0].signal).toBeInstanceOf(AbortSignal);
    expect(refreshCalls[0].signal!.aborted).toBe(false);
    await submitForm();
    expect(saveCalls).toHaveLength(1);

    // Resolve refresh -> calls onSaved once and closes editor.
    await act(async () => { refreshCalls[0].gate.resolve(true); });
    await flush();
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(textarea()).toBeNull();

    // Custom renderView receives complete view props.
    let capturedView: ConfirmedFactsViewProps | null = null;
    await act(async () => root.render(
      <ConfirmedFacts
        kind="people"
        id="example"
        value="Rendered custom"
        auth={AUTH}
        current={() => true}
        refresh={refresh}
        onSaved={onSaved}
        deps={{
          scopeKey: "scope-1",
          savePinned,
          renderView: (vp) => {
            capturedView = vp;
            return <div data-custom-facts={vp.value} />;
          },
        }}
      />,
    ));
    expect(host.querySelector("[data-custom-facts]")?.getAttribute("data-custom-facts")).toBe("Rendered custom");
    expect(capturedView!.editing).toBe(false);
    expectNoDefaultSingletons();
  });
});

describe("ConfirmedFacts concurrency, ABA fencing, editor close/reopen, and try/finally recovery", () => {
  it("keeps committed editor and in-flight savePinned intact when a concurrent transition render is abandoned", async () => {
    const suspendGate = deferred<void>();
    const saveGate = deferred<void>();
    const refreshGate = deferred<boolean>();
    let capturedSignal: AbortSignal | null = null;
    const onSaved = vi.fn();
    const renderedScopes: string[] = [];

    function SuspendIf({ block }: { block: boolean }) {
      if (block) throw suspendGate.promise;
      return null;
    }

    let setHarness!: (next: { scope: string; kind: string; id: string; block: boolean }) => void;
    function Harness() {
      const [state, setState] = useState({ scope: "s1", kind: "people", id: "id-1", block: false });
      setHarness = setState;
      renderedScopes.push(`${state.scope}:${state.kind}:${state.id}`);
      return (
        <Suspense fallback={<div data-fallback />}>
          <ConfirmedFacts
            kind={state.kind}
            id={state.id}
            value="Original"
            auth={AUTH}
            current={() => true}
            refresh={() => refreshGate.promise}
            onSaved={onSaved}
            deps={{
              scopeKey: state.scope,
              savePinned: (_k, _id, _p, _a, signal) => {
                capturedSignal = signal;
                return saveGate.promise;
              },
            }}
          />
          <SuspendIf block={state.block} />
        </Suspense>
      );
    }

    await act(async () => root.render(<Harness />));
    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("Updated in s1");
    await submitForm();
    expect(capturedSignal!.aborted).toBe(false);
    expect(textarea()!.disabled).toBe(true);

    // Suspended concurrent transition to another scope + target must NOT abort committed s1 save.
    await act(async () => {
      startTransition(() => setHarness({ scope: "s2", kind: "projects", id: "id-2", block: true }));
    });
    expect(renderedScopes).toContain("s2:projects:id-2");
    expect(capturedSignal!.aborted).toBe(false);
    expect(textarea()!.value).toBe("Updated in s1");

    // Abandon transition back to s1; resolving save + refresh completes cleanly.
    await act(async () => setHarness({ scope: "s1", kind: "people", id: "id-1", block: false }));
    expect(capturedSignal!.aborted).toBe(false);
    await act(async () => { saveGate.resolve(); });
    await flush();
    await act(async () => { refreshGate.resolve(true); });
    await flush();
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(textarea()).toBeNull();
    expectNoDefaultSingletons();
  });

  it("aborts in-flight save/refresh and fences stale callbacks across scope switches, synchronous ABA scope bursts, target (kind, id) ABA, editor close/reopen, and unmount", async () => {
    const scopeListeners = new Set<() => void>();
    let currentScope = "s1";
    let currentKind = "people";
    let currentId = "entry-1";
    const saveCalls: Array<{ signal: AbortSignal; gate: ReturnType<typeof deferred<void>> }> = [];
    const refreshCalls: Array<{ signal?: AbortSignal; gate: ReturnType<typeof deferred<boolean>> }> = [];
    const onSaved = vi.fn();
    const onError = vi.fn();
    let latestView: ConfirmedFactsViewProps | null = null;

    const makeDeps = (captureView = false): ConfirmedFactsDeps => ({
      scopeKey: () => currentScope,
      subscribeScopeKey: (l) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; },
      savePinned: (_k, _id, _p, _a, signal) => {
        const gate = deferred<void>();
        saveCalls.push({ signal, gate });
        return gate.promise;
      },
      formatError: (e) => `fmt:${e instanceof Error ? e.message : String(e)}`,
      onError,
      ...(captureView
        ? {
            renderView: (vp) => {
              latestView = vp;
              return <ConfirmedFactsView {...vp} />;
            },
          }
        : {}),
    });
    const refresh = (signal?: AbortSignal) => {
      const gate = deferred<boolean>();
      refreshCalls.push({ signal, gate });
      return gate.promise;
    };

    const renderCurrent = (captureView = false) => root.render(
      <ConfirmedFacts
        kind={currentKind}
        id={currentId}
        value="Original"
        auth={AUTH}
        current={() => true}
        refresh={refresh}
        onSaved={onSaved}
        deps={makeDeps(captureView)}
      />,
    );

    // 1) Synchronous ABA burst on scopeKey (s1 -> s2 -> s1) aborts save and discards late resolve/reject.
    await act(async () => renderCurrent());
    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("ABA scope draft");
    await submitForm();
    expect(saveCalls).toHaveLength(1);

    await act(async () => {
      currentScope = "s2";
      scopeListeners.forEach((l) => l());
      currentScope = "s1";
      scopeListeners.forEach((l) => l());
    });
    expect(saveCalls[0].signal.aborted).toBe(true);
    expect(textarea()).toBeNull();
    await act(async () => { saveCalls[0].gate.resolve(); });
    await flush();
    expect(refreshCalls).toHaveLength(0);
    expect(onSaved).not.toHaveBeenCalled();

    // 2) Target (kind, id) ABA: (people, entry-1) -> (projects, entry-2) -> (people, entry-1) fences visit 1 while visit 2 saves.
    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("Visit 1 draft");
    await submitForm();
    expect(saveCalls).toHaveLength(2);
    await act(async () => { saveCalls[1].gate.resolve(); });
    await flush();
    expect(refreshCalls).toHaveLength(1);

    currentKind = "projects";
    currentId = "entry-2";
    await act(async () => renderCurrent());
    expect(refreshCalls[0].signal!.aborted).toBe(true);
    expect(textarea()).toBeNull();

    currentKind = "people";
    currentId = "entry-1";
    await act(async () => renderCurrent());
    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("Visit 2 draft");
    await submitForm();
    expect(saveCalls).toHaveLength(3);

    // Stale visit-1 refresh settles while visit-2 save is in flight: must NOT close editor or call onSaved.
    await act(async () => { refreshCalls[0].gate.resolve(true); });
    await flush();
    expect(onSaved).not.toHaveBeenCalled();
    expect(textarea()!.value).toBe("Visit 2 draft");
    expect(textarea()!.disabled).toBe(true);

    // Visit-2 save and refresh finish cleanly.
    await act(async () => { saveCalls[2].gate.resolve(); });
    await flush();
    expect(refreshCalls).toHaveLength(2);
    await act(async () => { refreshCalls[1].gate.resolve(true); });
    await flush();
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(textarea()).toBeNull();

    // 3) Closing and reopening editor programmatically while a save is in flight aborts old save and fences its settle.
    await act(async () => renderCurrent(true));
    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("First open draft");
    await submitForm();
    expect(saveCalls).toHaveLength(4);

    await act(async () => { latestView!.onClose(); });
    expect(saveCalls[3].signal.aborted).toBe(true);
    expect(textarea()).toBeNull();

    await act(async () => { latestView!.onStartEdit(); });
    await setTextarea("Reopened draft");
    await act(async () => { saveCalls[3].gate.reject(new Error("late closed failure")); });
    await flush();
    expect(onError).not.toHaveBeenCalled();
    expect(textarea()!.value).toBe("Reopened draft");
    expect(textarea()!.disabled).toBe(false);

    // 4) Unmount while save is in flight aborts signal and ignores late rejection.
    await submitForm();
    expect(saveCalls).toHaveLength(5);
    await act(async () => root.render(null));
    expect(saveCalls[4].signal.aborted).toBe(true);
    await act(async () => { saveCalls[4].gate.reject(new Error("after unmount")); });
    await flush();
    expect(onError).not.toHaveBeenCalled();
    expect(onSaved).toHaveBeenCalledTimes(1);
    expectNoDefaultSingletons();
  });

  it("recovers via try/finally across save sync throw, save reject, refresh false/reject, and throwing formatError/onError/onSaved observers", async () => {
    let saveMode: "sync-throw" | "reject" | "ok" = "sync-throw";
    let refreshMode: "false" | "reject" | "ok" = "false";
    let throwFormat = true;
    let throwOnError = true;
    let throwOnSaved = true;
    const onError = vi.fn(() => {
      if (throwOnError) throw new Error("observer crashed");
    });
    const onSaved = vi.fn(() => {
      if (throwOnSaved) throw new Error("onSaved crashed");
    });
    const savePinned = vi.fn(() => {
      if (saveMode === "sync-throw") throw new Error("sync save fail");
      if (saveMode === "reject") return Promise.reject(new Error("async save fail"));
      return Promise.resolve();
    });
    const refresh = vi.fn(() => {
      if (refreshMode === "false") return Promise.resolve(false);
      if (refreshMode === "reject") return Promise.reject(new Error("readback fail"));
      return Promise.resolve(true);
    });

    await act(async () => root.render(
      <ConfirmedFacts
        kind="people"
        id="example"
        value="Before"
        auth={AUTH}
        current={() => true}
        refresh={refresh}
        onSaved={onSaved}
        deps={{
          scopeKey: "recovery",
          savePinned,
          formatError: (err) => {
            if (throwFormat) throw new Error("formatter crashed");
            return `custom:${err instanceof Error ? err.message : String(err)}`;
          },
          onError,
        }}
      />,
    ));

    // Sync throw + throwing formatError + throwing onError -> falls back to errorText, keeps draft, releases busy.
    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("Keep my exact draft.");
    await submitForm();
    await flush();
    expect(textarea()!.value).toBe("Keep my exact draft.");
    expect(textarea()!.disabled).toBe(false);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("default-err:sync save fail");
    expect(onError).toHaveBeenCalledWith("default-err:sync save fail");

    // Async reject with working formatError -> keeps draft, shows custom formatted error, allows retry.
    throwFormat = false;
    throwOnError = false;
    saveMode = "reject";
    await click(t("knowledge-confirmedfacts.retry_save"));
    await flush();
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("custom:async save fail");
    expect(textarea()!.value).toBe("Keep my exact draft.");

    // Save succeeds, refresh returns false -> enters refresh-error without repeating savePinned.
    saveMode = "ok";
    await click(t("knowledge-confirmedfacts.retry_save"));
    await flush();
    expect(savePinned).toHaveBeenCalledTimes(3);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(findButton(t("knowledge-confirmedfacts.retry_refresh"))).toBeDefined();

    // Retry refresh rejects -> stays in refresh-error, surfaces custom error, releases busy.
    refreshMode = "reject";
    await click(t("knowledge-confirmedfacts.retry_refresh"));
    await flush();
    expect(savePinned).toHaveBeenCalledTimes(3);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("custom:readback fail");

    // Retry refresh succeeds even when onSaved throws -> closes editor cleanly and releases busy.
    refreshMode = "ok";
    await click(t("knowledge-confirmedfacts.retry_refresh"));
    await flush();
    expect(savePinned).toHaveBeenCalledTimes(3);
    expect(refresh).toHaveBeenCalledTimes(3);
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(textarea()).toBeNull();

    // Reopening and saving again proves busyRef was cleanly released even after throwing onSaved.
    throwOnSaved = false;
    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("Second save works.");
    await submitForm();
    await flush();
    expect(savePinned).toHaveBeenCalledTimes(4);
    expect(onSaved).toHaveBeenCalledTimes(2);
  });

  it("preserves backward-compatible default api and errorText behavior when deps is omitted and aborts default PUT on unmount", async () => {
    const req1 = deferred<void>();
    const req2 = deferred<void>();
    const signals: Array<AbortSignal | undefined> = [];
    mocks.api
      .mockImplementationOnce((_path: string, init: { signal?: AbortSignal }) => {
        signals.push(init.signal);
        return req1.promise;
      })
      .mockImplementationOnce((_path: string, init: { signal?: AbortSignal }) => {
        signals.push(init.signal);
        return req2.promise;
      });
    const refresh = vi.fn(async () => true);
    const onSaved = vi.fn();

    await act(async () => root.render(
      <ConfirmedFacts
        kind="people"
        id="my/id"
        value="Initial"
        auth={AUTH}
        current={() => true}
        refresh={refresh}
        onSaved={onSaved}
      />,
    ));

    await click(t("knowledge-confirmedfacts.edit"));
    await setTextarea("Updated default");
    await submitForm();
    expect(mocks.api).toHaveBeenCalledTimes(1);
    expect(mocks.api.mock.calls[0][0]).toBe("/kb/people/my%2Fid");
    expect(mocks.api.mock.calls[0][1]).toMatchObject({ method: "PUT", json: { pinned: "Updated default" }, ...AUTH });
    expect(signals[0]).toBeInstanceOf(AbortSignal);

    // Default error formatting via api.errorText.
    await act(async () => { req1.reject(new Error("hub unavailable")); });
    await flush();
    expect(mocks.errorText).toHaveBeenCalledTimes(1);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("default-err:hub unavailable");

    // Retry then unmount aborts default PUT signal.
    await click(t("knowledge-confirmedfacts.retry_save"));
    expect(signals[1]!.aborted).toBe(false);
    await act(async () => root.render(null));
    expect(signals[1]!.aborted).toBe(true);
    expect(onSaved).not.toHaveBeenCalled();
  });
});
