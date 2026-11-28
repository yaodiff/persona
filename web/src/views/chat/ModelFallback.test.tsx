// #2528: the fallback card says what the record says and, after a restore, only that the next start will use the
// configured model, never that it runs; the refusal notice is a line of its own with the record verbatim; the
// missing-fallback turn error offers no "Continue". In a Persona conversation both say one plain sentence and keep the
// record under a closed Details (#5202); a terminal session shows everything.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { chatLines } from "../../lib/turns";
import type { Item, Session } from "../../lib/types";
import { RefusalNotice } from "./RefusalNotice";
import { ModelFallbackCard } from "./ModelFallbackCard";

const fallback = { subtype: "model_refusal_fallback", original_model: "primary-model", fallback_model: "fallback-model", scope: "session" };
const base = { machine_id: "m", session_id: "s", state: "idle", model_fallback: fallback } as unknown as Session;
const pending = { model_restore: { state: "next_start", to: "configured-model", at: "2026-11-09T00:00:00Z" } } as Partial<Session>;

const card = (extra: Partial<Session> = {}, headless = true, onNewMain?: () => void) =>
  renderToStaticMarkup(<ModelFallbackCard session={{ ...base, ...extra } as Session} headless={headless} onNewMain={onNewMain} />);

describe("ModelFallbackCard", () => {
  it("before a restore: one plain sentence, the record under a closed Details, and a button that names no model", () => {
    const html = card({ turn_error: "API Error: 404 fallback-model is not available" });
    expect(html).toContain("上游拒绝请求后，此对话当前使用 fallback-model 进行回复。");
    expect(html).not.toContain("上游拒绝请求后，此会话已切换至回退模型");
    expect(html).toMatch(/<details data-testid="model-fallback-details"(?![^>]*\bopen\b)[^>]*><summary[^>]*>详情<\/summary>/);
    const details = html.slice(html.indexOf("<details"), html.indexOf("</details>"));
    expect(details).toContain("原请求模型：primary-model");
    expect(details).toContain("回退模型：fallback-model");
    expect(details).toContain("后续错误：API Error: 404 fallback-model is not available");
    expect(details).toContain("不会重发任何消息");
    const buttons = [...html.matchAll(/<button[^>]*>(.*?)<\/button>/g)].map((m) => m[1].replace(/<[^>]+>/g, ""));
    expect(buttons).toEqual(["恢复已配置模型"]);
    expect(html).not.toContain("下次启动");
  });
  it("pending: says only that the next start will use the configured model, with no button and no claim it runs", () => {
    const html = card(pending);
    expect(html).toContain("下次启动将使用已配置的模型");
    expect(html).toContain("已配置模型：configured-model");
    expect(html).toContain("下一次回复");
    expect(html).toContain("回退模型：fallback-model");
    expect(html).not.toContain("<button");
    expect(html).not.toMatch(/已恢复|已换回|正在使用配置|now uses|restored/);
  });
  it("is disabled while the session works, and offers New main chat only when given", () => {
    expect(card({ state: "working" } as Partial<Session>)).toMatch(/<button[^>]*disabled/);
    expect(card()).not.toContain("新建主对话");
    expect(card({}, true, () => {})).toContain("新建主对话");
    expect(card(pending, true, () => {})).toContain("新建主对话"); // the way out stays while a restore is pending
  });
  it("a terminal session gets the manual step and no restore button", () => {
    const html = card({}, false);
    expect(html).toContain("/model");
    expect(html).not.toContain("<button");
  });
  it("a terminal session shows the record as it is, with no Details", () => {
    const html = card({ turn_error: "API Error: 404" }, false);
    expect(html).toContain("上游拒绝请求后，此会话已切换至回退模型");
    expect(html).toContain("原请求模型：primary-model");
    expect(html).toContain("后续错误：API Error: 404");
    expect(html).not.toContain("<details");
  });
  it("names Claude's models by their short names in the plain sentence, and the configured one while pending", () => {
    const claude = { model_fallback: { ...fallback, original_model: "claude-opus-5-5", fallback_model: "claude-sonnet-5-5" } } as Partial<Session>;
    expect(card(claude)).toContain("此对话当前使用 Sonnet 5.5 进行回复。");
    const waiting = card({ ...claude, model_restore: { state: "next_start", to: "claude-opus-5-5", at: "2026-11-09T00:00:00Z" } } as Partial<Session>);
    expect(waiting).toContain("已配置模型：Opus 5.5");
    expect(waiting.slice(waiting.indexOf("<details"))).toContain("回退模型：claude-sonnet-5-5");
  });
  it("nothing without an active fallback", () => {
    expect(card({ model_fallback: undefined })).toBe("");
  });
  it("appends the node's recovery cause to the plain sentence without changing a runtime record's wording", () => {
    const caused = card({ model_fallback: { ...fallback, category: "rate_limited" } });
    expect(caused).toContain("上游拒绝请求后，此对话当前使用 fallback-model 进行回复（触发限流）。");
    expect(caused).not.toContain("上游拒绝请求后，此对话当前使用 fallback-model 进行回复。");
    // no recovery fields, like every runtime-written record: the original sentence and nothing more
    const runtime = card();
    expect(runtime).toContain("上游拒绝请求后，此对话当前使用 fallback-model 进行回复。");
    expect(runtime).not.toContain("（");
  });
  it("shows the returning line with the configured model once a probe was answered", () => {
    const html = card({ model_fallback: { ...fallback, original_model: "primary-model", recovery_returning: true } });
    expect(html).toContain("primary-model 已恢复可用，下一条消息将自动切回");
    expect(html).toMatch(/data-testid="model-fallback-returning"/);
    // without the flag the line is absent
    expect(card()).not.toContain("已恢复");
  });
  it("hides the card for an exhausted chain, whose failure card carries the recovery instead", () => {
    expect(card({ model_fallback: { ...fallback, fallback_model: "", recovery_exhausted: true } })).toBe("");
  });
});

describe("a refusal notice", () => {
  const MARKER = "HIDDEN-MARKER-2528-web";
  const words = "Upstream refused <this> request\n  policy & more";
  // what an older hub sent: the record's whole line as the notice's text and as model_refusal.raw
  const line = `{"type":"system","subtype":"model_refusal_fallback","content":"x","syntheticHidden":{"nested":{"deeper":"${MARKER}"}}}`;
  const refusal = { subtype: "model_refusal_fallback", original_model: "primary-x", fallback_model: "fallback-y", category: "cat-z", scope: "session", explanation: "Upstream refused", text: words };
  const items: Item[] = [
    { id: "u1", seq: 1, role: "user", kind: "text", text: "hi" } as Item,
    { id: "t1", seq: 2, role: "assistant", kind: "text", text: "working" } as Item,
    { id: "r1", seq: 3, role: "system", kind: "notice", text: words, model_refusal: refusal } as Item,
    { id: "t2", seq: 4, role: "assistant", kind: "text", text: "answer" } as Item,
  ];
  const legacy = items.map((i) => (i.id === "r1" ? ({ ...i, text: line, model_refusal: { ...refusal, raw: line } } as unknown as Item) : i));
  const render = (its: Item[], assistant = false) => {
    const line = chatLines(its).find((l) => l.kind === "notice")!;
    return renderToStaticMarkup(<RefusalNotice item={line.item} plain={assistant} />);
  };
  const escaped = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  it("is a line of its own, not folded into the work row", () => {
    const n = chatLines(items).find((l) => l.kind === "notice");
    expect(n?.kind === "notice" && n.item.id).toBe("r1");
  });
  it("shows the original and fallback models, category, scope, explanation and the refusal's words untouched", () => {
    for (const its of [items, legacy]) {
      const html = render(its);
      expect(html).toContain('data-testid="refusal-notice"');
      expect(html).toContain("Claude Code 已自动从 primary-x 切换至 fallback-y");
      expect(html).toContain("本会话的后续轮次可能会继续使用该模型");
      expect(html).toContain("类别：cat-z");
      expect(html).toContain("说明：Upstream refused");
      expect(html).toContain("拒绝原文");
      expect(html).toContain(escaped(words));
    }
  });
  it("never shows the record's line an older hub sent, nor its hidden fields", () => {
    const html = render(legacy);
    expect(html).not.toContain(MARKER);
    expect(html).not.toContain("syntheticHidden");
  });
  it("in a Persona conversation: one plain sentence with the short name, the whole record under a closed Details", () => {
    const claude = items.map((i) => (i.id === "r1" ? { ...i, model_refusal: { ...refusal, original_model: "claude-opus-5-5", fallback_model: "claude-sonnet-5-5" } } : i));
    const html = render(claude, true);
    expect(html).toContain("上游拒绝了此请求，已自动切换至 Sonnet 5.5。");
    expect(html).toMatch(/<details data-testid="refusal-details"(?![^>]*\bopen\b)[^>]*><summary[^>]*>详情<\/summary>/);
    const details = html.slice(html.indexOf("<details"), html.indexOf("</details>"));
    for (const part of ["Claude Code 已自动从 claude-opus-5-5 切换至 claude-sonnet-5-5", "类别：cat-z", "说明：Upstream refused", "拒绝原文", escaped(words)]) {
      expect(details).toContain(part);
    }
    const plain = render(items.map((i) => (i.id === "r1" ? { ...i, model_refusal: { subtype: "model_refusal_future", text: "future words" } } : i)), true);
    expect(plain).toContain("上游拒绝了此请求<");
    expect(plain).not.toContain("已自动切换至");
    expect(render(items)).not.toContain("<details"); // an Agents conversation shows it all
  });
  it("an unknown or blocked record claims no switch", () => {
    const html = render(items.map((i) => (i.id === "r1" ? { ...i, model_refusal: { subtype: "model_refusal_future", text: "future words" } } : i)));
    expect(html).toContain("记录中未显示切换至其他模型");
    expect(html).not.toContain("已自动从");
    expect(html).toContain("future words");
  });
});

describe("a refusal notice with the node's recovery marker (#7824)", () => {
  const words = "Upstream refused";
  const item = (refusal: Record<string, unknown>): Item[] => [
    { id: "u1", seq: 1, role: "user", kind: "text", text: "hi" } as Item,
    { id: "r1", seq: 2, role: "system", kind: "notice", text: words, model_refusal: refusal } as unknown as Item,
  ];
  const render = (refusal: Record<string, unknown>, assistant = true) => {
    const line = chatLines(item(refusal)).find((l) => l.kind === "notice")!;
    return renderToStaticMarkup(<RefusalNotice item={line.item} plain={assistant} />);
  };
  it("appends the plain-words cause in the Persona one-sentence mode", () => {
    const html = render({
      subtype: "model_refusal_fallback", original_model: "claude-opus-5-5", fallback_model: "claude-sonnet-5-5",
      category: "quota_exhausted", scope: "session",
    });
    expect(html).toContain("上游拒绝了此请求，已自动切换至 Sonnet 5.5（额度已用尽）。");
  });
  it("shows the returning line with the configured model's short name", () => {
    const html = render({
      subtype: "model_refusal_fallback", original_model: "claude-opus-5-5", fallback_model: "claude-sonnet-5-5",
      recovery_returning: true,
    });
    expect(html).toMatch(/data-testid="refusal-returning"/);
    expect(html).toContain("Opus 5.5 已恢复可用，下一条消息将自动切回");
  });
  it("keeps the existing one-sentence wording for a runtime-written record without the new fields", () => {
    const html = render({
      subtype: "model_refusal_fallback", original_model: "claude-opus-5-5", fallback_model: "claude-sonnet-5-5",
      category: "cat-z", scope: "session", text: words,
    });
    expect(html).toContain("上游拒绝了此请求，已自动切换至 Sonnet 5.5。");
    expect(html).not.toContain("已恢复");
  });
});
