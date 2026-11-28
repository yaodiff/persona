// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LarkAttachment } from "../../lib/lark-attachments";
import { loadDraft, saveDraft } from "../../lib/lark-chat";

const stageMock = vi.hoisted(() => vi.fn<(file: File) => Promise<LarkAttachment>>());
const releaseMock = vi.hoisted(() => vi.fn<(attachments: LarkAttachment[]) => void>());
const toasts = vi.hoisted(() => [] as string[]);
vi.mock("../../lib/lark-attachments", async (orig) => ({
  ...(await orig<typeof import("../../lib/lark-attachments")>()),
  stageLarkAttachment: (f: File) => stageMock(f),
  releaseLarkAttachments: (list: LarkAttachment[]) => releaseMock(list),
}));
vi.mock("../../ui/atoms", async (orig) => ({
  ...(await orig<typeof import("../../ui/atoms")>()),
  toast: (msg: string) => void toasts.push(msg),
}));

import { LarkComposer } from "./LarkComposer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  stageMock.mockReset();
  releaseMock.mockReset();
  toasts.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

const box = () => host.querySelector<HTMLTextAreaElement>("textarea")!;
const fileInput = () => host.querySelector<HTMLInputElement>('input[type="file"]')!;
const sendButton = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "发送")!;

const type = async (value: string) => {
  const el = box();
  const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  await act(async () => {
    set.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const pick = async (file: File) => {
  const el = fileInput();
  Object.defineProperty(el, "files", { value: [file], configurable: true });
  await act(async () => {
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
};

const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};

describe("LarkComposer lifecycle, double-submit, attachment leaks, and Safari IME", () => {
  it("prevents synchronous double-submit on rapid Enter presses before React re-renders", async () => {
    let finish!: (v: boolean) => void;
    const onSend = vi.fn(() => new Promise<boolean>((r) => { finish = r; }));
    await act(async () => root.render(<StrictMode><LarkComposer onSend={onSend} /></StrictMode>));
    await type("hello once");

    // Two Enter events in the same act() tick before sending state updates.
    await act(async () => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      sendButton().click();
    });
    expect(onSend).toHaveBeenCalledTimes(1);

    await act(async () => { finish(true); });
    await flush();
    expect(box().value).toBe("");
  });

  it("preserves text typed and attachments staged while onSend is in flight", async () => {
    let finish!: (v: boolean) => void;
    const onSend = vi.fn(() => new Promise<boolean>((r) => { finish = r; }));
    const firstFile = new File(["a"], "first.png", { type: "image/png" });
    const secondFile = new File(["b"], "second.png", { type: "image/png" });
    const firstAtt: LarkAttachment = { id: "att-1", data: firstFile, kind: "image", name: "first.png", mime: "image/png", size: 1, url: "blob:test/1" };
    const secondAtt: LarkAttachment = { id: "att-2", data: secondFile, kind: "image", name: "second.png", mime: "image/png", size: 1, url: "blob:test/2" };
    stageMock.mockResolvedValueOnce(firstAtt).mockResolvedValueOnce(secondAtt);

    await act(async () => root.render(<StrictMode><LarkComposer onSend={onSend} attachments /></StrictMode>));
    await pick(firstFile);
    await flush();
    await type("first message");

    await act(async () => { sendButton().click(); });
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend).toHaveBeenCalledWith("first message", [firstAtt], undefined, []);

    // User types a new draft and stages another attachment while first send is still pending.
    await type("next draft");
    await pick(secondFile);
    await flush();

    // First send completes: only submitted attachment is removed; new text and new attachment stay intact and un-revoked.
    await act(async () => { finish(true); });
    await flush();
    expect(box().value).toBe("next draft");
    expect(host.querySelector('img[src="blob:test/2"]')).not.toBeNull();
    expect(host.querySelector('img[src="blob:test/1"]')).toBeNull();
    expect(releaseMock).not.toHaveBeenCalled();

    // Unmounting now revokes only the remaining staged attachment (never the handed-off firstAtt).
    await act(async () => root.unmount());
    expect(releaseMock).toHaveBeenCalledWith([secondAtt]);
  });

  it("revokes late-resolving staged attachments when unmounted before stageLarkAttachment finishes", async () => {
    let resolveStage!: (a: LarkAttachment) => void;
    stageMock.mockImplementationOnce(() => new Promise<LarkAttachment>((r) => { resolveStage = r; }));
    const file = new File(["img"], "late.png", { type: "image/png" });
    const att: LarkAttachment = { id: "att-late", data: file, kind: "image", name: "late.png", mime: "image/png", size: 3, url: "blob:test/late" };

    await act(async () => root.render(<StrictMode><LarkComposer onSend={vi.fn()} attachments /></StrictMode>));
    await pick(file);

    await act(async () => root.unmount());
    await act(async () => { resolveStage(att); });
    await flush();
    expect(releaseMock).toHaveBeenCalledWith([att]);
  });

  it("clears saved draft when in-flight send succeeds after unmount and revokes previews when it fails after unmount", async () => {
    let finishOk!: (v: boolean) => void;
    const onSendOk = vi.fn(() => new Promise<boolean>((r) => { finishOk = r; }));
    await act(async () => root.render(<LarkComposer onSend={onSendOk} draft="chat:1" />));
    await type("send before switch");
    await act(async () => { sendButton().click(); });
    await act(async () => root.unmount());
    expect(loadDraft("chat:1").text).toBe("send before switch");
    await act(async () => { finishOk(true); });
    await flush();
    expect(loadDraft("chat:1").text).toBe("");

    // Failed send after unmount must revoke the un-handed-off attachment preview.
    root = createRoot(host);
    let finishFail!: (v: boolean) => void;
    const onSendFail = vi.fn(() => new Promise<boolean>((r) => { finishFail = r; }));
    const file = new File(["x"], "fail.png", { type: "image/png" });
    const att: LarkAttachment = { id: "att-fail", data: file, kind: "image", name: "fail.png", mime: "image/png", size: 1, url: "blob:test/fail" };
    stageMock.mockResolvedValueOnce(att);
    await act(async () => root.render(<LarkComposer onSend={onSendFail} attachments />));
    await pick(file);
    await flush();
    await act(async () => { sendButton().click(); });
    await act(async () => root.unmount());
    expect(releaseMock).not.toHaveBeenCalled();
    await act(async () => { finishFail(false); });
    await flush();
    expect(releaseMock).toHaveBeenCalledWith([att]);
  });

  it("ignores macOS Safari/WKWebView compositionend-to-Enter tail and sends after tail expires", async () => {
    let now = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    const onSend = vi.fn(async () => true);
    await act(async () => root.render(<LarkComposer onSend={onSend} />));
    await type("你好");

    await act(async () => {
      box().dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
      box().dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
      // Safari/WKWebView delivers Enter with isComposing=false, keyCode=13 right after compositionend.
      now += 15;
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, isComposing: false, bubbles: true, cancelable: true }));
    });
    expect(onSend).not.toHaveBeenCalled();

    // Once past the 80ms composition tail, Enter sends normally.
    now += 100;
    await act(async () => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, isComposing: false, bubbles: true, cancelable: true }));
    });
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend).toHaveBeenCalledWith("你好", [], undefined, []);
  });

  it("handles Shift+Enter, Alt+Enter, Chrome isComposing/229, and blur recovery cleanly", async () => {
    const onSend = vi.fn(async () => true);
    await act(async () => root.render(<LarkComposer onSend={onSend} />));
    await type("line one");

    await act(async () => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true, cancelable: true }));
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", altKey: true, bubbles: true, cancelable: true }));
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", isComposing: true, bubbles: true, cancelable: true }));
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 229, bubbles: true, cancelable: true }));
      // Composition started and then blurred without compositionend: blur must reset active IME state.
      box().focus();
      box().dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
      box().blur();
    });
    expect(onSend).not.toHaveBeenCalled();

    await act(async () => {
      box().focus();
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it("catches rejected onSend cleanly, keeps draft for retry, clears sent replyTo, and avoids wiping newer stored drafts after unmount", async () => {
    const onSend = vi.fn<() => Promise<boolean>>()
      .mockRejectedValueOnce(new Error("network down"))
      .mockResolvedValueOnce(true);
    const reply = { id: "om_q1", sender_name: "Alice", text: "quoted" };
    await act(async () => root.render(<LarkComposer onSend={onSend} draft="chat:reply" replyTo={reply} />));
    await type("retry me");

    // 1. Rejected onSend must not produce unhandledrejection, shows toast, keeps draft, and unlocks for retry.
    await act(async () => { sendButton().click(); });
    await flush();
    expect(toasts).toContain("network down");
    expect(box().value).toBe("retry me");
    expect(sendButton().disabled).toBe(false);

    // 2. Retry succeeds and unmounting immediately afterwards must not write the sent replyTo back to localStorage.
    await act(async () => { sendButton().click(); });
    await flush();
    await act(async () => root.unmount());
    expect(loadDraft("chat:reply")).toEqual({ text: "", replyTo: undefined, mentions: undefined });

    // 3. Delayed send resolving after unmount must NOT clear a newer draft typed after returning to the chat.
    root = createRoot(host);
    let finishDelayed!: (v: boolean) => void;
    const delayedSend = vi.fn(() => new Promise<boolean>((r) => { finishDelayed = r; }));
    await act(async () => root.render(<LarkComposer onSend={delayedSend} draft="chat:reopen" />));
    await type("old message");
    await act(async () => { sendButton().click(); });
    await act(async () => root.unmount());
    // Simulate user re-entering the chat and saving a newer draft before old send resolves.
    saveDraft("chat:reopen", { text: "brand new draft", mentions: [] });
    await act(async () => { finishDelayed(true); });
    await flush();
    expect(loadDraft("chat:reopen").text).toBe("brand new draft");
  });
});
