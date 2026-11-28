import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import type { AgentMutes } from "./agent-groups";
import type { AgentView } from "./agent-view-wire";
import { inferAgentView, legacyAgentViews } from "./legacy-agent-view";
import { agentKey } from "./pins";
import { isMuted } from "./session-actions";
import type { Interaction, Session } from "./types";

interface FixtureCase {
  name: string;
  input: { sessions: Session[]; interactions: Interaction[]; quiet: string[]; mutes: AgentMutes };
  expected: { views: { id: string; view: AgentView }[] };
}

const { cases } = JSON.parse(readFileSync(new URL("../../../spec/fixtures/agent-view/cases.json", import.meta.url), "utf8")) as {
  cases: FixtureCase[];
};

describe("legacy fleet views", () => {
  it.each(cases)("$name", ({ input, expected }) => {
    const sessions = input.sessions.map((s) => ({ ...s, quiet: input.quiet.includes(agentKey(s)) }));
    const views = legacyAgentViews(sessions, input.interactions, (s) => isMuted(s, input.mutes));
    expect(Array.from(views, ([id, view]) => ({ id, view }))).toStrictEqual(expected.views);
  });
});

const session: Session = { machine_id: "machine", session_id: "parent", title: "Parent", cwd: "", status: "working" };

describe("legacy single-session views", () => {
  it("uses the supplied row before the session's turn and errors", () => {
    const s = { ...session, start_error: "Cannot start", pending_interactions: 3 };
    expect(inferAgentView(s, { cards: 0, counted: false })).toStrictEqual({
      phase: "needs_you", reason: { kind: "terminal_dialog" }, rank: 1,
    });
    expect(inferAgentView(s, { cards: 2, counted: false })).toStrictEqual({
      phase: "needs_you", reason: { kind: "cards", n: 2 }, rank: 1,
    });
    expect(inferAgentView(s, { cards: 1, counted: false, held: true }).reason).toEqual({ kind: "cards", n: 1, held: true });
  });

  it("infers waiting from the session alone and honors its badge exclusions", () => {
    const waiting = { ...session, status: "blocked" };
    expect(inferAgentView(waiting)).toStrictEqual({ phase: "needs_you", reason: { kind: "terminal_dialog" }, counted: true, rank: 0 });
    expect(inferAgentView({ ...waiting, pending_interactions: 2 }).reason).toEqual({ kind: "cards", n: 2 });
    for (const excluded of [{ quiet: true }, { folded_under: "lead" }, { hidden_by_directory: true }, { assistant: true }]) {
      expect(inferAgentView({ ...waiting, ...excluded })).toStrictEqual({ phase: "needs_you", reason: { kind: "terminal_dialog" }, rank: 1 });
    }
  });

  it("uses the host's known state before interrupted status and separates start and turn errors", () => {
    const errors = { ...session, status: "interrupted", start_error_code: "launcher_missing", turn_error: "Failed" };
    expect(inferAgentView(errors).reason).toEqual({ kind: "restart_interrupted", resumable: true });
    expect(inferAgentView({ ...errors, state: "working" }).reason).toEqual({ kind: "turn" });
    expect(inferAgentView({ ...errors, state: "unread" }).reason).toEqual({ kind: "start_refused", code: "launcher_missing" });
    expect(inferAgentView({ ...session, status: "done" }).reason).toEqual({ kind: "unseen" });
  });
});

it("keeps cross-computer folds on each computer's published list", () => {
  const child: Session = { ...session, machine_id: "other", session_id: "child", folded_under: "parent", folded_under_machine_id: "machine" };
  const card: Interaction = { machine_id: child.machine_id, session_id: child.session_id, id: "question", kind: "question" };
  const views = legacyAgentViews([session, child], [card]);
  expect(views.get(agentKey(session))?.phase).toBe("working");
  expect(views.get(agentKey(child))).toStrictEqual({ phase: "needs_you", reason: { kind: "cards", n: 1 }, counted: true, rank: 0 });
});

it("counts a folded terminal dialog even when every card in its row is held", () => {
  const child: Session = { ...session, session_id: "child", folded_under: "parent", state: "needs_you" };
  const card: Interaction = { machine_id: session.machine_id, session_id: session.session_id, id: "question", kind: "question", delegating: true };
  const views = legacyAgentViews([session, child], [card]);
  expect(views.get(agentKey(session))).toStrictEqual({ phase: "needs_you", reason: { kind: "cards", n: 1, held: true }, counted: true, rank: 0 });
  expect(views.get(agentKey(child))).toStrictEqual({ phase: "needs_you", reason: { kind: "terminal_dialog" }, rank: 1 });
});

it("places muted errors and finished turns last within their phase and keeps the idle rank", () => {
  const sessions: Session[] = [
    { ...session, session_id: "failed", state: "idle", start_error: "Failed" },
    { ...session, session_id: "finished", state: "unread" },
    { ...session, session_id: "offline", state: "offline" },
  ];
  const views = legacyAgentViews(sessions, [], () => true);
  expect([...views.values()].map((v) => v.rank)).toEqual([19, 39, 42]);
});
