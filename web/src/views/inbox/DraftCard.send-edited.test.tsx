// @vitest-environment jsdom
// The AI twin's draft card sends what it shows (#2793): an edit the draft dialog kept (closed without Cancel) and the
// proposed changes left ticked there, marked "Edited"; Cancel puts the card back to the AI's draft.
import { registerTestMailbox } from "../../test/mailbox-fixture";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((media: string) => ({ matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const listed = vi.hoisted(() => ({ items: [] as unknown[] }));
const replies = vi.hoisted(() => [] as { path: string; json: unknown }[]);
const hub = vi.hoisted(() => ({ refuse: false }));
vi.mock("../../lib/api", async (original) => ({
  ...await original<typeof import("../../lib/api")>(),
  api: async (path: string, init?: { json?: unknown }) => {
    if (path === "/mail/accounts") return { accounts: [{ id: "a1", email: "me@example.com" }] };
    if (path.startsWith("/inbox?")) return { items: listed.items };
    if (path.endsWith("/reply")) {
      replies.push({ path, json: init?.json });
      if (hub.refuse) throw new Error("Synthetic refusal");
      return {};
    }
    return {};
  },
}));
// the store's first snapshot is in (the list waits for it before drawing its rows)
vi.mock("../../lib/store", async (original) => {
  const m = await original<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));

import { t } from "../../i18n";
import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { InboxList } from "./InboxList";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const AI = "Synthetic AI reply";
const mail: InboxItem = {
  id: "mail:a1:thread:1", source: "mail", kind: "mail", account: "a1", account_name: "Example mail", title: "Example thread",
  preview: "Synthetic question", count: 1, updated_at: "2026-11-09T08:00:00Z", agent: { state: "drafted", draft: AI },
};
const status = (value: string) => [{ field: "f1", name: "Status", value, options: [{ name: "Doing" }, { name: "Done" }] }];
const doc: InboxItem = {
  id: "doc:d1:comment:1", source: "lark_doc", kind: "doc", account: "lark", account_name: "Lark", title: "Example plan",
  preview: "Someone commented", count: 1, updated_at: "2026-11-09T08:00:00Z",
  doc: { type: "docx", notice: "comment", rows: [{ ref: "r1", title: "Row one", status: status("Doing") }, { ref: "r2", title: "Row two", status: status("Doing") }] },
  agent: { state: "drafted", draft: AI, changes: [{ ref: "r1", field: "f1", value: "Done" }, { ref: "r2", field: "f1", value: "Done" }] },
} as InboxItem;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const card = () => host.querySelector("[data-draft-text]")!.parentElement!;
const cardButton = (label: string) => [...card().querySelectorAll("button")].find((b) => b.textContent === label)!;
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const area = () => dialog()!.querySelector("textarea")!;
const type = (text: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area(), text);
  area().dispatchEvent(new Event("input", { bubbles: true }));
});
const escape = () => act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
const show = async (it: InboxItem, text = AI) => {
  listed.items = [it];
  act(() => root.render(<InboxList />));
  await flush();
  expect(card().querySelector("[data-draft-text]")!.textContent).toBe(text);
};
const edit = async () => {
  await act(async () => cardButton(t("inbox-inboxparts.edit")).click());
  expect(dialog()).not.toBeNull();
};

beforeEach(() => {
  resetInboxForTests();
  resetMailForTests();
  replies.length = 0;
  hub.refuse = false;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetInboxForTests();
});

registerTestMailbox();

describe("the draft card after the draft dialog", () => {
  it("shows the edit kept by closing the dialog, marked Edited, and its Send sends that text", async () => {
    await show(mail);
    await edit();
    type(`${AI} (edited)`);
    await escape();
    expect(dialog()).toBeNull();
    expect(card().querySelector("[data-draft-text]")!.textContent).toBe(`${AI} (edited)`);
    expect(card().querySelector("[data-draft-edited]")?.textContent).toBe(t("inbox-inboxparts.draft_edited"));
    await act(async () => cardButton(t("inbox-inboxparts.send")).click());
    await flush();
    expect(replies).toEqual([{ path: "/inbox/mail:a1:thread:1/reply", json: { text: `${AI} (edited)`, changes: undefined } }]);
  });

  it("goes back to the AI's draft after Cancel", async () => {
    await show(mail);
    await edit();
    type("Discarded words");
    await act(async () => [...dialog()!.querySelectorAll("button")].find((b) => b.textContent === t("common.cancel"))!.click());
    expect(card().querySelector("[data-draft-text]")!.textContent).toBe(AI);
    expect(card().querySelector("[data-draft-edited]")).toBeNull();
  });

  it("lists and sends only the proposed changes left ticked when the dialog closed", async () => {
    await show(doc);
    expect(card().textContent).toContain("Row two");
    await edit();
    const boxes = dialog()!.querySelectorAll<HTMLInputElement>("input[type=checkbox]");
    expect(boxes).toHaveLength(2);
    await act(async () => boxes[1].click());
    await escape();
    expect(card().textContent).not.toContain("Row two");
    expect(card().querySelector("[data-draft-edited]")).not.toBeNull();
    await act(async () => cardButton(t("inbox-inboxparts.send")).click());
    await flush();
    expect(replies.map((r) => r.json)).toEqual([{ text: AI, changes: [{ ref: "r1", field: "f1", value: "Done" }] }]);
  });

  it("is not marked Edited after a failed Send of the AI's own draft, which is sent trimmed", async () => {
    await show({ ...mail, agent: { state: "drafted", draft: `${AI}\n` } }, `${AI}\n`);
    hub.refuse = true;
    await act(async () => cardButton(t("inbox-inboxparts.send")).click());
    await flush();
    expect(replies.map((r) => r.json)).toEqual([{ text: AI, changes: undefined }]);
    expect(card().querySelector("[data-draft-edited]")).toBeNull();
  });
});
