// @vitest-environment jsdom
// ↑ in an empty composer edits the last message (#4503): the box gets it with an "Editing" bar, Esc or × cancels with
// nothing asked of anyone, a send commits (ChatView's rewind) before it goes, and the #2434 history stays reachable.
import { act, useMemo, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { addQuote, getQuotes, sendDraft, setQuotes } from "../../lib/drafts";
import { setComposerImages } from "../../lib/composer-images";
import { recordSent } from "../../lib/send-history";
import { Composer, type Outgoing } from "./Composer";
import { t } from "../../i18n";

vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const KEY = "hub/edit";
// the last prompt, sent with a quote: the quote goes back to the bar, the words into the box
const LAST = "> Quoted text\n\nRevise this paragraph";

let host: HTMLDivElement;
let root: Root;
const commit = vi.fn<() => Promise<boolean>>();
const onSend = vi.fn<(m: Outgoing) => Promise<boolean>>();
const started = vi.fn();
const cancelled = vi.fn();
const calls: string[] = [];

/** ChatView's side: Edit last message on offer, the edit it starts, its commit and cancel. */
function Harness(props: Partial<React.ComponentProps<typeof Composer>> & { offered?: boolean }) {
  const { offered = true, ...rest } = props;
  const [editing, setEditing] = useState(false);
  const start = () => {
    started();
    setEditing(true);
    sendDraft(KEY, { type: "replace", text: LAST, edit: true });
  };
  const edit = useMemo(
    () => (editing ? {
      commit: async () => {
        calls.push("commit");
        const ok = await commit();
        if (ok) setEditing(false);
        return ok;
      },
      cancel: () => {
        cancelled();
        setEditing(false);
      },
    } : undefined),
    [editing],
  );
  return <Composer draftKey={KEY} placeholder="" onSend={async (m) => { calls.push("send"); return onSend(m); }} onEditLastMessage={offered ? start : undefined} editingLast={edit} {...rest} />;
}

const mount = (props: React.ComponentProps<typeof Harness> = {}) => act(() => root.render(<Harness {...props} />));
const box = () => host.querySelector("textarea")!;
const bar = () => host.querySelector('[data-testid="composer-editing"]');
const press = (k: string, extra: KeyboardEventInit = {}) => act(() => void box().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...extra })));
const typeInto = (text: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), text);
    box().dispatchEvent(new Event("input", { bubbles: true }));
  });
const sendButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.send")}"]`)!;
const cancelButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("rewind.cancel_editing")}"]`);

beforeEach(() => {
  localStorage.clear();
  setQuotes(KEY, []);
  commit.mockReset().mockResolvedValue(true);
  onSend.mockReset().mockResolvedValue(true);
  started.mockReset();
  cancelled.mockReset();
  calls.length = 0;
  // a precise pointer: the #2434 history is on (it is off on touch screens)
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("fine"), addEventListener() {}, removeEventListener() {} }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  setComposerImages(KEY, []);
  host.remove();
  vi.unstubAllGlobals();
});

describe("↑ in an empty box", () => {
  it("starts editing the last message: its words and quote come back, a bar says so, nothing is committed or sent", () => {
    mount();
    press("ArrowUp");
    expect(started).toHaveBeenCalledTimes(1);
    expect(box().value).toBe("Revise this paragraph");
    expect(getQuotes(KEY).list.map((q) => q.text)).toEqual(["Quoted text"]);
    expect(bar()?.textContent).toContain(t("rewind.editing"));
    expect(commit).not.toHaveBeenCalled();
    expect(onSend).not.toHaveBeenCalled();
  });

  it("recalls the sent history as before where Edit last message is not on offer", () => {
    recordSent(KEY, "sent earlier", localStorage, 1);
    mount({ offered: false });
    press("ArrowUp");
    expect(started).not.toHaveBeenCalled();
    expect(box().value).toBe("sent earlier");
    expect(bar()).toBeNull();
  });

  it.each([
    ["working", { working: true }],
    ["waiting on a card", { needsYou: true }],
    ["disabled", { disabled: true }],
    ["blocked", { blocked: { label: "Not ready" } }],
    ["a rewind running", { loadingDraft: true }],
  ] as const)("is the history's while the session is %s", (_, props) => {
    mount(props);
    press("ArrowUp");
    expect(started).not.toHaveBeenCalled();
    expect(bar()).toBeNull();
  });

  it("keeps its old job while the box holds anything", () => {
    mount();
    typeInto("half typed");
    press("ArrowUp");
    typeInto(" ");
    press("ArrowUp");
    typeInto("");
    act(() => void addQuote(KEY, { text: "a quote" }));
    press("ArrowUp");
    expect(started).not.toHaveBeenCalled();
    act(() => setQuotes(KEY, []));
    act(() => void sendDraft(KEY, { type: "image", image: { name: "a.png", mime: "image/png", data: btoa("png") } }));
    press("ArrowUp");
    expect(started).not.toHaveBeenCalled();
    expect(bar()).toBeNull();
  });

  it.each([
    ["Shift", { shiftKey: true }], ["Ctrl", { ctrlKey: true }], ["Alt", { altKey: true }], ["⌘", { metaKey: true }],
    ["a held-down repeat", { repeat: true }], ["an input method composing", { isComposing: true }], ["keyCode 229", { keyCode: 229 }],
  ] as const)("is not taken with %s", (_, extra) => {
    mount();
    press("ArrowUp", extra);
    expect(started).not.toHaveBeenCalled();
    expect(box().value).toBe("");
  });

  it("is not taken while the Ctrl+R search or a dialog is open", async () => {
    mount();
    // The search chunk loads on intent; keep the composer mounted while React resolves it.
    await act(async () => { box().dispatchEvent(new KeyboardEvent("keydown", { key: "r", ctrlKey: true, bubbles: true, cancelable: true })); });
    expect(host.querySelector('[role="listbox"], [role="dialog"], input')).not.toBeNull();
    press("ArrowUp");
    expect(started).not.toHaveBeenCalled();
    act(() => root.unmount());
    root = createRoot(host);
    const dialog = document.body.appendChild(Object.assign(document.createElement("div"), { role: "dialog" }));
    dialog.setAttribute("role", "dialog");
    try {
      mount();
      press("ArrowUp");
      expect(started).not.toHaveBeenCalled();
    } finally {
      dialog.remove();
    }
  });
});

describe("while editing", () => {
  it("Esc cancels: an empty box, the quote gone, nothing committed or sent, and the Esc stops there", () => {
    // ChatView interrupts a working turn on an Esc that reaches the window
    const onWindow = vi.fn((e: KeyboardEvent) => e.key);
    const escapes = () => onWindow.mock.results.filter((r) => r.value === "Escape");
    addEventListener("keydown", onWindow);
    try {
      mount();
      press("ArrowUp");
      press("Escape");
      expect(cancelled).toHaveBeenCalledTimes(1);
      expect(box().value).toBe("");
      expect(getQuotes(KEY).list).toEqual([]);
      expect(bar()).toBeNull();
      expect(commit).not.toHaveBeenCalled();
      expect(onSend).not.toHaveBeenCalled();
      expect(escapes()).toHaveLength(0);
    } finally {
      removeEventListener("keydown", onWindow);
    }
  });

  it("× cancels the same way", () => {
    mount();
    press("ArrowUp");
    act(() => cancelButton()!.click());
    expect(cancelled).toHaveBeenCalledTimes(1);
    expect(box().value).toBe("");
    expect(getQuotes(KEY).list).toEqual([]);
  });

  it("a send commits first, then sends the edited words with the quote", async () => {
    mount();
    press("ArrowUp");
    typeInto("Revised paragraph");
    await act(async () => sendButton().click());
    expect(calls).toEqual(["commit", "send"]);
    expect(onSend.mock.calls[0][0].text).toBe("> Quoted text\n\nRevised paragraph");
    // Restoring the old quote for an edit is not a new quote reply: its existing follow behavior stays unchanged (#10346).
    expect(onSend.mock.calls[0][0].quoteReply).toBeUndefined();
    expect(box().value).toBe("");
    expect(bar()).toBeNull();
  });

  it("a refused commit sends nothing and keeps the edit in the box", async () => {
    commit.mockResolvedValue(false);
    mount();
    press("ArrowUp");
    typeInto("Revised paragraph");
    await act(async () => sendButton().click());
    expect(commit).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
    expect(box().value).toBe("Revised paragraph");
    expect(getQuotes(KEY).list.map((q) => q.text)).toEqual(["Quoted text"]);
    expect(bar()).not.toBeNull();
  });

  it("↑ on the untouched edit goes on to the older sent messages and ends the edit", () => {
    recordSent(KEY, "older message", localStorage, 1);
    recordSent(KEY, "Revise this paragraph", localStorage, 2);
    mount();
    press("ArrowUp");
    expect(bar()).not.toBeNull();
    press("ArrowUp");
    expect(box().value).toBe("older message");
    expect(bar()).toBeNull();
    expect(cancelled).toHaveBeenCalledTimes(1);
    expect(getQuotes(KEY).list).toEqual([]);
    // and ↓ walks back as #2434 does: the newest sent message, then an empty box
    press("ArrowDown");
    expect(box().value).toBe("Revise this paragraph");
    press("ArrowDown");
    expect(box().value).toBe("");
  });

  it("↓ on the untouched edit goes back to an empty box, even with nothing sent from here", () => {
    mount();
    press("ArrowUp");
    press("ArrowDown");
    expect(box().value).toBe("");
    expect(bar()).toBeNull();
    expect(cancelled).toHaveBeenCalledTimes(1);
  });

  it("an edit with no older message stays an edit on ↑", () => {
    recordSent(KEY, "Revise this paragraph", localStorage, 1);
    mount();
    press("ArrowUp");
    press("ArrowUp");
    expect(box().value).toBe("Revise this paragraph");
    expect(bar()).not.toBeNull();
    expect(cancelled).not.toHaveBeenCalled();
  });

  it("once changed, the arrows are the caret's", () => {
    recordSent(KEY, "older message", localStorage, 1);
    mount();
    press("ArrowUp");
    typeInto("Revise this paragraph, shorter");
    press("ArrowUp");
    press("ArrowDown");
    expect(box().value).toBe("Revise this paragraph, shorter");
    expect(bar()).not.toBeNull();
  });

  it("other words put in the box (Edit as new) end the edit", () => {
    mount();
    press("ArrowUp");
    act(() => void sendDraft(KEY, { type: "replace", text: "Another message" }));
    expect(cancelled).toHaveBeenCalledTimes(1);
    expect(box().value).toBe("Another message");
    expect(bar()).toBeNull();
  });
});
