// The context bar below the composer (#2430, moved by #5056): shown whenever context is known, whatever "Show details in
// Agent rows" says. The model is the composer's model pill (#5032), not the usage row's.
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { t } from "../../i18n";
import { setAgentDetails } from "../../lib/context-usage";
import type { Session } from "../../lib/types";
import { ContextMeter } from "./ContextMeter";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const s: Session = { machine_id: "m", session_id: "s", title: "t", cwd: "/code", status: "idle", model: "claude-opus-5-5", context_tokens: 78000, context_window: 200000 };

describe("ContextMeter", () => {
  afterEach(() => {
    setAgentDetails(false);
  });
  it("shows labelled usage even while the sidebar details setting is off", () => {
    setAgentDetails(false);
    const html = renderToStaticMarkup(<ContextMeter session={{ ...s, context_tokens: 20000 }} />);
    expect(html).toContain(">上下文 10%<");
    expect(html).toContain('aria-valuenow="10"');
    expect(html).toContain('style="width:10%;');
  });
  it.each([170000, 180000])("shows amber caution when using %s of a 200k window", (tokens) => {
    const html = renderToStaticMarkup(<ContextMeter session={{ ...s, context_tokens: tokens }} />);
    const percent = tokens / 2000;
    expect(html).toContain(`>上下文 ${percent}%<`);
    expect(html).toContain(`aria-valuenow="${percent}"`);
    expect(html).toContain(`style="width:${percent}%;`);
    expect(html).toContain("bg-orange");
    expect(html).toContain("text-orange");
    expect(html).toContain("/compact");
  });
  it("shows nothing for a model without any observed usage", () => {
    setAgentDetails(true);
    expect(renderToStaticMarkup(<ContextMeter session={{ ...s, context_tokens: undefined, context_window: undefined }} />)).toBe("");
  });
  it("names both shares for assistive technology and in the tooltip", () => {
    const html = renderToStaticMarkup(<ContextMeter session={s} />);
    expect(html).toMatch(/^<button type="button" aria-haspopup="dialog" aria-expanded="false"/);
    expect(html).toContain('aria-label="上下文已用 39%，剩余 61%"');
    expect(html).toContain('role="progressbar" aria-label="上下文已用 39%，剩余 61%" aria-valuenow="39"');
    expect(html).toMatch(/title="上下文已用 39%，剩余 61%\n上下文表示 Agent/);
    expect(html).toContain('aria-description="上下文表示 Agent 在当前对话中能够记住的内容容量');
    expect(html).not.toContain("opus");
  });
  it("keeps the labelled share and popover button in compact mode", () => {
    const html = renderToStaticMarkup(<ContextMeter session={s} compact />);
    expect(html).toMatch(/^<button type="button" aria-haspopup="dialog"/);
    expect(html).toContain(">上下文 39%<");
    expect(html).toContain('aria-label="上下文已用 39%，剩余 61%"');
    expect(html).not.toContain("progressbar");
    expect(html).not.toContain("opus");
    expect(renderToStaticMarkup(<ContextMeter session={{ ...s, context_tokens: 0 }} compact />)).toBe("");
  });
  it("shows current agy context ahead of cumulative usage when the window is unknown", () => {
    const html = renderToStaticMarkup(
      <ContextMeter session={{ ...s, agent: "agy", model: "custom", context_tokens: 7200, context_window: 0 }}
        items={[{ id: "1", seq: 1, role: "assistant", kind: "text", usage: [{ in: 25_000 }] }]} />,
    );
    expect(html).toContain(t("context.current_tokens", { tokens: "7.2k" }));
    expect(html).not.toMatch(/progressbar|%|25.0k/);
  });
  it("shows a compact token pill when context window is unknown but item token usage exists", () => {
    const html = renderToStaticMarkup(
      <ContextMeter
        session={{ ...s, context_tokens: undefined, context_window: undefined, assistant: true }}
        items={[{ id: "1", seq: 1, role: "assistant", kind: "text", usage: [{ in: 2500, out: 1500 }] }]}
        assistant
      />,
    );
    expect(html).toContain("4.0k tok");
    expect(html).not.toContain("progressbar");
  });
  it("deduplicates subagent reports by per-field maxima so the pill total matches aggregateSessionTokens", () => {
    const html = renderToStaticMarkup(
      <ContextMeter
        session={{ ...s, context_tokens: undefined, context_window: undefined }}
        items={[
          {
            id: "1",
            seq: 1,
            role: "assistant",
            kind: "text",
            usage: [{ id: "sub-1", model: "anthropic/claude-opus-4-5", in: 1200, out: 400 }],
          },
          {
            id: "2",
            seq: 2,
            role: "assistant",
            kind: "text",
            usage: [{ id: "sub-1", model: "claude-opus-4-5-20250929", in: 900, out: 700 }],
          },
        ]}
      />,
    );
    // max(1200, 900) + max(400, 700) = 1900 -> 1.9k tok (not max(1600, 1600) = 1.6k tok)
    expect(html).toContain("1.9k tok");
  });
});
