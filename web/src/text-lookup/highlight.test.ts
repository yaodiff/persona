// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { attachSavedHighlights, highlightSupported } from "./highlight";

afterEach(() => {
  document.body.replaceChildren();
  document.head.querySelector("[data-text-lookup]")?.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function stub() {
  let frame: FrameRequestCallback | null = null;
  window.requestAnimationFrame = (cb: FrameRequestCallback) => { frame = cb; return 1; };
  window.cancelAnimationFrame = () => { frame = null; };
  const painted = new Set<AbstractRange>();
  class Highlight {
    add(range: AbstractRange) { painted.add(range); }
    delete(range: AbstractRange) { painted.delete(range); }
  }
  const highlights = { set: vi.fn(), delete: vi.fn() };
  vi.stubGlobal("Highlight", Highlight);
  vi.stubGlobal("CSS", { highlights });
  const flush = async () => {
    await vi.advanceTimersByTimeAsync(200);
    for (let i = 0; i < 20 && frame; i++) {
      const run = frame;
      frame = null;
      run(0);
    }
  };
  return { painted, flush };
}

describe("saved-word underline", () => {
  it("does nothing where the highlight API is missing", () => {
    expect(highlightSupported()).toBe(false);
    const attached = attachSavedHighlights();
    attached.update(["hello"]);
    expect(document.head.querySelector("[data-text-lookup]")).toBeNull();
    attached.stop();
  });

  it("paints a saved word and drops it when underlining turns off", async () => {
    vi.useFakeTimers();
    let frame: FrameRequestCallback | null = null;
    window.requestAnimationFrame = (cb: FrameRequestCallback) => { frame = cb; return 1; };
    window.cancelAnimationFrame = () => { frame = null; };
    const painted = new Set<AbstractRange>();
    class Highlight {
      add(range: AbstractRange) { painted.add(range); }
      delete(range: AbstractRange) { painted.delete(range); }
    }
    const highlights = { set: vi.fn(), delete: vi.fn() };
    (window as unknown as { Highlight: typeof Highlight }).Highlight = Highlight;
    (window as unknown as { CSS: { highlights: typeof highlights } }).CSS = { highlights };
    const p = document.createElement("p");
    p.textContent = "Say hello there.";
    document.body.append(p);
    const attached = attachSavedHighlights();
    attached.update(["hello"]);
    await vi.advanceTimersByTimeAsync(200);
    (frame as FrameRequestCallback | null)?.(0);
    expect(highlights.set).toHaveBeenCalledWith("persona-saved-words", expect.any(Highlight));
    expect([...painted].map((range) => range.toString())).toEqual(["hello"]);
    expect(document.head.querySelector("[data-text-lookup]")?.textContent).toContain("::highlight(persona-saved-words)");
    attached.update([]);
    expect(painted.size).toBe(0);
    attached.stop();
    expect(highlights.delete).toHaveBeenCalledWith("persona-saved-words");
    expect(document.head.querySelector("[data-text-lookup]")).toBeNull();
  });

  it("paints a text node that arrives on its own, without touching the DOM", async () => {
    vi.useFakeTimers();
    const { painted, flush } = stub();
    const p = document.createElement("p");
    p.textContent = "Nothing yet.";
    document.body.append(p);
    const attached = attachSavedHighlights();
    attached.update(["deploy"]);
    await flush();
    expect(painted.size).toBe(0);
    // a streamed reply appends a bare text node: the observer hands that node itself to the walk
    const text = document.createTextNode(" Then deploy it.");
    p.append(text);
    await Promise.resolve();
    await flush();
    expect([...painted].map((r) => [r.startContainer, r.toString()])).toEqual([[text, "deploy"]]);
    // no wrapper elements: the underline is painted over the existing nodes
    expect(p.innerHTML).toBe("Nothing yet. Then deploy it.");
    expect(p.childNodes).toHaveLength(2);
    expect(p.textContent).toBe("Nothing yet. Then deploy it.");
    // an edit to that node's text repaints it
    text.data = " No more.";
    await Promise.resolve();
    await flush();
    expect(painted.size).toBe(0);
    attached.stop();
  });

  it("walks a large page a slice at a time", async () => {
    vi.useFakeTimers();
    let frames = 0;
    const { painted } = stub();
    let frame: FrameRequestCallback | null = null;
    window.requestAnimationFrame = (cb: FrameRequestCallback) => { frames++; frame = cb; return 1; };
    for (let i = 0; i < 200; i++) {
      const p = document.createElement("p");
      p.textContent = `Line ${i}: deploy`;
      document.body.append(p);
    }
    const attached = attachSavedHighlights();
    attached.update(["deploy"]);
    await vi.advanceTimersByTimeAsync(200);
    frame!(0);
    // one frame paints at most its budget of text nodes, then yields
    expect(painted.size).toBeGreaterThan(0);
    expect(painted.size).toBeLessThan(200);
    while (frame && frames < 50) {
      const run: FrameRequestCallback = frame;
      frame = null;
      run(0);
    }
    expect(painted.size).toBe(200);
    expect(frames).toBeGreaterThan(1);
    attached.stop();
  });
});
