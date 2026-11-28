import { conversationDeleted, conversationKeyDeleted, onConversationDeleted } from "./conversation-deleted";
// Messages other pages send for a conversation ("Ask Persona" from "Knowledge", "Hand to AI" from the inbox, a new Persona conversation):
// the page moves to the conversation at once and the message shows there as a sending bubble until the transcript has
// it, as the app's outbox does (AppModel.sendNow / askPersona). A failed one stays with the reason, "Retry" and "Delete",
// where it was sent (placeFailed). The conversation's own composer keeps its local sending bubbles (ChatView); one of
// those that fails comes here (keepFailed), so its words stay in the chat and not in the box, as in the apps (#1122).
import { t } from "../i18n";
import { toast } from "../ui/atoms";
import { api, ApiError, credentialOwner, errorText, getToken, sessionPath } from "./api";
import { pickedLauncher, pickedModel, conversationKey } from "./assistant-chat";
import { showLauncherNote, startConversation } from "./assistant-start";
import { removeStoredValue, storedValue, storeValue } from "./compatibility";
import { uuid } from "./format";
import { conversationPage } from "./conversation-page";
import { navigate } from "./router";
import { getState, refreshAssistant, setOutbox } from "./store";
import { chosenManager, noteConversation, otherManager, ownerHeaders } from "./manager-choice";
import { apiOp, isTransient, OP_OFFLINE_MS, OP_RETRY_MS } from "./ops";
import { echoed, isEcho, type Seen, type Sent } from "./echo";
import { parseUserText, type ChatLine, type UserMessage } from "./turns";
import type { AssistantInfo, Item } from "./types";
import { beginMessageSend, settleMessageEcho } from "./message-send";
import { assertDelivered, isKept, showKept, type KeptReply } from "./undelivered";

export interface OutboxImage {
  mime: string;
  data: string;
}

export interface OutboxEntry {
  /** The client_msg_id: a retry sends the same one, so the hub takes it once. */
  id: string;
  /** It went in another way (the prompt that started a conversation), not with `id`: only its words find it. */
  wordsOnly?: boolean;
  text: string;
  images: OutboxImage[];
  at: number;
  /** Acceptance starts the transcript grace without changing the send time used to match an untagged copy. */
  acceptedAt?: number;
  /** sent: the hub has it (or it started the conversation); the bubble waits for the transcript. */
  state: "sending" | "sent" | "failed";
  error?: string;
  /** The code its failed send was refused with (dialog_open: a dialog had the terminal, #7343). Kept across a reload
   *  (keepOutgoing); cleared once it is sent, retried or settled by a Check. */
  code?: string;
  dialog?: string;
  dialogMachine?: string;
  dialogSession?: string;
  /** The computer's input box holds text (input_draft): what "Clear and send" clears, when it can. */
  clearable?: string;
  /** The hub keeps this id's definite answer, so an ordinary Retry is another operation. */
  newId?: boolean;
  /** Its send ended with delivered "unknown" (no answer in the hub's time, the link dropped while it went): Check reads
   *  the same operation and refreshes the transcript, never another message POST. A reliable copy shows it as sent. */
  inDoubt?: boolean;
  /** Why the computer sent the owner's message through a Claude session's message inbox (relay_reason): an uncertain
   *  one whose session runs an older Persona plugin says what fixes it. */
  relay?: string;
  /** Brought back after a reload: even an unconfirmed send can settle by the transcript's words. */
  restored?: boolean;
  /** A reload recovered the message's words, but its images only lived in the old page. */
  lostImages?: boolean;
  launcher?: string;
  model?: string;
}

/** How long a sent message waits for the transcript before its bubble goes (the transcript may word it differently). */
export const SENT_GRACE_MS = 60_000;

export function outboxKey(mid: string, sid: string) {
  return conversationKey(mid, sid);
}

/** The bubble an entry shows as: contexts as quote cards, the images, like the transcript will. */
export function outboxMessage(e: OutboxEntry): UserMessage {
  return {
    id: `outbox:${e.id}`,
    ...parseUserText(e.text),
    images: e.images.map((image, i): Item => ({ id: `outbox:${e.id}#${i}`, seq: 0, role: "user", kind: "image", image })),
  };
}

type Described = Pick<OutboxEntry, "text" | "images" | "at"> & Partial<Pick<OutboxEntry, "id" | "wordsOnly">>;

const seenAs = (e: Described & Partial<Pick<OutboxEntry, "inDoubt">>): Sent => ({
  id: e.wordsOnly ? undefined : e.id, text: parseUserText(e.text).text, images: e.images, at: e.at, inDoubt: e.inDoubt,
});

/** The transcript has this message: one of its latest user messages, or an item the hub tied to it, is its copy (lib/echo.ts). */
export function arrived(e: Described, users: Seen[]): boolean {
  return users.some((u) => isEcho(seenAs(e), u));
}

/** What stays once the transcript moved: arrived ones go (one copy each), and sent ones past the grace; sending and
 *  failed stay, but a failed one the hub tied a copy to went in after all (delivered unknown, #1121): the copy is it.
 *  One whose send ended in doubt is found by its words too: the hub's tie can come late or not at all (a node slow to
 *  answer wrote the message after the hub stopped waiting), and a failed bubble over a message that went in had the
 *  owner send it again. A refusal (nothing went in) is never settled by words alone.
 *  `users`: the latest user messages and the items the hub tied to a message (taggedSeen). `claimed`: the ones that
 *  already answered one, kept across calls. A definite new-id failure never settles by words alone: its Retry must stay. */
export function settle(list: OutboxEntry[], users: Seen[], now: number, claimed = new Set<string>()): OutboxEntry[] {
  const shown = echoed(list.filter((e) => e.state !== "failed" || (!e.newId && (e.restored || e.inDoubt))), users, seenAs, claimed);
  const tied = new Set(users.flatMap((u) => u.client_msg_id ?? []));
  return list.filter((e) => !shown.has(e) && !(e.state === "failed" && !e.wordsOnly && tied.has(e.id)) && (e.state !== "sent" || now - (e.acceptedAt ?? e.at) < SENT_GRACE_MS));
}

// When this page wrote the messages it sent, by client_msg_id: a failed message goes before the first one written
// after it (placeFailed), by this clock rather than the computer's.
const written = new Map<string, number>();

/** A message this page sends now (ChatView's own, or an outbox entry): its time, for placeFailed. */
export function noteWritten(id: string, at: number) {
  if (written.size >= 500) written.delete(written.keys().next().value!);
  written.set(id, at);
}

/** When a line was written, for placeFailed: an exchange's message (this page's time for one sent from here; else the
 *  computer's, two minutes' slack for its clock, so a near tie leaves the failed message below); none for other lines. */
function lineWritten(line: ChatLine): number | undefined {
  if (line.kind === "supervision") return line.lines.map(lineWritten).find((at) => at !== undefined);
  if (line.kind !== "segment" || !line.seg.user) return undefined;
  const mine = line.seg.user.client_msg_id ? written.get(line.seg.user.client_msg_id) : undefined;
  if (mine !== undefined) return mine;
  const ts = Date.parse(line.seg.user.ts ?? "");
  return Number.isNaN(ts) ? undefined : ts - 120_000;
}

/** Where failed messages go (#1122, ChatLines.placeFailed in the apps): one written before a message sent after it goes
 *  right before the first such message's exchange, so the chat reads in the order things were written. One with nothing
 *  after it, and every message still on its way, stay at the end (`rest`), in the order they were written. `before`: by
 *  the id of the line each goes before. */
export function placeFailed(lines: ChatLine[], list: OutboxEntry[]): { before: Map<string, OutboxEntry[]>; rest: OutboxEntry[] } {
  const ordered = [...list].sort((a, b) => a.at - b.at);
  const before = new Map<string, OutboxEntry[]>();
  if (!ordered.some((e) => e.state === "failed")) return { before, rest: ordered };
  const after = lines.flatMap((line) => {
    const at = lineWritten(line);
    return at === undefined ? [] : [{ id: line.id, at }];
  });
  const rest: OutboxEntry[] = [];
  for (const e of ordered) {
    const next = e.state === "failed" ? after.find((l) => l.at > e.at) : undefined;
    if (next) before.set(next.id, [...(before.get(next.id) ?? []), e]);
    else rest.push(e);
  }
  return { before, rest };
}

/** An uncertain send whose computer said where it is (a Claude session outside Persona's terminals, through its peer
 *  inbox): its own words, not only "not sure it arrived", so the owner knows what to do (approve it on the computer). */
const IN_DOUBT_REASONS: Record<string, string> = {
  peer_held: "lib-outbox.held_on_computer", // a bypass permissions session holds it for approval at the computer
  peer_unconfirmed: "lib-outbox.peer_unconfirmed", // handed over, not taken: held there, or dropped as a repeat
};

/** The code's own words for an uncertain send, when it has some. One the inbox read and the session did not take,
 *  relayed because the session runs a Persona plugin from before busy delivery (plugin_outdated), says that reopening
 *  the session on the computer fixes it (docs/integrations/claude-integration.md "Owner input through the Persona plugin"). */
function inDoubtReason(code: unknown, relay?: unknown): string | undefined {
  if (code === "peer_unconfirmed" && relay === "plugin_outdated") return t("lib-outbox.peer_unconfirmed_old_plugin");
  const key = typeof code === "string" ? IN_DOUBT_REASONS[code] : undefined;
  return key ? t(key) : undefined;
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function dialogOpenText(dialog: unknown): string {
  const title = nonEmptyString(dialog);
  return title ? t("lib-outbox.dialog_open_named", { dialog: title }) : t("lib-outbox.dialog_open");
}

/** Why it didn't go, in words. */
export function failureText(e: unknown): string {
  if (e instanceof ApiError && e.code === "input_draft") return t("lib-outbox.input_draft");
  if (e instanceof ApiError && e.code === "dialog_open") return dialogOpenText(e.body.dialog);
  if (e instanceof ApiError && e.body.delivered === "unknown") return inDoubtReason(e.code, e.body.relay_reason) ?? t("lib-outbox.delivery_unknown");
  return errorText(e);
}

/** What a kept entry's bubble says, using the page's current language for a dialog or a known uncertain send. */
export function entryReason(e: Pick<OutboxEntry, "error" | "code" | "inDoubt" | "relay" | "dialog" | "lostImages">): string {
  if (e.code === "dialog_open" && !e.inDoubt) {
    const reason = dialogOpenText(e.dialog);
    return e.lostImages && e.error ? `${reason} (${e.error})` : reason;
  }
  return (e.inDoubt ? inDoubtReason(e.code, e.relay) : undefined) ?? e.error ?? "";
}

/** Held for approval on the computer (peer_held): not a failure, the owner's step there is what is left. */
export function heldOnComputer(e: Pick<OutboxEntry, "code" | "inDoubt">): boolean {
  return Boolean(e.inDoubt) && e.code === "peer_held";
}

// ── the store's side ────────────────────────────────────

function list(key: string): OutboxEntry[] {
  return getState().outbox[key] ?? [];
}

/** Keep messages until their transcript copy arrives, they expire, or the user deletes them. */
function write(key: string, next: OutboxEntry[]) {
  if (conversationKeyDeleted(key)) return;
  const before = list(key);
  setOutbox(key, next);
  for (const e of before) if (!next.some((x) => x.id === e.id)) forgetOutgoing(key, e.id);
  for (const e of next) {
    const previous = before.find((x) => x.id === e.id);
    if (previous === e) continue; // another tab may already have settled this entry
    if (e.state === "sent" && previous) acceptOutgoing(key, e.id, e.acceptedAt ?? e.at);
    else keepOutgoing(key, e);
  }
}

function update(key: string, id: string, patch: Partial<OutboxEntry>) {
  write(key, list(key).map((e) => (e.id === id ? { ...e, ...patch } : e)));
}

const claimedBy = new Map<string, Set<string>>();

/** A fresh page for the tests: which transcript lines answered a sent message is kept for as long as the page runs
 *  (one answers no later message with the same words), so cases with the same chat and line ids start without it. */
export function resetOutboxClaimsForTests() {
  claimedBy.clear();
}

/** Drops what the transcript now has (ChatView calls it with the latest user messages and the tagged items). */
export function settleOutbox(mid: string, sid: string, users: Seen[]) {
  const key = outboxKey(mid, sid);
  const before = list(key);
  if (!before.length) return;
  const claimed = claimedBy.get(key) ?? new Set<string>();
  claimedBy.set(key, claimed);
  const after = settle(before, users, Date.now(), claimed);
  if (after.length !== before.length) {
    for (const e of before) if (!after.includes(e) && arrived(e, users)) settleMessageEcho(mid, sid, e.id);
    write(key, after);
  }
}

const clearFailure = { error: undefined, code: undefined, dialog: undefined, dialogMachine: undefined, dialogSession: undefined };
type FailureDetails = Pick<OutboxEntry, keyof typeof clearFailure>;

function failureDetails(err: unknown): FailureDetails {
  const apiError = err instanceof ApiError ? err : undefined;
  const body = apiError?.code === "dialog_open" ? apiError.body : undefined;
  return {
    error: failureText(err),
    code: apiError?.code,
    dialog: nonEmptyString(body?.dialog),
    dialogMachine: nonEmptyString(body?.machine_id),
    dialogSession: nonEmptyString(body?.session_id),
  };
}

/** A failed entry's fields for the error its send ended with. */
function failedWith(err: unknown): FailureDetails & Pick<OutboxEntry, "state" | "clearable" | "newId" | "inDoubt" | "relay"> {
  const inDoubt = err instanceof ApiError && err.body.delivered === "unknown";
  const relay = err instanceof ApiError && typeof err.body.relay_reason === "string" ? err.body.relay_reason : undefined;
  return {
    ...failureDetails(err),
    relay,
    // the hub stopped waiting (delivered "unknown"): it may have gone in, and its copy in the transcript settles it (settle)
    inDoubt,
    state: "failed",
    clearable: err instanceof ApiError && err.code === "input_draft" && typeof err.body.clearable === "string" ? err.body.clearable : undefined,
    // A definite answer stays on this id, so its ordinary Retry is another operation. An uncertain send has no Retry:
    // Check queries the original operation instead (a network gap with no answer still retries with the same id).
    newId: !inDoubt && !isTransient(err),
  };
}

/** A message this conversation's own composer sent that did not go (ChatView): from now on a failed bubble here, with
 *  why, "Retry" and "Delete", where it was sent; the box stays empty (#1122). */
export function keepFailed(mid: string, sid: string, sent: Pick<OutboxEntry, "id" | "text" | "images" | "at" | "launcher" | "model">, err: unknown) {
  const key = outboxKey(mid, sid);
  write(key, [...list(key).filter((e) => e.id !== sent.id), { ...sent, ...failedWith(err) }]);
}

// ── one conversation's sends, in order ──────────────────

const sendTurns = new Map<string, Promise<void>>();
/** As long as a send can honestly take (a gap waited out, a session resuming): a POST still open after this hung (a send
 *  has no fetch timeout), and the later ones stop waiting for it. */
const SEND_TURN_MAX_MS = OP_OFFLINE_MS + OP_RETRY_MS;

/** Runs `post` once the conversation's earlier sends have settled, however they ended: two POSTs in flight can reach the
 *  hub in either order. With none before it, it starts at once. */
export function inSendOrder<T>(key: string, post: () => Promise<T>): Promise<T> {
  const before = sendTurns.get(key);
  const turn = before ? Promise.race([before, new Promise<void>((done) => setTimeout(done, SEND_TURN_MAX_MS))]).then(post) : post();
  const settled = turn.then(() => {}, () => {});
  sendTurns.set(key, settled);
  void settled.then(() => {
    if (sendTurns.get(key) === settled) sendTurns.delete(key);
  });
  return turn;
}

// ── kept across reloads ─────────────────────────────────

const KEPT_PREFIX = "outbox-msg:";
const keptKey = (key: string, id: string) => `${KEPT_PREFIX}${key}/${id}`;
type Outgoing = Pick<OutboxEntry, "id" | "text" | "images" | "at"> & Partial<OutboxEntry>;

// In-memory image payloads (and image-only sends that have no text to persist) kept across client-side
// conversation switches in the same tab, keyed by keptKey(key, id). Never written to localStorage.
const keptImages = new Map<string, OutboxImage[]>();
const keptImageOnly = new Map<string, Outgoing>();
const cloneImages = (images: OutboxImage[]): OutboxImage[] => images.map(({ mime, data }) => ({ mime, data }));

/** Keeps one message's words until its transcript copy arrives (pictures stay in memory for same-tab navigation,
 *  never in localStorage where they would fill the quota). Storage can be full or refused; the message then lasts
 *  only as long as the page. */
export function keepOutgoing(key: string, e: Outgoing) {
  if (conversationKeyDeleted(key)) return;
  const hasText = Boolean(e.text.trim());
  const hasImages = e.images.length > 0;
  if (!hasText && !hasImages) return;
  const storageKey = keptKey(key, e.id);
  if (!hasText) {
    keptImageOnly.set(storageKey, { ...e, images: cloneImages(e.images) });
    keptImages.set(storageKey, cloneImages(e.images));
    return;
  }
  keptImageOnly.delete(storageKey);
  try {
    storeValue(storageKey, JSON.stringify({
      id: e.id, text: e.text, at: e.at, launcher: e.launcher, model: e.model, newId: e.newId, inDoubt: e.inDoubt,
      error: e.error, code: e.code, dialog: e.dialog, dialogMachine: e.dialogMachine, dialogSession: e.dialogSession,
      relay: e.relay, state: e.state, acceptedAt: e.acceptedAt, wordsOnly: e.wordsOnly, clearable: e.clearable, images: e.images.length,
    }));
    if (hasImages) keptImages.set(storageKey, cloneImages(e.images));
    else keptImages.delete(storageKey);
  } catch {
    keptImages.delete(storageKey);
    // only this page's copy then
  }
}

/** HTTP acceptance keeps the bubble pending across reloads, but never revives a copy already settled by an echo
 *  (possibly in another tab) before the HTTP response arrived. */
export function acceptOutgoing(key: string, id: string, acceptedAt = Date.now()) {
  if (conversationKeyDeleted(key)) return;
  // Navigation can restore the message while its original page's POST is still open. Update that existing bubble
  // too; the original component's setPending no longer reaches it. An already settled entry stays absent.
  const entries = list(key);
  if (entries.some((e) => e.id === id && e.state !== "sent")) {
    setOutbox(key, entries.map((e) => e.id === id ? { ...e, ...clearFailure, state: "sent", acceptedAt, clearable: undefined, newId: undefined, inDoubt: undefined } : e));
  }
  const storageKey = keptKey(key, id);
  const mem = keptImageOnly.get(storageKey);
  if (mem) {
    keptImageOnly.set(storageKey, { ...mem, ...clearFailure, state: "sent", acceptedAt, clearable: undefined, newId: undefined, inDoubt: undefined });
  }
  try {
    const saved = storedValue(storageKey);
    if (saved === null) return;
    storeValue(storageKey, JSON.stringify({ ...JSON.parse(saved), ...clearFailure, state: "sent", acceptedAt, newId: undefined, inDoubt: undefined }));
  } catch {
    // storage unavailable: keep only the current page's bubble
  }
}

export function forgetOutgoing(key: string, id: string) {
  const storageKey = keptKey(key, id);
  keptImages.delete(storageKey);
  keptImageOnly.delete(storageKey);
  try {
    removeStoredValue(storageKey);
  } catch {
    // nothing to clear
  }
}

function keptEntries(key: string): (Outgoing & { lostImages: boolean })[] {
  const out: (Outgoing & { lostImages: boolean })[] = [];
  const seen = new Set<string>();
  const prefix = keptKey(key, "");
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const name = localStorage.key(i);
      if (!name?.startsWith(prefix)) continue;
      const { images, ...k } = JSON.parse(storedValue(name) ?? "null") as Omit<Outgoing, "images"> & { images?: number };
      if (k.id && typeof k.text === "string") {
        const kept = keptImages.get(name);
        out.push({ ...k, images: kept ? cloneImages(kept) : [], lostImages: Boolean(images) && !kept?.length });
        seen.add(k.id);
      }
    }
  } catch {
    // storage unavailable or an unreadable entry: nothing to bring back from localStorage
  }
  for (const [name, k] of keptImageOnly) {
    if (!name.startsWith(prefix) || seen.has(k.id)) continue;
    out.push({ ...k, images: cloneImages(k.images), lostImages: false });
  }
  return out;
}

const restored = new Set<string>();

/** After a reload: accepted messages return as sent, without resending; unconfirmed ones as failed with Retry (or Check
 *  when their outcome was uncertain) and Delete, once the transcript is in, so a message that did reach the hub is
 *  settled (settle) before it shows.
 *  `inFlight`: ids this page is sending right now, which already have their bubble. */
export function restoreOutbox(mid: string, sid: string, inFlight: string[] = []) {
  if (conversationDeleted(mid, sid)) return;
  const key = outboxKey(mid, sid);
  const first = !restored.has(key);
  restored.add(key);
  const back = keptEntries(key).filter((k) => {
    // Only the first visit recovers unconfirmed/failed sends. A later transcript update must not turn another tab's
    // live POST into a failed bubble with Retry. Accepted messages can return whenever this conversation is revisited.
    if (k.state !== "sent" && !first) return false;
    if (inFlight.includes(k.id) || list(key).some((e) => e.id === k.id)) return false;
    // Preserve the original grace across reloads: a transcript may never echo a command verbatim, and retaining it
    // indefinitely would leave a stale pending bubble and stored text forever. Unconfirmed sends still need Retry.
    if (k.state === "sent" && Date.now() - (k.acceptedAt ?? k.at) >= SENT_GRACE_MS) {
      forgetOutgoing(key, k.id);
      return false;
    }
    return true;
  });
  if (!back.length) return;
  const entries = back.map(({ lostImages, images, ...k }): OutboxEntry => ({
    ...k, images, restored: true,
    ...(k.state === "sent"
      ? { ...clearFailure, state: "sent" }
      : { state: "failed", lostImages, error: lostImages ? t("lib-outbox.not_confirmed_images") : k.error ?? t("lib-outbox.not_confirmed") }),
  }));
  setOutbox(key, [...list(key), ...entries]);
}

/** A fresh page for the tests: every conversation can restore its unconfirmed messages once again. */
export function resetRestoredForTests() {
  restored.clear();
  keptImages.clear();
  keptImageOnly.clear();
}

/** How the node says your own message went into a Claude outside Persona's terminals (session.send's answer). */
export type DeliveryReply = { delivered_as?: string; relay_reason?: string };

/**
 * What to tell you when your own message to a Claude with Persona's plugin went through its message inbox instead of
 * as your input (docs/integrations/claude-integration.md "Owner input through the Persona plugin"): the Agent may not
 * take it as your approval. Nothing when it went in as your input, or when the session has no plugin (it always used
 * the inbox there, and its row says so).
 */
export function relayNotice(reply?: DeliveryReply): string | undefined {
  if (reply?.delivered_as !== "relayed") return undefined;
  switch (reply.relay_reason) {
    case "busy":
      return t("lib-outbox.relayed_busy");
    case "plugin_outdated":
      return t("lib-outbox.relayed_old_plugin");
    case "plugin_unavailable":
    case "plugin_refused":
    case "plugin_unconfirmed":
      return t("lib-outbox.relayed_plugin");
  }
  return undefined;
}

async function deliver(mid: string, sid: string, e: OutboxEntry, clearDraft?: string, flight = beginMessageSend(mid, sid, e.id, e.text)): Promise<boolean> {
  const key = outboxKey(mid, sid);
  try {
    // stays sending through a gap (a hub restart); the client_msg_id is the op id
    const r = await inSendOrder(key, () => apiOp<({ launcher_note?: string; waits_on_computer?: boolean } & DeliveryReply & KeptReply) | undefined>(`${sessionPath(mid, sid)}/messages`, {
      ...flight.request,
      method: "POST",
      opId: e.id,
      json: { text: e.text, images: e.images.length ? e.images : undefined, client_msg_id: e.id, launcher: e.launcher || undefined, model: e.model || undefined, clear_draft: clearDraft },
    }));
    if (!flight.current()) return true;
    assertDelivered(r); // a bare operation receipt is no delivery (#10194)
    if (isKept(r)) {
      // the assistant keeps it and sends it by itself: the row's bubble shows it on every device from now on
      flight.finish("failed");
      write(key, list(key).filter((x) => x.id !== e.id));
      showKept(mid, sid, r?.undelivered);
      return true;
    }
    flight.finish("delivered");
    update(key, e.id, { ...clearFailure, state: "sent", acceptedAt: Date.now(), clearable: undefined });
    showLauncherNote(`${mid}/${sid}`, r?.launcher_note);
    // An outside-terminal Claude took it behind a question only the computer can answer (#5890).
    if (r?.waits_on_computer) toast(t("lib-outbox.waits_on_computer"), "info");
    const relayed = relayNotice(r);
    if (relayed) toast(relayed, "info");
    return true;
  } catch (err) {
    if (!flight.current()) return false;
    if (flight.echoed()) return true;
    flight.finish("failed");
    update(key, e.id, failedWith(err));
    return false;
  } finally {
    flight.finish();
  }
}

/** Shows the message in that conversation and sends it; false when it didn't go (the bubble says why). */
export function sendToSession(mid: string, sid: string, out: { text: string; images?: OutboxImage[] }, opts: { launcher?: string; model?: string } = {}): Promise<boolean> {
  return queueToSession(mid, sid, out, opts).sent;
}

/** sendToSession, also naming the entry it made: for a place that offers its own retry of it (the overview's Continue). */
export function queueToSession(mid: string, sid: string, out: { text: string; images?: OutboxImage[] }, opts: { launcher?: string; model?: string } = {}): { id: string; sent: Promise<boolean> } {
  const key = outboxKey(mid, sid);
  const e: OutboxEntry = { id: uuid(), text: out.text, images: out.images ?? [], at: Date.now(), state: "sending", launcher: opts.launcher, model: opts.model };
  const flight = beginMessageSend(mid, sid, e.id, e.text);
  noteWritten(e.id, e.at);
  write(key, [...list(key), e]);
  return { id: e.id, sent: deliver(mid, sid, e, undefined, flight) };
}

/** One entry of a conversation's outbox, as it stands. */
export function outboxEntry(mid: string, sid: string, id: string): OutboxEntry | undefined {
  return list(outboxKey(mid, sid)).find((e) => e.id === id);
}

interface ClientOperation {
  id?: string;
  kind?: string;
  target?: { path?: string };
  state?: string;
  result?: { status?: number; body?: string };
}

export type OutboxCheck = "applied" | "not_applied" | "unknown" | "stale";
const checks = new Map<string, Promise<OutboxCheck>>();
const rechecks = new Map<string, Set<() => void>>();

/** Every mounted view of a conversation receives late Check refreshes, including for a Check started before a leave
 *  and return. Each cleanup removes only its own registration, leaving other mounted views registered. */
export function registerOutboxRecheck(mid: string, sid: string, recheck: () => void): () => void {
  const key = outboxKey(mid, sid);
  const mounted = rechecks.get(key) ?? new Set<() => void>();
  mounted.add(recheck);
  rechecks.set(key, mounted);
  return () => {
    mounted.delete(recheck);
    if (!mounted.size && rechecks.get(key) === mounted) rechecks.delete(key);
  };
}

function messageOperation(op: ClientOperation, mid: string, sid: string, id: string): boolean {
  // Go stores r.URL.Path, already decoded once. Compare the raw ids directly: decoding the receipt again would turn a
  // literal "%2F" in an id into a slash. Also accept the previously encoded form for compatibility.
  const decoded = `/api/v1/sessions/${mid}/${sid}/messages`, encoded = `/api/v1${sessionPath(mid, sid)}/messages`;
  return op.id?.toLowerCase() === id.toLowerCase() && op.kind === "message" && (op.target?.path === decoded || op.target?.path === encoded);
}

/** The original HTTP reason on a definite receipt, without putting the operation's internals in the bubble. */
function operationFailure(op: ClientOperation): FailureDetails {
  const saved = op.result;
  if (saved?.body && typeof saved.status === "number") {
    try {
      const bytes = Uint8Array.from(atob(saved.body), (c) => c.charCodeAt(0));
      const body = JSON.parse(new TextDecoder().decode(bytes)) as Record<string, unknown>;
      const message = typeof body.error === "string" && body.error ? body.error : `HTTP ${saved.status}`;
      return failureDetails(new ApiError(saved.status, message, body));
    } catch {
      // A receipt without readable original JSON still has its authoritative state below.
    }
  }
  return { ...clearFailure, error: t("lib-outbox.delivery_failed") };
}

/** Read-only reconciliation of an uncertain send: query its original operation, then let the open transcript's ordinary
 *  echo check run. Only a saved success or a reliable echo settles it; only a saved definite failure brings Retry back. */
export function checkOutbox(mid: string, sid: string, id: string): Promise<OutboxCheck> {
  const key = outboxKey(mid, sid), checkKey = `${key}/${id}`;
  const existing = checks.get(checkKey);
  if (existing) return existing;
  const e = list(key).find((x) => x.id === id);
  if (!e || e.state !== "failed" || !e.inDoubt) return Promise.resolve("stale");
  const token = getToken(), tokenOwner = credentialOwner();
  const current = () => credentialOwner() === tokenOwner && getToken() === token && list(key).find((x) => x.id === id) === e;
  const reread = () => {
    if (!current()) return;
    // Snapshot the registrations: views mounted or unmounted by a reload take part in the next Check, not this one.
    for (const recheck of [...(rechecks.get(key) ?? [])]) {
      try { recheck(); } catch {
        // A view that closed cannot refresh; the other views still can, and the next visit reconciles the same entry.
      }
    }
  };
  const run = (async (): Promise<OutboxCheck> => {
    let op: ClientOperation;
    try {
      op = await api<ClientOperation>(`/ops/${encodeURIComponent(id)}`, { token, tokenOwner, isAuthContextCurrent: current });
    } catch {
      if (!current()) return "stale";
      reread(); // A missing receipt (404 included) proves nothing; a transcript copy may still settle the message.
      return "unknown";
    }
    if (!current()) return "stale";
    if (!messageOperation(op, mid, sid, id)) {
      reread();
      return "unknown";
    }
    if (op.state === "done") {
      reread();
      if (!current()) return "stale";
      update(key, id, { ...clearFailure, state: "sent", acceptedAt: Date.now(), clearable: undefined, newId: undefined, inDoubt: undefined });
      return "applied";
    }
    if (op.state === "failed") {
      reread();
      if (!current()) return "stale";
      update(key, id, { inDoubt: false, newId: true, ...operationFailure(op) });
      return "not_applied";
    }
    reread(); // pending and unknown stay uncertain: never a new message id, never an ordinary Retry.
    return "unknown";
  })();
  checks.set(checkKey, run);
  const forget = () => { if (checks.get(checkKey) === run) checks.delete(checkKey); };
  void run.then(forget, forget);
  return run;
}

/** A message that already went in another way (the prompt that started a conversation): its bubble until the transcript has it. */
export function noteSent(mid: string, sid: string, out: { text: string; images?: OutboxImage[] }) {
  const key = outboxKey(mid, sid);
  write(key, [...list(key), { id: uuid(), wordsOnly: true, text: out.text, images: out.images ?? [], at: Date.now(), state: "sent" }]);
}

export function retryOutbox(mid: string, sid: string, id: string, clearDraft?: string): Promise<boolean> {
  return retryOutboxEntry(mid, sid, id, clearDraft).sent;
}

/** retryOutbox, also naming the entry the retry is (a new id only after a definite answer). */
export function retryOutboxEntry(mid: string, sid: string, id: string, clearDraft?: string): { id: string; sent: Promise<boolean> } {
  const key = outboxKey(mid, sid);
  const e = list(key).find((x) => x.id === id);
  if (!e || e.state !== "failed") return { id, sent: Promise.resolve(false) };
  // Every entry point is held to the same rule: an uncertain send is checked, never posted again under a new id.
  if (e.inDoubt) return { id, sent: checkOutbox(mid, sid, id).then((result) => result === "applied") };
  // A gap the hub never answered is retried with the same id (the hub does it at most once). A definite answer stays
  // on the old receipt, so this ordinary Retry is a new operation.
  const next: OutboxEntry = { ...e, ...clearFailure, id: e.newId ? uuid() : e.id, newId: false, state: "sending", acceptedAt: undefined, at: Date.now() };
  const flight = beginMessageSend(mid, sid, next.id, next.text);
  noteWritten(next.id, next.at);
  write(key, list(key).map((x) => (x.id === id ? next : x)));
  return { id: next.id, sent: deliver(mid, sid, next, clearDraft, flight) };
}

/** Drops one message from the conversation's outbox, and its copy kept for reloads; nothing is sent. A failed one is
 *  only on this device, so this is its Delete (#4626). Returns what puts it back where it was (Undo), nothing when it
 *  was not there. */
export function discardOutbox(mid: string, sid: string, id: string): (() => void) | undefined {
  const key = outboxKey(mid, sid);
  const before = list(key);
  const at = before.findIndex((e) => e.id === id);
  if (at < 0) return undefined;
  const gone = before[at];
  write(key, before.filter((e) => e.id !== id));
  return () => {
    const now = list(key);
    if (now.some((e) => e.id === gone.id)) return;
    write(key, [...now.slice(0, at), gone, ...now.slice(at)]);
  };
}

/**
 * "Ask Persona": the text goes to Persona's main conversation (AppModel.askPersona), which opens at once with the message
 * on its way; with no main conversation yet, this one starts it (and becomes it). Throws only when starting fails.
 */
export async function askPersona(text: string): Promise<void> {
  let a = getState().assistant;
  if (!a?.main) {
    await refreshAssistant().catch(() => {});
    a = getState().assistant;
  }
  const chosen = chosenManager() ?? undefined;
  if (a?.main) {
    void sendToMain(a.machine_id, a.main, text, chosen);
    return;
  }
  const r = await startConversation({ prompt: text, main: true });
  // the manager the page showed when it started (it named that one): the conversation's requests go there
  noteConversation(r.session.session_id, chosen);
  noteSent(r.session.machine_id, r.session.session_id, { text });
  void refreshAssistant().catch(() => {});
  navigate(conversationPage(r.session.session_id, r.session.machine_id, chosen));
}

/**
 * askPersona for one manager's main conversation (an inbox item's `manager`, none: the default's), whatever the assistant
 * page shows (Android's mainConversationOf): its main conversation is read from that manager, the page opens that
 * conversation (the current assistant stays as it is, docs/assistant/managers.md "Clients") and the text goes there;
 * with none yet, the text starts it. Throws when the manager can't be read or the conversation can't start, before the
 * page moves; `sent` then says whether the text got there (a failed bubble says why).
 */
export async function askPersonaOf(owner: string | undefined, text: string): Promise<{ sent: Promise<boolean> }> {
  const headers = ownerHeaders(owner);
  const a = await api<AssistantInfo>("/assistant", { headers });
  if (a.machine_id && a.main) {
    adoptConversation(owner, a.main, a.machine_id);
    return { sent: sendToMain(a.machine_id, a.main, text, owner) };
  }
  const r = await startConversation({ prompt: text, main: true }, headers);
  adoptConversation(owner, r.session.session_id, r.session.machine_id);
  noteSent(r.session.machine_id, r.session.session_id, { text });
  void refreshAssistant().catch(() => {});
  navigate(conversationPage(r.session.session_id, r.session.machine_id, owner));
  return { sent: Promise.resolve(true) };
}

/** Opens owner's main conversation (owner none: the default's) and sends text there with the launcher and model picked
 *  for it. */
function sendToMain(mid: string, sid: string, text: string, owner: string | undefined): Promise<boolean> {
  navigate(conversationPage(sid, mid, owner));
  const key = outboxKey(mid, sid);
  return sendToSession(mid, sid, { text }, { launcher: pickedLauncher(key), model: pickedModel(key) });
}

// The conversations a page opened for an item's own assistant (a hand-off, an inbox item's Ask Persona, an inbox
// chat's summary): the computer each runs on and whose it is. The page that shows one (PersonaAssistant) finds it there when it
// is another assistant's than the current one, which stays current (docs/assistant/managers.md "Clients": an item's
// things open as themselves and never change the pick).
const homes = new Map<string, { machine: string; manager?: string }>();

/** owner's conversation sid, on computer machine (owner none: the default's): its requests go to owner, and its page
 *  shows it on that computer, whichever assistant is current. */
export function adoptConversation(owner: string | undefined, sid: string, machine: string) {
  noteConversation(sid, otherManager(owner));
  if (sid && machine) homes.set(sid, { machine, manager: owner });
}

/** Where a conversation adopted here runs and whose it is (undefined: none adopted). */
export function adoptedConversation(sid: string): { machine: string; manager?: string } | undefined {
  return homes.get(sid);
}

onConversationDeleted((mid, sid) => {
  const key = outboxKey(mid, sid);
  for (const entry of keptEntries(key)) forgetOutgoing(key, entry.id);
  const prefix = keptKey(key, "");
  for (const name of [...keptImages.keys()]) if (name.startsWith(prefix)) keptImages.delete(name);
  for (const name of [...keptImageOnly.keys()]) if (name.startsWith(prefix)) keptImageOnly.delete(name);
  restored.delete(key);
});
