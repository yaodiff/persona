// @vitest-environment jsdom
// An older page or a fold asked for one conversation that comes back after the chat moved to another (or the
// transcript was rewritten) is dropped, and a second ask while one is on its way sends nothing.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { EngineLeg, Item, ItemsPage } from "../../lib/types";
import EarlierStretches from "./EarlierStretches";
import { useEarlierStretches, type Stitched } from "./useEarlierStretches";
import { useTranscript, type Transcript } from "./useTranscript";

interface Pending {
  url: string;
  resolve: (p: ItemsPage) => void;
  reject: (e: unknown) => void;
}
const calls: Pending[] = [];

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (url: string) => new Promise((resolve, reject) => calls.push({ url, resolve, reject })),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  listenSession: () => () => {},
  send: () => {},
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let shown: Transcript;

function Probe({ sid }: { sid: string }) {
  shown = useTranscript("m", sid);
  return null;
}

let stitched: Stitched;
const engineHistory: EngineLeg[] = [
  { session_id: "A", agent: "claude", until: "2026-11-15T09:10:00Z" },
  { session_id: "B", agent: "codex", until: "2026-11-15T09:20:00Z" },
];
function StitchedProbe() {
  const tr = useTranscript("m", "A");
  stitched = useEarlierStretches("m", "A", engineHistory, tr);
  return stitched.host && <EarlierStretches {...stitched.host} />;
}

const render = (sid: string) => act(() => root.render(<Probe sid={sid} />));
const item = (id: string, seq: number): Item => ({ id, seq, role: "user", kind: "text", text: id });
/** The one pending request whose URL has all of `parts`, settled with `page`. */
async function answer(parts: string[], page: ItemsPage) {
  const i = calls.findIndex((c) => parts.every((p) => c.url.includes(p)));
  expect(i, `a pending request for ${parts.join(" ")}`).toBeGreaterThanOrEqual(0);
  const [c] = calls.splice(i, 1);
  await act(async () => c.resolve(page));
}
async function fail(parts: string[]) {
  const i = calls.findIndex((c) => parts.every((p) => c.url.includes(p)));
  expect(i).toBeGreaterThanOrEqual(0);
  const [c] = calls.splice(i, 1);
  await act(async () => c.reject(new Error("boom")));
}
const ids = () => shown.items.map((i) => i.id);

beforeEach(() => {
  calls.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("useTranscript older pages", () => {
  it("drops an older page of the last conversation that lands after the switch", async () => {
    render("A");
    await answer(["/m/A/items?", "turns=2"], { items: [item("a10", 10)], has_more: true, to_seq: 10 });
    expect(shown.hasMore).toBe(true);
    let pending!: Promise<symbol | undefined>;
    act(() => void (pending = shown.loadEarlier()));
    expect(shown.loadingEarlier).toBe(true);

    render("B");
    await answer(["/m/B/items?", "turns=2"], { items: [item("b50", 50)], has_more: false, to_seq: 50 });
    await answer(["/m/A/items?", "before_seq=10"], { items: [item("a1", 1), item("a2", 2)], has_more: true });
    await act(async () => pending);

    expect(ids()).toEqual(["b50"]);
    expect(shown.hasMore).toBe(false);
    expect(shown.loadingEarlier).toBe(false);
    expect(shown.error).toBe("");
  });

  it("does not show the last conversation's older-page failure in the new one", async () => {
    render("A");
    await answer(["/m/A/items?", "turns=2"], { items: [item("a10", 10)], has_more: true, to_seq: 10 });
    let pending!: Promise<symbol | undefined>;
    act(() => void (pending = shown.loadEarlier()));
    render("B");
    await answer(["/m/B/items?", "turns=2"], { items: [item("b50", 50)], has_more: true, to_seq: 50 });
    await fail(["/m/A/items?", "before_seq=10"]);
    await act(async () => pending);

    expect(shown.error).toBe("");
    expect(ids()).toEqual(["b50"]);
    // and B can page back on its own
    act(() => void shown.loadEarlier());
    expect(calls.filter((c) => c.url.includes("/m/B/") && c.url.includes("before_seq=50"))).toHaveLength(1);
  });

  it("drops a fold of the last conversation that lands after the switch", async () => {
    render("A");
    const fold: Item = { ...item("f", 3), kind: "fold", fold: { through_seq: 8 } } as Item;
    await answer(["/m/A/items?", "turns=2"], { items: [fold, item("a10", 10)], to_seq: 10 });
    let pending!: Promise<unknown>;
    act(() => void (pending = shown.expandFold(fold)));
    render("B");
    await answer(["/m/B/items?", "turns=2"], { items: [item("b50", 50)], to_seq: 50 });
    await answer(["/m/A/items?", "from_seq=3"], { items: [item("a4", 4)] });
    await act(async () => pending);

    expect(await pending).toBe(false);
    expect(ids()).toEqual(["b50"]);
  });

  it("sends one request when asked twice before a render", async () => {
    render("A");
    await answer(["/m/A/items?", "turns=2"], { items: [item("a10", 10)], has_more: true, to_seq: 10 });
    act(() => {
      void shown.loadEarlier();
      void shown.loadEarlier();
    });
    expect(calls.filter((c) => c.url.includes("before_seq="))).toHaveLength(1);

    await answer(["/m/A/items?", "before_seq=10"], { items: [item("a5", 5)], has_more: true });
    expect(ids()).toEqual(["a5", "a10"]);
    expect(shown.loadingEarlier).toBe(false);
    // the next ask goes out once the first is in
    act(() => void shown.loadEarlier());
    expect(calls.filter((c) => c.url.includes("before_seq=5"))).toHaveLength(1);
  });

  it("joins an A → B → A page until the earlier stretch renders, then asks from its new cursor", async () => {
    act(() => root.render(<StitchedProbe />));
    await answer(["/m/A/items?", "turns=2"], {
      items: [{ ...item("live-a", 9), ts: "2026-11-15T09:25:00Z" }], has_more: true,
    });
    act(() => void stitched.loadEarlier());
    await answer(["/m/A/items?", "before_seq=9"], {
      items: [{ ...item("old-a", 3), ts: "2026-11-15T09:06:00Z" }], has_more: true,
    });
    act(() => void stitched.loadEarlier());
    await answer(["/m/B/items?"], {
      items: [{ ...item("middle-b", 2), ts: "2026-11-15T09:15:00Z" }], has_more: false,
    });
    expect(host.querySelector('[data-engine-stretch="A"]')?.textContent).toContain("old-a");

    let joined!: Promise<symbol | undefined>;
    let result: symbol | undefined;
    await act(async () => {
      const first = stitched.loadEarlier();
      const pending = calls.shift()!;
      expect(pending.url).toContain("before_seq=3");
      pending.resolve({ items: [{ ...item("first-a", 1), ts: "2026-11-15T09:01:00Z" }], has_more: true });
      result = await first;
      expect(host.querySelector('[data-engine-stretch="A"]')?.textContent).not.toContain("first-a");
      joined = stitched.loadEarlier();
      expect(calls).toHaveLength(0);
    });
    expect(await joined).toBe(result);
    expect(host.querySelector('[data-engine-stretch="A"]')?.textContent).toContain("first-a");
    act(() => void stitched.loadEarlier());
    await answer(["/m/A/items?", "before_seq=1"], { items: [], has_more: false });
    expect(stitched.hasMore).toBe(false);
  });

  it("reports a fold read cut off by the page cap as incomplete", async () => {
    render("A");
    const fold: Item = { ...item("f", 3), kind: "fold", fold: { through_seq: 100 } } as Item;
    await answer(["/m/A/items?", "turns=2"], { items: [fold], to_seq: 3 });
    let pending!: Promise<unknown>;
    act(() => void (pending = shown.expandFold(fold)));
    for (let n = 0; n < 20; n++) await answer(["from_seq=", "through_seq=100"], { items: [item(`a${n + 4}`, n + 4)], more: true });
    expect(await pending).toBe(false);
  });

  it("reports a fold read that reaches the end as complete", async () => {
    render("A");
    const fold: Item = { ...item("f", 3), kind: "fold", fold: { through_seq: 4 } } as Item;
    await answer(["/m/A/items?", "turns=2"], { items: [fold], to_seq: 3 });
    let pending!: Promise<unknown>;
    act(() => void (pending = shown.expandFold(fold)));
    await answer(["from_seq=3", "through_seq=4"], { items: [item("a4", 4)], more: false });
    expect(await pending).toBe(true);
  });

  it("reports an empty fold range as incomplete instead of spinning forever", async () => {
    render("A");
    const fold: Item = { ...item("f", 3), kind: "fold", fold: { through_seq: 4 } } as Item;
    await answer(["/m/A/items?", "turns=2"], { items: [fold], to_seq: 3 });
    let pending!: Promise<unknown>;
    act(() => void (pending = shown.expandFold(fold)));
    await answer(["from_seq=3", "through_seq=4"], { items: [], more: false });
    expect(await pending).toBe(false);
    expect(ids()).toEqual(["f"]);
  });
});
