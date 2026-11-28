import { useEffect, useId, useRef, useState } from "react";
import { t } from "../i18n";
import { credentialOwner, onCredentialChange } from "../lib/api";
import { defaultKnown, defaultManager, onManagerChoice, shownManager } from "../lib/manager-choice";
import { getState, onHubEvent, subscribe } from "../lib/store";
import type { Session } from "../lib/types";
import { Button, Dialog, Switch } from "../ui/atoms";
import { ShareSize } from "../ui/share-tokens";
import "../ui/share-tokens.css";
import { SharesClient } from "./client";
import { SharingModel } from "./model";
import type { PublicMessage, Selection } from "./types";

const label = (key: string, params?: Record<string, string | number>) => t(`agent-sharing.${key}`, params);
const reasons = new Set(["ownership_unbound", "owner_enrollment_required", "not_owner", "source_unsupported", "source_incomplete", "source_limit", "authority_unavailable", "manager_unsupported", "public_origin_unavailable", "scope_changed", "lost"]);
export function sharingError(code: string | null) {
  if (!code) return "";
  return label(reasons.has(code) ? code : /conflict|expired|changed|stale/.test(code) ? "conflict" : "failure");
}
function scope(session: Session) {
  const state = getState(); const machine = state.machines.find((m) => m.id === session.machine_id);
  const live = state.sessions[session.machine_id]?.find((s) => s.session_id === session.session_id && (!session.manager || s.manager === session.manager)) ?? session;
  return JSON.stringify({ owner: credentialOwner(), manager: shownManager(), default: defaultManager(), known: defaultKnown(),
    mesh: state.mesh.map((m) => [m.hub_id, m.self]).sort(), machine: machine && [machine.id, machine.machine_uid, machine.manager, machine.hub_id],
    session: [live.machine_id, live.session_id, live.manager, live.source_instance_id] });
}
export default function SharingDialog({ session, onClose }: { session: Session; onClose: () => void }) {
  const [, redraw] = useState(0); const modelRef = useRef<SharingModel | null>(null);
  const [model, setModel] = useState<SharingModel | null>(null);
  const [confirmation, setConfirmation] = useState<"save" | "revoke" | null>(null);
  const [copied, setCopied] = useState(false); const [copyFailed, setCopyFailed] = useState(false);
  const publicURL = model?.state.publicURL;
  useEffect(() => { setCopied(false); setCopyFailed(false); }, [publicURL]);
  const props = useRef({ session, onClose }); props.current = { session, onClose };
  useEffect(() => {
    const original = scope(session);
    const machine = getState().machines.find((m) => m.id === session.machine_id);
    const conflict = !!session.manager && !!machine?.manager && session.manager !== machine.manager;
    const manager = conflict ? "" : session.manager ?? machine?.manager ?? (defaultKnown() ? defaultManager() : "");
    const client = new SharesClient(manager, session.machine_id, session.session_id, () => scope(props.current.session) === original);
    const next = new SharingModel(client, () => redraw((n) => n + 1));
    modelRef.current = next; setModel(next); void next.list();
    const retire = () => { next.dispose(); props.current.onClose(); };
    const check = () => { if (!client.valid()) retire(); };
    const offs = [onCredentialChange(retire), onManagerChoice(retire), subscribe(check), onHubEvent("hello", retire), onHubEvent("managers", retire)];
    const storage = () => { credentialOwner(); check(); }; window.addEventListener("storage", storage);
    return () => { offs.forEach((off) => off()); window.removeEventListener("storage", storage); next.dispose(); modelRef.current = null; };
  }, [session.machine_id, session.session_id, session.manager]);
  const close = () => { if (!model?.state.busy) { model?.dispose(); onClose(); } };
  const state = model?.state;
  const deadlines = [state?.preview?.expires_at, state?.catalog?.expires_at].filter((value): value is string => !!value).map(Date.parse);
  const validityDeadline = Math.min(...deadlines);
  useEffect(() => {
    const remaining = validityDeadline - Date.now();
    if (!Number.isFinite(remaining) || remaining <= 0) return;
    const timer = setTimeout(() => redraw((n) => n + 1), Math.min(remaining + 1, 2_147_483_647));
    return () => clearTimeout(timer);
  }, [validityDeadline]);
  const copy = async () => {
    const current = modelRef.current; const url = current?.state.publicURL; if (!url || !current?.client.valid()) return;
    try { await navigator.clipboard.writeText(url); if (current === modelRef.current && current.client.valid()) setCopied(true); }
    catch { if (current === modelRef.current && current.client.valid()) setCopyFailed(true); }
  };
  return <>
    <Dialog open onClose={close} title={label("title")} width={ShareSize.sheet} footer={<div className="share-buttons">
      {state && state.stage !== "list" && state.stage !== "created" && <Button onClick={() => void model?.list()} disabled={state.busy}>{label("back")}</Button>}
      {state?.stage === "list" && <Button tone="primary" onClick={() => void model?.beginCreate()} disabled={state.busy}>{label("new")}</Button>}
      {(state?.stage === "create" || state?.stage === "edit") && <>
        {model?.needsPreview() && <Button onClick={() => void model.preview()} disabled={state.busy || !model.policy() || state.removed.length > 0}>{label("preview")}</Button>}
        <Button tone="primary" disabled={state.busy || !model?.canSave()} onClick={() => state.stage === "create" ? void model?.save() : setConfirmation("save")}>{label(state.stage === "create" ? "create" : "save")}</Button>
      </>}
      <Button onClick={close} disabled={!!state?.busy}>{label("done")}</Button>
    </div>}>
      <div className="share-stack">
        <p className="share-label">{label("intro")}</p>
        {state?.error && <div role="alert" className="share-error share-text">{sharingError(state.error)} <Button disabled={state.busy} onClick={() => state.stage === "edit" && state.target ? void model?.edit(state.target.share_id) : state.stage === "create" ? void model?.refreshCatalog() : void model?.list()}>{label("refresh")}</Button></div>}
        {(!state || state.busy) && <div role="status" className="share-label">{t("common.loading")}</div>}
        {state?.stage === "list" && <section className="share-stack">
          <h3>{label("links")}</h3>
          {!state.shares.length && !state.busy && <p className="share-label">{label("empty")}</p>}
          {state.shares.map((share) => <div key={share.share_id} className="share-box share-row">
            <div className="min-w-0 flex-1"><strong>{label(share.status)}</strong><div className="share-label">{label(share.publication_status)} · {label("expiry")} {displayTime(share.expires_at)}</div><div className="share-label">{label("selected", { count: share.selection.count })} · {label(share.show_tools ? "tools_on" : "tools_off")}</div></div>
            <Button onClick={() => void model?.edit(share.share_id)} disabled={state.busy}>{label("edit")}</Button>
          </div>)}
        </section>}
        {state?.stage === "created" && <div className="share-stack share-box">
          <h3>{label("created")}</h3><p className="share-label">{label("once")}</p>
          <div className="share-link" data-sharing-link>{state.publicURL}</div>
          <div className="share-buttons"><Button onClick={() => void copy()}>{label("copy")}</Button><Button onClick={() => void model?.list()}>{label("links")}</Button></div>
          {(copied || copyFailed) && <p role="status" className="share-label">{label(copyFailed ? "copy_failed" : "copied")}</p>}
        </div>}
        {(state?.stage === "create" || state?.stage === "edit") && model && <SharingForm model={model} />}
        {state?.stage === "edit" && state.target && <div className="share-buttons"><Button tone="danger" onClick={() => setConfirmation("revoke")} disabled={state.busy || state.target.status !== "active"}>{label("revoke")}</Button></div>}
      </div>
    </Dialog>
    {confirmation && <Dialog open title={label(confirmation === "save" ? "confirm_save" : "confirm_revoke")} onClose={() => setConfirmation(null)} footer={<div className="share-buttons">
      <Button onClick={() => setConfirmation(null)} disabled={state?.busy}>{label("cancel")}</Button>
      <Button tone={confirmation === "revoke" ? "danger" : "primary"} disabled={state?.busy || (confirmation === "save" && !model?.canSave())} onClick={() => { const action = confirmation; setConfirmation(null); if (action === "save") void model?.save(); else void model?.revoke(); }}>{label(confirmation === "save" ? "save" : "revoke")}</Button>
    </div>}><p>{label(confirmation === "save" ? "save_help" : "revoke_help")}</p></Dialog>}
  </>;
}
function SharingForm({ model }: { model: SharingModel }) {
  const s = model.state; const expiryID = useId(); const ackID = useId(); const transportID = useId();
  const active = !s.target || s.target.status === "active";
  const blocked = s.busy || !active;
  const disabledWiden = blocked || !s.canWiden || s.removed.length > 0;
  const futureUnavailable = s.stage === "create" || s.replacing ? !s.catalog?.can_include_future : !s.canWiden;
  return <div className="share-stack">
    {s.target && <div className="share-box"><strong>{label(s.target.status)}</strong><p className="share-label">{label(s.target.publication_status)}</p>{s.target.publication_status === "paused" && <p className="share-label">{label("paused_help")}</p>}</div>}
    {s.stage === "create" || s.replacing ? <CatalogSelection model={model} /> : <section className="share-stack">
      <h3>{label("sealed")}</h3><p className="share-label">{label("remove_help")}</p>
      {s.sealed?.messages.map((m) => <MessageChoice key={m.message_id} message={m} type="checkbox" checked={s.removed.includes(m.message_id)} disabled={blocked || s.removed.length >= 1000 && !s.removed.includes(m.message_id)} onChange={() => model.change({ removed: s.removed.includes(m.message_id) ? s.removed.filter((id) => id !== m.message_id) : [...s.removed, m.message_id] })} />)}
      {s.sealed?.next_cursor && <Button disabled={blocked} onClick={() => void model.moreSealed()}>{label("older")}</Button>}
      <div className="share-buttons"><Button disabled={disabledWiden} onClick={() => void model.replaceSelection()}>{label("replace")}</Button>{s.removed.length > 0 && <Button onClick={() => model.change({ removed: [] })} disabled={blocked}>{label("cancel")}</Button>}</div>
      {!s.canWiden && <p className="share-label">{sharingError(s.widenReason || "authority_unavailable")}</p>}
    </section>}
    <div className="share-toggle"><div><strong>{label("future")}</strong><p className="share-label">{label("future_help")}</p></div><Switch label={label("future")} on={s.includeFuture} disabled={blocked || !s.includeFuture && (futureUnavailable || s.removed.length > 0)} onChange={(includeFuture) => model.change({ includeFuture })} /></div>
    <div className="share-toggle"><div><strong>{label("tools")}</strong><p className="share-label">{label("tools_help")}</p></div><Switch label={label("tools")} on={s.showTools} disabled={blocked || !s.showTools && s.stage === "edit" && (!s.canWiden || s.removed.length > 0)} onChange={(showTools) => model.change({ showTools })} /></div>
    <label className="share-stack" htmlFor={expiryID}><strong>{label(s.stage === "create" ? "lifetime" : "expiry")}</strong>
      {s.stage === "create" ? <select id={expiryID} className="share-field" value={s.days} disabled={blocked} onChange={(e) => model.change({ days: Number(e.target.value) as 1 | 3 | 7 | 30 })}>
        {[1, 3, 7, 30].map((days) => <option key={days} value={days}>{label(days === 1 ? "day" : "days", { count: days })}</option>)}
      </select> : <input id={expiryID} className="share-field" type="datetime-local" disabled={blocked} value={localTime(s.expiresAt)} max={s.target ? localTime(isoTime(Date.parse(s.target.created_at) + 30 * 86400000)) || undefined : undefined} onChange={(e) => model.change({ expiresAt: e.target.value ? isoTime(e.target.value) : "" })} />}
      <span className="share-label">{label("cap")}</span>
    </label>
    {s.preview && <section className="share-stack share-box"><h3>{label("preview_title")}</h3>
      <p className="share-warning share-label">{label("warning")}</p>
      <p className="share-label">{label("selected_now", { count: s.preview.selected_count })} · {label("omitted", { count: s.preview.omitted_count })}</p>
      <div className="share-messages">{s.preview.messages.map((m) => <PlainMessage key={m.message_id} message={m} />)}</div>
      {s.preview.next_cursor && <Button disabled={blocked} onClick={() => void model.morePreview()}>{label("older")}</Button>}
      <label htmlFor={ackID} className="share-selection"><input id={ackID} type="checkbox" checked={s.acknowledged} disabled={blocked || !model.previewValid()} onChange={(e) => model.acknowledge(e.target.checked)} /><span>{label("ack")}</span></label>
      {s.preview.requires_insecure_transport_ack && <><p className="share-warning share-label" role="note">{transportWarning(s.preview.transport_warning)}</p><label htmlFor={transportID} className="share-selection"><input id={transportID} type="checkbox" checked={s.transportAcknowledged} disabled={blocked || !model.previewValid()} onChange={(e) => model.acknowledgeTransport(e.target.checked)} /><span>{label("transport_ack")}</span></label></>}
    </section>}
  </div>;
}
/** The hub's localized warning is plain text rendered escaped; a missing or oversized one falls back to the catalog. */
function transportWarning(text: string | null | undefined) { return text && text.length <= 1000 ? text : label("transport_warning"); }
const LOCAL_DT_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?$/;
export function displayTime(iso: string) { const d = new Date(iso); return Number.isFinite(d.getTime()) ? d.toLocaleString() : "—"; }
export function isoTime(raw: string | number) {
  if (typeof raw === "number") { const d = new Date(raw); return Number.isFinite(d.getTime()) ? d.toISOString() : ""; }
  const s = raw.trim(); const m = LOCAL_DT_RE.exec(s);
  if (!m) return "";
  const y = Number(m[1]), mo = Number(m[2]), da = Number(m[3]), hh = Number(m[4]), mi = Number(m[5]), ss = Number(m[6] ?? "0");
  if (hh > 23 || mi > 59 || ss > 59) return "";
  const d = new Date(s);
  return Number.isFinite(d.getTime()) && d.getFullYear() === y && d.getMonth() + 1 === mo && d.getDate() === da ? d.toISOString() : "";
}
export function localTime(iso: string) {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return "";
  const y = d.getFullYear();
  if (y < 0 || y > 9999) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${String(y).padStart(4, "0")}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}
function CatalogSelection({ model }: { model: SharingModel }) {
  const s = model.state, c = s.catalog;
  const selection = s.selection; const group = useId();
  if (!c) return null;
  if (!c.can_share) return <p className="share-label">{sharingError(c.reason || "unavailable")}</p>;
  const modes: Selection["mode"][] = ["all", "from", "individual"];
  const pick = (mode: Selection["mode"]): Selection => mode === "all" ? { mode } : mode === "from" ? { mode, from_message_id: "" } : { mode, message_ids: [] };
  return <section className="share-stack">
    <h3>{label("selection")}</h3><div className="share-row" role="radiogroup" aria-label={label("selection")}>{modes.map((mode) => <label key={mode} className="share-choice"><input type="radio" name={group} checked={selection.mode === mode} disabled={s.busy} onChange={() => model.change({ selection: pick(mode) })} />{label(mode)}</label>)}</div>
    <p className="share-label">{label("total", { count: c.total_messages ?? 0 })} · {label("omitted", { count: c.omitted_messages ?? 0 })}</p>
    {selectedCount(c, selection) !== null && <p className="share-label" role="status" data-sharing-selected>{label("selected_now", { count: selectedCount(c, selection)! })}</p>}
    {!!c.omissions?.length && <div className="share-label"><p>{label("omission_help")}</p><ul className="share-reasons">{[...new Set(c.omissions)].map((reason) => <li key={reason}>{omissionReason(reason)}</li>)}</ul></div>}
    {!c.complete && <p className="share-warning share-label">{label("coverage")}</p>}
    <details className="share-work" open={selection.mode !== "all"}><summary>{label("catalog")}</summary><div>{c.messages.map((m) => selection.mode === "all" ? <PlainMessage key={m.message_id} message={m} /> : <MessageChoice key={m.message_id} message={m} type={selection.mode === "from" ? "radio" : "checkbox"} name={group + "message"} checked={selection.mode === "from" ? selection.from_message_id === m.message_id : selection.message_ids.includes(m.message_id)} disabled={s.busy || !m.selectable || selection.mode === "individual" && selection.message_ids.length >= 1000 && !selection.message_ids.includes(m.message_id)} onChange={() => model.change({ selection: selection.mode === "from" ? { mode: "from", from_message_id: m.message_id } : { mode: "individual", message_ids: selection.mode === "individual" && selection.message_ids.includes(m.message_id) ? selection.message_ids.filter((id) => id !== m.message_id) : [...(selection.mode === "individual" ? selection.message_ids : []), m.message_id] } })} />)}</div>
    {c.next_cursor && <Button disabled={s.busy} onClick={() => void model.moreCatalog()}>{label("older")}</Button>}</details>
  </section>;
}
const omissionReasons = new Set(["unknown_provenance", "unfinished_turn", "unassociated_execution", "component_incomplete", "component_limit"]);
function omissionReason(code: string) { return label(omissionReasons.has(code) ? `omission_${code}` : "omission_other"); }
/** How many messages the selection covers. "From" counts against the complete catalog, whose pages arrive oldest first. */
export function selectedCount(c: { total_messages?: number; messages: { message_id: string }[] }, selection: Selection): number | null {
  if (selection.mode === "all") return c.total_messages ?? c.messages.length;
  if (selection.mode === "individual") return selection.message_ids.length;
  const at = c.messages.findIndex((m) => m.message_id === selection.from_message_id);
  return at < 0 || c.total_messages === undefined ? null : c.total_messages - at;
}
function MessageChoice({ message, type, name, checked, disabled, onChange }: { message: PublicMessage; type: "radio" | "checkbox"; name?: string; checked: boolean; disabled: boolean; onChange: () => void }) {
  const id = useId();
  return <div className="share-selection"><input id={id} type={type} name={name} checked={checked} disabled={disabled} onChange={onChange} /><label htmlFor={id}><PlainMessage message={message} /></label></div>;
}
export function PlainMessage({ message: m }: { message: PublicMessage }) {
  return <article><div className="share-label">{t(m.role === "user" ? "public-share.user" : "public-share.assistant")}</div><div className="share-text">{m.text}</div>
    {!!m.execution?.length && <details className="share-work"><summary>{label("execution")}</summary>{m.execution.map((item, index) => <div className="share-box" key={index}>{item.kind === "tool" ? <><strong>{item.name}</strong><p>{label("arguments")}</p><pre>{item.arguments}</pre><p>{label("results")}</p>{item.results.map((r, i) => <pre key={i}>{r.text}</pre>)}</> : <>{item.kind === "agent" && <strong>{label("agent_work")}</strong>}<div className="share-text">{item.text}</div></>}</div>)}</details>}
  </article>;
}
