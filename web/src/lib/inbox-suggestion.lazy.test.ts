/// <reference types="node" />
// lib/inbox.ts is on every first screen and needs only statusOf, actOnSuggestion and actedText of an item's event.
// Importing them from inbox-events.ts put that whole module (the Inbox page's chain, suggestion card and handling
// line helpers) and all of its words on every first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../i18n/startup-namespaces.json";
import * as events from "./inbox-events";
import { actedText, actOnSuggestion, statusOf } from "./inbox-suggestion";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const runtimeImport = (from: string) => new RegExp(`^import\\s+(?!type\\b)[^;]*from\\s+["']${from}["']`, "m");

describe("inbox-suggestion", () => {
  it("keeps inbox-events.ts and its words off the first screen", () => {
    expect(read("./inbox.ts")).not.toMatch(runtimeImport("\\./inbox-events"));
    expect(read("./inbox-suggestion.ts")).not.toMatch(runtimeImport("\\./inbox-events"));
    const eventKeys = startup.filter((key) => key === "lib-inbox-events" || key.startsWith("lib-inbox-events."));
    expect(eventKeys.sort()).toEqual([
      "lib-inbox-events.agent_started",
      "lib-inbox-events.filed",
      "lib-inbox-events.handed_back",
      "lib-inbox-events.handed_off",
      "lib-inbox-events.handoff_unavailable",
      "lib-inbox-events.instructed",
      "lib-inbox-events.rejected",
      "lib-inbox-events.sent",
      "lib-inbox-events.stale",
      "lib-inbox-events.todo_added",
    ]);
  });

  it("is what inbox-events.ts offers the Inbox page", () => {
    expect(events.statusOf).toBe(statusOf);
    expect(events.actOnSuggestion).toBe(actOnSuggestion);
    expect(events.actedText).toBe(actedText);
  });
});
