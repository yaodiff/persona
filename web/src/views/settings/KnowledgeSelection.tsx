import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { api, credentialOwner, enc, errorText, getToken, subscribeCredentialOwner } from "../../lib/api";
import type { HubConnector } from "../../lib/connectors";
import type { KBDetailResponse, KBList } from "../../lib/kb";
import { groupSelectionRule, moveSelectionLevel, selectionItemKey, validSelection, type KBSelection, type SelectionCandidates, type SelectionItem, type SelectionRules } from "../../lib/kb-selection";
import { defaultManager, MANAGER_HEADER, messageManager } from "../../lib/manager-choice";
import { onHubEvent } from "../../lib/store";
import { Button, Loading } from "../../ui/atoms";
import { ConfigurationManager } from "./ConfigurationManager";
import { KnowledgeTopicsSwitch } from "./KnowledgeTopics";
import { Group, Row, RowFailed, settingsInputClass, Switch } from "./SettingsParts";

const text = (key: string, values?: Record<string, string | number>) => t(`settings-knowledge.${key}`, values);

export function KnowledgeSelection({ manager }: { manager?: string } = {}) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  return <ConfigurationManager manager={manager}>{(target) => <>
    <KnowledgeTopicsSwitch key={`topics/${owner}/${target}`} manager={target} />
    <KnowledgeSelectionEditor key={`${owner}/${target}`} manager={target} owner={owner} />
  </>}</ConfigurationManager>;
}

/** Exported for public-adapter tests; Settings uses the roster and credential keyed wrapper above. */
export function KnowledgeSelectionEditor({ manager, owner }: { manager: string; owner: string }) {
  const [token] = useState(getToken);
  const [selection, setSelection] = useState<KBSelection | null>(null);
  const [saved, setSaved] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [stale, setStale] = useState(false);
  const [notice, setNotice] = useState("");
  const [picker, setPicker] = useState<"chat" | "doc" | null>(null);
  const form = useRef<HTMLFormElement>(null);
  const active = useRef(false);
  const readEpoch = useRef(0);
  const changeEpoch = useRef(0);
  const state = useRef({ dirty: false, saving: false });
  const controllers = useRef(new Set<AbortController>());
  const dirty = selection !== null && JSON.stringify(selection) !== saved;
  state.current = { dirty, saving };
  const current = () => active.current && credentialOwner() === owner && getToken() === token;
  async function request<T>(path: string, method = "GET", json?: unknown): Promise<T> {
    const controller = new AbortController();
    controllers.current.add(controller);
    try { return await api<T>(path, { method, json, token, tokenOwner: owner, headers: { [MANAGER_HEADER]: manager }, signal: controller.signal }); }
    finally { controllers.current.delete(controller); }
  }
  async function load() {
    const epoch = ++readEpoch.current;
    setLoading(true);
    setError("");
    setPicker(null);
    try {
      const next = await request<unknown>("/kb/selection");
      if (!current() || epoch !== readEpoch.current) return;
      if (!validSelection(next)) throw new Error(text("unsupported"));
      setSelection(next);
      setSaved(JSON.stringify(next));
      setStale(false);
      setNotice("");
    } catch (e) { if (current() && epoch === readEpoch.current) setError(errorText(e)); }
    finally { if (current() && epoch === readEpoch.current) setLoading(false); }
  }
  useEffect(() => {
    active.current = true;
    void load();
    const stop = onHubEvent("kb_selection", (msg) => {
      if (!current() || messageManager(msg) !== manager) return;
      changeEpoch.current++;
      // There is no revision/CAS in this API. A dirty draft must be reconciled explicitly.
      if (state.current.dirty || state.current.saving) setStale(true);
      else void load();
    });
    return () => { active.current = false; readEpoch.current++; controllers.current.forEach((c) => c.abort()); stop(); };
    // The wrapper remounts the editor for a different credential or manager.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const edit = (change: (draft: KBSelection) => void) => {
    state.current.dirty = true;
    setSelection((before) => {
      if (!before) return before;
      const next = structuredClone(before);
      change(next);
      return next;
    });
    setNotice("");
  };
  async function save() {
    if (!current() || !selection || !dirty || loading || state.current.saving || stale || !form.current?.reportValidity() || !validSelection(selection)) return;
    setSaving(true);
    state.current.saving = true;
    const changesBeforeSave = changeEpoch.current;
    setError("");
    try {
      const next = await request<unknown>("/kb/selection", "PUT", selection);
      if (!current()) return;
      if (!validSelection(next)) throw new Error(text("unsupported"));
      setSelection(next);
      setSaved(JSON.stringify(next));
      state.current.dirty = false;
      setNotice(text("saved"));
      if (changeEpoch.current !== changesBeforeSave) {
        // A PUT broadcasts too. Clear its echo only after a matching canonical read, with no newer event.
        const changesBeforeRead = changeEpoch.current;
        const canonical = await request<unknown>("/kb/selection");
        if (current() && changesBeforeRead === changeEpoch.current && JSON.stringify(canonical) === JSON.stringify(next)) setStale(false);
      }
    } catch (e) { if (current()) setError(errorText(e)); }
    finally { if (current()) { setSaving(false); state.current.saving = false; } }
  }
  const add = (item: SelectionItem) => {
    if (!current()) return;
    edit((s) => { s.items ??= []; if (!s.items.some((it) => selectionItemKey(it) === selectionItemKey(item))) s.items.push(item); });
    setPicker(null);
  };
  return <form ref={form} onSubmit={(e) => { e.preventDefault(); void save(); }} aria-label={text("title")}>
    <p className="mb-4 text-[13px] leading-relaxed text-ink-3">{text("intro")}</p>
    {loading && <Loading />}
    {error && <Group><RowFailed text={error} retry={text("reload")} onRetry={() => void load()} /></Group>}
    {stale && <Group><p role="alert" className="px-4 py-3 text-[13px] text-orange">{text("external_change")}</p></Group>}
    {selection && <>
      <fieldset disabled={loading || saving}>
        <Group title={text("items")} anchor="knowledge-items" footer={text("items_hint")}>
          {(selection.items ?? []).length === 0 && <p className="px-4 py-3 text-[13px] text-ink-3">{text("no_items")}</p>}
          {(selection.items ?? []).map((item, index) => <div key={selectionItemKey(item)} className="px-4 py-3" data-selection-item={selectionItemKey(item)}>
            <div className="mb-2 flex min-w-0 items-center gap-2">
              <span className="min-w-0 flex-1 break-words text-[13px] font-medium text-ink">{item.name || item.id}</span>
              <Button tone="quiet" onClick={() => edit((s) => { s.items!.splice(index, 1); })}>{text("remove")}</Button>
            </div>
            <p className="mb-2 break-all text-[11.5px] text-ink-3">{text(item.kind)} · {item.source} · {item.id}</p>
            <div className="flex flex-wrap items-center gap-4">
              <Toggle label={text("read")} on={item.read} onChange={(on) => edit((s) => { s.items![index].read = on; if (!on) s.items![index].store = false; })} />
              <Toggle label={text("store")} on={item.store} disabled={!item.read} onChange={(on) => edit((s) => { s.items![index].store = on; })} />
              <Days label={text("days")} value={item.days} onChange={(n) => edit((s) => { s.items![index].days = n; })} />
            </div>
            <p className="mt-2 text-[11.5px] leading-relaxed text-ink-3">{text(!item.read ? "not_read" : item.store ? item.kind === "doc" ? "store_all_doc" : "store_all" : item.kind === "chat" ? "chat_personal" : "doc_rules")}</p>
          </div>)}
          <div className="flex flex-wrap gap-2 px-4 py-3">
            <Button onClick={() => setPicker(picker === "chat" ? null : "chat")}>{text("add_chat")}</Button>
            <Button onClick={() => setPicker(picker === "doc" ? null : "doc")}>{text("add_doc")}</Button>
          </div>
          {picker && <ItemPicker key={picker} kind={picker} request={request} current={current} items={selection.items ?? []} onAdd={add} manager={manager} />}
        </Group>
        {(["messages", "docs"] as const).map((kind) => <RuleEditor key={kind} kind={kind} value={selection[kind]} onChange={(value) => edit((s) => { s[kind] = value; })} />)}
      </fieldset>
      {!validSelection(selection) && <p role="alert" className="mb-3 text-[12.5px] text-red">{text("invalid_fields")}</p>}
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Button type="submit" tone="primary" busy={saving} disabled={!dirty || loading || stale || !validSelection(selection)}>{text("save")}</Button>
        <Button disabled={saving || loading} onClick={() => void load()}>{text(dirty ? "discard_reload" : "reload")}</Button>
        <span role="status" className="text-[12.5px] text-ink-3">{notice || (dirty ? text("unsaved") : "")}</span>
      </div>
    </>}
    <p className="text-[11.5px] leading-relaxed text-ink-3">{text("pruning")}</p>
  </form>;
}

function Days({ label, value, onChange, context = false }: { label: string; value: number | null | undefined; onChange: (n: number | null) => void; context?: boolean }) {
  return <label className="flex min-w-0 flex-wrap items-center gap-2 text-[12px] text-ink-2"><span className="shrink-0">{label}</span>
    <input type="number" min={context ? 0 : 1} max={context ? 200 : 36500} step={1} value={value ?? ""} placeholder={text("default")} onChange={(e) => onChange(e.target.value === "" ? null : Number(e.target.value))} className={`${settingsInputClass} max-w-24 shrink-0`} />
  </label>;
}

function Toggle(props: { label: string; on: boolean; disabled?: boolean; onChange: (on: boolean) => void }) {
  return <span className="flex items-center gap-2 text-[13px] text-ink"><span>{props.label}</span><Switch {...props} /></span>;
}

function RuleEditor({ kind, value, onChange }: { kind: "messages" | "docs"; value: SelectionRules; onChange: (rules: SelectionRules) => void }) {
  const rule = (name: string, field: string, next: number | boolean | null) => onChange({ ...value, rules: { ...value.rules, [name]: { ...value.rules[name], [field]: next } } });
  return <Group title={text(kind)} anchor={`knowledge-${kind}`} footer={<>{text("priority_hint")} {text(kind === "messages" ? "message_days" : "doc_days")}</>}>
    {value.order.map((level, index) => <div key={level.join("/")} className="px-4 py-3" data-selection-level={`${kind}/${index}`}>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="flex-1 text-[12px] font-medium text-ink-3">{text("level", { n: index + 1 })}</span>
        <Button disabled={index === 0} onClick={() => onChange({ ...value, order: moveSelectionLevel(value.order, index, -1) })}>{text("up")}</Button>
        <Button disabled={index === value.order.length - 1} onClick={() => onChange({ ...value, order: moveSelectionLevel(value.order, index, 1) })}>{text("down")}</Button>
      </div>
      {level.map((name) => <div key={name} className="mb-3 last:mb-0" data-selection-rule={`${kind}/${name}`}>
        <div className="flex flex-wrap items-center gap-3">
          <Toggle label={text(`rule_${kind}_${name}`)} on={value.rules[name].on} onChange={(on) => rule(name, "on", on)} />
          <Days label={text("days")} value={value.rules[name].days} onChange={(n) => rule(name, "days", n)} />
          {kind === "messages" && <Days label={text("attachments")} value={value.rules[name].attachment_days} onChange={(n) => rule(name, "attachment_days", n)} />}
        </div>
        {kind === "messages" && name === "mentions" && <Row label={text("include_all")}><Switch label={text("include_all")} on={value.rules[name].include_all ?? true} onChange={(on) => rule(name, "include_all", on)} /></Row>}
        {kind === "messages" && name === "mine" && <div className="mt-2"><Days context label={text("context")} value={value.rules[name].context} onChange={(n) => rule(name, "context", n)} /></div>}
        <div className="mt-2 flex flex-wrap gap-2">
          <Button tone="quiet" disabled={index === 0} onClick={() => onChange({ ...value, order: groupSelectionRule(value.order, name, true) })}>{text("join")}</Button>
          <Button tone="quiet" disabled={level.length === 1} onClick={() => onChange({ ...value, order: groupSelectionRule(value.order, name, false) })}>{text("split")}</Button>
        </div>
      </div>)}
    </div>)}
  </Group>;
}

function ItemPicker({ kind, request, current, items, onAdd, manager }: {
  kind: "chat" | "doc"; request: <T>(path: string) => Promise<T>; current: () => boolean;
  items: SelectionItem[]; onAdd: (item: SelectionItem) => void; manager: string;
}) {
  const [sources, setSources] = useState<HubConnector[]>([]);
  const [source, setSource] = useState("");
  const [choices, setChoices] = useState<SelectionItem[]>([]);
  const [docs, setDocs] = useState<KBList["items"]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(true);
  const [cached, setCached] = useState(false);
  const [query, setQuery] = useState("");
  const epoch = useRef(0);
  useEffect(() => {
    let live = true;
    setBusy(true);
    const read = kind === "chat" ? request<{ connectors: HubConnector[] }>("/connectors") : request<KBList>("/kb/docs");
    void read.then((data) => {
      if (!live || !current()) return;
      if ("connectors" in data) setSources((data.connectors ?? []).filter((c) => (c.manager ?? defaultManager()) === manager && c.capabilities?.includes("history")));
      else setDocs(data.items ?? []);
    }).catch((e) => { if (live && current()) setError(errorText(e)); }).finally(() => { if (live && current()) setBusy(false); });
    return () => { live = false; epoch.current++; };
    // Request ownership is immutable for the lifetime of this picker.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  async function chats(id: string) {
    setSource(id); setChoices([]); setError(""); setCached(false);
    const seq = ++epoch.current;
    if (!id) { setBusy(false); return; }
    setBusy(true);
    try {
      const data = await request<SelectionCandidates>(`/kb/selection/candidates?source=${enc(id)}`);
      if (seq !== epoch.current || !current()) return;
      setCached(data.cached === true);
      setChoices((data.chats ?? []).filter((c) => c.source && c.id).map((c) => ({ source: c.source, kind: "chat", id: c.id, name: c.name, read: true, store: false, days: null })));
    } catch (e) { if (seq === epoch.current && current()) setError(errorText(e)); }
    finally { if (seq === epoch.current && current()) setBusy(false); }
  }
  async function document(id: string, name: string) {
    const seq = ++epoch.current;
    setBusy(true); setError("");
    try {
      const data = await request<KBDetailResponse>(`/kb/docs/${enc(id)}`);
      if (seq !== epoch.current || !current()) return;
      const meta = data.detail?.meta;
      if (typeof meta?.source !== "string" || !meta.source || typeof meta.id !== "string" || !meta.id) throw new Error(text("missing_identity"));
      onAdd({ source: meta.source, kind: "doc", id: meta.id, name, read: true, store: false, days: null });
    } catch (e) { if (seq === epoch.current && current()) setError(errorText(e)); }
    finally { if (seq === epoch.current && current()) setBusy(false); }
  }
  const exists = (item: SelectionItem) => items.some((it) => selectionItemKey(it) === selectionItemKey(item));
  return <div className="px-4 py-3" aria-label={text(kind === "chat" ? "add_chat" : "add_doc")}>
    {kind === "chat" && <label className="mb-3 block text-[12px] text-ink-2">{text("source")}
      <select aria-label={text("source")} value={source} onChange={(e) => void chats(e.target.value)} className={settingsInputClass}>
        <option value="">{text("choose_source")}</option>
        {sources.map((s) => <option key={s.id} value={s.id}>{s.name} · {s.id}</option>)}
      </select>
    </label>}
    <input aria-label={text("search")} placeholder={text("search")} className={settingsInputClass} value={query} onChange={(e) => setQuery(e.target.value)} />
    {busy && <Loading />}
    {error && <p role="alert" className="mt-2 text-[12px] text-red">{error}</p>}
    {cached && <p className="mt-2 text-[12px] text-ink-3">{text("cached")}</p>}
    {!busy && !error && kind === "chat" && sources.length === 0 && <p className="mt-2 text-[12px] text-ink-3">{text("no_sources")}</p>}
    {!busy && <div className="mt-2 max-h-64 overflow-y-auto">
      {kind === "chat" ? choices.filter((it) => `${it.name} ${it.id}`.toLocaleLowerCase().includes(query.toLocaleLowerCase())).map((it) => <Row key={selectionItemKey(it)} label={it.name || it.id} hint={`${it.source} · ${it.id}`} wrapHint><Button disabled={exists(it)} onClick={() => onAdd(it)}>{text(exists(it) ? "added" : "add")}</Button></Row>) : docs?.filter((it) => `${it.name} ${it.id}`.toLocaleLowerCase().includes(query.toLocaleLowerCase())).map((it) => <Row key={it.id} label={it.name} hint={it.id} wrapHint><Button onClick={() => void document(it.id, it.name)}>{text("add")}</Button></Row>)}
      {((kind === "doc" && !docs?.length) || (kind === "chat" && source && choices.length === 0)) && <p className="text-[12px] text-ink-3">{text("no_candidates")}</p>}
    </div>}
  </div>;
}
