// Compatibility for sessions from a manager that did not send Session.view.
// Keep the precedence in step with assistant/agentview.Decide and fleetcountadapt.Views.
import type { AgentReason, AgentView } from "./agent-view-wire";
import { agentKey } from "./pins";
import type { Interaction, Session } from "./types";

interface WaitingRow {
  cards: number;
  counted: boolean;
  /** All the row's cards are still delegated. Muting alone never sets this. */
  held?: boolean;
}

function waiting({ cards, counted, held }: WaitingRow): AgentView {
  const reason: AgentReason = cards > 0 ? { kind: "cards", n: cards } : { kind: "terminal_dialog" };
  if (reason.kind === "cards" && held) reason.held = true;
  return counted ? { phase: "needs_you", reason, counted, rank: 0 } : { phase: "needs_you", reason, rank: 1 };
}

// The oldest hubs sent only status (spec/fixtures/sort/session_state.json).
// A present state, including a future value, takes precedence over that translation.
function hostState(s: Session): string {
  if (s.state != null) return s.state;
  switch (s.status) {
    case "blocked": return "needs_you";
    case "done": return "unread";
    case "working":
    case "idle":
    case "offline": return s.status;
    default: return "unknown";
  }
}

function turnView(s: Session, state: string): AgentView {
  if (state === "needs_you") return waiting({ cards: 0, counted: false });
  if (state === "working") {
    if (!s.background_working) return { phase: "working", reason: { kind: "turn" }, rank: 20 };
    if (s.background_waiting) return { phase: "working", reason: { kind: "background", waiting: true }, rank: 22 };
    return { phase: "working", reason: { kind: "background" }, rank: 21 };
  }
  if (state !== "idle" && state !== "unread" && state !== "offline" && s.status === "interrupted") {
    const reason: AgentReason = { kind: "restart_interrupted" };
    if (!s.capabilities || s.capabilities.send || s.capabilities.terminal) reason.resumable = true;
    return { phase: "error", reason, rank: 10 };
  }
  if (s.start_error || s.start_error_code) {
    const reason: AgentReason = { kind: "start_refused" };
    if (s.start_error_code) reason.code = s.start_error_code;
    if (s.start_error_args && Object.keys(s.start_error_args).length) reason.args = { ...s.start_error_args };
    return { phase: "error", reason, rank: 10 };
  }
  if (s.turn_error && s.turn_error_kind !== "stopped") {
    if (s.turn_error_kind === "usage_limit") {
      const reason: AgentReason = { kind: "usage_limit" };
      if (s.turn_error_resets_at) reason.resets_at = s.turn_error_resets_at;
      if (s.turn_error_resumes) reason.resumes = true;
      return { phase: "error", reason, rank: reason.resumes ? 11 : 10 };
    }
    const reason: AgentReason = { kind: "model" };
    if (s.turn_error_code) reason.code = s.turn_error_code;
    return { phase: "error", reason, rank: 10 };
  }
  if ((state === "unread" || state === "idle") && s.awaits_reply) {
    const reason: AgentReason = { kind: "asks" };
    const question = s.asks?.trim();
    if (question) reason.question = question;
    if (state === "unread") reason.unseen = true;
    return { phase: "done", reason, rank: reason.unseen ? 30 : 31 };
  }
  if (state === "unread") return { phase: "done", reason: { kind: "unseen" }, rank: 30 };
  if (state === "idle") {
    if (s.work?.just_finished) return { phase: "done", reason: { kind: "just_finished" }, rank: 32 };
    return { phase: "idle", reason: { kind: "seen" }, rank: 40 };
  }
  if (state === "offline") return { phase: "idle", reason: { kind: "not_running" }, rank: 42 };
  return { phase: "idle", reason: { kind: "host_unknown" }, rank: 41 };
}

function listed(s: Session): boolean {
  return !s.assistant && !s.hidden_by_directory;
}

function localParent(s: Session): string | undefined {
  if (s.folded_under_machine_id && s.folded_under_machine_id !== s.machine_id) return;
  if (s.folded_under !== s.session_id) return s.folded_under;
}

/** Infer from a single session or its known waiting row. Use legacyAgentViews for
 * exact counts, card ownership, folded rows and mute ranks from a complete snapshot. */
export function inferAgentView(session: Session, row?: WaitingRow): AgentView {
  if (row) return waiting(row);
  const state = hostState(session);
  const cards = session.pending_interactions ?? 0;
  if (cards > 0 || state === "needs_you") {
    return waiting({ cards, counted: listed(session) && !session.quiet && !localParent(session) });
  }
  return turnView(session, state);
}

interface OwnCards {
  total: number;
  supervised: number;
  hasOwnerCard: boolean;
}

interface FleetRow {
  cards: number;
  hasOwnerCard: boolean;
  hasDialog: boolean;
}

// The manager publishes one computer at a time. A missing, hidden or
// cross-computer parent leaves the child listed on its own computer.
function rowHost(s: Session, byKey: ReadonlyMap<string, Session>): Session {
  let host = s;
  for (let hops = 0; hops < byKey.size; hops++) {
    const under = localParent(host);
    if (!under) break;
    const parent = byKey.get(`${host.machine_id}/${under}`);
    if (!parent || !listed(parent)) break;
    host = parent;
  }
  return host;
}

/** The legacy views for one manager's snapshot, keyed by machine_id/session_id.
 * Pass the original sessions (before folding), pending interactions and mute lookup. */
export function legacyAgentViews(
  sessions: readonly Session[],
  interactions: readonly Interaction[],
  isMuted: (session: Session) => boolean = () => false,
): Map<string, AgentView> {
  const byKey = new Map<string, Session>();
  for (const s of sessions) {
    if (s.machine_id?.trim() && s.session_id?.trim()) byKey.set(agentKey(s), s);
  }

  const ownCards = new Map<Session, OwnCards>();
  for (const ix of interactions) {
    const asker = byKey.get(agentKey(ix));
    if (!asker || !ix.id?.trim()) continue;
    const own = ownCards.get(asker) ?? { total: 0, supervised: 0, hasOwnerCard: false };
    own.total++;
    if (!ix.delegating) own.hasOwnerCard = true;
    else if (ix.kind !== "question") own.supervised++;
    ownCards.set(asker, own);
  }

  const rows = new Map<Session, FleetRow>();
  for (const s of byKey.values()) {
    if (!listed(s)) continue;
    const own = ownCards.get(s);
    const cards = own ? own.total - own.supervised : 0;
    const dialog = !own && hostState(s) === "needs_you";
    if (!cards && !dialog) continue;
    const host = rowHost(s, byKey);
    const row = rows.get(host) ?? { cards: 0, hasOwnerCard: false, hasDialog: false };
    row.cards += cards;
    row.hasOwnerCard ||= !!own?.hasOwnerCard;
    row.hasDialog ||= dialog;
    rows.set(host, row);
  }

  const views = new Map<string, AgentView>();
  for (const [key, s] of byKey) {
    const row = rows.get(s);
    const own = ownCards.get(s);
    const muted = isMuted(s);
    let view: AgentView;
    if (row) {
      view = waiting({
        cards: row.cards,
        counted: !muted && !s.quiet && (row.hasOwnerCard || row.hasDialog),
        held: !row.hasOwnerCard,
      });
    } else if (own && own.total === own.supervised) {
      view = { phase: "working", reason: { kind: "turn" }, rank: 20 };
    } else if (own) {
      view = waiting({ cards: own.total, counted: false, held: !own.hasOwnerCard });
    } else {
      view = turnView(s, hostState(s));
    }
    if (muted && view.phase !== "needs_you" && view.phase !== "idle") {
      view.rank = Math.floor(view.rank / 10) * 10 + 9;
    }
    views.set(key, view);
  }
  return views;
}
