// @vitest-environment jsdom
// #2602: work a background task woke the agent for stays in an ordinary summary, never a tool drawer of its own kind.
// #9140: that summary is the woken turn's own; the answer of the turn before the wake stays on screen.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { chatLines, mergeItems } from "../../lib/turns";
import { emptyTaskIndex } from "../../lib/tasks";
import type { Item } from "../../lib/types";
import { ChatContext } from "./chat-context";
import { forgetExpanded } from "./expanded";
import { ChatLineView } from "./Lines";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
const item = (seq: number, kind: Item["kind"], text?: string): Item => ({ id: `i${seq}#0`, seq, role: kind === "notice" || kind === "recap" ? "system" : "assistant", kind, text, ts: new Date(Date.parse("2026-11-04T10:00:00Z") + seq * 1000).toISOString() });
const tool = (seq: number): Item => ({ ...item(seq, "tool_use"), tool_name: "Bash", tool_use_id: `call-${seq}`, tool_input: { command: `echo example-${seq}` } });
const user = { ...item(1, "text", "Check the example."), role: "user" as const };
const wake = { ...item(4, "notice", "Background task done"), wake: true };
const first = [user, tool(2), item(3, "text", "Earlier progress.")];
let host: HTMLDivElement;
let root: Root;
beforeEach(() => { forgetExpanded(); host = document.createElement("div"); document.body.append(host); root = createRoot(host); });
afterEach(() => { act(() => root.unmount()); host.remove(); });
// each exchange's own summary line (the groups inside an opened one are not it)
const headers = () => [...host.querySelectorAll<HTMLElement>("[data-work-group]")].filter((g) => !g.parentElement?.closest("[data-work-group]")).map((g) => g.querySelector<HTMLButtonElement>(":scope > button")!);
const slots = () => [...host.querySelectorAll("[data-latest-text]")];
const details = () => [...host.querySelectorAll("[data-work-detail]")].map((d) => d.textContent ?? "");

describe.each(["assistant", "agents"] as const)("%s background continuation", (style) => {
  function render(items: Item[], live: boolean, streaming?: string) {
    const lines = chatLines(items, { live });
    act(() => root.render(<ChatContext.Provider value={{ scope: "wake-test:", tasks: emptyTaskIndex(), subagents: [], lineStyle: style }}>
      {lines.map((line) => <ChatLineView key={line.id} line={line} mid="machine" sid="session" streaming={line.kind === "segment" && line.live ? streaming : undefined} expandFold={async () => {}} />)}
    </ChatContext.Provider>));
  }
  it("keeps the answer before a wake and gives the woken work a collapsed summary of its own", () => {
    render(first, false);
    const answered = slots()[0];
    expect(answered.textContent).toContain("Earlier progress.");
    const live = [...first, wake, tool(5), item(6, "text", "New progress."), tool(7)];
    render(live, true);
    // the turn before the wake was over: its last words stay where they were, its summary is untouched
    expect(slots()[0]).toBe(answered);
    expect(answered.textContent).toContain("Earlier progress.");
    // the woken work opens with its notice (hairline divider, no capsule or bell icon) and has one summary and one text slot of its own
    expect(host.textContent).toContain(wake.text);
    const wakeNotice = host.querySelector<HTMLElement>("[data-wake-notice]");
    expect(wakeNotice).not.toBeNull();
    expect(wakeNotice?.getAttribute("role")).toBe("separator");
    expect(wakeNotice?.querySelector("svg")).toBeNull();
    expect(wakeNotice?.className).not.toContain("rounded-full");
    expect(host.querySelectorAll("[data-segment]")).toHaveLength(2);
    expect(headers()).toHaveLength(2);
    expect(slots()).toHaveLength(2);
    const woken = slots()[1];
    expect(woken.textContent).toContain("New progress.");
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    // nothing opens by itself, during execution or after it (super rule 2)
    expect(headers().map((h) => h.getAttribute("aria-expanded"))).toEqual(["false", "false"]);
    act(() => headers()[1].click());
    for (const h of host.querySelectorAll<HTMLButtonElement>("[data-work-detail] [data-work-group] > button")) if (h.getAttribute("aria-expanded") === "false") act(() => h.click());
    for (const seq of [5, 7]) expect(details().join("\n")).toContain(`echo example-${seq}`);
    expect(details().join("\n")).not.toContain("echo example-2");
    const done = [...live, item(9, "text", "Final result.")];
    render(done, false);
    expect(slots()[0]).toBe(answered);
    expect(answered.textContent).toContain("Earlier progress.");
    expect(slots()[1]).toBe(woken);
    expect(woken.textContent).toContain("Final result.");
    expect(headers().map((h) => h.getAttribute("aria-expanded"))).toEqual(["false", "false"]);
    // every step stays inspectable in the summary of the turn it belongs to
    act(() => headers()[1].click());
    expect(details().join("\n")).toContain("New progress.");
    act(() => headers()[0].click());
    expect(details().join("\n")).toContain("echo example-2");
  });
  it("draws work a wake started after a recap under the recap, without an empty live placeholder", () => {
    const recap = item(4, "recap", "Earlier context.");
    const resumed = [...first, recap, { ...wake, id: "wake5#0", seq: 5 }, tool(6), item(7, "text", "Resumed progress.")];
    render(resumed, true);
    expect(host.querySelectorAll("[data-segment]")).toHaveLength(2);
    expect(headers()).toHaveLength(2);
    expect(slots().map((s) => s.textContent)).toEqual([expect.stringContaining("Earlier progress."), expect.stringContaining("Resumed progress.")]);
    act(() => headers()[1].click());
    expect(details().join("\n")).toContain("echo example-6");
    render([...resumed, item(8, "text", "Resumed final result.")], false);
    expect(headers()[1].getAttribute("aria-expanded")).toBe("false");
    expect(slots()[1].textContent).toContain("Resumed final result.");
    expect(slots()[0].textContent).toContain("Earlier progress.");
  });
  it("keeps a complete reply visible outside Working... while a woken turn streams new progress text", () => {
    const completeReply = item(3, "text", "Full completed reply delivered to the user.");
    const delivered = [user, tool(2), completeReply];
    render(delivered, false);
    const firstSlot = slots()[0];
    expect(firstSlot.textContent).toContain("Full completed reply delivered to the user.");

    // Background run_command finishes: wake notice at same seq as non-numeric resumed thinking + live streaming progress.
    const resumedThinking: Item = { id: "4@r#thinking", seq: 4, role: "assistant", kind: "thinking", text: "Inspecting task result.", ts: "2026-11-04T10:00:05Z" };
    const sameSeqWake: Item = { id: "3@c#-1", seq: 4, role: "system", kind: "notice", wake: true, text: "Background task updated", ts: "2026-11-04T10:00:05Z" };
    const wokenItems = mergeItems([...delivered, resumedThinking], [sameSeqWake]);
    render(wokenItems, true, "Running the follow-up step now...");

    // Prior complete reply stays independently visible (not swallowed into Working... and not overwritten by live progress).
    expect(host.querySelectorAll("[data-segment]")).toHaveLength(2);
    expect(slots()).toHaveLength(2);
    expect(slots()[0]).toBe(firstSlot);
    expect(slots()[0].textContent).toContain("Full completed reply delivered to the user.");
    expect(slots()[1].textContent).toContain("Running the follow-up step now...");
    expect(headers()).toHaveLength(2);
    expect(headers().map((h) => h.getAttribute("aria-expanded"))).toEqual(["false", "false"]);
  });
});
