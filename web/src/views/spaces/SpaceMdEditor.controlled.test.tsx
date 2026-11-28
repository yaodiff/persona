// @vitest-environment jsdom
// Controlled SpaceMdEditor: pure view isolation, singleton bypass, sameValue getter caching,
// direct-to-subscribed transitions, abandoned concurrent render purity, scope/slug/open/ABA/unmount fencing,
// recoverable draft preservation on 409/400/network errors, dismissal blocking while saving,
// try/finally recovery on throws/rejections, and default fallback parity.
import { act, startTransition, StrictMode, Suspense, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { ApiError } from "../../lib/api";
import { editableParts, splitSpaceMd } from "../../lib/space-md";
import type { SpaceMd } from "../../lib/tracks";
import type { SpaceMdEditorDeps, SpaceMdEditorViewProps } from "./SpaceMdEditor";

const mocks = vi.hoisted(() => ({
  readSpaceMd: vi.fn(),
  writeSpaceMd: vi.fn(),
  spaceMdError: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../lib/tracks", async (original) => {
  const real = await original<typeof import("../../lib/tracks")>();
  return {
    ...real,
    readSpaceMd: (slug: string) => mocks.readSpaceMd(slug),
    writeSpaceMd: (slug: string, content: string, baseDigest: string) => mocks.writeSpaceMd(slug, content, baseDigest),
    spaceMdError: (e: unknown) => {
      mocks.spaceMdError(e);
      return real.spaceMdError(e);
    },
  };
});

vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: (message: string, tone?: string) => mocks.toast(message, tone),
}));

const { SpaceMdEditor, SpaceMdEditorView, spaceMdErrorText } = await import("./SpaceMdEditor");

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const MD_V1: SpaceMd = {
  content: "# Money\n\nOriginal notes.\n\n<!-- persona:managed:state:begin -->\nTaxes filed.\n<!-- persona:managed:state:end -->\n",
  digest: "d1",
  state: "Taxes filed.",
};

const MD_V2: SpaceMd = {
  content: "# Money\n\nServer updated.\n\n<!-- persona:managed:state:begin -->\nTaxes filed.\n<!-- persona:managed:state:end -->\n",
  digest: "d2",
  state: "Taxes filed.",
};

const expectNoDefaultSingletons = () => {
  expect(mocks.readSpaceMd).not.toHaveBeenCalled();
  expect(mocks.writeSpaceMd).not.toHaveBeenCalled();
  expect(mocks.spaceMdError).not.toHaveBeenCalled();
  expect(mocks.toast).not.toHaveBeenCalled();
};

const setTextarea = async (el: HTMLTextAreaElement, value: string) => {
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
};

const activeDialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const findButton = (rootEl: ParentNode, text: string) =>
  [...rootEl.querySelectorAll("button")].find((b) => b.textContent?.trim() === text) as HTMLButtonElement | undefined;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  window.matchMedia ??= ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
  mocks.readSpaceMd.mockReset();
  mocks.writeSpaceMd.mockReset();
  mocks.spaceMdError.mockReset();
  mocks.toast.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("Pure SpaceMdEditorView", () => {
  it("renders managed blocks, editable fields, read/save errors, and blocks dismissal while saving without touching singletons", async () => {
    const onEdit = vi.fn();
    const onSave = vi.fn();
    const onReload = vi.fn();
    const onClose = vi.fn();
    const shape = editableParts(splitSpaceMd(MD_V1.content));

    // Read error state with Retry button.
    await act(async () => root.render(
      <SpaceMdEditorView
        open
        shape={null}
        edits={{}}
        readError="Offline right now"
        saveError={null}
        changed={false}
        saving={false}
        onEdit={onEdit}
        onSave={onSave}
        onReload={onReload}
        onClose={onClose}
      />,
    ));
    expect(activeDialog()?.querySelector('[role="alert"]')?.textContent).toContain("Offline right now");
    await act(async () => { findButton(activeDialog()!, t("spaces.retry"))!.click(); });
    expect(onReload).toHaveBeenCalledTimes(1);

    // Editable state with stale error + saving blocks Cancel and Escape dismissal.
    await act(async () => root.render(
      <SpaceMdEditorView
        open
        shape={shape}
        edits={{ 0: "Draft body" }}
        readError=""
        saveError={{ kind: "stale", text: t("spaces.md_stale") }}
        changed
        saving
        onEdit={onEdit}
        onSave={onSave}
        onReload={onReload}
        onClose={onClose}
      />,
    ));
    expect(activeDialog()?.querySelector("textarea")?.value).toBe("Draft body");
    expect(activeDialog()?.textContent).toContain("Taxes filed.");
    expect(activeDialog()?.querySelector('[role="alert"]')?.textContent).toBe(t("spaces.md_stale"));
    expect(findButton(activeDialog()!, t("spaces.cancel"))?.disabled).toBe(true);
    expect(findButton(activeDialog()!, t("spaces.md_reload"))?.disabled).toBe(true);
    expect(findButton(activeDialog()!, t("spaces.save"))?.getAttribute("aria-busy")).toBe("true");
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).not.toHaveBeenCalled();

    // When not saving, Cancel calls onClose and Save calls onSave.
    await act(async () => root.render(
      <SpaceMdEditorView
        open
        shape={shape}
        edits={{ 0: "Draft body" }}
        readError=""
        saveError={null}
        changed
        saving={false}
        onEdit={onEdit}
        onSave={onSave}
        onReload={onReload}
        onClose={onClose}
      />,
    ));
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(onSave).toHaveBeenCalledTimes(1);
    await act(async () => { findButton(activeDialog()!, t("spaces.cancel"))!.click(); });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(spaceMdErrorText("other", new Error("boom"), () => "custom:boom")).toBe(t("spaces.md_failed", { error: "custom:boom" }));
    expectNoDefaultSingletons();
  });
});

describe("Controlled SpaceMdEditor getter caching, subscription stability, and renderView", () => {
  it("caches fresh-object md getter via sameValue, avoids subscription churn, and preserves typed draft across direct-to-subscribed switch", async () => {
    const scopeListeners = new Set<() => void>();
    const mdListeners = new Set<() => void>();
    let currentMd = MD_V1;
    let reads = 0;
    const seenShapes: Array<SpaceMdEditorViewProps["shape"]> = [];

    const captureView = (props: SpaceMdEditorViewProps) => {
      seenShapes.push(props.shape);
      return <SpaceMdEditorView {...props} />;
    };

    await act(async () => root.render(
      <StrictMode>
        <SpaceMdEditor
          open
          slug="money"
          onClose={() => {}}
          deps={{
            scopeKey: "s1",
            md: MD_V1,
            notify: vi.fn(),
            renderView: captureView,
          }}
        />
      </StrictMode>,
    ));

    await setTextarea(activeDialog()!.querySelector("textarea")!, "# Money\n\nMy unsaved draft.\n\n");
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("# Money\n\nMy unsaved draft.\n\n");

    const subscribeScopeKey = vi.fn((l: () => void) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; });
    const subscribeMd = vi.fn((l: () => void) => { mdListeners.add(l); return () => { mdListeners.delete(l); }; });
    const renderSub = () => root.render(
      <StrictMode>
        <SpaceMdEditor
          open
          slug="money"
          onClose={() => {}}
          deps={{
            scopeKey: () => "s1",
            subscribeScopeKey,
            md: () => {
              reads += 1;
              return { ...currentMd };
            },
            subscribeMd,
            notify: vi.fn(),
            renderView: captureView,
          }}
        />
      </StrictMode>,
    );
    await act(async () => renderSub());

    // Draft survives switching from direct md prop to subscribed fresh-object getter on the same scope.
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("# Money\n\nMy unsaved draft.\n\n");
    expect(reads).toBeLessThan(20);
    const stableShape = seenShapes.at(-1)!;

    // Equal re-emission keeps memoized shape intact.
    await act(async () => { mdListeners.forEach((l) => l()); });
    expect(seenShapes.at(-1)).toBe(stableShape);

    // Inline arrow getters do not churn subscriptions.
    const scopeSubCount = subscribeScopeKey.mock.calls.length;
    const mdSubCount = subscribeMd.mock.calls.length;
    await act(async () => renderSub());
    await act(async () => renderSub());
    expect(subscribeScopeKey).toHaveBeenCalledTimes(scopeSubCount);
    expect(subscribeMd).toHaveBeenCalledTimes(mdSubCount);

    // Changing underlying md updates the shape cleanly.
    currentMd = MD_V2;
    await act(async () => { mdListeners.forEach((l) => l()); });
    expect(seenShapes.at(-1)).not.toBe(stableShape);
    expectNoDefaultSingletons();
  });
});

describe("Controlled SpaceMdEditor error recovery, dismissal guard, fencing, render purity, and default fallback", () => {
  it("preserves recoverable draft on 409 conflict, 400 managed-block, and network errors, blocks dismissal while saving, and recovers via try/finally", async () => {
    const onClose = vi.fn();
    const onSaved = vi.fn();
    const notify = vi.fn(() => { throw new Error("toast crashed"); });
    const onError = vi.fn(() => { throw new Error("observer crashed"); });
    let readCount = 0;
    let writeGate: ReturnType<typeof deferred<SpaceMd>> | null = null;
    let writeMode: "deferred" | "stale" | "managed" | "network" | "syncThrow" | "ok" = "deferred";

    const deps: SpaceMdEditorDeps = {
      scopeKey: "s1",
      readSpaceMd: () => {
        readCount += 1;
        return Promise.resolve(readCount === 1 ? MD_V1 : MD_V2);
      },
      writeSpaceMd: (_slug, content) => {
        if (writeMode === "syncThrow") throw new Error("sync write boom");
        if (writeMode === "stale") return Promise.reject(new ApiError(409, "conflict", {}));
        if (writeMode === "managed") return Promise.reject(new ApiError(400, "managed", {}));
        if (writeMode === "network") return Promise.reject(new Error("socket hangup"));
        if (writeMode === "deferred") {
          writeGate = deferred<SpaceMd>();
          return writeGate.promise;
        }
        return Promise.resolve({ content, digest: "d3", state: "Taxes filed." });
      },
      spaceMdError: (e) => {
        if (e instanceof ApiError && e.status === 409) return "stale";
        if (e instanceof ApiError && e.status === 400) return "managed";
        return "other";
      },
      formatError: () => { throw new Error("format crashed"); },
      notify,
      onError,
    };

    await act(async () => root.render(<SpaceMdEditor open slug="money" onClose={onClose} onSaved={onSaved} deps={deps} />));
    await flush();

    // Client-side managed block marker injection keeps draft and blocks writeSpaceMd.
    await setTextarea(activeDialog()!.querySelector("textarea")!, "<!-- persona:managed:state:begin -->\nhacked");
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toBe(t("spaces.md_managed"));
    expect(activeDialog()!.querySelector("textarea")!.value).toContain("hacked");

    // Valid edit -> start deferred save -> dismissal (Cancel & Escape) is blocked while saving is in flight.
    await setTextarea(activeDialog()!.querySelector("textarea")!, "# Money\n\nImportant unsaved edits.\n\n");
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(findButton(activeDialog()!, t("spaces.save"))?.getAttribute("aria-busy")).toBe("true");
    expect(findButton(activeDialog()!, t("spaces.cancel"))?.disabled).toBe(true);
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).not.toHaveBeenCalled();

    // Reject deferred save with 409 conflict -> draft preserved, Reload button offered, saving cleared.
    await act(async () => {
      writeGate!.reject(new ApiError(409, "changed meanwhile", {}));
      await Promise.resolve();
    });
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toBe(t("spaces.md_stale"));
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("# Money\n\nImportant unsaved edits.\n\n");
    expect(findButton(activeDialog()!, t("spaces.md_reload"))).toBeTruthy();
    expect(findButton(activeDialog()!, t("spaces.cancel"))?.disabled).toBe(false);

    // Server 400 managed-block error preserves draft and releases saving.
    writeMode = "managed";
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    await flush();
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toBe(t("spaces.md_managed"));
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("# Money\n\nImportant unsaved edits.\n\n");

    // Network error preserves draft and falls back to errorText when formatError throws.
    writeMode = "network";
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    await flush();
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toBe(t("spaces.md_failed", { error: "socket hangup" }));
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("# Money\n\nImportant unsaved edits.\n\n");

    // Synchronous throw preserves draft and releases saving via try/finally.
    writeMode = "syncThrow";
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(activeDialog()!.querySelector('[role="alert"]')?.textContent).toBe(t("spaces.md_failed", { error: "sync write boom" }));
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("# Money\n\nImportant unsaved edits.\n\n");
    expect(findButton(activeDialog()!, t("spaces.save"))?.getAttribute("aria-busy")).toBeNull();

    // Retry succeeds even though notify throws -> calls onSaved and onClose.
    writeMode = "ok";
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    await flush();
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(notify).toHaveBeenCalledWith(t("spaces.md_saved"), "ok");
    expectNoDefaultSingletons();
  });

  it("aborts in-flight read/save and resets drafts/errors across scope switch, slug switch, open ABA, synchronous scope ABA, and unmount", async () => {
    const scopeListeners = new Set<() => void>();
    let currentScope = "s1";
    let currentSlug = "money";
    let currentOpen = true;
    const onClose = vi.fn();
    const onSaved = vi.fn();
    const notify = vi.fn();
    const onError = vi.fn();

    const readCalls: Array<{ slug: string; signal: AbortSignal; gate: ReturnType<typeof deferred<SpaceMd>> }> = [];
    const writeCalls: Array<{ slug: string; signal: AbortSignal; gate: ReturnType<typeof deferred<SpaceMd>> }> = [];

    const makeDeps = (): SpaceMdEditorDeps => ({
      scopeKey: () => currentScope,
      subscribeScopeKey: (l) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; },
      readSpaceMd: (slug, signal) => {
        const gate = deferred<SpaceMd>();
        readCalls.push({ slug, signal, gate });
        return gate.promise;
      },
      writeSpaceMd: (slug, _c, _d, signal) => {
        const gate = deferred<SpaceMd>();
        writeCalls.push({ slug, signal, gate });
        return gate.promise;
      },
      spaceMdError: () => "other",
      notify,
      onError,
    });

    const renderEditor = () => root.render(
      <SpaceMdEditor open={currentOpen} slug={currentSlug} onClose={onClose} onSaved={onSaved} deps={makeDeps()} />,
    );

    await act(async () => renderEditor());
    expect(readCalls).toHaveLength(1);
    await act(async () => {
      readCalls[0].gate.resolve(MD_V1);
      await Promise.resolve();
    });

    // 1) Type draft, start save, then switch slug money -> health: aborts save & resets draft.
    await setTextarea(activeDialog()!.querySelector("textarea")!, "# Money\n\nStale draft\n\n");
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(writeCalls).toHaveLength(1);

    currentSlug = "health";
    await act(async () => renderEditor());
    expect(writeCalls[0].signal.aborted).toBe(true);
    await act(async () => {
      writeCalls[0].gate.resolve(MD_V2);
      await Promise.resolve();
    });
    expect(onSaved).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(notify).not.toHaveBeenCalled();

    // Resolve health read.
    expect(readCalls).toHaveLength(2);
    await act(async () => {
      readCalls[1].gate.resolve(MD_V1);
      await Promise.resolve();
    });

    // 2) Synchronous ABA scope burst (s1 -> s2 -> s1) aborts in-flight save and discards late rejection.
    await setTextarea(activeDialog()!.querySelector("textarea")!, "# Health\n\nABA draft\n\n");
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(writeCalls).toHaveLength(2);

    await act(async () => {
      currentScope = "s2";
      scopeListeners.forEach((l) => l());
      currentScope = "s1";
      scopeListeners.forEach((l) => l());
    });
    expect(writeCalls[1].signal.aborted).toBe(true);
    await act(async () => {
      writeCalls[1].gate.reject(new Error("stale ABA error"));
      await Promise.resolve();
    });
    expect(onError).not.toHaveBeenCalled();

    // Resolve new read after ABA burst.
    expect(readCalls).toHaveLength(3);
    await act(async () => {
      readCalls[2].gate.resolve(MD_V1);
      await Promise.resolve();
    });

    // 3) open true -> false -> true (ABA) aborts in-flight save and clears draft.
    await setTextarea(activeDialog()!.querySelector("textarea")!, "# Health\n\nBefore close\n\n");
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(writeCalls).toHaveLength(3);

    currentOpen = false;
    await act(async () => renderEditor());
    expect(writeCalls[2].signal.aborted).toBe(true);
    currentOpen = true;
    await act(async () => renderEditor());
    expect(readCalls).toHaveLength(4);
    await act(async () => {
      readCalls[3].gate.resolve(MD_V1);
      await Promise.resolve();
    });
    expect(activeDialog()!.querySelector("textarea")!.value).toBe("# Money\n\nOriginal notes.\n\n");

    // 4) Unmount aborts active save and ignores late rejection.
    await setTextarea(activeDialog()!.querySelector("textarea")!, "# Health\n\nBefore unmount\n\n");
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(writeCalls).toHaveLength(4);
    await act(async () => root.render(null));
    expect(writeCalls[3].signal.aborted).toBe(true);
    await act(async () => {
      writeCalls[3].gate.reject(new Error("unmounted write"));
      await Promise.resolve();
    });
    expect(onError).not.toHaveBeenCalled();
    expectNoDefaultSingletons();
  });

  it("keeps committed draft and in-flight save intact when a concurrent transition render is abandoned", async () => {
    const suspendGate = deferred<void>();
    const writeGate = deferred<SpaceMd>();
    let capturedSignal: AbortSignal | null = null;
    const onSaved = vi.fn();
    const onClose = vi.fn();
    const notify = vi.fn();
    const renderedScopes: string[] = [];

    function SuspendIf({ block }: { block: boolean }) {
      if (block) throw suspendGate.promise;
      return null;
    }

    let setHarness!: (next: { scope: string; block: boolean }) => void;
    function Harness() {
      const [state, setState] = useState({ scope: "s1", block: false });
      setHarness = setState;
      renderedScopes.push(state.scope);
      return (
        <Suspense fallback={<div data-fallback />}>
          <SpaceMdEditor
            open
            slug="money"
            onClose={onClose}
            onSaved={onSaved}
            deps={{
              scopeKey: state.scope,
              md: MD_V1,
              writeSpaceMd: (_slug, _content, _digest, signal) => {
                capturedSignal = signal;
                return writeGate.promise;
              },
              notify,
            }}
          />
          <SuspendIf block={state.block} />
        </Suspense>
      );
    }

    await act(async () => root.render(<Harness />));
    await setTextarea(activeDialog()!.querySelector("textarea")!, "# Money\n\nConcurrent draft\n\n");
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    expect(capturedSignal!.aborted).toBe(false);

    // Suspended concurrent transition to s2 must NOT abort s1's save or corrupt committed refs.
    await act(async () => {
      startTransition(() => setHarness({ scope: "s2", block: true }));
    });
    expect(renderedScopes).toContain("s2");
    expect(capturedSignal!.aborted).toBe(false);

    // Abandon transition back to s1; resolving writeGate completes cleanly.
    await act(async () => setHarness({ scope: "s1", block: false }));
    expect(capturedSignal!.aborted).toBe(false);
    await act(async () => {
      writeGate.resolve(MD_V2);
      await Promise.resolve();
    });
    expect(onSaved).toHaveBeenCalledWith(MD_V2);
    expect(notify).toHaveBeenCalledWith(t("spaces.md_saved"), "ok");
    expect(onClose).toHaveBeenCalledTimes(1);
    expectNoDefaultSingletons();
  });

  it("preserves 100% backward-compatible default singleton behavior when deps is omitted", async () => {
    mocks.readSpaceMd.mockResolvedValue(MD_V1);
    mocks.writeSpaceMd.mockResolvedValue(MD_V2);
    const onClose = vi.fn();
    const onSaved = vi.fn();

    await act(async () => root.render(<SpaceMdEditor open slug="money" onClose={onClose} onSaved={onSaved} />));
    await flush();
    expect(mocks.readSpaceMd).toHaveBeenCalledWith("money");

    await setTextarea(activeDialog()!.querySelector("textarea")!, "# Money\n\nUpdated.\n\n");
    await act(async () => { findButton(activeDialog()!, t("spaces.save"))!.click(); });
    await flush();
    expect(mocks.writeSpaceMd).toHaveBeenCalledWith(
      "money",
      "# Money\n\nUpdated.\n\n<!-- persona:managed:state:begin -->\nTaxes filed.\n<!-- persona:managed:state:end -->\n",
      "d1",
    );
    expect(onSaved).toHaveBeenCalledWith(MD_V2);
    expect(mocks.toast).toHaveBeenCalledWith(t("spaces.md_saved"), "ok");
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
