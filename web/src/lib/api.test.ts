import { afterEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import { api, apiBlob, ApiError, CLIENT_FEATURES, errorText, isTransient } from "./api";
import { isOfflineError } from "./feedback";
import { sessionLoadError } from "./session-link";

afterEach(() => vi.unstubAllGlobals());

describe("readable hub errors", () => {
  it.each([
    [503, "kb_disabled", "lib-api.knowledge_disabled"],
    [503, "kb_unavailable", "lib-api.knowledge_unavailable"],
    [403, "kb_read_only", "lib-api.knowledge_read_only"],
    [503, "assistant_offline", "lib-api.manager_offline"],
  ] as const)("keeps %s %s distinct from a hub outage", async (status, code, key) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      code, error: "Request failed", detail: "private diagnostic",
    }), { status, headers: { "X-Request-Id": "request-1" } })));
    const error = await api("/kb/projects", { token: "test-token" }).catch((e: unknown) => e);
    expect(errorText(error)).toBe(t(key));
    expect(isOfflineError(errorText(error))).toBe(false);
    expect(errorText(error)).not.toContain("private diagnostic");
    expect((error as ApiError).requestId).toBe("request-1");
    expect((error as ApiError).body.detail).toBe("private diagnostic");
  });

  it.each(["/kb/projects", "/kb/chats?tag=example", "/kb-search?q=example", "/recordings/example"])(
    "recognizes the exact older disabled response only on knowledge routes: %s", async (path) => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        code: "internal", detail: "the knowledge base is off", error: "Request failed",
      }), { status: 503 })));
      expect(errorText(await api(path, { token: "test-token" }).catch((e: unknown) => e))).toBe(t("lib-api.knowledge_disabled"));
    },
  );

  it.each([
    ["/models", 503, "internal", "the knowledge base is off"],
    ["/kb-other/projects", 503, "internal", "the knowledge base is off"],
    ["/kb/projects", 502, "internal", "the knowledge base is off"],
    ["/kb/projects", 503, "internal", "wrapped: the knowledge base is off"],
    ["/kb/projects", 503, "unknown", "the knowledge base is off"],
    ["/kb/projects", 503, undefined, "the knowledge base is off"],
  ])("does not infer disabled from an ambiguous legacy response: %s %s %s %s", async (path, status, code, detail) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ code, detail, error: "Request failed" }), { status })));
    const error = await api(path as string, { token: "test-token" }).catch((e: unknown) => e);
    expect(errorText(error)).not.toBe(t("lib-api.knowledge_disabled"));
    expect(errorText(error)).not.toContain(detail);
  });

  it.each([
    new ApiError(404, "not found", {}),
    new ApiError(404, "entry missing", {}),
    new ApiError(400, "entry missing", { code: "not_found" }),
    new ApiError(400, "not_found", {}),
    new ApiError(400, "Not Found\n", {}),
  ])("explains missing content or an unsupported endpoint: %s", (error) => {
    expect(errorText(error)).toBe(t("lib-api.not_found", { detail: `HTTP ${error.status}: ${error.message}` }));
    expect(errorText(error)).toContain(error.message);
  });

  it("declares client features including tool_views on REST requests unless overridden", async () => {
    expect(CLIENT_FEATURES).toContain("tool_views");
    const fetch = vi.fn().mockImplementation(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetch);
    await api("/sessions/m/s/items", { token: "test-token" });
    expect(new Headers(fetch.mock.calls[0][1].headers).get("X-Persona-Features")).toContain("tool_views");
    await api("/sessions/m/s/messages", { token: "test-token", headers: { "X-Persona-Features": "agent_message_hold" } });
    expect(new Headers(fetch.mock.calls[1][1].headers).get("X-Persona-Features")).toBe("agent_message_hold");
  });

  it("adds an explicit query without changing the route used for routing and reports", async () => {
    const fetch = vi.fn().mockImplementation(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetch);
    await api("/todos", { token: "test-token", query: { closed_limit: "50" } });
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/todos?closed_limit=50");
    expect(fetch.mock.calls[0][1].query).toBeUndefined();
  });

  it.each([500, 501, 505, 599])("explains HTTP %s without discarding the server's reason", (status) => {
    expect(errorText(new ApiError(status, "storage failed", {}))).toBe(t("lib-api.server_error", { detail: `HTTP ${status}: storage failed` }));
  });

  it.each([502, 503, 504])("explains HTTP %s without claiming the hub is unreachable", (status) => {
    const text = errorText(new ApiError(status, "upstream unavailable", {}));
    expect(text).toBe(t("lib-api.hub_unavailable", { detail: `HTTP ${status}: upstream unavailable` }));
    expect(isOfflineError(text)).toBe(false);
  });

  // The hub's own sentence, not "the service couldn't complete this request (HTTP 502)" (#17431): that line is for what
  // stands in front of the hub.
  it.each([502, 503])("shows the hub's own reason for a coded HTTP %s, without its diagnostic detail", async (status) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      code: "accept_not_taken", error: "The answer did not go: the computer is offline", detail: "private diagnostic", delivered: "none",
    }), { status })));
    const error = await api("/todos/todo_v1/accept", { method: "POST", token: "test-token" }).catch((e: unknown) => e);
    expect(errorText(error)).toBe("The answer did not go: the computer is offline");
    expect(errorText(error)).not.toContain("private diagnostic");
    expect(isTransient(error)).toBe(false);
  });

  it.each([["", "HTTP 502"], ["Bad Gateway", "HTTP 502: Bad Gateway"]])("keeps the generic line for an empty or non-JSON gateway answer: %j", async (body, detail) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(body, { status: 502 })));
    const error = await api("/todos/todo_v1/accept", { method: "POST", token: "test-token" }).catch((e: unknown) => e);
    expect(errorText(error)).toBe(t("lib-api.hub_unavailable", { detail }));
    expect(isTransient(error)).toBe(true);
  });

  it.each(["", "HTTP 500"])("keeps a bare status only once: %s", (message) => {
    expect(errorText(new ApiError(500, message, {}, false))).toBe(t("lib-api.server_error", { detail: "HTTP 500" }));
  });

  it("keeps authentication and other client errors intact", () => {
    for (const [status, message] of [[401, "invalid token"], [403, "forbidden"], [409, "already exists"], [429, "slow down"], [400, "file not found in upload"]] as const) {
      expect(errorText(new ApiError(status, message, {}))).toBe(message);
    }
  });

  it("preserves non-API errors and the existing network explanation", () => {
    expect(errorText(new TypeError("Failed to fetch"))).toBe(t("lib-api.cant_reach_hub"));
    expect(errorText(new Error("invalid token"))).toBe("invalid token");
    expect(errorText("not found")).toBe("not found");
    expect(errorText(null)).toBe("null");
  });

  it("leaves the original error available to callers and session-specific explanations", () => {
    const message = "transcript for session deadbeef not found";
    const error = new ApiError(404, message, { code: "not_found" }, true, "request-1");
    expect(sessionLoadError(errorText(error))).toBe("找不到该会话，可能已被删除或链接中的 ID 不完整。");
    expect(error.message).toBe(message);
    expect(error.code).toBe("not_found");
    expect(error.requestId).toBe("request-1");
  });

  it.each([
    [404, '{"error":"not found"}', "HTTP 404: not found"],
    [500, "", "HTTP 500"],
    [502, "Bad Gateway", "HTTP 502: Bad Gateway"],
  ] as const)("formats a failed HTTP %s response through the REST client", async (status, body, detail) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(body, { status })));
    const error = await api("/missing", { token: "test-token" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(errorText(error)).toContain(detail);
    expect(errorText(error)).not.toBe(detail);
  });
});

describe("an error page from a proxy in front of the hub", () => {
  it.each(["<!DOCTYPE html><html><title>502: Bad gateway</title></html>", "\n  <html><body>Bad Gateway</body></html>"])("shows the status, not the markup: %s", async (body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(body, { status: 502 })));
    const text = errorText(await api("/missing", { token: "test-token" }).catch((e: unknown) => e));
    expect(text).toBe(t("lib-api.hub_unavailable", { detail: "HTTP 502" }));
  });
});

// A meeting photo's image (#3862 phase 3): an <img> can't send the bearer token or the manager, so the page reads the
// bytes itself and shows them from an object URL.
describe("a hub route's bytes", () => {
  it("sends the credentials and headers api does and gives the body back as it came", async () => {
    const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47]);
    const fetch = vi.fn().mockResolvedValue(new Response(png, { status: 200, headers: { "Content-Type": "image/png" } }));
    vi.stubGlobal("fetch", fetch);
    const blob = await apiBlob("/recordings/r1/photos/1", { token: "test-token", headers: { "X-Persona-Manager": "m-1" } });
    expect(blob.type).toBe("image/png");
    expect(new Uint8Array(await blob.arrayBuffer())).toEqual(png);
    const headers = new Headers(fetch.mock.calls[0][1].headers);
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/recordings/r1/photos/1");
    expect(headers.get("Authorization")).toBe("Bearer test-token");
    expect(headers.get("X-Persona-Manager")).toBe("m-1");
  });

  it("fails with the hub's error as api does", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "no such photo", code: "photo_not_found" }), { status: 404 })));
    const err = await apiBlob("/recordings/r1/photos/9", { token: "test-token" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("photo_not_found");
  });

  // The visualization route's own answer is an HTML fragment (#10321): text/html on /api is legitimate only for
  // callers that explicitly say so, and the bytes come back untouched instead of an edge interception error.
  it("accepts a text/html answer only when the caller opts into it", async () => {
    const html = "<!doctype html><div>chart</div>";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(html, { status: 200, headers: { "Content-Type": "text/html; charset=utf-8" } })));
    const blob = await apiBlob("/sessions/m/s/items/i1/visualization", { token: "test-token", allowHtmlResponse: true });
    expect(blob.type).toBe("text/html;charset=utf-8");
    expect(await blob.text()).toBe(html);
  });
});

// What the hub answers when the computer did not (server/internal/apierr Body and Call): the summary may already be in the
// page's language, the raw text in detail; the code is what the page goes by (#5031).
describe("a computer that did not answer", () => {
  const offline = new ApiError(503, "这台电脑未连接，请连接后重试。", { code: "node_unreachable", retryable: true, delivered: "none", detail: "MacBook: machine is offline" });
  const dropped = new ApiError(502, "rpc: connection closed", { code: "node_unreachable" });
  const timedOut = new ApiError(502, "send: context deadline exceeded", { code: "timeout" });

  it.each([
    ["offline", offline, "lib-api.computer_offline"],
    ["cut off mid-call", dropped, "lib-api.computer_dropped"],
    ["too slow", timedOut, "lib-api.computer_timeout"],
  ] as const)("says what became of a computer that is %s and what to do next", (_, error, key) => {
    const text = errorText(error);
    expect(text).toBe(t(key));
    expect(text).not.toContain(error.message);
    expect(text).not.toContain("HTTP 50");
    // the hub itself answered: not folded into "can't reach the hub" while the page's own connection is down
    expect(isOfflineError(text)).toBe(false);
  });

  it("calls it offline only when nothing went out", () => {
    for (const delivered of [undefined, "unknown", "yes"]) {
      expect(errorText(new ApiError(503, "machine is offline", { code: "node_unreachable", delivered }))).toBe(t("lib-api.computer_dropped"));
    }
  });

  it("leaves other gateway failures as they were", () => {
    expect(errorText(new ApiError(503, "upstream unavailable", {}))).toBe(t("lib-api.hub_unavailable", { detail: "HTTP 503: upstream unavailable" }));
    const internal = new ApiError(503, "private internal exception", { code: "internal", detail: "private diagnostic" });
    expect(errorText(internal)).toBe(t("lib-api.hub_unavailable", { detail: "HTTP 503" }));
    expect(internal.message).toBe("private internal exception");
  });
});

// The hub answered that nothing it can call directly is set up for the request (an Inbox draft): where to set one up,
// not a bare "HTTP 503" that reads as an outage.
describe("no model set up", () => {
  it("says where to set one up", () => {
    const noModel = new ApiError(503, "No model can be called directly to draft a reply", { code: "model_unavailable" });
    const text = errorText(noModel);
    expect(text).toBe(t("lib-api.model_unavailable"));
    expect(text).not.toContain("HTTP 50");
    expect(isOfflineError(text)).toBe(false);
  });
});

// A decision's accept the Agent's terminal refused, a dialog being open there (#7343): the hub answers 409 dialog_open,
// and the toast says to close the dialog in the terminal, not "try again later (HTTP 502)".
describe("a dialog open in the terminal", () => {
  it("says to close it there first", () => {
    const refused = new ApiError(409, "终端正在等待选择，请先到终端处理。", { code: "dialog_open", delivered: "none", retryable: false }); // i18n-ignore
    const text = errorText(refused);
    expect(text).toBe(t("lib-api.dialog_open"));
    expect(text).toContain("请先在终端中关闭该对话框"); // i18n-ignore
    expect(text).not.toContain("HTTP");
    expect(isTransient(refused)).toBe(false);
  });
});

describe("error language", { timeout: 30_000 }, () => {
  afterEach(() => initI18n("zh-Hans"), 30_000);

  it.each([
    ["zh-Hans", "未找到相关内容", "服务出现异常", "服务未能完成此次请求"],
    ["en", "Content not found", "The service ran into a problem", "The service couldn't complete this request"],
    ["ja", "内容が見つからない", "サービスで問題が発生しました", "サービスがこの要求を完了できませんでした"],
  ] as const)("uses the current %s catalog and retains diagnostics", async (lang, missing, server, gateway) => {
    await initI18n(lang);
    for (const [status, explanation] of [[404, missing], [500, server], [503, gateway]] as const) {
      const text = errorText(new ApiError(status, "original error", {}));
      expect(text).toContain(explanation);
      expect(text).toContain(`HTTP ${status}: original error`);
    }
  });

  it.each([
    ["zh-Hans", "当前离线", "连接已断开", "请求超时"],
    ["en", "is offline", "connection to this computer dropped", "timed out"],
    ["ja", "オフライン", "接続が切れました", "タイムアウト"],
  ] as const)("says what became of the computer in %s", async (lang, offline, dropped, timedOut) => {
    await initI18n(lang);
    expect(errorText(new ApiError(503, "MacBook: machine is offline", { code: "node_unreachable", delivered: "none" }))).toContain(offline);
    expect(errorText(new ApiError(502, "rpc: connection closed", { code: "node_unreachable" }))).toContain(dropped);
    expect(errorText(new ApiError(502, "send: context deadline exceeded", { code: "timeout" }))).toContain(timedOut);
  });
});
