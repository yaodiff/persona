/// <reference types="node" />
// A morning/evening briefing card (Brief.tsx) is drawn only in a routine conversation that has a `brief` item, and
// AcceptRow is drawn only inside the Needs you tray (where TaskDecision.tsx already imports it lazily). Keeping
// Tasks.tsx off static imports of ./Brief keeps Brief.tsx and its assistant-brief words off every cold first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../../i18n/startup-namespaces.json";
import { startupMatcher } from "../../i18n/startup-keys";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");

describe("Brief.tsx stays off the first screen", () => {
  it("loads BriefCard and AcceptRow lazily from Tasks.tsx and keeps briefing copy out of startup-namespaces", () => {
    const tasks = read("./Tasks.tsx");
    expect(tasks).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/Brief["']/m);
    expect(tasks).toContain('lazyPage(() => import("./Brief").then((m) => m.BriefCard))');
    expect(tasks).not.toMatch(/lazyPage\([^)]*\.\/Brief[^)]*idle\s*:\s*true/);
    expect(read("./TaskDecision.tsx")).toMatch(/^import\s+[^;]*\bAcceptRow\b[^;]*from\s+["']\.\/Brief["']/m);

    const briefStartup = startup.filter((key) => key === "assistant-brief" || key.startsWith("assistant-brief."));
    // Tasks.tsx (first screen) toasts an accept's outcome (#15680); the rest of the briefing words stay off it
    expect(briefStartup).toEqual(["assistant-brief.accept_failed", "assistant-brief.accepted", "assistant-brief.failed_", "assistant-brief.open"]);

    const inStartup = startupMatcher(startup);
    expect(inStartup("assistant-brief.open")).toBe(true);
    expect(inStartup("assistant-brief.open_row")).toBe(true);
    expect(inStartup("assistant-brief.failed_task")).toBe(true);
    expect(inStartup("assistant-brief.failed_run")).toBe(true);
    expect(inStartup("assistant-brief.failed_skipped")).toBe(true);
    for (const deferred of [
      "assistant-brief.title",
      "assistant-brief.waiting_head",
      "assistant-brief.past_head",
      "assistant-brief.accept",
      "assistant-brief.accept_head",
      "assistant-brief.later_done",
      "assistant-brief.progress_head",
      "assistant-brief.recommend",
      "assistant-brief.take",
      "assistant-brief.waited",
    ]) {
      expect(inStartup(deferred), deferred).toBe(false);
    }
  });
});
