// @vitest-environment jsdom
// The key bar's line box (#14527): one hidden line for a prompt in the pane. Enter or its key types it with Return,
// once, then the box closes and the field is empty; an empty line sends nothing; Show reveals it; nothing offers it to
// a password manager.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../../i18n";
import { LineBox } from "./LineBox";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const field = () => host.querySelector("input") as HTMLInputElement;
const button = (label: string) => host.querySelector(`button[aria-label="${label}"]`) as HTMLButtonElement;
const type = (value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field(), value);
    field().dispatchEvent(new Event("input", { bubbles: true }));
  });

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("LineBox", () => {
  it("takes a hidden line with no password manager offer", () => {
    act(() => root.render(<LineBox onSend={() => {}} onClose={() => {}} />));
    const input = field();
    expect(input.type).toBe("password");
    expect(input.getAttribute("autocomplete")).toBe("one-time-code");
    expect(input.getAttribute("spellcheck")).toBe("false");
    expect(input.closest("form")).toBeNull();
    expect(input.hasAttribute("name")).toBe(false);
    expect(document.activeElement).toBe(input);
    act(() => button(t("session-terminal-keybar.line_show")).click());
    expect(field().type).toBe("text");
  });

  it("types the line with Return once, then closes", () => {
    const onSend = vi.fn();
    const onClose = vi.fn();
    act(() => root.render(<LineBox onSend={onSend} onClose={onClose} />));
    expect(button(t("session-terminal-keybar.line_send")).disabled).toBe(true);
    act(() => field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(onSend).not.toHaveBeenCalled();
    type("hunter2-test");
    act(() => field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend).toHaveBeenCalledWith("hunter2-test");
    expect(onClose).toHaveBeenCalled();
    expect(field().value).toBe("");
  });

  it("sends with its key too, and Escape closes without sending", () => {
    const onSend = vi.fn();
    const onClose = vi.fn();
    act(() => root.render(<LineBox onSend={onSend} onClose={onClose} />));
    type("pin-1234");
    act(() => button(t("session-terminal-keybar.line_send")).click());
    expect(onSend).toHaveBeenCalledWith("pin-1234");
    onClose.mockClear();
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); });
    expect(onClose).toHaveBeenCalled();
    expect(onSend).toHaveBeenCalledTimes(1);
  });
});
