// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { NotLivePill } from "./ChatView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("NotLivePill", () => {
  it("renders clickable Reload button above chat scroller and invokes onReload", () => {
    const onReload = vi.fn();
    act(() => root.render(<NotLivePill onReload={onReload} />));

    const pill = host.querySelector("[data-testid=\"chat-not-live\"]") as HTMLElement;
    expect(pill).not.toBeNull();
    expect(pill.className).toContain("z-20");
    expect(pill.className).toContain("top-3");

    const btn = pill.querySelector("button") as HTMLButtonElement;
    expect(btn.type).toBe("button");
    expect(btn.disabled).toBe(false);
    expect(btn.className).toContain("cursor-pointer");
    expect(btn.textContent).toBe(t("chat-chatview.not_live_reload"));

    act(() => btn.click());
    expect(onReload).toHaveBeenCalledTimes(1);
  });

  it("disables the button and shows reloading feedback while reloading", () => {
    const onReload = vi.fn();
    act(() => root.render(<NotLivePill onReload={onReload} reloading lower error="timeout" />));

    const pill = host.querySelector("[data-testid=\"chat-not-live\"]") as HTMLElement;
    expect(pill.className).toContain("top-14");
    expect(pill.textContent).toContain(t("chat-chatview.refresh_failed"));

    const btn = pill.querySelector("button") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    expect(btn.textContent).toBe(t("chat-chatview.not_live_reloading"));

    act(() => btn.click());
    expect(onReload).not.toHaveBeenCalled();
  });
});
