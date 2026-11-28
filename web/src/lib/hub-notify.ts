// The hub's `notify` events on an open page (#8145, docs/web/web.md "Attention signals"): what a phone is pushed and a
// closed browser gets by Web Push, for the conversations the page does not follow itself. The page works out its
// Agents' "Needs you" and finished turns from their states (lib/attention.ts); the assistant's own conversations (the
// main chat, a leader's, a scheduled task's run) are not among them (lib/sessions.ts useAgentSessions), and a
// `notify_user`, a decision or a `personad notify` alert has no state to read. This file is the logic without a DOM;
// lib/attention-signals.ts posts and plays.
import { homeKey } from "./folded-rows";
import type { Interaction } from "./types";

/** What the page makes of one hub `notify` event. */
export interface HubNotice {
  /** The system notification's tag: the one the page and the service worker give the same event (lib/sw-push.ts pushTag),
   *  else one of the event's own, so the open tabs post it once. */
  tag: string;
  /** The session it is about (`<machine>/<session>`, its parent's for a child's card), "" for none. */
  key: string;
  /** The card it is about: the notification goes when the card does (answeredTags). */
  card?: string;
  title: string;
  body: string;
  /** The exact occurrence the event announced; absent from an older hub. */
  created_at?: string;
  /** Where clicking it goes, a path of lib/router.ts. */
  path: string;
  /** Under this browser's "When something needs you" switch, as the hub's push is under the device's (`input`). */
  needs: boolean;
  /** Inside quiet hours it comes without a sound, as the hub marks it `quiet` (hub/quiethours.go quietHush); the
   *  others come as they are (quietNone). */
  hush: boolean;
  /** The manager it is from, when the event or its card says. */
  manager?: string;
}

const str = (v: unknown) => (typeof v === "string" ? v : "");

/** A short stable tag for an event that names none (the hub gives a `notify_user` no id). */
function digest(text: string): string {
  let h = 5381;
  for (let i = 0; i < text.length; i++) h = ((h << 5) + h + text.charCodeAt(i)) | 0;
  return (h >>> 0).toString(36);
}

/**
 * The notice for a hub `notify` event, or undefined when the page leaves it alone:
 * - `interaction` / `reply` (a card, a reminder of one still waiting) only for a session the page does not follow
 *   (`followed`, by agentKey): an Agent's are its own "Needs you" (detectTransitions, detectNewCards), and a card of no
 *   session is announced from the store as well;
 * - `assistant` (the assistant's `notify_user` and pushes, a decision with its to-do) and `alert` always;
 * - `done` never: an Agent's finished turn is the page's own, and the assistant's replies are not announced here;
 *   kinds it does not know neither.
 * `cards` are the pending cards, for the session a child's card belongs to and the card's manager.
 */
export function hubNotice(msg: Record<string, unknown>, followed: (key: string) => boolean, cards: readonly Interaction[]): HubNotice | undefined {
  const kind = str(msg.kind);
  const mid = str(msg.machine_id), sid = str(msg.session_id), ixId = str(msg.interaction_id), id = str(msg.id);
  const occurrence = str(msg.interaction_created_at) || undefined;
  const title = str(msg.title).trim(), subtitle = str(msg.subtitle).trim(), text = str(msg.body).trim();
  const words = { title: title || subtitle || "Persona", body: [title ? subtitle : "", text].filter(Boolean).join("\n") };
  const at = mid && sid ? `/s/${encodeURIComponent(mid)}/${encodeURIComponent(sid)}` : "";
  if (kind === "interaction" || kind === "reply") {
    if (!sid) return undefined;
    const ix = ixId ? cards.find((c) => c.id === ixId) : undefined;
    if (occurrence && ix?.created_at && ix.created_at !== occurrence) return undefined;
    const key = ix ? homeKey(ix) : `${mid}/${sid}`;
    if (followed(key)) return undefined;
    return { ...words, tag: `${key}#needs`, key, card: ixId || undefined, ...(occurrence ? { created_at: occurrence } : {}), path: at || "/", needs: true, hush: true, manager: ix?.manager };
  }
  if (kind !== "assistant" && kind !== "alert") return undefined;
  const todo = msg.todo && typeof msg.todo === "object" ? (msg.todo as Record<string, unknown>) : undefined;
  const todoId = str(todo?.id);
  const key = mid && sid ? `${mid}/${sid}` : "";
  if (todoId) {
    // a decision waits for the user (hub notifyDecide): "Needs you", hushed in quiet hours
    return { ...words, tag: `todo:${todoId}`, key, path: `/todos/${encodeURIComponent(todoId)}`, needs: true, hush: true, manager: str(todo?.manager) || undefined };
  }
  const tag = id ? `alert:${id}` : `${kind}:${digest([mid, sid, title, subtitle, text].join("\n"))}`;
  // notify_user and the assistant's pushes go to every device (no "Needs you" switch); an alert is under it (notify_api.go)
  return { ...words, tag, key, path: at || "/", needs: kind === "alert", hush: false };
}
