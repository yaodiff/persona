// Merge and "Add to to-dos" on the Inbox (#6480; docs/assistant/inbox.md "Merge rows and Add to to-dos"): the requests, the due
// choices, the toasts with Undo. A merge and a to-do each go to one manager (the hub sends them to the manager named); rows of
// several managers cannot be merged, but each manager's rows make their own to-dos.
import { api, errorText } from "./api";
import { applyInboxChange, itemKey, reloadInbox, type InboxItem } from "./inbox";
import { itemHeaders, otherManager } from "./manager-choice";
import { t } from "../i18n";
import { toast } from "../ui/atoms";

/** When a to-do made from a row is due: an hour from now, today (all day), a moment, or never. */
export type DueChoice = { kind: "hour" } | { kind: "today" } | { kind: "at"; at: Date } | { kind: "none" };

const pad = (n: number) => String(n).padStart(2, "0");

/** The local day as 2006-01-02. */
export function localDay(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** The request's due fields for a choice: a moment as RFC 3339 (due_at), a day as 2006-01-02 (due_date, all day), none: nothing. */
export function dueBody(c: DueChoice, now = new Date()): { due_at?: string; due_date?: string } {
  switch (c.kind) {
    case "hour":
      return { due_at: new Date(now.getTime() + 3600_000).toISOString() };
    case "today":
      return { due_date: localDay(now) };
    case "at":
      return { due_at: c.at.toISOString() };
    default:
      return {};
  }
}

/** A <input type="datetime-local"> value (local time, minutes) read as a moment; null when empty or not a date. */
export function fromLocalMoment(v: string): Date | null {
  const d = v ? new Date(v) : null;
  return d && !Number.isNaN(d.getTime()) ? d : null;
}

/** A moment as a datetime-local value, rounded up to the next quarter hour: where the picker starts. */
export function pickerStart(now = new Date()): string {
  const d = new Date(Math.ceil(now.getTime() / 900_000) * 900_000);
  return `${localDay(d)}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** The managers' rows apart: a request goes to one manager. */
function byManager(items: InboxItem[]): Map<string, InboxItem[]> {
  const m = new Map<string, InboxItem[]>();
  for (const it of items) {
    const k = otherManager(it.manager) ?? "";
    m.set(k, [...(m.get(k) ?? []), it]);
  }
  return m;
}

/** Rows that can be merged: two or more open rows of one manager. */
export function canMerge(items: InboxItem[]): boolean {
  return items.length >= 2 && byManager(items).size === 1;
}

interface MergeAnswer {
  item: InboxItem;
  members: string[];
  undo: { id: string };
}

/** "Merge": the rows become one (the newest row, its sources' later messages follow it); the members leave the list at once the hub
 * answers, and the toast has Undo. Resolves to whether it merged. */
export async function mergeItems(items: InboxItem[]): Promise<boolean> {
  if (!canMerge(items)) return false;
  const manager = otherManager(items[0].manager);
  try {
    const r = await api<MergeAnswer>("/inbox/merge", { method: "POST", json: { items: items.map((it) => ({ id: it.id, version: it.version })) }, headers: itemHeaders(manager) });
    applyInboxChange([r.item], r.members ?? [], manager);
    toast(t("lib-inbox-actions.merged", { count: (r.members?.length ?? 0) + 1 }), "ok", { label: t("lib-inbox-actions.undo"), run: () => void unmergeItems(r.undo.id, manager) });
    return true;
  } catch (e) {
    toast(t("lib-inbox-actions.merge_failed", { error: errorText(e) }), "error");
    void reloadInbox();
    return false;
  }
}

/** Undo of a merge: the members get their rows back; the list is read again for them (the hub lists them now). */
async function unmergeItems(id: string, manager: string | undefined): Promise<void> {
  try {
    const r = await api<{ changed: InboxItem[] }>("/inbox/merge/undo", { method: "POST", json: { id }, headers: itemHeaders(manager) });
    applyInboxChange(r.changed ?? [], [], manager);
    void reloadInbox();
    toast(t("lib-inbox-actions.unmerged"), "ok");
  } catch (e) {
    toast(t("lib-inbox-actions.unmerge_failed", { error: errorText(e) }), "error");
  }
}

interface TodoAnswer {
  changed: InboxItem[];
  skipped?: { id: string; reason: string }[];
  failed?: { id: string }[];
  todos?: { item: InboxItem; todo: { id: string } }[];
  undo?: { items: { id: string; todo_id: string }[] };
}

/** "Add to to-dos": one to-do per row, all with the same due choice, each titled like its row (`titles`: the title the user made of
 * a row, by item key). The rows count as handled; the toast has Undo. Resolves to how many were added. */
export async function addTodos(items: InboxItem[], due: DueChoice, titles: Record<string, string> = {}): Promise<number> {
  let added = 0;
  const undo: { manager: string | undefined; items: { id: string; todo_id: string }[] }[] = [];
  let failure: unknown;
  for (const [m, list] of byManager(items)) {
    const manager = m || undefined;
    try {
      const body = { items: list.map((it) => ({ id: it.id, version: it.version, title: titles[itemKey(it)] || undefined })), ...dueBody(due) };
      const r = await api<TodoAnswer>("/inbox/todo", { method: "POST", json: body, headers: itemHeaders(manager) });
      applyInboxChange(r.changed ?? [], [], manager);
      added += r.todos?.length ?? 0;
      if (r.undo?.items?.length) undo.push({ manager, items: r.undo.items });
    } catch (e) {
      failure = e;
    }
  }
  if (failure !== undefined) {
    toast(added ? t("lib-inbox-actions.todo_partly", { count: added, error: errorText(failure) }) : t("lib-inbox-actions.todo_failed", { error: errorText(failure) }), "error");
    void reloadInbox();
  }
  if (added && failure === undefined) {
    toast(t("lib-inbox-actions.todo_added", { count: added }), "ok", { label: t("lib-inbox-actions.undo"), run: () => void undoTodos(undo) });
  }
  return added;
}

async function undoTodos(groups: { manager: string | undefined; items: { id: string; todo_id: string }[] }[]): Promise<void> {
  try {
    for (const g of groups) {
      const r = await api<{ changed: InboxItem[] }>("/inbox/todo/undo", { method: "POST", json: { items: g.items }, headers: itemHeaders(g.manager) });
      applyInboxChange(r.changed ?? [], [], g.manager);
    }
    toast(t("lib-inbox-actions.todo_undone"), "ok");
  } catch (e) {
    toast(t("lib-inbox-actions.todo_undo_failed", { error: errorText(e) }), "error");
    void reloadInbox();
  }
}
