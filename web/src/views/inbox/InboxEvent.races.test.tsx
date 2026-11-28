// @vitest-environment jsdom
// Regression tests for InboxEvent active-edit preservation, Safari/WKWebView IME guards,
// double-submit locks, StrictMode mount safety, and cross-manager/suggestion keying.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { StrictMode, act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});

type RequestOptions = { method?: string; json?: unknown; headers?: unknown };

const calls = vi.hoisted(() => ({
  list: [] as ({ path: string } & RequestOptions)[],
  suggestion: undefined as (() => Promise<unknown>) | undefined,
  retry: undefined as (() => Promise<unknown>) | undefined,
}));

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: RequestOptions) => {
    calls.list.push({ path, method: init?.method, json: init?.json, headers: init?.headers });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: [] });
    if (path.endsWith("/suggestion")) return calls.suggestion?.() ?? Promise.resolve({});
    if (path.endsWith("/retry")) return calls.retry?.() ?? Promise.resolve({});
    return Promise.resolve({});
  },
}));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined, translatesChat: () => false, translatesMail: () => false, languageName: () => "" }));

import type { InboxItem } from "../../lib/inbox";
import { resetInboxForTests } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { getState } from "../../lib/store";
import { settleRosterDefault } from "../../lib/manager-choice";
import { COMPOSITION_TAIL_MS } from "../../lib/ime";
import { suggestionButtons } from "../../lib/inbox-events";
import { t } from "../../i18n";
import { EventBody, SuggestionCard } from "./InboxEvent";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const fixture = JSON.parse(readFileSync(join(__dirname, "../../../../scripts/mock/inbox_events.json"), "utf8")) as { items: InboxItem[] };
const byId = (id: string) => fixture.items.find((x) => x.id === id)!;
const open = { ...byId("lark:oc_demo3:300") };
const failed = { ...byId("lark:oc_demo6:600") };
const withSuggestion = { ...byId("lark:oc_demo1:100") };

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

const setInputValue = async (el: HTMLInputElement | HTMLTextAreaElement, value: string) => {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setValue = Object.getOwnPropertyDescriptor(proto, "value")!.set!;
  await act(async () => {
    setValue.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const pressButton = async (label: string) => {
  const btn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
  if (!btn) throw new Error(`missing button ${label}`);
  await act(async () => btn.click());
};

const sentSuggestions = () => calls.list.filter((c) => c.path.endsWith("/suggestion"));
const sentRetries = () => calls.list.filter((c) => c.path.endsWith("/retry"));

beforeEach(() => {
  resetInboxForTests();
  resetMailForTests();
  Object.assign(getState(), { sessions: {} });
  calls.list = [];
  calls.suggestion = undefined;
  calls.retry = undefined;
  host = document.createElement("div");
  document.body.replaceChildren(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("InboxEvent edit preservation, IME, StrictMode, keying, and double-submit guards", () => {
  it("preserves in-progress TextSuggestion edits across WebSocket suggestion text pushes", async () => {
    const base: InboxItem = {
      ...withSuggestion,
      suggestion: { version: 2, kind: "reply", text: "Initial server text", at: "2026-01-01T09:00:00Z" },
    };
    await act(async () => root.render(<EventBody item={base} onSource={() => {}} />));
    await pressButton(t("inbox-inboxevent.edit"));
    const editor = host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")!;
    expect(editor.value).toBe("Initial server text");

    await setInputValue(editor, "My unsaved draft edit");
    expect(editor.value).toBe("My unsaved draft edit");

    // Simulate WebSocket push updating the same suggestion version's text while editing.
    const pushed: InboxItem = {
      ...base,
      suggestion: { ...base.suggestion!, text: "Server re-pushed text" },
    };
    await act(async () => root.render(<EventBody item={pushed} onSource={() => {}} />));
    expect(host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")?.value).toBe("My unsaved draft edit");

    // Canceling restores the latest server text.
    await pressButton(t("common.cancel"));
    expect(host.querySelector("[data-suggestion-text]")?.textContent).toBe("Server re-pushed text");
    await pressButton(t("inbox-inboxevent.edit"));
    expect(host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")?.value).toBe("Server re-pushed text");
  });

  it("ignores Safari/WKWebView compositionend->Enter(13) and keyCode 229, then submits after tail in HandlingLine under StrictMode", async () => {
    let now = 1_000;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    let resolveReq!: (v: unknown) => void;
    calls.suggestion = () => new Promise((resolve) => { resolveReq = resolve; });

    await act(async () =>
      root.render(
        <StrictMode>
          <EventBody item={open} onSource={() => {}} />
        </StrictMode>,
      ),
    );
    await pressButton(t("inbox-inboxdetail.say_something"));
    const input = host.querySelector<HTMLInputElement>("[data-event-say-input]")!;
    await setInputValue(input, "Check timeline");

    // Safari IME keyCode 229 Enter must not submit.
    await act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 229, bubbles: true }));
    });
    expect(sentSuggestions()).toHaveLength(0);

    // macOS Safari/WKWebView fires compositionend right before keydown(Enter, keyCode: 13).
    await act(async () => {
      input.dispatchEvent(new Event("compositionstart", { bubbles: true }));
      input.dispatchEvent(new Event("compositionend", { bubbles: true }));
      now += COMPOSITION_TAIL_MS - 10;
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true }));
    });
    expect(sentSuggestions()).toHaveLength(0);

    // After the composition tail passes, rapid double Enter submits exactly once and disables the input.
    now += 20;
    await act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true }));
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true }));
    });
    expect(sentSuggestions()).toHaveLength(1);
    expect(input.disabled).toBe(true);

    resolveReq({ ...open, handling: { owner: "manager", state: "looking" } });
    await flush();
    // StrictMode double-mount must leave mountedRef true so state closes cleanly on success.
    expect(host.querySelector("[data-event-say]")).toBeNull();
  });

  it("prevents synchronous double-click retry in HandlingLine", async () => {
    let resolveRetry!: (v: unknown) => void;
    calls.retry = () => new Promise((resolve) => { resolveRetry = resolve; });

    await act(async () => root.render(<EventBody item={failed} onSource={() => {}} />));
    const retryBtn = host.querySelector<HTMLButtonElement>('[data-testid="inbox-retry"]')!;
    await act(async () => {
      retryBtn.click();
      retryBtn.click();
    });
    expect(sentRetries()).toHaveLength(1);

    resolveRetry({ ...failed, handling: { owner: "manager", state: "looking" } });
    await flush();
  });

  it("guards Safari compositionend->Enter(13), disables editor while busy, preserves draft on confirm failure, and unlocks retry", async () => {
    let now = 2_000;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    let rejectReq!: (e: Error) => void;
    calls.suggestion = () => new Promise((_, reject) => { rejectReq = reject; });

    const item: InboxItem = {
      ...withSuggestion,
      suggestion: { version: 4, kind: "agent", text: "Draft agent plan", at: "2026-01-01T09:00:00Z" },
    };
    await act(async () =>
      root.render(
        <StrictMode>
          <EventBody item={item} onSource={() => {}} />
        </StrictMode>,
      ),
    );
    await pressButton(t("inbox-inboxevent.edit"));
    const editor = host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")!;
    const sayInput = host.querySelector<HTMLInputElement>("[data-suggestion-say]")!;
    await setInputValue(editor, "Edited agent plan");
    await setInputValue(sayInput, "Keep it brief");

    // Suppress both keyCode 229 and macOS Safari compositionend -> Enter(13) tail.
    await act(async () => {
      sayInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 229, bubbles: true }));
      sayInput.dispatchEvent(new Event("compositionstart", { bubbles: true }));
      sayInput.dispatchEvent(new Event("compositionend", { bubbles: true }));
      now += COMPOSITION_TAIL_MS - 10;
      sayInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true }));
    });
    expect(sentSuggestions()).toHaveLength(0);

    // Confirm double-click fires once, disables both textarea and say input while in flight.
    const confirmLabel = suggestionButtons(item.suggestion!).confirm;
    const confirmBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === confirmLabel)!;
    await act(async () => {
      confirmBtn.click();
      confirmBtn.click();
    });
    expect(sentSuggestions()).toHaveLength(1);
    expect(editor.disabled).toBe(true);
    expect(sayInput.disabled).toBe(true);

    // Failure keeps edit mode, edited text, and say draft intact, and re-enables for retry.
    rejectReq(new Error("offline"));
    await flush();
    expect(host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")?.value).toBe("Edited agent plan");
    expect(host.querySelector<HTMLInputElement>("[data-suggestion-say]")?.value).toBe("Keep it brief");
    expect(host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")?.disabled).toBe(false);
    expect(host.querySelector<HTMLInputElement>("[data-suggestion-say]")?.disabled).toBe(false);

    // Post-tail Enter submits instruct once and closes editor cleanly on success.
    let resolveInstruct!: (v: unknown) => void;
    calls.suggestion = () => new Promise((resolve) => { resolveInstruct = resolve; });
    now += 20;
    await act(async () => {
      sayInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true }));
      sayInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true }));
    });
    expect(sentSuggestions()).toHaveLength(2);
    resolveInstruct({ ...item, handling: { owner: "manager", state: "looking" } });
    await flush();
    expect(host.querySelector("[data-suggestion-editor]")).toBeNull();
  });

  it("resets edit and say state when switching managers on the same item id or direct SuggestionCard props", async () => {
    const m1: InboxItem = {
      ...withSuggestion,
      manager: "m-one",
      suggestion: { version: 2, kind: "reply", text: "Manager one draft", at: "2026-01-01T09:00:00Z" },
    };
    const m2: InboxItem = {
      ...withSuggestion,
      manager: "m-two",
      suggestion: { version: 2, kind: "reply", text: "Manager two draft", at: "2026-01-01T09:00:00Z" },
    };
    await act(async () => root.render(<EventBody item={m1} onSource={() => {}} />));
    await pressButton(t("inbox-inboxevent.edit"));
    await setInputValue(host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")!, "Unsaved for m-one");
    await act(async () => root.render(<EventBody item={m2} onSource={() => {}} />));
    expect(host.querySelector("[data-suggestion-editor]")).toBeNull();
    expect(host.querySelector("[data-suggestion-text]")?.textContent).toBe("Manager two draft");

    // Direct SuggestionCard callers without an outer key also reset across manager/version switches.
    await act(async () => root.render(<SuggestionCard item={m1} suggestion={m1.suggestion!} />));
    await pressButton(t("inbox-inboxevent.edit"));
    await setInputValue(host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")!, "Direct draft");
    await act(async () => root.render(<SuggestionCard item={m2} suggestion={m2.suggestion!} />));
    expect(host.querySelector("[data-suggestion-editor]")).toBeNull();
    expect(host.querySelector("[data-suggestion-text]")?.textContent).toBe("Manager two draft");

    // HandlingLine also resets open say draft when switching managers on the same item id.
    await act(async () => root.render(<EventBody item={{ ...open, manager: "m-one" }} onSource={() => {}} />));
    await pressButton(t("inbox-inboxdetail.say_something"));
    await setInputValue(host.querySelector<HTMLInputElement>("[data-event-say-input]")!, "Note for m-one");
    await act(async () => root.render(<EventBody item={{ ...open, manager: "m-two" }} onSource={() => {}} />));
    expect(host.querySelector("[data-event-say]")).toBeNull();
  });

  it("prevents synchronous double-click confirm in TodoSuggestion", async () => {
    let resolveReq!: (v: unknown) => void;
    calls.suggestion = () => new Promise((resolve) => { resolveReq = resolve; });

    const todoItem: InboxItem = {
      ...withSuggestion,
      suggestion: { version: 1, kind: "todo", text: "Ship Wave 6", at: "2026-01-01T09:00:00Z" },
    };
    await act(async () => root.render(<EventBody item={todoItem} onSource={() => {}} />));
    const addBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("lib-inbox-events.add_todo"))!;
    await act(async () => {
      addBtn.click();
      addBtn.click();
    });
    expect(sentSuggestions()).toHaveLength(1);

    resolveReq({ ...todoItem, done_at: "2026-01-01T10:00:00Z", done_by: "user" });
    await flush();
  });
});
