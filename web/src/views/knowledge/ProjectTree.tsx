// The knowledge map's "Project tree" view (#12927; lib/kb-tree.ts): the data, the moves (drag, Move to…) with their
// Undo, the node menu (Move to…, Rename, Milestone) and the side panel, which is the project's home: where it sits, the
// projects under it, its milestones and its own yesterday's notes (a parent's are not inherited), with a Report tab
// that a later version fills in.
import { useCallback, useEffect, useRef, useState } from "react";
import { formatDate, t } from "../../i18n";
import { api, enc, errorText, isAbort } from "../../lib/api";
import { type KBDetailResponse } from "../../lib/kb";
import { personaLine, type MapNode } from "../../lib/kb-map";
import {
  canMove, depthOf, MILESTONE_STATES, milestoneStateLabel, moved, SUGGESTED_DEPTH, treeOf, treeStatus,
  type Milestone, type MilestoneState, type ProjectTree, type TreeEntry,
} from "../../lib/kb-tree";
import { navigate } from "../../lib/router";
import { Button, cx, IconButton, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { type MenuItem } from "../../ui/menu-model";
import { SectionTitle, TonePill } from "./KBParts";

// ── data ────────────────────────────────────────────────

/** A parent or milestone change's answer: the update that undoes it, and where the project now sits. */
interface TreeAnswer {
  id: string;
  undo?: { parent?: string; milestones?: Milestone[] };
  tree?: ProjectTree;
}

export function putProject(id: string, body: Record<string, unknown>): Promise<TreeAnswer> {
  return api<TreeAnswer>(`/kb/projects/${enc(id)}`, { method: "PUT", json: body });
}

export interface TreeData {
  list: TreeEntry[] | null;
  /** child → parent, as drawn: the hub's, with a move in flight already applied */
  parents: Map<string, string>;
  error: string;
  /** reads again, quietly: what is drawn stays */
  refresh: () => void;
  /** where the tree shows a project before the hub confirms it (a drag or Move to…) */
  place: (id: string, parent: string | null) => void;
}

/** The project list with parents and milestones, read when the view opens and again on refresh(). */
export function useTreeData(active: boolean, reload: number): TreeData {
  const [list, setList] = useState<TreeEntry[] | null>(null);
  const [parents, setParents] = useState(new Map<string, string>());
  const [error, setError] = useState("");
  const [n, setN] = useState(0);
  useEffect(() => {
    if (!active) return;
    const ac = new AbortController();
    api<{ items: TreeEntry[] | null }>("/kb/projects", { signal: ac.signal })
      .then((r) => {
        const items = r.items ?? [];
        setList(items);
        setParents(treeOf(items).parents);
        setError("");
      })
      .catch((e) => {
        if (!isAbort(e)) setError(errorText(e));
      });
    return () => ac.abort();
  }, [active, reload, n]);
  const refresh = useCallback(() => setN((x) => x + 1), []);
  const place = useCallback((id: string, parent: string | null) => setParents((p) => moved(p, id, parent)), []);
  return { list, parents, error, refresh, place };
}

const nameOf = (list: TreeEntry[], id: string | null | undefined) => (id ? list.find((e) => e.id === id)?.name ?? id : "");

/** Moves a project (null: to the top level) as the user: at once on the map, then on the hub; an Undo toast, or back
 *  where it was with the hub's reason. */
export async function moveProject(data: TreeData, id: string, target: string | null, quiet = false) {
  const list = data.list ?? [];
  const was = data.parents.get(id) ?? null;
  data.place(id, target);
  try {
    await putProject(id, { parent: target ?? "" });
    if (!quiet) {
      const text = target ? t("knowledge-projecttree.moved_under", { name: nameOf(list, id), parent: nameOf(list, target) }) : t("knowledge-projecttree.moved_top", { name: nameOf(list, id) });
      toast(text, "ok", { label: t("knowledge-projecttree.undo"), run: () => void moveProject(data, id, was, true) });
    }
  } catch (e) {
    data.place(id, was);
    toast(t("knowledge-projecttree.move_failed", { reason: errorText(e) }), "error");
  }
  data.refresh();
}

/** Saves a milestone change, with an Undo that puts the list back as it was. */
export async function saveMilestone(data: TreeData, id: string, body: Record<string, unknown>, done: string, after?: () => void) {
  try {
    const r = await putProject(id, body);
    const undo = r.undo;
    toast(done, "ok", undo?.milestones ? { label: t("knowledge-projecttree.undo"), run: () => void putProject(id, { milestones: undo.milestones }).then(() => { data.refresh(); after?.(); }, (e) => toast(errorText(e), "error")) } : undefined);
  } catch (e) {
    toast(errorText(e), "error");
  }
  data.refresh();
  after?.();
}

// ── node menu ───────────────────────────────────────────

/** Move to…: the top level, then every other project as it sits in the tree; the ones it can't go to say why. */
export function moveTargets(data: TreeData, id: string): MenuItem[] {
  const list = data.list ?? [];
  const { live } = treeOf(list);
  const order: { e: TreeEntry; depth: number }[] = [];
  const kids = new Map<string, TreeEntry[]>();
  for (const e of [...live].sort((a, b) => a.name.localeCompare(b.name))) {
    const p = data.parents.get(e.id) ?? "";
    kids.set(p, [...(kids.get(p) ?? []), e]);
  }
  const walk = (p: string, depth: number) => {
    for (const e of kids.get(p) ?? []) {
      order.push({ e, depth });
      walk(e.id, depth + 1);
    }
  };
  walk("", 0);
  const row = (target: string | null, label: string, depth: number): MenuItem => {
    const v = canMove(data.parents, id, target);
    return {
      key: target ?? "", label: `${" ".repeat(Math.min(depth, 4))}${label}`, disabled: !v.ok, detail: v.ok ? v.warn : v.why,
      onSelect: () => void moveProject(data, id, target),
    };
  };
  return [row(null, t("knowledge-projecttree.top_level"), 0), ...order.filter((x) => x.e.id !== id).map((x) => row(x.e.id, x.e.name, x.depth + 1))];
}

/** A project node's menu (right click, long press, the panel's ⋯): Move to…, Rename, Add a milestone. */
export function nodeMenu(data: TreeData, e: TreeEntry, onMilestone: () => void): MenuItem[] {
  return [
    { key: "move", label: t("knowledge-projecttree.move_to"), icon: "arrowRight", submenu: true, children: moveTargets(data, e.id), onSelect: () => {} },
    {
      key: "rename", label: t("knowledge-projecttree.rename"), icon: "pencil", onSelect: () => {},
      rename: {
        value: e.name,
        commit: async (name) => {
          await putProject(e.id, { name });
          data.refresh();
        },
      },
    },
    { key: "milestone", label: t("knowledge-projecttree.add_milestone"), icon: "flag", onSelect: onMilestone },
  ];
}

// ── panel: the project's home ───────────────────────────

/** Its own timeline notes from yesterday (the main chat's lines), never its parent's. */
export function yesterdayNotes(log: string[] | null | undefined, now = new Date()): string[] {
  const y = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
  const day = `${y.getFullYear()}-${String(y.getMonth() + 1).padStart(2, "0")}-${String(y.getDate()).padStart(2, "0")}`;
  return (log ?? []).map(personaLine).filter((l) => l?.when.startsWith(day)).map((l) => l!.text);
}

export function ProjectHome({ node, data, onOpen, onMenu, milestoneAsk }: {
  node: MapNode;
  data: TreeData;
  /** select a project on the map */
  onOpen: (id: string) => void;
  onMenu: (el: HTMLElement) => void;
  /** bumped to open the new-milestone form (the node menu's Add a milestone) */
  milestoneAsk: number;
}) {
  const e = node.project!;
  const [tab, setTab] = useState<"home" | "report">("home");
  const [detail, setDetail] = useState<{ tree?: ProjectTree; log: string[] } | null>(null);
  const [failed, setFailed] = useState("");
  const [n, setN] = useState(0);
  const change = useRef<HTMLButtonElement>(null);
  useEffect(() => setTab("home"), [e.id]);
  useEffect(() => {
    const ac = new AbortController();
    setFailed("");
    api<KBDetailResponse & { tree?: ProjectTree }>(`/kb/projects/${enc(e.id)}`, { signal: ac.signal })
      .then((r) => setDetail({ tree: r.tree, log: r.detail.log ?? [] }))
      .catch((x) => { if (!isAbort(x)) setFailed(errorText(x)); });
    return () => ac.abort();
  }, [e.id, n, data.list]);
  const again = () => setN((x) => x + 1);
  const list = data.list ?? [];
  const tree = detail?.tree;
  // Where it sits as drawn (a move in flight shows at once); the hub's tree for its milestones.
  const way: string[] = [];
  for (let at = data.parents.get(e.id); at && !way.includes(at) && way.length < 32; at = data.parents.get(at)) way.unshift(at);
  const depth = depthOf(data.parents, e.id);
  const children = list.filter((x) => data.parents.get(x.id) === e.id).sort((a, b) => a.name.localeCompare(b.name));
  const milestones = tree?.milestones ?? e.milestones ?? [];
  const notes = yesterdayNotes(detail?.log);
  const parent = data.parents.get(e.id) ?? null;
  return (
    <div className="p-5" data-project-home={e.id}>
      {way.length > 0 && (
        <nav aria-label={t("knowledge-projecttree.path")} className="mb-1 flex flex-wrap items-center gap-1 text-label text-ink-3">
          {way.map((id) => (
            <button key={id} type="button" onClick={() => onOpen(id)} className="hover:text-accent-ink">{nameOf(list, id)} ›</button>
          ))}
        </nav>
      )}
      <div className="flex items-start gap-2">
        <h2 className="min-w-0 flex-1 text-lg font-semibold leading-snug text-ink">{e.name}</h2>
        <IconButton icon="more" label={t("knowledge-projecttree.actions")} size={28} onClick={(ev) => onMenu(ev.currentTarget)} />
      </div>
      <div className="mt-2 mb-3 flex flex-wrap gap-1.5">
        <TonePill>{t(`knowledge-projecttree.status_${["done", "paused"].includes(treeStatus(e)) ? treeStatus(e) : "active"}`)}</TonePill>
        {e.importance && <TonePill>{e.importance}</TonePill>}
        {tree?.parent_locked && <TonePill title={t("knowledge-projecttree.locked_detail")}>{t("knowledge-projecttree.locked")}</TonePill>}
      </div>
      <div role="tablist" aria-label={t("knowledge-projecttree.tabs")} className="mb-4 inline-flex rounded-control bg-inset p-0.5 shadow-[inset_0_0_0_1px_var(--line)]">
        {(["home", "report"] as const).map((x) => (
          <button key={x} type="button" role="tab" aria-selected={tab === x} onClick={() => setTab(x)}
            className={cx("h-7 whitespace-nowrap rounded-[7px] px-3 text-meta", tab === x ? "bg-surface font-semibold text-ink shadow-btn" : "text-ink-2 hover:text-ink")}>
            {t(x === "home" ? "knowledge-projecttree.tab_home" : "knowledge-projecttree.tab_report")}
          </button>
        ))}
      </div>
      {tab === "report" ? (
        <div className="rounded-[12px] bg-inset px-3.5 py-3 text-meta leading-relaxed text-ink-2" data-report-placeholder="">
          <div className="mb-0.5 flex items-center gap-1.5 text-label font-semibold text-ink"><Icon name="chart" size={13} />{t("knowledge-projecttree.report_title")}</div>
          {t("knowledge-projecttree.report_later")}
        </div>
      ) : (
        <>
          {e.one_line && <p className="mb-4 text-meta leading-relaxed text-ink">{e.one_line}</p>}
          <section className="mb-4">
            <SectionTitle title={t("knowledge-projecttree.parent")} />
            <div className="flex items-center gap-2 px-1 text-meta">
              {parent ? (
                <button type="button" onClick={() => onOpen(parent)} className="min-w-0 truncate text-ink hover:text-accent-ink">{nameOf(list, parent)}</button>
              ) : <span className="text-ink-3">{t("knowledge-projecttree.top_level")}</span>}
              <span className="flex-1" />
              <Button tone="secondary" size="sm" ref={change} onClick={() => change.current && onMenu(change.current)}>{t("knowledge-projecttree.change")}</Button>
            </div>
            {depth > SUGGESTED_DEPTH && <p className="mt-1.5 px-1 text-label text-attention">{t("knowledge-projecttree.deep_hint", { levels: depth, suggested: SUGGESTED_DEPTH })}</p>}
          </section>
          <section className="mb-4">
            <SectionTitle title={t("knowledge-projecttree.children")} count={children.length} />
            {children.length ? children.map((c) => (
              <div key={c.id} className="flex items-center gap-2 border-t border-line py-1.5 pl-1 text-meta">
                <button type="button" onClick={() => onOpen(c.id)} className="flex min-w-0 flex-1 items-center gap-2 text-left text-ink hover:text-accent-ink">
                  <Icon name="folder" size={13} className="shrink-0 text-ink-3" /><span className="truncate">{c.name}</span>
                </button>
                <span className="shrink-0 text-submeta text-ink-3">{t(`knowledge-projecttree.status_${["done", "paused"].includes(treeStatus(c)) ? treeStatus(c) : "active"}`)}</span>
                <Button tone="quiet" size="sm" onClick={() => void moveProject(data, c.id, parent)}>{t("knowledge-projecttree.move_out")}</Button>
              </div>
            )) : <p className="px-1 text-label text-ink-3">{t("knowledge-projecttree.no_children")}</p>}
          </section>
          <Milestones id={e.id} list={milestones} data={data} ask={milestoneAsk} onSaved={again} />
          <section className="mb-4">
            <SectionTitle title={t("knowledge-projecttree.yesterday")} />
            {!detail && !failed ? <Loading /> : failed ? <p className="px-1 text-label text-orange">{failed}</p>
              : notes.length ? (
                <ul className="ml-5 list-disc space-y-1 text-meta leading-relaxed text-ink marker:text-ink-3">{notes.map((x, i) => <li key={i}>{x}</li>)}</ul>
              ) : <p className="px-1 text-label text-ink-3">{t("knowledge-projecttree.no_yesterday")}</p>}
          </section>
          <Button tone="secondary" icon="external" onClick={() => navigate({ name: "kb", kind: "projects", id: e.id })}>{t("knowledge-projecttree.open_page")}</Button>
        </>
      )}
    </div>
  );
}

const STATE_TONE: Record<MilestoneState, "neutral" | "accent" | "orange"> = { open: "neutral", accepted: "accent", not_accepted: "orange" };

function Milestones({ id, list, data, ask, onSaved }: { id: string; list: Milestone[]; data: TreeData; ask: number; onSaved: () => void }) {
  const [editing, setEditing] = useState<string | null>(null); // a milestone's id, "new", or none
  const first = useRef(ask);
  useEffect(() => {
    if (ask !== first.current) setEditing("new");
  }, [ask]);
  useEffect(() => setEditing(null), [id]);
  return (
    <section className="mb-4" data-milestones="">
      <SectionTitle title={t("knowledge-projecttree.milestones")} count={list.length}
        right={editing !== "new" && <Button tone="quiet" size="sm" icon="plus" onClick={() => setEditing("new")}>{t("knowledge-projecttree.add")}</Button>} />
      {list.map((m) => editing === m.id ? (
        <MilestoneForm key={m.id} start={m} onCancel={() => setEditing(null)} onSave={(v) => void saveMilestone(data, id, { milestone: { id: m.id, ...v } }, t("knowledge-projecttree.milestone_saved"), () => { setEditing(null); onSaved(); })}
          onRemove={() => void saveMilestone(data, id, { milestone: { id: m.id, remove: true } }, t("knowledge-projecttree.milestone_removed"), () => { setEditing(null); onSaved(); })} />
      ) : (
        <button key={m.id} type="button" onClick={() => setEditing(m.id)} className="flex w-full items-center gap-2 border-t border-line py-2 pl-1 text-left text-meta text-ink hover:bg-hover">
          <Icon name="flag" size={12} className={cx("shrink-0", m.state === "accepted" ? "text-green" : m.state === "not_accepted" ? "text-orange" : "text-accent-ink")} />
          <span className="min-w-0 flex-1 truncate">{m.title}</span>
          {m.target && <span className="shrink-0 text-submeta text-ink-3 tabular-nums">{formatDate(`${m.target}T12:00:00`, { month: "short", day: "numeric" })}</span>}
          <TonePill tone={STATE_TONE[m.state] ?? "neutral"}>{milestoneStateLabel(m.state)}</TonePill>
        </button>
      ))}
      {!list.length && editing !== "new" && <p className="px-1 text-label text-ink-3">{t("knowledge-projecttree.no_milestones")}</p>}
      {editing === "new" && (
        <MilestoneForm start={{ id: "", title: "", state: "open" }} onCancel={() => setEditing(null)}
          onSave={(v) => void saveMilestone(data, id, { milestone: v }, t("knowledge-projecttree.milestone_added"), () => { setEditing(null); onSaved(); })} />
      )}
    </section>
  );
}

function MilestoneForm({ start, onSave, onCancel, onRemove }: { start: Milestone; onSave: (v: { title: string; target: string; state: MilestoneState }) => void; onCancel: () => void; onRemove?: () => void }) {
  const [title, setTitle] = useState(start.title);
  const [target, setTarget] = useState(start.target ?? "");
  const [state, setState] = useState<MilestoneState>(start.state);
  const field = "h-8 w-full rounded-control bg-field px-2.5 text-meta text-ink outline-none";
  return (
    <form className="mt-1 rounded-[12px] bg-inset p-3" data-milestone-form="" onSubmit={(ev) => { ev.preventDefault(); if (title.trim()) onSave({ title: title.trim(), target, state }); }}>
      <div className="grid grid-cols-[88px_1fr] items-center gap-2 text-label text-ink-3">
        <label htmlFor="ms-title">{t("knowledge-projecttree.ms_title")}</label>
        <input id="ms-title" className={field} value={title} autoFocus onChange={(ev) => setTitle(ev.target.value)} />
        <label htmlFor="ms-target">{t("knowledge-projecttree.ms_target")}</label>
        <input id="ms-target" type="date" className={field} value={target} onChange={(ev) => setTarget(ev.target.value)} />
        <label htmlFor="ms-state">{t("knowledge-projecttree.ms_state")}</label>
        <select id="ms-state" className={field} value={state} onChange={(ev) => setState(ev.target.value as MilestoneState)}>
          {MILESTONE_STATES.map((s) => <option key={s} value={s}>{milestoneStateLabel(s)}</option>)}
        </select>
      </div>
      <div className="mt-3 flex items-center gap-2">
        {onRemove && <Button tone="quiet" size="sm" onClick={onRemove}>{t("knowledge-projecttree.remove")}</Button>}
        <span className="flex-1" />
        <Button tone="secondary" size="sm" onClick={onCancel}>{t("knowledge-projecttree.cancel")}</Button>
        <Button tone="primary" size="sm" type="submit" disabled={!title.trim()}>{t("knowledge-projecttree.save")}</Button>
      </div>
    </form>
  );
}

/** The tree view's root panel: what it is and how to use it. */
export function TreeRootPanel({ data }: { data: TreeData }) {
  const { live } = treeOf(data.list ?? []);
  const nested = live.filter((e) => data.parents.has(e.id)).length;
  const flagged = live.filter((e) => e.milestones?.length).length;
  return (
    <div className="p-5">
      <h2 className="text-lg font-semibold text-ink">{t("knowledge-projecttree.title")}</h2>
      <div className="my-4 grid grid-cols-3 gap-2">
        {([[live.length, t("knowledge-projecttree.kpi_projects")], [nested, t("knowledge-projecttree.kpi_nested")], [flagged, t("knowledge-projecttree.kpi_flagged")]] as const).map(([v, l]) => (
          <div key={l} className="rounded-[12px] bg-inset px-3 py-2.5">
            <div className="text-xl leading-tight font-semibold tabular-nums text-ink">{v}</div>
            <div className="mt-0.5 text-submeta text-ink-3">{l}</div>
          </div>
        ))}
      </div>
      <p className="text-meta leading-relaxed text-ink-2">{t("knowledge-projecttree.about")}</p>
    </div>
  );
}
