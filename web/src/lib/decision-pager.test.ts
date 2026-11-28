import { describe, expect, it } from "vitest";
import { decisionCards, decisionDots, decisionKey, decisionPosition, taskDecisionCards } from "./decision-pager";
import { blockedDecisionSkip, decisionSource } from "./decision-details";
import type { Interaction, Item } from "./types";
import { makeSessionLookup } from "./todos";

function question(id: string, extra: Partial<Interaction> = {}): Interaction {
  return { id, machine_id: "machine", session_id: "session", kind: "question", ...extra };
}

describe("assistant decisions", () => {
  it("keeps the conversation's order, sorts the tasks by age, and removes duplicate cards", () => {
    const own = question("own", { created_at: "2026-11-24T10:00:00Z" });
    const cards = decisionCards([own, question("another")], [
      { interaction: question("later", { created_at: "2026-11-24T09:30:00Z" }), taskTitle: "Later" },
      { interaction: own, taskTitle: "Duplicate" },
      { interaction: question("early", { created_at: "2026-11-24T09:00:00Z" }), taskTitle: "Early" },
    ]);
    expect(cards.map(({ interaction }) => interaction.id)).toEqual(["own", "another", "early", "later"]);
    expect(cards[0].taskTitle).toBeUndefined();
    expect(decisionKey(own)).not.toBe(decisionKey({ ...own, manager: "another-manager" }));
  });

  it("keeps the selected ID on arrival and advances from the previous position when answered", () => {
    expect(decisionPosition(["new", "a", "b", "c"], "b", 1)).toBe(2);
    expect(decisionPosition(["a", "c"], "b", 1)).toBe(1);
    expect(decisionPosition(["a"], "b", 1)).toBe(0);
    expect(decisionPosition([], "b", 1)).toBe(-1);
    expect(decisionDots(11, 10)).toEqual([4, 5, 6, 7, 8, 9, 10]);
  });

  it("only projects active task agents in this conversation and manager", () => {
    const time = "2026-11-24T11:00:00Z";
    const todo = {
      id: "task", origin: "assistant", title: "Check the backup", manager: "local",
      created_at: time, updated_at: time,
      agents: [{ machine_id: "machine", session_id: "session", linked_at: time },
        { machine_id: "machine", session_id: "released", linked_at: time, released_at: time }],
    };
    const own = question("task-card", { manager: "local" });
    const differentManager = question("wrong-manager", { manager: "remote" });
    const released = question("old-card", { manager: "local", session_id: "released" });
    const input = { conversation: "assistant", manager: "local", todos: [todo],
      interactions: [own, differentManager, released], feed: [], plans: {}, lookup: makeSessionLookup([]) };
    expect(taskDecisionCards(input)).toEqual([{ interaction: own, taskTitle: todo.title }]);
    expect(taskDecisionCards({ ...input, conversation: "another-assistant" })).toEqual([]);
    expect(taskDecisionCards({ ...input, manager: "remote" })).toEqual([]);
  });

  it("treats an explicit assistant manager as the built-in default", () => {
    const time = "2026-11-24T11:00:00Z";
    const todo = {
      id: "task", origin: "assistant", title: "Check the backup", manager: "assistant",
      created_at: time, updated_at: time,
      agents: [{ machine_id: "machine", session_id: "session", linked_at: time }],
    };
    const ask = question("assistant-card", { manager: "assistant" });
    expect(taskDecisionCards({ conversation: "assistant", manager: "", todos: [todo], interactions: [ask],
      feed: [], plans: {}, lookup: makeSessionLookup([]) })).toEqual([{ interaction: ask, taskTitle: todo.title }]);
  });

  it("uses a cross-machine child's task home and does not surface supervisor-held requests", () => {
    const time = "2026-11-24T11:00:00Z";
    const agent = { machine_id: "machine", session_id: "session", linked_at: time };
    const makeTodo = (id: string, title: string) => ({
      id, title, origin: "assistant", manager: "local", created_at: time, updated_at: time, agents: [agent],
    });
    const first = makeTodo("a", "Other task");
    const tied = makeTodo("z", "Task with the child");
    const child = question("child", { manager: "local", machine_id: "other-machine", session_id: "worker",
      folded_under: agent.session_id, folded_under_machine_id: agent.machine_id, folded_as: "worker" });
    const held = { ...child, id: "held", delegating: true };
    const lookup = makeSessionLookup([{ machine_id: agent.machine_id, session_id: agent.session_id,
      cwd: "/project", title: "Agent", status: "needs_you", todo: { id: tied.id, title: tied.title } }]);
    const input = { conversation: "assistant", conversationMachine: "machine", manager: "local", todos: [first, tied],
      interactions: [child, held], lookup, feed: [], plans: {} };
    expect(taskDecisionCards(input)).toEqual([{ interaction: child, taskTitle: tied.title }]);
    // The other conversation's task is outside this pager; the request still belongs to this conversation's task
    // that lists the agent, rather than being dropped because the other task came first in the card slots.
    expect(taskDecisionCards({ ...input, main: "assistant", todos: [first, { ...tied, origin: "other-assistant" }] })).toEqual([
      { interaction: child, taskTitle: first.title },
    ]);
    const foldedIntoAssistant = question("task-agent", { manager: "local", folded_under: "assistant" });
    expect(taskDecisionCards({ ...input, interactions: [foldedIntoAssistant] })).toEqual([
      { interaction: foldedIntoAssistant, taskTitle: tied.title },
    ]);
  });

  it("keeps a task this conversation supervises when its origin is another conversation", () => {
    const time = "2026-11-24T11:00:00Z";
    const agent = { machine_id: "machine", session_id: "session", linked_at: time };
    const todo = {
      id: "supervised", title: "Review the change", origin: "other-conversation", manager: "local",
      created_at: time, updated_at: time, agents: [agent],
      supervision: { supervisor: "assistant", state: "user_check" as const },
    };
    const ask = question("supervised-ask", { manager: "local" });
    const input = {
      conversation: "assistant", main: "main", manager: "local", todos: [todo],
      interactions: [ask], feed: [], plans: {}, lookup: makeSessionLookup([]),
    };
    expect(taskDecisionCards(input)).toEqual([{ interaction: ask, taskTitle: todo.title }]);
  });

  it("keeps terminal requests and approvals in their existing answer channels", () => {
    expect(blockedDecisionSkip(question("read", { kind: "screen" }))).toBe("terminal");
    expect(blockedDecisionSkip(question("read-with-chat", { kind: "screen", dialog: { options: [{ key: "2", label: "Reply in chat", kind: "chat" }] } }))).toBe("terminal_chat");
    expect(blockedDecisionSkip(question("command", { kind: "screen", dialog: { command: "echo example" } }))).toBe("permission");
    expect(blockedDecisionSkip(question("permit", { kind: "permission" }))).toBe("permission");
    expect(blockedDecisionSkip(question("plan", { kind: "plan" }))).toBe("plan");
    expect(blockedDecisionSkip(question("plain"))).toBeUndefined();
  });

  it("finds an exact tool call, or the newest question for a terminal-read task", () => {
    const items: Item[] = [
      { id: "request", role: "user", kind: "text", seq: 1 },
      { id: "first", role: "assistant", kind: "tool_use", tool_use_id: "exact", tool_name: "AskUserQuestion", seq: 2 },
      { id: "reply", role: "assistant", kind: "text", text: "Here are two more choices.", seq: 3 },
      { id: "last", role: "assistant", kind: "tool_use", tool_use_id: "other", tool_name: "request_user_input", seq: 4 },
    ];
    expect(decisionSource(question("card", { tool_use_id: "exact" }), items, true)).toBe("request");
    expect(decisionSource(question("terminal", { kind: "screen" }), items, true)).toBe("reply");
    expect(decisionSource(question("missing", { tool_use_id: "missing" }), items, true)).toBeUndefined();
  });
});
