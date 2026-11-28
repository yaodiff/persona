// @vitest-environment jsdom
// Controlled NewSpace: pure view isolation, singleton bypass (including useTracks/noteTrack/router),
// sameValue getter caching, direct-to-subscribed transitions, abandoned concurrent render purity,
// scope/ABA/unmount fencing, ref-locked double-submit guard, try/finally recovery with draft preservation,
// and default fallback parity.
import { act, startTransition, StrictMode, Suspense, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { Route } from "../../lib/router";
import type { Track, TrackCreate, TrackKind } from "../../lib/tracks";
import type { NewSpaceDeps, NewSpaceViewProps } from "./NewSpace";

const mocks = vi.hoisted(() => ({
  tracks: [] as Track[],
  useTracks: vi.fn(),
  listTrackKinds: vi.fn(),
  createTrack: vi.fn(),
  noteTrack: vi.fn(),
  navigate: vi.fn(),
}));

vi.mock("../../lib/tracks", async (original) => {
  const real = await original<typeof import("../../lib/tracks")>();
  return {
    ...real,
    useTracks: () => {
      mocks.useTracks();
      return { tracks: mocks.tracks, supported: true };
    },
    listTrackKinds: () => mocks.listTrackKinds(),
    createTrack: (body: TrackCreate) => mocks.createTrack(body),
    noteTrack: (track: Track) => mocks.noteTrack(track),
  };
});

vi.mock("../../lib/router", async (original) => {
  const real = await original<typeof import("../../lib/router")>();
  return {
    ...real,
    navigate: (...args: [Route | string, boolean?]) => mocks.navigate(...args),
  };
});

const { NewSpace, NewSpaceView } = await import("./NewSpace");

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const makeTrack = (slug: string, title: string): Track => ({
  slug,
  kind: "space",
  title,
  icon: "layers",
  description: "",
  default_categories: [],
  template: "space",
  builtin: false,
  archived: false,
  revision: 1,
  config: {
    schema: 1,
    template: "space",
    context: "track",
    default_categories: [],
    tag_focus: [],
    rhythms: [],
    attention: { push_cap: "P0", deep_default: "quick", enter_mode: "none" },
    supervision: "none",
    usage: [],
    assembly: "track",
    capture_target: true,
    channels: [],
    sidebar: { group: "spaces" },
  },
});

const makeKind = (slug: string, title: string, disabled?: string): TrackKind => ({
  slug,
  title,
  builtin: false,
  revision: 1,
  disabled,
  config: makeTrack(slug, title).config,
});

const expectNoDefaultSingletons = () => {
  expect(mocks.useTracks).not.toHaveBeenCalled();
  expect(mocks.listTrackKinds).not.toHaveBeenCalled();
  expect(mocks.createTrack).not.toHaveBeenCalled();
  expect(mocks.noteTrack).not.toHaveBeenCalled();
  expect(mocks.navigate).not.toHaveBeenCalled();
};

const setInput = async (input: HTMLInputElement, value: string) => {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
};

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
  mocks.tracks = [];
  mocks.useTracks.mockReset();
  mocks.listTrackKinds.mockReset();
  mocks.createTrack.mockReset();
  mocks.noteTrack.mockReset();
  mocks.navigate.mockReset();
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

describe("Pure NewSpaceView", () => {
  it("renders all form controls, taken-slug warning, error alert, and callbacks without touching singletons", async () => {
    const onSubmit = vi.fn();
    const onCancel = vi.fn();
    const onToggleMore = vi.fn();
    const onToggleCategory = vi.fn();
    const onIconChange = vi.fn();

    await act(async () => root.render(
      <NewSpaceView
        title="Family"
        description="Shared notes"
        icon="home"
        categories={["finance"]}
        kinds={[makeKind("space", "Space"), makeKind("project", "Project")]}
        kind="project"
        shownSlug="family"
        slugOk={false}
        slugTaken
        more
        busy={false}
        ready={false}
        error="Create failed"
        onToggleSidebar={() => {}}
        onTitleChange={() => {}}
        onDescriptionChange={() => {}}
        onIconChange={onIconChange}
        onToggleCategory={onToggleCategory}
        onToggleMore={onToggleMore}
        onKindChange={() => {}}
        onSlugChange={() => {}}
        onSubmit={onSubmit}
        onCancel={onCancel}
      />,
    ));

    expect(host.querySelector('[role="alert"]')?.textContent).toBe("Create failed");
    expect(host.textContent).toContain(t("spaces.field_slug_taken"));
    expect(findButton(host, t("spaces.create"))?.disabled).toBe(true);

    await act(async () => { findButton(host, t("spaces.cancel"))!.click(); });
    expect(onCancel).toHaveBeenCalledTimes(1);
    await act(async () => { findButton(host, t("spaces.more_options"))!.click(); });
    expect(onToggleMore).toHaveBeenCalledTimes(1);
    await act(async () => {
      host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expectNoDefaultSingletons();
  });
});

describe("Controlled NewSpace getter caching, subscription stability, and renderView", () => {
  it("caches fresh-array spaces/kinds getters via sameValue, avoids subscription churn, and preserves draft across direct-to-subscribed switch", async () => {
    const scopeListeners = new Set<() => void>();
    const spacesListeners = new Set<() => void>();
    const kindsListeners = new Set<() => void>();
    let currentSpaces = [{ slug: "family" }];
    let currentKinds = [makeKind("space", "Space"), makeKind("project", "Project")];
    let spaceReads = 0;
    let kindReads = 0;
    const seenSlugs: string[] = [];

    const captureView = (props: NewSpaceViewProps) => {
      seenSlugs.push(props.shownSlug);
      return <NewSpaceView {...props} />;
    };

    await act(async () => root.render(
      <StrictMode>
        <NewSpace
          onToggleSidebar={() => {}}
          deps={{
            scopeKey: "s1",
            spaces: currentSpaces,
            kinds: currentKinds,
            navigate: vi.fn(),
            renderView: captureView,
          }}
        />
      </StrictMode>,
    ));

    // Type title + description and expand More options.
    const inputs = host.querySelectorAll("input");
    await setInput(inputs[0], "Family");
    await setInput(inputs[1], "House plans");
    await act(async () => { findButton(host, t("spaces.more_options"))!.click(); });
    expect(seenSlugs.at(-1)).toBe("family-2");

    // Transition in-place to fresh-array getters + subscriptions on same scopeKey.
    const subscribeScopeKey = vi.fn((l: () => void) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; });
    const subscribeSpaces = vi.fn((l: () => void) => { spacesListeners.add(l); return () => { spacesListeners.delete(l); }; });
    const subscribeKinds = vi.fn((l: () => void) => { kindsListeners.add(l); return () => { kindsListeners.delete(l); }; });
    const renderSub = () => root.render(
      <StrictMode>
        <NewSpace
          onToggleSidebar={() => {}}
          deps={{
            scopeKey: () => "s1",
            subscribeScopeKey,
            spaces: () => {
              spaceReads += 1;
              return currentSpaces.map((s) => ({ ...s }));
            },
            subscribeSpaces,
            kinds: () => {
              kindReads += 1;
              return currentKinds.map((k) => ({ ...k }));
            },
            subscribeKinds,
            navigate: vi.fn(),
            renderView: captureView,
          }}
        />
      </StrictMode>,
    );
    await act(async () => renderSub());

    // Draft and expanded section stay intact across direct -> subscribed switch.
    const inputsAfter = host.querySelectorAll("input");
    expect(inputsAfter[0].value).toBe("Family");
    expect(inputsAfter[1].value).toBe("House plans");
    expect(inputsAfter[2].value).toBe("family-2");
    expect(spaceReads).toBeLessThan(20);
    expect(kindReads).toBeLessThan(20);

    // Inline arrow getters must not churn subscriptions.
    const scopeSubCount = subscribeScopeKey.mock.calls.length;
    const spaceSubCount = subscribeSpaces.mock.calls.length;
    const kindSubCount = subscribeKinds.mock.calls.length;
    await act(async () => renderSub());
    await act(async () => renderSub());
    expect(subscribeScopeKey).toHaveBeenCalledTimes(scopeSubCount);
    expect(subscribeSpaces).toHaveBeenCalledTimes(spaceSubCount);
    expect(subscribeKinds).toHaveBeenCalledTimes(kindSubCount);

    // Adding "family-2" to taken spaces updates suggested slug to "family-3".
    currentSpaces = [{ slug: "family" }, { slug: "family-2" }];
    await act(async () => { spacesListeners.forEach((l) => l()); });
    expect(host.querySelectorAll("input")[2].value).toBe("family-3");
    expectNoDefaultSingletons();
  });
});

describe("Controlled NewSpace double-submit lock, fencing, render purity, try/finally recovery, and default fallback", () => {
  it("guards double-submit with a ref lock, preserves draft and role=alert on sync throw & async reject, and recovers via try/finally", async () => {
    const createCalls: Array<{ body: TrackCreate; signal: AbortSignal }> = [];
    const navCalls: Array<{ route: Route; replace?: boolean }> = [];
    const notify = vi.fn(() => { throw new Error("toast crashed"); });
    const onError = vi.fn(() => { throw new Error("observer crashed"); });
    const formatError = vi.fn(() => { throw new Error("format crashed"); });
    let gate: ReturnType<typeof deferred<Track>> | null = null;
    let mode: "deferred" | "syncThrow" | "ok" = "deferred";

    const deps: NewSpaceDeps = {
      scopeKey: "s1",
      listTrackKinds: () => Promise.resolve([makeKind("space", "Space", "schema"), makeKind("orchestration", "Main"), makeKind("project", "Project")]),
      listSpaces: () => Promise.resolve([{ slug: "family" }]),
      createSpace: (body, signal) => {
        createCalls.push({ body, signal });
        if (mode === "syncThrow") throw new Error("sync create fail");
        if (mode === "deferred") {
          gate = deferred<Track>();
          return gate.promise;
        }
        return Promise.resolve(makeTrack(body.slug, body.title));
      },
      navigate: (route, replace) => { navCalls.push({ route, replace }); },
      formatError,
      notify,
      onError,
    };

    await act(async () => root.render(<NewSpace onToggleSidebar={() => {}} deps={deps} />));
    await flush();

    const inputs = host.querySelectorAll("input");
    await setInput(inputs[0], "Family");
    await setInput(inputs[1], "Shared budget");

    // Fire two rapid submit events back-to-back before React even flushes disabled button state.
    const form = host.querySelector("form")!;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(createCalls).toHaveLength(1);
    expect(createCalls[0].body).toMatchObject({ slug: "family-2", kind: "project", title: "Family", description: "Shared budget" });
    expect(findButton(host, t("spaces.create"))?.getAttribute("aria-busy")).toBe("true");

    // Async rejection preserves draft, displays role="alert", and releases ref lock & busy state via try/finally.
    await act(async () => {
      gate!.reject(new Error("server unavailable"));
      await Promise.resolve();
    });
    expect(host.querySelector('[role="alert"]')?.textContent).toBe(t("spaces.create_failed", { error: "server unavailable" }));
    expect(host.querySelectorAll("input")[0].value).toBe("Family");
    expect(host.querySelectorAll("input")[1].value).toBe("Shared budget");
    expect(findButton(host, t("spaces.create"))?.getAttribute("aria-busy")).toBeNull();
    expect(navCalls).toHaveLength(0);

    // Sync throw preserves draft, updates alert, and releases ref lock immediately.
    mode = "syncThrow";
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(createCalls).toHaveLength(2);
    expect(host.querySelector('[role="alert"]')?.textContent).toBe(t("spaces.create_failed", { error: "sync create fail" }));
    expect(host.querySelectorAll("input")[0].value).toBe("Family");

    // Retry succeeds and navigates with replace=true.
    mode = "ok";
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    await flush();
    expect(createCalls).toHaveLength(3);
    expect(navCalls).toEqual([{ route: { name: "track", slug: "family-2", fresh: true }, replace: true }]);

    // Cancel invokes controlled navigate({ name: "persona" }).
    await act(async () => { findButton(host, t("spaces.cancel"))!.click(); });
    expect(navCalls.at(-1)).toEqual({ route: { name: "persona" }, replace: undefined });
    expectNoDefaultSingletons();
  });

  it("aborts listTrackKinds, listSpaces, and createSpace and resets drafts/errors across scope switch, synchronous ABA burst, and unmount", async () => {
    const scopeListeners = new Set<() => void>();
    let currentScope = "s1";
    const navCalls: Route[] = [];
    const onError = vi.fn();

    const kindCalls: Array<{ signal: AbortSignal; gate: ReturnType<typeof deferred<TrackKind[]>> }> = [];
    const spaceCalls: Array<{ signal: AbortSignal; gate: ReturnType<typeof deferred<Array<{ slug: string }>>> }> = [];
    const createCalls: Array<{ signal: AbortSignal; gate: ReturnType<typeof deferred<Track>> }> = [];

    const makeDeps = (): NewSpaceDeps => ({
      scopeKey: () => currentScope,
      subscribeScopeKey: (l) => { scopeListeners.add(l); return () => { scopeListeners.delete(l); }; },
      listTrackKinds: (signal) => {
        const gate = deferred<TrackKind[]>();
        kindCalls.push({ signal, gate });
        return gate.promise;
      },
      listSpaces: (signal) => {
        const gate = deferred<Array<{ slug: string }>>();
        spaceCalls.push({ signal, gate });
        return gate.promise;
      },
      createSpace: (_body, signal) => {
        const gate = deferred<Track>();
        createCalls.push({ signal, gate });
        return gate.promise;
      },
      navigate: (route) => { navCalls.push(route); },
      onError,
    });

    await act(async () => root.render(<NewSpace onToggleSidebar={() => {}} deps={makeDeps()} />));
    expect(kindCalls).toHaveLength(1);
    expect(spaceCalls).toHaveLength(1);

    // 1) Type draft, start createSpace, then switch scope s1 -> s2.
    await setInput(host.querySelectorAll("input")[0], "Travel");
    await act(async () => { findButton(host, t("spaces.create"))!.click(); });
    expect(createCalls).toHaveLength(1);

    currentScope = "s2";
    await act(async () => { scopeListeners.forEach((l) => l()); });
    expect(kindCalls[0].signal.aborted).toBe(true);
    expect(spaceCalls[0].signal.aborted).toBe(true);
    expect(createCalls[0].signal.aborted).toBe(true);
    expect(host.querySelectorAll("input")[0].value).toBe("");

    await act(async () => {
      createCalls[0].gate.resolve(makeTrack("travel", "Travel"));
      await Promise.resolve();
    });
    expect(navCalls).toHaveLength(0);

    // 2) Synchronous ABA scope burst (s2 -> s3 -> s2) aborts active createSpace and clears draft.
    await setInput(host.querySelectorAll("input")[0], "Garden");
    await act(async () => { findButton(host, t("spaces.create"))!.click(); });
    expect(createCalls).toHaveLength(2);

    await act(async () => {
      currentScope = "s3";
      scopeListeners.forEach((l) => l());
      currentScope = "s2";
      scopeListeners.forEach((l) => l());
    });
    expect(createCalls[1].signal.aborted).toBe(true);
    expect(host.querySelectorAll("input")[0].value).toBe("");
    await act(async () => {
      createCalls[1].gate.reject(new Error("stale ABA create"));
      await Promise.resolve();
    });
    expect(onError).not.toHaveBeenCalled();
    expect(host.querySelector('[role="alert"]')).toBeNull();

    // 3) Unmount aborts active createSpace and ignores late rejection.
    await setInput(host.querySelectorAll("input")[0], "Reading");
    await act(async () => { findButton(host, t("spaces.create"))!.click(); });
    expect(createCalls).toHaveLength(3);
    await act(async () => root.render(null));
    expect(createCalls[2].signal.aborted).toBe(true);
    await act(async () => {
      createCalls[2].gate.reject(new Error("unmounted create"));
      await Promise.resolve();
    });
    expect(onError).not.toHaveBeenCalled();
    expectNoDefaultSingletons();
  });

  it("keeps committed draft and in-flight createSpace intact when a concurrent transition render is abandoned", async () => {
    const suspendGate = deferred<void>();
    const createGate = deferred<Track>();
    let capturedSignal: AbortSignal | null = null;
    const navCalls: Route[] = [];
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
          <NewSpace
            onToggleSidebar={() => {}}
            deps={{
              scopeKey: state.scope,
              spaces: [],
              kinds: [makeKind("space", "Space")],
              createSpace: (_body, signal) => {
                capturedSignal = signal;
                return createGate.promise;
              },
              navigate: (route) => { navCalls.push(route); },
            }}
          />
          <SuspendIf block={state.block} />
        </Suspense>
      );
    }

    await act(async () => root.render(<Harness />));
    await setInput(host.querySelectorAll("input")[0], "Fitness");
    await act(async () => { findButton(host, t("spaces.create"))!.click(); });
    expect(capturedSignal!.aborted).toBe(false);

    // Suspended concurrent transition to s2 must NOT abort s1's createSpace or reset committed draft.
    await act(async () => {
      startTransition(() => setHarness({ scope: "s2", block: true }));
    });
    expect(renderedScopes).toContain("s2");
    expect(capturedSignal!.aborted).toBe(false);

    // Abandon transition back to s1; resolving createGate navigates cleanly.
    await act(async () => setHarness({ scope: "s1", block: false }));
    expect(capturedSignal!.aborted).toBe(false);
    await act(async () => {
      createGate.resolve(makeTrack("fitness", "Fitness"));
      await Promise.resolve();
    });
    expect(navCalls).toEqual([{ name: "track", slug: "fitness", fresh: true }]);
    expectNoDefaultSingletons();
  });

  it("preserves 100% backward-compatible default singleton behavior when deps is omitted", async () => {
    mocks.tracks = [makeTrack("money", "Money")];
    mocks.listTrackKinds.mockResolvedValue([makeKind("space", "Space")]);
    const created = makeTrack("money-2", "Money");
    mocks.createTrack.mockResolvedValue(created);

    await act(async () => root.render(<NewSpace onToggleSidebar={() => {}} />));
    await flush();
    expect(mocks.useTracks).toHaveBeenCalled();
    expect(mocks.listTrackKinds).toHaveBeenCalledTimes(1);

    await setInput(host.querySelectorAll("input")[0], "Money");
    await act(async () => { findButton(host, t("spaces.create"))!.click(); });
    await flush();
    expect(mocks.createTrack).toHaveBeenCalledWith({
      slug: "money-2",
      kind: "space",
      title: "Money",
      icon: "layers",
      description: "",
      default_categories: [],
    });
    expect(mocks.noteTrack).toHaveBeenCalledWith(created);
    expect(mocks.navigate).toHaveBeenCalledWith({ name: "track", slug: "money-2", fresh: true }, true);

    await act(async () => { findButton(host, t("spaces.cancel"))!.click(); });
    expect(mocks.navigate).toHaveBeenCalledWith({ name: "persona" });
  });
});
