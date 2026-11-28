// Handing work to another computer and stopping idle Agents (#8037): the one message the assistant gets, which Agents
// a hand-off picks by itself, which Agents "Stop idle Agents" may stop, and that stopping goes one at a time and says
// how each went.
import { describe, expect, it } from "vitest";
import { defaultHandoff, handoffMessage, stopAgents, stoppableAgents } from "./machine-handoff";
import type { Session } from "./types";

const agent = (over: Partial<Session>): Session => ({ machine_id: "laptop", session_id: "s", title: "Agent", cwd: "/code", status: "idle", state: "idle", ...over });

describe("handoffMessage", () => {
  it("names both computers, every Agent and to-do by title and id, and how to do it", () => {
    const text = handoffMessage({ id: "laptop", name: "Travel Laptop" }, { id: "mini", name: "Office Mini" }, [
      agent({ session_id: "s1", title: "Widget", todo: { id: "t1", title: "Ship 1.3" } }),
      agent({ session_id: "s2", title: "Screenshots" }),
    ]);
    expect(text).toContain("Travel Laptop（电脑 ID：laptop）");
    expect(text).toContain("Office Mini（电脑 ID：mini）");
    expect(text).toContain("待办「Ship 1.3」（待办 ID：t1），Agent「Widget」（会话 ID：s1）");
    expect(text).toContain("Agent「Screenshots」（会话 ID：s2，无关联待办）");
    expect(text).toContain("continues");
    // the source stops only after the new one started
    expect(text.indexOf("启动后")).toBeLessThan(text.indexOf("再停止 Travel Laptop"));
    expect(text.split("\n").filter((l) => l.startsWith("- "))).toHaveLength(2);
  });
});

describe("defaultHandoff", () => {
  it("picks the working and idle Agents that belong to a to-do, never one that needs you", () => {
    const todo = { id: "t", title: "T" };
    const list = [
      agent({ session_id: "work", status: "working", state: "working", todo }),
      agent({ session_id: "idle", todo }),
      agent({ session_id: "done", status: "done", state: "unread", todo }),
      agent({ session_id: "needs", status: "blocked", state: "needs_you", todo }),
      agent({ session_id: "loose" }),
    ];
    expect(defaultHandoff(list).map((s) => s.session_id)).toEqual(["work", "idle", "done"]);
  });
});

describe("stoppableAgents", () => {
  it("only idle or finished Agents with nothing running, never working or needs-you ones", () => {
    const list = [
      agent({ session_id: "idle" }),
      agent({ session_id: "done", status: "done", state: "unread" }),
      agent({ session_id: "work", status: "working", state: "working" }),
      agent({ session_id: "needs", status: "blocked", state: "needs_you" }),
      agent({ session_id: "loop", loop: { kind: "loop", prompt: "check" } }),
      agent({ session_id: "server", ports: [{ port: 3000 }] as Session["ports"] }),
      agent({ session_id: "readonly", source_kind: "codex_history", capabilities: { read: true } as Session["capabilities"] }),
      agent({ session_id: "assistant", assistant: true }),
      agent({ session_id: "unknown", status: "idle", state: "unknown" }),
    ];
    expect(stoppableAgents(list).map((s) => s.session_id)).toEqual(["idle", "done"]);
  });
});

describe("stopAgents", () => {
  it("stops one at a time and reports each outcome", async () => {
    const order: string[] = [];
    let running = 0;
    const out = await stopAgents([agent({ session_id: "a" }), agent({ session_id: "b" }), agent({ session_id: "c" })], async (s) => {
      running++;
      expect(running).toBe(1);
      order.push(s.session_id);
      await new Promise((r) => setTimeout(r, 1));
      running--;
      if (s.session_id === "b") throw new Error("node offline");
    });
    expect(order).toEqual(["a", "b", "c"]);
    expect(out.map((o) => [o.session.session_id, o.ok, o.error])).toEqual([["a", true, undefined], ["b", false, "node offline"], ["c", true, undefined]]);
  });
});
