// #2528: the fallback card's view keeps "the next start will use the configured model" apart from proof that it runs,
// "back to the configured model" posts nothing but the session's path, and the node's refusals read in the page's
// language by their code.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";

const calls: { path: string; init: RequestInit & { json?: unknown } }[] = [];
let failWith: ApiError | null = null;
vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  api: async (path: string, init: RequestInit & { json?: unknown } = {}) => {
    calls.push({ path, init });
    if (failWith) throw failWith;
    return { restore: "next_start", restore_to: "configured-model", closed: true }; // node/model_restore.go's answer
  },
}));

const { fallbackView, recoveryCause, recoveryExhausted, recoveryReturning, refusalFacts } = await import("./model-fallback");
const { restoreErrorText, restoreModel } = await import("./model-restore");
const { usageLimitClock } = await import("./usage-limit");

const fallback = {
  subtype: "model_refusal_fallback",
  original_model: "primary-model",
  fallback_model: "fallback-model",
  category: "synthetic-policy",
  scope: "session",
  explanation: "Upstream refused the request",
  text: "Synthetic refusal words",
};

describe("fallbackView", () => {
  it("none without an active fallback, or for a record that names no fallback model", () => {
    expect(fallbackView({})).toBeNull();
    expect(fallbackView({ model_fallback: { ...fallback, fallback_model: "" } })).toBeNull();
    expect(fallbackView({ model_fallback: { ...fallback, subtype: "model_refusal_blocked" } })).toBeNull();
  });
  it("before a restore names the refused and fallback models and the error since, no target", () => {
    const v = fallbackView({ model_fallback: fallback, turn_error: "API Error: 404 fallback-model not available" });
    expect(v).toEqual({ original: "primary-model", fallback: "fallback-model", error: "API Error: 404 fallback-model not available", pending: false, target: "" });
  });
  it("after the restore the target is the configured model the node read, not the refused one", () => {
    const v = fallbackView({ model_fallback: fallback, model_restore: { state: "next_start", to: "configured-model", at: "2026-11-09T00:00:00Z" } });
    expect(v?.pending).toBe(true);
    expect(v?.target).toBe("configured-model");
    expect(v?.original).toBe("primary-model");
  });
  it("an unknown restore state is not pending", () => {
    expect(fallbackView({ model_fallback: fallback, model_restore: { state: "later", to: "x", at: "" } })?.pending).toBe(false);
  });
});

describe("restoreModel", () => {
  beforeEach(() => {
    calls.length = 0;
    failWith = null;
  });
  it("POSTs the session's restore-model with no body", async () => {
    const res = await restoreModel("m 1", "s/1");
    expect(res).toEqual({ restore: "next_start", restore_to: "configured-model", closed: true });
    expect(calls).toHaveLength(1);
    expect(calls[0].path).toBe("/sessions/m%201/s%2F1/restore-model");
    expect(calls[0].init.method).toBe("POST");
    expect(calls[0].init.json).toBeUndefined();
    expect(calls[0].init.body).toBeUndefined();
  });
  it("words the node's refusals by code, and keeps other errors' own words", async () => {
    const cases: [string, string][] = [
      ["restore_busy", "正在运行中"],
      ["restore_unsupported", "/model"],
      ["restore_nothing", "已不再使用回退模型"],
      ["restore_unknown", "未执行关闭操作"],
      ["node_outdated", "版本过旧"],
    ];
    for (const [code, words] of cases) {
      failWith = new ApiError(409, "node words " + code, { code, error: "node words " + code });
      const err = await restoreModel("m", "s").catch((e: unknown) => e);
      expect(restoreErrorText(err)).toContain(words);
    }
    expect(restoreErrorText(new ApiError(409, "其他原因", { code: "restore_gone" }))).toBe("其他原因");
  });
});

describe("refusalFacts", () => {
  it("keeps every public value as recorded and the refusal's words untouched", () => {
    const text = "Upstream refused <this>\n  policy";
    expect(refusalFacts({ ...fallback, text })).toEqual({
      original: "primary-model",
      fallback: "fallback-model",
      category: "synthetic-policy",
      scope: "session",
      explanation: "Upstream refused the request",
      text,
    });
    expect(refusalFacts({ subtype: "model_refusal_future", text: "  " })).toEqual({
      original: undefined, fallback: undefined, category: undefined, scope: undefined, explanation: undefined, text: undefined,
    });
  });
  it("reads nothing of what an older hub sent beyond the public fields", () => {
    const MARKER = "HIDDEN-MARKER-2528-facts";
    const old = { ...fallback, raw: `{"syntheticHidden":{"nested":"${MARKER}"}}`, extra: { deeper: MARKER } } as unknown as typeof fallback;
    expect(JSON.stringify(refusalFacts(old))).not.toContain(MARKER);
    expect(refusalFacts(old).text).toBe("Synthetic refusal words");
  });
});

// #7824: the node's bounded recovery adds the exhausted-chain view, the plain-words cause and the returning line.
// A runtime-written record carries none of the new fields and must keep rendering exactly as before.
const recovery = {
  ...fallback,
  category: "quota_exhausted",
  recovery_reset: "2026-11-15T07:30:00Z",
  recovery_exhausted: false,
  recovery_returning: false,
  recovery_tried: "primary-model -> fallback-model",
};
const NOW = Date.parse("2026-11-15T06:00:00Z");

describe("recoveryCause", () => {
  it("maps the node's five recovery categories to plain words and leaves anything else alone", () => {
    expect(recoveryCause("safeguards_refusal")).toBe("模型拒绝了此请求");
    expect(recoveryCause("quota_exhausted")).toBe("额度已用尽");
    expect(recoveryCause("rate_limited")).toBe("触发限流");
    expect(recoveryCause("server_error")).toBe("服务异常");
    expect(recoveryCause("model_unavailable")).toBe("模型不可用");
    // runtime-written records and unknown categories keep their existing wording
    expect(recoveryCause("synthetic-policy")).toBeUndefined();
    expect(recoveryCause(undefined)).toBeUndefined();
  });
});

describe("recoveryExhausted", () => {
  it("is null without the exhausted marker", () => {
    expect(recoveryExhausted({ model_fallback: fallback })).toBeNull();
    expect(recoveryExhausted({})).toBeNull();
  });
  it("names the cause, the reset time in the user's clock and every model tried", () => {
    const v = recoveryExhausted({ model_fallback: { ...recovery, recovery_exhausted: true } }, NOW);
    expect(v).toEqual({
      cause: "额度已用尽",
      reset: usageLimitClock("2026-11-15T07:30:00Z", NOW),
      tried: "primary-model -> fallback-model",
    });
    expect(v!.reset.length).toBeGreaterThan(0);
  });
  it("keeps an unknown category and a missing reset as empty values, and still carries the tried list", () => {
    const v = recoveryExhausted(
      { model_fallback: { subtype: "model_refusal_fallback", recovery_exhausted: true, category: "something-new", recovery_tried: "a -> b" } },
      NOW,
    );
    expect(v).not.toBeNull();
    expect(v!).toEqual({ cause: "", reset: "", tried: "a -> b" });
  });
  it("treats an unparsable reset time as absent", () => {
    const v = recoveryExhausted(
      { model_fallback: { subtype: "model_refusal_fallback", recovery_exhausted: true, recovery_reset: "not-a-time" } },
      NOW,
    );
    expect(v?.reset).toBe("");
  });
});

describe("recoveryReturning", () => {
  it("names the configured model only once a probe was answered", () => {
    expect(recoveryReturning({ model_fallback: { ...recovery, recovery_returning: true } })).toBe("primary-model");
    expect(recoveryReturning({ model_fallback: recovery })).toBeUndefined();
    expect(recoveryReturning({})).toBeUndefined();
  });
});

describe("fallbackView with the node's recovery marker", () => {
  it("appends the plain-words cause and names the returning model", () => {
    const v = fallbackView({ model_fallback: { ...recovery, recovery_returning: true } });
    expect(v?.cause).toBe("额度已用尽");
    expect(v?.returning).toBe("primary-model");
  });
  it("leaves a runtime-written record's view without cause or returning", () => {
    const v = fallbackView({ model_fallback: fallback });
    expect(v?.cause).toBeUndefined();
    expect(v?.returning).toBeUndefined();
  });
});
