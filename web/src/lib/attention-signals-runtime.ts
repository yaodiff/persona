// The lazy background notification, audio synthesis, desktop presence, Web Push, and Service Worker reply runtime for
// lib/attention-signals.ts. Loaded asynchronously after the first screen renders (via Vite's holdLazyImports, so the
// `notify` i18n namespace in client-rest is in before this module runs).
import { t } from "../i18n";
import {
  answeredTags,
  attentionSoundEnabled,
  batchDue,
  detectNewCards,
  detectTransitions,
  finishedBatch,
  isHeldLevel,
  onAttentionSoundChange,
  pickEvent,
  rateLimiter,
  SETTLE_MS,
  settledTags,
  suppressed,
  trackTurns,
  type Turn,
} from "./attention";
import { stripBase } from "./base";
import { requestRoute } from "./card-focus";
import { homeKey } from "./folded-rows";
import { attentionReason, interactionCanAnswer, sessionTitle, activityOf } from "./format";
import { hubNotice } from "./hub-notify";
import { defaultManager } from "./manager-choice";
import { failedTagsOver, listenForNotificationClicks } from "./notification-answer";
import {
  claimNotification,
  closeNotification,
  dismissNotification,
  FRONT_BEAT_MS,
  inQuietHours,
  loadNotifyPrefs,
  managerMuted,
  markFront,
  notificationText,
  notifyPrefs,
  onSystemNotifyChange,
  otherTabInFront,
  postNotification,
  releaseClaim,
  systemNotifyActive,
  systemNotifyAllowed,
  waitingCard,
  wants,
} from "./notify";
import { agentKey } from "./pins";
import { needsFieldValues } from "./quick-answers";
import { href, navigate, parse } from "./router";
import { isMuted } from "./session-actions";
import {
  appSocketReady,
  connect,
  DESKTOP_UNREGISTER_FEATURE,
  disconnect,
  getState,
  onHello,
  onHubEvent,
  sayHello,
  send,
  setHelloExtras,
} from "./store";
import { supervisorHeld } from "./supervisor-held";
import type { AgentState, Interaction, Session } from "./types";
import { webPushFlag } from "./web-push-flag";

const ICONS = 'link[rel="icon"]';

const currentIcon = () =>
  document.querySelector<HTMLLinkElement>(ICONS)?.dataset.original ??
  document.querySelector<HTMLLinkElement>(ICONS)?.href;

// ── sounds (uses the AudioContext unlocked synchronously in attention-signals.ts) ──

// [frequency Hz, start s, length s, peak gain]
type Note = [number, number, number, number];
/** "Needs you": two rising notes, clear. */
const CHIME: Note[] = [[784, 0, 0.22, 0.16], [1175, 0.14, 0.34, 0.16]];
/** "Done": one low, soft note. */
const DONE: Note[] = [[523, 0, 0.4, 0.07]];

export function playOnContext(ctx: AudioContext | undefined, kind: "needs" | "finished"): void {
  if (!ctx || ctx.state !== "running") return;
  const t0 = ctx.currentTime;
  for (const [freq, at, len, peak] of kind === "needs" ? CHIME : DONE) {
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.type = "sine";
    osc.frequency.value = freq;
    gain.gain.setValueAtTime(0.0001, t0 + at);
    gain.gain.exponentialRampToValueAtTime(peak, t0 + at + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.0001, t0 + at + len);
    osc.connect(gain).connect(ctx.destination);
    osc.start(t0 + at);
    osc.stop(t0 + at + len + 0.05);
  }
}

// ── notifications ────────────────────────────────────────

/** The page is the one in front: its tab is showing and its window has the focus. */
function inFront(): boolean {
  return document.visibilityState === "visible" && document.hasFocus();
}

/** How long after the hello that starts asking for the hub's `notify` events they are taken as what the hub kept, not
 *  news (it sends those right after that hello). */
const HUB_REPLAY_MS = 3_000;

const ASK_TITLE = {
  question: "notify.question_title",
  plan: "notify.plan_title",
  permission: "notify.permission_title",
  screen: "notify.screen_title",
  elicitation: "notify.elicitation_title",
} as const;

/** No notification goes out from any tab now: they are off, or one of the tabs is in front (that one's title, badge and
 *  sounds cover it) and "Always show system notifications" is off (#2451). */
const unannounced = () => !systemNotifyAllowed({ here: inFront(), other: otherTabInFront() });

/** A system notification for the event (Settings › Notifications): only while unannounced says so, and once for all
 *  the open tabs. Clicking it opens the agent's conversation. `card` is the new
 *  card of a session that was already "Needs you" (#4058): it replaces the agent's earlier "Needs you" one (same tag), and the tabs
 *  share it by the card, so a transition just posted does not keep it back. Undefined when it was not posted, else
 *  the card a "Needs you" one is about. */
function notify(s: Session | undefined, kind: "needs" | "finished", card?: Interaction): { about?: string } | undefined {
  if (unannounced()) return;
  if (!s && card?.session_id !== "") return;
  const tag = `${s ? agentKey(s) : `card:${card!.id}`}#${kind}`;
  if (!claimNotification(card ? `${tag}@${card.id}` : tag)) return;
  // "Needs you": what the card asks, not the agent's last words (#4056); the hub tells the card before the session's state
  const waiting = kind === "needs" ? { ix: card ?? (s ? waitingCard(getState().interactions, s) : undefined), reason: s ? attentionReason(s) : undefined } : {};
  const { title, body } = notificationText(
    s ?? { title: card?.agent_label || card?.session_title || "", cwd: card?.cwd ?? "", last_text: "" },
    kind,
    {
      needs: (name) => t("notify.needs_title", { name }),
      finished: (name) => t("notify.finished_title", { name }),
      needsBody: t("notify.needs_body"),
      finishedBody: t("notify.finished_body"),
      asks: (ask, name) => t(ASK_TITLE[ask], { name }),
    },
    waiting,
  );
  const route = card ? requestRoute(card) : { name: "session", mid: s!.machine_id, sid: s!.session_id } as const;
  const icon = currentIcon();
  // a permission card's gets Allow / Deny (#4464), when the session can be answered from here and nothing has to be
  // filled in first (fields: as the hub's push and the overview's quick answers); the rest need the conversation
  // (never on a card a task's supervisor holds: it is not the user's to answer, lib/supervisor-held.ts)
  const answerable = waiting.ix?.kind === "permission" && !supervisorHeld(waiting.ix) && !needsFieldValues(waiting.ix) && interactionCanAnswer(waiting.ix, s) ? waiting.ix : undefined;
  const ix = answerable?.id;
  // inside quiet hours a "Needs you" one comes without a sound (a finished one doesn't come at all: wants)
  postNotification({ title, body, tag, url: href(route), icon, ix, created_at: answerable?.created_at, silent: inQuietHours(notifyPrefs()) }, () => navigate(route));
  return { about: waiting.ix?.id };
}

/** The tag of the one notification for several finished turns (#4945): the next batch replaces it. */
const BATCH_TAG = "finished#batch";
/** What the tabs claim a batch by: its agents, so the tabs post one batch once. */
const batchClaim = (keys: readonly string[]) => `${BATCH_TAG}@${[...keys].sort().join(",")}`;

/** One notification for several finished turns (#4945), under the same rules as notify's; clicking it opens the overview.
 *  `names` are the first few of `keys` (finishedBatch), `more` that there are others. The caller checked unannounced. */
function notifyBatch(keys: readonly string[], names: readonly string[], more: boolean): void {
  // this tab's own single ones are told in the batch now; their claims stay, another tab may hold them
  for (const k of keys) dismissNotification(`${k}#finished`);
  // the tabs see the same batch: the first to post it does
  if (!claimNotification(batchClaim(keys))) return;
  const list = names.join(t("notify.finished_batch_separator"));
  const title = t("notify.finished_batch_title", { count: keys.length, names: more ? t("notify.finished_batch_more", { names: list }) : list });
  const route = { name: "home" } as const;
  const icon = currentIcon();
  postNotification({ title, body: t("notify.finished_batch_body"), tag: BATCH_TAG, url: href(route), icon }, () => navigate(route));
}

// ── runtime controller ───────────────────────────────────

export interface AttentionSnapshot {
  pane: { mid: string; sid: string } | undefined;
  sessions: Session[];
  interactions: Interaction[];
  loaded: boolean;
  pushGrading: boolean;
}

export interface EarlyHubNotifyState {
  asking: boolean | undefined;
  quietUntil: number;
}

export interface AttentionRuntimeOptions {
  sawHello?: boolean;
  getAudioContext?: () => AudioContext | undefined;
  hubNotify?: EarlyHubNotifyState;
}

export interface AttentionRuntime {
  setPane: (pane: { mid: string; sid: string } | undefined) => void;
  syncLoaded: (loaded: boolean) => void;
  update: (sessions: Session[], interactions: Interaction[], loaded: boolean, pushGrading: boolean) => void;
  replayHubNotify: (msg: Record<string, unknown>, at: number) => void;
  dispose: () => void;
}

const paneKey = (pane: { mid: string; sid: string } | undefined) =>
  pane ? agentKey({ machine_id: pane.mid, session_id: pane.sid }) : undefined;

function startDesktopLifecycle(sawHello: boolean): () => void {
  const desktop = typeof window === "undefined" ? undefined : window.personaDesktop;
  if (!desktop) return () => {};
  // What this socket was last told: the shell repeats its presence every 30 s whether it changed or not, and a
  // repeat tells the hub nothing new. A hello is a new socket, told again.
  let told: "on" | "off" | undefined;
  const reconcile = () => {
    if (!appSocketReady()) return;
    // A newer web app can run in an older desktop: missing additive capabilities retain the browser behavior.
    const active = desktop.desktopPresence?.().active ?? true;
    const want = active && systemNotifyActive() ? "on" : "off";
    if (want === told) return;
    told = want;
    if (want === "on") {
      send({ type: "register_desktop", device_name: desktop.deviceName });
      return;
    }
    if (getState().features.includes(DESKTOP_UNREGISTER_FEATURE)) send({ type: "unregister_desktop" });
    else disconnect(); // Old hub: only closing this socket can withdraw its desktop registration.
  };
  const offHello = onHello(() => {
    told = undefined;
    reconcile();
  });
  // Recovery must also work after an old hub's socket-close withdrawal: there is no new hello without reconnecting.
  // Only a socket that is gone is opened again: connect() replaces an open one, which every presence repeat did.
  const restore = () => {
    if (!appSocketReady() && (desktop.desktopPresence?.().active ?? true) && systemNotifyActive()) connect();
    reconcile();
  };
  const offPresence = desktop.onPresenceChange?.(restore) ?? (() => {});
  const offNotifications = onSystemNotifyChange(restore);
  const offNotification = desktop.onOpenNotification?.((route: string) => {
    if (route.startsWith("/") && !route.startsWith("//") && !route.includes("\\")) navigate(stripBase(route));
  }) ?? (() => {});
  // Reconcile immediately if hello already completed before this lazy runtime finished loading.
  if (sawHello || getState().conn === "online") reconcile();
  return () => { offHello(); offPresence(); offNotifications(); offNotification(); };
}

function startFrontBeat(): () => void {
  // Tells the other tabs whether this one is in front (notify: none of them posts then).
  const mark = () => markFront(inFront());
  mark();
  const beat = setInterval(() => inFront() && markFront(true), FRONT_BEAT_MS);
  const away = () => markFront(false);
  window.addEventListener("focus", mark);
  window.addEventListener("blur", mark);
  document.addEventListener("visibilitychange", mark);
  window.addEventListener("pagehide", away);
  return () => {
    clearInterval(beat);
    away();
    window.removeEventListener("focus", mark);
    window.removeEventListener("blur", mark);
    document.removeEventListener("visibilitychange", mark);
    window.removeEventListener("pagehide", away);
  };
}

/** Starts the background attention-signals subsystem (desktop lifecycle, SW clicks, front-tab heartbeat,
 *  session/card transitions, and hub `notify` events). Returns a controller for the thin shell hook. */
export function createAttentionRuntime(initial: AttentionSnapshot, opts: AttentionRuntimeOptions = {}): AttentionRuntime {
  const offDesktop = startDesktopLifecycle(Boolean(opts.sawHello));
  // Allow / Deny clicked on a permission card's notification: the service worker hands it to this page (#4464)
  const offSwClicks = listenForNotificationClicks();
  const offFront = startFrontBeat();
  const playSound = (kind: "needs" | "finished") => playOnContext(opts.getAudioContext?.(), kind);

  let viewing = paneKey(initial.pane);
  let prev: ReadonlyMap<string, AgentState> = new Map();
  let turns: ReadonlyMap<string, Turn> = new Map();
  const allow = rateLimiter();
  let latest = initial.sessions;
  const timers = new Set<ReturnType<typeof setTimeout>>();
  // the card occurrence keys seen pending (undefined before the first run), the card each "Needs you" notification is about, and the
  // sessions whose "Needs you" came before any card (its first card is the same wait, CardEvent.follows)
  let cards: ReadonlyMap<string, number> | undefined = undefined;
  const announced = new Map<string, string>();
  const bare = new Set<string>();
  // finished turns gathering for one notification (#4945), and the agents the finished notification on screen names
  let batch: { keys: string[]; firstAt: number; timer?: ReturnType<typeof setTimeout> } = { keys: [], firstAt: 0 };
  let batched: string[] = [];

  let lastLoaded = false;
  const syncLoaded = (loaded: boolean) => {
    if (loaded === lastLoaded) return;
    lastLoaded = loaded;
    if (!loaded) return;
    loadNotifyPrefs().catch(() => {}); // the cached copy stays when the hub can't say
    // Web Push on in this browser (#7459): its subscription is still the hub's, in this page's language
    if (webPushFlag()) void import("./web-push").then((m) => m.syncWebPush());
  };

  let initialized = false;
  let lastSessions: Session[] | undefined;
  let lastInteractions: Interaction[] | undefined;
  let lastUpdateLoaded: boolean | undefined;
  let lastPushGrading: boolean | undefined;

  const update = (sessions: Session[], interactions: Interaction[], loaded: boolean, pushGrading: boolean) => {
    latest = sessions;
    if (
      initialized &&
      Object.is(sessions, lastSessions) &&
      Object.is(interactions, lastInteractions) &&
      Object.is(loaded, lastUpdateLoaded) &&
      Object.is(pushGrading, lastPushGrading)
    ) {
      return;
    }
    initialized = true;
    lastSessions = sessions;
    lastInteractions = interactions;
    lastUpdateLoaded = loaded;
    lastPushGrading = pushGrading;
    if (!loaded) return;
    const now = Date.now();
    const { next, events } = detectTransitions(prev, sessions, getState().mutes, interactions, pushGrading);
    // a session already "Needs you" that gets another card speaks again (#4058)
    const fresh = detectNewCards(cards, interactions, prev, next, sessions, bare, now, pushGrading);
    cards = fresh.ids;
    for (const k of bare) if (next.get(k) !== "needs_you") bare.delete(k);
    for (const e of fresh.events) bare.delete(e.key);
    for (const e of events) if (e.kind === "needs" && !interactions.some((ix) => homeKey(ix) === e.key)) bare.add(e.key);
    // answered or seen, here or elsewhere: its system notification goes too (#3458); and one about a card that is gone,
    // while the session waits on another; and a Could not allow one about a card that is gone
    const settled = new Set([...settledTags(prev, next), ...answeredTags(announced, interactions), ...failedTagsOver(interactions)]);
    settled.forEach(closeNotification);
    // every agent the batch notification names was seen (or is gone): it goes too (a single one goes by settledTags)
    if (batched.length && !batched.some((k) => next.get(k) === "unread")) {
      releaseClaim(batchClaim(batched)); // the same agents finishing again post again
      batched = [];
      closeNotification(BATCH_TAG);
    }
    for (const tag of settled) if (tag.endsWith("#needs")) announced.delete(tag.slice(0, -"#needs".length));
    // what was pushed while every page was closed (#7459) goes too once its event is over, as the page's own do
    if (webPushFlag()) {
      const pending = new Set(interactions.map((ix) => ix.id));
      void import("./web-push").then((m) => m.settlePushed(next, pending));
    }
    prev = next;
    turns = trackTurns(turns, sessions, now);
    const prefs = notifyPrefs();
    const ran = (k: string) => {
      const turn = turns.get(k);
      return turn?.endedAt !== undefined ? turn.endedAt - turn.start : undefined;
    };
    // a manager this device turned off (Settings › Managers) says nothing here either, as its pushes don't reach the phone
    const ownMuted = (e: { key: string; card?: Interaction }) => managerMuted(prefs, e.card?.manager ?? latest.find((x) => agentKey(x) === e.key)?.manager, defaultManager());
    const wanted = [...events, ...fresh.events].filter((e) => wants(e.kind, prefs, e.kind === "finished" ? ran(e.key) : undefined) && !ownMuted(e));
    const sound = attentionSoundEnabled();
    const speak = (kind: "needs" | "finished", k: string) => {
      if (!sound || inQuietHours(prefs) || suppressed(k, { key: viewing, focused: inFront() })) return;
      if (allow(Date.now())) playSound(kind);
    };
    const find = (k: string) => latest.find((x) => agentKey(x) === k);
    // The finished turns of a batch make one notification (#4945): one alone is its own, as before.
    const flush = () => {
      if (getState().pushGrading) return;
      const keys = batch.keys;
      batch = { keys: [], firstAt: 0 };
      const unread = new Map(latest.filter((x) => activityOf(x) === "unread").map((x) => [agentKey(x), sessionTitle(x)]));
      if (finishedBatch(keys, unread).kind === "none") return;
      // it replaces the batch notification on screen: the agents that one named and are still unseen stay in it
      const shown = finishedBatch([...batched, ...keys], unread);
      if (shown.kind === "none" || unannounced()) return;
      // remembered whichever tab won the claim, so every tab forms the next batch from the same agents
      if (shown.kind === "one") {
        const s = find(shown.key);
        if (!s) return;
        notify(s, "finished");
        batched = [shown.key]; // a batch after it, while it is unseen, takes it in
      } else {
        notifyBatch(shown.keys, shown.names, shown.more);
        batched = shown.keys;
      }
    };
    const gather = (k: string) => {
      const at = Date.now();
      const b = batch;
      if (!b.keys.length) b.firstAt = at;
      b.keys.push(k);
      if (b.timer) {
        clearTimeout(b.timer);
        timers.delete(b.timer);
      }
      const timer = setTimeout(() => {
        timers.delete(timer);
        flush();
      }, batchDue(b.firstAt, at) - at);
      b.timer = timer;
      timers.add(timer);
    };
    const urgent = wanted.filter((e) => e.kind === "needs");
    // a card that follows its session's "Needs you" rewrites that notification without a second sound
    const first = pickEvent(urgent.filter((e) => !e.follows));
    if (first) speak(first.kind, first.key);
    // The sound of "Needs you" does not look at mutes (detectTransitions); the notification does, as the phone's push and
    // the Mac's do (hub announce, MacAttention.transitions).
    for (const e of urgent) {
      const s = find(e.key);
      if (s ? isMuted(s, getState().mutes) : e.card?.session_id !== "") continue;
      const about = notify(s, "needs", e.card)?.about;
      if (about) announced.set(e.key, about);
    }
    // A finished turn speaks once it has stayed finished (the status flaps working → done → working around a turn).
    // No sound when a "Needs you" one is going out with it: that one is the more urgent.
    for (const done of wanted.filter((e) => e.kind === "finished")) {
      const timer = setTimeout(() => {
        timers.delete(timer);
        if (getState().pushGrading) return;
        const s = find(done.key);
        if (!s || activityOf(s) !== "unread") return;
        if (!first) speak("finished", done.key);
        gather(done.key);
      }, SETTLE_MS);
      timers.add(timer);
    }
  };

  // The hub's `notify` events (#8145, lib/hub-notify.ts): what the hub tells a phone of the conversations this page does
  // not follow itself (the assistant's), a notify_user, a decision, an alert. Asked for only while this page can alert
  // (the alert sound or system notifications on), so the hub counts it as a device that hears them only then. The hello
  // that starts asking has the hub send what it kept while this browser was not asking (hub/notifyring.go): opening the
  // page announces none of that, as it announces no state it finds on loading.
  let quietUntil = opts.hubNotify?.quietUntil ?? 0;
  let asking: boolean | undefined = opts.hubNotify?.asking;
  const able = () => attentionSoundEnabled() || systemNotifyActive();
  setHelloExtras(() => {
    const on = able();
    if (on && !asking) quietUntil = Date.now() + HUB_REPLAY_MS;
    asking = on;
    return on ? ["ws_notify"] : [];
  });
  const follow = () => {
    if (able() !== Boolean(asking)) sayHello();
  };
  follow();
  const offSound = onAttentionSoundChange(follow);
  const offSystem = onSystemNotifyChange(follow);
  const handleHubNotify = (msg: Record<string, unknown>, at = Date.now()) => {
    if (at < quietUntil) return;
    const state = getState();
    const ixId = typeof msg.interaction_id === "string" ? msg.interaction_id : "";
    const occurrence = typeof msg.interaction_created_at === "string" ? msg.interaction_created_at : undefined;
    const cardLevel = ixId ? state.interactions.find((c) => c.id === ixId && (!occurrence || c.created_at === occurrence))?.push_level : undefined;
    const msgLevel = typeof msg.push_level === "string" ? msg.push_level : undefined;
    if (state.pushGrading && (isHeldLevel(msgLevel) || isHeldLevel(cardLevel))) return;
    const n = hubNotice(msg, (k) => prev.has(k), state.interactions);
    if (!n) return;
    const mid = String(msg.machine_id ?? ""), sid = String(msg.session_id ?? "");
    const row = (state.sessions[mid] ?? []).find((x) => x.session_id === sid);
    const prefs = notifyPrefs();
    if ((n.needs && !wants("needs", prefs)) || managerMuted(prefs, n.manager ?? row?.manager, defaultManager())) return;
    const hushed = n.hush && inQuietHours(prefs);
    // no sound for the conversation on screen while the window is in front, as for an Agent's (suppressed); the main
    // chat's address names no conversation
    const on = parse(stripBase(location.pathname), location.search);
    const looking = inFront() && Boolean(n.key) && (n.key === viewing || (on.name === "persona" && (on.sid ?? state.assistant?.main) === sid));
    if (attentionSoundEnabled() && !hushed && !looking && allow(Date.now())) playSound("needs");
    if (unannounced() || !claimNotification(n.card ? `${n.tag}@${n.card}` : n.tag)) return;
    const ix = n.card ? state.interactions.find((c) => c.id === n.card && (!n.created_at || c.created_at === n.created_at)) : undefined;
    const answerable = ix?.kind === "permission" && !supervisorHeld(ix) && !needsFieldValues(ix) && interactionCanAnswer(ix, row) ? ix : undefined;
    const answer = answerable?.id;
    const route = parse(n.path);
    const icon = currentIcon();
    postNotification({ title: n.title, body: n.body, tag: n.tag, url: href(route), icon, ix: answer, created_at: answerable?.created_at, silent: hushed }, () => navigate(route));
    // taken back once its card is answered or withdrawn (answeredTags), as a card the page announced itself
    if (n.card && n.key) announced.set(n.key, n.card);
  };
  const offNotify = onHubEvent("notify", (msg) => handleHubNotify(msg));

  syncLoaded(initial.loaded);
  update(initial.sessions, initial.interactions, initial.loaded, initial.pushGrading);

  return {
    setPane(pane) {
      viewing = paneKey(pane);
    },
    syncLoaded,
    update,
    replayHubNotify(msg, at) {
      handleHubNotify(msg, at);
    },
    dispose() {
      offDesktop();
      offSwClicks();
      offFront();
      offSound();
      offSystem();
      offNotify();
      setHelloExtras(() => []);
      timers.forEach(clearTimeout);
      timers.clear();
      batch = { keys: [], firstAt: 0 }; // nothing gathered is posted by a later mount
    },
  };
}
