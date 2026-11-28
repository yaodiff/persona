// @vitest-environment jsdom
// What the main conversation still needs (docs/assistant/onboarding.md 4.4, 5.1, 5.2): which step the card shows, what the send
// button says, a refused start's card, a turn's model error that the user must fix.
import { afterEach, describe, expect, it, vi } from "vitest";
import { getState } from "./store";
import { ApiError } from "./api";
import { parseAPIError } from "./api-errors";
import { providerName } from "./model-config";
import { initI18n } from "../i18n";
import {
  actionLabel, blockingStep, claudeMissing, readinessChanged, refreshReadiness, resetReadiness, retryWhileBlocked, claudeWorks, codedFailure, helpTopic, knownAction, passesBySelf, providersPath, runAction, sendLabel, turnErrorFix, type Readiness,
  type ReadinessStep,
} from "./readiness";

const step = (id: string, state: string, o: Partial<ReadinessStep> = {}): ReadinessStep => ({ id, state, ...o });
const ready = (mode: string, steps: ReadinessStep[], next: string | null = null): Readiness => ({
  version: 1, mode, ready: mode === "full", next, machine: null, launcher: "claude", steps, dismissed: false, checked_at: "2026-11-06T09:00:00Z",
});

describe("blockingStep", () => {
  it("is the first missing step in setup", () => {
    const r = ready("setup", [step("computer", "todo", { code: "no_machine", action: { id: "add_computer" } }), step("claude", "unknown"), step("model", "unknown"), step("first_message", "unknown")], "computer");
    expect(blockingStep(r)?.code).toBe("no_machine");
  });

  it("skips ok, skipped, unknown and checking steps", () => {
    const r = ready("setup", [step("computer", "ok"), step("claude", "skipped"), step("model", "problem", { code: "not_logged_in" }), step("first_message", "todo")], "model");
    expect(blockingStep(r)?.id).toBe("model");
  });

  it("never shows in full, and a rate limit never blocks", () => {
    expect(blockingStep(ready("full", [step("computer", "ok"), step("claude", "ok"), step("model", "unknown", { code: "model_unverified" }), step("first_message", "todo")], "model"))).toBeNull();
    expect(blockingStep(ready("setup", [step("computer", "ok"), step("claude", "ok"), step("model", "problem", { code: "rate_limited" })], "model"))).toBeNull();
  });

  it("is never 试一句, and nothing on a hub without readiness", () => {
    expect(blockingStep(ready("setup", [step("computer", "ok"), step("claude", "ok"), step("model", "ok"), step("first_message", "problem", { code: "auth_failed" })], "first_message"))).toBeNull();
    expect(blockingStep(null)).toBeNull();
  });

  it("takes `next` for a step this page does not know", () => {
    const r = ready("setup", [step("computer", "ok"), step("claude", "ok"), step("model", "ok"), step("vpn", "brand_new", { title: "接上 VPN" })], "vpn");
    expect(blockingStep(r)?.title).toBe("接上 VPN");
  });
});

describe("sendLabel", () => {
  it("says what to do first", () => {
    expect(sendLabel(step("computer", "todo", { code: "no_machine" }))).toBe("先添加电脑");
    expect(sendLabel(step("computer", "problem", { code: "node_unreachable" }))).toBe("先唤醒电脑");
    expect(sendLabel(step("claude", "problem", { code: "claude_missing" }))).toBe("先连接模型");
    expect(sendLabel(step("model", "problem", { code: "auth_failed" }))).toBe("先连接模型");
  });
});

describe("codedFailure", () => {
  it("keeps the hub's words, the action and the original for 详情", () => {
    const e = new ApiError(409, "Persona 要在你的一台电脑上干活，现在还没接上电脑。", { code: "no_machine", error: "…", detail: "assistant machine is not configured", action: "add_computer", request_id: "r1" });
    expect(codedFailure(e)).toEqual({ code: "no_machine", error: e.message, detail: "assistant machine is not configured", action: "add_computer", machine: undefined, launcher: undefined, requestId: "r1" });
  });

  it("gives an offline computer its button and leaves other errors without a code", () => {
    expect(codedFailure(new ApiError(502, "小林的 MacBook 现在不在线", { code: "node_unreachable" })).action).toBe("wake_computer");
    expect(codedFailure(new ApiError(500, "boom", { code: "internal" }))).toMatchObject({ code: "", error: "boom", action: undefined });
    expect(codedFailure(new TypeError("Failed to fetch")).code).toBe("");
  });

  it("keeps a private profile's sign-in refusal actionable", () => {
    const e = new ApiError(409, "Sign in to the dedicated profile", {
      code: "not_logged_in", action: "connect_model", detail: "CLAUDE_CONFIG_DIR=/example/profile claude", delivered: "none",
    });
    expect(codedFailure(e)).toMatchObject({ action: "connect_model", detail: "CLAUDE_CONFIG_DIR=/example/profile claude" });
  });

  it("knows the restart that passes by itself", () => {
    expect(passesBySelf(new ApiError(503, "x", { code: "assistant_offline" }))).toBe(true);
    expect(passesBySelf(new ApiError(409, "x", { code: "no_machine" }))).toBe(false);
  });
});

describe("actions and help", () => {
  it("draws buttons only for known actions", () => {
    expect(knownAction("connect_model")).toBe(true);
    expect(knownAction("launch_rocket")).toBe(false);
    expect(knownAction(undefined)).toBe(false);
  });

  it("opens the provider wizard or one provider", () => {
    expect(providersPath()).toBe("/settings?tab=models&add=1");
    expect(providersPath("my key")).toBe("/settings?tab=models&launcher=my%20key");
  });

  it("offers another app or setup and opens model settings in every language", async () => {
    const labels = {
      en: "Choose another app or setup",
      "zh-Hans": "选择其他应用或配置",
      ja: "別のアプリや設定を選ぶ",
    };
    const onHelp = vi.fn();
    try {
      for (const [lang, label] of Object.entries(labels)) {
        await initI18n(lang);
        history.replaceState(null, "", "/");
        expect(knownAction("main_chat_model")).toBe(true);
        expect(actionLabel("main_chat_model")).toBe(label);
        runAction("main_chat_model", { onHelp });
        expect(location.pathname + location.search).toBe("/settings?tab=models");
        expect(onHelp).not.toHaveBeenCalled();
      }
    } finally {
      history.replaceState(null, "", "/");
      await initI18n("zh-Hans");
    }
  });

  it("opens the computer flows through the page: add as help (the page picks the flow), confirm in place", () => {
    const onHelp = vi.fn();
    const onConfirm = vi.fn();
    runAction("add_computer", { onHelp, onConfirm });
    expect(onHelp).toHaveBeenCalledWith("add_computer");
    runAction("confirm_computer", { onHelp, onConfirm });
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("opens help for a local question, never sending it", () => {
    expect(helpTopic("how_to_connect_model")).toBe("how_to_connect_model");
    expect(helpTopic("something_new")).toBe("what_can_you_do");
  });
});

describe("turn errors", () => {
  it("says what to fix for the user's codes only", () => {
    expect(turnErrorFix("not_logged_in")?.action).toBe("connect_model");
    expect(turnErrorFix("auth_failed")?.action).toBe("edit_provider");
    expect(turnErrorFix("model_route")?.action).toBe("pick_model");
    expect(turnErrorFix("tool_search_unsupported")?.action).toBe("retry");
    expect(turnErrorFix("rate_limited")).toBeNull();
    expect(turnErrorFix(undefined)).toBeNull();
  });

  it("names the affected launcher and keeps it with the repair action", async () => {
    await initI18n("en");
    try {
      expect(turnErrorFix("not_logged_in", "team-relay")?.title).toBe("team-relay · Model not connected: not signed in");
      expect(turnErrorFix("not_logged_in", "claude")?.title).toBe("claude · Model not connected: Claude Code CLI isn't signed in");
      expect(turnErrorFix("auth_failed", "team-relay")?.title).toBe("team-relay · Model not connected: the key is wrong");
      expect(turnErrorFix("model_route", "team-relay")?.title).toBe("team-relay · Model not connected: this model isn't available");
      expect(turnErrorFix("auth_failed", "team-relay")?.launcher).toBe("team-relay");
      // An older caller with no launcher keeps the generic words rather than showing a blank name.
      expect(turnErrorFix("auth_failed")?.title).toBe("Model not connected: the key is wrong");
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("opens the named launcher for a scoped connection repair", () => {
    history.replaceState(null, "", "/s/m/c");
    try {
      const fix = turnErrorFix("not_logged_in", "team-relay")!;
      runAction(fix.action, { launcher: fix.launcher, onHelp: () => {} });
      expect(location.pathname + location.search).toBe(providersPath("team-relay"));
      expect(turnErrorFix("model_route", "team-relay")?.action).toBe("pick_model");
    } finally {
      history.replaceState(null, "", "/");
    }
  });

  it("normalizes a direct connection repair's launcher", () => {
    history.replaceState(null, "", "/s/m/c");
    try {
      runAction("connect_model", { launcher: "  team-relay  ", onHelp: () => {} });
      expect(location.pathname + location.search).toBe(providersPath("team-relay"));
      runAction("connect_model", { launcher: "   ", onHelp: () => {} });
      expect(location.pathname + location.search).toBe(providersPath());
    } finally {
      history.replaceState(null, "", "/");
    }
  });

  it("says a signed-out CLI is the one to sign in, not the desktop app, in every language (#9366)", async () => {
    const words = {
      en: ["Claude Code CLI", "Claude desktop app's sign-in doesn't count", "claude auth login", "API key"],
      "zh-Hans": ["Claude Code 命令行", "Claude 桌面版的登录状态不与此通用", "claude auth login", "API Key"],
      ja: ["Claude Code CLI", "Claude デスクトップアプリのログインはこれに含まれません", "claude auth login", "API キー"],
    };
    try {
      for (const [lang, [cli, desktop, command, key]] of Object.entries(words)) {
        await initI18n(lang);
        // The built-in Claude launcher keeps these words; without launcher metadata the card is generic (#10918).
        const fix = turnErrorFix("not_logged_in", "claude")!;
        expect(fix.title).toContain(cli);
        expect(fix.detail).toContain(desktop);
        expect(fix.detail).toContain(command);
        expect(fix.detail).toContain(key);
        const unknown = turnErrorFix("not_logged_in")!;
        expect(unknown.title).not.toContain(cli);
        expect(unknown.detail).not.toContain(desktop);
      }
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("names another launcher and asks it to sign in, and says something generic with no launcher (#10918)", async () => {
    await initI18n("en");
    try {
      const other = turnErrorFix("not_logged_in", "team-relay")!;
      expect(other.title).toBe("team-relay · Model not connected: not signed in");
      expect(other.detail).toContain("team-relay");
      expect(other.detail).not.toContain("Claude");
      expect(other.action).toBe("connect_model");
      expect(other.launcher).toBe("team-relay");

      const unknown = turnErrorFix("not_logged_in")!;
      expect(unknown.title).toBe("Model not connected: not signed in");
      expect(unknown.detail).toBe("Sign in to the model service on that computer, or use an API key instead.");
      expect(unknown.action).toBe("connect_model");
      expect(unknown.launcher).toBeUndefined();
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("sends a model the chat can't run to the chat's own picker, never to the new-provider wizard (#10365)", () => {
    const fix = turnErrorFix("model_route")!;
    try {
      history.replaceState(null, "", "/s/m/c");
      const onPickModel = vi.fn(() => true);
      runAction(fix.action, { onHelp: () => {}, onPickModel });
      expect(onPickModel).toHaveBeenCalledTimes(1);
      expect(location.pathname + location.search).toBe("/s/m/c");
      // no picker on this page: model settings, still not the wizard
      runAction(fix.action, { onHelp: () => {}, onPickModel: () => false });
      expect(location.pathname + location.search).toBe("/settings?tab=models");
      history.replaceState(null, "", "/s/m/c");
      runAction(fix.action, { onHelp: () => {} });
      expect(location.pathname + location.search).toBe("/settings?tab=models");
      expect(location.search).not.toContain("add=1");
      // signing in is still a new connection: the wizard
      runAction(turnErrorFix("not_logged_in")!.action, { onHelp: () => {} });
      expect(location.pathname + location.search).toBe(providersPath());
    } finally {
      history.replaceState(null, "", "/");
    }
  });

  it("says a gateway that refused on-demand tools needs only a retry, in every language", async () => {
    // The card can't tell whether the conversation's computer runs a node that now sends every tool there (an older one
    // keeps tool search on), so its words promise only a retry; the hub's readiness step says more when it knows.
    const words = {
      en: ["load tools on demand", "Try again", "every tool"],
      "zh-Hans": ["按需加载工具", "重试", "全部工具"],
      ja: ["必要時読み込み", "再試行", "すべてのツール"],
    };
    try {
      for (const [lang, [title, detail, promise]] of Object.entries(words)) {
        await initI18n(lang);
        const fix = turnErrorFix("tool_search_unsupported")!;
        expect(fix.title).toContain(title);
        expect(fix.detail).toContain(detail);
        expect(fix.detail).not.toContain(promise);
        expect(fix.action).toBe("retry");
      }
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("reads a gateway's tool-search refusal from its 400, for a hub that does not code it", () => {
    // i18n-ignore: a gateway's words
    const edge = "API Error: 400 request is not representable on this edge: ToolSearch is not supported on this endpoint (defer_loading:true, tool_search_*, tool_reference); resend with all tools loaded";
    expect(parseAPIError(edge)).toMatchObject({ code: "tool_search_unsupported", status: 400, transient: false });
    expect(parseAPIError("API Error: 400 tools.3.defer_loading: Extra inputs are not permitted")?.code).toBe("tool_search_unsupported");
    // another 400, or a tool-search word outside a refused request, is not it
    expect(parseAPIError("API Error: 400 invalid request")?.code).toBeUndefined();
    expect(parseAPIError("API Error: 500 tool_reference upstream crashed")?.code).toBeUndefined();
    expect(parseAPIError("API Error: 400 There's an issue with the selected model")?.code).toBe("model_route");
  });

  it("reads Claude Code's own refusals as model errors (the hub's markers)", () => {
    // i18n-ignore: Claude Code's words
    expect(parseAPIError("Not logged in · Please run /login")).toMatchObject({ code: "not_logged_in", transient: false });
    expect(parseAPIError("Invalid API key · Fix external API key")).toMatchObject({ code: "auth_failed", transient: false });
    expect(parseAPIError("There's an issue with the selected model (x). It may not exist")?.code).toBe("model_route");
    expect(parseAPIError("API Error: 401 Failed to authenticate")?.code).toBe("auth_failed");
    // a reply that talks about being logged out is a reply
    expect(parseAPIError("Not logged in to GitHub, so I can't push")).toBeNull();
  });
});

describe("providerName", () => {
  it("hides the hub's old provider ids", () => {
    expect(providerName("legacy.kb", [])).toBeUndefined();
    expect(providerName("legacy.kb", [{ id: "legacy.kb", name: "hub 知识 API" } as never])).toBe("hub 知识 API");
    expect(providerName("mine", [])).toBe("mine");
  });
});

describe("what the provider wizard reads", () => {
  const ok = (id: string) => step(id, "ok");
  it("knows a Claude Code that already answers", () => {
    expect(claudeWorks(ready("full", [ok("computer"), ok("claude"), ok("model"), step("first_message", "todo")]))).toBe(true);
    // never verified: not known to work, so a new key may still take over
    expect(claudeWorks(ready("full", [ok("computer"), ok("claude"), step("model", "unknown", { code: "model_unverified" })]))).toBe(false);
    expect(claudeWorks(ready("setup", [ok("computer"), ok("claude"), step("model", "problem", { code: "not_logged_in" })]))).toBe(false);
    expect(claudeWorks(null)).toBe(false);
  });

  it("knows a computer with no Claude Code", () => {
    expect(claudeMissing(ready("setup", [ok("computer"), step("claude", "problem", { code: "claude_missing" })]))).toBe(true);
    expect(claudeMissing(ready("setup", [ok("computer"), step("claude", "problem", { code: "launcher_missing" })]))).toBe(false);
    expect(claudeMissing(ready("full", [ok("computer"), ok("claude")]))).toBe(false);
    expect(claudeMissing(null)).toBe(false);
  });
});

describe("asking again", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    resetReadiness();
  });

  it("shares the request in flight, except after a change made here", async () => {
    Object.assign(getState(), { features: ["assistant_readiness"] });
    let n = 0;
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    vi.stubGlobal("fetch", vi.fn(() => (n++, gate.then(() => new Response(JSON.stringify(ready("full", [step("computer", "ok")])), { status: 200, headers: { "Content-Type": "application/json" } })))));
    resetReadiness();
    const a = refreshReadiness({ force: true });
    const b = refreshReadiness({ force: true });
    expect(b).toBe(a);
    const c = readinessChanged();
    expect(c).not.toBe(a);
    release();
    await Promise.all([a, b, c]);
    expect(n).toBe(2);
  });

  it("does not let a forced ask share a request made earlier", async () => {
    Object.assign(getState(), { features: ["assistant_readiness"] });
    let n = 0;
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    vi.stubGlobal("fetch", vi.fn(() => (n++, gate.then(() => new Response(JSON.stringify(ready("full", [step("computer", "ok")])), { status: 200, headers: { "Content-Type": "application/json" } })))));
    resetReadiness();
    const a = refreshReadiness({ force: true });
    await Promise.resolve();
    const b = refreshReadiness({ force: true });
    expect(b).not.toBe(a);
    release();
    await Promise.all([a, b]);
    expect(n).toBe(2);
  });

  it("keeps asking at growing intervals while the button is held, and stops when told", async () => {
    vi.useFakeTimers();
    try {
      Object.assign(getState(), { features: ["assistant_readiness"] });
      const fetcher = vi.fn(() => Promise.reject(new TypeError("down")));
      vi.stubGlobal("fetch", fetcher);
      resetReadiness();
      const stop = retryWhileBlocked();
      await vi.advanceTimersByTimeAsync(1_999);
      expect(fetcher).toHaveBeenCalledTimes(0);
      await vi.advanceTimersByTimeAsync(1);
      expect(fetcher).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(4_000);
      expect(fetcher).toHaveBeenCalledTimes(2);
      stop();
      await vi.advanceTimersByTimeAsync(120_000);
      expect(fetcher).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
