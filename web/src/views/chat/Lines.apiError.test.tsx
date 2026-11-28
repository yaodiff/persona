// @vitest-environment jsdom
// A model error is cindy's error notice. The one the chat stopped on is the banner over the composer (ErrorBanner, #5313)
// with what can be done about it now: "Retry" when it can help, the text-derived "Fix it" in Persona's own
// conversations only, "Edit last message" for the safeguards error, and ✕. In the chat it is a record without them.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { parseAPIError } from "../../lib/api-errors";
import { turnErrorFix } from "../../lib/readiness";
import { recoveryExhausted } from "../../lib/model-fallback";
import { usageLimitClock } from "../../lib/usage-limit";
import type { ChatLine } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { APIErrorDetails, ChatLineView, ErrorBanner, errorKey } from "./Lines";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeAll(async () => { await APIErrorDetails.preload(); });

type ErrorLine = Extract<ChatLine, { kind: "apiError" }>;
function line(text: string): ErrorLine {
  const item = { id: "e1", seq: 1, role: "assistant", kind: "text", text } as Item;
  return { kind: "apiError", id: "err-e1", item, info: parseAPIError(text)!, count: 1 };
}
const banner = (text: string, assistant: boolean, extra: Partial<Parameters<typeof ErrorBanner>[0]> = {}) =>
  renderToStaticMarkup(<ErrorBanner line={line(text)} onRetry={() => {}} onDismiss={() => {}} assistant={assistant} {...extra} />);
const inline = (text: string, assistant = false) => renderToStaticMarkup(<ChatLineView line={line(text)} mid="m" sid="s" expandFold={async () => {}} assistant={assistant} />);

describe("a model error's fix", () => {
  it("offers 去修 in a Persona conversation", () => {
    expect(banner("Invalid API key · Fix external API key", true)).toContain("修复");
    // a record in the chat keeps the fix its own text names
    expect(inline("Invalid API key · Fix external API key", true)).toContain("修复");
  });

  it("names the affected launcher and opens its provider settings", () => {
    history.replaceState(null, "", "/");
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    const fix = turnErrorFix("auth_failed", "team-relay");
    act(() => root.render(<ErrorBanner line={line("Invalid API key · Fix external API key")} onDismiss={() => {}} assistant fix={fix} />));
    expect(host.textContent).toContain("team-relay");
    const repair = [...host.querySelectorAll("button")].find((b) => b.textContent === "修复")!;
    act(() => repair.click());
    expect(location.pathname + location.search).toBe("/settings?tab=models&launcher=team-relay");
    act(() => root.unmount());
    host.remove();
    history.replaceState(null, "", "/");
  });

  it("keeps the launcher on text-derived fallback fixes", () => {
    for (const kind of ["line", "banner"] as const) {
      history.replaceState(null, "", "/");
      const host = document.createElement("div");
      document.body.append(host);
      const root = createRoot(host);
      const props = { line: line("Invalid API key · Fix external API key"), assistant: true, launcher: "team-relay" };
      act(() => root.render(kind === "line"
        ? <ChatLineView {...props} mid="m" sid="s" expandFold={async () => {}} />
        : <ErrorBanner {...props} onDismiss={() => {}} />));
      expect(host.textContent).toContain("team-relay ·");
      const repair = [...host.querySelectorAll("button")].find((b) => b.textContent === "修复")!;
      act(() => repair.click());
      expect(location.pathname + location.search).toBe("/settings?tab=models&launcher=team-relay");
      act(() => root.unmount());
      host.remove();
    }
    history.replaceState(null, "", "/");
  });

  it("does not point an agent session at Persona's settings", () => {
    expect(banner("Invalid API key · Fix external API key", false)).not.toContain("修复");
  });

  it("keeps 重试 on an agent session's passing error", () => {
    const html = banner("API Error: 529 overloaded", false);
    expect(html).toContain("重试");
    expect(html).not.toContain("修复");
  });

  it("recognizes safeguards as requiring action and offers editing only on a supported agent chat", () => {
    const text = "API Error: Opus 5.5's safeguards flagged this message. Details: [frontier_llm]";
    const info = parseAPIError(text)!;
    expect(info.code).toBe("safeguards");
    expect(info.transient).toBe(false);
    const html = banner(text, false, { onEditLastMessage: () => {} });
    expect(html).toContain("编辑上一条消息");
    expect(html).toContain("模型的安全检查拦截了这条消息");
    expect(html).not.toContain(">重试</button>");
    expect(banner(text, false)).not.toContain("编辑上一条消息");
    expect(banner(text, true, { onEditLastMessage: () => {} })).not.toContain("编辑上一条消息");
  });
});

describe("a gateway that refused on-demand tools (tool_search_unsupported)", () => {
  // i18n-ignore: a gateway's words
  const edge = "API Error: 400 request is not representable on this edge: ToolSearch is not supported on this endpoint (defer_loading:true, tool_search_*, tool_reference); resend with all tools loaded";

  it("says so in a Persona conversation and offers Retry, not a provider to set up", () => {
    const html = banner(edge, true);
    expect(html).toContain("该网关不支持按需加载工具");
    expect(html).not.toContain("全部工具"); // an older computer's node keeps tool search on: no promise it can't keep
    expect(html).toContain(">重试</button>");
    expect(html).not.toContain("修复");
    // the record in the chat says the same, with no button of its own
    expect(inline(edge, true)).toContain("该网关不支持按需加载工具");
    expect(inline(edge, true)).not.toContain("修复");
  });

  it("is the hub's code too, and an Agent session's card keeps the rejected request without a Retry that would fail the same way", () => {
    expect(banner("API Error: 400 Bad Request", true, { fix: turnErrorFix("tool_search_unsupported") })).toContain(">重试</button>");
    const html = banner(edge, false);
    expect(html).toContain("请求被拒绝");
    expect(html).not.toContain("按需加载工具");
    expect(html).not.toContain(">重试</button>");
  });
});

describe("a model the chat can't run (model_route)", () => {
  const route = "There's an issue with the selected model (x). It may not exist or you may not have access to it.";

  it("offers Fix it, which opens the chat's picker, and the way back while the chat keeps a pick", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    const onPickModel = vi.fn(() => true);
    const run = vi.fn();
    const fix = { ...turnErrorFix("model_route")!, onPickModel, back: { label: "恢复跟随默认", run } };
    act(() => root.render(<ErrorBanner line={line(route)} assistant fix={fix} onRetry={() => {}} onDismiss={() => {}} />));
    const buttons = [...host.querySelectorAll("button")];
    expect(buttons.some((b) => b.textContent === "重试")).toBe(false);
    act(() => buttons.find((b) => b.textContent === "修复")!.click());
    expect(onPickModel).toHaveBeenCalledTimes(1);
    act(() => buttons.find((b) => b.textContent === "恢复跟随默认")!.click());
    expect(run).toHaveBeenCalledTimes(1);
    act(() => root.unmount());
    host.remove();
  });
});

describe("a model error is cindy's error notice (#5157)", () => {
  const gpu = "API Error: 502 upstream error: Req 202610302115A1B2C3D4E5F6 Session 1ebd0000:7e470000 no available GPU pod";

  it("is a neutral card: the red mark only, what happened, what helps, then Retry and Error details", () => {
    const html = banner(gpu, false);
    expect(html).toContain("data-error-notice");
    expect(html).toContain("bg-surface p-3 shadow-card");
    expect(html).not.toMatch(/bg-red-tint|var\(--red\)_22%/);
    expect(html).toContain('class="mt-0.5 inline-flex shrink-0 text-red"');
    expect(html).toContain("推理服务暂时没有可用的 GPU");
    expect(html).toContain("临时故障，通常很快会恢复。");
    expect(html).toContain("重试");
    // a 28 px action keeps a finger-sized target on a touch screen
    expect(html).toMatch(/<button[^>]*class="[^"]*touch-hit[^"]*"[^>]*>(?:(?!<\/button>).)*重试<\/button>/);
    expect(html).toContain('title="继续当前任务"');
    expect(html).toContain('aria-expanded="false"');
    expect(html).toContain("错误详情");
    // the head stays quiet: the status code, the ids and the original wait under Error details
    expect(html).not.toContain(">502<");
    expect(html).not.toContain("1ebd0000:7e470000");
  });

  it("holds the ids, the status code, how many times and the original under Error details", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    const l = { ...line(gpu), count: 3 };
    act(() => root.render(<ChatLineView line={l} mid="m" sid="s" expandFold={async () => {}} />));
    const toggle = [...host.querySelectorAll("button")].find((b) => b.textContent === "错误详情")!;
    act(() => toggle.click());
    const details = host.querySelector("[data-error-details]")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(toggle.textContent).toBe("收起详情");
    expect(details.textContent).toContain("202610302115A1B2C3D4E5F6");
    expect(details.textContent).toContain("1ebd0000:7e470000");
    expect(details.textContent).toContain("502");
    expect(details.textContent).toContain("连续 3 次");
    expect(details.querySelector("pre")!.textContent).toBe(gpu);
    expect(details.textContent).toContain("复制原文");
    act(() => root.unmount());
    host.remove();
  });

  it("draws a usage limit as the same notice, saying when it resets", () => {
    const html = renderToStaticMarkup(<ChatLineView line={line(gpu)} mid="m" sid="s" expandFold={async () => {}} limit="用量上限 · 15:00 重置" />);
    expect(html).toContain("data-error-notice");
    expect(html).toContain('role="status"');
    expect(html).toContain("用量上限 · 15:00 重置");
    expect(html).not.toContain("错误详情");
    expect(html).not.toContain("重试");
  });
});

describe("the error the chat stopped on, over the composer (#5313)", () => {
  const gpu = "API Error: 502 upstream error: no available GPU pod";

  it("is the same card with ✕ in its top corner: cindy's 28 px round button, labelled", () => {
    const html = banner(gpu, false);
    expect(html).toContain("data-error-banner");
    // out of the transcript, it is announced as a status of its own; the record in the chat is not
    expect(html).toMatch(/<div[^>]*data-error-banner=""[^>]*role="status"/);
    expect(inline(gpu)).not.toContain('role="status"');
    expect(html).toMatch(/<button[^>]*aria-label="关闭错误提示"[^>]*title="关闭错误提示"[^>]*class="touch-hit -mt-1 -mr-1 flex size-7 shrink-0 items-center justify-center rounded-full text-ink-2/);
  });

  it("✕ is the caller's to decide; Retry calls the retry", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    const onRetry = vi.fn();
    const onDismiss = vi.fn();
    act(() => root.render(<ErrorBanner line={line(gpu)} onRetry={onRetry} onDismiss={onDismiss} />));
    act(() => host.querySelector<HTMLButtonElement>('[aria-label="关闭错误提示"]')!.click());
    expect(onDismiss).toHaveBeenCalledTimes(1);
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === "重试")!.click());
    expect(onRetry).toHaveBeenCalledTimes(1);
    act(() => root.unmount());
    host.remove();
  });

  it("in the chat it is a record: no Retry, no Edit last message, no ✕", () => {
    const html = inline(gpu);
    expect(html).toContain("data-error-notice");
    expect(html).not.toContain("重试");
    expect(html).not.toContain("关闭错误提示");
    expect(html).not.toContain("data-error-banner");
    expect(inline("API Error: Opus 5.5's safeguards flagged this message. Details: [frontier_llm]")).not.toContain("编辑上一条消息");
  });

  it("says a usage limit as the banner too, with ✕ and no Retry", () => {
    const html = banner(gpu, false, { limit: "用量上限 · 15:00 重置" });
    expect(html).toContain("用量上限 · 15:00 重置");
    expect(html).toContain('role="status"');
    expect(html).toContain("关闭错误提示");
    expect(html).not.toContain("重试");
  });

  it("is a new error once the same one comes again after a retry (the line folds with a higher count)", () => {
    expect(errorKey(line(gpu))).not.toBe(errorKey({ ...line(gpu), count: 2 }));
  });
});

// #7824: when every model of the recovery chain failed, the failure card offers Retry even for an error the parser
// calls non-transient (a safeguards refusal), names the cause and the reset time, and keeps the tried models folded.
describe("an exhausted recovery chain's failure card", () => {
  const safeguards = "API Error: Opus 5.5's safeguards flagged this message. Details: [frontier_llm]";
  const exhausted = recoveryExhausted(
    {
      model_fallback: {
        subtype: "model_refusal_fallback",
        category: "safeguards_refusal",
        recovery_exhausted: true,
        recovery_reset: "2026-11-15T07:30:00Z",
        recovery_tried: "model-a -> model-b",
      },
    },
    Date.parse("2026-11-15T06:00:00Z"),
  )!;

  const resetText = usageLimitClock("2026-11-15T07:30:00Z", Date.parse("2026-11-15T06:00:00Z"));

  it("offers Retry on a safeguards error the parser treats as non-transient, with the cause and reset time", () => {
    const html = banner(safeguards, false, { exhausted });
    expect(html).toContain(">重试</button>");
    expect(html).toContain("模型拒绝了此请求");
    expect(html).toContain(resetText);
    expect(html).toMatch(/data-testid="recovery-exhausted"/);
  });

  it("keeps the models tried inside the folded error details, off the card's face", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    act(() => root.render(<ErrorBanner line={line(safeguards)} onRetry={() => {}} onDismiss={() => {}} exhausted={exhausted} />));
    // before the fold opens, the card's face names the cause but not the tried models
    expect(host.textContent).toContain("模型拒绝了此请求");
    expect(host.textContent).not.toContain("已尝试的模型");
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === "错误详情")!.click());
    const details = host.querySelector("[data-error-details]")!;
    expect(details.textContent).toContain("已尝试的模型：model-a -> model-b");
    act(() => root.unmount());
    host.remove();
  });

  it("still hides Retry without the exhausted marker, so ordinary safeguards errors keep their old behavior", () => {
    const html = banner(safeguards, false);
    expect(html).not.toContain(">重试</button>");
    expect(html).not.toContain("模型拒绝了此请求");
  });

  it("shows the cause alone when the upstream gave no reset time", () => {
    const noReset = recoveryExhausted({
      model_fallback: { subtype: "model_refusal_fallback", category: "quota_exhausted", recovery_exhausted: true },
    })!;
    const html = banner("API Error: 429 rate_limited", false, { exhausted: noReset });
    expect(html).toContain("额度已用尽");
    expect(html).not.toContain("恢复可用");
    expect(html).toContain(">重试</button>");
  });

  it("reads the same on the record left in the chat, which keeps no Retry button of its own", () => {
    const html = renderToStaticMarkup(
      <ChatLineView line={line(safeguards)} mid="m" sid="s" expandFold={async () => {}} exhausted={exhausted} />,
    );
    expect(html).toContain("模型拒绝了此请求");
    expect(html).toContain(resetText);
    expect(html).not.toContain(">重试</button>");
  });
});

describe("image-limit turn failures (#13045)", () => {
  const samples = [
    "Request too large (max 32MB). Accumulated images and attachments in the conversation pushed the request over the limit.",
    "API Error: an image in the conversation could not be processed and was removed. Please try again.",
    "API Error: 400 At least one of the image dimensions exceed max allowed size for many-image requests: 2000 pixels",
  ];

  it("shows one plain localized sentence in both Persona and Agent chats, keeps raw text under Details, and suppresses Retry", () => {
    for (const raw of samples) {
      for (const assistant of [true, false]) {
        const host = document.createElement("div");
        document.body.append(host);
        const root = createRoot(host);
        const onCompactAndResend = vi.fn();
        act(() => root.render(
          <ErrorBanner
            line={line(raw)}
            assistant={assistant}
            onRetry={() => {}}
            onCompactAndResend={onCompactAndResend}
            onDismiss={() => {}}
          />,
        ));
        expect(host.textContent).toContain("该对话中的图片数量过多或体积过大，超出了模型限制。");
        expect(host.textContent).not.toContain("需要你手动处理，重试无法解决。");
        expect(host.textContent).not.toContain(raw);
        const buttons = [...host.querySelectorAll("button")];
        expect(buttons.some((b) => b.textContent === "重试")).toBe(false);
        expect(buttons.some((b) => b.textContent === "修复")).toBe(false);
        const compactBtn = host.querySelector<HTMLButtonElement>('[data-testid="compact-and-resend"]');
        expect(compactBtn?.textContent).toBe("压缩并重发");
        act(() => compactBtn!.click());
        expect(onCompactAndResend).toHaveBeenCalledTimes(1);
        // Raw error text is kept under Details.
        act(() => buttons.find((b) => b.textContent === "错误详情")!.click());
        expect(host.querySelector("[data-error-details] pre")!.textContent).toBe(raw);
        act(() => root.unmount());
        host.remove();
      }
    }
  });

  it("omits Compact and send again when compaction is unsupported and on inline history records", () => {
    const html = banner(samples[0], false);
    expect(html).toContain("该对话中的图片数量过多或体积过大，超出了模型限制。");
    expect(html).not.toContain("压缩并重发");
    expect(html).not.toContain(">重试</button>");
    const record = inline(samples[0], true);
    expect(record).toContain("该对话中的图片数量过多或体积过大，超出了模型限制。");
    expect(record).not.toContain("压缩并重发");
  });

  it("recognizes hub turnErrorCode=images_too_large even on generic API Error wording", () => {
    const html = banner("API Error: 400 Bad Request", false, { turnErrorCode: "images_too_large", onCompactAndResend: () => {} });
    expect(html).toContain("该对话中的图片数量过多或体积过大，超出了模型限制。");
    expect(html).toContain("压缩并重发");
    expect(html).not.toContain(">重试</button>");
  });
});
