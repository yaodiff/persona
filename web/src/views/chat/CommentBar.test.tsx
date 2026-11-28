// @vitest-environment jsdom
import { act, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { onJumpToComment } from "../../lib/comment-marks";
import { textQuote, parseCommentBlock } from "../../lib/comments";
import { addQuote, getQuotes, removeQuote, setQuotes, withQuoteBlock } from "../../lib/drafts";
import { t } from "../../i18n";
import { GutterNumber, LineBox, useLinePick } from "../session/files/LineComment";
import { ChatActionsContext } from "./chat-actions";
import { CommentBar, placeCommentBar } from "./CommentBar";
import { preloadCommentBar } from "./CommentBarOverlay";
import { CommentMarks } from "./CommentMarks";
import { QuoteBars } from "./Quote";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function setInputValue(input: HTMLTextAreaElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function mockRangeRect(range: Range, rect: DOMRect) {
  range.getClientRects = () => [rect] as unknown as DOMRectList;
  range.getBoundingClientRect = () => rect;
}

describe("inline comment floating bar and compact composer chip (#11585)", () => {
  let host: HTMLDivElement;
  let root: Root;
  const draftKey = "m1/s1";

  beforeAll(async () => { await preloadCommentBar(); });

  beforeEach(() => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    setQuotes(draftKey, []);
    Object.defineProperty(window, "innerWidth", { value: 1280, configurable: true });
    Object.defineProperty(window, "innerHeight", { value: 800, configurable: true });
    window.matchMedia = vi.fn().mockImplementation((query: string) => ({
      matches: query === "(any-pointer: fine)",
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }));
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    document.getSelection()?.removeAllRanges();
    setQuotes(draftKey, []);
    vi.restoreAllMocks();
  });

  it("clamps floating bar position inside the viewport and flips above when near the bottom", () => {
    expect(placeCommentBar({ x: 300, top: 100, bottom: 120 }, 320, 44, 1280, 800)).toEqual({ left: 140, top: 128 });
    // near right edge: clamped to vw - w - 8
    expect(placeCommentBar({ x: 1260, top: 100, bottom: 120 }, 320, 44, 1280, 800)).toEqual({ left: 952, top: 128 });
    // near bottom edge: flips above anchor.top
    expect(placeCommentBar({ x: 300, top: 760, bottom: 780 }, 320, 44, 1280, 800)).toEqual({ left: 140, top: 708 });
  });

  it("saves with Enter or ✓, allows empty-note comments, and cancels with Esc leaving no comment", () => {
    const onSave = vi.fn();
    const onCancel = vi.fn();

    act(() => {
      root.render(<CommentBar anchor={{ x: 200, top: 100, bottom: 120 }} autoFocus onSave={onSave} onCancel={onCancel} />);
    });

    const input = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    expect(input).toBeTruthy();
    expect(input.placeholder).toBe(t("chat-comments.placeholder"));
    expect(document.activeElement).toBe(input);

    // Save via Enter with a note
    act(() => {
      setInputValue(input, "Check this edge case");
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(onSave).toHaveBeenCalledWith("Check this edge case");

    // Empty-note comment via ✓ button
    onSave.mockClear();
    act(() => {
      setInputValue(input, "");
      document.querySelector<HTMLButtonElement>("[data-comment-save]")!.click();
    });
    expect(onSave).toHaveBeenCalledWith("");

    // Esc cancels
    act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("docks cleanly at narrow widths (~390px) while showing number and context", () => {
    Object.defineProperty(window, "innerWidth", { value: 390, configurable: true });
    window.matchMedia = vi.fn().mockImplementation((query: string) => ({
      matches: query.includes("639px"),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }));

    act(() => {
      root.render(
        <CommentBar
          anchor={{ x: 300, top: 200, bottom: 220 }}
          number={2}
          context="Retry with exponential backoff"
          onSave={() => {}}
          onCancel={() => {}}
        />,
      );
    });

    const bar = document.querySelector<HTMLElement>("[data-comment-bar]")!;
    expect(bar.dataset.docked).toBe("true");
    const ctx = bar.querySelector("[data-comment-bar-context]")!;
    expect(ctx.textContent).toContain("2");
    expect(ctx.textContent).toContain("Retry with exponential backoff");
  });

  it("keeps the composer to a single compact chip row even with 3+ comments, and popover supports jump and remove", () => {
    const id1 = addQuote(draftKey, { ...textQuote("first passage", undefined), comment: { body: "note one" } }, false);
    const id2 = addQuote(draftKey, { ...textQuote("second passage", undefined), comment: { body: "" } }, false);
    const id3 = addQuote(draftKey, { ...textQuote("third passage", undefined), comment: { body: "note three" } }, false);
    const jumped: string[] = [];
    const offJump = onJumpToComment((id) => {
      jumped.push(id);
      return true;
    });

    const renderBars = () => {
      const quotes = getQuotes(draftKey).list;
      act(() => {
        root.render(
          <QuoteBars
            draftKey={draftKey}
            quotes={quotes}
            onRemove={(id) => {
              removeQuote(draftKey, id);
              renderBars();
            }}
          />,
        );
      });
    };

    try {
      renderBars();
      // Only a single compact chip row — no stacked full comment cards
      expect(host.querySelectorAll("[data-comment-chip-row]")).toHaveLength(1);
      expect(host.querySelectorAll("[data-comment-card]")).toHaveLength(0);
      const chip = host.querySelector<HTMLButtonElement>("[data-comment-chip]")!;
      expect(chip.textContent).toContain(t("chat-comments.count", { count: 3 }));

      // Open popover
      act(() => chip.click());
      const popover = document.querySelector<HTMLElement>("[data-comment-popover]")!;
      expect(popover).toBeTruthy();
      const items = popover.querySelectorAll("[data-comment-item]");
      expect(items).toHaveLength(3);
      expect(items[0].textContent).toContain("1");
      expect(items[0].textContent).toContain("first passage");
      expect(items[0].textContent).toContain("note one");

      // Jump to comment 2
      act(() => popover.querySelector<HTMLButtonElement>('[data-comment-jump="2"]')!.click());
      expect(jumped).toEqual([id2]);
      expect(document.querySelector("[data-comment-popover]")).toBeNull();

      // Reopen popover and remove comment 1
      act(() => chip.click());
      act(() => document.querySelector<HTMLButtonElement>('[data-comment-remove="1"]')!.click());
      expect(getQuotes(draftKey).list.map((q) => q.id)).toEqual([id2, id3]);
      expect(host.querySelector<HTMLButtonElement>("[data-comment-chip]")!.textContent).toContain(t("chat-comments.count", { count: 2 }));
      expect(id1).toBeTruthy();
    } finally {
      offJump();
    }
  });

  it("opens the floating bar on selection, saves without focusing composer, and clicks pin to edit or delete in place", () => {
    const composerFocus = vi.fn();

    function Harness() {
      const scroller = useRef<HTMLDivElement>(null);
      return (
        <ChatActionsContext.Provider value={{ draftKey, title: "Test", canCompose: true, speaker: "Persona", images: () => [] }}>
          <div ref={scroller} style={{ position: "relative", height: 400 }}>
            <div>
              <div data-items="msg-1">
                <p>Alpha beta gamma delta</p>
              </div>
            </div>
            <CommentMarks scroller={scroller} />
          </div>
          <textarea data-testid="fake-composer" onFocus={composerFocus} />
        </ChatActionsContext.Provider>
      );
    }

    // Mock ResizeObserver & scrollTo for jsdom
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );

    act(() => root.render(<Harness />));

    const scroller = host.firstElementChild as HTMLDivElement;
    scroller.getBoundingClientRect = () => new DOMRect(0, 0, 800, 400);
    const textNode = host.querySelector("p")!.firstChild as Text;

    // Select "beta gamma"
    const range = document.createRange();
    range.setStart(textNode, 6);
    range.setEnd(textNode, 16);
    mockRangeRect(range, new DOMRect(60, 40, 80, 18));
    const origCreateRange = document.createRange.bind(document);
    vi.spyOn(document, "createRange").mockImplementation(() => {
      const r = origCreateRange();
      mockRangeRect(r, new DOMRect(60, 40, 80, 18));
      return r;
    });
    const sel = document.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);

    // Trigger selectionchange + rAF
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      cb(0);
      return 1;
    });
    act(() => {
      document.dispatchEvent(new Event("selectionchange"));
    });

    // Floating bar appears at selection
    const bar = document.querySelector<HTMLElement>("[data-comment-bar]")!;
    expect(bar).toBeTruthy();
    const input = bar.querySelector("textarea")!;

    // Pressing C focuses the floating bar input
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "c", bubbles: true }));
    });
    expect(document.activeElement).toBe(input);

    // Type note and press Enter -> saves without focusing composer
    act(() => {
      setInputValue(input, "Clarify this");
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(document.querySelector("[data-comment-bar]")).toBeNull();
    expect(composerFocus).not.toHaveBeenCalled();
    expect(getQuotes(draftKey).list).toHaveLength(1);
    expect(getQuotes(draftKey).list[0].text).toBe("beta gamma");
    expect(getQuotes(draftKey).list[0].comment?.body).toBe("Clarify this");
    expect(getQuotes(draftKey).editing).toBeNull();

    // Numbered pin is rendered in the transcript
    const pin = host.querySelector<HTMLButtonElement>('[data-comment-pin="1"]')!;
    expect(pin).toBeTruthy();

    // Clicking the pin reopens the floating bar with existing note
    act(() => pin.click());
    const editInput = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    expect(editInput.value).toBe("Clarify this");

    // Edit in place and save
    act(() => {
      setInputValue(editInput, "Updated note");
      editInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(getQuotes(draftKey).list[0].comment?.body).toBe("Updated note");

    // Reopen pin and delete in place
    act(() => pin.click());
    act(() => document.querySelector<HTMLButtonElement>("[data-comment-delete]")!.click());
    expect(getQuotes(draftKey).list).toHaveLength(0);
    expect(host.querySelector('[data-comment-pin="1"]')).toBeNull();

    vi.unstubAllGlobals();
  });

  it("reuses the floating bar for code line comments (LineBox)", () => {
    const onAdd = vi.fn();
    const onCancel = vi.fn();

    act(() => {
      root.render(
        <LineBox
          where="src/app.ts:12-14 · unstaged"
          anchor={{ x: 120, top: 200, bottom: 220 }}
          onAdd={onAdd}
          onCancel={onCancel}
        />,
      );
    });

    const bar = document.querySelector<HTMLElement>("[data-comment-bar]")!;
    expect(bar).toBeTruthy();
    expect(bar.querySelector("[data-comment-bar-context]")!.textContent).toContain("src/app.ts:12-14 · unstaged");

    const input = bar.querySelector("textarea")!;
    act(() => {
      setInputValue(input, "Extract helper");
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(onAdd).toHaveBeenCalledWith("Extract helper");
  });

  it("extends the picked line range on shift-click even while LineBox is already open", () => {
    function GutterHarness() {
      const pick = useLinePick(true);
      return (
        <div>
          {[1, 2, 3, 4, 5].map((n) => (
            <GutterNumber key={n} n={n} pick={pick} enabled />
          ))}
          {pick.range && (
            <LineBox
              where={`src/app.ts:${pick.range.from}-${pick.range.to}`}
              anchor={pick.anchor}
              onAdd={() => pick.clear()}
              onCancel={pick.clear}
            />
          )}
        </div>
      );
    }

    act(() => root.render(<GutterHarness />));

    const line2 = host.querySelector<HTMLButtonElement>('[data-gutter="2"]')!;
    const line5 = host.querySelector<HTMLButtonElement>('[data-gutter="5"]')!;

    // Pick line 2 first -> opens LineBox at 2-2
    act(() => {
      line2.dispatchEvent(new MouseEvent("pointerdown", { button: 0, bubbles: true }));
      window.dispatchEvent(new Event("pointerup"));
    });
    expect(document.querySelector("[data-comment-bar-context]")?.textContent).toContain("src/app.ts:2-2");

    // Shift-click line 5 while LineBox is open -> extends range to 2-5 instead of resetting startLine
    act(() => {
      line5.dispatchEvent(new MouseEvent("pointerdown", { button: 0, shiftKey: true, bubbles: true }));
      window.dispatchEvent(new Event("pointerup"));
    });
    expect(document.querySelector("[data-comment-bar-context]")?.textContent).toContain("src/app.ts:2-5");
  });

  it("keeps LineBox anchored at the drag start line when sweeping across multiple gutter rows", () => {
    let latestAnchor: { x: number; top: number; bottom: number } | null = null;
    function DragHarness() {
      const pick = useLinePick(true);
      latestAnchor = pick.anchor;
      return (
        <div>
          {[1, 2, 3, 4, 5].map((n) => (
            <GutterNumber key={n} n={n} pick={pick} enabled />
          ))}
          {pick.range && (
            <LineBox
              where={`src/app.ts:${pick.range.from}-${pick.range.to}`}
              anchor={pick.anchor}
              onAdd={() => pick.clear()}
              onCancel={pick.clear}
            />
          )}
        </div>
      );
    }

    act(() => root.render(<DragHarness />));

    const line2 = host.querySelector<HTMLButtonElement>('[data-gutter="2"]')!;
    const line5 = host.querySelector<HTMLButtonElement>('[data-gutter="5"]')!;
    line2.getBoundingClientRect = () => new DOMRect(10, 120, 32, 20);
    line5.getBoundingClientRect = () => new DOMRect(10, 740, 32, 20);

    // Drag from line 2 down to line 5 -> range is 2-5, anchor spans full picked range (start top 120 to end bottom 760)
    act(() => {
      line2.dispatchEvent(new MouseEvent("pointerdown", { button: 0, bubbles: true }));
      line5.dispatchEvent(new MouseEvent("pointerover", { bubbles: true }));
      window.dispatchEvent(new Event("pointerup"));
    });

    expect(document.querySelector("[data-comment-bar-context]")?.textContent).toContain("src/app.ts:2-5");
    expect(latestAnchor).toEqual({ x: 50, top: 120, bottom: 760 });
  });

  it("establishes a new start line when shift-click dragging after a cancelled or saved pick", () => {
    let latestAnchor: { x: number; top: number; bottom: number } | null = null;
    function ShiftDragHarness() {
      const pick = useLinePick(true);
      latestAnchor = pick.anchor;
      return (
        <div>
          {[1, 2, 3, 4, 5].map((n) => (
            <GutterNumber key={n} n={n} pick={pick} enabled />
          ))}
          {pick.range && (
            <LineBox
              where={`src/app.ts:${pick.range.from}-${pick.range.to}`}
              anchor={pick.anchor}
              onAdd={() => pick.clear()}
              onCancel={pick.clear}
            />
          )}
        </div>
      );
    }

    act(() => root.render(<ShiftDragHarness />));

    const line1 = host.querySelector<HTMLButtonElement>('[data-gutter="1"]')!;
    const line3 = host.querySelector<HTMLButtonElement>('[data-gutter="3"]')!;
    const line5 = host.querySelector<HTMLButtonElement>('[data-gutter="5"]')!;
    line3.getBoundingClientRect = () => new DOMRect(10, 200, 32, 20);
    line5.getBoundingClientRect = () => new DOMRect(10, 280, 32, 20);

    // Pick line 1 and cancel via Escape (calls pick.clear(), resetting startLine to null)
    act(() => {
      line1.dispatchEvent(new MouseEvent("pointerdown", { button: 0, bubbles: true }));
      window.dispatchEvent(new Event("pointerup"));
    });
    expect(document.querySelector("[data-comment-bar-context]")?.textContent).toContain("src/app.ts:1-1");
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(document.querySelector("[data-comment-bar]")).toBeNull();

    // Shift-click line 3 and drag to line 5 -> establishes start line 3 and sweeps 3-5 spanning top 200 to bottom 300
    act(() => {
      line3.dispatchEvent(new MouseEvent("pointerdown", { button: 0, shiftKey: true, bubbles: true }));
      line5.dispatchEvent(new MouseEvent("pointerover", { bubbles: true }));
      window.dispatchEvent(new Event("pointerup"));
    });
    expect(document.querySelector("[data-comment-bar-context]")?.textContent).toContain("src/app.ts:3-5");
    expect(latestAnchor).toEqual({ x: 50, top: 200, bottom: 300 });
  });

  it("reactively switches between floating and docked placement when viewport width crosses 639px on resize", () => {
    act(() => {
      root.render(
        <CommentBar
          anchor={{ x: 300, top: 200, bottom: 220 }}
          number={1}
          context="Resize reactivity check"
          onSave={() => {}}
          onCancel={() => {}}
        />,
      );
    });

    const bar = () => document.querySelector<HTMLElement>("[data-comment-bar]")!;
    expect(bar().dataset.docked).toBeUndefined();

    // Shrink window below 639px and fire resize -> docks immediately
    act(() => {
      Object.defineProperty(window, "innerWidth", { value: 390, configurable: true });
      window.dispatchEvent(new Event("resize"));
    });
    expect(bar().dataset.docked).toBe("true");

    // Grow window back above 639px and fire resize -> returns to floating placement
    act(() => {
      Object.defineProperty(window, "innerWidth", { value: 1280, configurable: true });
      window.dispatchEvent(new Event("resize"));
    });
    expect(bar().dataset.docked).toBeUndefined();
  });

  it("keeps the bar open on outside pointerdown when the note is dirty, and dismisses when empty or unchanged", () => {
    const onCancel = vi.fn();

    act(() => {
      root.render(
        <CommentBar
          anchor={{ x: 200, top: 100, bottom: 120 }}
          initialValue="existing note"
          autoFocus
          onSave={() => {}}
          onCancel={onCancel}
        />,
      );
    });

    const input = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;

    // Type a change -> dirty note must NOT be discarded on outside pointerdown
    act(() => {
      setInputValue(input, "existing note updated");
      document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    });
    expect(onCancel).not.toHaveBeenCalled();

    // Restore original value -> clean/unchanged note dismisses on outside pointerdown
    act(() => {
      setInputValue(input, "existing note");
      document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("re-anchors LineBox on scroll and switches to docked placement when the start line scrolls out of view", () => {
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      cb(0);
      return 1;
    });

    function ScrollLineHarness() {
      const pick = useLinePick(true);
      return (
        <div data-testid="file-code">
          {[1, 2, 3].map((n) => (
            <GutterNumber key={n} n={n} pick={pick} enabled />
          ))}
          {pick.range && (
            <LineBox
              where={`src/app.ts:${pick.range.from}-${pick.range.to}`}
              anchor={pick.anchor}
              onAdd={() => pick.clear()}
              onCancel={pick.clear}
            />
          )}
        </div>
      );
    }

    act(() => root.render(<ScrollLineHarness />));

    const container = host.querySelector<HTMLDivElement>('[data-testid="file-code"]')!;
    container.getBoundingClientRect = () => new DOMRect(0, 100, 600, 400);
    const line2 = host.querySelector<HTMLButtonElement>('[data-gutter="2"]')!;
    let currentTop = 200;
    line2.getBoundingClientRect = () => new DOMRect(10, currentTop, 32, 20);

    // Pick line 2 -> opens floating near y=228 (200 + 20 + 8)
    act(() => {
      line2.dispatchEvent(new MouseEvent("pointerdown", { button: 0, bubbles: true }));
      window.dispatchEvent(new Event("pointerup"));
    });
    const bar = () => document.querySelector<HTMLElement>("[data-comment-bar]")!;
    expect(bar().dataset.docked).toBeUndefined();
    expect(bar().style.top).toBe("228px");

    // Scroll container so line 2 moves up to y=140 -> bar follows to y=168
    act(() => {
      currentTop = 140;
      container.dispatchEvent(new Event("scroll", { bubbles: false }));
    });
    expect(bar().dataset.docked).toBeUndefined();
    expect(bar().style.top).toBe("168px");

    // Scroll container so line 2 leaves container bounds (above top=100) -> bar docks
    act(() => {
      currentTop = 40;
      container.dispatchEvent(new Event("scroll", { bubbles: false }));
    });
    expect(bar().dataset.docked).toBe("true");
  });

  it("preserves multiline notes through open and save (Enter submits, Shift+Enter does not)", () => {
    const onSave = vi.fn();
    act(() => {
      root.render(
        <CommentBar
          anchor={{ x: 200, top: 100, bottom: 120 }}
          initialValue={"line one\nline two"}
          autoFocus
          onSave={onSave}
          onCancel={() => {}}
        />,
      );
    });

    const field = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    expect(field.value).toBe("line one\nline two");

    // Shift+Enter does not submit
    act(() => {
      field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true }));
    });
    expect(onSave).not.toHaveBeenCalled();

    // Enter saves preserving the newline
    act(() => {
      field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(onSave).toHaveBeenCalledWith("line one\nline two");
  });

  it("persists a dirty popover edit when clicking the comments chip", () => {
    const restored = parseCommentBlock(["Comment 1", "Selected text:", "> passage", "", "initial note"].join("\n"))!;
    const id = addQuote(draftKey, restored, false);

    const renderBars = () => {
      act(() => {
        root.render(<QuoteBars draftKey={draftKey} quotes={getQuotes(draftKey).list} onRemove={(qid) => removeQuote(draftKey, qid)} />);
      });
    };

    renderBars();
    const chip = host.querySelector<HTMLButtonElement>("[data-comment-chip]")!;
    act(() => chip.click());
    act(() => document.querySelector<HTMLButtonElement>('[data-comment-edit="1"]')!.click());

    const field = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    act(() => setInputValue(field, "dirty note before chip click"));

    // Clicking the chip flushes the dirty note instead of discarding it
    act(() => chip.click());
    expect(getQuotes(draftKey).list.find((q) => q.id === id)?.comment?.body).toBe("dirty note before chip click");
  });

  it("persists a dirty pin edit when switching directly to another pin", () => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );

    const q1 = textQuote("alpha", { item: "msg-1", start: 0, end: 5, exact: "alpha", before: "" });
    q1.comment!.body = "first original";
    const q2 = textQuote("gamma", { item: "msg-1", start: 11, end: 16, exact: "gamma", before: "beta " });
    q2.comment!.body = "second original";

    const origCreateRange = document.createRange.bind(document);
    vi.spyOn(document, "createRange").mockImplementation(() => {
      const r = origCreateRange();
      mockRangeRect(r, new DOMRect(60, 40, 80, 18));
      return r;
    });

    function PinsHarness() {
      const scroller = useRef<HTMLDivElement>(null);
      return (
        <ChatActionsContext.Provider value={{ draftKey, title: "Test", canCompose: true, speaker: "Persona", images: () => [] }}>
          <div ref={scroller} style={{ position: "relative", height: 400 }}>
            <div>
              <div data-items="msg-1">
                <p>alpha beta gamma delta</p>
              </div>
            </div>
            <CommentMarks scroller={scroller} />
          </div>
        </ChatActionsContext.Provider>
      );
    }

    try {
      act(() => root.render(<PinsHarness />));
      let id1 = "";
      let id2 = "";
      act(() => {
        id1 = addQuote(draftKey, q1, false);
        id2 = addQuote(draftKey, q2, false);
      });
      const pin1 = host.querySelector<HTMLButtonElement>('[data-comment-pin="1"]')!;
      const pin2 = host.querySelector<HTMLButtonElement>('[data-comment-pin="2"]')!;
      expect(pin1).toBeTruthy();
      expect(pin2).toBeTruthy();

    // Open pin 1 and type a dirty edit -> immediately live-persisted in the quote store (e.g. if user hits Send)
      act(() => pin1.click());
      const field1 = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
      act(() => setInputValue(field1, "first updated live"));
      expect(getQuotes(draftKey).list.find((q) => q.id === id1)?.comment?.body).toBe("first updated live");

      // Re-clicking the same pin preserves the typed textarea without resetting it
      act(() => pin1.click());
      expect(document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!.value).toBe("first updated live");

      // Click pin 2 directly -> pin 1's live edit stays persisted and pin 2 opens
      act(() => pin2.click());
      expect(getQuotes(draftKey).list.find((q) => q.id === id1)?.comment?.body).toBe("first updated live");
      expect(document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!.value).toBe("second original");

      // Typing in pin 2 live-persists, and explicit Esc restores pin 2's originalBody
      const field2 = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
      act(() => setInputValue(field2, "second temporary edit"));
      expect(getQuotes(draftKey).list.find((q) => q.id === id2)?.comment?.body).toBe("second temporary edit");
      act(() => {
        field2.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(getQuotes(draftKey).list.find((q) => q.id === id2)?.comment?.body).toBe("second original");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("treats whitespace-only edits as dirty and does not ignore [data-comment-pin] clicks from CommentsChip editor", () => {
    const restored = parseCommentBlock(["Comment 1", "Selected text:", "> passage", "", "clean"].join("\n"))!;
    const id = addQuote(draftKey, restored, false);

    // Mount an unrelated pin button outside the chip editor
    const pin = document.createElement("button");
    pin.setAttribute("data-comment-pin", "1");
    document.body.appendChild(pin);

    try {
      act(() => {
        root.render(<QuoteBars draftKey={draftKey} quotes={getQuotes(draftKey).list} onRemove={(qid) => removeQuote(draftKey, qid)} />);
      });
      const chip = host.querySelector<HTMLButtonElement>("[data-comment-chip]")!;
      act(() => chip.click());
      act(() => document.querySelector<HTMLButtonElement>('[data-comment-edit="1"]')!.click());

      // When clean, clicking an external [data-comment-pin] dismisses CommentsChip's editor (not unconditionally exempt)
      act(() => {
        pin.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
      });
      expect(document.querySelector("[data-comment-bar]")).toBeNull();

      // Reopen and add trailing whitespace -> raw dirty comparison keeps the bar open on outside click and live-persists
      act(() => chip.click());
      act(() => document.querySelector<HTMLButtonElement>('[data-comment-edit="1"]')!.click());
      const field = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
      act(() => setInputValue(field, "clean "));
      expect(getQuotes(draftKey).list.find((q) => q.id === id)?.comment?.body).toBe("clean ");
      act(() => {
        pin.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
      });
      expect(document.querySelector("[data-comment-bar]")).toBeTruthy();

      // Esc restores originalBody
      act(() => {
        field.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(getQuotes(draftKey).list.find((q) => q.id === id)?.comment?.body).toBe("clean");
    } finally {
      pin.remove();
    }
  });

  it("saves a non-empty note typed in a new-selection bar when clicking an existing pin", () => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      cb(0);
      return 1;
    });

    const origCreateRange = document.createRange.bind(document);
    vi.spyOn(document, "createRange").mockImplementation(() => {
      const r = origCreateRange();
      mockRangeRect(r, new DOMRect(60, 40, 80, 18));
      return r;
    });

    function DraftToPinHarness() {
      const scroller = useRef<HTMLDivElement>(null);
      return (
        <ChatActionsContext.Provider value={{ draftKey, title: "Test", canCompose: true, speaker: "Persona", images: () => [] }}>
          <div ref={scroller} style={{ position: "relative", height: 400 }}>
            <div>
              <div data-items="msg-1">
                <p>alpha beta gamma delta</p>
              </div>
            </div>
            <CommentMarks scroller={scroller} />
          </div>
        </ChatActionsContext.Provider>
      );
    }

    try {
      act(() => root.render(<DraftToPinHarness />));
      const scroller = host.firstElementChild as HTMLDivElement;
      scroller.getBoundingClientRect = () => new DOMRect(0, 0, 800, 400);

      // Seed existing comment pin 1 on "alpha"
      const existing = textQuote("alpha", { item: "msg-1", start: 0, end: 5, exact: "alpha", before: "" });
      existing.comment!.body = "existing note";
      act(() => {
        addQuote(draftKey, existing, false);
      });
      const pin1 = host.querySelector<HTMLButtonElement>('[data-comment-pin="1"]')!;
      expect(pin1).toBeTruthy();

      // Select "gamma" to open a new-selection bar, focus it, and type a note
      const textNode = host.querySelector("p")!.firstChild as Text;
      const range = origCreateRange();
      range.setStart(textNode, 11);
      range.setEnd(textNode, 16);
      mockRangeRect(range, new DOMRect(120, 40, 60, 18));
      const sel = document.getSelection()!;
      sel.removeAllRanges();
      sel.addRange(range);
      act(() => {
        document.dispatchEvent(new Event("selectionchange"));
      });
      const draftField = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
      act(() => {
        draftField.focus();
        setInputValue(draftField, "do not lose this");
      });

      // Clicking pin 1 saves the typed new-selection comment before opening pin 1
      act(() => pin1.click());
      const list = getQuotes(draftKey).list;
      expect(list).toHaveLength(2);
      expect(list[1].text).toBe("gamma");
      expect(list[1].comment?.body).toBe("do not lose this");
      expect(document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!.value).toBe("existing note");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("closes an active chip editor without reverting when a transcript pin is opened, so Esc only cancels the pin edit", () => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );

    const origCreateRange = document.createRange.bind(document);
    vi.spyOn(document, "createRange").mockImplementation(() => {
      const r = origCreateRange();
      mockRangeRect(r, new DOMRect(60, 40, 80, 18));
      return r;
    });

    function CombinedHarness() {
      const scroller = useRef<HTMLDivElement>(null);
      return (
        <ChatActionsContext.Provider value={{ draftKey, title: "Test", canCompose: true, speaker: "Persona", images: () => [] }}>
          <div ref={scroller} style={{ position: "relative", height: 400 }}>
            <div>
              <div data-items="msg-1">
                <p>alpha beta gamma delta</p>
              </div>
            </div>
            <CommentMarks scroller={scroller} />
          </div>
          <QuoteBars draftKey={draftKey} quotes={getQuotes(draftKey).list} onRemove={(id) => removeQuote(draftKey, id)} />
        </ChatActionsContext.Provider>
      );
    }

    try {
      const q1 = textQuote("alpha", { item: "msg-1", start: 0, end: 5, exact: "alpha", before: "" });
      q1.comment!.body = "pin one original";
      const q2 = parseCommentBlock(["Comment 2", "Selected text:", "> pinless", "", "chip original"].join("\n"))!;
      act(() => root.render(<CombinedHarness />));
      let id1 = "";
      let id2 = "";
      act(() => {
        id1 = addQuote(draftKey, q1, false);
        id2 = addQuote(draftKey, q2, false);
      });
      act(() => root.render(<CombinedHarness />));

      // Open chip editor for comment 2 and type a dirty edit
      const chip = host.querySelector<HTMLButtonElement>("[data-comment-chip]")!;
      act(() => chip.click());
      act(() => document.querySelector<HTMLButtonElement>('[data-comment-edit="2"]')!.click());
      const chipField = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
      act(() => setInputValue(chipField, "chip updated live"));
      expect(getQuotes(draftKey).list.find((q) => q.id === id2)?.comment?.body).toBe("chip updated live");

      // Click transcript pin 1 -> chip editor yields cleanly (keeping its live edit) and only pin 1 bar remains mounted
      const pin1 = host.querySelector<HTMLButtonElement>('[data-comment-pin="1"]')!;
      act(() => pin1.click());
      expect(document.querySelectorAll("[data-comment-bar]")).toHaveLength(1);
      expect(getQuotes(draftKey).list.find((q) => q.id === id2)?.comment?.body).toBe("chip updated live");

      // Type in pin 1 and press Esc -> reverts ONLY pin 1, leaving comment 2's chip edit intact
      const pinField = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
      act(() => setInputValue(pinField, "pin one temporary"));
      act(() => {
        pinField.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(getQuotes(draftKey).list.find((q) => q.id === id1)?.comment?.body).toBe("pin one original");
      expect(getQuotes(draftKey).list.find((q) => q.id === id2)?.comment?.body).toBe("chip updated live");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("refocuses a selection bar on C after the user blurs the textarea", () => {
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      cb(0);
      return 1;
    });

    function BlurRefocusHarness() {
      const scroller = useRef<HTMLDivElement>(null);
      return (
        <ChatActionsContext.Provider value={{ draftKey, title: "Test", canCompose: true, speaker: "Persona", images: () => [] }}>
          <div ref={scroller} style={{ position: "relative", height: 400 }}>
            <div>
              <div data-items="msg-1">
                <p>alpha beta gamma delta</p>
              </div>
            </div>
            <CommentMarks scroller={scroller} />
          </div>
        </ChatActionsContext.Provider>
      );
    }

    act(() => root.render(<BlurRefocusHarness />));
    const scroller = host.firstElementChild as HTMLDivElement;
    scroller.getBoundingClientRect = () => new DOMRect(0, 0, 800, 400);

    const textNode = host.querySelector("p")!.firstChild as Text;
    const range = document.createRange();
    range.setStart(textNode, 6);
    range.setEnd(textNode, 10);
    mockRangeRect(range, new DOMRect(60, 40, 40, 18));
    const sel = document.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);
    act(() => {
      document.dispatchEvent(new Event("selectionchange"));
    });

    const field = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    // First press C -> focuses bar
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "c", bubbles: true }));
    });
    expect(document.activeElement).toBe(field);

    // Blur the textarea -> bar goes dormant again
    act(() => {
      field.blur();
    });
    expect(document.activeElement).not.toBe(field);

    // Press C again -> refocuses the bar cleanly
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "c", bubbles: true }));
    });
    expect(document.activeElement).toBe(field);
  });

  it("keeps a blurred dirty selection bar registered so opening another editor yields and saves it", () => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      cb(0);
      return 1;
    });
    const origCreateRange = document.createRange.bind(document);
    vi.spyOn(document, "createRange").mockImplementation(() => {
      const r = origCreateRange();
      mockRangeRect(r, new DOMRect(60, 40, 40, 18));
      return r;
    });

    function YieldAfterBlurHarness({ showLine }: { showLine: boolean }) {
      const scroller = useRef<HTMLDivElement>(null);
      return (
        <ChatActionsContext.Provider value={{ draftKey, title: "Test", canCompose: true, speaker: "Persona", images: () => [] }}>
          <div ref={scroller} style={{ position: "relative", height: 400 }}>
            <div>
              <div data-items="msg-1">
                <p>alpha beta gamma delta</p>
              </div>
            </div>
            <CommentMarks scroller={scroller} />
          </div>
          {showLine && (
            <LineBox
              where="src/app.ts:4"
              anchor={{ x: 100, top: 100, bottom: 120 }}
              onAdd={() => {}}
              onCancel={() => {}}
            />
          )}
        </ChatActionsContext.Provider>
      );
    }

    try {
      act(() => root.render(<YieldAfterBlurHarness showLine={false} />));
      const scroller = host.firstElementChild as HTMLDivElement;
      scroller.getBoundingClientRect = () => new DOMRect(0, 0, 800, 400);

      const textNode = host.querySelector("p")!.firstChild as Text;
      const range = document.createRange();
      range.setStart(textNode, 6);
      range.setEnd(textNode, 10);
      const sel = document.getSelection()!;
      sel.removeAllRanges();
      sel.addRange(range);
      act(() => {
        document.dispatchEvent(new Event("selectionchange"));
      });

      const field = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
      act(() => {
        field.focus();
        setInputValue(field, "blurred dirty note");
        field.blur();
      });
      expect(document.activeElement).not.toBe(field);

      // Opening a LineBox while the dirty selection bar is blurred triggers yield -> saves the dirty selection comment
      act(() => root.render(<YieldAfterBlurHarness showLine />));
      expect(getQuotes(draftKey).list).toHaveLength(1);
      expect(getQuotes(draftKey).list[0].text).toBe("beta");
      expect(getQuotes(draftKey).list[0].comment?.body).toBe("blurred dirty note");
      expect(document.querySelectorAll("[data-comment-bar]")).toHaveLength(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("does not discard a blurred dirty selection draft on transcript pointerdown", () => {
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      cb(0);
      return 1;
    });
    const origCreateRange = document.createRange.bind(document);
    vi.spyOn(document, "createRange").mockImplementation(() => {
      const r = origCreateRange();
      mockRangeRect(r, new DOMRect(60, 40, 40, 18));
      return r;
    });

    function DirtyTranscriptHarness() {
      const scroller = useRef<HTMLDivElement>(null);
      return (
        <ChatActionsContext.Provider value={{ draftKey, title: "Test", canCompose: true, speaker: "Persona", images: () => [] }}>
          <div ref={scroller} style={{ position: "relative", height: 400 }}>
            <div>
              <div data-items="msg-1">
                <p>alpha beta gamma delta</p>
              </div>
            </div>
            <CommentMarks scroller={scroller} />
          </div>
        </ChatActionsContext.Provider>
      );
    }

    act(() => root.render(<DirtyTranscriptHarness />));
    const scroller = host.firstElementChild as HTMLDivElement;
    scroller.getBoundingClientRect = () => new DOMRect(0, 0, 800, 400);

    const p = host.querySelector("p")!;
    const textNode = p.firstChild as Text;
    const range = document.createRange();
    range.setStart(textNode, 6);
    range.setEnd(textNode, 10);
    const sel = document.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);
    act(() => {
      document.dispatchEvent(new Event("selectionchange"));
    });

    const field = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    act(() => {
      field.focus();
      setInputValue(field, "keep me alive");
      field.blur();
    });

    // Clicking inside the transcript while blurred + dirty must keep the bar open with its note intact
    act(() => {
      p.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
      document.dispatchEvent(new Event("selectionchange"));
    });
    const stillThere = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea");
    expect(stillThere).toBeTruthy();
    expect(stillThere!.value).toBe("keep me alive");
  });

  it("calls onDismiss instead of onCancel on clean outside pointerdown, and ignores Safari post-composition Enter/Esc", () => {
    const onSave = vi.fn();
    const onCancel = vi.fn();
    const onDismiss = vi.fn();

    act(() => {
      root.render(
        <CommentBar
          anchor={{ x: 200, top: 100, bottom: 120 }}
          initialValue="unchanged"
          autoFocus
          onSave={onSave}
          onCancel={onCancel}
          onDismiss={onDismiss}
        />,
      );
    });

    const field = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;

    // Safari post-composition tail: compositionend followed immediately by Enter or Escape must be ignored
    act(() => {
      field.dispatchEvent(new Event("compositionstart", { bubbles: true }));
      field.dispatchEvent(new Event("compositionend", { bubbles: true }));
      field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      field.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(onSave).not.toHaveBeenCalled();
    expect(onCancel).not.toHaveBeenCalled();

    // Outside pointerdown when unchanged invokes onDismiss, not onCancel
    act(() => {
      document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    });
    expect(onDismiss).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("preserves untrimmed selection text and recomputes live range in saveDraft", () => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      cb(0);
      return 1;
    });
    const origCreateRange = document.createRange.bind(document);
    vi.spyOn(document, "createRange").mockImplementation(() => {
      const r = origCreateRange();
      mockRangeRect(r, new DOMRect(60, 40, 60, 18));
      return r;
    });

    function LiveRangeHarness() {
      const scroller = useRef<HTMLDivElement>(null);
      return (
        <ChatActionsContext.Provider value={{ draftKey, title: "Test", canCompose: true, speaker: "Persona", images: () => [] }}>
          <div ref={scroller} style={{ position: "relative", height: 400 }}>
            <div>
              <div data-items="msg-1">
                <p>alpha beta gamma</p>
              </div>
            </div>
            <CommentMarks scroller={scroller} />
          </div>
        </ChatActionsContext.Provider>
      );
    }

    try {
      act(() => root.render(<LiveRangeHarness />));
      const scroller = host.firstElementChild as HTMLDivElement;
      scroller.getBoundingClientRect = () => new DOMRect(0, 0, 800, 400);
      const textNode = host.querySelector("p")!.firstChild as Text;

      // Select " beta " with leading & trailing spaces
      const range = document.createRange();
      range.setStart(textNode, 5);
      range.setEnd(textNode, 11);
      const sel = document.getSelection()!;
      sel.removeAllRanges();
      sel.addRange(range);
      act(() => {
        document.dispatchEvent(new Event("selectionchange"));
      });

      // Simulate streaming prepend before saving -> anchor offsets shift, saveDraft re-resolves live offsets
      textNode.data = "PRE " + textNode.data;

      act(() => {
        document.querySelector<HTMLButtonElement>("[data-comment-save]")!.click();
      });
      const saved = getQuotes(draftKey).list[0];
      expect(saved.text).toBe(" beta ");
      expect(saved.comment?.anchor?.start).toBe(9);
      expect(saved.comment?.anchor?.end).toBe(15);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("lets page-level Escape pass through when LineBox is open but focus is outside it", () => {
    const onCancel = vi.fn();
    const pageEscape = vi.fn();
    window.addEventListener("keydown", pageEscape);

    try {
      act(() => {
        root.render(
          <>
            <LineBox where="src/app.ts:10" anchor={{ x: 100, top: 100, bottom: 120 }} onAdd={() => {}} onCancel={onCancel} />
            <textarea data-testid="external-composer" />
          </>,
        );
      });

      const external = host.querySelector<HTMLTextAreaElement>('[data-testid="external-composer"]')!;
      act(() => external.focus());
      expect(document.activeElement).toBe(external);

      act(() => {
        external.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(pageEscape).toHaveBeenCalledTimes(1);
      expect(onCancel).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", pageEscape);
    }
  });

  it("does not query another mounted file's gutter when scopeRef is missing or disconnected", () => {
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      cb(0);
      return 1;
    });

    // Mount an unrelated file's gutter button with data-gutter-at="2" elsewhere in the document
    const foreignGutter = document.createElement("button");
    foreignGutter.setAttribute("data-gutter-at", "2");
    foreignGutter.getBoundingClientRect = () => new DOMRect(500, 300, 32, 20);
    document.body.appendChild(foreignGutter);

    let latestAnchor: { x: number; top: number; bottom: number } | null = null;
    function UnscopedHarness({ showRow }: { showRow: boolean }) {
      const pick = useLinePick(true);
      latestAnchor = pick.anchor;
      return (
        <div>
          {showRow && <GutterNumber n={2} pick={pick} enabled />}
          {pick.range && (
            <LineBox
              where={`src/app.ts:${pick.range.from}-${pick.range.to}`}
              anchor={pick.anchor}
              onAdd={() => pick.clear()}
              onCancel={pick.clear}
            />
          )}
        </div>
      );
    }

    try {
      act(() => root.render(<UnscopedHarness showRow />));
      const line2 = host.querySelector<HTMLButtonElement>('[data-gutter="2"]')!;
      line2.getBoundingClientRect = () => new DOMRect(10, 120, 32, 20);

      act(() => {
        line2.dispatchEvent(new MouseEvent("pointerdown", { button: 0, bubbles: true }));
        window.dispatchEvent(new Event("pointerup"));
      });
      expect(latestAnchor).toEqual({ x: 50, top: 120, bottom: 140 });

      // Unmount our row (simulating virtualization) with no connected scopeRef -> scroll must dock (null), NOT latch onto foreignGutter
      act(() => root.render(<UnscopedHarness showRow={false} />));
      act(() => {
        window.dispatchEvent(new Event("scroll"));
      });
      expect(latestAnchor).toBeNull();
      expect(document.querySelector<HTMLElement>("[data-comment-bar]")?.dataset.docked).toBe("true");
    } finally {
      foreignGutter.remove();
    }
  });

  it("flips/clamps the floating bar when only viewport height shrinks on resize", () => {
    act(() => {
      root.render(
        <CommentBar
          anchor={{ x: 300, top: 520, bottom: 540 }}
          autoFocus
          onSave={() => {}}
          onCancel={() => {}}
        />,
      );
    });

    const bar = () => document.querySelector<HTMLElement>("[data-comment-bar]")!;
    // At innerHeight=800, bar sits below anchor: 540 + 8 = 548px
    expect(bar().style.top).toBe("548px");

    // Shrink ONLY viewport height (same innerWidth=1280) so below (548 + 44 = 592) overflows 560 -> flips above (520 - 44 - 8 = 468px)
    act(() => {
      Object.defineProperty(window, "innerHeight", { value: 560, configurable: true });
      window.dispatchEvent(new Event("resize"));
    });
    expect(bar().style.top).toBe("468px");
  });

  it("distinguishes unfocused selection bar state with shortcut hint and activates cleanly on click or focus", () => {
    const onCancel = vi.fn();
    const pageEscape = vi.fn();
    window.addEventListener("keydown", pageEscape);

    try {
      act(() => {
        root.render(
          <CommentBar
            anchor={{ x: 200, top: 100, bottom: 120 }}
            autoFocus={false}
            shortcutHint="C"
            onSave={() => {}}
            onCancel={onCancel}
          />,
        );
      });

      const bar = document.querySelector<HTMLElement>("[data-comment-bar]")!;
      const input = bar.querySelector<HTMLTextAreaElement>("textarea")!;
      expect(bar.dataset.focused).toBe("false");
      expect(bar.getAttribute("role")).toBeNull();
      expect(bar.querySelector("[data-comment-shortcut]")?.textContent).toBe("C");

      // While dormant, Escape still passes through to the page
      act(() => {
        window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(pageEscape).toHaveBeenCalledTimes(1);
      expect(onCancel).not.toHaveBeenCalled();

      // Clicking the bar focuses the textarea, flips data-focused="true", adds role="dialog", and hides the dormant C keycap
      act(() => {
        bar.click();
      });
      expect(document.activeElement).toBe(input);
      expect(bar.dataset.focused).toBe("true");
      expect(bar.getAttribute("role")).toBe("dialog");
      expect(bar.querySelector("[data-comment-shortcut]")).toBeNull();

      // Once focused, Escape cancels the bar and stops propagation to the page
      pageEscape.mockClear();
      act(() => {
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(onCancel).toHaveBeenCalledTimes(1);
      expect(pageEscape).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", pageEscape);
    }
  });

  it("lets Escape pass through when selection bar is unfocused and ignores Escape during IME composition", () => {
    const onCancel = vi.fn();
    const pageEscape = vi.fn();
    window.addEventListener("keydown", pageEscape);

    try {
      act(() => {
        root.render(<CommentBar anchor={{ x: 200, top: 100, bottom: 120 }} autoFocus={false} onSave={() => {}} onCancel={onCancel} />);
      });

      const input = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
      expect(document.activeElement).not.toBe(input);

      // Unfocused selection bar: Escape passes through to the page handler and does not cancel
      act(() => {
        window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(pageEscape).toHaveBeenCalledTimes(1);
      expect(onCancel).not.toHaveBeenCalled();

      // Focused bar during IME composition: Escape does not close the bar or lose input
      act(() => {
        input.focus();
        setInputValue(input, "nihon");
        const imeEsc = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
        Object.defineProperty(imeEsc, "isComposing", { value: true });
        input.dispatchEvent(imeEsc);
      });
      expect(onCancel).not.toHaveBeenCalled();
      expect(input.value).toBe("nihon");

      // Focused bar with normal Escape: intercepts and cancels without reaching page listener
      pageEscape.mockClear();
      act(() => {
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(onCancel).toHaveBeenCalledTimes(1);
      expect(pageEscape).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", pageEscape);
    }
  });

  it("edits a pinless comment from the CommentsChip popover and preserves the original note on cancel", () => {
    // Simulate a comment restored from a sent message (parseCommentBlock -> no transcript/gutter anchor)
    const restored = parseCommentBlock(["Comment 1", "Selected text:", "> restored passage", "", "original note"].join("\n"))!;
    expect(restored.comment?.anchor).toBeUndefined();
    const id = addQuote(draftKey, restored, false);

    const renderBars = () => {
      const quotes = getQuotes(draftKey).list;
      act(() => {
        root.render(
          <QuoteBars
            draftKey={draftKey}
            quotes={quotes}
            onRemove={(qid) => {
              removeQuote(draftKey, qid);
              renderBars();
            }}
          />,
        );
      });
    };

    renderBars();
    const chip = host.querySelector<HTMLButtonElement>("[data-comment-chip]")!;

    // Open popover and click edit -> opens CommentBar prefilled with original note
    act(() => chip.click());
    const editBtn = document.querySelector<HTMLButtonElement>('[data-comment-edit="1"]')!;
    expect(editBtn).toBeTruthy();
    act(() => editBtn.click());

    const input = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    expect(input.value).toBe("original note");

    // Modify and cancel with Esc -> stored note stays unchanged
    act(() => {
      setInputValue(input, "discarded edit");
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(document.querySelector("[data-comment-bar]")).toBeNull();
    expect(getQuotes(draftKey).list.find((q) => q.id === id)?.comment?.body).toBe("original note");

    // Reopen popover, edit again, and save with Enter -> stored note updates
    act(() => chip.click());
    act(() => document.querySelector<HTMLButtonElement>('[data-comment-edit="1"]')!.click());
    const input2 = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    act(() => {
      setInputValue(input2, "updated before resend");
      input2.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(document.querySelector("[data-comment-bar]")).toBeNull();
    expect(getQuotes(draftKey).list.find((q) => q.id === id)?.comment?.body).toBe("updated before resend");
  });

  it("keeps outgoing serialized message format unchanged", () => {
    const q1 = textQuote("first quote", undefined);
    q1.comment!.body = "first note";
    const q2 = textQuote("second quote", undefined);
    // empty note allowed
    expect(withQuoteBlock([q1, q2], "Please review")).toBe(
      [
        "> Comment 1",
        "> Selected text:",
        "> > first quote",
        "> ",
        "> first note",
        "",
        "> Comment 2",
        "> Selected text:",
        "> > second quote",
        "",
        "Please review",
      ].join("\n"),
    );
  });
});
