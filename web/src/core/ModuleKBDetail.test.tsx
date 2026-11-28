// @vitest-environment jsdom
// ModuleKBDetail slot switch safety (#15214): switching kinds or modules clears stale parts synchronously,
// ignores stale async resolutions on ABA switches, and cleanly renders the readOnly fallback when off.
import { act, createRef, type ComponentType } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getState, reset, subscribe } from "../lib/store";
import {
  connectModuleHub,
  registerModule,
  resetModulesForTest,
  type ModuleCode,
  type ModuleDescriptor,
  type ModuleKBDetailProps,
} from "./module-host";
import { forgetModuleSwitches, setModuleSwitchedOff } from "./module-switch";
import { ModuleKBDetail, resetKBDetailPartsForTest } from "./ModuleKBDetail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (err: unknown) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const auth = { token: "t", tokenOwner: "owner", headers: {} };
const scroller = createRef<HTMLDivElement>();

const makeDescriptor = (id: string, kind: string, load: () => Promise<ModuleCode>): ModuleDescriptor => ({
  id,
  feature: `module.${id}`,
  title: () => id,
  description: () => id,
  kbKinds: [kind],
  load,
});

const makePart = (tag: string, seenKinds: string[]): ComponentType<ModuleKBDetailProps> => {
  return function Part(props: ModuleKBDetailProps) {
    seenKinds.push(`${tag}:${props.kind}`);
    return <>{props.children({ lead: <div data-module-part={tag}>{`${tag}:${props.kind}`}</div> })}</>;
  };
};

const ReadOnlyFallback: ComponentType<ModuleKBDetailProps> = ({ kind, children }) => (
  <>{children({ lead: <div data-readonly-fallback={kind}>{`readonly:${kind}`}</div> })}</>
);

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
};

beforeEach(() => {
  localStorage.clear();
  forgetModuleSwitches();
  reset();
  connectModuleHub(getState, subscribe);
  resetModulesForTest();
  resetKBDetailPartsForTest();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetModulesForTest();
  resetKBDetailPartsForTest();
  localStorage.clear();
  forgetModuleSwitches();
  vi.restoreAllMocks();
});

describe("ModuleKBDetail", () => {
  const renderKind = (kind: string, readOnly?: ComponentType<ModuleKBDetailProps>) => {
    act(() => {
      root.render(
        <ModuleKBDetail
          kind={kind}
          id="entry-1"
          entry={{ kind, id: "entry-1", name: "Entry", relevance: 0 }}
          auth={auth}
          current={() => true}
          manager="assistant"
          reload={async () => true}
          onSaved={() => {}}
          onOpen={() => {}}
          scroller={scroller}
          readOnly={readOnly}
        >
          {(parts) => <div data-page-kind={kind}>{parts.lead ?? <span data-no-parts="1">no-parts</span>}</div>}
        </ModuleKBDetail>,
      );
    });
  };

  it("clears stale Parts synchronously when switching to another module kind and blocks ABA stale loads", async () => {
    const loadA = deferred<ModuleCode>();
    const loadB = deferred<ModuleCode>();
    const seen: string[] = [];
    const PartA = makePart("alpha", seen);
    const PartB = makePart("beta", seen);

    registerModule(makeDescriptor("alpha", "alpha-kind", () => loadA.promise));
    registerModule(makeDescriptor("beta", "beta-kind", () => loadB.promise));

    renderKind("alpha-kind");
    expect(host.querySelector("[data-no-parts]")).not.toBeNull();

    loadA.resolve({ KBDetail: PartA });
    await flush();
    expect(host.querySelector('[data-module-part="alpha"]')?.textContent).toBe("alpha:alpha-kind");

    // Switch from loaded alpha-kind to still-loading beta-kind: alpha must NEVER render with beta-kind props
    renderKind("beta-kind");
    expect(host.querySelector('[data-module-part="alpha"]')).toBeNull();
    expect(host.querySelector("[data-no-parts]")).not.toBeNull();
    expect(seen).not.toContain("alpha:beta-kind");

    // ABA: switch back to alpha-kind before beta-kind finishes loading, then resolve beta-kind late
    renderKind("alpha-kind");
    expect(host.querySelector('[data-module-part="alpha"]')?.textContent).toBe("alpha:alpha-kind");

    loadB.resolve({ KBDetail: PartB });
    await flush();
    expect(host.querySelector('[data-module-part="beta"]')).toBeNull();
    expect(host.querySelector('[data-module-part="alpha"]')?.textContent).toBe("alpha:alpha-kind");
    expect(seen).not.toContain("beta:alpha-kind");

    // Switching back to beta-kind now uses the cached PartB cleanly
    renderKind("beta-kind");
    expect(host.querySelector('[data-module-part="beta"]')?.textContent).toBe("beta:beta-kind");
  });

  it("falls back cleanly to readOnly or empty parts when a module turns off mid-load and recovers when turned on", async () => {
    const loadRec = deferred<ModuleCode>();
    const seen: string[] = [];
    const PartRec = makePart("rec", seen);
    registerModule(makeDescriptor("rec-mod", "recordings", () => loadRec.promise));

    renderKind("recordings", ReadOnlyFallback);
    expect(host.querySelector("[data-no-parts]")).not.toBeNull();

    // Turn module off while its chunk is still in flight
    act(() => setModuleSwitchedOff("rec-mod", true));
    expect(host.querySelector('[data-readonly-fallback="recordings"]')?.textContent).toBe("readonly:recordings");

    // Late resolution while off must not replace the read-only fallback
    loadRec.resolve({ KBDetail: PartRec });
    await flush();
    expect(host.querySelector('[data-readonly-fallback="recordings"]')?.textContent).toBe("readonly:recordings");
    expect(host.querySelector('[data-module-part="rec"]')).toBeNull();

    // Without a readOnly component, turning off renders NO_PARTS cleanly
    renderKind("recordings");
    expect(host.querySelector("[data-no-parts]")).not.toBeNull();

    // Turning back on renders the module part
    act(() => setModuleSwitchedOff("rec-mod", false));
    await flush();
    expect(host.querySelector('[data-module-part="rec"]')?.textContent).toBe("rec:recordings");
  });

  it("handles a failed module load without leaving stale parts behind", async () => {
    const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const loadGood = deferred<ModuleCode>();
    const loadBad = deferred<ModuleCode>();
    const seen: string[] = [];
    registerModule(makeDescriptor("good", "good-kind", () => loadGood.promise));
    registerModule(makeDescriptor("bad", "bad-kind", () => loadBad.promise));

    renderKind("good-kind");
    loadGood.resolve({ KBDetail: makePart("good", seen) });
    await flush();
    expect(host.querySelector('[data-module-part="good"]')).not.toBeNull();

    renderKind("bad-kind");
    loadBad.reject(new Error("chunk failed"));
    await flush();
    expect(host.querySelector('[data-module-part="good"]')).toBeNull();
    expect(host.querySelector("[data-no-parts]")).not.toBeNull();
    expect(seen).not.toContain("good:bad-kind");
    expect(errSpy).toHaveBeenCalled();
  });
});
