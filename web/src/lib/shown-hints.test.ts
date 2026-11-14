// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { onShownHint } from "./shown-hints";

const hub = vi.hoisted(() => ({ handlers: new Set<{ event: string; fn: () => void }>() }));
vi.mock("./store", () => ({
  onHubEvent: (event: string, fn: () => void) => {
    const entry = { event, fn };
    hub.handlers.add(entry);
    return () => { hub.handlers.delete(entry); };
  },
}));

const hint = (event = "sessions_list") => { for (const entry of [...hub.handlers]) if (entry.event === event) entry.fn(); };
function setShown(shown: boolean) {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => shown ? "visible" : "hidden" });
  document.dispatchEvent(new Event("visibilitychange"));
}

afterEach(() => {
  setShown(true);
  hub.handlers.clear();
});

describe("onShownHint", () => {
  it("follows every hint while the page is shown", () => {
    const fn = vi.fn();
    onShownHint("sessions_list", fn);
    hint(); hint(); hint("agents");
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("holds the hints of a hidden page and acts once when it is shown again", () => {
    const fn = vi.fn();
    onShownHint("sessions_list", fn);
    setShown(false);
    for (let i = 0; i < 10; i++) hint();
    expect(fn).not.toHaveBeenCalled();
    setShown(true);
    expect(fn).toHaveBeenCalledTimes(1);
    // nothing missed: showing it again reads nothing
    setShown(false);
    setShown(true);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("stops listening for hints and for the page once unsubscribed", () => {
    const fn = vi.fn();
    const off = onShownHint("sessions_list", fn);
    setShown(false);
    hint();
    off();
    setShown(true);
    hint();
    expect(fn).not.toHaveBeenCalled();
    expect(hub.handlers.size).toBe(0);
  });
});
