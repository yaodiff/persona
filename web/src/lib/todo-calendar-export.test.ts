// Dates are constructed in the browser's time zone. Run in both a DST zone and a zone east of UTC.
import { describe, expect, it } from "vitest";
import { calendarEntries, calendarRange } from "./todo-calendar";
import { deadlineCalendar } from "./todo-calendar-export";
import type { Todo } from "./todos";

const stamp = Date.UTC(2026, 10, 8, 12, 34, 56);
const source = "calendar-owner-a";
const revised = new Date(stamp).toISOString();
const todo = (id: string, title: string, fields: Partial<Todo>): Todo => ({ id, title, created_at: revised, updated_at: revised, ...fields });
const items = [
  todo("private-id-1", "Bring flowers", { due_date: "2026-11-01", notes: "private notes", origin: "private-session", manager: "private-device", plan_id: "private-plan" }),
  todo("private-id-2", "First Sunday shift", { due_at: "2026-11-01T01:30:00-04:00" }),
  todo("private-id-3", "Second Sunday shift", { due_at: "2026-11-01T01:30:00-05:00", done_at: "2026-11-01T12:00:00Z" }),
  todo("private-id-4", "Next month", { due_date: "2026-12-10" }),
  todo("private-id-5", "Unscheduled", {}),
];
function visible(todos = items, showDone = true) {
  const [from, to] = calendarRange("month", new Date(2026, 10, 1).getTime(), 1);
  return calendarEntries(todos, { from, to, showDone, now: stamp });
}
const unfold = (file: string) => file.replaceAll("\r\n ", "");

describe("deadline calendar download", () => {
  it("keeps a local date and both instants of the repeated hour, leaving availability free", () => {
    const file = deadlineCalendar(visible(), source);
    expect(file).toMatch(/^BEGIN:VCALENDAR\r\n/);
    expect(file).toContain("VERSION:2.0\r\n");
    expect(file).toContain("PRODID:");
    expect(file).toMatch(/END:VCALENDAR\r\n$/);
    expect(file.match(/BEGIN:VEVENT\r\n/g)).toHaveLength(3);
    expect(file.match(/END:VEVENT\r\n/g)).toHaveLength(3);
    expect(file.match(/TRANSP:TRANSPARENT\r\n/g)).toHaveLength(3);
    expect(file.match(/DTSTAMP:20261108T123456Z\r\n/g)).toHaveLength(3);
    expect(file).toContain("DTSTART;VALUE=DATE:20261101\r\n");
    expect(file).toContain("DTSTART:20261101T053000Z\r\n");
    expect(file).toContain("DTSTART:20261101T063000Z\r\n");
    expect(file).not.toMatch(/private-|private notes|DTEND|DURATION|Next month|Unscheduled/);
    expect(deadlineCalendar(visible(items, false), source)).not.toContain("Second Sunday shift");
  });

  it("keeps distinct opaque UIDs after edits, navigation and another download", () => {
    const entries = visible();
    const ids = (file: string) => [...unfold(file).matchAll(/^UID:(.+)\r$/gm)].map(m => m[1]);
    const first = ids(deadlineCalendar(entries, source));
    const edited = { ...entries[1], title: "Rescheduled", start: entries[1].start + 3600000 };
    expect(new Set(first).size).toBe(entries.length);
    expect(ids(deadlineCalendar([edited], source))).toEqual([first[1]]);
  });

  it("keeps token-bearing Persona links, including their hosts, paths and query strings, out of titles", () => {
    const links = [
      "https://private.example.test/proxy/api/v1/machines/private-device/file/download?path=notes(2)'s.md&cwd=private-dir&inline=1&token=synthetic-key",
      "/proxy/api/v1/machines/private-device/file/thumb?token=synthetic-key&path=private-file",
      "/api/v1/sessions/private-device/private-session/items/private-item/image?token=synthetic-key",
    ];
    for (const link of links) {
      const file = unfold(deadlineCalendar([{ ...visible()[0], title: `Read ${link} then https://example.org/guide` }], source));
      expect(file).toContain("SUMMARY:Read … then https://example.org/guide");
      expect(file).not.toMatch(/private|synthetic-key|api\/v1|token=/);
    }
  });

  it("separates identical IDs from different hubs and managers and dates cached revisions by the source", () => {
    const event = todo("same-id", "Meeting", { due_date: "2026-11-01" });
    const old = visible([event]);
    const current = visible([{ ...event, title: "Moved", updated_at: "2026-11-09T12:34:56Z" }]);
    const uid = (file: string) => /^UID:(.+)$/m.exec(file)![1];
    const newer = deadlineCalendar(current, source);
    const cached = deadlineCalendar(old, source);
    expect(uid(cached)).toBe(uid(newer));
    expect(uid(cached)).not.toBe(uid(deadlineCalendar(old, "calendar-owner-b")));
    expect(uid(cached)).not.toBe(uid(deadlineCalendar(visible([{ ...event, manager: "other" }]), source)));
    expect(cached).toContain("DTSTAMP:20261108T123456Z");
    expect(newer).toContain("DTSTAMP:20261109T123456Z");
    expect(cached).not.toContain(source);
  });

  it("retains all four digits of an early all-day year", () => {
    const entries = calendarEntries([todo("early", "Anniversary", { due_date: "0999-07-05" })],
      { from: -Infinity, to: Infinity, showDone: false });
    expect(deadlineCalendar(entries, source)).toContain("DTSTART;VALUE=DATE:09990705");
  });

  it("escapes TEXT and folds at 75 UTF-8 bytes without splitting a character", () => {
    const words = "予定📅周末".repeat(13);
    const title = words + ", tea; milk\\coffee\r\nLOCATION:home\nend\rfinish";
    const file = deadlineCalendar([{ ...visible()[0], title }], source);
    for (const line of file.split("\r\n")) expect(new TextEncoder().encode(line).length).toBeLessThanOrEqual(75);
    expect(file).toContain("\r\n ");
    expect(unfold(file)).toContain("SUMMARY:" + words + "\\, tea\\; milk\\\\coffee\\nLOCATION:home\\nend\\nfinish\r\n");
    expect(file.replaceAll("\r\n", "")).not.toMatch(/[\r\n]/);
  });
});
