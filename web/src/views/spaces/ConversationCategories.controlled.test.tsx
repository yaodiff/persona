// @vitest-environment jsdom
// Controlled ConversationCategories: pure view isolation, singleton bypass, sameValue getter caching,
// direct-to-subscribed transitions, abandoned concurrent render purity, scope/sid/ABA/unmount fencing,
// try/finally recovery on sync throws & async rejections, and default fallback parity.
import { act, startTransition, StrictMode, Suspense, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { ConversationCategories as Categories } from "../../lib/tracks";
import type { ConversationCategoriesDeps, ConversationCategoriesViewProps } from "./ConversationCategories";
import { categoryLabel } from "./labels";

const mocks = vi.hoisted(() => ({
  readConversationCategories: vi.fn(),
  changeConversationCategories: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../lib/tracks", async (original) => {
  const real = await original<typeof import("../../lib/tracks")>();
  return {
    ...real,
    readConversationCategories: (sid: string) => mocks.readConversationCategories(sid),
    changeConversationCategories: (sid: string, change: { add?: string[]; remove?: string[] }) =>
      mocks.changeConversationCategories(sid, change),
  };
});

vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: (message: string, tone?: string) => mocks.toast(message, tone),
}));

const { ConversationCategories, ConversationCategoriesView } = await import("./ConversationCategories");

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const CATS_A: Categories = {
  categories: ["health", "finance"],
  floor: ["health"],
  removed: ["emotion"],
};

const CATS_B: Categories = {
  categories: ["legal_id"],
  floor: [],
  removed: ["relationships"],
};

const expectNoDefaultSingletons = () => {
  expect(mocks.readConversationCategories).not.toHaveBeenCalled();
  expect(mocks.changeConversationCategories).not.toHaveBeenCalled();
  expect(mocks.toast).not.toHaveBeenCalled();
};

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
  mocks.readConversationCategories.mockReset();
  mocks.changeConversationCategories.mockReset();
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

describe("Pure ConversationCategoriesView", () => {
  it("renders null when empty, renders floor locks/remove buttons/restore menu without touching singletons", async () => {
    const onRemove = vi.fn();
    const onAdd = vi.fn();
    const onOpenMenu = vi.fn();
    const onCloseMenu = vi.fn();

    await act(async () => root.render(
      <ConversationCategoriesView
        categories={null}
        menuAnchor={null}
        onRemove={onRemove}
        onAdd={onAdd}
        onOpenMenu={onOpenMenu}
        onCloseMenu={onCloseMenu}
      />,
    ));
    expect(host.innerHTML).toBe("");

    await act(async () => root.render(
      <ConversationCategoriesView
        categories={{ categories: [], floor: [], removed: [] }}
        menuAnchor={null}
        onRemove={onRemove}
        onAdd={onAdd}
        onOpenMenu={onOpenMenu}
        onCloseMenu={onCloseMenu}
      />,
    ));
    expect(host.innerHTML).toBe("");

    await act(async () => root.render(
      <ConversationCategoriesView
        categories={CATS_A}
        busyCategory="finance"
        menuAnchor={null}
        onRemove={onRemove}
        onAdd={onAdd}
        onOpenMenu={onOpenMenu}
        onCloseMenu={onCloseMenu}
      />,
    ));
    expect(host.textContent).toContain(categoryLabel("health"));
    expect(host.textContent).toContain(categoryLabel("finance"));
    expect(host.querySelector(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("health") })}"]`)).toBeNull();
    const removeFinance = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("finance") })}"]`)!;
    expect(removeFinance.disabled).toBe(true);

    // Non-busy remove triggers callback; restore button opens menu.
    await act(async () => root.render(
      <ConversationCategoriesView
        categories={CATS_A}
        busyCategory=""
        menuAnchor={null}
        onRemove={onRemove}
        onAdd={onAdd}
        onOpenMenu={onOpenMenu}
        onCloseMenu={onCloseMenu}
      />,
    ));
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("finance") })}"]`)!.click();
    });
    expect(onRemove).toHaveBeenCalledWith("finance");

    const restoreBtn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_restore")}"]`)!;
    await act(async () => { restoreBtn.click(); });
    expect(onOpenMenu).toHaveBeenCalledTimes(1);

    // Passing menuAnchor renders the restore menu and invokes onAdd when selected.
    await act(async () => root.render(
      <ConversationCategoriesView
        categories={CATS_A}
        menuAnchor={restoreBtn}
        onRemove={onRemove}
        onAdd={onAdd}
        onOpenMenu={onOpenMenu}
        onCloseMenu={onCloseMenu}
      />,
    ));
    const restoreItem = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (b) => b.textContent?.includes(categoryLabel("emotion")),
    )!;
    await act(async () => { restoreItem.click(); });
    expect(onAdd).toHaveBeenCalledWith("emotion");
    expectNoDefaultSingletons();
  });
});

describe("Controlled ConversationCategories getter caching, subscription stability, and renderView", () => {
  it("caches fresh-object getter snapshots via sameValue, avoids subscription churn, and preserves open menu across direct-to-subscribed transitions", async () => {
    const scopeListeners = new Set<() => void>();
    const catListeners = new Set<() => void>();
    const seenCats: Array<Categories | null> = [];
    let currentCats: Categories = CATS_A;
    let reads = 0;

    const captureView = (props: ConversationCategoriesViewProps) => {
      seenCats.push(props.categories);
      return <ConversationCategoriesView {...props} />;
    };

    // Start with direct props on scope s1.
    await act(async () => root.render(
      <StrictMode>
        <ConversationCategories
          sid="c1"
          deps={{
            scopeKey: "s1",
            categories: CATS_A,
            notify: vi.fn(),
            renderView: captureView,
          }}
        />
      </StrictMode>,
    ));

    // Open the restore menu in direct mode.
    const restoreBtn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_restore")}"]`)!;
    await act(async () => { restoreBtn.click(); });
    expect(document.querySelector('[role="menu"]')).not.toBeNull();

    // Transition in-place to fresh-object getter + subscribe on the same scopeKey & sid.
    const subscribeScopeKey = vi.fn((l: () => void) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; });
    const subscribeCategories = vi.fn((l: () => void) => { catListeners.add(l); return () => { catListeners.delete(l); }; });
    const renderSub = () => root.render(
      <StrictMode>
        <ConversationCategories
          sid="c1"
          deps={{
            scopeKey: () => "s1",
            subscribeScopeKey,
            categories: () => {
              reads += 1;
              return {
                categories: [...currentCats.categories],
                floor: [...currentCats.floor],
                removed: [...currentCats.removed],
              };
            },
            subscribeCategories,
            notify: vi.fn(),
            renderView: captureView,
          }}
        />
      </StrictMode>,
    );
    await act(async () => renderSub());

    // Open menu is preserved across direct -> subscribed switch on the same scope.
    expect(document.querySelector('[role="menu"]')).not.toBeNull();
    expect(reads).toBeLessThan(20);
    const stableRef = seenCats.at(-1)!;

    // Equal re-emission keeps the exact cached Categories object reference.
    await act(async () => { catListeners.forEach((l) => l()); });
    expect(seenCats.at(-1)).toBe(stableRef);

    // Re-rendering with fresh inline arrow getters must not churn subscriptions.
    const scopeSubCount = subscribeScopeKey.mock.calls.length;
    const catSubCount = subscribeCategories.mock.calls.length;
    await act(async () => renderSub());
    await act(async () => renderSub());
    expect(subscribeScopeKey).toHaveBeenCalledTimes(scopeSubCount);
    expect(subscribeCategories).toHaveBeenCalledTimes(catSubCount);

    // Mutating underlying categories updates the rendered chips.
    currentCats = CATS_B;
    await act(async () => { catListeners.forEach((l) => l()); });
    expect(host.textContent).toContain(categoryLabel("legal_id"));
    expectNoDefaultSingletons();
  });
});

describe("Controlled ConversationCategories fencing, render purity, try/finally recovery, and default fallback", () => {
  it("aborts in-flight read/change requests and resets chips/menu/busy across scope switch, sid switch, synchronous ABA bursts, and unmount", async () => {
    const scopeListeners = new Set<() => void>();
    let currentScope = "s1";
    let currentSid = "c1";
    const notify = vi.fn();
    const onError = vi.fn();

    const readCalls: Array<{ sid: string; signal: AbortSignal; gate: ReturnType<typeof deferred<Categories>> }> = [];
    const changeCalls: Array<{ sid: string; change: { add?: string[]; remove?: string[] }; signal: AbortSignal; gate: ReturnType<typeof deferred<Categories>> }> = [];

    const makeDeps = (): ConversationCategoriesDeps => ({
      scopeKey: () => currentScope,
      subscribeScopeKey: (l) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; },
      readConversationCategories: (sid, signal) => {
        const gate = deferred<Categories>();
        readCalls.push({ sid, signal, gate });
        return gate.promise;
      },
      changeConversationCategories: (sid, change, signal) => {
        const gate = deferred<Categories>();
        changeCalls.push({ sid, change, signal, gate });
        return gate.promise;
      },
      notify,
      onError,
    });

    await act(async () => root.render(<ConversationCategories sid={currentSid} deps={makeDeps()} />));
    expect(readCalls).toHaveLength(1);

    // 1) Switch sid before read #1 resolves: read #1 aborts and its late resolution is discarded.
    currentSid = "c2";
    await act(async () => root.render(<ConversationCategories sid={currentSid} deps={makeDeps()} />));
    expect(readCalls[0].signal.aborted).toBe(true);
    expect(readCalls).toHaveLength(2);
    await act(async () => {
      readCalls[0].gate.resolve(CATS_A);
      await Promise.resolve();
    });
    expect(host.innerHTML).toBe("");

    // Resolve read #2 -> chips appear.
    await act(async () => {
      readCalls[1].gate.resolve(CATS_A);
      await Promise.resolve();
    });
    expect(host.textContent).toContain(categoryLabel("finance"));

    // 2) Open restore menu and start remove mutation, then switch scope s1 -> s2.
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_restore")}"]`)!.click();
    });
    expect(document.querySelector('[role="menu"]')).not.toBeNull();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("finance") })}"]`)!.click();
    });
    expect(changeCalls).toHaveLength(1);
    expect(changeCalls[0].signal.aborted).toBe(false);

    currentScope = "s2";
    await act(async () => { scopeListeners.forEach((l) => l()); });
    expect(changeCalls[0].signal.aborted).toBe(true);
    expect(document.querySelector('[role="menu"]')).toBeNull();
    expect(host.innerHTML).toBe("");
    await act(async () => {
      changeCalls[0].gate.resolve(CATS_B);
      await Promise.resolve();
    });
    expect(notify).not.toHaveBeenCalled();

    // Resolve read #3 for s2.
    expect(readCalls).toHaveLength(3);
    await act(async () => {
      readCalls[2].gate.resolve(CATS_A);
      await Promise.resolve();
    });

    // 3) Synchronous ABA scope burst (s2 -> s3 -> s2) aborts active change and closes open menu.
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_restore")}"]`)!.click();
    });
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("finance") })}"]`)!.click();
    });
    expect(changeCalls).toHaveLength(2);
    await act(async () => {
      currentScope = "s3";
      scopeListeners.forEach((l) => l());
      currentScope = "s2";
      scopeListeners.forEach((l) => l());
    });
    expect(changeCalls[1].signal.aborted).toBe(true);
    expect(document.querySelector('[role="menu"]')).toBeNull();
    await act(async () => {
      changeCalls[1].gate.reject(new Error("stale ABA failure"));
      await Promise.resolve();
    });
    expect(onError).not.toHaveBeenCalled();
    expect(notify).not.toHaveBeenCalled();

    // 4) Unmount aborts active read and active change and ignores late rejections.
    expect(readCalls).toHaveLength(4);
    await act(async () => {
      readCalls[3].gate.resolve(CATS_A);
      await Promise.resolve();
    });
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("finance") })}"]`)!.click();
    });
    expect(changeCalls).toHaveLength(3);
    await act(async () => root.render(null));
    expect(changeCalls[2].signal.aborted).toBe(true);
    await act(async () => {
      changeCalls[2].gate.reject(new Error("unmounted failure"));
      await Promise.resolve();
    });
    expect(onError).not.toHaveBeenCalled();
    expect(notify).not.toHaveBeenCalled();
    expectNoDefaultSingletons();
  });

  it("keeps committed chips, menu, and in-flight change intact across an abandoned concurrent transition render", async () => {
    const suspendGate = deferred<void>();
    const changeGate = deferred<Categories>();
    let capturedSignal: AbortSignal | null = null;
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
          <ConversationCategories
            sid="c1"
            deps={{
              scopeKey: state.scope,
              readConversationCategories: () => CATS_A,
              changeConversationCategories: (_sid, _change, signal) => {
                capturedSignal = signal;
                return changeGate.promise;
              },
              notify,
            }}
          />
          <SuspendIf block={state.block} />
        </Suspense>
      );
    }

    await act(async () => root.render(<Harness />));
    expect(host.textContent).toContain(categoryLabel("finance"));

    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("finance") })}"]`)!.click();
    });
    expect(capturedSignal!.aborted).toBe(false);

    // Suspended concurrent transition to s2 must NOT abort s1's change or corrupt committed refs.
    await act(async () => {
      startTransition(() => setHarness({ scope: "s2", block: true }));
    });
    expect(renderedScopes).toContain("s2");
    expect(capturedSignal!.aborted).toBe(false);

    // Abandon transition back to s1; resolving changeGate completes cleanly.
    await act(async () => setHarness({ scope: "s1", block: false }));
    expect(capturedSignal!.aborted).toBe(false);
    await act(async () => {
      changeGate.resolve({ categories: ["health"], floor: ["health"], removed: ["emotion", "finance"] });
      await Promise.resolve();
    });
    expect(notify).toHaveBeenCalledWith(t("spaces.category_removed", { name: categoryLabel("finance") }), "ok");
    expect(host.textContent).not.toContain(categoryLabel("finance"));
    expectNoDefaultSingletons();
  });

  it("recovers busy via try/finally on sync throw and async reject even when formatError, notify, and onError throw", async () => {
    let mode: "sync" | "async" | "ok" = "sync";
    const notify = vi.fn(() => { throw new Error("toast crashed"); });
    const formatError = vi.fn(() => { throw new Error("format crashed"); });
    const onError = vi.fn(() => { throw new Error("observer crashed"); });

    await act(async () => root.render(
      <ConversationCategories
        sid="c1"
        deps={{
          scopeKey: "s1",
          readConversationCategories: () => CATS_A,
          changeConversationCategories: () => {
            if (mode === "sync") throw new Error("sync boom");
            if (mode === "async") return Promise.reject(new Error("async boom"));
            return Promise.resolve({ categories: ["health"], floor: ["health"], removed: ["emotion", "finance"] });
          },
          notify,
          formatError,
          onError,
        }}
      />,
    ));

    const removeBtn = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("finance") })}"]`)!;

    // Sync throw -> busy released, fallback errorText used because formatError threw.
    await act(async () => { removeBtn().click(); });
    expect(removeBtn().disabled).toBe(false);
    expect(notify).toHaveBeenLastCalledWith(t("spaces.category_failed", { error: "sync boom" }), "error");
    expect(onError).toHaveBeenCalledTimes(1);

    // Async reject -> busy released cleanly.
    mode = "async";
    await act(async () => { removeBtn().click(); });
    await flush();
    expect(removeBtn().disabled).toBe(false);
    expect(notify).toHaveBeenLastCalledWith(t("spaces.category_failed", { error: "async boom" }), "error");
    expect(onError).toHaveBeenCalledTimes(2);

    // Retry succeeds cleanly.
    mode = "ok";
    await act(async () => { removeBtn().click(); });
    await flush();
    expect(host.textContent).not.toContain(categoryLabel("finance"));
    expect(notify).toHaveBeenLastCalledWith(t("spaces.category_removed", { name: categoryLabel("finance") }), "ok");
    expectNoDefaultSingletons();
  });

  it("preserves 100% backward-compatible default singleton behavior when deps is omitted", async () => {
    mocks.readConversationCategories.mockResolvedValue(CATS_A);
    mocks.changeConversationCategories.mockResolvedValue({
      categories: ["health"],
      floor: ["health"],
      removed: ["emotion", "finance"],
    });

    await act(async () => root.render(<ConversationCategories sid="c-default" />));
    await flush();
    expect(mocks.readConversationCategories).toHaveBeenCalledWith("c-default");
    expect(host.textContent).toContain(categoryLabel("finance"));

    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("spaces.category_remove", { name: categoryLabel("finance") })}"]`)!.click();
    });
    await flush();
    expect(mocks.changeConversationCategories).toHaveBeenCalledWith("c-default", { remove: ["finance"] });
    expect(mocks.toast).toHaveBeenCalledWith(t("spaces.category_removed", { name: categoryLabel("finance") }), "ok");
  });
});
