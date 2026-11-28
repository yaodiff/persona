// @vitest-environment jsdom
// The app-root host (#10977): without the hub's `text_lookup` feature it loads nothing and starts nothing.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ features: [] as string[], listeners: new Set<() => void>(), started: 0, stopped: 0 }));
vi.mock("../lib/store", async () => {
  const { useSyncExternalStore } = await import("react");
  const subscribe = (l: () => void) => {
    hub.listeners.add(l);
    return () => hub.listeners.delete(l);
  };
  return {
    getState: () => ({ features: hub.features }),
    onHubEvent: () => () => {},
    useStore: <T,>(select: (s: { features: string[] }) => T) => useSyncExternalStore(subscribe, () => select({ features: hub.features })),
  };
});
vi.mock("./listen", () => ({
  startTextLookup: () => {
    hub.started++;
    return () => void hub.stopped++;
  },
}));

import { TextLookupHost } from "./host";

let root: Root;
beforeEach(() => {
  hub.features = [];
  hub.started = 0;
  hub.stopped = 0;
  root = createRoot(document.body.appendChild(document.createElement("div")));
});
afterEach(() => {
  act(() => root.unmount());
  document.body.replaceChildren();
});

function setFeatures(next: string[]) {
  hub.features = next;
  hub.listeners.forEach((l) => l());
}

describe("the text-lookup host", () => {
  it("starts nothing without the feature, then starts and stops with it", async () => {
    await act(async () => root.render(<TextLookupHost />));
    await act(async () => {});
    expect(hub.started).toBe(0);
    expect(document.querySelector("[data-testid=text-lookup]")).toBeNull();
    await act(async () => setFeatures(["text_lookup"]));
    await vi.waitFor(() => expect(hub.started).toBe(1));
    await act(async () => setFeatures([]));
    expect(hub.stopped).toBe(1);
  });
});
