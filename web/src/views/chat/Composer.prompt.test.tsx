// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { beforeAll, expect, it, vi } from "vitest";
import { Composer } from "./Composer";
import type { MentionRow, PanelPick } from "./ComposerPanel";
import { getContexts, type ContextRef } from "../../lib/links";
import { readInlineDraft, writeInlineDraft } from "../../lib/inline-mentions";
import { loadPages } from "../../lib/lazy-page";
import { t } from "../../i18n";

vi.mock("./ComposerPanel", () => ({
  ComposerPanel: ({ onPick }: { onPick: (pick: PanelPick) => void }) => (
    <button onClick={() => onPick({
      kind: "row",
      // Model an unsupported row arriving through the lazy picker.
      row: { kind: "resource", id: "resource:notes.txt", name: "Notes", dir: false, insert: "@notes.txt " } as unknown as MentionRow,
    })}>Pick resource</button>
  ),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeAll(() => loadPages());

it("ignores rows outside the task file picker types even when they carry file-like text", async () => {
  const onChange = vi.fn();
  const scope = { machine: "box", cwd: "/work", launcher: "claude", runtime: "" };
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(
      <Composer draftKey="task-resource" machineId={scope.machine} placeholder=""
        mention={{ cwd: scope.cwd, files: () => [], referenceFiles: scope }}
        editor={{ text: "Read it", refs: [], label: "Task prompt", onChange }} />,
    ));
    act(() => host.querySelector<HTMLButtonElement>("[data-composer-plus]")!.click());
    const resource = [...host.querySelectorAll("button")].find((button) => button.textContent === "Pick resource");
    expect(resource).toBeDefined();
    act(() => resource!.click());
    expect(onChange).not.toHaveBeenCalled();
    expect(host.querySelector("textarea")?.value).toBe("Read it");
  } finally {
    act(() => root.unmount());
    host.remove();
  }
});

it("edits and removes a task reference without chat sends, history or stored chat drafts", async () => {
  const sent = vi.fn(async () => true);
  const initial: ContextRef = {
    id: "file:notes.txt", kind: "file", title: "Notes", url: "", text: "notes.txt",
    attrs: { path: "notes.txt", machine: "box", cwd: "/work", launcher: "claude", runtime: "" },
  };
  function Form() {
    const [value, setValue] = useState({ text: "Read it", refs: [initial] });
    return <Composer draftKey="prompt-test" editor={{
      ...value, label: "Task prompt", onChange: (text, refs) => setValue({ text, refs }),
    }} placeholder="" onSend={sent} />;
  }
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(<Form />));
    const box = host.querySelector("textarea")!;
    expect(box.value).toBe("Read it");
    expect(host.textContent).toContain("Notes");
    expect(host.querySelector('[data-testid="composer-send"]')).toBeNull();
    expect(getContexts("prompt-test")).toHaveLength(0);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, "  Read latest ");
      box.dispatchEvent(new Event("input", { bubbles: true }));
      box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(box.value).toBe("  Read latest ");
    expect(sent).not.toHaveBeenCalled();
    const remove = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("link-contextchips.remove_label")}"]`)!;
    act(() => remove.click());
    expect(host.textContent).not.toContain("Notes");
    expect(box.value).toBe("  Read latest ");
  } finally {
    act(() => root.unmount());
    host.remove();
  }
  expect(localStorage.getItem("draft:prompt-test")).toBeNull();
});

it("keeps long pastes and mixed file+text pastes inline instead of dropping them in editor mode", async () => {
  const onChange = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const longText = Array.from({ length: 16 }, (_, i) => `step ${i + 1}: check release gates`).join("\n");
  try {
    await act(async () => root.render(
      <Composer draftKey="task-paste-plain" editor={{ text: "", refs: [], label: "Task prompt", onChange }} placeholder="" />,
    ));
    const box = host.querySelector("textarea")!;
    const longPaste = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(longPaste, "clipboardData", {
      value: { files: [], getData: (type: string) => (type === "text/plain" ? longText : "") },
    });
    await act(async () => {
      box.dispatchEvent(longPaste);
    });
    expect(longPaste.defaultPrevented).toBe(false);
    expect(host.querySelector(`button[aria-label="${t("chat-composeroverlays.pasted_remove")}"]`)).toBeNull();
    expect(host.textContent).not.toContain(t("chat-composeroverlays.pasted_chip", { count: longText.length }));

    // Rich clipboard payloads (for example from a document editor) can carry both a file/image and plain text.
    // Editor mode does not attach files, so it must not swallow the paste either.
    const mixedPaste = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(mixedPaste, "clipboardData", {
      value: {
        files: [new File(["img"], "clip.png", { type: "image/png" })],
        getData: (type: string) => (type === "text/plain" ? "copied checklist" : ""),
      },
    });
    await act(async () => {
      box.dispatchEvent(mixedPaste);
    });
    expect(mixedPaste.defaultPrevented).toBe(false);
  } finally {
    act(() => root.unmount());
    host.remove();
  }
});

it("handles long, image-only, and mixed HTML/file pastes safely when the task prompt uses inline tokens", async () => {
  const skill: ContextRef = {
    id: "skill:release", kind: "skill", title: "release", url: "", text: "release",
    attrs: { name: "release", launcher: "claude" },
  };
  const initial = writeInlineDraft(["Run ", skill, " "]);
  const longText = "x".repeat(900);
  const dispatchPaste = (box: HTMLElement, data: { files?: File[]; text?: string; html?: string }) => {
    const paste = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(paste, "clipboardData", {
      value: {
        files: data.files ?? [],
        getData: (type: string) => (type === "text/plain" ? (data.text ?? "") : type === "text/html" ? (data.html ?? "") : ""),
      },
    });
    act(() => { box.dispatchEvent(paste); });
    return paste;
  };

  // 1. Long plain-text paste inserts inline and preserves the existing skill token.
  {
    let latest = initial;
    const onChange = vi.fn((next: string) => { latest = next; });
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(
        <Composer draftKey="task-paste-inline-long" editor={{ text: latest, refs: [], label: "Task prompt", onChange }} placeholder="" />,
      ));
      const box = host.querySelector<HTMLElement>('[role="textbox"]')!;
      expect(box).not.toBeNull();
      const paste = dispatchPaste(box, { text: longText });
      expect(paste.defaultPrevented).toBe(true);
      expect(onChange).toHaveBeenCalledTimes(1);
      const parts = readInlineDraft(latest);
      expect(parts).toContainEqual(skill);
      expect(parts.filter((p): p is string => typeof p === "string").join("")).toContain("Run ");
      expect(parts.filter((p): p is string => typeof p === "string").join("")).toContain(longText);
      expect(host.querySelector(`button[aria-label="${t("chat-composeroverlays.pasted_remove")}"]`)).toBeNull();
    } finally {
      act(() => root.unmount());
      host.remove();
    }
  }

  // 2. Pure image/HTML clipboard does not call onChange or inject an <img> into the contenteditable box.
  {
    const onChange = vi.fn();
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(
        <Composer draftKey="task-paste-inline-image" editor={{ text: initial, refs: [], label: "Task prompt", onChange }} placeholder="" />,
      ));
      const box = host.querySelector<HTMLElement>('[role="textbox"]')!;
      const paste = dispatchPaste(box, {
        files: [new File(["img"], "clip.png", { type: "image/png" })],
        text: "",
        html: '<img src="x">',
      });
      expect(paste.defaultPrevented).toBe(true);
      expect(onChange).not.toHaveBeenCalled();
      expect(box.querySelector("img")).toBeNull();
    } finally {
      act(() => root.unmount());
      host.remove();
    }
  }

  // 3. Mixed file + HTML + plain text inserts only the plain text and preserves the skill token without raw HTML.
  {
    let latest = initial;
    const onChange = vi.fn((next: string) => { latest = next; });
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(
        <Composer draftKey="task-paste-inline-mixed" editor={{ text: latest, refs: [], label: "Task prompt", onChange }} placeholder="" />,
      ));
      const box = host.querySelector<HTMLElement>('[role="textbox"]')!;
      const paste = dispatchPaste(box, {
        files: [new File(["img"], "clip.png", { type: "image/png" })],
        html: '<b>hi</b><img src="x">',
        text: "copied checklist",
      });
      expect(paste.defaultPrevented).toBe(true);
      expect(onChange).toHaveBeenCalledTimes(1);
      expect(box.querySelector("b, img")).toBeNull();
      const parts = readInlineDraft(latest);
      expect(parts).toContainEqual(skill);
      expect(parts.filter((p): p is string => typeof p === "string").join("")).toContain("Run ");
      expect(parts.filter((p): p is string => typeof p === "string").join("")).toContain("copied checklist");
    } finally {
      act(() => root.unmount());
      host.remove();
    }
  }
});
