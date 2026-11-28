// @vitest-environment jsdom
// usePanes: `busy` must clear when every machine goes offline mid-read, an older panes reply must not replace a newer one,
// and a poll that brings back what is shown must not re-render (#6291).
import { act, useMemo } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PanesResponse } from "./terminals";
import type { Machine } from "./types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface Deferred {
  path: string;
  resolve: (v: unknown) => void;
  reject: (e: unknown) => void;
}
const calls: Deferred[] = [];

vi.mock("./api", () => ({
  api: (path: string) =>
    new Promise((resolve, reject) => {
      calls.push({ path, resolve, reject });
    }),
  enc: encodeURIComponent,
  errorText: (e: unknown) => String(e),
}));
vi.mock("./store", () => ({ onHubEvent: () => () => {} }));
vi.mock("./shown-hints", () => ({ onShownHint: () => () => {} }));

const { REFRESH_MS, usePanes } = await import("./terminal-panes");

type State = ReturnType<typeof usePanes>;
let state: State;
// what TerminalPage (trees) and the sidebar (terminals) build from the listing: once per listing they get
let builds = 0;
function Probe({ machines }: { machines: Machine[] }) {
  state = usePanes(machines);
  useMemo(() => builds++, [state.listing]);
  return null;
}

const machine = (id: string, online: boolean) => ({ id, online }) as Machine;
const panes = (tag: string) => ({ tag }) as unknown as PanesResponse;

describe("usePanes", () => {
  let root: ReturnType<typeof createRoot>;
  beforeEach(() => {
    calls.length = 0;
    root = createRoot(document.createElement("div"));
  });
  afterEach(() => act(() => root.unmount()));

  it("clears busy when the last online machine goes offline before its read returns", async () => {
    act(() => root.render(<Probe machines={[machine("m1", true)]} />));
    expect(state.busy).toBe(true);
    expect(calls).toHaveLength(1);
    act(() => root.render(<Probe machines={[machine("m1", false)]} />));
    await act(async () => calls[0].resolve(panes("late")));
    expect(state.busy).toBe(false);
  });

  it("keeps the newer reply when an older read for the same machine lands after it", async () => {
    act(() => root.render(<Probe machines={[machine("m1", true)]} />));
    let refreshed!: Promise<void>;
    act(() => {
      refreshed = state.refresh();
    });
    expect(calls).toHaveLength(2);
    await act(async () => {
      calls[1].resolve(panes("new"));
      await refreshed;
    });
    expect(state.listing.m1?.data).toEqual(panes("new"));
    expect(state.busy).toBe(false);
    await act(async () => calls[0].resolve(panes("old")));
    expect(state.listing.m1?.data).toEqual(panes("new"));
    // an older failed read does not put an error over the newer list either
    act(() => void state.reload(["m1"]));
    act(() => void state.reload(["m1"]));
    await act(async () => calls[3].resolve(panes("newest")));
    await act(async () => calls[2].reject(new Error("gone")));
    expect(state.listing.m1).toEqual({ data: panes("newest") });
  });

  it("shows an older reply that lands first, then the newer one", async () => {
    act(() => root.render(<Probe machines={[machine("m1", true)]} />));
    act(() => void state.reload(["m1"]));
    await act(async () => calls[0].resolve(panes("a")));
    expect(state.listing.m1?.data).toEqual(panes("a"));
    expect(state.busy).toBe(false);
    await act(async () => calls[1].resolve(panes("b")));
    expect(state.listing.m1?.data).toEqual(panes("b"));
  });
});

describe("usePanes polling", () => {
  let root: ReturnType<typeof createRoot>;
  beforeEach(() => {
    vi.useFakeTimers();
    calls.length = 0;
    root = createRoot(document.createElement("div"));
  });
  afterEach(() => {
    act(() => root.unmount());
    vi.useRealTimers();
  });

  // Answers every read still out, machine by machine as the hub would; `fail` rejects them instead.
  async function answer(reply: (mid: string) => unknown, fail = false) {
    for (const c of calls.splice(0)) {
      const mid = decodeURIComponent(c.path.split("/")[2]);
      await act(async () => (fail ? c.reject(reply(mid)) : c.resolve(reply(mid))));
    }
  }
  const tick = () => act(async () => void vi.advanceTimersByTime(REFRESH_MS));
  // a new object each time, as each reply is parsed afresh
  const layout = (mid: string, title = "zsh") => ({ hosts: [{ kind: "herdr", panes: [{ pane: `${mid}-1`, title }] }] }) as unknown as PanesResponse;
  const three = [machine("m1", true), machine("m2", true), machine("m3", true)];

  it("keeps the listing, and what is built from it, when the polls bring back the same layouts", async () => {
    act(() => root.render(<Probe machines={three} />));
    await answer((mid) => layout(mid));
    const first = state.listing;
    expect(Object.keys(first)).toEqual(["m1", "m2", "m3"]);
    builds = 0;
    for (let i = 0; i < 3; i++) {
      await tick();
      await answer((mid) => layout(mid));
    }
    // was 9: the trees built again for each machine's reply on each of the 3 polls
    expect(builds).toBe(0);
    expect(state.listing).toBe(first);
  });

  it("shows a changed layout, and only the machine that changed gets a new entry", async () => {
    act(() => root.render(<Probe machines={three} />));
    await answer((mid) => layout(mid));
    const first = state.listing;
    await tick();
    await answer((mid) => layout(mid, mid === "m2" ? "vim" : "zsh"));
    expect(state.listing).not.toBe(first);
    expect(state.listing.m2?.data).toEqual(layout("m2", "vim"));
    expect(state.listing.m1).toBe(first.m1);
    expect(state.listing.m3).toBe(first.m3);
  });

  it("keeps a repeated failure as it is, and clears the error when the same layout comes back", async () => {
    act(() => root.render(<Probe machines={[machine("m1", true)]} />));
    await answer((mid) => layout(mid));
    await tick();
    await answer(() => new Error("offline"), true);
    expect(state.listing.m1).toEqual({ data: layout("m1"), error: "Error: offline" });
    const failed = state.listing;
    builds = 0;
    await tick();
    await answer(() => new Error("offline"), true);
    expect(builds).toBe(0);
    expect(state.listing).toBe(failed);
    await tick();
    await answer(() => new Error("timed out"), true);
    expect(state.listing.m1?.error).toBe("Error: timed out");
    await tick();
    await answer((mid) => layout(mid));
    expect(state.listing.m1).toEqual({ data: layout("m1") });
  });
});
