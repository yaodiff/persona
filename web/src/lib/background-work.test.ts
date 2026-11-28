import { describe, expect, it } from "vitest";
import { backgroundStall, backgroundTurn, backgroundWork, taskBackgroundWork, waitingOnSubagents } from "./background-work";
import type { Session } from "./types";
import type { Todo } from "./todos";

const session: Session = { machine_id: "fixture-device", session_id: "worker", title: "Fixture", cwd: "/demo", state: "working", status: "working" };
const flow = { task_id: "wf", status: "running", running: true, phase: "Review", agents_done: 5, agents_started: 6 };

describe("background work projections", () => {
  it("summary zeros override stale detail arrays instead of resurrecting completed work", () => {
    expect(backgroundWork({ ...session, waiting: false, background_tasks_count: 0, background_count: 7,
      workflows: [flow], subagents: [{ id: "sub", session_id: "worker@sub", running: true }], background: [{ id: "shell", kind: "shell" }] })).toBeUndefined();
  });

  it("old raw summary counts preserve active work without guessing an independent job count", () => {
    expect(backgroundWork({ ...session, waiting: true, background_count: 7 })?.tasks).toBe(0);
    expect(backgroundWork({ ...session, waiting: true, background_count: 7 })).toBeDefined();
  });

  it("legacy full details classify workflow and subagent entries without inventing shell jobs", () => {
    expect(backgroundWork({ ...session, background: [{ id: "wf", kind: "workflow" }, { id: "sub", kind: "agent" }, { id: "mon", kind: "monitor" }] }))
      .toEqual({ tasks: 1, subagents: 1, workflows: 1, workflow: undefined });
  });

  it("deduplicates identities before an answered-state lookup that returns new objects", () => {
    const binding = { machine_id: session.machine_id, session_id: session.session_id, linked_at: "2026-11-10T08:00:00Z" };
    const todo: Todo = { id: "task", title: "Fixture", created_at: "2026-11-10T08:00:00Z", updated_at: "2026-11-10T08:00:00Z", agents: [binding, binding, { ...binding, session_id: "released", released_at: "2026-11-10T08:00:00Z" }] };
    const lookup = () => ({ ...session, waiting: true, background_tasks_count: 2 });
    expect(taskBackgroundWork(todo, lookup)?.tasks).toBe(2);
    expect(taskBackgroundWork({ ...todo, done_at: "2026-11-10T08:00:00Z" }, lookup)).toBeUndefined();
  });
});

describe("waitingOnSubagents (#5030)", () => {
  const sub = { id: "a", session_id: "worker@a", running: true, background: true };
  it("the turn is over and a background subagent still runs", () => {
    expect(waitingOnSubagents({ ...session, background_working: true, subagents: [sub] })).toBe(true);
    expect(waitingOnSubagents({ ...session, background_working: true }, [sub])).toBe(true);
  });
  it("not while the turn runs, not for a foreground or finished one, not when a workflow says it first", () => {
    expect(waitingOnSubagents({ ...session, subagents: [sub] })).toBe(false);
    expect(waitingOnSubagents({ ...session, background_working: true, subagents: [{ ...sub, background: false }] })).toBe(false);
    expect(waitingOnSubagents({ ...session, background_working: true, subagents: [{ ...sub, running: false }] })).toBe(false);
    expect(waitingOnSubagents({ ...session, background_working: true, subagents: [sub], workflows: [flow] })).toBe(false);
    expect(waitingOnSubagents(undefined)).toBe(false);
  });
});

describe("backgroundTurn (#7341)", () => {
  const quiet = { streaming: false, outgoing: false, awaiting: false };
  const shell = { id: "build", kind: "shell", description: "Build release", command: "make release\nmake sign", started_at: "2026-11-13T01:02:03Z" };
  const sub = { id: "a", session_id: "worker@a", running: true, background: true };

  it("a real background command with the turn over returns the command kind and its own line", () => {
    const s = { ...session, background_working: true, state_since: "2026-11-13T01:00:00Z",
      background: [shell, { id: "watch", kind: "monitor", command: "watch logs", started_at: "2026-11-13T01:03:00Z" }] };
    expect(backgroundTurn(s, undefined, quiet)).toEqual({ kind: "command", line: { what: "Build release", more: 1, since: shell.started_at } });
  });

  it("the flag alone still returns the command kind with an empty line for an older hub or runtime", () => {
    expect(backgroundTurn({ ...session, background_working: true }, undefined, quiet)).toEqual({ kind: "command", line: { what: "", more: 0 } });
  });

  it("agent or workflow-only background payloads do not become a shell-command fallback", () => {
    for (const kind of ["agent", "workflow"]) {
      expect(backgroundTurn({ ...session, background_working: true, background: [{ id: kind, kind }] }, undefined, quiet)).toBeNull();
    }
  });

  it("orders workflow, subagents, wait, then command without changing the existing wait line", () => {
    const base = { ...session, background_working: true, background_waiting: true, state_since: "2026-11-13T01:01:00Z", background: [shell], subagents: [sub] };
    expect(backgroundTurn({ ...base, workflows: [flow] }, [sub], quiet)).toEqual({ kind: "workflow" });
    expect(backgroundTurn(base, [sub], quiet)).toEqual({ kind: "subtasks" });
    expect(backgroundTurn(base, [], quiet)).toEqual({ kind: "waiting", line: { what: "Build release", more: 0, since: base.state_since } });
  });

  it("streaming, an outgoing message and awaiting each make it a live turn again", () => {
    const s = { ...session, background_working: true, background: [shell] };
    expect(backgroundTurn(s, undefined, { ...quiet, streaming: true })).toBeNull();
    expect(backgroundTurn(s, undefined, { ...quiet, outgoing: true })).toBeNull();
    expect(backgroundTurn(s, undefined, { ...quiet, awaiting: true })).toBeNull();
  });

  it("a foreground turn with a listed command, false background_working and an older hub stay live", () => {
    expect(backgroundTurn({ ...session, background: [shell] }, undefined, quiet)).toBeNull();
    expect(backgroundTurn({ ...session, background_working: false, background: [shell] }, undefined, quiet)).toBeNull();
    expect(backgroundTurn(undefined, undefined, quiet)).toBeNull();
  });
});

describe("backgroundStall (#13744)", () => {
  const base = { ...session, state: "working" as const, background_working: true, background_stalled_since: "2026-11-13T01:00:00Z" };
  const at = (min: number) => Date.parse("2026-11-13T01:00:00Z") + min * 60_000;
  it("says how long a Working background session has been quiet, in one unit", () => {
    expect(backgroundStall(base, at(12))?.text).toBe("后台命令已 12 分钟无活动");
    expect(backgroundStall(base, at(185))?.text).toBe("后台命令已 3 小时无活动");
    expect(backgroundStall(base, at(60 * 24 * 3))?.text).toBe("后台命令已 3 天无活动");
    expect(backgroundStall(base, at(12))?.title).toContain("当前状态仍计为「工作中」");
  });
  it("shows nothing without a stall, or for a session that is not Working in the background", () => {
    expect(backgroundStall({ ...base, background_stalled_since: undefined }, at(12))).toBeNull();
    expect(backgroundStall({ ...base, background_stalled_since: "0001-01-01T00:00:00Z" }, at(12))).toBeNull();
    expect(backgroundStall({ ...base, state: "idle" }, at(12))).toBeNull();
    expect(backgroundStall({ ...base, background_working: false }, at(12))).toBeNull();
    expect(backgroundStall({ ...base, background_waiting: true }, at(12))).toBeNull();
    expect(backgroundStall(undefined)).toBeNull();
  });
});

describe("a stalled background command's status line (#13744)", () => {
  it("carries the time it went quiet, and only while it is not waiting", () => {
    const shell = { id: "b1", kind: "shell", description: "pnpm test", started_at: "2026-11-13T00:00:00Z" };
    const busy = { ...session, background_working: true, background: [shell], background_stalled_since: "2026-11-13T01:00:00Z" };
    const idle = { streaming: false, outgoing: false, awaiting: false };
    expect(backgroundTurn(busy, [], idle)).toMatchObject({ kind: "command", line: { what: "pnpm test", stalled: "2026-11-13T01:00:00Z" } });
    expect(backgroundTurn({ ...busy, background_stalled_since: undefined }, [], idle)?.line?.stalled).toBeUndefined();
  });
});
