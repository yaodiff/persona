import { describe, expect, it } from "vitest";
import type { Todo } from "./todos";
import type { Session } from "./types";
import {
  classifyTodoDetailTemplate,
  firstSafePrUrl,
  resolveWorkGoalActions,
  safeDetailHttpUrl,
} from "./todo-detail-template";

const baseTodo = (patch: Partial<Todo> = {}): Todo => ({
  id: "t1",
  title: "Sample item",
  created_at: "2026-11-24T10:00:00Z",
  updated_at: "2026-11-24T10:00:00Z",
  ...patch,
});

const makeSession = (patch: Partial<Session> & { machine_id: string; session_id: string }): Session => ({
  agent: "claude",
  title: "Agent",
  cwd: "/repo",
  status: "active",
  state: "working",
  updated_at: "2026-11-24T11:00:00Z",
  ...patch,
});

describe("classifyTodoDetailTemplate", () => {
  it.each(["personal", "quick", "concern"])("keeps explicit %s items personal even with execution signals", (item_type) => {
    const todo = baseTodo({ item_type, origin: "conv-1", plan_id: "plan-1", audience: "autonomous" });
    expect(classifyTodoDetailTemplate(todo, 2, { repositoryCount: 1 })).toBe("personal");
  });

  it.each(["task", "goal"])("uses the work template for an explicit %s without execution signals", (item_type) => {
    expect(classifyTodoDetailTemplate(baseTodo({ item_type, audience: "human" }))).toBe("work_goal");
  });

  it("uses structural signals for an unfamiliar item type", () => {
    expect(classifyTodoDetailTemplate(baseTodo({ item_type: "future" }))).toBe("personal");
    expect(classifyTodoDetailTemplate(baseTodo({ item_type: "future", origin: "conv-1" }))).toBe("work_goal");
  });

  it("classifies plain personal notes and reminders as personal", () => {
    expect(classifyTodoDetailTemplate(baseTodo(), 0)).toBe("personal");
    expect(
      classifyTodoDetailTemplate(
        baseTodo({ audience: "human", due_date: "2026-12-01", project: "ops", notes: "Buy milk" }),
        0,
      ),
    ).toBe("personal");
    // Pure personal item with only direction_id or part_of and no execution signals stays personal
    expect(classifyTodoDetailTemplate(baseTodo({ direction_id: "dir-1", audience: "human" }), 0)).toBe("personal");
  });

  it("classifies items with agents, supervision, repositories, PRs, subtasks, or execution metadata as work_goal", () => {
    expect(
      classifyTodoDetailTemplate(
        baseTodo({ agents: [{ machine_id: "m1", session_id: "s1", linked_at: "2026-11-24T10:00:00Z" }] }),
        0,
      ),
    ).toBe("work_goal");
    expect(
      classifyTodoDetailTemplate(baseTodo({ supervision: { supervisor: "main", state: "needs_user" } }), 0),
    ).toBe("work_goal");
    expect(
      classifyTodoDetailTemplate({ ...baseTodo(), repositories: [{ repo_id: "web" }] }, 0),
    ).toBe("work_goal");
    expect(classifyTodoDetailTemplate(baseTodo(), 0, { repositoryCount: 1 })).toBe("work_goal");
    expect(classifyTodoDetailTemplate(baseTodo({ pr_url: "https://github.com/org/repo/pull/1" }), 0)).toBe("work_goal");
    expect(classifyTodoDetailTemplate({ ...baseTodo(), prs: ["https://github.com/org/repo/pull/2"] }, 0)).toBe("work_goal");
    expect(classifyTodoDetailTemplate(baseTodo({ audience: "human" }), 2)).toBe("work_goal");
    expect(classifyTodoDetailTemplate(baseTodo({ direction_id: "dir-1", origin: "conv-1" }), 0)).toBe("work_goal");
    expect(classifyTodoDetailTemplate(baseTodo({ part_of: "goal-1", audience: "autonomous" }), 0)).toBe("work_goal");
  });
});

describe("safeDetailHttpUrl & firstSafePrUrl", () => {
  it("accepts http(s) URLs and rejects unsafe schemes, credentials, and backslash confusion", () => {
    expect(safeDetailHttpUrl("https://github.com/org/repo/pull/42")).toBe("https://github.com/org/repo/pull/42");
    expect(safeDetailHttpUrl("http://example.com/pr/1")).toBe("http://example.com/pr/1");
    expect(safeDetailHttpUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeDetailHttpUrl("data:text/html,hi")).toBeUndefined();
    expect(safeDetailHttpUrl("ftp://example.com/file")).toBeUndefined();
    expect(safeDetailHttpUrl("not a url")).toBeUndefined();
    expect(safeDetailHttpUrl("https://user:pass@example.com/org/repo/pull/42")).toBeUndefined();
    expect(safeDetailHttpUrl("https://evil.example\\@example.com/org/repo/pull/42")).toBeUndefined();
    expect(safeDetailHttpUrl("https:/org/repo/pull/42")).toBeUndefined();
    expect(safeDetailHttpUrl("https://example.com/a b")).toBeUndefined();
  });

  it("extracts the first safe PR URL across pr_status, pr_url, other_prs, and prs", () => {
    expect(
      firstSafePrUrl(
        baseTodo({
          pr_url: "javascript:alert(1)",
          other_prs: [
            {
              url: "https://github.com/org/repo/pull/9",
              state: "open",
              draft: false,
              review: "pending",
              checks: "pending",
              conflicts: false,
              checked_at: "2026-11-24T11:00:00Z",
            },
          ],
        }),
      ),
    ).toBe("https://github.com/org/repo/pull/9");
  });
});

describe("resolveWorkGoalActions", () => {
  it("keeps hand_over as primary when there is no active agent", () => {
    const res = resolveWorkGoalActions(baseTodo(), []);
    expect(res).toEqual({
      primaryAction: "hand_over",
      handOverTone: "accent",
      handOverMode: "hand_over",
    });
  });

  it("ignores released, failed-start, and offline agents", () => {
    const todo = baseTodo({
      agents: [
        { machine_id: "m1", session_id: "s-released", linked_at: "2026-11-24T10:00:00Z", released_at: "2026-11-24T10:30:00Z" },
        { machine_id: "m1", session_id: "s-failed", linked_at: "2026-11-24T10:00:00Z", start_error: "boom" },
        { machine_id: "m1", session_id: "s-offline", linked_at: "2026-11-24T10:00:00Z" },
      ],
    });
    const sessions: Session[] = [
      makeSession({ machine_id: "m1", session_id: "s-released", state: "working" }),
      makeSession({ machine_id: "m1", session_id: "s-offline", state: "offline" }),
    ];
    expect(resolveWorkGoalActions(todo, sessions).primaryAction).toBe("hand_over");
  });

  it("prioritizes needs_you over working over idle and demotes Hand to Agent to Add Agent", () => {
    const todo = baseTodo({
      pr_url: "https://github.com/org/repo/pull/77",
      agents: [
        { machine_id: "m1", session_id: "s-idle", linked_at: "2026-11-24T10:00:00Z" },
        { machine_id: "m1", session_id: "s-work", linked_at: "2026-11-24T10:10:00Z" },
        { machine_id: "m2", session_id: "s-ask", linked_at: "2026-11-24T10:20:00Z" },
      ],
    });
    const sessions: Session[] = [
      makeSession({ machine_id: "m1", session_id: "s-idle", state: "idle", title: "Idle worker" }),
      makeSession({ machine_id: "m1", session_id: "s-work", state: "working", title: "Working worker" }),
      makeSession({ machine_id: "m2", session_id: "s-ask", state: "needs_you", title: "Needs you worker" }),
    ];
    const res = resolveWorkGoalActions(todo, sessions);
    expect(res).toEqual({
      primaryAction: "open_agent",
      activeAgent: {
        machineId: "m2",
        sessionId: "s-ask",
        title: "Needs you worker",
        state: "needs_you",
      },
      prUrl: "https://github.com/org/repo/pull/77",
      handOverTone: "secondary",
      handOverMode: "add_agent",
    });
  });

  it("falls back to working agent when none needs_you", () => {
    const todo = baseTodo({
      agents: [
        { machine_id: "m1", session_id: "s-idle", linked_at: "2026-11-24T10:00:00Z" },
        { machine_id: "m1", session_id: "s-work", linked_at: "2026-11-24T10:10:00Z" },
      ],
    });
    const sessions: Session[] = [
      makeSession({ machine_id: "m1", session_id: "s-idle", state: "idle" }),
      makeSession({ machine_id: "m1", session_id: "s-work", state: "working", title: "Active coder" }),
    ];
    const res = resolveWorkGoalActions(todo, sessions);
    expect(res.primaryAction).toBe("open_agent");
    expect(res.activeAgent?.sessionId).toBe("s-work");
    expect(res.handOverMode).toBe("add_agent");
    expect(res.handOverTone).toBe("secondary");
  });
});
