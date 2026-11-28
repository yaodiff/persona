import type { BriefBody, BriefContribution, BriefFailed, BriefRow, BriefSetup, FeedItem } from "./feed";
import type { Todo, TodoAsk } from "./todos";

/** What a `BriefRow` field says when it is text: the value if it is a string, nothing otherwise. The rows come off the
 *  wire and out of to-dos whose own `ask.from` is an object (`TodoAsk["from"]`), so a field typed `string` is not
 *  proof the value is one: drawn into a line it would read `[object Object]`. */
function text(v: unknown): string | undefined {
  return typeof v === "string" && v ? v : undefined;
}

/** A row as drawn: only the fields it has, each of the type it is declared with. A field of another type is dropped. */
export function briefRow(raw: unknown): BriefRow | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const r = raw as Record<string, unknown>;
  const id = text(r.id);
  if (!id) return undefined;
  const row: BriefRow = { id, title: text(r.title) ?? "", waited_s: typeof r.waited_s === "number" && Number.isFinite(r.waited_s) ? r.waited_s : 0 };
  for (const key of ["detail", "from", "link", "evidence_link", "asked_at", "verdict", "recommend", "machine_id", "session_id"] as const) {
    const v = text(r[key]);
    if (v) row[key] = v;
  }
  if (r.nudged === true) row.nudged = true;
  const outcome = text(r.outcome);
  if (outcome) row.outcome = outcome;
  return row;
}

/** The accept row of a to-do's open ask, as its Needs you card draws it. Built field by field, never from the ask
 *  itself: `ask.from` is an object (where the task came from), `BriefRow.from` is the name of who asked. */
export function askRow(todo: Pick<Todo, "id" | "title">, ask: TodoAsk, now = Date.now()): BriefRow {
  return { id: todo.id, title: todo.title, waited_s: Math.max(0, (now - Date.parse(ask.asked_at)) / 1000), outcome: ask.outcome, verdict: ask.verdict, detail: ask.summary, evidence_link: ask.link, recommend: ask.recommend, asked_at: ask.asked_at };
}

function readContribution(value: unknown): BriefContribution | undefined {
  if (!value || typeof value !== "object") return undefined;
  const raw = value as Record<string, unknown>;
  const extension_id = text(raw.extension_id);
  const contribution_id = text(raw.contribution_id);
  const title = text(raw.title);
  if (!extension_id || !contribution_id || !title) return undefined;
  const facts = Array.isArray(raw.facts) ? raw.facts.filter((f): f is string => Boolean(text(f))) : [];
  const sections = Array.isArray(raw.sections) ? raw.sections.flatMap((s: unknown) => {
    if (!s || typeof s !== "object" || !("title" in s) || !("body" in s)) return [];
    const title = text(s.title);
    const body = text(s.body);
    return title && body ? [{ title, body }] : [];
  }) : [];
  if (!facts.length && !sections.length) return undefined;
  return { extension_id, contribution_id, title, facts, sections };
}

/** A `brief` item's body, every list a list (a hub that left one out, or an item that is not one, gives nothing to draw). */
export function briefBody(it: FeedItem): BriefBody | undefined {
  const b = it.kind === "brief" ? it.body : undefined;
  if (!b || typeof b !== "object") return undefined;
  const list = <T,>(v: unknown): T[] => (Array.isArray(v) ? (v as T[]) : []);
  const rows = (v: unknown): BriefRow[] => list<unknown>(v).map(briefRow).filter((row): row is BriefRow => !!row);
  const waiting = rows(b.waiting);
  const accept = rows(b.accept);
  const later = rows(b.later);
  const failed = list<BriefFailed>(b.failed);
  const done = list<BriefBody["done"][number]>(b.done);
  const progress = list<BriefBody["progress"][number]>(b.progress);
  const num = (v: unknown, n: number) => (typeof v === "number" ? v : n);
  return {
    title: typeof b.title === "string" ? b.title : "",
    at: typeof b.at === "string" ? b.at : it.at,
    hours: num(b.hours, 12),
    waiting_unknown: b.waiting_unknown === true,
    waiting,
    failed,
    failed_total: num(b.failed_total, failed.length),
    done,
    done_total: num(b.done_total, done.length),
    accept,
    later,
    later_total: num(b.later_total, later.length),
    progress,
    progress_total: num(b.progress_total, progress.length),
    setup: list<BriefSetup>(b.setup).filter((x) => x && typeof x.id === "string" && typeof x.text === "string" && x.text !== ""),
    deferred_supervision: list<BriefBody["deferred_supervision"][number]>(b.deferred_supervision).filter((x) =>
      x && typeof x.id === "string" && typeof x.todo_id === "string" && typeof x.title === "string" && typeof x.reason === "string"),
    contributions: list<unknown>(b.contributions).map(readContribution).filter((c): c is BriefContribution => !!c),
  };
}
