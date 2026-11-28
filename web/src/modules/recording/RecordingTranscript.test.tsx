// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { recording } from "./module";
import { RecordingTranscript } from "./RecordingTranscript";
import { recordingView, type RecordingView } from "./recording-view";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
const jump = vi.fn();
const rec = recordingView("recordings", { bookmarks: [{ at: 4, label: "Review" }] }, [
  { start: 2, end: 3, text: "First literal (x)" }, { start: 2, end: 3, text: "Second literal (X)" },
])!;
function display(value: RecordingView, query?: string) {
  act(() => root.render(<RecordingTranscript recordingId="meeting" rec={value} query={query} highlighted="" onJump={jump} />));
}
function button(label: string) {
  return [...host.querySelectorAll("button")].find(el => (el.getAttribute("aria-label") || el.textContent?.trim()) === label)!;
}
function type(input: HTMLInputElement, text: string) {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}
beforeEach(async () => {
  await initI18n("en", recording.locales!);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  jump.mockReset();
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); });

it("reviews duplicate-time segments, keeps citations reachable, and restores focus and KB highlights on Escape", () => {
  display(rec, "literal");
  expect(host.querySelectorAll("strong")).toHaveLength(2);
  act(() => button("Review transcript").click());
  const input = host.querySelector<HTMLInputElement>('input[type="search"]')!;
  expect(document.activeElement).toBe(input);
  act(() => type(input, "(x)"));
  expect([...host.querySelectorAll("mark")].map(el => el.textContent)).toEqual(["(x)", "(X)"]);
  const ids = [...host.querySelectorAll("li[id]")].map(el => el.id);
  for (const shiftKey of [false, false, false, true]) {
    act(() => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey, bubbles: true })));
  }
  expect(jump.mock.calls.map(([id]) => id)).toEqual([ids[0], ids[1], ids[0], ids[1]]);
  act(() => button("Bookmarks").click());
  const bookmark = host.querySelector<HTMLButtonElement>('ol[aria-label="Transcript bookmarks"] button')!;
  act(() => bookmark.click());
  expect(jump).toHaveBeenLastCalledWith(ids[2]);
  expect(host.querySelectorAll("li[id]")).toHaveLength(3);
  act(() => type(input, "not present"));
  expect(button("Next match").disabled).toBe(true);
  expect(host.querySelector('[role="status"]')?.textContent).toBe("No matching lines");
  act(() => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(host.querySelector("input")).toBeNull();
  expect(document.activeElement).toBe(button("Review transcript"));
  expect(host.querySelectorAll("strong")).toHaveLength(2);
});

it("does not display or search expired speech even when retained metadata comes with stale segments", () => {
  display({ ...rec, trimmedAt: "2026-11-01T12:00:00Z" });
  act(() => button("Review transcript").click());
  const input = host.querySelector<HTMLInputElement>("input")!;
  act(() => type(input, "literal"));
  expect(host.querySelector('[role="status"]')?.textContent).toBe("No matching lines");
  expect(host.querySelectorAll("li[id]")).toHaveLength(1);
  expect(host.textContent).not.toContain("First literal");
  expect(host.textContent).toContain("Review");
});
