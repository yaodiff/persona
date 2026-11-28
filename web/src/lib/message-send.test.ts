// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "./types";
const fake = vi.hoisted(() => ({
  cursor: undefined as number | undefined, row: undefined as Session | undefined, owner: "first", manager: "assistant", receipts: true,
  events: new Map<string, (event: Record<string, unknown>) => void>(),
  changes: [] as (() => void)[], credentials: [] as (() => void)[], managers: [] as (() => void)[], send: vi.fn(),
}));
vi.mock("./api", () => ({ credentialOwner: () => fake.owner, getToken: () => "synthetic-token", onCredentialChange: (f: () => void) => fake.credentials.push(f), sessionPath: (mid: string, sid: string) => `/sessions/${mid}/${sid}` }));
vi.mock("./format", () => ({ activityOf: (s: Session) => s.state ?? s.status }));
vi.mock("./manager-choice", () => ({ MANAGER_HEADER: "X-Persona-Manager", managerHeaderFor: () => fake.manager, shownManager: () => fake.manager, onManagerChoice: (f: () => void) => fake.managers.push(f) }));
vi.mock("./store", () => ({ getSession: () => fake.row, onHubEvent: (name: string, f: (event: Record<string, unknown>) => void) => fake.events.set(name, f), send: fake.send, subscribe: (f: () => void) => fake.changes.push(f) }));
import { beginMessageSend, configureMessageFeedback, messageFeedback, observeMessageItems, receiveMessageSend, resetMessageFeedback, settleMessageEcho, stopMessageFeedback } from "./message-send";
configureMessageFeedback({ getSession: () => fake.row, onHubEvent: (name, f) => fake.events.set(name, f), send: fake.send, subscribe: f => fake.changes.push(f), receiptsAllowed: () => fake.receipts, watchCursor: (mid, sid) => mid === "example" && sid === "chat" ? fake.cursor : undefined });
const event = (state: string, at = "2026-11-12T10:00:00.000000001Z", extra = {}) => ({ type: "message_send_state", machine_id: "example", session_id: "chat", client_msg_id: "message", state, at, ...extra });
const feedback = () => messageFeedback("example", "chat", "message");
beforeEach(() => {
  vi.useFakeTimers(); fake.cursor = undefined; resetMessageFeedback(); fake.send.mockClear();
  fake.owner = "first"; fake.manager = "assistant"; fake.receipts = true;
  fake.row = { machine_id: "example", session_id: "chat", state: "idle", status: "idle", state_since: "2026-11-12T09:00:00Z", queued: [] } as unknown as Session;
});
afterEach(() => { resetMessageFeedback(); vi.useRealTimers(); });
describe("per-message hub feedback", () => {
  it("uses the active watch on send and hello, reconciles an unknown receipt from its cursor, and watches other targets before POST", () => {
    fake.cursor = 27;
    beginMessageSend("example", "chat", "message");
    fake.events.get("hello")!({});
    expect(fake.send).not.toHaveBeenCalled();
    receiveMessageSend(event("unknown"));
    expect(fake.send).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ session_id: "chat", since_seq: 27 }));
    beginMessageSend("example", "another", "other-message");
    expect(fake.send).toHaveBeenLastCalledWith({ type: "subscribe", machine_id: "example", session_id: "another", lazy_images: true, fold_work: true });
  });

  it("ignores a legacy or unauthenticated hub's forged receipt and uses only local/HTTP evidence", () => {
    fake.receipts = false;
    receiveMessageSend(event("processing")); expect(feedback()).toBeUndefined();
    const flight = beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("processing")); expect(feedback()).toBe("sending");
    flight.finish("delivered"); expect(feedback()).toBe("processing");
  });
  it("publishes local Sending and subscribes before the request starts", () => {
    const flight = beginMessageSend("example", "chat", "message");
    expect(feedback()).toBe("sending");
    expect(fake.send).toHaveBeenCalledWith(expect.objectContaining({ type: "subscribe", machine_id: "example", session_id: "chat" }));
    // beside the manager, the send declares that it shows a message held for an Agent whose computer is away (#10661)
    expect(flight.request.headers).toEqual({ "X-Persona-Manager": "assistant", "X-Persona-Features": "agent_message_hold" });
  });
  it("selects the newest active feedback while preserving ended, missing and per-ID projections", () => {
    const older = beginMessageSend("example", "chat", "older");
    const newer = beginMessageSend("example", "chat", "newer");
    newer.finish("delivered"); older.finish("unknown");
    expect(messageFeedback("example", "chat")).toBe("processing");
    expect(messageFeedback("example", "chat", "older")).toBe("unknown");
    newer.finish("failed");
    expect(messageFeedback("example", "chat")).toBe("unknown");
    expect(messageFeedback("example", "chat", "newer")).toBe("");
    older.finish("failed");
    expect(messageFeedback("example", "chat")).toBe("");
    expect(messageFeedback("example", "chat", "missing")).toBeUndefined();
    beginMessageSend("other", "chat", "other");
    expect(messageFeedback("example", "chat")).toBe("");
    expect(messageFeedback("missing", "chat")).toBeUndefined();
  });
  it("keeps an evicted pending request valid without reviving its feedback on HTTP completion", () => {
    const first = beginMessageSend("example", "chat", "message");
    for (let i = 0; i < 256; i++) beginMessageSend("example", "chat", `queued-${i}`);
    expect(feedback()).toBeUndefined();
    expect(first.current()).toBe(true); expect(first.request.isAuthContextCurrent()).toBe(true);
    settleMessageEcho("example", "chat", "message"); expect(first.echoed()).toBe(true);
    first.finish("delivered"); expect(feedback()).toBeUndefined();
    expect(first.current()).toBe(false); // a completed attempt cannot be dispatched again, even after cache eviction
  });
  it("retires an evicted pending attempt when its same ID is replaced", () => {
    const first = beginMessageSend("example", "chat", "message");
    for (let i = 0; i < 256; i++) beginMessageSend("example", "chat", `queued-${i}`);
    const next = beginMessageSend("example", "chat", "message");
    expect(first.current()).toBe(false); expect(first.request.isAuthContextCurrent()).toBe(false);
    first.finish("delivered"); first.finish(); expect(feedback()).toBe("sending");
    const replacement = beginMessageSend("example", "chat", "message");
    expect(next.current()).toBe(false); expect(replacement.current()).toBe(true);
  });
  it.each(["manager", "credential", "epoch", "reset"])("still retires an evicted pending request on %s replacement", reason => {
    const first = beginMessageSend("example", "chat", "message");
    for (let i = 0; i < 256; i++) beginMessageSend("example", "chat", `queued-${i}`);
    if (reason === "manager") { fake.manager = "another"; fake.managers.forEach(f => f()); fake.manager = "assistant"; fake.managers.forEach(f => f()); }
    else if (reason === "reset") resetMessageFeedback();
    else { fake.owner = reason === "epoch" ? "first:new-epoch" : "replacement"; fake.credentials.forEach(f => f()); fake.owner = "first"; }
    expect(first.current()).toBe(false); expect(first.request.isAuthContextCurrent()).toBe(false);
    first.finish("delivered"); expect(feedback()).toBeUndefined();
  });
  it("keeps Processing through delayed HTTP and the delivered-to-Working gap without touching session state", () => {
    const original = structuredClone(fake.row);
    const flight = beginMessageSend("example", "chat", "message");
    fake.events.get("message_send_state")!(event("processing"));
    expect(feedback()).toBe("processing");
    receiveMessageSend(event("delivered", "2026-11-12T10:00:00.000000002Z"));
    flight.finish("delivered");
    expect(feedback()).toBe("processing"); expect(fake.row).toEqual(original);
    fake.row = { ...fake.row!, state: "working", state_since: "2026-11-12T10:00:01Z" };
    fake.changes.forEach(f => f());
    expect(feedback()).toBe(""); expect(fake.row.state).toBe("working");
  });
  it("shows Processing on another subscribed client with no local draft", () => {
    receiveMessageSend(event("processing"));
    expect(messageFeedback("example", "chat")).toBe("processing");
    expect(messageFeedback("other", "chat")).toBeUndefined();
    receiveMessageSend(event("failed", "2026-11-12T10:00:01Z"));
    expect(feedback()).toBe("");
  });
  it("does not let receipts for another identity affect this message", () => {
    beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("delivered", undefined, { machine_id: "other" }));
    receiveMessageSend(event("delivered", undefined, { session_id: "other" }));
    receiveMessageSend(event("delivered", undefined, { client_msg_id: "other" }));
    expect(feedback()).toBe("sending");
  });
  it("rejects malformed and stale same-millisecond nanosecond timestamps", () => {
    beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("processing", "2026-11-12T10:00:00.000000009Z"));
    receiveMessageSend(event("failed", "2026-11-12T10:00:00.000000008Z"));
    receiveMessageSend(event("failed", "not a timestamp"));
    receiveMessageSend(event("invented", "2026-11-12T10:00:01Z"));
    expect(feedback()).toBe("processing");
  });
  it("keeps normalized UTC ordering when Date.parse normalizes a noncanonical day", () => {
    beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("processing", "2026-03-01T12:00:00Z"));
    receiveMessageSend(event("failed", "2026-02-30T00:00:00Z"));
    expect(feedback()).toBe("processing");
  });
  it("ignores a late processing frame once that attempt ended", () => {
    beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("failed"));
    receiveMessageSend(event("processing", "2026-11-12T10:00:01Z"));
    expect(feedback()).toBe("");
  });
  it.each(["delivered", "failed", "unknown"])("keeps the %s receipt terminal when later processing arrives", state => {
    beginMessageSend("example", "chat", "message"); receiveMessageSend(event(state));
    const terminal = feedback();
    receiveMessageSend(event("processing", "2026-11-12T10:00:01Z"));
    expect(feedback()).toBe(terminal);
  });
  it("fences old HTTP completions and terminal frames across a same-id retry", () => {
    const first = beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("failed")); first.finish("failed");
    const second = beginMessageSend("example", "chat", "message");
    first.finish("delivered"); expect(first.current()).toBe(false);
    receiveMessageSend(event("failed", "2026-11-12T10:00:01Z"));
    expect(feedback()).toBe("sending");
    receiveMessageSend(event("processing", "2026-11-12T10:00:02Z"));
    second.finish("delivered"); expect(feedback()).toBe("processing");
  });
  it("never revives feedback after an echo arrived before HTTP", () => {
    const flight = beginMessageSend("example", "chat", "message");
    fake.events.get("items")!({ machine_id: "example", session_id: "chat", items: [{ client_msg_id: "message" }] });
    expect(flight.echoed()).toBe(true);
    flight.finish("delivered"); receiveMessageSend(event("processing"));
    expect(feedback()).toBe("processing");
    fake.row = { ...fake.row!, state: "working", state_since: "2026-11-12T10:00:02Z" };
    fake.changes.forEach(f => f());
    flight.finish("delivered");
    expect(feedback()).toBe("");
  });
  it("keeps definite failure recovery separate from the transport line", () => {
    const flight = beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("processing")); flight.finish("failed");
    expect(feedback()).toBe("");
  });
  it("checks uncertain delivery by resubscribing, never by posting a message", () => {
    beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("unknown")); expect(feedback()).toBe("unknown");
    expect(fake.send.mock.calls.every(([m]) => m.type === "subscribe")).toBe(true);
    settleMessageEcho("example", "chat", "message"); expect(feedback()).toBe("processing");
  });
  it("bounds quiet feedback and checks the transcript after a minute", () => {
    const flight = beginMessageSend("example", "chat", "message"); flight.finish("delivered");
    vi.advanceTimersByTime(60_000); expect(feedback()).toBe("unknown");
    expect(fake.send).toHaveBeenCalledTimes(2);
  });
  it("ends stopped feedback without pretending the message failed or the turn finished", () => {
    const original = structuredClone(fake.row);
    const flight = beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("processing")); flight.finish("delivered");
    stopMessageFeedback("example", "chat");
    settleMessageEcho("example", "chat", "message"); expect(feedback()).toBe("");
    receiveMessageSend(event("processing", "2026-11-12T10:00:01Z")); expect(feedback()).toBe("");
    flight.finish("delivered"); expect(feedback()).toBe(""); expect(fake.row).toEqual(original);
  });
  it("does not claim that stopping an older turn cancelled input still being dispatched", () => {
    fake.row = { ...fake.row!, state: "working" };
    beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("processing"));
    stopMessageFeedback("example", "chat");
    fake.row = { ...fake.row!, state: "idle", work: { phase: "done" }, failure_reason: "Previous round interrupted" };
    fake.changes.forEach(f => f());
    expect(feedback()).toBe("processing");
  });
  it("hands accepted input to existing Working without requiring another snapshot", () => {
    fake.row = { ...fake.row!, state: "working" };
    const flight = beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("processing")); expect(feedback()).toBe("processing");
    flight.finish("delivered"); expect(feedback()).toBe("");
    expect(fake.row.state).toBe("working");
  });
  it("retires old manager and credential generations, including a switch back", () => {
    const flight = beginMessageSend("example", "chat", "message");
    fake.manager = "another"; fake.managers.forEach(f => f());
    expect(flight.current()).toBe(false); flight.finish("delivered");
    fake.manager = "assistant"; fake.managers.forEach(f => f());
    expect(flight.current()).toBe(false);
    fake.owner = "replacement"; fake.credentials.forEach(f => f());
    expect(feedback()).toBeUndefined(); flight.finish("delivered"); expect(feedback()).toBeUndefined();
  });
  it.each([false, true])("does not let a completed observation from an old manager end a new same-ID flight (switch back: %s)", switchBack => {
    observeMessageItems("example", "chat", [{ id: "user", seq: 1, role: "user", kind: "text", client_msg_id: "message" }, { id: "reply", seq: 2, role: "assistant", kind: "text" }]);
    fake.manager = "another"; fake.managers.forEach(f => f());
    if (switchBack) { fake.manager = "assistant"; fake.managers.forEach(f => f()); }
    const flight = beginMessageSend("example", "chat", "message");
    expect(feedback()).toBe("sending");
    observeMessageItems("example", "chat", []);
    expect(feedback()).toBe("sending");
    receiveMessageSend(event("processing")); expect(feedback()).toBe("processing");
    flight.finish("delivered"); expect(feedback()).toBe("processing");
  });
  it("does not mistake metadata edits or someone else's queued input for execution", () => {
    const flight = beginMessageSend("example", "chat", "message", "This input");
    flight.finish("delivered");
    fake.row = { ...fake.row!, title: "Renamed", state_since: "2026-11-12T10:00:03Z", queued: ["Other input"] };
    fake.changes.forEach(f => f()); expect(feedback()).toBe("processing");
    fake.row = { ...fake.row, queued: ["Other input", "This input"] };
    fake.changes.forEach(f => f()); expect(feedback()).toBe("");
  });
  it("retains authority handoff when Working and Done both precede HTTP, without an echo", () => {
    const flight = beginMessageSend("example", "chat", "message");
    receiveMessageSend(event("processing"));
    fake.row = { ...fake.row!, state: "working", state_since: "2026-11-12T10:00:01Z" };
    fake.changes.forEach(f => f());
    fake.row = { ...fake.row, state: "idle", work: { phase: "done" } };
    fake.changes.forEach(f => f()); flight.finish("delivered");
    expect(feedback()).toBe("");
  });
  it("recognizes a completed legacy reply even when neither an echo nor Working was pushed", () => {
    observeMessageItems("example", "chat", [{ id: "old", seq: 1, role: "assistant", kind: "text", text: "Earlier reply" }]);
    const flight = beginMessageSend("example", "chat", "message", "Legacy input");
    receiveMessageSend(event("processing"));
    observeMessageItems("example", "chat", [{ id: "input", seq: 2, role: "user", kind: "text", text: "Legacy input" }, { id: "new", seq: 3, role: "assistant", kind: "text", text: "Completed reply" }]);
    flight.finish("delivered"); expect(feedback()).toBe("");
  });
  it("retains the previous page's maximum sequence and accepts unordered newer legacy input and reply", () => {
    observeMessageItems("example", "chat", [{ id: "old", seq: 5, role: "assistant", kind: "text", text: "Earlier reply" }]);
    beginMessageSend("example", "chat", "message", "New input"); receiveMessageSend(event("processing"));
    observeMessageItems("example", "chat", [{ id: "older-input", seq: 2, role: "user", kind: "text", text: "New input" }, { id: "older-reply", seq: 3, role: "assistant", kind: "text", text: "Old reply" }]);
    expect(feedback()).toBe("processing");
    observeMessageItems("example", "chat", [{ id: "reply", seq: 7, role: "assistant", kind: "text", text: "New reply" }, { id: "input", seq: 6, role: "user", kind: "text", text: "New input" }]);
    expect(feedback()).toBe("");
  });
  it("does not hand a new input to an older reply that landed late", () => {
    observeMessageItems("example", "chat", [{ id: "old", seq: 1, role: "assistant", kind: "text", text: "Earlier reply" }]);
    const flight = beginMessageSend("example", "chat", "message", "New input");
    receiveMessageSend(event("processing"));
    observeMessageItems("example", "chat", [{ id: "late", seq: 2, role: "assistant", kind: "text", text: "Previous round's delayed final reply" }]);
    flight.finish("delivered"); expect(feedback()).toBe("processing");
  });
  it("does not revive a cached completed ID after a hub restart, while new peer inputs still show", () => {
    observeMessageItems("example", "chat", [{ id: "user", seq: 1, role: "user", kind: "text", client_msg_id: "message", text: "Old input" }, { id: "reply", seq: 2, role: "assistant", kind: "text", text: "Old reply" }]);
    observeMessageItems("example", "chat", [{ id: "user", seq: 1, role: "user", kind: "text", client_msg_id: "message", text: "Old input" }]);
    receiveMessageSend(event("processing")); expect(feedback()).toBeUndefined();
    receiveMessageSend(event("processing", undefined, { client_msg_id: "new-peer-input" }));
    expect(messageFeedback("example", "chat", "new-peer-input")).toBe("processing");
  });
  it("uses HTTP as the older-hub fallback without requiring a receipt", () => {
    const flight = beginMessageSend("example", "chat", "message");
    expect(feedback()).toBe("sending"); flight.finish("delivered"); expect(feedback()).toBe("processing");
  });
});
