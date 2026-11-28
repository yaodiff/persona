// What an open page makes of the hub's notify events (#8145): only what it does not announce itself, under the tags the
// service worker gives the same events.
import { describe, expect, it } from "vitest";
import { hubNotice } from "./hub-notify";
import { pushTag } from "./sw-push";
import type { Interaction } from "./types";

const nobody = () => false;
const ask = { kind: "interaction", machine_id: "server", session_id: "main", interaction_id: "ask1", title: "Persona is asking you", subtitle: "Persona · hub", body: "Move the meeting?" };

describe("hubNotice", () => {
  it("a card of a conversation the page does not follow: Needs you, hushed in quiet hours, about the card", () => {
    expect(hubNotice(ask, nobody, [])).toEqual({
      title: "Persona is asking you", body: "Persona · hub\nMove the meeting?", tag: "server/main#needs", key: "server/main", card: "ask1",
      path: "/s/server/main", needs: true, hush: true, manager: undefined,
    });
  });

  it("does not rebind a retained notification to a same-ID replacement", () => {
    const at = "2026-01-02T03:04:05.000000001Z";
    const replacement: Interaction = { id: "ask1", machine_id: "server", session_id: "main", kind: "permission", created_at: "2026-01-02T03:04:05.000000002Z" };
    expect(hubNotice({ ...ask, interaction_created_at: at }, nobody, [replacement])).toBeUndefined();
    const original: Interaction = { ...replacement, created_at: at };
    expect(hubNotice({ ...ask, interaction_created_at: at }, nobody, [original])).toMatchObject({ card: "ask1", created_at: at });
  });

  it("leaves a followed Agent's cards and reminders to the page's own signals", () => {
    const followed = (k: string) => k === "m/a";
    expect(hubNotice({ ...ask, machine_id: "m", session_id: "a" }, followed, [])).toBeUndefined();
    expect(hubNotice({ ...ask, kind: "reply", machine_id: "m", session_id: "a" }, followed, [])).toBeUndefined();
    // a child's card is its parent's (#5120)
    const child: Interaction = { id: "c1", machine_id: "m", session_id: "kid", folded_under: "a", kind: "question", manager: "work" };
    expect(hubNotice({ ...ask, machine_id: "m", session_id: "kid", interaction_id: "c1" }, followed, [child])).toBeUndefined();
    expect(hubNotice({ ...ask, machine_id: "m", session_id: "kid", interaction_id: "c1" }, nobody, [child])).toMatchObject({ key: "m/a", manager: "work" });
  });

  it("leaves a card of no session, a finished turn and unknown kinds alone", () => {
    expect(hubNotice({ ...ask, session_id: "" }, nobody, [])).toBeUndefined();
    expect(hubNotice({ kind: "done", machine_id: "server", session_id: "main", title: "Persona" }, nobody, [])).toBeUndefined();
    expect(hubNotice({ kind: "test" }, nobody, [])).toBeUndefined();
    expect(hubNotice({}, nobody, [])).toBeUndefined();
  });

  it("the assistant's notify_user: every device's, not under Needs you, and the same event keeps its tag", () => {
    const told = { kind: "assistant", machine_id: "server", session_id: "main", title: "Persona", body: "The report is ready" };
    const n = hubNotice(told, nobody, [])!;
    expect(n).toMatchObject({ title: "Persona", body: "The report is ready", key: "server/main", path: "/s/server/main", needs: false, hush: false });
    expect(n.tag).toMatch(/^assistant:/);
    expect(hubNotice({ ...told }, nobody, [])!.tag).toBe(n.tag);
    expect(hubNotice({ ...told, body: "Another thing" }, nobody, [])!.tag).not.toBe(n.tag);
  });

  it("a decision opens its to-do; an alert is under Needs you", () => {
    const decide = { kind: "assistant", machine_id: "server", session_id: "main", title: "Approve the plan?", todo: { id: "td1", manager: "work", recommend: "a" } };
    expect(hubNotice(decide, nobody, [])).toMatchObject({ tag: "todo:td1", path: "/todos/td1", needs: true, hush: true, manager: "work" });
    expect(hubNotice({ kind: "alert", id: "build", title: "Build failed" }, nobody, [])).toMatchObject({ tag: "alert:build", key: "", path: "/", needs: true, hush: false });
  });

  it("gives the tags the service worker gives the same events", () => {
    const decide = { kind: "assistant", machine_id: "server", session_id: "main", todo: { id: "td1" } };
    for (const [msg, pushed] of [[ask, ask], [{ ...ask, kind: "reply" }, { ...ask, kind: "reply" }], [decide, { ...decide, todo_id: "td1" }], [{ kind: "alert", id: "x" }, { kind: "alert", id: "x" }]] as const) {
      expect(hubNotice(msg, nobody, [])!.tag).toBe(pushTag(pushed));
    }
  });
});
