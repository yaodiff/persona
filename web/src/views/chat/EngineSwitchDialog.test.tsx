// @vitest-environment jsdom
// "Switch Agent?" (#5054, after Cindy's): a pick in an Agent's model panel that moves the conversation to the other
// engine asks first, over the panel (held open under it). Its words and order; Switch Agent records the switch (drawn at
// once with the engine it moves to, said in a toast, put back with the hub's words on a refusal); Keep Current Agent,
// Escape and the backdrop change nothing; "Don't remind me again" (ticked and confirmed) skips it from then on; a pick of
// the engine it runs never asks, nor a second pick inside the engine just accepted; picking what runs drops the switch.
// The panel is a stand-in that keeps its props (ui/model-panel has its own tests); the dialog is the real one.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { ENGINE_SWITCH_SKIP_KEY } from "../../lib/engine-switch-ask";
import type { PanelProvider } from "../../lib/model-panel";
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
    return <div data-testid="panel" data-held={p.held ? "true" : undefined} />;
  },
}));
const toast = vi.hoisted(() => vi.fn());
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast }));

const { AgentModelPill } = await import("./ModelPill");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const flush = () => act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); });
const ALL = ["low", "medium", "high", "xhigh", "max"];
const claudeList = { agent: "claude", models: [
  { id: "default", name: "Default", default: true, resolved: "claude-opus-5-5", efforts: ALL },
  { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: ALL },
  { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5", efforts: ["low", "high"] },
] };
const codexList = { agent: "codex", models: [
  { id: "gpt-5.5", name: "GPT-5.5", default: true, efforts: ["low", "medium", "high", "xhigh"], default_effort: "medium" },
  { id: "gpt-5.5-mini", name: "GPT-5.5-Mini", efforts: ["low", "medium", "high"], default_effort: "medium" },
] };

let host: HTMLDivElement;
let root: Root;
let calls: { method: string; url: string; body?: unknown }[];
let modesAnswer: (body: unknown) => Response;
let machine = 0;

const agent = (x: Partial<Session> = {}): Session =>
  ({ machine_id: `mac-${machine}`, session_id: "s1", title: "Ship the release notes", cwd: "/code", status: "idle", state: "idle", agent: "claude", launcher: "gateway", model: "claude-opus-5-5", effort: "high", pane_id: "w1:p1", ...x }) as Session;
const terminalCodex = (x: Partial<Session> = {}) => agent({ agent: "codex", launcher: "cx", model: "gpt-5.5", effort: "medium", ...x });

beforeEach(() => {
  machine++;
  panel.props = null;
  toast.mockClear();
  localStorage.clear();
  Object.assign(getState(), { features: ["launcher_switch", "engine_switch"], sessions: {} });
  calls = [];
  modesAnswer = () => json({ ok: true });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method: init?.method ?? "GET", url, body });
    if (url.includes("/quota")) return json({ unsupported: true });
    if (url.includes("/models")) return json(url.includes("/cx/") ? codexList : claudeList);
    if (/\/launchers(\?|$)/.test(url)) return json({ launchers: [{ name: "claude" }, { name: "gateway" }, { name: "cx", agent: "codex" }] });
    if (url.endsWith("/modes")) return modesAnswer(body);
    return json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

async function show(s: Session) {
  upsertSession(s);
  act(() => root.render(<AgentModelPill session={s} />));
  await flush();
}
const pill = () => host.querySelector<HTMLButtonElement>("button[data-model-pill]");
async function open() {
  await act(async () => pill()!.click());
  await act(async () => vi.dynamicImportSettled());
  await flush();
}
const modes = () => calls.filter((c) => c.url.endsWith("/modes")).map((c) => c.body);
const provider = (id: string) => panel.props!.providers.find((p: PanelProvider) => p.id === id)!;
const dialog = () => document.body.querySelector<HTMLElement>("[data-engine-switch-dialog]");
const buttonNamed = (name: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => b.textContent === name)!;

type Config = { provider: string; model: string; effort?: string };
/** Starts a pick that asks: the dialog shows, and `result` is the pick's once it is answered (`settled`). */
async function asking(config: Config) {
  let result: Promise<boolean> = Promise.resolve(false);
  act(() => void (result = panel.props!.onPick(config)));
  await flush();
  expect(dialog()).not.toBeNull();
  return { result };
}
/** What a pick that was answered (or that never asked) came to, its requests through. */
async function settled(result: Promise<boolean>) {
  let ok = false;
  await act(async () => void (ok = await result));
  await flush();
  return ok;
}
/** A pick that doesn't ask: no dialog, and what it came to. */
async function pick(config: Config) {
  let result: Promise<boolean> = Promise.resolve(false);
  act(() => void (result = panel.props!.onPick(config)));
  await flush();
  expect(dialog()).toBeNull();
  return settled(result);
}

describe("Switch Agent?", () => {
  it("asks in Cindy's words and order, over the panel held open under it", async () => {
    await show(agent());
    await open();
    expect(provider("cx").unavailable).toBeUndefined();
    const { result } = await asking({ provider: "cx", model: "gpt-5.5" });
    const d = dialog()!;
    const modal = d.closest('[role="dialog"]')!;
    expect(modal.getAttribute("aria-modal")).toBe("true");
    expect(document.getElementById(modal.getAttribute("aria-labelledby")!)!.textContent).toBe(t("chat-modelpanel.engine_dialog_title"));
    expect(document.getElementById(modal.getAttribute("aria-describedby")!)!.textContent).toBe(t("chat-modelpanel.engine_dialog_body"));
    expect(d.querySelector("label")!.textContent).toBe(t("chat-modelpanel.engine_dialog_skip"));
    expect((d.querySelector('input[type="checkbox"]') as HTMLInputElement).checked).toBe(false);
    // Switch Agent first, then Keep Current Agent
    expect([...d.querySelectorAll("button")].map((b) => b.textContent)).toEqual([t("chat-modelpanel.engine_dialog_switch"), t("chat-modelpanel.engine_dialog_keep")]);
    expect(host.querySelector('[data-testid="panel"]')!.getAttribute("data-held")).toBe("true");
    expect(modes()).toEqual([]); // nothing sent while it asks
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_keep")).click());
    expect(await settled(result)).toBe(false);
  });

  it("Switch Agent records the switch with the engine it moves to, says so, and the pill shows it waiting", async () => {
    const s = agent();
    await show(s);
    await open();
    const { result } = await asking({ provider: "cx", model: "gpt-5.5-mini", effort: "low" });
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_switch")).click());
    expect(await settled(result)).toBe(true);
    expect(dialog()).toBeNull();
    expect(modes()).toEqual([{ launcher: "cx", from: "gateway", model: "gpt-5.5-mini", effort: "low" }]); // no `agent` on the wire
    expect(getSession(s.machine_id, "s1")?.pending_switch).toMatchObject({ launcher: "cx", model: "gpt-5.5-mini", effort: "low", agent: "codex" });
    expect(toast).toHaveBeenCalledWith(t("chat-modelpanel.switch_scheduled_engine", { agent: "Codex", model: "GPT-5.5 Mini" }), "ok");
    expect(localStorage.getItem(ENGINE_SWITCH_SKIP_KEY)).toBeNull(); // not ticked: it asks next time
    // the pill: a clock, the engine and model it moves to; reopened, the panel warns and stands on what runs now
    act(() => root.render(<AgentModelPill session={getSession(s.machine_id, "s1")!} />));
    await flush();
    expect(pill()!.getAttribute("data-model-pill")).toBe("pending");
    expect(pill()!.querySelector("[data-pill-target]")!.textContent).toBe("cx");
    expect(pill()!.getAttribute("aria-label")).toBe(t("chat-modelpill.aria_pending_effort", { agent: "cx", model: "GPT-5.5 Mini", effort: "Low" }));
    expect(panel.props!.state.note).toBe(t("chat-modelpanel.engine_note"));
    expect(panel.props!.state.current).toMatchObject({ provider: "gateway", model: "opus" });
  });

  it("Keep Current Agent, Escape and the backdrop change nothing", async () => {
    const s = agent();
    await show(s);
    await open();
    for (const close of [
      () => buttonNamed(t("chat-modelpanel.engine_dialog_keep")).click(),
      () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })),
      () => {
        const backdrop = dialog()!.closest('[role="dialog"]')!.parentElement!;
        backdrop.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
      },
    ]) {
      const { result } = await asking({ provider: "cx", model: "gpt-5.5" });
      act(close);
      expect(await settled(result)).toBe(false);
      expect(dialog()).toBeNull();
    }
    expect(modes()).toEqual([]);
    expect(getSession(s.machine_id, "s1")?.pending_switch).toBeUndefined();
    expect(toast).not.toHaveBeenCalled();
  });

  it("\"Don't remind me again\", ticked and confirmed, skips it from then on; ticked and kept, it doesn't", async () => {
    await show(agent());
    await open();
    let { result } = await asking({ provider: "cx", model: "gpt-5.5" });
    act(() => dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_keep")).click());
    expect(await settled(result)).toBe(false);
    expect(localStorage.getItem(ENGINE_SWITCH_SKIP_KEY)).toBeNull();
    // asked again: the box starts unticked
    ({ result } = await asking({ provider: "cx", model: "gpt-5.5" }));
    expect(dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(false);
    act(() => dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_switch")).click());
    expect(await settled(result)).toBe(true);
    expect(localStorage.getItem(ENGINE_SWITCH_SKIP_KEY)).toBe("1");
    // dropped, then the other engine again: no question
    expect(await pick({ provider: "gateway", model: "opus", effort: "high" })).toBe(true);
    expect(getSession(`mac-${machine}`, "s1")?.pending_switch).toBeUndefined();
    expect(await pick({ provider: "cx", model: "gpt-5.5" })).toBe(true);
    expect(modes()).toEqual([{ launcher: "cx", from: "gateway" }, { launcher: "gateway", from: "gateway" }, { launcher: "cx", from: "gateway" }]);
  });

  it("never asks for the engine it runs, nor for a second pick inside the engine just accepted; after a cancel it asks again", async () => {
    await show(agent());
    await open();
    // its own engine: another launcher (#5035) and its own model switch
    expect(await pick({ provider: "claude", model: "haiku" })).toBe(true);
    expect(await pick({ provider: "gateway", model: "opus", effort: "high" })).toBe(true); // drops that switch
    // the other engine: asked, accepted
    let { result } = await asking({ provider: "cx", model: "gpt-5.5" });
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_switch")).click());
    expect(await settled(result)).toBe(true);
    // another model of that engine: only the model changes, no question (Cindy)
    expect(await pick({ provider: "cx", model: "gpt-5.5-mini" })).toBe(true);
    expect(getSession(`mac-${machine}`, "s1")?.pending_switch).toMatchObject({ launcher: "cx", model: "gpt-5.5-mini", agent: "codex" });
    // cancelled by picking what runs, then the other engine again: asked again
    expect(await pick({ provider: "gateway", model: "opus", effort: "high" })).toBe(true);
    expect(getSession(`mac-${machine}`, "s1")?.pending_switch).toBeUndefined();
    ({ result } = await asking({ provider: "cx", model: "gpt-5.5" }));
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_keep")).click());
    expect(await settled(result)).toBe(false);
  });

  it("a refused switch goes back to what waited before, with the hub's words", async () => {
    const s = agent({ pending_switch: { launcher: "claude", model: "haiku", at: "2026-11-12T10:00:00Z" } });
    await show(s);
    await open();
    modesAnswer = () => json({ error: "This computer's Persona can't switch engines yet: update it", code: "switch_unsupported" }, 409);
    const { result } = await asking({ provider: "cx", model: "gpt-5.5" });
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_switch")).click());
    expect(await settled(result)).toBe(false);
    expect(getSession(s.machine_id, "s1")?.pending_switch).toMatchObject({ launcher: "claude", model: "haiku" });
    expect(getSession(s.machine_id, "s1")?.pending_switch?.agent).toBeUndefined();
    expect(toast).toHaveBeenLastCalledWith(expect.stringContaining("can't switch engines yet"), "error");
  });

  it("\"Don't remind me again\" on a switch the hub refuses isn't kept: the next pick asks again", async () => {
    await show(agent());
    await open();
    for (const code of ["switch_stale", "switch_unsupported", "node_outdated"]) {
      modesAnswer = () => json({ error: `refused: ${code}`, code }, 409);
      const { result } = await asking({ provider: "cx", model: "gpt-5.5" });
      act(() => dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
      act(() => buttonNamed(t("chat-modelpanel.engine_dialog_switch")).click());
      expect(await settled(result)).toBe(false);
      expect(localStorage.getItem(ENGINE_SWITCH_SKIP_KEY)).toBeNull();
      expect(getSession(`mac-${machine}`, "s1")?.pending_switch).toBeUndefined();
      // the chat draws the row put back, as ChatView does from the store
      act(() => root.render(<AgentModelPill session={getSession(`mac-${machine}`, "s1")!} />));
      await flush();
    }
    // the hub takes it now: asked again, ticked, and from then on kept
    modesAnswer = () => json({ ok: true });
    const { result } = await asking({ provider: "cx", model: "gpt-5.5" });
    act(() => dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_switch")).click());
    expect(await settled(result)).toBe(true);
    expect(localStorage.getItem(ENGINE_SWITCH_SKIP_KEY)).toBe("1");
  });

  it("a Codex Agent in a terminal: its pill opens on such a hub, picks Claude Code with the question, and drops it from its own row", async () => {
    Object.assign(getState(), { features: ["engine_switch"] });
    await show(terminalCodex());
    expect(pill()).not.toBeNull();
    await open();
    expect(provider("gateway").unavailable).toBeUndefined();
    expect(provider("cx").unavailable).toBe(t("chat-modelpanel.model_fixed", { agent: "Codex" }));
    const { result } = await asking({ provider: "gateway", model: "opus", effort: "high" });
    act(() => buttonNamed(t("chat-modelpanel.engine_dialog_switch")).click());
    expect(await settled(result)).toBe(true);
    expect(getSession(`mac-${machine}`, "s1")?.pending_switch).toMatchObject({ launcher: "gateway", model: "opus", agent: "claude" });
    expect(toast).toHaveBeenCalledWith(t("chat-modelpanel.switch_scheduled_engine", { agent: "Claude Code", model: "Opus 5.5" }), "ok");
    // its own model, the one way back: drops the switch, no question
    expect(await pick({ provider: "cx", model: "gpt-5.5", effort: "medium" })).toBe(true);
    expect(modes()).toEqual([{ launcher: "gateway", from: "cx", model: "opus", effort: "high" }, { launcher: "cx", from: "cx" }]);
    expect(getSession(`mac-${machine}`, "s1")?.pending_switch).toBeUndefined();
  });

  it("without engine switching, a terminal Codex pill still opens to explain unsupported effort", async () => {
    Object.assign(getState(), { features: ["launcher_switch"] });
    await show(agent());
    await open();
    expect(provider("cx").unavailable).toBe(t("chat-modelpanel.other_engine", { from: "Claude Code", to: "Codex" }));
    expect(await pick({ provider: "cx", model: "gpt-5.5" })).toBe(false);
    expect(modes()).toEqual([]);
    act(() => root.render(<></>));
    await show(terminalCodex({ session_id: "s2" }));
    expect(pill()).not.toBeNull();
    await open();
    expect(provider("cx").models?.find((m) => m.id === "gpt-5.5")?.effortUnavailable).toBe(t("chat-modelpanel.effort_terminal"));
    expect(modes()).toEqual([]);
  });

  it.each([
    { name: "CLI alias", session: { agent: "codex-cli" }, reason: "effort_terminal" },
    { name: "history without an agent ID", session: { agent: undefined, source_kind: "codex_history", capabilities: { send: false } }, reason: "effort_read_only" },
  ])("a Codex $name keeps the actual effort and an explanation without engine switching", async ({ session, reason }) => {
    Object.assign(getState(), { features: [] });
    await show(terminalCodex({ ...session, effort: "max" }));
    expect(pill()).not.toBeNull();
    await open();
    expect(panel.props!.state.current?.effort).toBe("max");
    expect(provider("cx").models?.find((m) => m.id === "gpt-5.5")?.effortUnavailable).toBe(t(`chat-modelpanel.${reason}`));
    expect(modes()).toEqual([]);
  });
});
