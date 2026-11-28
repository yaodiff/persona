import { onConversationDeleted } from "./conversation-deleted";
// Per-message transport feedback. It never changes session state, list ordering or counts.
import { useSyncExternalStore } from "react";
import { credentialOwner, getToken, onCredentialChange, sessionPath } from "./api";
import { BASE } from "./base";
import { activityOf } from "./format";
import { MANAGER_HEADER, managerHeaderFor, onManagerChoice, shownManager } from "./manager-choice";
import type { Item, Session } from "./types";
import { sameMessage } from "./echo";
import { timeNs } from "./times";

interface FeedbackDeps {
  getSession(mid: string, sid: string): Session | undefined;
  send(msg: Record<string, unknown>): void;
  onHubEvent(type: string, listener: (msg: Record<string, unknown>) => void): unknown;
  subscribe(listener: () => void): unknown;
  receiptsAllowed?(): boolean;
  watchCursor?(mid: string, sid: string): number | undefined;
}
let deps: Pick<FeedbackDeps, "getSession" | "send" | "receiptsAllowed" | "watchCursor"> = { getSession: () => undefined, send: () => {} };

export type SendFeedback = "sending" | "processing" | "unknown" | "";
type Phase = "sending" | "processing" | "delivered" | "failed" | "unknown";
interface Flight {
  mid: string; sid: string; id: string; scope: string; phase: Phase; feedback: SendFeedback;
  at?: string; echoed?: boolean; settled?: boolean; retry?: boolean; retired?: boolean; text?: string;
  since?: string; state?: string; failure?: string; work?: string; baseSeq?: number; queued: string[]; timer?: ReturnType<typeof setTimeout>;
}
const observed = new Map<string, { scope: string; mid: string; sid: string; seq: number; done: boolean }>();
const pages = new Map<string, { scope: string; seq: number }>();
const flights = new Map<string, Flight>();
// Pending requests are not a UI cache: eviction must not cancel dispatch or discard its HTTP result.
const requests = new Map<string, Flight>();
const listeners = new Set<() => void>();
let manager: string | undefined;
let listening = false;
let managerGeneration = 0;
function listenOwners() {
  if (listening) return;
  listening = true;
  manager = shownManager();
  onCredentialChange(resetMessageFeedback);
  onManagerChoice(() => {
    if (manager !== shownManager()) { manager = shownManager(); managerGeneration++; requests.clear(); }
    for (const e of flights.values()) if (!current(e)) end(e);
    publish();
  });
}
const key = (mid: string, sid: string, id: string) => JSON.stringify([mid, sid, id]);
const scope = () => JSON.stringify([globalThis.location?.origin, BASE, credentialOwner(), shownManager(), managerGeneration]);
function scopedObservation(k: string, owner = scope()) {
  const item = observed.get(k);
  return item?.scope === owner ? item : undefined;
}
const publish = () => { handoffAuthority(); listeners.forEach((f) => f()); };
const owned = (e: Flight) => e.scope === scope() && !e.retired;
const current = (e: Flight) => owned(e) && flights.get(key(e.mid, e.sid, e.id)) === e;
function reconcile(e: Flight, openOnly = false) {
  const cursor = deps.watchCursor?.(e.mid, e.sid);
  if (openOnly && cursor !== undefined) return;
  deps.send({ type: "subscribe", machine_id: e.mid, session_id: e.sid, ...(cursor ? { since_seq: cursor } : {}), lazy_images: true, fold_work: true });
}
function end(e: Flight) { clearTimeout(e.timer); e.feedback = ""; }
function wait(e: Flight) {
  clearTimeout(e.timer);
  e.timer = setTimeout(() => {
    if (!current(e) || !e.feedback) return;
    if (e.echoed) { e.settled = true; end(e); publish(); return; }
    e.feedback = "unknown";
    reconcile(e); // read the transcript; never retry a message on the strength of a receipt
    publish();
  }, 60_000);
}
function create(mid: string, sid: string, id: string, old = flights.get(key(mid, sid, id))) {
  if (old) clearTimeout(old.timer);
  if (flights.size >= 256) {
    const first = flights.values().next().value!;
    clearTimeout(first.timer);
    flights.delete(key(first.mid, first.sid, first.id));
  }
  const row = deps.getSession(mid, sid);
  const e: Flight = { mid, sid, id, scope: scope(), phase: "sending", feedback: "sending", since: row?.state_since, state: row && activityOf(row), queued: row?.queued ?? [], failure: row?.failure_reason, at: old?.at, retry: Boolean(old) };
  const page = pages.get(key(mid, sid, ""));
  e.baseSeq = page?.scope === e.scope ? page.seq : undefined;
  e.work = row?.work?.phase;
  if (scopedObservation(key(mid, sid, id), e.scope)?.done) { e.settled = true; e.feedback = ""; }
  flights.set(key(mid, sid, id), e);
  return e;
}
/** What every message send says on its own request (#10661), as client_hello does (lib/api.ts CLIENT_FEATURES): it shows a
 *  message the hub holds for an Agent whose computer is away (lib/undelivered.ts). The hub holds one only for a page that
 *  declares it, and a page whose socket is down has no client_hello to go by. */
const SEND_HEADERS = { "X-Persona-Features": "agent_message_hold" };
/** Capture the request owner before queuing, so quiet retries cannot move to another login or manager. */
export function beginMessageSend(mid: string, sid: string, id: string, text?: string) {
  listenOwners();
  const k = key(mid, sid, id), previous = requests.get(k) ?? flights.get(k);
  if (previous) previous.retired = true;
  const e = create(mid, sid, id, flights.get(k) ?? previous);
  requests.set(k, e);
  e.text = text;
  wait(e);
  publish(); // local Sending precedes network and outbox work
  reconcile(e, true); // the target must be subscribed before POST, including sends from another page
  const manager = managerHeaderFor(sessionPath(mid, sid));
  let pending = true;
  const isCurrent = () => pending && owned(e);
  return {
    current: isCurrent,
    echoed: () => Boolean(e.echoed),
    request: { token: getToken(), tokenOwner: credentialOwner(), isAuthContextCurrent: isCurrent, headers: manager ? { [MANAGER_HEADER]: manager, ...SEND_HEADERS } : SEND_HEADERS },
    finish(phase?: "delivered" | "failed" | "unknown") {
      pending = false;
      if (requests.get(k) === e) requests.delete(k);
      if (!phase || !current(e)) return;
      e.phase = phase;
      if (e.settled || phase === "failed") end(e);
      else { e.feedback = phase === "unknown" ? "unknown" : "processing"; wait(e); }
      publish();
    },
  };
}
/** UTC timestamps retain nanoseconds: Date.parse alone would let stale same-millisecond events through. */
export function receiveMessageSend(msg: Record<string, unknown>) {
  if (!deps.receiptsAllowed?.()) return; // only the current authenticated hub may advertise this reserved type
  const { machine_id: mid, session_id: sid, client_msg_id: id, state, at } = msg;
  if (typeof mid !== "string" || typeof sid !== "string" || typeof id !== "string" || !id || typeof at !== "string") return;
  if (!at.endsWith("Z") || timeNs(at) === null || !["processing", "delivered", "failed", "unknown"].includes(String(state))) return;
  const time = `${at.slice(0, 19)}.${at.slice(20, -1).padEnd(9, "0")}Z`;
  listenOwners();
  if (scopedObservation(key(mid, sid, id))?.done) return;
  let e = flights.get(key(mid, sid, id));
  // Another subscribed client has no local draft. Only processing can open its short-lived feedback line.
  if (!e && state === "processing") e = create(mid, sid, id);
  if (!e || !current(e) || e.echoed || e.settled || (e.at && time <= e.at)) return;
  if (e.phase === "delivered" || e.phase === "failed" || e.phase === "unknown") return;
  // A same-id retry must see its new processing receipt before an old terminal frame may apply.
  if (e.retry && e.phase === "sending" && state !== "processing") return;
  e.at = time;
  e.phase = state as Phase;
  if (state === "failed") end(e);
  else { e.feedback = state === "unknown" ? "unknown" : "processing"; wait(e); }
  if (state === "unknown") reconcile(e);
  publish();
}
export function settleMessageEcho(mid: string, sid: string, id: string, queued = false) {
  const e = requests.get(key(mid, sid, id)) ?? flights.get(key(mid, sid, id));
  if (!e || !owned(e)) return;
  e.echoed = true;
  if (!current(e)) return;
  if (queued) e.settled = true;
  if (e.settled) end(e);
  else { e.feedback = "processing"; wait(e); }
  publish();
}
export function stopMessageFeedback(mid: string, sid: string) {
  for (const e of flights.values()) if (e.mid === mid && e.sid === sid && current(e) && (e.phase === "delivered" || e.echoed)) { e.settled = true; end(e); }
  publish();
}
export function messageFeedback(mid: string, sid: string, id?: string, exclude?: ReadonlySet<string>): SendFeedback | undefined {
  const entries = id !== undefined ? [flights.get(key(mid, sid, id))] : flights.values();
  let feedback: SendFeedback | undefined;
  // Later nonempty feedback wins; keep "" when all matching flights have ended.
  for (const e of entries) if (e && e.mid === mid && e.sid === sid && !exclude?.has(e.id) && current(e)) feedback = e.feedback || feedback || "";
  return feedback;
}
export function useMessageFeedback(mid: string, sid: string, id?: string, exclude?: ReadonlySet<string>) {
  return useSyncExternalStore((f) => { listeners.add(f); return () => { listeners.delete(f); }; }, () => messageFeedback(mid, sid, id, exclude), () => undefined);
}
export function observeMessageItems(mid: string, sid: string, items: Item[]) {
  const owner = scope();
  const pageKey = key(mid, sid, "");
  const old = pages.get(pageKey);
  let seq = old?.scope === owner ? old.seq : 0, assistant = 0;
  for (const item of items) {
    seq = Math.max(seq, item.seq);
    if (item.role === "assistant") assistant = Math.max(assistant, item.seq);
  }
  pages.set(pageKey, { scope: owner, seq });
  if (pages.size > 128) pages.delete(pages.keys().next().value!);
  for (const item of items) if (item.client_msg_id) {
    const id = key(mid, sid, item.client_msg_id);
    const known = scopedObservation(id, owner);
    observed.set(id, { scope: owner, mid, sid, seq: known ? Math.min(known.seq, item.seq) : item.seq, done: known?.done || Boolean(item.queued) || assistant > item.seq });
    settleMessageEcho(mid, sid, item.client_msg_id, item.queued);
  }
  for (const known of observed.values()) if (known.scope === owner && known.mid === mid && known.sid === sid && assistant > known.seq) known.done = true;
  while (observed.size > 256) observed.delete(observed.keys().next().value!);
  let changed = false;
  for (const e of flights.values()) {
    const input = e.text && e.baseSeq !== undefined ? items.find(i => i.seq > e.baseSeq! && i.role === "user" && i.kind === "text" && sameMessage(i.text ?? "", e.text!)) : undefined;
    if (current(e) && e.mid === mid && e.sid === sid && e.feedback && (scopedObservation(key(mid, sid, e.id), owner)?.done || (e.state !== "working" && input && assistant > input.seq))) { e.settled = true; end(e); changed = true; }
  }
  if (changed) publish();
}
export function resetMessageFeedback() {
  managerGeneration++; requests.clear();
  observed.clear(); pages.clear();
  for (const e of flights.values()) clearTimeout(e.timer);
  flights.clear();
  publish();
}
export function configureMessageFeedback(next: FeedbackDeps) {
  deps = next;
  next.onHubEvent("message_send_state", receiveMessageSend);
  next.onHubEvent("items", (msg) => {
    if (!Array.isArray(msg.items)) return;
    observeMessageItems(String(msg.machine_id), String(msg.session_id), msg.items as Item[]);
  });
  next.onHubEvent("hello", () => { for (const e of flights.values()) if (current(e) && e.feedback) reconcile(e, true); });
  next.subscribe(() => { if (handoffAuthority()) publish(); });
}
function handoffAuthority() {
  let changed = false;
  for (const e of flights.values()) {
    if (!e.feedback || !current(e)) continue;
    const row = deps.getSession(e.mid, e.sid);
    if (!row) continue;
    const state = activityOf(row);
    const queued = e.text && (row.queued ?? []).filter(q => sameMessage(q, e.text!)).length > e.queued.filter(q => sameMessage(q, e.text!)).length;
    const fresh = e.state !== "working" && (row.state_since !== e.since || state !== e.state);
    const executing = (fresh || e.phase === "delivered" || e.echoed) && (state === "working" || state === "needs_you");
    const completed = e.state !== "working" && ((row.work?.phase === "done" && e.work !== "done") || (row.failure_reason && row.failure_reason !== e.failure) || (fresh && (state === "offline" || state === "unread")));
    if (queued || executing || completed) { e.settled = true; end(e); changed = true; }
  }
  return changed;
}

onConversationDeleted((mid, sid) => {
  for (const [k, flight] of new Map([...flights, ...requests])) {
    if (flight.mid !== mid || flight.sid !== sid) continue;
    flight.retired = true;
    end(flight);
    flights.delete(k);
    requests.delete(k);
  }
  publish();
});
