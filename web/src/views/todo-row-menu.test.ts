// A to-do row's menu (#4429): which rows it has for an open and a done to-do, what each choice saves, and the days and
// moments its presets pick. Local dates, so the results hold in any time zone; the to-dos are made up.
import { beforeAll, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import type { Todo, TodoPatch } from "../lib/todos";
import type { MenuItem } from "../ui/menu-model";
import { dueChoices, dueOn, duePatch, remindChoices, todoRowMenu, type TodoRowActions } from "./todo-row-menu";

beforeAll(async () => {
  await initI18n("en");
});

// Wednesday, 11 November 2026, 15:20:30 local
const NOW = new Date(2026, 10, 11, 15, 20, 30).getTime();
const MONDAY = 1;
const SUNDAY = 0;
const day = (d: number) => new Date(2026, 10, d).getTime();

const todo = (patch: Partial<Todo> = {}): Todo => ({ id: "t1", title: "Write the release notes", created_at: "2026-11-10T08:00:00Z", updated_at: "2026-11-10T08:00:00Z", ...patch });
const actions = () =>
  ({
    toggleDone: vi.fn<() => void>(),
    save: vi.fn<(patch: TodoPatch) => void>(),
    pickDue: vi.fn<() => void>(),
    handOver: vi.fn<() => void>(),
    remove: vi.fn<() => void>(),
  }) satisfies TodoRowActions;
const find = (items: MenuItem[], key: string): MenuItem => {
  for (const it of items) {
    if (it.key === key) return it;
    if (Array.isArray(it.children)) {
      const sub = it.children.find((c) => c.key === key);
      if (sub) return sub;
    }
  }
  throw new Error(`no row ${key}`);
};

describe("dueChoices", () => {
  it("offers today, tomorrow and the first day of next week", () => {
    expect(dueChoices(NOW, MONDAY)).toEqual({ today: day(11), tomorrow: day(12), nextWeek: day(16) });
  });

  it("starts next week where the browser's week starts", () => {
    expect(dueChoices(NOW, SUNDAY).nextWeek).toBe(day(15));
  });

  it("on the last day of a week, next week starts tomorrow", () => {
    const sunday = new Date(2026, 10, 15, 22, 0).getTime();
    expect(dueChoices(sunday, MONDAY)).toEqual({ today: day(15), tomorrow: day(16), nextWeek: day(16) });
  });
});

describe("remindChoices", () => {
  it("is an hour from now to the minute, and tomorrow at nine", () => {
    expect(remindChoices(NOW)).toEqual({
      inHour: new Date(2026, 10, 11, 16, 20).getTime(),
      tomorrow: new Date(2026, 10, 12, 9, 0).getTime(),
    });
  });
});

describe("dueOn and duePatch", () => {
  it("reads the day of a to-do due on a day or at a moment", () => {
    expect(dueOn(todo())).toBeUndefined();
    expect(dueOn(todo({ due_date: "2026-11-12" }))).toBe(day(12));
    expect(dueOn(todo({ due_at: new Date(2026, 10, 13, 18, 30).toISOString() }))).toBe(day(13));
  });

  it("moves a to-do due on a day, or not due, to the new day", () => {
    expect(duePatch(todo(), day(16))).toEqual({ due_date: "2026-11-16" });
    expect(duePatch(todo({ due_date: "2026-11-12" }), day(16))).toEqual({ due_date: "2026-11-16" });
  });

  it("keeps the time of a to-do due at one", () => {
    const patch = duePatch(todo({ due_at: new Date(2026, 10, 13, 18, 30).toISOString() }), day(16));
    expect(patch).toEqual({ due_at: new Date(2026, 10, 16, 18, 30).toISOString() });
  });
});

describe("todoRowMenu", () => {
  it("offers an open to-do every action, in the page's order", () => {
    const items = todoRowMenu(todo(), true, NOW, actions(), MONDAY);
    expect(items.map((it) => it.label)).toEqual(["Complete to-do", "Priority", "Due date", "Remind me", "Hand to agent…", "Delete to-do…"]);
    expect(find(items, "delete").danger).toBe(true);
  });

  it("offers a done to-do Reopen and no hand-over, which the hub refuses", () => {
    const act = actions();
    const items = todoRowMenu(todo({ done_at: "2026-11-11T09:00:00Z" }), false, NOW, act, MONDAY);
    expect(items.map((it) => it.key)).toEqual(["reopen", "priority", "due", "remind", "delete"]);
    find(items, "reopen").onSelect();
    expect(act.toggleDone).toHaveBeenCalledTimes(1);
  });

  it("labels an open concern's completion action Report and close", () => {
    const items = todoRowMenu(todo({ kind: "concern" }), true, NOW, actions(), MONDAY);
    expect(find(items, "complete").label).toBe("Report and close…");
  });

  it("labels a closed concern's action Reopen to-do", () => {
    const items = todoRowMenu(todo({ kind: "concern", done_at: "2026-11-11T09:00:00Z" }), false, NOW, actions(), MONDAY);
    expect(find(items, "reopen").label).toBe("Reopen to-do");
  });

  it("follows the row's checkbox: a failed start waiting on the owner reads open", () => {
    const items = todoRowMenu(todo({ done_at: "2026-11-11T09:00:00Z", start_error: "no launcher" }), true, NOW, actions(), MONDAY);
    expect(items[0].key).toBe("complete");
    expect(items.some((it) => it.key === "hand-over")).toBe(false);
  });

  it("marks the current priority and saves the one chosen", () => {
    const act = actions();
    const items = todoRowMenu(todo({ priority: "high" }), true, NOW, act, MONDAY);
    const sub = find(items, "priority").children as MenuItem[];
    expect(sub.map((it) => [it.key, it.radio, it.checked])).toEqual([
      ["priority-high", true, true],
      ["priority-normal", true, false],
      ["priority-low", true, false],
    ]);
    find(items, "priority-low").onSelect();
    expect(act.save).toHaveBeenCalledWith({ priority: "low" });
  });

  it("reads an old hub's normal priority as Normal", () => {
    const items = todoRowMenu(todo({ priority: "normal" }), true, NOW, actions(), MONDAY);
    expect(find(items, "priority-normal").checked).toBe(true);
  });

  it("saves the due day chosen, marks the one it is due on, and clears it only when there is one", () => {
    const act = actions();
    const none = todoRowMenu(todo(), true, NOW, act, MONDAY);
    expect(find(none, "due-none").disabled).toBe(true);
    find(none, "due-next-week").onSelect();
    expect(act.save).toHaveBeenLastCalledWith({ due_date: "2026-11-16" });
    find(none, "due-pick").onSelect();
    expect(act.pickDue).toHaveBeenCalledTimes(1);

    const tomorrow = todoRowMenu(todo({ due_date: "2026-11-12" }), true, NOW, act, MONDAY);
    expect((find(tomorrow, "due").children as MenuItem[]).filter((it) => it.checked).map((it) => it.key)).toEqual(["due-tomorrow"]);
    expect(find(tomorrow, "due-none").disabled).toBe(false);
    find(tomorrow, "due-none").onSelect();
    expect(act.save).toHaveBeenLastCalledWith({ due_at: null });
  });

  it("sets a reminder an hour out or tomorrow morning, and clears one only when there is one", () => {
    const act = actions();
    const items = todoRowMenu(todo(), true, NOW, act, MONDAY);
    expect(find(items, "remind-none").disabled).toBe(true);
    find(items, "remind-hour").onSelect();
    expect(act.save).toHaveBeenLastCalledWith({ remind_at: new Date(2026, 10, 11, 16, 20).toISOString() });
    find(items, "remind-tomorrow").onSelect();
    expect(act.save).toHaveBeenLastCalledWith({ remind_at: new Date(2026, 10, 12, 9, 0).toISOString() });

    const set = todoRowMenu(todo({ remind_at: "2026-11-12T01:00:00Z" }), true, NOW, act, MONDAY);
    find(set, "remind-none").onSelect();
    expect(act.save).toHaveBeenLastCalledWith({ remind_at: null });
  });

  it("hands over and deletes through the page's dialogs", () => {
    const act = actions();
    const items = todoRowMenu(todo(), true, NOW, act, MONDAY);
    find(items, "hand-over").onSelect();
    find(items, "delete").onSelect();
    expect(act.handOver).toHaveBeenCalledTimes(1);
    expect(act.remove).toHaveBeenCalledTimes(1);
    expect(act.save).not.toHaveBeenCalled();
  });
});
