// What the first screen's lib/inbox.ts uses of an inbox item's event (lib/inbox-events.ts, which re-exports it): the
// section an open item is in, and acting on its suggestion with the toast that follows. The rest of the event helpers
// and their words are the lazily loaded Inbox page's and hand-off card's only.

import { t } from "../i18n";
import { api, ApiError, errorText } from "./api";
import { itemHeaders } from "./manager-choice";
import { uuid } from "./format";
import type { InboxItem } from "./inbox";
import type { InboxStatus, InboxSuggestion, SuggestionAction } from "./inbox-events";

/** The section of an open item: the hub's `status` when it sends one, else the triage tier as before (empty = "Needs you"). */
export function statusOf(it: InboxItem): InboxStatus {
  switch (it.status) {
    case "action":
    case "working":
    case "fyi":
    case "noise":
      return it.status;
  }
  return it.tier === "fyi" || it.tier === "noise" ? it.tier : "action";
}

export type SuggestionResult =
  | { ok: true; item: InboxItem }
  | { ok: false; stale: true; item?: InboxItem; error: string }
  | { ok: false; stale: false; error: string; code?: string; item?: InboxItem; delivered?: "none" | "unknown"; retryable?: boolean; retry_after?: number };

/**
 * POST /api/v1/inbox/{id}/suggestion: "Confirm" (with the edited text when "Edit" changed it), "Reject", or "Say something" (instruct).
 * The version shown goes with it, so a suggestion that changed since is not run: the hub answers 409 suggestion_stale
 * with the item as it is now.
 */
export async function actOnSuggestion(path: string, version: number, action: SuggestionAction, text?: string, clientID = uuid(), manager?: string, due?: string): Promise<SuggestionResult> {
  try {
    // the item's manager (another than the default: docs/assistant/managers.md "Clients") is named on its request
    const item = await api<InboxItem>(`${path}/suggestion`, { method: "POST", json: { version, action, text: text || undefined, due: due || undefined, client_id: clientID }, headers: itemHeaders(manager) });
    return { ok: true, item };
  } catch (e) {
    if (e instanceof ApiError && e.status === 409 && e.body?.code === "suggestion_stale") {
      const item = e.body.item as InboxItem | undefined;
      return { ok: false, stale: true, item: item?.id ? item : undefined, error: t("lib-inbox-events.stale") };
    }
    const body = e instanceof ApiError ? e.body : undefined;
    const item = body?.item as InboxItem | undefined;
    return { ok: false, stale: false, error: errorText(e), code: typeof body?.code === "string" ? body.code : undefined,
      item: item?.id ? item : undefined,
      delivered: body?.delivered === "none" || body?.delivered === "unknown" ? body.delivered : undefined,
      retryable: typeof body?.retryable === "boolean" ? body.retryable : undefined,
      retry_after: typeof body?.retry_after === "number" && body.retry_after > 0 ? body.retry_after : undefined };
  }
}

/** The toast after a suggestion was acted on. */
export function actedText(s: InboxSuggestion, action: SuggestionAction): string {
  if (action === "reject") return t("lib-inbox-events.rejected");
  if (action === "instruct") return t("lib-inbox-events.instructed");
  if (s.handback) return t("lib-inbox-events.handed_back");
  switch (s.kind) {
    case "reply":
      return t("lib-inbox-events.sent");
    case "handoff":
      return t("lib-inbox-events.handed_off");
    case "agent":
      return t("lib-inbox-events.agent_started");
    case "conclusion":
      return t("lib-inbox-events.filed");
    case "todo":
      return t("lib-inbox-events.todo_added");
    default:
      return t("lib-inbox-events.handed_back");
  }
}
