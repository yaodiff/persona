// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { buildAgentGroups } from "./agent-groups";
import { matchesFilter } from "./agent-filter";
import type { AgentView } from "./agent-view-wire";
import { byAttention, stateOf, viewOf } from "./format";
import { managerBadges } from "./manager-attention";
import { rowLine } from "./row-line";
import { hasAgentViews, listedAgents, othersWaiting, stateCounts } from "./sessions";
import type { Interaction, Session } from "./types";

const machine = { id: "work", name: "Work", online: true };
const mutes = { spaces: [], sessions: [] };
const row = (id: string, view: AgentView): Session => ({
  machine_id: machine.id, session_id: id, title: id, cwd: "", status: "working", state: "working", view,
});

describe("the Agents list reads the manager's view on each row", () => {
  const asks = row("meeting", { phase: "done", reason: { kind: "asks", question: "Does Tuesday work?" }, rank: 31 });
  const error = row("calendar", { phase: "error", reason: { kind: "restart_interrupted", resumable: true }, rank: 10 });
  const reviewing = row("notes", { phase: "needs_you", reason: { kind: "cards", n: 2, held: true }, rank: 1 });
  const waiting = row("export", { phase: "needs_you", reason: { kind: "terminal_dialog" }, counted: true, rank: 0 });

  it("uses the supplied phase and rank even when the frozen host fields disagree", () => {
    expect(stateOf(error)).toBe("error");
    expect(viewOf(asks)).toBe(asks.view);
    const working = row("sync", { phase: "working", rank: 20 });
    const rows = [asks, working, reviewing, error, waiting];
    expect(rows.slice().sort(byAttention)).toEqual([waiting, reviewing, error, working, asks]);
    expect(buildAgentGroups(rows, [machine], "state").map(g => [g.state, g.sessions])).toEqual([
      ["needs_you", [waiting, reviewing]], ["error", [error]], ["working", [working]], ["done", [asks]],
    ]);
  });

  it("counts only the manager's counted rows even when a held row has live cards", () => {
    const card: Interaction = { id: "plan", machine_id: machine.id, session_id: reviewing.session_id, kind: "plan" };
    const rows = [asks, error, reviewing, waiting];
    expect(stateCounts(rows, [card])).toMatchObject({ needs: 1, badge: 1 });
    expect(rows.filter(s => matchesFilter(s, "needs", mutes))).toEqual([waiting]);
    expect(othersWaiting(1, reviewing)).toBe(1);
    expect(othersWaiting(1, waiting)).toBe(0);
    expect(Object.values(managerBadges({ default: "default", managers: [{ id: "default", name: "Default", online: true }] }, rows, [card]))).toEqual([1]);
  });

  it("recognizes views in paged rows during a rolling upgrade", () => {
    expect(hasAgentViews({ features: ["sessions_list"], sessionSummaries: [reviewing] })).toBe(true);
    expect(hasAgentViews({ features: ["sessions_list"], sessionSummaries: [{ ...reviewing, view: undefined }] })).toBe(false);
    expect(hasAgentViews({ features: ["agent_view"], sessionSummaries: [] })).toBe(true);
  });

  it("resolves only missing views with each manager's original rows and cards", () => {
    const old = { ...row("owner", { phase: "idle", rank: 40 }), manager: "alpha", view: undefined };
    const child = { ...old, session_id: "child", folded_under: "owner" };
    const other = { ...old, manager: "beta" };
    const card: Interaction = { id: "question", kind: "question", manager: "alpha", machine_id: machine.id, session_id: "child" };
    const shown = listedAgents([old, child, other, error], [card]);
    expect(shown.find(s => s.manager === "alpha")?.view).toMatchObject({ phase: "needs_you", counted: true, reason: { kind: "cards", n: 1 } });
    expect(shown.find(s => s.manager === "beta")?.view?.phase).toBe("working");
    expect(shown).toContain(error);
  });

  it("uses the question in the reason and tolerates a newer phase", () => {
    expect(rowLine(asks)?.text).toBe("Does Tuesday work?");
    const future = { ...waiting, view: JSON.parse('{"phase":"paused","rank":45,"counted":true}') };
    expect(viewOf(future)).toEqual({ phase: "idle", reason: { kind: "host_unknown" }, rank: 45, counted: true });
  });

  it("keeps a cross-computer child's counted request visible where its manager put it", () => {
    const parent = row("parent", { phase: "working", rank: 20 });
    const child = { ...waiting, machine_id: "second", is_subagent: true, folded_under: parent.session_id, folded_under_machine_id: parent.machine_id };
    const card: Interaction = { id: "question", kind: "question", machine_id: child.machine_id, session_id: child.session_id };
    for (const sessions of [[parent, child], [{ ...parent, view: undefined }, { ...child, view: undefined }]]) {
      const shown = listedAgents(sessions, [card]);
      expect(shown.filter(s => s.view?.counted).map(s => s.session_id)).toEqual([child.session_id]);
      expect(stateCounts(shown).badge).toBe(1);
      expect(buildAgentGroups(shown, [machine], "state")[0].sessions[0].session_id).toBe(child.session_id);
    }
  });

  it("looks up card ownership in the manager that sent the card", () => {
    const managers = [{ id: "alpha", name: "Alpha", online: true }, { id: "beta", name: "Beta", online: true }];
    const card: Interaction = { id: "approval", manager: "beta", kind: "permission", machine_id: machine.id, session_id: reviewing.session_id };
    expect(managerBadges({ default: "alpha", managers }, [{ ...reviewing, manager: "alpha" }], [card])).toEqual({ alpha: 0, beta: 1 });
  });
});
