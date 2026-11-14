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
import { NEW_CONVERSATION, pickedEffort, pickedFast, pickedLauncher, pickedModel, pickEffort, pickFast, pickLauncher, pickModel } from "../../lib/assistant-chat";
import { panelSections } from "../../lib/model-panel";
import { getSession, getState, upsertSession } from "../../lib/store";
import type { Session } from "../../lib/types";
import type { ModelPanelProps } from "../../ui/model-panel/ModelPanel";

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

const { AssistantModelPill } = await import("./ModelPill");

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
  { id: "gpt-standard", name: "GPT Standard", efforts: ["low", "high"] },
] };
let machine = 0;
let host: HTMLDivElement;
let root: Root;
let calls: { method: string; url: string; body?: unknown }[];
let pickAnswer: () => Response;
let defaultSetup: { provider: string; model: string; effort: string };

const mid = () => `persona-${machine}`;
const chat = (x: Partial<Session> = {}): Session =>
  ({ machine_id: mid(), session_id: "c1", title: "t", cwd: "/a", status: "idle", state: "idle", assistant: true, agent: "claude", launcher: "claude", model: "claude-opus-5-5", effort: "high", ...x }) as Session;

beforeEach(() => {
  machine++;
  panel.props = null;
  toast.mockClear();
  calls = [];
  pickAnswer = () => json({ ok: true, model_pick: {} });
  defaultSetup = { provider: "claude", model: "opus", effort: "high" };
  Object.assign(getState(), { features: ["assistant_model_pick", "assistant_start_service_tier", "typed_models"], sessions: {}, assistant: { machine_id: mid(), launcher: "claude", main: "c1", conversations: [] } });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method: init?.method ?? "GET", url, body });
    if (url.includes("/codex/models")) return json(codexList);
    if (url.includes("/models")) return json(claudeList);
    if (url.includes("/launchers?purpose=assistant")) return json({
      launchers: [{ name: "claude", source: "builtin" }, { name: "relay", source: "zshrc" }, { name: "codex", source: "builtin", agent: "codex", agent_type: "codex" }],
      default: defaultSetup,
      unavailable: [{ name: "gpt-direct", kind: "api", code: "api_protocol", reason: "Responses API" }],
    });
    if (url.includes("/assistant/conversations/c1/model")) return pickAnswer();
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
  pickFast(false);
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
    expect(strip()!.textContent).toBe(t("chat-modelpill.header_default", { setup: "claude · Opus 5.5" }));
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

  it("hides the pill on a conversation the hub does not name as the main one (#6911)", async () => {
    getState().assistant!.main = "other";
    await show(chat({ launcher: "relay", model: "claude-haiku-5-5" }));
    expect(pill()).toBeNull();
    expect(calls.some((c) => c.url.includes("/assistant/conversations/c1/model"))).toBe(false);
  });

  it("keeps its hooks when the main conversation changes away from this one (#7070)", async () => {
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
      expect(pill()).toBeNull();
      expect(error).not.toHaveBeenCalled();
    } finally {
      error.mockRestore();
    }
  });

  it("keeps its hooks when this conversation becomes the main one (#7070)", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      const s = chat();
      getState().assistant!.main = "other";
      await show(s);
      expect(pill()).toBeNull();
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
    expect(await act(() => panel.props!.onApplyCurrent!({ fast: true }))).toBe(true);
    expect(calls.filter((c) => c.url.endsWith("/modes")).map((c) => c.body)).toEqual([{ service_tier: "priority" }]);
  });

  it("re-reads the effective Main chat model when a hub push bumps the conversation revision (#6542)", async () => {
    const s = chat();
    await show(s);
    const assistantReads = () => calls.filter((c) => c.url.includes("/launchers?purpose=assistant")).length;
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
    expect(strip()!.textContent).toBe(t("chat-modelpill.header_new", { setup: "claude · Opus 5.5" }));
    expect(panel.props!.state.current).toEqual({ provider: "claude", model: "opus", effort: "high" });
  });

  it("the new-conversation page offers Fast only for a model whose catalog advertises it, independently of effort", async () => {
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-fast");
    pickEffort(NEW_CONVERSATION, "low");
    await show(undefined);
    await open();
    const models = panel.props!.providers.find((p) => p.id === "codex")!.models!;
    expect(models.find((m) => m.id === "gpt-fast")!.fast).toBe(true);
    expect(models.find((m) => m.id === "gpt-standard")!.fast).not.toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "low" });
    expect(await act(() => panel.props!.onApplyCurrent!({ fast: true }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "low", fast: true });
    expect(await act(() => panel.props!.onApplyCurrent!({ effort: "high" }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "high", fast: true });
    expect(await act(() => panel.props!.onApplyCurrent!({ fast: false }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "high" });
    // Reset can carry effort and Fast together; neither is dropped.
    await act(() => panel.props!.onApplyCurrent!({ fast: true }));
    expect(await act(() => panel.props!.onApplyCurrent!({ effort: "", fast: false }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast" });
    // A whole-setup pick applies a Fast-only change, and an unsupported model never inherits it.
    expect(await act(() => panel.props!.onPick({ provider: "codex", model: "gpt-fast", fast: true }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", fast: true });
    expect(await act(() => panel.props!.onPick({ provider: "codex", model: "gpt-standard" }))).toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-standard" });
    expect(pickedFast()).toBe(false);
  });

  it("a Fast-only choice pins the displayed launcher, model and effort for a cleared main", async () => {
    defaultSetup = { provider: "codex", model: "gpt-fast", effort: "low" };
    await show(undefined); // no launcher/model pick of its own, as after /clear
    await open();
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast", effort: "low" });
    expect(await act(() => panel.props!.onApplyCurrent!({ fast: true }))).toBe(true);
    expect(pickedLauncher(NEW_CONVERSATION)).toBe("codex");
    expect(pickedModel(NEW_CONVERSATION)).toBe("gpt-fast");
    expect(pickedEffort(NEW_CONVERSATION)).toBe("low");
    expect(pickedFast()).toBe(true);
    expect(await act(() => panel.props!.onApplyCurrent!({ fast: true, effort: "high" }))).toBe(true);
    expect(pickedEffort(NEW_CONVERSATION)).toBe("high");
  });

  it("a Fast-only whole-setup pick pins the displayed default launcher, model and effort", async () => {
    defaultSetup = { provider: "codex", model: "gpt-fast", effort: "low" };
    await show(undefined);
    await open();
    const shown = panel.props!.state.current!;
    expect(shown).toEqual({ provider: "codex", model: "gpt-fast", effort: "low" });
    expect(await act(() => panel.props!.onPick({ ...shown, fast: true }))).toBe(true);
    expect(pickedLauncher(NEW_CONVERSATION)).toBe("codex");
    expect(pickedModel(NEW_CONVERSATION)).toBe("gpt-fast");
    expect(pickedEffort(NEW_CONVERSATION)).toBe("low");
    expect(pickedFast()).toBe(true);
    // A speed-only edit keeps the setup; only the explicit Follow default resets every part.
    expect(await act(() => panel.props!.onApplyCurrent!({ fast: false }))).toBe(true);
    expect(pickedLauncher(NEW_CONVERSATION)).toBe("codex");
    expect(pickedModel(NEW_CONVERSATION)).toBe("gpt-fast");
    expect(pickedEffort(NEW_CONVERSATION)).toBe("low");
    expect(pickedFast()).toBe(false);
    await act(async () => strip()!.querySelector("button")!.click());
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual([undefined, undefined, undefined]);
    expect(pickedFast()).toBe(false);
  });

  it("a Recent row for the shown default with Fast is pickable and sends fast: true", async () => {
    defaultSetup = { provider: "codex", model: "gpt-fast", effort: "low" };
    await show(undefined);
    await open();
    const recent = panelSections({
      providers: panel.props!.providers,
      state: panel.props!.state,
      view: { kind: "recent" },
      query: "",
      favorites: [],
      recent: [{ provider: "codex", model: "gpt-fast", effort: "low", fast: true }],
      overrides: {},
    }).flatMap((s) => s.rows).find((r) => r.kind === "recent");
    expect(recent?.provider.unavailable).toBeUndefined();
    expect(recent?.setup).toEqual({ provider: "codex", model: "gpt-fast", effort: "low", fast: true });
    expect(await act(() => panel.props!.onPick(recent!.setup))).toBe(true);
    expect(pickedFast()).toBe(true);
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual(["codex", "gpt-fast", "low"]);
  });

  it("an older hub does not offer a first-turn Fast choice it would ignore", async () => {
    Object.assign(getState(), { features: ["assistant_model_pick", "typed_models"] });
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-fast");
    pickFast(true); // a draft left by a hub that just downgraded
    await show(undefined);
    await open();
    const models = panel.props!.providers.find((p) => p.id === "codex")!.models!;
    expect(models.find((m) => m.id === "gpt-fast")!.fast).not.toBe(true);
    expect(panel.props!.state.current).toEqual({ provider: "codex", model: "gpt-fast" });
    expect(pickedFast()).toBe(false);
  });

  it("the new-conversation page keeps its pick, the effort too, for the start", async () => {
    await show(undefined);
    expect(pill().textContent).toContain("Opus 5.5");
    await open();
    expect(strip()!.textContent).toBe(t("chat-modelpill.header_new", { setup: "claude · Opus 5.5" }));
    // nothing runs yet: every engine can start it
    expect(panel.props!.providers.find((p) => p.id === "codex")!.unavailable).toBeUndefined();
    expect(await act(() => panel.props!.onPick({ provider: "relay", model: "haiku", effort: "low" }))).toBe(true);
    expect([pickedLauncher(NEW_CONVERSATION), pickedModel(NEW_CONVERSATION), pickedEffort(NEW_CONVERSATION)]).toEqual(["relay", "haiku", "low"]);
    expect(calls.some((c) => c.method !== "GET")).toBe(false);
  });
});
