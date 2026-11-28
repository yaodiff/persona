// @vitest-environment jsdom
// "Can't reach the hub" only for a request that never got its answer: a TypeError from the page's own code (a list row
// with a field it did not expect) says what broke instead, and is not retried as a network gap.
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { errorText as adminErrorText } from "../admin/api";
import { initI18n, t } from "../i18n";
import { api, apiBlob, errorText, HubUnreachableError, isNetworkFailure, isTransient } from "./api";
import { writeErrorText } from "./doc-comments";
import { completionCheckError } from "./task-completion-check";

beforeAll(() => initI18n("en"));
afterEach(() => vi.unstubAllGlobals());

const programError = () => {
  try {
    // what a row without an expected field does to code that trusts it
    (undefined as unknown as { trim(): string }).trim();
  } catch (e) {
    return e;
  }
  throw new Error("unreachable");
};

describe("isNetworkFailure", () => {
  it.each(["Failed to fetch", "Load failed", "NetworkError when attempting to fetch resource.", "Network request failed", "network error", "fetch failed", "The network connection was lost."])(
    "counts the browser's fetch failure %j", (message) => {
      expect(isNetworkFailure(new TypeError(message))).toBe(true);
      expect(errorText(new TypeError(message))).toBe(t("lib-api.cant_reach_hub"));
    });

  it("counts api()'s own wrapped rejection whatever the browser called it", () => {
    expect(isNetworkFailure(new HubUnreachableError(new TypeError("net::ERR_CONNECTION_RESET")))).toBe(true);
  });

  it("does not count a program error, which shows its own message and is not retried", () => {
    const e = programError();
    expect(e).toBeInstanceOf(TypeError);
    expect(isNetworkFailure(e)).toBe(false);
    expect(isTransient(e)).toBe(false);
    const text = errorText(e);
    expect(text).not.toBe(t("lib-api.cant_reach_hub"));
    expect(text).toBe(t("lib-api.page_error", { detail: (e as Error).message }));
    expect(text).toContain("trim");
  });

  it("keeps the other callers' network wording for real gaps only", () => {
    const e = programError();
    expect(writeErrorText(new TypeError("Failed to fetch"))).toBe(t("lib-api.cant_reach_hub"));
    expect(writeErrorText(e)).not.toBe(t("lib-api.cant_reach_hub"));
    expect(adminErrorText(new TypeError("Failed to fetch"))).toBe(t("admin-api.hub_unreachable"));
    expect(adminErrorText(e)).not.toBe(t("admin-api.hub_unreachable"));
    expect(completionCheckError(new TypeError("Failed to fetch"))).toBe(t("todos.check_network"));
    expect(completionCheckError(e)).toBe(t("todos.check_failed"));
  });
});

describe("api() marks a request that never got its answer", () => {
  it("turns fetch's rejection into HubUnreachableError, read as Can't reach the hub", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("net::ERR_CONNECTION_REFUSED")));
    for (const read of [() => api("/sessions"), () => api("/sessions", { method: "POST", json: {} }), () => apiBlob("/files/x")]) {
      const error = await read().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(HubUnreachableError);
      expect(errorText(error)).toBe(t("lib-api.cant_reach_hub"));
    }
  });

  it("counts an answer whose body breaks off as never reached (the hub logged its 200)", async () => {
    const broken = () => new Response(new ReadableStream({ start(c) { c.error(new TypeError("network error")); } }), { status: 200 });
    vi.stubGlobal("fetch", vi.fn(async () => broken()));
    const error = await api("/sessions?agents_only=1").catch((e: unknown) => e);
    expect(error).toBeInstanceOf(HubUnreachableError);
    expect(errorText(error)).toBe(t("lib-api.cant_reach_hub"));
  });

  it("keeps an abort an abort", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new DOMException("aborted", "AbortError")));
    const error = await api("/sessions", { dedupe: false, timeout: 0 }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(DOMException);
  });
});
