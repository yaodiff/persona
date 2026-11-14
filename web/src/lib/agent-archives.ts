// Archiving Agent sessions (docs/web/web.md "Archived agents", docs/protocol/protocol.md "Agent session archives"): an
// archive takes a session out of the Agents list and keeps it under Archived, from where it is restored; its transcript
// stays on its computer, and new activity brings it back by itself. "Archive and close" also closes the agent on its
// computer (session.retire: its tab and process go, the next message resumes it). The manager decides; the rules here
// only decide what the menus offer, and every refusal still comes back from the hub (409 agent_busy). The menus offer
// it as "Close…" (#7125, closeOffer), which closes a busy agent too after saying what that interrupts.
import { useSyncExternalStore } from "react";
import { api, ApiError, errorText, sessionPath } from "./api";
import { sessionMenuActions, sessionTitle, stateOf } from "./format";
import { dropSession, getState, refresh, upsertSession, useStore } from "./store";
import { toast } from "../ui/atoms";
import { t } from "../i18n";
import type { Session } from "./types";

/** The hub's features (hello): archives at all, and "Archive and close" with them. */
export const AGENT_ARCHIVES_FEATURE = "agent_session_archives";
export const AGENT_ARCHIVE_CLOSE_FEATURE = "agent_session_archive_close";

/** Busy as the manager counts it for an archive (agent_archives.go agentArchiveBusy): working, starting, waiting for an
 *  answer, background work, running subagents or workflows. Awaiting the user's reply is not busy. */
export function archiveBusy(s: Session): boolean {
  const st = stateOf(s);
  return Boolean(
    s.starting || s.status === "working" || s.status === "blocked" || st === "working" || st === "needs_you" ||
      s.background_working || s.background_waiting || (s.pending_interactions ?? 0) > 0 ||
      (s.background?.length ?? 0) > 0 || (s.background_count ?? 0) > 0 ||
      (s.running_subagents ?? 0) > 0 || s.subagents?.some((x) => x.running) ||
      (s.running_workflows ?? 0) > 0 || s.workflows?.some((x) => x.running),
  );
}

/** Busy for closing (agent_archive_close.go agentCloseBusy): also a /loop, queued messages, or a server it runs. */
export function closeBusy(s: Session): boolean {
  return archiveBusy(s) || Boolean(s.loop) || (s.queued?.length ?? 0) > 0 || (s.ports?.length ?? 0) > 0;
}

/** What closing a busy agent would cut off, said in "Close…" (#7125): an answer it waits for, its work (queued
 *  messages included), a /loop or a server it runs; undefined when it is not busy (closeBusy). */
export type CloseBusyReason = "waiting" | "working" | "loop" | "server";
export function closeBusyReason(s: Session): CloseBusyReason | undefined {
  if (!closeBusy(s)) return undefined;
  if (s.status === "blocked" || stateOf(s) === "needs_you" || (s.pending_interactions ?? 0) > 0) return "waiting";
  if (archiveBusy(s) || (s.queued?.length ?? 0) > 0) return "working";
  return s.loop ? "loop" : "server";
}

/** "ok": offered; "busy": shown disabled with why; "no": not offered. */
export type ArchiveOffer = "ok" | "busy" | "no";

/** What a session's menus offer: Archive, and Archive and close.
 *  Archive: any agent but the assistant's conversations (those archive in All chats), on a hub that has archives.
 *  Archive and close: an agent running in a terminal tab that the node can bring back after closing it, as session.retire
 *  decides (node retire.go): Claude Code, and Codex or a compatible CLI that Persona started (its launcher is known);
 *  not a runtime's or a desktop app's session (their tool keeps them), and not one already closed. */
export function archiveOffers(s: Session, features: readonly string[]): { archive: ArchiveOffer; close: ArchiveOffer } {
  if (s.assistant || s.archived || !features.includes(AGENT_ARCHIVES_FEATURE)) return { archive: "no", close: "no" };
  const archive: ArchiveOffer = archiveBusy(s) ? "busy" : "ok";
  const agent = s.agent ?? "";
  const resumable = agent === "" || agent === "claude" || ((agent === "codex" || agent === "traex") && Boolean(s.started_by_persona));
  const open = stateOf(s) !== "offline" && s.status !== "offline" && !s.start_error;
  const closable = features.includes(AGENT_ARCHIVE_CLOSE_FEATURE) && !s.source_kind && resumable && open;
  return { archive, close: !closable ? "no" : closeBusy(s) ? "busy" : "ok" };
}

/** What "Close…" does (#7125), the one item that took the place of "Close agent…" and "Archive and close…":
 *  "archive": archive and close (`archive?close=1`); "close": close only (`POST …/close`), for a busy agent (the hub
 *  refuses to archive one) or one the hub cannot archive and close (an older hub, a runtime's session, a Codex session
 *  Persona did not start); "busy": busy and its node offers no plain close, so shown disabled with why; "no": not
 *  offered (offline, the assistant's, nothing to close it with). */
export type CloseOffer = "archive" | "close" | "busy" | "no";
export function closeOffer(s: Session, features: readonly string[]): CloseOffer {
  const archived = archiveOffers(s, features).close;
  if (archived === "ok") return "archive";
  if (sessionMenuActions(s).includes("close")) return "close";
  return archived === "busy" ? "busy" : "no";
}

/** Without archives on the hub, the old "Delete chat" stays (an older hub); with them, archive takes its place. */
export function deleteInsteadOfArchive(features: readonly string[]): boolean {
  return !features.includes(AGENT_ARCHIVES_FEATURE);
}

/** What "Archive and close" did beyond the archive (the hub's `close`). */
export interface CloseResult {
  closed: boolean;
  outcome: "closed" | "not_running" | "refused" | "offline" | "failed" | string;
  reason?: string;
  detail?: string;
}

// ── the hub ──────────────────────────────────────────────

export function archiveSession(s: Pick<Session, "machine_id" | "session_id">, close = false, force = false): Promise<{ ok: boolean; close?: CloseResult }> {
  return api(`${sessionPath(s.machine_id, s.session_id)}/archive${force ? "?force=1" : close ? "?close=1" : ""}`, { method: "POST", json: {} });
}

export function unarchiveSession(s: Pick<Session, "machine_id" | "session_id">): Promise<unknown> {
  return api(`${sessionPath(s.machine_id, s.session_id)}/unarchive`, { method: "POST", json: {} });
}

/** Every archived agent the hub's managers keep, newest activity first. */
export async function listArchived(signal?: AbortSignal): Promise<Session[]> {
  const r = await api<{ sessions?: Session[] }>("/sessions/archived", { signal });
  const rows = (r.sessions ?? []).map((s) => ({ ...s, archived: true }));
  return rows.sort((a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? "") || a.machine_id.localeCompare(b.machine_id) || a.session_id.localeCompare(b.session_id));
}

/** Why the hub refused an archive, in this page's words. */
export function archiveErrorText(e: unknown, close = false): string {
  if (e instanceof ApiError && e.status === 409) return t(close ? "lib-agent-archives.close_busy" : "lib-agent-archives.busy");
  if (e instanceof ApiError && e.status === 501) return t(close ? "lib-agent-archives.close_upgrade" : "lib-agent-archives.upgrade");
  return t("lib-agent-archives.failed", { error: errorText(e) });
}

// ── what this page knows is archived (the Archived page's list, and what was archived here) ──

const known = new Map<string, Session>();
let knownRev = 0;
const knownListeners = new Set<() => void>();
const keyOf = (mid: string, sid: string) => `${mid}/${sid}`;
function knownChanged() {
  knownRev++;
  knownListeners.forEach((l) => l());
}

/** Takes the archived list as the hub gave it: what it has is archived now, and nothing else. */
export function noteArchivedList(rows: Session[]) {
  known.clear();
  for (const s of rows) known.set(keyOf(s.machine_id, s.session_id), s);
  knownChanged();
}

export function noteArchived(s: Session, archived: boolean) {
  if (archived) known.set(keyOf(s.machine_id, s.session_id), { ...s, archived: true });
  else known.delete(keyOf(s.machine_id, s.session_id));
  knownChanged();
}

/** The archived session mid/sid as this page last saw it; undefined when it is not known to be archived. */
export function useArchived(mid: string, sid: string): Session | undefined {
  useSyncExternalStore(
    (l) => {
      knownListeners.add(l);
      return () => knownListeners.delete(l);
    },
    () => knownRev,
    () => knownRev,
  );
  // back in the lists (new activity restored it, or another device did): not archived any more
  const listed = useStore((st) => Boolean(st.sessions[mid]?.some((x) => x.session_id === sid)));
  return listed ? undefined : known.get(keyOf(mid, sid));
}

// ── the actions, with their toasts (tests pass their own deps) ──

export interface ArchiveDeps {
  archive: (s: Session, close: boolean) => Promise<{ close?: CloseResult }>;
  unarchive: (s: Session) => Promise<unknown>;
  /** Takes the row out of the lists at once (the hub's push follows). */
  drop: (s: Session) => void;
  /** Puts back the removed snapshot after a refusal. */
  restore: (s: Session) => void;
  /** Reconciles with the hub after the local update. */
  refresh: () => Promise<unknown>;
  notify: (text: string, tone: "ok" | "error" | "info", action?: { label: string; run: () => void }) => void;
}

/** Archive (and close, with `close`): the row leaves at once; the toast says what happened and offers Undo; a refusal
 *  puts it back and says why. Resolves with what the close did (undefined for a plain archive or a refusal), and
 *  `busy` when the hub refused because the agent is busy (409), which "Close…" then offers to close anyway. */
export async function archiveAgent(s: Session, close: boolean, deps: ArchiveDeps): Promise<{ ok: boolean; close?: CloseResult; busy?: boolean }> {
  const title = sessionTitle(s);
  deps.drop(s);
  try {
    const r = await deps.archive(s, close);
    noteArchived(s, true);
    const undo = { label: t("lib-agent-archives.undo"), run: () => void restoreAgent(s, deps) };
    if (!close) deps.notify(t("lib-agent-archives.archived", { title }), "ok", undo);
    else if (!r.close) deps.notify(t("lib-agent-archives.archived_close_unknown", { title }), "info", undo); // a hub that did not say
    else if (r.close.closed) deps.notify(t("lib-agent-archives.archived_closed", { title }), "ok", undo);
    else if (r.close.outcome === "not_running") deps.notify(t("lib-agent-archives.archived_not_running", { title }), "ok", undo);
    else deps.notify(t("lib-agent-archives.archived_still_open", { title, reason: r.close.reason || r.close.detail || r.close.outcome }), "info", undo);
    return { ok: true, close: r.close };
  } catch (e) {
    deps.restore(s);
    deps.notify(archiveErrorText(e, close), "error");
    return e instanceof ApiError && e.status === 409 ? { ok: false, busy: true } : { ok: false };
  } finally {
    await deps.refresh().catch(() => {});
  }
}

/** Restore: back in the Agents list. Nothing is reopened on the computer; the next message resumes a closed agent. */
export async function restoreAgent(s: Session, deps: Pick<ArchiveDeps, "unarchive" | "refresh" | "notify">): Promise<boolean> {
  const title = sessionTitle(s);
  try {
    await deps.unarchive(s);
    noteArchived(s, false);
    deps.notify(t("lib-agent-archives.restored", { title }), "ok");
    return true;
  } catch (e) {
    deps.notify(t("lib-agent-archives.restore_failed", { error: errorText(e) }), "error");
    return false;
  } finally {
    await deps.refresh().catch(() => {});
  }
}

/** The hub, the store and the toasts, for the app. An archived agent whose chat is open stays open: the chat says it is
 *  archived and offers Restore (ChatView). */
export const appArchiveDeps: ArchiveDeps = {
  archive: (s, close) => archiveSession(s, close),
  unarchive: unarchiveSession,
  drop: (s) => dropSession(s.machine_id, s.session_id),
  restore: (s) => {
    // A newer socket row wins over the snapshot removed by this request.
    if (!getState().sessions[s.machine_id]?.some(row => row.session_id === s.session_id)) upsertSession(s);
  },
  refresh: () => refresh(),
  notify: (text, tone, action) => toast(text, tone, action),
};
