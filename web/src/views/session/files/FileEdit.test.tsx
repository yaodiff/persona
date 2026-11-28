// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// CodeMirror needs layout jsdom does not have: a plain text area stands in for it, with the same props.
const typed: { value: string | null } = vi.hoisted(() => ({ value: null }));
// A lazy chunk can take a later task even when the editor itself is mocked.
vi.mock("./CodeEditor", async () => {
  await new Promise((resolve) => setTimeout(resolve, 250));
  return {
    default: ({ text, onChange, bind }: { text: string; onChange: () => void; bind: (r: () => string) => void }) => {
      bind(() => typed.value ?? text);
      return <textarea data-testid="code-editor" defaultValue={text} onChange={(e) => { typed.value = e.target.value; onChange(); }} />;
    },
  };
});
import { setToken } from "../../../lib/api";
import { t } from "../../../i18n";
import type { FileContent } from "../../../lib/files";
import { copyName, editProblem, editText, FileEdit, fileText, MAX_EDIT_BYTES, sha256Hex } from "./FileEdit";

// Editing a text file on the web (#5829): what can be edited, line endings kept, the save over the version opened only,
// and the choices when the file changed on the computer.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const file = (over: Partial<FileContent> = {}): FileContent => ({ path: "/w/a.txt", name: "a.txt", size: 6, text: "a\r\nb\r\n", truncated: false, is_dir: false, revision: "r1", ...over });

describe("edit helpers", () => {
  it("says what can't be edited", () => {
    expect(editProblem(file(), true)).toBeNull();
    expect(editProblem(file({ text: undefined, data: "AA==" }), true)).toBe(t("files-editor.not_text"));
    expect(editProblem(file({ size: MAX_EDIT_BYTES + 1 }), true)).toBe(t("files-editor.too_large"));
    expect(editProblem(file(), false)).toBe(t("files-editor.partial"));
    expect(editProblem(null, true)).toBe(t("files-editor.not_text"));
  });

  it("keeps line endings and the byte order mark", () => {
    const how = editText("﻿a\r\nb\r\n");
    expect(how).toEqual({ body: "a\nb\n", crlf: true, bom: true });
    expect(fileText("a\nb\nc\n", how)).toBe("﻿a\r\nb\r\nc\r\n");
    const mixed = editText("a\r\nb\nc");
    expect(mixed.crlf).toBe(false);
    expect(fileText(mixed.body, mixed)).toBe("a\r\nb\nc");
  });

  it("names copies as Finder does", () => {
    expect(copyName("a.txt", 2)).toBe("a 2.txt");
    expect(copyName("Makefile", 3)).toBe("Makefile 3");
    expect(copyName(".env", 2)).toBe(".env 2");
  });

  it("hashes like the node", async () => {
    expect(await sha256Hex("hello")).toBe("2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824");
  });
});

let host: HTMLDivElement, root: Root, sent: Record<string, unknown>[], answers: ((body: Record<string, unknown>) => Response)[], done: boolean[];
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const appears = (check: () => unknown) => vi.waitFor(async () => {
  await act(async () => {});
  expect(check()).toBeTruthy();
}, { timeout: 3000 });
async function click(label: string) { const b = button(label); expect(b, label).toBeTruthy(); await act(async () => b!.click()); await flush(); }
async function type(text: string) {
  const area = document.querySelector<HTMLTextAreaElement>('[data-testid="code-editor"]')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, text);
    area.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await flush();
}

describe("FileEdit", () => {
  beforeEach(async () => {
    setToken("synthetic-A"); sent = []; answers = []; done = []; typed.value = null;
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body ?? "{}"));
      sent.push(body);
      const answer = answers.shift();
      return answer ? answer(body) : Response.json({ path: body.path, size: 1, revision: "r2" });
    }));
    host = document.createElement("div"); document.body.append(host); root = createRoot(host);
    await act(async () => {
      root.render(<FileEdit mid="m1" cwd="/w" file={file()} text={"a\r\nb\r\n"} onDone={(c) => done.push(c)} />);
      await import("./CodeEditor");
    });
    await appears(() => host.querySelector('[data-testid="code-editor"]'));
  });
  afterEach(() => { act(() => root.unmount()); host.remove(); document.body.innerHTML = ""; setToken(""); vi.unstubAllGlobals(); });

  it("saves over the version opened, with the file's line endings", async () => {
    expect(button(t("files-editor.save"))!.disabled).toBe(true);
    await type("a\nb\nc\n");
    await click(t("files-editor.save"));
    expect(sent).toEqual([{ path: "/w/a.txt", cwd: "/w", text: "a\r\nb\r\nc\r\n", revision: "r1", sha256: await sha256Hex("a\r\nb\r\n") }]);
    expect(done).toEqual([true]);
  });

  it("asks before throwing edits away", async () => {
    await type("x");
    await click(t("files-editor.cancel"));
    expect(document.body.textContent).toContain(t("files-editor.discard_title"));
    await click(t("files-editor.discard"));
    expect(done).toEqual([false]);
    expect(sent).toEqual([]);
  });

  it("does not overwrite a newer version unless asked twice", async () => {
    answers.push(() => Response.json({ error: "changed", code: "file_changed", revision: "r5" }, { status: 409 }));
    await type("x\n");
    await click(t("files-editor.save"));
    await appears(() => document.body.textContent?.includes(t("files-editor.changed_title", { name: "a.txt" })));
    expect(document.body.textContent).toContain(t("files-editor.changed_title", { name: "a.txt" }));
    expect(done).toEqual([]);
    await click(t("files-editor.overwrite_anyway"));
    await appears(() => document.body.textContent?.includes(t("files-editor.overwrite_title")));
    expect(document.body.textContent).toContain(t("files-editor.overwrite_title"));
    await click(t("files-editor.overwrite"));
    await appears(() => done.length === 1);
    expect(sent.at(-1)).toMatchObject({ text: "x\r\n", force: true });
    expect(done).toEqual([true]);
  });

  it("saves a copy under the first free name", async () => {
    answers.push(() => Response.json({ error: "changed", code: "file_changed" }, { status: 409 }));
    answers.push(() => Response.json({ error: "taken", code: "exists", name: "a 2.txt" }, { status: 409 }));
    await type("y\n");
    await click(t("files-editor.save"));
    await appears(() => document.body.textContent?.includes(t("files-editor.changed_title", { name: "a.txt" })));
    await click(t("files-editor.save_copy"));
    await appears(() => done.length === 1);
    expect(sent.slice(1).map((b) => [b.path, b.create])).toEqual([["/w/a 2.txt", true], ["/w/a 3.txt", true]]);
    expect(done).toEqual([true]);
  });

  it("saves a copy beside a Windows computer's file, not in the session's folder (#9968)", async () => {
    await act(async () => root.render(<FileEdit key="windows" mid="m1" cwd={"C:\\Users\\me"} file={file({ path: "C:\\Users\\me\\docs\\a.txt" })} text={"a\r\nb\r\n"} onDone={(c) => done.push(c)} />));
    await appears(() => host.querySelector('[data-testid="code-editor"]'));
    answers.push(() => Response.json({ error: "changed", code: "file_changed" }, { status: 409 }));
    await type("y\n");
    await click(t("files-editor.save"));
    await appears(() => document.body.textContent?.includes(t("files-editor.changed_title", { name: "a.txt" })));
    await click(t("files-editor.save_copy"));
    await appears(() => done.length === 1);
    expect(sent.slice(1).map((b) => [b.path, b.cwd, b.create])).toEqual([["C:\\Users\\me\\docs/a 2.txt", "C:\\Users\\me", true]]);
  });

  it("handles Escape safely: asks to discard when dirty, closes edit when clean, ignores IME/defaultPrevented, and blocks enclosing panel close", async () => {
    const outerKeyDown = vi.fn();
    window.addEventListener("keydown", outerKeyDown);
    const area = () => document.querySelector<HTMLTextAreaElement>('[data-testid="code-editor"]')!;
    const sendEsc = (init: KeyboardEventInit & { keyCode?: number } = {}, prevent = false) => {
      const ev = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true, ...init });
      if (init.keyCode !== undefined) Object.defineProperty(ev, "keyCode", { value: init.keyCode });
      if (prevent) ev.preventDefault();
      act(() => { area().dispatchEvent(ev); });
    };
    try {
      // 1. Dirty editor + IME Escape (isComposing / Safari keyCode 229) or already defaultPrevented: neither discards nor closes.
      await type("unsaved change");
      sendEsc({ isComposing: true });
      sendEsc({ keyCode: 229 });
      sendEsc({}, true);
      expect(document.body.textContent).not.toContain(t("files-editor.discard_title"));
      expect(done).toEqual([]);

      // 2. Dirty editor + real Escape: opens discard confirmation dialog and stops propagation so SidePanel does not unmount.
      outerKeyDown.mockClear();
      sendEsc();
      expect(document.body.textContent).toContain(t("files-editor.discard_title"));
      expect(done).toEqual([]);
      expect(outerKeyDown).not.toHaveBeenCalled();

      // Dismiss discard dialog, keep editing.
      await click(t("files-editor.keep_editing"));
      expect(document.body.textContent).not.toContain(t("files-editor.discard_title"));

      // 2a. External SidePanel Escape delegation (e.g. focus on ResizeHandle) prompts discard when dirty.
      const editRoot = host.querySelector<HTMLElement>('[data-testid="file-edit"]')!;
      expect(editRoot.hasAttribute("data-unsaved-edit")).toBe(true);
      const delegated = new CustomEvent("persona:escape-unsaved", { cancelable: true });
      act(() => { editRoot.dispatchEvent(delegated); });
      expect(delegated.defaultPrevented).toBe(true);
      expect(document.body.textContent).toContain(t("files-editor.discard_title"));
      await click(t("files-editor.keep_editing"));
      expect(document.body.textContent).not.toContain(t("files-editor.discard_title"));

      // 2b. While saving is in flight, Escape consumes the event without opening discard dialog or closing.
      let finishSave!: (r: Response) => void;
      vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((resolve) => { finishSave = resolve; })));
      await act(async () => { button(t("files-editor.save"))!.click(); });
      outerKeyDown.mockClear();
      sendEsc();
      expect(document.body.textContent).not.toContain(t("files-editor.discard_title"));
      expect(done).toEqual([]);
      expect(outerKeyDown).not.toHaveBeenCalled();
      finishSave(Response.json({ path: "/w/a.txt", size: 1, revision: "r2" }));
      await appears(() => done.length === 1);
      expect(done).toEqual([true]);
      done.length = 0;

      // 3. Clean editor + real Escape: exits edit mode cleanly without propagating to enclosing SidePanel.
      await act(async () => { root.render(null); });
      await act(async () => {
        root.render(<FileEdit mid="m1" cwd="/w" file={file()} text={"a\r\nb\r\n"} onDone={(c) => done.push(c)} />);
        await import("./CodeEditor");
      });
      await appears(() => host.querySelector('[data-testid="code-editor"]'));
      outerKeyDown.mockClear();
      sendEsc();
      expect(done).toEqual([false]);
      expect(outerKeyDown).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", outerKeyDown);
    }
  });
});
