// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConversationTagFilterBar } from "./ConversationTagFilterBar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const flush = async () => { for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); }); };

describe("ConversationTagFilterBar (#12018)", () => {
  let host: HTMLDivElement, root: Root;
  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => { act(() => root.unmount()); host.remove(); });

  it("keeps the default chips free of editing controls and renders no fresh-install row", () => {
    const props = { active: "", customized: false, onSelect: vi.fn(), onChange: vi.fn(), onDone: vi.fn() };
    act(() => root.render(<ConversationTagFilterBar {...props} entries={["trip"]} editing={false} />));
    expect(host.textContent).toContain("trip");
    expect(host.textContent).not.toContain("添加过滤");
    expect(host.querySelectorAll("button")).toHaveLength(1);
    act(() => root.render(<ConversationTagFilterBar {...props} entries={[]} editing={false} />));
    expect(host.children).toHaveLength(0);
  });

  it("adds, removes, selects and resets through caller-owned data", async () => {
    const onSelect = vi.fn(), onChange = vi.fn().mockResolvedValue(true), onDone = vi.fn();
    act(() => root.render(<ConversationTagFilterBar entries={["planning", "trip"]} active="planning" customized editing onSelect={onSelect} onChange={onChange} onDone={onDone} />));
    const chip = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "planning")!;
    act(() => chip.click());
    expect(onSelect).toHaveBeenCalledWith("planning");

    const input = host.querySelector("input")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "  new topic  ");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => input.closest("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    await flush();
    expect(onChange).toHaveBeenCalledWith(["planning", "trip", "new topic"]);

    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="移除过滤「planning」"]')!.click());
    await flush();
    expect(onChange).toHaveBeenCalledWith(["trip"]);
    const reset = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "恢复全部在用标签")!;
    act(() => reset.click());
    await flush();
    expect(onChange).toHaveBeenCalledWith(null);
    const done = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "完成")!;
    act(() => done.click());
    expect(onDone).toHaveBeenCalledOnce();
  });

  it("keeps a rejected draft for retry and shows a deliberate custom empty set", async () => {
    const onChange = vi.fn().mockResolvedValue(false);
    act(() => root.render(<ConversationTagFilterBar entries={[]} active="" customized editing onSelect={vi.fn()} onChange={onChange} onDone={vi.fn()} />));
    const input = host.querySelector("input")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "keep me");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => input.closest("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    await flush();
    expect(input.value).toBe("keep me");
    act(() => root.render(<ConversationTagFilterBar entries={[]} active="" customized={true} editing={false} onSelect={vi.fn()} onChange={onChange} onDone={vi.fn()} />));
    expect(host.textContent).toContain("暂无标签过滤项");
  });
});
