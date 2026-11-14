// What the service worker shows for a Web Push (#7459, docs/web/web.md "Web Push"): the hub's message
// (server/internal/hub/webpush.go webPushMessage), already decrypted by the browser, as one notification. Only sw.ts
// (and tests) import this: a module the page imports too would become a chunk the classic worker can't load
// (docs/lessons/web.md "Service workers and pages must not share modules with runtime values").

/** The hub's message. Every field is optional: a hub may send less, never something the worker must understand. */
export interface PushMessage {
  v?: number;
  /** interaction | reply | done | assistant | alert | test */
  kind?: string;
  title?: string;
  subtitle?: string;
  body?: string;
  machine_id?: string;
  session_id?: string;
  interaction_id?: string;
  /** A notify API alert's own id. */
  id?: string;
  todo_id?: string;
  /** Inside the browser's quiet hours: shown without a sound. */
  quiet?: boolean;
}

/** What the notification's click reads (sw-click.ts takes `url`); `pushed` tells the page it came by Web Push. */
export interface PushedData {
  url: string;
  pushed: true;
  kind: string;
}

export interface PushedNotification {
  title: string;
  options: { body: string; tag?: string; icon: string; silent: boolean; data: PushedData };
}

const str = (v: unknown) => (typeof v === "string" ? v : "");

/** The message out of whatever the push carried; an empty one for anything that isn't one. */
export function readPushMessage(raw: unknown): PushMessage {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const m = raw as Record<string, unknown>;
  return {
    kind: str(m.kind),
    title: str(m.title),
    subtitle: str(m.subtitle),
    body: str(m.body),
    machine_id: str(m.machine_id),
    session_id: str(m.session_id),
    interaction_id: str(m.interaction_id),
    id: str(m.id),
    todo_id: str(m.todo_id),
    quiet: m.quiet === true,
  };
}

/**
 * The tag the page gives the same event (lib/attention-signals.ts notify), so a later one replaces it and the page can
 * take it back once the event is over: `<machine>/<session>#needs` or `#finished`, `card:<id>#needs` for a card of no
 * session, `todo:<id>`, `alert:<id>`. The hub's Topic is made from the same tag (webPushTag).
 */
export function pushTag(m: PushMessage): string | undefined {
  const { kind, machine_id: mid = "", session_id: sid = "", interaction_id: ix = "" } = m;
  if (kind === "interaction" || kind === "reply") {
    if (sid) return `${mid}/${sid}#needs`;
    if (ix) return `card:${ix}#needs`;
  }
  if (kind === "done" && sid) return `${mid}/${sid}#finished`;
  if (kind === "assistant" && m.todo_id) return `todo:${m.todo_id}`;
  if (kind === "test") return "webpush-test";
  if (m.id) return `alert:${m.id}`;
  return undefined;
}

/** Where a click opens, under the worker's scope (the app's root): the agent's conversation, the card of no session,
 *  the to-do, or the app. The paths are lib/router.ts routePath's. */
export function pushUrl(m: PushMessage, scope: string): string {
  const enc = encodeURIComponent;
  const root = scope.endsWith("/") ? scope : `${scope}/`;
  if (m.session_id && m.machine_id) return `${root}s/${enc(m.machine_id)}/${enc(m.session_id)}`;
  if (m.interaction_id) return `${root}requests/${enc(m.interaction_id)}`;
  if (m.todo_id) return `${root}todos/${enc(m.todo_id)}`;
  return root;
}

/** The longest a notification's text gets: the hub shortened it to fit the push; a system notification shows less. */
const BODY_MAX = 400;

/**
 * The notification for a push: the hub's title, and its subtitle (the conversation and the computer) over its body, as
 * a phone shows the three. Always one: a push the worker doesn't show would cost the subscription (Safari revokes it).
 */
export function pushedNotification(raw: unknown, scope: string): PushedNotification {
  const m = readPushMessage(raw);
  const title = m.title || m.subtitle || "Persona";
  const lines = [m.title ? m.subtitle : "", m.body].map((s) => (s ?? "").trim()).filter(Boolean);
  const body = lines.join("\n");
  const root = scope.endsWith("/") ? scope : `${scope}/`;
  return {
    title,
    options: {
      body: body.length > BODY_MAX ? `${body.slice(0, BODY_MAX - 1)}…` : body,
      tag: pushTag(m),
      icon: `${root}app-icon-192.png`,
      silent: m.quiet === true,
      data: { url: pushUrl(m, scope), pushed: true, kind: m.kind || "alert" },
    },
  };
}
