// The shared search box: whatever its tone and size, the control lights its edge while it has focus (to-dos, Terminal and
// Agents had their own boxes that showed only a caret), names its field, and "Clear" (or Esc) empties it.
// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SEARCH_FOCUS, SearchField } from "./SearchField";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  document.body.innerHTML = "";
});

describe("SearchField", () => {
  it("has the focus ring in every tone and size", () => {
    for (const tone of ["surface", "inset"] as const)
      for (const size of ["sm", "md"] as const) {
        const html = renderToStaticMarkup(<SearchField tone={tone} size={size} value="" onChange={() => {}} placeholder="搜索待办" />);
        expect(html).toContain(SEARCH_FOCUS);
        expect(html).toContain('aria-label="搜索待办"');
      }
  });

  it("keeps the tone's own edge unfocused, and the caller's class is placement only", () => {
    const html = renderToStaticMarkup(<SearchField tone="inset" className="mx-3" value="" onChange={() => {}} placeholder="跳转" label="跳转到窗格" />);
    expect(html).toContain("bg-inset shadow-hairline");
    expect(html).toContain("mx-3");
    expect(html).toContain('aria-label="跳转到窗格"');
    expect(renderToStaticMarkup(<SearchField icon={false} value="" onChange={() => {}} placeholder="x" />)).not.toContain("<svg");
  });

  it("offers 清除 only with something typed, and it empties the field", () => {
    const host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    let value = "abc";
    act(() => root!.render(<SearchField value={value} onChange={(v) => (value = v)} placeholder="搜索" />));
    const clear = host.querySelector<HTMLButtonElement>('button[aria-label="清除"]');
    expect(clear).not.toBeNull();
    act(() => clear!.click());
    expect(value).toBe("");
    act(() => root!.render(<SearchField value="" onChange={() => {}} placeholder="搜索" />));
    expect(host.querySelector("button")).toBeNull();
  });

  // the to-dos' search kept "zzzz" after Esc, its empty list with it, and only the × cleared it
  describe("Esc", () => {
    function field(value: string, onKeyDown?: (e: React.KeyboardEvent<HTMLInputElement>) => void) {
      const host = document.createElement("div");
      document.body.append(host);
      root = createRoot(host);
      const onChange = vi.fn();
      act(() => root!.render(<SearchField value={value} onChange={onChange} onKeyDown={onKeyDown} placeholder="搜索待办" />));
      // what a Dialog or a side panel around it hears: Esc reaching the window
      const around = vi.fn();
      window.addEventListener("keydown", around);
      const press = (init: KeyboardEventInit = {}) => {
        const e = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true, ...init });
        act(() => void host.querySelector("input")!.dispatchEvent(e));
        return e;
      };
      return { onChange, around, press, done: () => window.removeEventListener("keydown", around) };
    }

    it("empties a field with something in it, and is spent on that: what the field is in stays open", () => {
      const f = field("zzzz不存在");
      const e = f.press();
      expect(f.onChange).toHaveBeenCalledExactlyOnceWith("");
      expect(e.defaultPrevented).toBe(true);
      expect(f.around).not.toHaveBeenCalled();
      f.done();
    });

    it("in an empty field is left to what the field is in (a Dialog closes as before)", () => {
      const f = field("");
      f.press();
      expect(f.onChange).not.toHaveBeenCalled();
      expect(f.around).toHaveBeenCalledOnce();
      f.done();
    });

    it("is the input method's while it composes, and the caller's when its own handler took it", () => {
      const f = field("zz", (e) => e.key === "Escape" && e.preventDefault());
      f.press();
      expect(f.onChange).not.toHaveBeenCalled();
      f.done();
      act(() => root!.unmount());
      const g = field("zz");
      g.press({ isComposing: true });
      g.press({ keyCode: 229 });
      expect(g.onChange).not.toHaveBeenCalled();
      g.done();
    });
  });
});
