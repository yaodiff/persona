// @vitest-environment jsdom
// Audit item 16: "Edit" on a drafted reply opens the dialog with the caret at the end of the words, so typing goes
// straight in; while the AI still drafts, once the words are in. A touch screen alone keeps the focus on the dialog
// (ui/atoms Dialog), so a phone's keyboard does not come up over the draft.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const pointer = vi.hoisted(() => ({ fine: true }));
vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    get matches() { return media === "(any-pointer: fine)" ? pointer.fine : false; },
    media, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
});
const request = vi.hoisted(() => vi.fn<(path: string, init?: unknown) => Promise<unknown>>());
vi.mock("../../lib/api", async (original) => ({ ...await original<typeof import("../../lib/api")>(), api: request }));

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { DraftDialog } from "./InboxDialogs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "mail:demo:thread:1", source: "mail", kind: "mail", account: "demo", account_name: "Example mail",
  title: "Example thread", preview: "Synthetic message", count: 1, agent: { state: "drafted", draft: "Generated reply" }, ...patch,
});

let host: HTMLDivElement;
let root: Root;
const area = () => document.querySelector<HTMLTextAreaElement>("[role=dialog] textarea")!;

beforeEach(() => {
  pointer.fine = true;
  resetInboxForTests();
  request.mockReset().mockImplementation(async (path) => { throw new Error(`Unexpected request: ${path}`); });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetInboxForTests();
});

describe("the draft dialog's focus (audit item 16)", () => {
  it("puts the caret at the end of the AI's draft as it opens", async () => {
    await act(async () => root.render(<DraftDialog item={item()} onClose={() => {}} />));
    expect(document.activeElement).toBe(area());
    expect(area().selectionStart).toBe("Generated reply".length);
    expect(area().selectionEnd).toBe("Generated reply".length);
  });

  it("waits for a draft still being written, then takes the focus once", async () => {
    let answer!: (v: unknown) => void;
    request.mockImplementation(() => new Promise((yes) => { answer = yes; }));
    await act(async () => root.render(<DraftDialog item={item({ agent: undefined })} onClose={() => {}} />));
    expect(area().disabled).toBe(true);
    expect(document.activeElement?.getAttribute("role")).toBe("dialog");
    await act(async () => answer({ text: "Fresh draft" }));
    expect(area().disabled).toBe(false);
    expect(document.activeElement).toBe(area());
    expect(area().selectionStart).toBe("Fresh draft".length);
  });

  it("leaves the instruction field alone when it was typed into while drafting", async () => {
    let answer!: (v: unknown) => void;
    request.mockImplementation(() => new Promise((yes) => { answer = yes; }));
    await act(async () => root.render(<DraftDialog item={item({ agent: undefined })} onClose={() => {}} />));
    const instruction = document.querySelector<HTMLInputElement>("[role=dialog] input")!;
    act(() => instruction.focus());
    await act(async () => answer({ text: "Fresh draft" }));
    expect(document.activeElement).toBe(instruction);
  });

  it("keeps the focus on the dialog on a touch screen, so no keyboard comes up", async () => {
    pointer.fine = false;
    await act(async () => root.render(<DraftDialog item={item()} onClose={() => {}} />));
    expect(document.activeElement?.getAttribute("role")).toBe("dialog");
  });
});
