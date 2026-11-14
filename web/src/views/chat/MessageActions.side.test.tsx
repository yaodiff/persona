// @vitest-environment jsdom
// Your bubble's hover bar (audit item 73): beside the bubble, to its left, where that fits inside what shows the chat; in
// a narrow window a wide bubble leaves no room there, and the same buttons sit under it instead, so neither the pointer
// nor Tab ever reaches a button off the screen.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatActionsContext, type ChatActions } from "./chat-actions";
import { MessageActions, sideBarFits } from "./MessageActions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const chat: ChatActions = { draftKey: "m/s", canCompose: true, speaker: "Persona", title: "Persona", images: () => [] };

let host: HTMLDivElement;
let root: Root;
// where the bubble's row starts, and how wide its bar is: what layout would say
let rowLeft = 0;
const BAR = 110;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.hasAttribute("data-side-bar") ? BAR : 0;
  });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    const left = this.classList.contains("group/msg") ? rowLeft : 0;
    return { left, right: left + 200, top: 0, bottom: 40, width: 200, height: 40, x: left, y: 0, toJSON: () => ({}) } as DOMRect;
  });
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

function draw() {
  act(() =>
    root.render(
      <ChatActionsContext.Provider value={chat}>
        <MessageActions text="给天气 App 加一个主屏幕小组件" isUser ts="2026-11-14T05:00:00Z" side>
          <div>给天气 App 加一个主屏幕小组件</div>
        </MessageActions>
      </ChatActionsContext.Provider>,
    ),
  );
}
const bar = () => host.querySelector<HTMLElement>("[data-side-bar]")!;
const copy = () => host.querySelector<HTMLButtonElement>('[data-side-bar] button[aria-label="复制消息"]')!;

describe("your bubble's hover bar", () => {
  it("stays beside a bubble with room to its left", () => {
    rowLeft = 400;
    draw();
    act(() => copy().focus());
    expect(bar().getAttribute("data-side-bar")).toBe("beside");
    expect(bar().className).toContain("right-full");
  });

  it("moves under a bubble with no room to its left when Tab reaches it, keeping the focus", () => {
    rowLeft = 60;
    draw();
    expect(bar().getAttribute("data-side-bar")).toBe("beside");
    act(() => copy().focus());
    expect(bar().getAttribute("data-side-bar")).toBe("below");
    expect(bar().className).toContain("top-full");
    expect(document.activeElement).toBe(copy());
    // hidden, it takes no clicks meant for what is under it
    expect(bar().className).toContain("pointer-events-none");
    expect(bar().className).toContain("focus-within:pointer-events-auto");
  });

  it("is placed again when the pointer comes", () => {
    rowLeft = 60;
    draw();
    const row = host.querySelector<HTMLElement>(".group\\/msg")!;
    act(() => row.dispatchEvent(new PointerEvent("pointerover", { bubbles: true, pointerType: "mouse" })));
    expect(bar().getAttribute("data-side-bar")).toBe("below");
    rowLeft = 500;
    act(() => row.dispatchEvent(new PointerEvent("pointerout", { bubbles: true, pointerType: "mouse" })));
    act(() => row.dispatchEvent(new PointerEvent("pointerover", { bubbles: true, pointerType: "mouse" })));
    expect(bar().getAttribute("data-side-bar")).toBe("beside");
  });

  it("measures against the window's edge and the bar's gap", () => {
    const row = document.createElement("div");
    const side = document.createElement("div");
    side.setAttribute("data-side-bar", "");
    row.classList.add("group/msg");
    document.body.append(row);
    rowLeft = BAR + 6;
    expect(sideBarFits(row, side)).toBe(true);
    rowLeft = BAR + 5;
    expect(sideBarFits(row, side)).toBe(false);
    row.remove();
  });
});
