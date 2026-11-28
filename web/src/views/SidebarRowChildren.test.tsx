// A parent's sidebar row carries its child sessions (#5120): its mark is the folded state, its second line what a
// waiting child asks, its count holds the children's cards, and a line says how many subagents run.
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Interaction, Session } from "../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const fake = vi.hoisted(() => ({ interactions: [] as Interaction[] }));
vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), interactions: fake.interactions }) };
});

const { SessionRow } = await import("./Sidebar");
const { doneOverviewSubagentsAsk, useOverviewSubagentsAsk } = await import("../lib/subagent-link");

const parent: Session = { machine_id: "mac", session_id: "lead", title: "Migrate the tests", cwd: "/code", status: "idle", state: "idle" };
const props = { k: "s:mac/lead", reg: () => () => {}, hover: () => {}, active: false, to: { name: "session", mid: "mac", sid: "lead" } as const, go: () => () => {} };
const render = (s: Session, interactions: Interaction[] = []) => {
  fake.interactions = interactions;
  return renderToStaticMarkup(<SessionRow {...props} s={s} />);
};
const mark = (html: string) => html.match(/class="status-mark[^"]*"[^>]*data-state="([^"]+)"/)?.[1];
const line = (html: string) => html.match(/<span data-row-line[^>]*>((?:<span[^>]*>.*?<\/span>)?[^<]*)<\/span>/)?.[1]?.replace(/<[^>]+>/g, "");
const childLine = (html: string) => html.match(/<span data-children-line="([^"]+)"[^>]*>.*?<span class="truncate tabular-nums">(.*?)<\/span>/)?.[2];
const childLineKind = (html: string) => html.match(/<span data-children-line="([^"]+)"/)?.[1];
const badge = (html: string) => html.match(/text-attention tabular-nums[^"]*">(\d+)<\/span>/)?.[1];

describe("a parent's sidebar row (#5120, #14323)", () => {
  it("draws as before without children, and stays quiet when all children have ended", () => {
    const html = render(parent);
    expect(mark(html)).toBe("idle");
    expect(html).not.toContain("data-children-line");
    expect(badge(html)).toBeUndefined();
    const ended = render({ ...parent, children: { total: 3, working: 0, needs_you: 0 } });
    expect(ended).not.toContain("data-children-line");
  });

  it("works while a child works, and says how many subagents run", () => {
    const html = render({ ...parent, children: { total: 3, working: 2 } });
    expect(mark(html)).toBe("working");
    expect(childLineKind(html)).toBe("running");
    expect(childLine(html)).toBe("2 个运行中");
  });

  it("counts Paseo runtime background agents once and sums disjoint embedded + cross-machine children", () => {
    // a Paseo runtime parent that lists its running children as its own background agents too
    const paseo = render({ ...parent, source_kind: "runtime", waiting: false, running_subagents: 3, children: { total: 3, working: 2 } });
    expect(childLine(paseo)).toBe("3 个运行中");
    // a Claude parent with 1 embedded subagent + 1 cross-machine A2A child
    const mixed = render({ ...parent, agent: "claude", subagents: [{ id: "a1", session_id: "lead@a1", running: true }], children: { total: 1, working: 1 } });
    expect(childLine(mixed)).toBe("2 个运行中");
  });

  it("needs you when a child asks: attention subtitle takes precedence over running, and count holds it", () => {
    const card = { id: "c1", machine_id: "mac", session_id: "child", folded_under: "lead", kind: "question", questions: [{ question: "Which test runner?" }] } as Interaction;
    const html = render({ ...parent, children: { total: 3, working: 2, needs_you: 1 } }, [card]);
    expect(mark(html)).toBe("needs_you");
    expect(line(html)).toBe("向你提问：Which test runner?");
    expect(childLineKind(html)).toBe("needs_you");
    expect(childLine(html)).toBe("1 个待你确认");
    expect(badge(html)).toBe("1");
  });

  it("counts its own cards and its same-machine + cross-machine children's cards together", () => {
    const own = { id: "o1", machine_id: "mac", session_id: "lead", kind: "permission", tool_name: "Bash", tool_input: { command: "swift test" } } as Interaction;
    const child = { id: "c1", machine_id: "mac", session_id: "child", folded_under: "lead", kind: "question", questions: [{ question: "Which test runner?" }] } as Interaction;
    const crossChild = { id: "x2", machine_id: "box", session_id: "remote-child", folded_under: "lead", folded_under_machine_id: "mac", kind: "question", questions: [{ question: "Deploy to staging?" }] } as Interaction;
    const other = { id: "x1", machine_id: "other", session_id: "child", folded_under: "lead", kind: "question", questions: [{ question: "Not this one" }] } as Interaction;
    const html = render({ ...parent, state: "needs_you", pending_interactions: 1, children: { total: 2, needs_you: 2 } }, [own, child, crossChild, other]);
    expect(badge(html)).toBe("3");
    expect(line(html)).not.toContain("Which test runner?");
    expect(html).not.toContain("Not this one");
  });

  it("registers an overview subagents focus ask when clicking the subtitle", () => {
    expect(typeof useOverviewSubagentsAsk).toBe("function");
    doneOverviewSubagentsAsk("mac", "lead");
  });
});

describe("a Working row whose background commands look stuck (#13744)", () => {
  const since = new Date(Date.now() - 3 * 3600_000 - 60_000).toISOString();
  const bg: Session = { ...parent, status: "done", state: "working", background_working: true, background_stalled_since: since };
  it("stays Working and says how long they have been quiet", () => {
    const html = render(bg);
    expect(mark(html)).toBe("working");
    expect(html).toContain("data-bg-stalled");
    expect(html).toContain("后台命令已 3 小时无活动");
  });
  it("shows nothing for a row without the stall, or one that is not Working", () => {
    expect(render({ ...bg, background_stalled_since: undefined })).not.toContain("data-bg-stalled");
    expect(render({ ...bg, state: "idle", status: "idle" })).not.toContain("data-bg-stalled");
  });
});
