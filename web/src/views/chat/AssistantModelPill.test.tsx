// @vitest-environment jsdom
// A Persona chat's model pill and panel (#5033): on a hub that keeps picks a pick is PUT for the conversation (drawn at
// once, put back with the hub's words on a refusal) and following the default again is a DELETE; the strip on top says
// which it is. A chat that runs offers no provider of another engine; the new-conversation page offers every kind. An older hub keeps the pick in this page and the effort on the session (POST …/modes), as the old chips
// did; the new-conversation page keeps its pick for the start. Codex's Fast goes through the speed request. The panel
// is a stand-in that keeps its props and draws its header (ui/model-panel has its own tests).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { agentName } from "../../lib/agent-kind";
import { NEW_CONVERSATION, pickedEffort, pickedLauncher, pickedModel, pickedServiceTier, pickEffort, pickLauncher, pickModel, pickServiceTier } from "../../lib/assistant-chat";
import { panelSections } from "../../lib/model-panel";
import { parseAPIError } from "../../lib/api-errors";
import { requestModelPicker } from "../../lib/model-switch";
import { getSession, getState, upsertSession, useStore } from "../../lib/store";
import type { ChatLine } from "../../lib/turns";
import type { Item, Session } from "../../lib/types";
import type { ModelPanelProps } from "../../ui/model-panel/ModelPanel";
import launcherFixture from "../../../../spec/fixtures/assistant-launchers/cases.json";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});
const panel = vi.hoisted(() => ({ props: null as ModelPanelProps | null }));
vi.mock("../../ui/model-panel/ModelPanel", () => ({
  ModelPanel: (p: ModelPanelProps) => {
    panel.props = p;
    return <div data-testid="panel">{p.header}</div>;
  },
}));
const toast = vi.hoisted(() => vi.fn());
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast }));

const { AssistantModelPill, assistantPickerKey, useAssistantTurnFix } = await import("./ModelPill");
const { ErrorBanner } = await import("./Lines");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const flush = () => act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); });
const ALL = ["low", "medium", "high", "xhigh", "max"];
const claudeList = { agent: "claude", models: [
  { id: "default", name: "Default (recommended)", default: true, resolved: "claude-opus-5-5", efforts: ALL },
  { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: ALL },
  { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5", efforts: ["low", "high"] },
] };
const codexList = { agent: "codex", models: [
  { id: "gpt-fast", name: "GPT Fast", efforts: ["low", "high"], service_tiers: [{ id: "default", name: "Standard" }, { id: "priority", name: "Fast" }] },
  { id: "gpt-ultrafast", name: "GPT Ultrafast", efforts: ["low", "high"], service_tiers: [{ id: "priority", name: "Fast" }, { id: "ultrafast", name: "Ultrafast" }] },
  { id: "gpt-standard", name: "GPT Standard", efforts: ["low", "high"] },
] };
const sharedLaunchers = launcherFixture.cases[0];
let machine = 0;
let host: HTMLDivElement;
let root: Root;
let calls: { method: string; url: string; body?: unknown }[];
let pickAnswer: () => Response;
let switchAnswer: () => Response;
let defaultSetup: { provider: string; model: string; effort: string };
let superviseSetup: { provider: string; model: string; effort: string };
let mainSetup: { provider: string; model: string; effort: string } | null | undefined;
let sharedPicker = false;
let runtimeMain = false;

const mid = () => `persona-${machine}`;
const chat = (x: Partial<Session> = {}): Session =>
  ({ machine_id: mid(), session_id: "c1", title: "t", cwd: "/a", status: "idle", state: "idle", assistant: true, agent: "claude", launcher: "claude", model: "claude-opus-5-5", effort: "high", ...x }) as Session;

beforeEach(() => {
  machine++;
  panel.props = null;
  toast.mockClear();
  calls = [];
  pickAnswer = () => json({ ok: true, model_pick: {} });
  switchAnswer = () => json({ phase: "preparing" });
  defaultSetup = { provider: "claude", model: "opus", effort: "high" };
  mainSetup = undefined;
  superviseSetup = defaultSetup; // the supervisor's own purpose follows Main chat until it is set apart
  sharedPicker = false;
  runtimeMain = false;
  Object.assign(getState(), { features: ["assistant_model_pick", "assistant_start_service_tier", "typed_models"], sessions: {}, assistant: { machine_id: mid(), launcher: "claude", main: "c1", conversations: [] } });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method: init?.method ?? "GET", url, body });
    if (url.endsWith("/api/v1/assistant")) {
      const a = getState().assistant!;
      const s = getSession(mid(), "c1");
      const conversations = a.conversations ?? [];
      return json({ ...a, conversations: s && !conversations.some((c) => c.session_id === s.session_id) ? [...conversations, s] : conversations });
    }
    if (url.includes("/codex/models")) return json(codexList);
    if (sharedPicker && url.includes("/runtime/models")) return json({ agent: "claude", models: [] });
    if (url.includes("/models")) return json(claudeList);
    if (url.includes("/launchers?purpose=assistant.main")) return json({ default: mainSetup === undefined ? defaultSetup : mainSetup });
    if (url.includes("/launchers?purpose=assistant.supervise")) return json({ launchers: [{ name: "claude", source: "builtin" }], default: superviseSetup, unavailable: [] });
    if (url.includes("/launchers?purpose=assistant")) return json(sharedPicker ? {
      launchers: sharedLaunchers.candidates,
      default: defaultSetup,
      unavailable: [],
    } : {
      launchers: [{ name: "claude", source: "builtin" }, { name: "relay", source: "zshrc" }, { name: "codex", source: "builtin", agent: "codex", agent_type: "codex" }, ...(runtimeMain ? [{ name: "hosted-claude", source: "runtime-other", agent: "claude", assistant: true }] : [])],
      default: defaultSetup,
      unavailable: [{ name: "gpt-direct", kind: "api", code: "api_protocol", reason: "Responses API" }],
    });
    if (url.includes("/assistant/conversations/c1/segments?")) return json({ enabled: true, epoch: 4 });
    if (url.endsWith("/assistant/conversations/c1/switch")) return switchAnswer();
    if (url.includes("/assistant/conversations/c1/model")) return pickAnswer();
    // Newly listed chats re-read Assistant; return its complete snapshot instead of an operation receipt.
    if (url.endsWith("/assistant")) return json(getState().assistant);
    return json({ ok: true });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  pickLauncher(`${mid()}/c1`, undefined);
  pickLauncher(NEW_CONVERSATION, undefined);
  pickEffort(NEW_CONVERSATION, undefined);
  pickServiceTier(undefined);
});

async function show(s?: Session) {
  if (s) upsertSession(s);
  act(() => root.render(<AssistantModelPill session={s} />));
  await flush();
}
const pill = () => host.querySelector<HTMLButtonElement>("button[data-model-pill]")!;
async function open() {
  await act(async () => pill().click());
  await act(async () => vi.dynamicImportSettled());
  await flush();
}
const strip = () => document.querySelector<HTMLElement>("[data-pick-strip]");
const sent = (method: string) => calls.filter((c) => c.method === method && c.url.includes("/assistant/conversations/c1/model")).map((c) => c.body ?? null);

describe("a Persona chat's pill on a hub that keeps picks", () => {
  it("shows what the main chat runs, opens the panel with its providers and says it follows the default", async () => {
    await show(chat());
    expect(pill().textContent).toContain("Opus 5.5");
    expect(calls.some((c) => c.url.includes(`/machines/${mid()}/launchers?purpose=assistant`))).toBe(true);
    await open();
    expect(panel.props!.scope).toBe("assistant");
    // codex is another engine than this Claude Code chat runs: off, with why (a new conversation is needed)
    const otherEngine = t("chat-modelpanel.other_engine", { from: agentName("claude"), to: agentName("codex") });
    expect(panel.props!.providers.map((p) => [p.id, p.unavailable ?? ""])).toEqual([["claude", ""], ["relay", ""], ["codex", otherEngine], ["gpt-direct", "Responses API"]]);
    expect(panel.props!.state.current).toEqual({ provider: "claude", model: "opus", effort: "high" });
    expect(strip()!.getAttribute("data-pick-strip")).toBe("default");
    expect(strip()!.textContent).toBe(t("chat-modelpill.header_default", { setup: "Claude Code · Opus 5.5" }));
  });

  it("keeps a pick for the conversation (PUT), drawn at once, and follows the default again (DELETE)", async () => {
    const s = chat();
    await show(s);
    await open();
    expect(await act(() => panel.props!.onPick({ provider: "relay", model: "haiku", effort: "low" }))).toBe(true);
    expect(sent("PUT")).toEqual([{ launcher: "relay", model: "haiku", effort: "low", machine_id: mid() }]);
    expect(getSession(mid(), "c1")?.model_pick).toEqual({ launcher: "relay", model: "haiku", effort: "low" });
    // the provider's default entry goes without a model
    await act(() => panel.props!.onPick({ provider: "claude", model: "default" }));
    expect(sent("PUT")[1]).toEqual({ launcher: "claude", machine_id: mid() });
    act(() => root.render(<AssistantModelPill session={getSession(mid(), "c1")!} />));
    await flush();
    expect(strip()!.getAttribute("data-pick-strip")).toBe("pick");
    const back = strip()!.querySelector("button")!;
    expect(back.textContent).toBe(t("chat-modelpill.follow_default"));
    await act(async () => back.click());
    expect(sent("DELETE")).toEqual([null]);
    expect(getSession(mid(), "c1")?.model_pick).toBeUndefined();
  });

  it("puts a refused pick back, with the hub's words", async () => {
    await show(chat({ model_pick: { launcher: "claude", model: "haiku" } }));
    await open();
    pickAnswer = () => json({ error: "这个服务商不对这台电脑开放", code: "launcher_out_of_scope" }, 409);
    expect(await act(() => panel.props!.onPick({ provider: "relay", model: "opus" }))).toBe(false);
    expect(getSession(mid(), "c1")?.model_pick).toEqual({ launcher: "claude", model: "haiku" });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("这个服务商不对这台电脑开放"), "error");
  });

  it("puts back a pick the hub finds is another engine (409 launcher_other_kind), with its words", async () => {
    await show(chat());
    await open();
    pickAnswer = () => json({ error: "relay runs Codex; this conversation runs on Claude Code", code: "launcher_other_kind", conversation_agent: "claude", launcher: "relay", launcher_agent: "codex", delivered: "none" }, 409);
    expect(await act(() => panel.props!.onPick({ provider: "relay", model: "opus" }))).toBe(false);
    expect(sent("PUT")).toEqual([{ launcher: "relay", model: "opus", machine_id: mid() }]);
    expect(getSession(mid(), "c1")?.model_pick).toBeUndefined();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("relay runs Codex; this conversation runs on Claude Code"), "error");
  });

  it("shows the pill on a conversation that is not the main one and keeps its pick there (#9248)", async () => {
    getState().assistant!.main = "other";
    await show(chat({ launcher: "relay", model: "claude-haiku-5-5" }));
    expect(pill()).not.toBeNull();
    await open();
    // no pick, not a conversation that follows the default: it runs on what it started with, the strip says so
    expect(strip()!.getAttribute("data-pick-strip")).toBe("own");
    expect(await act(() => panel.props!.onPick({ provider: "claude", model: "opus" }))).toBe(true);
    expect(sent("PUT")).toEqual([{ launcher: "claude", model: "opus", machine_id: mid() }]);
    expect(getSession(mid(), "c1")?.model_pick).toEqual({ launcher: "claude", model: "opus" });
    act(() => root.render(<AssistantModelPill session={getSession(mid(), "c1")!} />));
    await flush();
    expect(strip()!.getAttribute("data-pick-strip")).toBe("pick");
    expect(pill().textContent).toContain("Opus 5.5");
    await act(async () => strip()!.querySelector("button")!.click());
    expect(sent("DELETE")).toEqual([null]);
  });

  it("shows a leader and a scheduled task's conversation what the next message runs on: Main chat until they pick (#9248)", async () => {
    for (const as of [{ leaders: ["c1"] }, {}]) {
      Object.assign(getState().assistant!, { main: "other", leaders: undefined }, as);
      // it last ran on Haiku, but its next message follows Model configuration › Main chat (Opus)
      await show(chat({ launcher: "relay", model: "claude-haiku-5-5", ...(as.leaders ? {} : { origin: "task" }) }));
      expect(getState().assistant).toMatchObject({ launcher: "claude", main: "other", leaders: as.leaders });
      expect(pill().textContent).toContain("Opus 5.5");
      expect(pill().textContent).not.toContain("Haiku");
      await open();
      expect(strip()!.getAttribute("data-pick-strip")).toBe("default");
      act(() => root.render(<></>));
      await flush();
    }
    // with a pick it shows the pick, as it runs on it
    getState().assistant!.leaders = ["c1"];
    await show(chat({ launcher: "relay", model: "claude-haiku-5-5", model_pick: { launcher: "claude", model: "haiku" } }));
    expect(pill().textContent).toContain("Haiku 5.5");
  });

  it("shows the background supervisor what its own model purpose runs, not Main chat's (#9248)", async () => {
    Object.assign(getState().assistant!, { main: "other", background_supervisor: "c1" });
    superviseSetup = { provider: "claude", model: "haiku", effort: "low" }; // Model configuration set the supervisor apart: Haiku, Main chat stays Opus
    await show(chat({ launcher: "claude", model: "claude-opus-5-5", effort: undefined, origin: "task" }));
    expect(pill().textContent).toContain("Haiku 5.5");
    expect(pill().textContent).not.toContain("Opus");
    await open();
    expect(strip()!.getAttribute("data-pick-strip")).toBe("default");
    expect(panel.props!.state.current).toEqual({ provider: "claude", model: "haiku", effort: "low" });
  });

  it("keeps its hooks when the conversation becomes or stops being the main one (#7070)", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      const s = chat();
      await show(s);
      expect(pill()).not.toBeNull();
      getState().assistant!.main = "other";
      expect(() => {
        act(() => root.render(<AssistantModelPill session={{ ...s }} />));
      }).not.toThrow();
      await flush();
      expect(pill()).not.toBeNull();
      getState().assistant!.main = "c1";
      expect(() => {
        act(() => root.render(<AssistantModelPill session={{ ...s }} />));
      }).not.toThrow();
      await flush();
      expect(pill()).not.toBeNull();
      expect(error).not.toHaveBeenCalled();
    } finally {
      error.mockRestore();
    }
  });

  it("Codex's Fast goes through the speed request", async () => {
    await show(chat({ agent: "codex", service_tiers: [{ id: "default", name: "Standard" }, { id: "priority", name: "Fast" }] }));
    await open();
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "fast" }))).toBe(true);
    expect(calls.filter((c) => c.url.endsWith("/modes")).map((c) => c.body)).toEqual([{ service_tier: "priority" }]);
  });

  it("names the acknowledged Ultrafast tier in a running Assistant chat", async () => {
    await show(chat({ agent: "codex", launcher: "codex", model: "gpt-ultrafast", service_tier: "ultrafast" }));
    expect(pill().querySelectorAll('[data-pill-speed="ultrafast"] svg.fill-current path')).toHaveLength(2);
    const speed = t("ui-modelpanel.speed_ultrafast");
    expect(pill().title).toContain(`High · ${speed}`);
    expect(pill().getAttribute("aria-label")).toContain(`High · ${speed}`);
  });

  it("re-reads the effective Main chat model when a hub push bumps the conversation revision (#6542)", async () => {
    const s = chat();
    await show(s);
    const assistantReads = () => calls.filter((c) => c.url.includes("/launchers?purpose=assistant.main")).length;
    const afterMount = assistantReads();
    expect(afterMount).toBeGreaterThan(0);
    // a re-render with no new push reads nothing again (the page-life cache stands)
    act(() => root.render(<AssistantModelPill session={s} />));
    await flush();
    expect(assistantReads()).toBe(afterMount);
    // a hub push that changes the session (a new rev) forces a fresh read so the pill matches the effective model
    const pushed = { ...s, rev: 7 };
    upsertSession(pushed);
    act(() => root.render(<AssistantModelPill session={pushed} />));
    await flush();
    expect(assistantReads()).toBe(afterMount + 1);
  });

  it("holds those re-reads while a turn streams and reads once when it ends", async () => {
    let s = chat({ status: "working", state: "working", rev: 1 });
    await show(s);
    const reads = () => {
      const l = calls.filter((c) => c.url.includes("/launchers?purpose="));
      return [l.filter((c) => c.url.includes("purpose=assistant.main")).length, l.filter((c) => /purpose=assistant(&|$)/.test(c.url)).length];
    };
    const afterMount = reads();
    // every write of the turn moves the revision: none of them reads again
    for (let rev = 2; rev <= 21; rev++) {
      s = { ...s, rev };
      await show(s);
    }
    expect(reads()).toEqual(afterMount);
    // the turn ends: one read of each, then nothing more until the revision moves again
    s = { ...s, status: "idle", state: "idle", rev: 22 };
    await show(s);
    expect(reads()).toEqual([afterMount[0] + 1, afterMount[1] + 1]);
    await show({ ...s });
    expect(reads()).toEqual([afterMount[0] + 1, afterMount[1] + 1]);
    // a move after the turn (a hub-side change) reads at once
    s = { ...s, rev: 23 };
    await show(s);
    expect(reads()).toEqual([afterMount[0] + 2, afterMount[1] + 2]);
  });
});

describe("an older hub and the new-conversation page", () => {
  it("an older hub keeps the pick in this page and the effort on the session", async () => {
    Object.assign(getState(), { features: ["typed_models"] });
    await show(chat());
    await open();
    expect(await act(() => panel.props!.onPick({ provider: "relay", model: "haiku", effort: "low" }))).toBe(true);
    expect(sent("PUT")).toEqual([]);
    expect([pickedLauncher(`${mid()}/c1`), pickedModel(`${mid()}/c1`)]).toEqual(["relay", "haiku"]);
    expect(calls.filter((c) => c.url.endsWith("/modes")).map((c) => c.body)).toEqual([{ effort: "low" }]);
    expect(strip()!.querySelector("span")!.textContent).toBe(t("chat-modelpill.header_picked_page", { setup: "relay · Haiku 5.5" }));
    await act(async () => strip()!.querySelector("button")!.click());
    expect(pickedLauncher(`${mid()}/c1`)).toBeUndefined();
  });

  it("drops this page's pick of a launcher the list no longer offers, saying so", async () => {
    Object.assign(getState(), { features: [] });
    pickLauncher(`${mid()}/c1`, "gone-gw");
    await show(chat());
    expect(pickedLauncher(`${mid()}/c1`)).toBeUndefined();
    expect(toast).toHaveBeenCalledWith(t("chat-modelpill.pick_dropped", { launcher: "gone-gw" }), "info");
  });

  it("the new-conversation page drops such a pick whole, its model and effort too, so none of it goes with the start", async () => {
    pickLauncher(NEW_CONVERSATION, "gone-gw");
    pickModel(NEW_CONVERSATION, "glm-5");
    pickEffort(NEW_CONVERSATION, "max");
    await show(undefined);
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual([undefined, undefined, undefined]);
    expect(toast).toHaveBeenCalledWith(t("chat-modelpill.pick_dropped", { launcher: "gone-gw" }), "info");
    // what it says now is the default's, effort included
    expect(pill().textContent).toContain("Opus 5.5");
    await open();
    expect(strip()!.textContent).toBe(t("chat-modelpill.header_new", { setup: "Claude Code · Opus 5.5" }));
    expect(panel.props!.state.current).toEqual({ provider: "claude", model: "opus", effort: "high" });
  });

  it("the new-conversation page offers Fast only for a model whose catalog advertises it, independently of effort", async () => {
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-fast");
    pickEffort(NEW_CONVERSATION, "low");
    await show(undefined);
    await open();
    const models = panel.props!.providers.find((p) => p.id === "codex")!.models!;
    expect(models.find((m) => m.id === "gpt-fast")!.speeds?.map((tier) => tier.id)).toEqual(["standard", "fast"]);
    expect(models.find((m) => m.id === "gpt-standard")!.speeds).toBeUndefined();
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "low", speed: "standard" });
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "fast" }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "low", speed: "fast" });
    expect(await act(() => panel.props!.onApplyCurrent!({ effort: "high" }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "high", speed: "fast" });
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "standard" }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "high", speed: "standard" });
    // Reset can carry effort and Fast together; neither is dropped.
    await act(() => panel.props!.onApplyCurrent!({ speed: "fast" }));
    expect(await act(() => panel.props!.onApplyCurrent!({ effort: "", speed: "standard" }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", speed: "standard" });
    // A whole-setup pick applies a Fast-only change, and an unsupported model never inherits it.
    expect(await act(() => panel.props!.onPick({ provider: "codex", model: "gpt-fast", speed: "fast" }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", speed: "fast" });
    expect(await act(() => panel.props!.onPick({ provider: "codex", model: "gpt-standard" }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-standard" });
    expect(pickedServiceTier()).toBeUndefined();
  });

  it("shows a draft's advertised Ultrafast choice and clears its mark when switching to a Fast-only model", async () => {
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-ultrafast");
    pickEffort(NEW_CONVERSATION, "high");
    await show(undefined);
    await open();
    const models = panel.props!.providers.find((p) => p.id === "codex")!.models!;
    expect(models.find((m) => m.id === "gpt-ultrafast")!.speeds?.map(({ id }) => id)).toEqual(["standard", "fast", "ultrafast"]);
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "ultrafast" }))).toBe(true);
    expect(pickedServiceTier()).toBe("ultrafast");
    expect(panel.props!.state.current).toMatchObject({ effort: "high", speed: "ultrafast" });
    expect(pill().querySelectorAll('[data-pill-speed="ultrafast"] svg.fill-current path')).toHaveLength(2);
    const speed = t("ui-modelpanel.speed_ultrafast");
    expect(pill().title).toContain(`High · ${speed}`);
    expect(pill().getAttribute("aria-label")).toContain(`High · ${speed}`);

    expect(await act(() => panel.props!.onPick({ provider: "codex", model: "gpt-fast" }))).toBe(true);
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "ultrafast" }))).toBe(false);
    expect(pickedServiceTier()).toBeUndefined();
    expect(pill().querySelector("[data-pill-speed]")).toBeNull();
  });

  it("a Fast-only choice pins the displayed launcher, model and effort for a cleared main", async () => {
    defaultSetup = { provider: "codex", model: "gpt-fast", effort: "low" };
    await show(undefined); // no launcher/model pick of its own, as after /clear
    await open();
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "low", speed: "standard" });
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "fast" }))).toBe(true);
    expect(pickedLauncher(NEW_CONVERSATION)).toBe("codex");
    expect(pickedModel(NEW_CONVERSATION)).toBe("gpt-fast");
    expect(pickedEffort(NEW_CONVERSATION)).toBe("low");
    expect(pickedServiceTier()).toBe("priority");
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "fast", effort: "high" }))).toBe(true);
    expect(pickedEffort(NEW_CONVERSATION)).toBe("high");
  });

  it("a Fast-only whole-setup pick pins the displayed default launcher, model and effort", async () => {
    defaultSetup = { provider: "codex", model: "gpt-fast", effort: "low" };
    await show(undefined);
    await open();
    const shown = panel.props!.state.current!;
    expect(shown).toEqual({ provider: "codex", model: "gpt-fast", effort: "low", speed: "standard" });
    expect(await act(() => panel.props!.onPick({ ...shown, speed: "fast" }))).toBe(true);
    expect(pickedLauncher(NEW_CONVERSATION)).toBe("codex");
    expect(pickedModel(NEW_CONVERSATION)).toBe("gpt-fast");
    expect(pickedEffort(NEW_CONVERSATION)).toBe("low");
    expect(pickedServiceTier()).toBe("priority");
    // A speed-only edit keeps the setup; only the explicit Follow default resets every part.
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "standard" }))).toBe(true);
    expect(pickedLauncher(NEW_CONVERSATION)).toBe("codex");
    expect(pickedModel(NEW_CONVERSATION)).toBe("gpt-fast");
    expect(pickedEffort(NEW_CONVERSATION)).toBe("low");
    expect(pickedServiceTier()).toBeUndefined();
    await act(async () => strip()!.querySelector("button")!.click());
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual([undefined, undefined, undefined]);
    expect(pickedServiceTier()).toBeUndefined();
  });

  it("a Recent row for the shown default with Fast is pickable and sends Fast", async () => {
    defaultSetup = { provider: "codex", model: "gpt-fast", effort: "low" };
    await show(undefined);
    await open();
    const recent = panelSections({
      providers: panel.props!.providers,
      state: panel.props!.state,
      view: { kind: "recent" },
      query: "",
      favorites: [],
      recent: [{ provider: "codex", model: "gpt-fast", effort: "low", speed: "fast" }],
      overrides: {},
    }).flatMap((s) => s.rows).find((r) => r.kind === "recent");
    expect(recent?.provider.unavailable).toBeUndefined();
    expect(recent?.setup).toEqual({ provider: "codex", model: "gpt-fast", effort: "low", speed: "fast" });
    expect(await act(() => panel.props!.onPick(recent!.setup))).toBe(true);
    expect(pickedServiceTier()).toBe("priority");
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual(["codex", "gpt-fast", "low"]);
  });

  it("an older hub does not offer a first-turn Fast choice it would ignore", async () => {
    Object.assign(getState(), { features: ["assistant_model_pick", "typed_models"] });
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-fast");
    pickServiceTier("priority"); // a draft left by a hub that just downgraded
    await show(undefined);
    await open();
    const models = panel.props!.providers.find((p) => p.id === "codex")!.models!;
    expect(models.find((m) => m.id === "gpt-fast")!.speeds).toBeUndefined();
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast" });
    expect(pickedServiceTier()).toBeUndefined();
  });

  it("the new-conversation page keeps its pick, the effort too, for the start", async () => {
    await show(undefined);
    expect(pill().textContent).toContain("Opus 5.5");
    await open();
    expect(strip()!.textContent).toBe(t("chat-modelpill.header_new", { setup: "Claude Code · Opus 5.5" }));
    // nothing runs yet: every engine can start it
    expect(panel.props!.providers.find((p) => p.id === "codex")!.unavailable).toBeUndefined();
    expect(await act(() => panel.props!.onPick({ provider: "relay", model: "haiku", effort: "low" }))).toBe(true);
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual(["relay", "haiku", "low"]);
    expect(calls.some((c) => c.method !== "GET")).toBe(false);
  });

  it("picking the displayed default model on a fresh draft pins it, and Follow default clears it explicitly (#13567)", async () => {
    await show(undefined);
    await open();
    const shown = panel.props!.state.current!;
    expect(shown).toEqual({ provider: "claude", model: "opus", effort: "high" });
    expect(strip()!.getAttribute("data-pick-strip")).toBe("default");
    // Picking the already-displayed named model must pin it instead of dropping the pick as a no-op.
    expect(await act(() => panel.props!.onPick(shown))).toBe(true);
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual(["claude", "opus", "high"]);
    await flush();
    expect(strip()!.getAttribute("data-pick-strip")).toBe("pick");
    // Picking the explicit "default" row follows the launcher default (launcher pinned, model omitted).
    expect(await act(() => panel.props!.onPick({ provider: "claude", model: "default" }))).toBe(true);
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION)]).toEqual(["claude", undefined]);
    // Clicking Follow default clears the whole draft pick back to following Model configuration.
    await act(async () => strip()!.querySelector("button")!.click());
    await flush();
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual([undefined, undefined, undefined]);
    expect(strip()!.getAttribute("data-pick-strip")).toBe("default");
  });

  it("picking a concrete model marked default: true on a fresh draft keeps its model ID (#13567)", async () => {
    defaultSetup = { provider: "claude", model: "", effort: "" };
    const origFetch = globalThis.fetch;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/claude/models")) {
        return json({
          agent: "claude",
          models: [
            { id: "default", name: "Default (recommended)", default: false, resolved: "claude-opus-5-5", efforts: ALL },
            { id: "opus", name: "Opus 5.5", default: true, resolved: "claude-opus-5-5", efforts: ALL },
          ],
        });
      }
      return origFetch(input, init);
    }));
    await show(undefined);
    await open();
    expect(panel.props!.state.current).toEqual({ provider: "claude", model: "opus" });
    expect(await act(() => panel.props!.onPick({ provider: "claude", model: "opus" }))).toBe(true);
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION)]).toEqual(["claude", "opus"]);
  });

  it("replays the shared eligible choices in both pickers without a legacy concrete default model", async () => {
    sharedPicker = true;
    defaultSetup = { provider: "runtime", model: "", effort: "high" };
    expect(launcherFixture.version).toBe(1);
    expect(sharedLaunchers.candidates.map((l) => l.name)).toEqual(["runtime", "codex-gw", "team-api"]);

    await show(undefined);
    await open();
    const newProviders = panel.props!.providers;
    const newNames = newProviders.filter((p) => !p.unavailable).map((p) => p.id);
    const newModels = newProviders.flatMap((p) => p.models ?? []).map((m) => [m.id, m.name]);
    act(() => root.render(<></>));
    await flush();

    await show(chat({ launcher: "runtime", agent: "claude", model: undefined, effort: "high" }));
    await open();
    const mainProviders = panel.props!.providers;
    expect(mainProviders.filter((p) => !p.unavailable).map((p) => p.id)).toEqual(["runtime", "team-api"]);
    expect(newNames).toEqual(sharedLaunchers.assistant);
    expect(mainProviders.find((p) => p.id === "codex-gw")?.unavailable).toBe(t("chat-modelpanel.other_engine", { from: agentName("claude"), to: agentName("codex") }));

    // An older node's display placeholder means the launcher's default, never a model ID or English row label.
    expect(newModels.some(([id, name]) => id === "默认" || name === "默认")).toBe(false);
    expect(mainProviders.flatMap((p) => p.models ?? []).some((m) => m.id === "默认" || m.name === "默认")).toBe(false);
    expect(panel.props!.state.current).toBeNull();
    expect(calls.every((c) => !c.url.includes("/launchers?") || c.url.includes("purpose=assistant"))).toBe(true);
  });
});

// A model the chat can't run (turn_error_code model_route): "Fix it" on its error card opens this chat's own panel, where
// another model is picked, never the new-provider wizard; a kept pick can be dropped from the card too (#10365).
describe("a model error's Fix it", () => {
  // i18n-ignore: Claude Code's words
  const text = "There's an issue with the selected model (haiku). It may not exist or you may not have access to it.";
  const errorLine = {
    kind: "apiError", id: "err-e1", count: 1, info: parseAPIError(text)!,
    item: { id: "e1", seq: 1, role: "assistant", kind: "text", text } as Item,
  } as Extract<ChatLine, { kind: "apiError" }>;
  // the chat's pill and the banner over its composer, both on the conversation as the store has it (ChatView)
  function Chat() {
    const s = useStore(() => getSession(mid(), "c1"))!;
    const fix = useAssistantTurnFix(s, true);
    return (
      <>
        <AssistantModelPill session={s} />
        <ErrorBanner line={errorLine} assistant fix={fix} onDismiss={() => {}} />
      </>
    );
  }
  const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === label);
  async function showChat(s: Session) {
    act(() => upsertSession(s));
    act(() => root.render(<Chat />));
    await flush();
  }
  afterEach(() => history.replaceState(null, "", "/"));

  it("opens the chat's own panel on a key of its own: `/model` in a Persona chat still sends the text", async () => {
    await show(chat());
    expect(requestModelPicker(`${mid()}/c1`)).toBe(false);
    await act(async () => void expect(requestModelPicker(assistantPickerKey(mid(), "c1"))).toBe(true));
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(strip()).not.toBeNull();
    act(() => root.render(<></>));
    expect(requestModelPicker(assistantPickerKey(mid(), "c1"))).toBe(false);
  });

  it("opens the panel rather than a page, and follows the default again from the card, put back if the hub says no", async () => {
    history.replaceState(null, "", "/s/c1");
    await showChat(chat({ turn_error_code: "model_route", model_pick: { launcher: "relay", model: "haiku" } }));
    expect(host.textContent).toContain(t("lib-readiness.turn_model_route"));
    await act(async () => button(t("chat-lines.fix_it"))!.click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(strip()?.getAttribute("data-pick-strip")).toBe("pick");
    expect(location.pathname + location.search).toBe("/s/c1");
    pickAnswer = () => json({ error: "the hub is busy" }, 500);
    await act(async () => button(t("chat-modelpill.follow_default"))!.click());
    await flush();
    expect(sent("DELETE")).toEqual([null]);
    expect(getSession(mid(), "c1")?.model_pick).toEqual({ launcher: "relay", model: "haiku" });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("the hub is busy"), "error");
    pickAnswer = () => json({ ok: true });
    await act(async () => button(t("chat-modelpill.follow_default"))!.click());
    await flush();
    expect(sent("DELETE")).toEqual([null, null]);
    expect(getSession(mid(), "c1")?.model_pick).toBeUndefined();
    // following the default, the card has nothing to undo
    expect(button(t("chat-modelpill.follow_default"))).toBeUndefined();
    expect(button(t("chat-lines.fix_it"))).toBeDefined();
  });

  it("undoes the pick of a chat that keeps what it runs, and offers no way back on an older hub", async () => {
    getState().assistant!.main = "other";
    await showChat(chat({ turn_error_code: "model_route", model_pick: { launcher: "relay", model: "haiku" } }));
    expect(button(t("chat-modelpill.undo_pick"))).toBeDefined();
    Object.assign(getState(), { features: ["typed_models"] });
    await showChat(chat({ turn_error_code: "model_route", model_pick: { launcher: "relay", model: "haiku" } }));
    expect(button(t("chat-modelpill.undo_pick"))).toBeUndefined();
    expect(button(t("chat-lines.fix_it"))).toBeDefined();
  });

  // the four dimensions' last working setup (POST …/model/restore): on the card of a chat whose pick the hub keeps
  it("restores the last working setup from the card, and says so when there is none", async () => {
    await showChat(chat({ turn_error_code: "model_route", model_pick: { launcher: "relay", model: "haiku" } }));
    pickAnswer = () => json({ ok: true, model_pick: { launcher: "claude", model: "opus", effort: "high", runtime: "claude", channel: "native:claude" } });
    await act(async () => button(t("chat-modelpill.restore_last_good"))!.click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(calls.filter((c) => c.method === "POST" && c.url.endsWith("/assistant/conversations/c1/model/restore"))).toHaveLength(1);
    expect(getSession(mid(), "c1")?.model_pick).toEqual({ launcher: "claude", model: "opus", effort: "high", runtime: "claude", channel: "native:claude" });
    expect(toast).toHaveBeenCalledWith(t("execution.restored"), "ok");
    pickAnswer = () => json({ error: "no last good" }, 404);
    await act(async () => button(t("chat-modelpill.restore_last_good"))!.click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(toast).toHaveBeenCalledWith(t("execution.restore_none"), "info");
    // an older hub keeps no pick: no restore either
    Object.assign(getState(), { features: ["typed_models"] });
    await showChat(chat({ turn_error_code: "model_route" }));
    expect(button(t("chat-modelpill.restore_last_good"))).toBeUndefined();
  });
});


describe("main-only model defaults", () => {
  it("shows the main binding and its next-message effort without changing a new side conversation", async () => {
    mainSetup = { provider: "relay", model: "haiku", effort: "low" };
    await show(chat({ effort: "high" }));
    await open();
    expect(panel.props!.state.current).toEqual(mainSetup);
    act(() => root.render(<></>));
    await flush();
    await show(undefined);
    await open();
    expect(panel.props!.state.current).toEqual(defaultSetup);
    act(() => root.render(<></>));
    await flush();
    mainSetup = { provider: "claude", model: "opus", effort: "high" };
    await show(chat());
    await open();
    expect(panel.props!.state.current).toEqual(mainSetup);
    expect(calls.every((c) => c.method === "GET")).toBe(true);
  });

  it("uses main settings on the empty main starter while an ordinary starter keeps shared defaults", async () => {
    mainSetup = { provider: "relay", model: "haiku", effort: "low" };
    await act(() => root.render(<AssistantModelPill mainStart />));
    await flush();
    await open();
    expect(panel.props!.state.current).toEqual(mainSetup);
    act(() => root.render(<></>));
    await flush();
    await show(undefined);
    await open();
    expect(panel.props!.state.current).toEqual(defaultSetup);
  });
});


it("keeps the shared default when an older hub returns no main-purpose binding", async () => {
  mainSetup = null;
  defaultSetup = { provider: "relay", model: "haiku", effort: "low" };
  await show(chat());
  await open();
  expect(panel.props!.state.current?.provider).toBe("relay");
  expect(panel.props!.state.current?.model).toBe("haiku");
});
describe("explicit logical conversation switch", () => {
  it("shows other engines on a capable hub and sends nothing before confirmation", async () => {
    getState().features.push("assistant_segments");
    await show(chat());
    await open();
    expect(panel.props!.providers.find((p) => p.id === "codex")?.unavailable).toBeUndefined();
    expect(await act(() => panel.props!.onPick({ provider: "codex", model: "gpt-standard", effort: "high" }))).toBe(false);
    expect(panel.props!.held).toBe(true);
    expect(calls.some((c) => c.url.endsWith("/switch"))).toBe(false);
    const cancel = [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((b) => b.textContent === t("common.cancel"))!;
    await act(() => cancel.click());
    expect(sent("PUT")).toEqual([]);
    expect(calls.some((c) => c.url.endsWith("/switch"))).toBe(false);
  });
  it("uses the explicit operation and preserves it after an unknown response", async () => {
    getState().features.push("assistant_segments");
    await show(chat());
    await open();
    await act(() => panel.props!.onPick({ provider: "codex", model: "gpt-standard", effort: "high" }));
    switchAnswer = () => json({ error: "response unconfirmed", delivered: "unknown", code: "timeout" }, 502);
    const confirm = () => [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((b) => b.textContent === t("chat-modelpanel.switch_confirm"))!;
    await act(async () => confirm().click());
    await flush();
    expect(document.querySelector("[role=alert]")?.textContent).toBe(t("chat-modelpanel.switch_unconfirmed", { error: t("lib-api.computer_timeout") }));
    const first = calls.find((c) => c.url.endsWith("/switch"))!;
    expect(first.body).toMatchObject({ expected_epoch: 4, source_machine_id: mid(), machine_id: mid(), launcher: "codex", model: "gpt-standard", effort: "high" });
    expect(getSession(mid(), "c1")?.model_pick).toBeUndefined();
    switchAnswer = () => json({ phase: "preparing" });
    await act(async () => confirm().click());
    await flush();
    expect(calls.filter((c) => c.url.endsWith("/switch")).map((c) => c.body)).toEqual([first.body, first.body]);
    expect(calls.filter((c) => c.url.includes("/segments?"))).toHaveLength(2);
    expect(sent("PUT")).toEqual([]);
  });
});


it("keeps the actual engine connection until a same-kind runtime default is explicitly confirmed", async () => {
  runtimeMain = true;
  mainSetup = { provider: "hosted-claude", model: "opus", effort: "high" };
  getState().features.push("assistant_segments");
  await show(chat());
  await open();
  expect(panel.props!.state.current?.provider).toBe("claude");
  expect(await act(() => panel.props!.onPick(mainSetup!))).toBe(false);
  expect(calls.some((c) => c.url.endsWith("/switch"))).toBe(false);
  expect(sent("PUT")).toEqual([]);
  const confirm = [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((b) => b.textContent === t("chat-modelpanel.switch_confirm"))!;
  await act(async () => confirm.click());
  await flush();
  expect(calls.filter((c) => c.url.endsWith("/switch")).map((c) => c.body)).toEqual([expect.objectContaining({ launcher: "hosted-claude", expected_epoch: 4, machine_id: mid() })]);
  expect(getSession(mid(), "c1")?.launcher).toBe("claude");
  expect(getSession(mid(), "c1")?.model_pick).toBeUndefined();
  expect(defaultSetup).toEqual({ provider: "claude", model: "opus", effort: "high" });
});

it("shows the actual AGY engine and Gemini model on main chat under default Claude config instead of Claude Default (recommended), and loads and switches models in the panel from a cold cache", async () => {
  defaultSetup = { provider: "claude", model: "", effort: "high" };
  mainSetup = { provider: "claude", model: "", effort: "high" };
  const agyCalls: string[] = [];
  const origFetch = globalThis.fetch;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/launchers?purpose=assistant") && !url.includes("purpose=assistant.main") && !url.includes("purpose=assistant.supervise")) {
      return json({
        launchers: [
          { name: "claude", source: "builtin" },
          { name: "agy", source: "agy", agent: "agy", assistant: true },
        ],
        default: defaultSetup,
        unavailable: [],
      });
    }
    if (url.includes("/agy/models")) {
      agyCalls.push(url);
      if (!url.includes("host=agy")) {
        return json({ agent: "agy", models: [] });
      }
      return json({
        agent: "agy",
        models: [
          { id: "gemini-3-pro-high", name: "Gemini 3 Pro (High)", default_effort: "high", efforts: ["low", "medium", "high"] },
          { id: "gemini-3-flash", name: "Gemini 3 Flash", efforts: ["low", "medium", "high"] },
        ],
      });
    }
    return origFetch(input, init);
  }));
  await show(chat({
    agent: "agy",
    launcher: undefined,
    source_kind: "runtime",
    source_instance_id: "agy",
    model: "gemini-3-pro-high",
    effort: "high",
  }));
  expect(pill().querySelector("[data-pill-engine]")?.getAttribute("data-pill-engine")).toBe("agy");
  expect(pill().textContent).toContain("Gemini 3 Pro");
  expect(pill().textContent).not.toContain("Default (recommended)");
  expect(agyCalls.some((url) => url.includes("/launchers/agy/models") && url.includes("host=agy"))).toBe(true);

  // Opening the panel on a cold cache must read the same host=agy cache key (never stuck in loading) and allow switching models.
  await open();
  const agyProvider = panel.props!.providers.find((p) => p.id === "agy")!;
  expect(agyProvider).toBeDefined();
  expect(agyProvider.loading).toBeFalsy();
  expect(agyProvider.models?.map((m) => m.id)).toEqual(["gemini-3-pro-high", "gemini-3-flash"]);
  expect(panel.props!.state.current).toEqual({ provider: "agy", model: "gemini-3-pro-high", effort: "high" });

  expect(await act(() => panel.props!.onPick({ provider: "agy", model: "gemini-3-flash", effort: "medium" }))).toBe(true);
  expect(sent("PUT")).toEqual([{ launcher: "agy", model: "gemini-3-flash", effort: "medium", machine_id: mid() }]);
  expect(getSession(mid(), "c1")?.model_pick).toEqual({ launcher: "agy", model: "gemini-3-flash", effort: "medium" });

  // Reloading AGY from the panel must also keep the host=agy cache key.
  const beforeReload = agyCalls.length;
  await act(async () => {
    panel.props!.onReload!("agy");
  });
  await flush();
  expect(agyCalls.slice(beforeReload).some((url) => url.includes("/launchers/agy/models") && url.includes("host=agy"))).toBe(true);
});
