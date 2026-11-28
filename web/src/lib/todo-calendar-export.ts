import { dayKey, type CalendarEntry } from "./todo-calendar";
import { BUILTIN_MANAGER } from "./manager-choice";
import { fingerprint } from "./feed-cache";

function utcStamp(ms: number): string {
  return new Date(ms).toISOString().replace(/[-:]/g, "").replace(/\.\d{3}Z$/, "Z");
}

// RFC 5545 TEXT uses a backslash for punctuation and newlines. Other control characters
// (except tab) cannot appear in a content line.
function summary(title: string): string {
  // File and image links can carry credentials and private host, machine, session and path names.
  const safeTitle = title.replace(/(?:https?:\/\/|\/)[^\s<>"]+/gi, url =>
    url.includes("/api/v1/") && /[?&]token=/i.test(url) ? "…" : url);
  return safeTitle.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, "")
    .replace(/\\/g, "\\\\").replace(/\r\n|[\r\n]/g, "\\n").replace(/[,;]/g, "\\$&");
}

// Each physical line gets 75 octets, including a continuation's leading space.
function fold(line: string): string {
  const encoder = new TextEncoder();
  let result = "";
  let length = 0;
  for (const character of line) {
    const bytes = encoder.encode(character).length;
    if (length + bytes > 75) {
      result += "\r\n ";
      length = 1;
    }
    result += character;
    length += bytes;
  }
  return result;
}

export function deadlineCalendar(entries: readonly CalendarEntry[], hub: string): string {
  const lines = ["BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Persona//To-do deadlines//EN"];
  for (const entry of entries) {
    // Each hub belongs to one owner. An ID can also be reused by a manager or a newly created task.
    const identity = JSON.stringify([hub, entry.manager || BUILTIN_MANAGER, entry.id, entry.createdAt]);
    lines.push(
      "BEGIN:VEVENT",
      `UID:${fingerprint(identity)}@todos.persona.invalid`,
      `DTSTAMP:${utcStamp(Date.parse(entry.updatedAt))}`,
      entry.dueDate ? `DTSTART;VALUE=DATE:${entry.dueDate.replaceAll("-", "")}` : `DTSTART:${utcStamp(entry.start)}`,
      fold(`SUMMARY:${summary(entry.title)}`),
      "TRANSP:TRANSPARENT",
      "END:VEVENT",
    );
  }
  return [...lines, "END:VCALENDAR", ""].join("\r\n");
}

export function downloadDeadlines(entries: readonly CalendarEntry[], firstDay: number, hub: string): void {
  const blob = new Blob([deadlineCalendar(entries, hub)], { type: "text/calendar;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.download = `deadlines-${dayKey(firstDay)}.ics`;
  link.href = url;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
