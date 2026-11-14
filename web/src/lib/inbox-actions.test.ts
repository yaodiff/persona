import { describe, expect, it } from "vitest";
import { dueBody, localDay, pickerStart, fromLocalMoment } from "./inbox-actions";
import { dueOf, dueParts } from "../views/inbox/InboxEvent";

describe("due choices", () => {
  const now = new Date(2026, 10, 14, 9, 30);
  it("an hour later is a moment, today a day, none nothing", () => {
    expect(dueBody({ kind: "hour" }, now)).toEqual({ due_at: new Date(2026, 10, 14, 10, 30).toISOString() });
    expect(dueBody({ kind: "today" }, now)).toEqual({ due_date: "2026-11-14" });
    expect(dueBody({ kind: "none" }, now)).toEqual({});
    const at = new Date(2026, 10, 20, 15, 0);
    expect(dueBody({ kind: "at", at }, now)).toEqual({ due_at: at.toISOString() });
  });
  it("the picker starts at the next quarter hour, and reads back", () => {
    expect(pickerStart(now)).toBe("2026-11-14T09:30");
    expect(pickerStart(new Date(2026, 10, 14, 23, 50))).toBe("2026-11-15T00:00");
    expect(fromLocalMoment("2026-11-14T09:30")?.getHours()).toBe(9);
    expect(fromLocalMoment("")).toBeNull();
    expect(localDay(new Date(2026, 0, 5))).toBe("2026-01-05");
  });
  it("a suggestion's due goes to the card's fields and back", () => {
    expect(dueParts("2026-11-20")).toEqual({ day: "2026-11-20", time: "" });
    expect(dueParts(undefined)).toEqual({ day: "", time: "" });
    const iso = new Date(2026, 10, 20, 15, 0).toISOString();
    expect(dueParts(iso)).toEqual({ day: "2026-11-20", time: "15:00" });
    expect(dueOf("2026-11-20", "")).toBe("2026-11-20");
    expect(dueOf("2026-11-20", "15:00")).toBe(iso);
    expect(dueOf("", "15:00")).toBeUndefined();
  });
});
