import { describe, expect, it } from "vitest";
import { filterCounts } from "./agent-filter";
import { byAttention } from "./format";
import {
  comparePending,
  orderPending,
  PENDING_URGENCY,
  pendingSince,
  pendingUrgency,
  UNKNOWN_PENDING_SINCE,
  type PendingOrderItem,
} from "./pending-order";
import type { AgentState, Session } from "./types";

const at = (hour: number) => `2026-01-02T${String(hour).padStart(2, "0")}:00:00Z`;

const item = (key: string, patch: Partial<PendingOrderItem> = {}): PendingOrderItem => ({ key, cards: [], ...patch });
const cardItem = (key: string, created_at?: string) => item(key, { cards: [{ created_at }] });
// `status` is only in the fixtures so they read like real hub sessions; the helper never reads it.
const sessionItem = (key: string, patch: NonNullable<PendingOrderItem["session"]> & { status?: string }) => item(key, { session: patch });

describe("pendingUrgency", () => {
  it("ranks a pending card or a bare host block as blocked on an answer", () => {
    expect(pendingUrgency(cardItem("card", at(1)))).toBe(PENDING_URGENCY.blockedAnswer);
    expect(pendingUrgency(sessionItem("bare", { state: "needs_you", status: "blocked" }))).toBe(PENDING_URGENCY.blockedAnswer);
    expect(pendingUrgency(item("request-only"))).toBe(PENDING_URGENCY.blockedAnswer);
  });

  it("puts an unseen error or attention-flagged stop, a resurfaced reminder and an unread turn in their own classes", () => {
    expect(pendingUrgency(sessionItem("error", { state: "needs_you", status: "blocked", failure_reason: "boom" }))).toBe(PENDING_URGENCY.needsCheck);
    expect(pendingUrgency(sessionItem("attention", { state: "needs_you", status: "blocked", attention_reason: "dialog open" }))).toBe(PENDING_URGENCY.needsCheck);
    expect(pendingUrgency(sessionItem("reminder", { state: "idle", status: "idle", awaits_reply: true }))).toBe(PENDING_URGENCY.resurfacedReminder);
    expect(pendingUrgency(sessionItem("unread", { state: "unread", status: "done" }))).toBe(PENDING_URGENCY.unreadTurn);
  });

  it("never lets a flag outrank a card waiting on an answer", () => {
    const flagged = sessionItem("flagged", { state: "needs_you", status: "blocked", turn_error: "rate limited" });
    expect(pendingUrgency(flagged)).toBe(PENDING_URGENCY.needsCheck);
    const withCard = item("flagged-with-card", { cards: [{ created_at: at(9) }], session: flagged.session });
    expect(pendingUrgency(withCard)).toBe(PENDING_URGENCY.blockedAnswer);
  });
});

describe("orderPending", () => {
  it("orders by urgency class before wait time, whatever order the rows arrive in", () => {
    const rows = [
      sessionItem("unread-new", { state: "unread", status: "done", state_since: at(9) }),
      sessionItem("error-old", { state: "needs_you", status: "blocked", failure_reason: "boom", state_since: at(8) }),
      cardItem("answer-recent", at(7)),
      sessionItem("reminder", { state: "idle", status: "idle", awaits_reply: true, state_since: at(6) }),
      cardItem("answer-old", at(1)),
    ];
    expect(orderPending(rows).map((r) => r.key)).toEqual(["answer-old", "answer-recent", "error-old", "reminder", "unread-new"]);
  });

  it("leads the longest-waiting row within one class", () => {
    const rows = [cardItem("newest", at(9)), cardItem("oldest", at(2)), cardItem("middle", at(5))];
    expect(orderPending(rows).map((r) => r.key)).toEqual(["oldest", "middle", "newest"]);
  });

  it("uses the session's state_since, then updated_at, when a row has no card timestamp", () => {
    const rows = [
      sessionItem("since", { state: "needs_you", status: "blocked", state_since: at(3), updated_at: at(9) }),
      sessionItem("updated", { state: "needs_you", status: "blocked", updated_at: at(4) }),
    ];
    expect(orderPending(rows).map((r) => r.key)).toEqual(["since", "updated"]);
    expect(pendingSince(rows[0])).toBe(Date.parse(at(3)));
  });

  it("sorts an unknown wait start after the dated rows in its class, without jumping to the front", () => {
    const rows = [
      cardItem("undated"),
      cardItem("dated", at(4)),
      sessionItem("bare-undated", { state: "needs_you", status: "blocked" }),
    ];
    expect(pendingSince(rows[0])).toBe(UNKNOWN_PENDING_SINCE);
    expect(orderPending(rows).map((r) => r.key)).toEqual(["dated", "bare-undated", "undated"]);
  });

  it("breaks equal class and wait ties by the stable key", () => {
    const rows = [cardItem("b", at(5)), cardItem("a", at(5)), cardItem("c", at(5))];
    expect(orderPending(rows).map((r) => r.key)).toEqual(["a", "b", "c"]);
    expect(comparePending(rows[0], rows[1])).toBeGreaterThan(0);
  });

  it("does not mutate the input array or its rows", () => {
    const rows = [cardItem("b", at(5)), cardItem("a", at(1))];
    const snapshot = rows.map((r) => [r.key, r.cards]);
    orderPending(rows);
    expect(rows.map((r) => [r.key, r.cards])).toEqual(snapshot);
  });
});

describe("the main Agents list stays independent of pending ordering", () => {
  const session = (id: string, state: AgentState, hour: number, patch: Partial<Session> = {}): Session => ({
    machine_id: "machine", session_id: id, title: id, cwd: "/work/example", status: state, state, updated_at: at(hour), ...patch,
  });

  it("keeps the state-first order and every badge count", () => {
    const sessions = [
      session("idle-1", "idle", 10),
      session("working-1", "working", 9, { state_since: at(8) }),
      session("unread-1", "unread", 7),
      session("needs-1", "needs_you", 6),
      session("needs-2", "needs_you", 5, { failure_reason: "boom" }),
      session("unread-2", "unread", 4),
      session("reply-1", "idle", 3, { awaits_reply: true }),
      session("offline-1", "offline", 2),
    ];
    expect([...sessions].sort(byAttention).map((s) => s.session_id)).toMatchInlineSnapshot(`
      [
        "needs-1",
        "needs-2",
        "working-1",
        "unread-1",
        "unread-2",
        "reply-1",
        "idle-1",
        "offline-1",
      ]
    `);
    expect(filterCounts(sessions, { spaces: [], sessions: [] })).toMatchInlineSnapshot(`
      {
        "bg_waiting": 0,
        "feishu": 0,
        "needs": 2,
        "terminal": 0,
        "unread": 2,
        "working": 1,
      }
    `);
  });
});
