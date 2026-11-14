import { describe, expect, it } from "vitest";
import { deviceTimeZone, heldPushDestination, heldPushGroups, heldPushSummaries, lnModeEnabled, localDayKey } from "./push-intercepts";

const NOW = new Date("2026-11-14T04:00:00Z");
const ZONE = "Asia/Shanghai";

function response(...intercepts: unknown[]) {
  return { intercepts };
}

describe("held-push decoding", () => {
  it("accepts full and minimal records, ignores unknown fields, and prefers the summary field", () => {
    const groups = heldPushGroups(response({
      at: "2026-11-14T03:59:59.123456789Z",
      level: "P1",
      reason: "ln_p1_held",
      summary: "Short summary",
      title: "Long title",
      body: "Full body",
      related_key: "todo_1",
      manager: "assistant",
      also: ["Second title", "Second title"],
      future_field: { ignored: true },
    }, {
      at: "2026-11-14T02:00:00+00:00",
      level: "P2",
      reason: "ln_p2_silent",
    }), NOW, ZONE);

    expect(groups.p1).toHaveLength(1);
    expect(groups.p1[0].summaries).toEqual(["Short summary", "Second title", "Second title"]);
    expect(groups.p1[0].date.toISOString()).toBe("2026-11-14T03:59:59.123Z");
    expect(groups.p2).toHaveLength(1);
    expect(groups.p2[0].summaries).toEqual([""]);
  });

  it("falls back to title, then body, and keeps merged titles", () => {
    expect(heldPushSummaries({ title: " Title\none ", body: "Body", also: [" Other\n two ", " "] })).toEqual(["Title one", "Other two"]);
    expect(heldPushSummaries({ body: " Body\nonly " })).toEqual(["Body only"]);
    expect(heldPushSummaries({ also: ["Merged only"] })).toEqual(["Merged only"]);
  });

  it("rejects a malformed envelope but skips malformed rows without losing valid rows", () => {
    expect(() => heldPushGroups({}, NOW, ZONE)).toThrow("Invalid push intercepts response");
    const groups = heldPushGroups(response(null, { at: "bad", level: "P1", reason: "ln_p1_held" }, {
      at: "2026-11-14T03:00:00Z", level: "P1", reason: "ln_p1_held", title: "Kept",
    }), NOW, ZONE);
    expect(groups.p1.map((item) => item.summaries[0])).toEqual(["Kept"]);
  });

  it("reads the switch only as an explicit true", () => {
    expect(lnModeEnabled({ enabled: true })).toBe(true);
    expect(lnModeEnabled({ enabled: false })).toBe(false);
    expect(lnModeEnabled({ enabled: "true" })).toBe(false);
    expect(lnModeEnabled(null)).toBe(false);
  });
});

describe("held-push local-day filtering and grouping", () => {
  it("uses the device day rather than UTC, includes P1/P2 dedup rows, and leaves out P0 dedup", () => {
    const groups = heldPushGroups(response(
      { at: "2026-11-13T16:30:00Z", level: "P1", reason: "ln_p1_held", title: "Today locally" },
      { at: "2026-11-13T15:59:59Z", level: "P1", reason: "ln_p1_held", title: "Yesterday locally" },
      { at: "2026-11-14T01:00:00Z", level: "P2", reason: "ln_dedup_12h", title: "P2 dedup" },
      { at: "2026-11-14T01:01:00Z", level: "P1", reason: "ln_dedup_12h", title: "P1 dedup" },
      { at: "2026-11-14T01:02:00Z", level: "P0", reason: "ln_dedup_12h", title: "Already alerted" },
      { at: "2026-11-14T01:03:00Z", level: "P3", reason: "ln_p1_held", title: "Unknown level" },
    ), NOW, ZONE);
    expect(groups.p1.map((item) => item.summaries[0])).toEqual(["Today locally", "P1 dedup"]);
    expect(groups.p2.map((item) => item.summaries[0])).toEqual(["P2 dedup"]);
  });

  it("handles the western side of midnight and both sides of a DST jump", () => {
    const westernNow = new Date("2026-11-14T04:00:00Z");
    const western = heldPushGroups(response(
      { at: "2026-11-14T03:30:00Z", level: "P1", reason: "ln_p1_held", title: "November 13 locally" },
      { at: "2026-11-14T05:00:00Z", level: "P1", reason: "ln_p1_held", title: "November 14 locally" },
    ), westernNow, "America/New_York");
    expect(western.p1.map((item) => item.summaries[0])).toEqual(["November 13 locally"]);

    const dstNow = new Date("2026-03-08T18:00:00Z");
    const dst = heldPushGroups(response(
      { at: "2026-03-08T07:30:00Z", level: "P1", reason: "ln_p1_held", title: "Before spring forward" },
      { at: "2026-03-08T08:30:00Z", level: "P2", reason: "ln_p2_silent", title: "After spring forward" },
    ), dstNow, "America/New_York");
    expect(dst.p1).toHaveLength(1);
    expect(dst.p2).toHaveLength(1);
    expect(localDayKey(dstNow, "America/New_York")).toBe("2026-03-08");
  });

  it("keeps IDs stable when newer records arrive and distinguishes exact duplicates", () => {
    const old = { at: "2026-11-14T02:00:00Z", level: "P1", reason: "ln_p1_held", title: "Same" };
    const before = heldPushGroups(response(old, old), NOW, ZONE);
    const after = heldPushGroups(response(
      { at: "2026-11-14T03:00:00Z", level: "P1", reason: "ln_p1_held", title: "New" }, old, old,
    ), NOW, ZONE);
    expect(after.p1.slice(1).map((item) => item.id)).toEqual(before.p1.map((item) => item.id));
    expect(new Set(after.p1.map((item) => item.id)).size).toBe(3);
    const merged = heldPushGroups(response({ ...old, also: ["Merged later"] }), NOW, ZONE);
    expect(merged.p1[0].id).toBe(before.p1[0].id);
  });

  it("always has a usable device zone fallback", () => {
    expect(deviceTimeZone()).toBeTruthy();
  });
});

describe("held-push destinations", () => {
  const base = { at: "2026-11-14T02:00:00Z", level: "P1", reason: "ln_p1_held" };
  it("uses typed task/chat/URL fields and bounded legacy forms", () => {
    const known = new Set(["todo_1"]);
    expect(heldPushDestination({ ...base, todo_id: "todo_1" }, known)).toEqual({ kind: "todo", id: "todo_1" });
    expect(heldPushDestination({ ...base, machine_id: "mac", session_id: "s1" }, known)).toEqual({ kind: "session", machineId: "mac", sessionId: "s1", group: undefined });
    expect(heldPushDestination({ ...base, related_key: "todo:todo_1@2026-11-14T00:00:00Z" }, known)).toEqual({ kind: "todo", id: "todo_1" });
    expect(heldPushDestination({ ...base, related_key: "https://example.com/pull/1" }, known)).toEqual({ kind: "url", url: "https://example.com/pull/1" });
  });

  it("returns no route for unknown tasks, opaque keys, bare sessions, and unsafe URLs", () => {
    for (const item of [
      { ...base, todo_id: "missing" },
      { ...base, related_key: "todo:missing@2026-11-14T00:00:00Z" },
      { ...base, related_key: "session-id-only" },
      { ...base, related_key: "ix:request-1" },
      { ...base, related_key: "notify:assistant/ext/id" },
      { ...base, related_key: "javascript:alert(1)" },
      { ...base, machine_id: "mac" },
    ]) expect(heldPushDestination(item, new Set(["todo_1"]))).toBeUndefined();
  });
});
