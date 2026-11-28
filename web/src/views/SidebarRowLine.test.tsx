// The sidebar's agent rows say under their title what they need from you or what they did (#3195): no hover needed.
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Interaction, Session } from "../lib/types";

// the sidebar's modules read the colour scheme and this browser's settings when they load
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

const base: Session = { machine_id: "mac", session_id: "s1", title: "补齐网络层的单元测试", cwd: "/code", status: "idle" };
const props = { k: "s:mac/s1", reg: () => () => {}, hover: () => {}, active: false, to: { name: "session", mid: "mac", sid: "s1" } as const, go: () => () => {} };
const render = (s: Session, interactions: Interaction[] = []) => {
  fake.interactions = interactions;
  return renderToStaticMarkup(<SessionRow {...props} s={s} />);
};
const line = (html: string) => html.match(/<span data-row-line[^>]*>(.*?)<\/span><\/span>/)?.[1]?.replace(/<[^>]+>/g, "");

describe("an agent's sidebar row (#3195)", () => {
  it("says what its card asks, under the title", () => {
    const card = { id: "q1", machine_id: "mac", session_id: "s1", kind: "question", questions: [{ question: "网络层的测试用哪个框架？" }] } as Interaction;
    const html = render({ ...base, state: "needs_you", pending_interactions: 1 }, [card]);
    expect(line(html)).toBe("向你提问：网络层的测试用哪个框架？");
    expect(html).toMatch(/<span class="font-medium text-attention">向你提问：<\/span>/);
  });

  it("does not take another agent's card", () => {
    const other = { id: "q2", machine_id: "mac", session_id: "s9", kind: "question", questions: [{ question: "别人的问题" }] } as Interaction;
    expect(render({ ...base, state: "needs_you", attention_reason: "等你在飞书里确认" }, [other])).not.toContain("别人的问题");
    expect(line(render({ ...base, state: "needs_you", attention_reason: "等你在飞书里确认" }, [other]))).toBe("等你在飞书里确认");
  });

  it("says what it did when it finished unseen", () => {
    expect(line(render({ ...base, state: "unread", last_text: "发布说明整理好了，按功能分了三段。" }))).toBe("发布说明整理好了，按功能分了三段。");
  });

  it("stays one line while it works, once seen, and for a Persona conversation", () => {
    expect(render({ ...base, state: "working", last_text: "在跑测试" })).not.toContain("data-row-line");
    expect(render({ ...base, state: "idle", last_text: "好了" })).not.toContain("data-row-line");
    expect(renderToStaticMarkup(<SessionRow {...props} s={{ ...base, state: "unread", last_text: "好了" }} showState={false} />)).not.toContain("data-row-line");
  });

  it("shows its own plan while it works (#5302)", () => {
    const plan = { done: 3, total: 5, current: "Reproducing the crash" };
    const html = render({ ...base, state: "working", plan });
    expect(html.match(/<span data-plan-line[^>]*>.*?<span class="truncate tabular-nums">(.*?)<\/span>/)?.[1]).toBe("3/5 · Reproducing the crash");
    expect(render({ ...base, state: "idle", plan: { done: 2, total: 2 } })).not.toContain("data-plan-line");
    expect(render({ ...base, state: "offline", plan })).not.toContain("data-plan-line");
  });
});
