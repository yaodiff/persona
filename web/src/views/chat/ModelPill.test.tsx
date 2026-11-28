// @vitest-environment jsdom
// The composer's model pill (#5032, after Cindy's): what it shows in each state and at each composer width, and how an
// Agent's pill drives the model panel: the providers and state it hands over, a pick applied model first then effort
// (a typed model ID too, #1644), busy and locked, Codex's effort and Fast, `/model` opening it, and the lists shown at
// once and read again behind them. The panel itself has its own tests (ui/model-panel); here it is a stand-in that keeps
// the props it was given.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { agentName } from "../../lib/agent-kind";
import type { PanelProvider } from "../../lib/model-panel";
import { requestModelPicker } from "../../lib/model-switch";
import { readModels, sessionSwitches } from "../../lib/session-models";
import { setToken } from "../../lib/api";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
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
    return <div data-testid="panel">{p.currentExtra}</div>;
  },
}));
const toast = vi.hoisted(() => vi.fn());
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast }));

const { AgentModelPill, ModelPill, pillShortName } = await import("./ModelPill");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const opus = { engine: "claude", provider: "gateway", model: "Opus 5.5", effort: "high" };
const markup = (node: React.ReactElement) => renderToStaticMarkup(node);
const classOf = (html: string, attr: string) => new RegExp(`<[^>]*${attr}[^>]*class="([^"]*)"`).exec(html)?.[1] ?? new RegExp(`<[^>]*class="([^"]*)"[^>]*${attr}`).exec(html)?.[1] ?? "";

describe("the pill's states", () => {
  it("normal: the engine's mark first, then model, effort, chevron; the tooltip and name say it all", () => {
    const html = markup(<ModelPill current={opus} onOpen={() => {}} />);
    expect(html).toMatch(/^<button type="button" data-model-pill="ready" aria-haspopup="listbox" aria-expanded="false"/);
    expect(html).toContain(`aria-label="${t("chat-modelpill.aria_effort", { model: "Opus 5.5", effort: "High" })}"`);
    expect(html).toContain('title="Claude Code · Opus 5.5 · gateway · High"');
    // in this order: the engine's mark (#5497: no letter tile, drawn once), the name, the effort, then the chevron
    const text = html.replace(/<[^>]+>/g, "|").split("|").filter(Boolean);
    expect(text).toEqual(["Opus 5.5", "High"]);
    expect(html.match(/data-agent-mark=/g)).toHaveLength(1);
    expect(html.indexOf('data-agent-mark="claude"')).toBeLessThan(html.indexOf('<span title="Opus 5.5"'));
    expect(html.indexOf("data-pill-effort")).toBeGreaterThan(html.indexOf('<span title="Opus 5.5"'));
    expect(html).not.toContain("·</span>");
    // bare at rest, a capsule on hover and while open
    expect(html).toContain("border-transparent");
    expect(html).toContain("hover:border-line-strong");
    expect(html).toContain("aria-expanded:bg-hover");
  });

  it.each([
    ["agy", "Gemini 4 Neon (Medium)", "medium", "Gemini 4 Neon", "Medium"],
    ["codex", "GPT-5.5 (Minimal) [1m]", "xhigh", "GPT-5.5 [1m]", "XHigh"],
    ["claude", "Opus 5.5 (1M context) (Max)", "max", "Opus 5.5 (1M context)", "Max"],
    ["opencode", "Example (Ultra)", "ultra", "Example", "Ultra"],
  ])("renders a single tier for %s and preserves its context size", (engine, model, effort, name, label) => {
    const host = document.createElement("div");
    host.innerHTML = markup(<ModelPill current={{ engine, provider: engine, model, effort }} onOpen={() => {}} />);
    const button = host.querySelector("button")!;
    const engineTitle = agentName(engine);
    expect(button.getAttribute("aria-label")).toBe(t("chat-modelpill.aria_effort", { model: name, effort: label }));
    expect(button.title).toBe(engineTitle === engine ? `${engineTitle} · ${name} · ${label}` : `${engineTitle} · ${name} · ${engine} · ${label}`);
    expect(button.querySelector("[data-pill-effort]")?.textContent).toBe(label);
    expect(button.textContent).toContain(name);
    if (pillShortName(name) !== name) expect(button.querySelector("[data-pill-short]")?.textContent).toBe(pillShortName(name));
    expect(button.textContent).not.toMatch(/\((Medium|Minimal|Max|Ultra)\)/);
  });

  it("omits the provider segment in the tooltip when it matches the engine name, while keeping distinct launchers", () => {
    const builtin = markup(<ModelPill current={{ engine: "claude", provider: "Claude Code", model: "Opus 5.5", effort: "high" }} onOpen={() => {}} />);
    expect(builtin).toContain('title="Claude Code · Opus 5.5 · High"');
    const launcher = markup(<ModelPill current={{ engine: "claude", provider: "claude", launcherName: "Claude Code", model: "Opus 5.5", effort: "high" }} onOpen={() => {}} />);
    expect(launcher).toContain('title="Claude Code · Opus 5.5 · High"');
    const desktop = markup(<ModelPill current={{ engine: "claude", provider: "Claude Desktop", model: "Opus 5.5", effort: "high" }} onOpen={() => {}} />);
    expect(desktop).toContain('title="Claude Code · Opus 5.5 · Claude Desktop · High"');
  });

  it("takes the composer card's widths: its words' width under 600, a step smaller under 520, the mark and chevron alone under 420", () => {
    const html = markup(<ModelPill current={opus} onOpen={() => {}} />);
    const root = /^<button[^>]*class="([^"]*)"/.exec(html)![1];
    expect(root).toContain("max-w-[min(320px,100%)]");
    expect(root).toContain("min-w-[72px]");
    // no fixed width while narrow: a fixed 148 px cut "Opus 5.5" to "Opus …" beside free room (audit item 50)
    expect(root).not.toMatch(/@max-\[600px\]\/composer:w-\[/);
    expect(root).toContain("shrink");
    expect(root).toContain("@max-[520px]/composer:text-[12px]");
    expect(root).toContain("@max-[420px]/composer:w-[64px]");
    const name = /<span title="Opus 5.5" class="([^"]*)"/.exec(html)![1];
    expect(name).toContain("@max-[600px]/composer:max-w-[108px]");
    expect(name).toContain("@max-[420px]/composer:hidden");
    expect(name).toContain("shrink-[1000]"); // the name gives way before the effort
    // the engine's mark stays under 420 px, with the chevron
    expect(classOf(html, 'data-pill-engine="claude"')).not.toContain("@max-[420px]/composer:hidden");
    // narrow, the effort may truncate (the engine and effort stay; the name gives way first)
    expect(classOf(html, "data-pill-effort")).toContain("@max-[600px]/composer:truncate");
  });

  it("narrow, a name drops its bracketed qualifier instead of being cut; the tooltip keeps it all (audit item 50)", () => {
    expect(pillShortName("Default (recommended)")).toBe("Default");
    expect(pillShortName("GPT-5 (beta)")).toBe("GPT-5");
    expect(pillShortName("Opus 5.5")).toBe("Opus 5.5");
    expect(pillShortName("(beta)")).toBe("(beta)");
    // a context size names another model: it stays, short, so the 1M model never reads as the standard one
    expect(pillShortName("Opus 5.5 (1M context)")).toBe("Opus 5.5 1M");
    expect(pillShortName("Sonnet 4.5 [1m]")).toBe("Sonnet 4.5 1M");
    expect(pillShortName("Sonnet 4.5（200k 上下文）")).toBe("Sonnet 4.5 200K");
    expect(pillShortName("Sonnet 4.5 [1m]")).not.toBe(pillShortName("Sonnet 4.5"));
    const html = markup(<ModelPill current={{ ...opus, model: "Opus 5.5 (1M context)" }} onOpen={() => {}} />);
    const name = /<span title="Opus 5.5 \(1M context\)" class="([^"]*)"/.exec(html)![1];
    expect(name).toContain("@max-[420px]/composer:hidden");
    const full = /<span class="([^"]*)">Opus 5.5 \(1M context\)<\/span>/.exec(html)![1];
    expect(full).toContain("@max-[600px]/composer:hidden");
    const short = classOf(html, "data-pill-short");
    expect(short).toMatch(/(^| )hidden( |$)/);
    expect(short).toContain("@max-[600px]/composer:inline");
    // a name with nothing to drop is drawn once
    expect(markup(<ModelPill current={opus} onOpen={() => {}} />)).not.toContain("data-pill-short");
  });

  it("one of Paseo's providers: its mark and its label, never the engine's id (#8142)", () => {
    const grok = markup(<ModelPill current={{ engine: "grok", engineLabel: "Grok", provider: "grok", model: "Grok 4" }} onOpen={() => {}} />);
    expect(grok).toContain('title="Grok · Grok 4 · grok"');
    expect(grok).toMatch(/data-pill-engine="grok"[^>]*>.*data-agent-mark="grok"/);
    expect(grok).not.toMatch(/paseo/i);
    const cursor = markup(<ModelPill current={{ engine: "cursor", engineLabel: "Cursor", provider: "cursor", model: "Auto" }} onOpen={() => {}} />);
    expect(cursor).toContain('data-agent-mark="cursor"');
    expect(cursor).toContain('title="Cursor · Auto · cursor"');
  });

  it("Fast: a filled bolt in the neutral colour, and Fast in the tooltip", () => {
    const html = markup(<ModelPill current={{ ...opus, engine: "codex", model: "5.5", speed: "fast" }} onOpen={() => {}} />);
    expect(html).toContain("fill-current");
    expect(html).toContain(`title="Codex · 5.5 · gateway · High · ${t("chat-sessiondialogs.speed_fast")}"`);
  });

  it("loading or failed, the pill keeps its 64 px under 420 px: only a waiting switch makes it grow there", () => {
    for (const html of [
      markup(<ModelPill current={{ engine: "claude", provider: "gateway" }} loading onOpen={() => {}} />),
      markup(<ModelPill current={{ engine: "claude", provider: "gateway" }} failed onOpen={() => {}} />),
    ]) {
      const root = /^<button[^>]*class="([^"]*)"/.exec(html)![1];
      expect(root).toContain("@max-[420px]/composer:w-[64px]");
      expect(root).not.toContain("@max-[420px]/composer:w-auto");
    }
  });

  it("loading before a model is known: a spinner and Loading model…", () => {
    const html = markup(<ModelPill current={{ engine: "claude", provider: "gateway" }} loading onOpen={() => {}} />);
    expect(html).toContain('data-model-pill="loading"');
    expect(html).toContain("border-t-ink-2"); // the spinner
    expect(html).toContain(t("chat-modelpill.loading"));
    // not known and not loading: what runs when no model is set
    expect(markup(<ModelPill current={{ engine: "claude", provider: "gateway" }} onOpen={() => {}} />)).toContain(t("chat-modelpill.default_model"));
  });

  it("the list failed: a warning in the error colour, said in the tooltip", () => {
    const none = markup(<ModelPill current={{ engine: "claude", provider: "gateway" }} failed onOpen={() => {}} />);
    expect(none).toContain('data-model-pill="failed"');
    expect(none).toContain(t("chat-modelpill.failed"));
    expect(none).toContain("text-red");
    const known = markup(<ModelPill current={opus} failed onOpen={() => {}} />);
    expect(known).toContain(`title="Claude Code · Opus 5.5 · gateway · High · ${t("chat-modelpill.failed")}"`);
    expect(known.match(/text-red/g)!.length).toBe(2); // the mark and the warning after the effort
  });

  it("pending (#5035): a clock first, the next engine named, both setups in the tooltip", () => {
    const next = { engine: "codex", provider: "cx", model: "5.5 (Low)", effort: "low" };
    const html = markup(<ModelPill current={{ ...opus, model: "Opus 5.5 (High)" }} pending={next} onOpen={() => {}} />);
    expect(html).toContain('data-model-pill="pending"');
    expect(html.indexOf("<svg")).toBeLessThan(html.indexOf('data-agent-mark="codex"')); // the clock before the target's mark
    // the target's engine before the name, gone when the pill is narrow
    expect(html).toMatch(/data-pill-target[^>]*>Codex<\/span>/);
    expect(classOf(html, "data-pill-target")).toContain("@max-[600px]/composer:hidden");
    expect(html).toContain('data-pill-engine="codex"');
    expect(html).toContain(`title="${t("chat-modelpill.current_and_next", { current: "Claude Code · Opus 5.5 · gateway · High", next: "Codex · 5.5 · cx · Low" })}"`);
    expect(html).toContain(`aria-label="${t("chat-modelpill.aria_pending_effort", { agent: "Codex", model: "5.5", effort: "Low" })}"`);
  });

  it("pending on another launcher of the same engine: that launcher named before the model", () => {
    const next = { engine: "claude", provider: "work", model: "GLM 5.3", effort: "high" };
    const html = markup(<ModelPill current={opus} pending={next} onOpen={() => {}} />);
    expect(html).toMatch(/data-pill-target[^>]*>work<\/span>/);
    expect(html).toContain('data-agent-mark="claude"'); // the target's engine
    // under 420 px the clock, the mark and the chevron: wider than the plain pill's 64 px
    expect(html).toContain("@max-[420px]/composer:w-auto");
    expect(html).not.toContain("@max-[420px]/composer:w-[64px]");
    expect(html).toContain(`title="${t("chat-modelpill.current_and_next", { current: "Claude Code · Opus 5.5 · gateway · High", next: "Claude Code · GLM 5.3 · work · High" })}"`);
    expect(html).toContain(`aria-label="${t("chat-modelpill.aria_pending_effort", { agent: "work", model: "GLM 5.3", effort: "High" })}"`);
  });

  it("disabled while a switch is on its way", () => {
    const html = markup(<ModelPill current={opus} disabled onOpen={() => {}} />);
    expect(html).toContain("disabled=\"\"");
    expect(html).toContain("pointer-events-none");
    expect(html).toContain("opacity-50");
  });

  it("a label, not a button, where nothing can be switched", () => {
    const html = markup(<ModelPill current={opus} />);
    expect(html).toMatch(/^<span data-model-pill="label" title="Claude Code · Opus 5.5 · gateway · High"/);
    expect(html).not.toContain("<button");
    expect(html).toContain(`<span class="sr-only">${t("chat-modelpill.aria_effort", { model: "Opus 5.5", effort: "High" })}</span>`);
    expect(html.match(/<svg/g)).toBeNull(); // no chevron (Claude's mark is an image)
  });

});

// ── an Agent's pill and its panel ──
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const flush = () => act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); });
const ALL = ["low", "medium", "high", "xhigh", "max"];
const claudeList = () => ({ agent: "claude", models: [
  { id: "default", name: "Default", default: true, resolved: "claude-opus-5-5", efforts: ALL },
  { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: ALL },
  { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5", efforts: ["low", "high"] },
  { id: "vendor/model-x", name: "vendor/model-x", custom: true },
] });
let host: HTMLDivElement;
let root: Root;
let calls: { method: string; url: string; body?: unknown }[];
let listAnswer: (url: string) => Response;
let modesAnswer: (body: unknown) => Response;
let forgetAnswer: () => Response;
let quotaAnswer: () => Response;
// lists answer once this settles (a test that wants to see what shows while one is read holds it)
let hold: Promise<unknown>;
let machine = 0;
let namedLauncher = false;

const agent = (x: Partial<Session> = {}): Session =>
  ({ machine_id: `mac-${machine}`, session_id: "s1", title: "整理发布说明", cwd: "/code", status: "idle", state: "idle", agent: "claude", launcher: "gateway", model: "claude-opus-5-5", effort: "high", pane_id: "p1", ...x }) as Session;

beforeEach(() => {
  resetManagerChoice();
  machine++;
  namedLauncher = false;
  panel.props = null;
  toast.mockClear();
  Object.assign(getState(), { features: [], sessions: {}, launches: {} });
  calls = [];
  listAnswer = (url) => (url.includes("/cx/") ? json({ agent: "codex", models: [{ id: "gpt-5.5", name: "gpt-5.5", efforts: ["low", "high"] }] }) : json(claudeList()));
  modesAnswer = () => json({ ok: true });
  forgetAnswer = () => json({ ok: true });
  quotaAnswer = () => json({ unsupported: true });
  hold = Promise.resolve();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method: init?.method ?? "GET", url, body });
    if (url.includes("/models/custom")) return forgetAnswer();
    if (url.includes("/quota")) return quotaAnswer();
    if (url.includes("/models")) return hold.then(() => listAnswer(url));
    if (/\/launchers(\?|$)/.test(url)) return json({ launchers: [
      { name: "claude" }, { name: "gateway" }, { name: "cx", agent: "codex" },
      ...(namedLauncher ? [{ name: "team-relay", display_name: "Team Relay" }] : []),
    ] });
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
  resetManagerChoice();
  setToken("");
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

describe("an Agent's pill", () => {
  it("names same-model launchers exactly as the panel, without changing the model or effort", async () => {
    await show(agent());
    expect(pill()!.querySelector("[data-pill-target]")?.textContent).toBe("gateway");
    expect(pill()!.getAttribute("aria-label")).toContain("gateway · Opus 5.5");
    await open();
    expect(provider("gateway").name).toBe(pill()!.querySelector("[data-pill-target]")?.textContent);
    await show(agent({ launcher: "claude" }));
    expect(pill()!.querySelector("[data-pill-target]")?.textContent).toBe("Claude Code");
    expect(pill()!.textContent).toContain("Opus 5.5");
    expect(pill()!.querySelector("[data-pill-effort]")?.textContent).toBe("High");
    expect(modes()).toEqual([]);
  });

  it("uses the launcher's display name in the pill, tooltip and panel", async () => {
    namedLauncher = true;
    await show(agent({ launcher: "team-relay" }));
    expect(pill()!.querySelector("[data-pill-target]")?.textContent).toBe("Team Relay");
    expect(pill()!.title).toContain("Team Relay");
    expect(pill()!.getAttribute("aria-label")).toContain("Team Relay");
    await open();
    expect(provider("team-relay").name).toBe("Team Relay");
    await show(agent({ launcher: "team-relay", pending_switch: { launcher: "gateway", model: "haiku" } }));
    expect(pill()!.title).toContain("Team Relay");
    expect(pill()!.querySelector("[data-pill-target]")?.textContent).toBe("gateway");
  });

  it("keeps a model-only label when the launcher is unknown, but names a queued target", async () => {
    await show(agent({ launcher: undefined }));
    expect(pill()!.querySelector("[data-pill-target]")).toBeNull();
    expect(pill()!.getAttribute("aria-label")).not.toContain("claude ·");
    await show(agent({ pending_switch: { launcher: "cx", agent: "codex", model: "gpt-5.5" } }));
    expect(pill()!.querySelector("[data-pill-target]")?.textContent).toBe("cx");
    expect(pill()!.dataset.modelPill).toBe("pending");
    expect(pill()!.title).toContain("gateway");
    expect(pill()!.title).toContain("cx");
    expect(pill()!.getAttribute("aria-label")).toContain("cx");
    expect(pill()!.querySelector("[data-pill-engine]")?.getAttribute("data-pill-engine")).toBe("codex");
    expect(modes()).toEqual([]);
  });

  it("reloads when the default manager becomes known or the credential owner changes", async () => {
    await show(agent());
    expect(calls.filter((c) => c.url.includes("/models"))).toHaveLength(1);
    listAnswer = () => json({ agent: "claude", models: [{ id: "opus", resolved: "claude-opus-5-5", name: "Default manager model" }] });
    await act(async () => setRosterDefault("known-default"));
    await flush();
    expect(calls.filter((c) => c.url.includes("/models"))).toHaveLength(2);
    expect(pill()!.textContent).toContain("Default manager model");
    listAnswer = () => json({ agent: "claude", models: [{ id: "opus", resolved: "claude-opus-5-5", name: "Replacement owner model" }] });
    await act(async () => setToken("replacement-review-owner"));
    await flush();
    expect(calls.filter((c) => c.url.includes("/models"))).toHaveLength(3);
    expect(pill()!.textContent).toContain("Replacement owner model");
  });

  it("reads its own list once for the model's name, and opens the panel with the computer's launchers", async () => {
    await show(agent());
    expect(pill()!.textContent).toContain("Opus 5.5");
    expect(calls.filter((c) => c.url.includes("/models")).map((c) => c.url.split("/launchers/")[1])).toEqual(["gateway/models"]);
    await open();
    expect(host.querySelector('[data-testid="panel"]')).not.toBeNull();
    expect(pill()!.getAttribute("aria-expanded")).toBe("true");
    expect(panel.props!.scope).toBe("agent");
    expect(panel.props!.anchor).toBe(pill());
    expect(panel.props!.providers.map((p) => p.id)).toEqual(["claude", "gateway", "cx"]);
    expect(panel.props!.state).toMatchObject({ current: { provider: "gateway", model: "opus", effort: "high" }, recommended: ["gateway"] });
    expect(provider("cx").unavailable).toBe(t("chat-modelpanel.other_engine", { from: "Claude Code", to: "Codex" }));
    // a second click closes it
    await act(async () => pill()!.click());
    expect(host.querySelector('[data-testid="panel"]')).toBeNull();
  });

  it("opens an existing AGY companion session with the companion catalog alongside a local CLI", async () => {
    const session = agent({
      agent: "agy", launcher: "agy", model: "companion-model", pane_id: undefined,
      source_kind: "runtime", source_instance_id: "agy", capabilities: { read: true, send: true, model: true },
    });
    listAnswer = (url) => {
      const request = new URL(url, "http://localhost");
      if (!request.pathname.endsWith("/launchers/agy/models")) return json(claudeList());
      const models = request.searchParams.get("host") === "agy"
        ? [{ id: "companion-model", name: "Companion Model" }, { id: "companion-other", name: "Another Companion Model" }]
        : [{ id: "local-model", name: "Local Pane Model" }];
      return json({ agent: "agy", models });
    };
    await readModels(session.machine_id, "agy"); // New Agent has already read the default herdr catalog.
    calls.length = 0;

    await show(session);
    expect(pill()!.textContent).toContain("Companion Model");
    await open();
    expect(provider("agy").models?.map((m) => m.id)).toEqual(["companion-model", "companion-other"]);
    await act(async () => panel.props!.onReload!("agy"));
    await flush();
    expect(calls.filter((c) => c.url.includes("/launchers/agy/models")).map((c) => c.url.split("?")[1]))
      .toEqual(["host=agy", "host=agy", "refresh=1&host=agy"]);
  });

  it("reads each provider's cached quota for this computer, without forcing a refresh", async () => {
    quotaAnswer = () => json({ machine: `mac-${machine}`, checked_at: "2026-11-12T00:00:00Z", quota: { gateway: "example", pools: [{ name: "a", windows: [{ label: "每周", used_ratio: 0.77 }] }] } });
    await show(agent());
    await open();
    for (const name of ["claude", "gateway", "cx"]) {
      expect(calls.some((c) => c.url.includes(`/launchers/${name}/quota?machine=mac-${machine}`) && !c.url.includes("refresh="))).toBe(true);
      expect(panel.props!.quotas?.[name]?.quota?.pools?.[0]?.windows?.[0]).toMatchObject({ label: "每周", used_ratio: 0.77 });
    }
  });

  it("applies a pick the model first, then the effort, and stops at a failure", async () => {
    await show(agent());
    await open();
    expect(await act(() => panel.props!.onPick({ provider: "gateway", model: "haiku", effort: "low" }))).toBe(true);
    expect(modes()).toEqual([{ model: "haiku" }, { effort: "low" }]);
    calls.length = 0;
    modesAnswer = () => json({ error: "dialog_open" }, 409);
    expect(await act(() => panel.props!.onPick({ provider: "gateway", model: "haiku", effort: "max" }))).toBe(false);
    expect(modes()).toEqual([{ model: "haiku" }]);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("dialog_open"), "error");
    // another launcher can't be applied here: nothing is sent
    calls.length = 0;
    expect(await act(() => panel.props!.onPick({ provider: "claude", model: "haiku" }))).toBe(false);
    expect(modes()).toEqual([]);
  });

  it("sends a model ID typed by hand as it is (#1644), and Forget puts a typed ID back with why when the hub says no", async () => {
    await show(agent());
    await open();
    expect(provider("gateway").typable).toBe(true);
    expect(await act(() => panel.props!.onPick({ provider: "gateway", model: "~vendor/model-latest" }))).toBe(true);
    expect(modes()).toEqual([{ model: "~vendor/model-latest" }]);
    forgetAnswer = () => json({ error: "只读设备不能改" }, 403);
    act(() => panel.props!.onForget!("gateway", "vendor/model-x"));
    expect(provider("gateway").models!.some((m) => m.id === "vendor/model-x")).toBe(false);
    await flush();
    expect(provider("gateway").models!.some((m) => m.id === "vendor/model-x")).toBe(true);
    expect(toast).toHaveBeenCalledWith(expect.stringMatching(/无法移除「vendor\/model-x」：.*只读设备不能改/), "error");
  });

  it("is busy while the agent works, and says why a launcher that fixes its model can't take a pick", async () => {
    listAnswer = () => json({ ...claudeList(), locked: "--model" });
    await show(agent({ status: "working", state: "working" }));
    await open();
    expect(panel.props!.state.busy).toBe(t("composer.busy"));
    expect(provider("gateway").locked).toBe(t("composer.locked"));
    expect(provider("gateway").typable).toBeUndefined();
  });

  it("shows the list from before at once and reads it again behind it", async () => {
    await show(agent());
    await open();
    act(() => root.render(<></>));
    let release = () => {};
    hold = new Promise<void>((r) => (release = r));
    listAnswer = (url) => json(url.includes("/gateway/") ? { agent: "claude", models: [{ id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5" }, { id: "vendor/model-new", name: "vendor/model-new", custom: true }] } : claudeList());
    act(() => root.render(<AgentModelPill session={agent()} />));
    await act(async () => pill()!.click());
    await act(async () => vi.dynamicImportSettled());
    // at once: the last list
    expect(provider("gateway").models!.map((m) => m.id)).toContain("vendor/model-x");
    release();
    await flush();
    expect(calls.filter((c) => c.url.includes(`/machines/mac-${machine}/launchers/gateway/models`)).length).toBe(3); // the pill's, then each opening
    expect(provider("gateway").models!.map((m) => m.id)).toEqual(["opus", "vendor/model-new"]);
  });

  it("`/model` Enter in the composer opens it where the agent switches models", async () => {
    await show(agent());
    let opened = false;
    act(() => void (opened = requestModelPicker(`mac-${machine}/s1`)));
    expect(opened).toBe(true);
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(host.querySelector('[data-testid="panel"]')).not.toBeNull();
  });

  it("a Codex thread applies effort and speed through the flyout and shows speed after the push", async () => {
    const codex = agent({ agent: "codex", source_kind: "codex_app_server", launcher: "cx", capabilities: { send: true }, model: "gpt-5.5", efforts: ["low", "medium", "high"], service_tiers: [{ id: "default", name: "Standard" }, { id: "priority", name: "Fast" }, { id: "ultrafast", name: "Ultrafast" }] });
    await show(codex);
    expect(requestModelPicker(`mac-${machine}/s1`)).toBe(false); // no model switch: `/model` goes as text
    await open();
    expect(provider("cx").unavailable).toBe(t("chat-modelpanel.model_fixed", { agent: "Codex" }));
    expect(panel.props!.state.recommended).toEqual([]);
    expect(provider("cx").models![0]).toMatchObject({ id: "gpt-5.5", efforts: ["low", "medium", "high"], speeds: [{ id: "standard" }, { id: "fast" }, { id: "ultrafast" }] });
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "fast" }))).toBe(true);
    expect(modes()).toEqual([{ service_tier: "priority" }]);
    expect(getSession(codex.machine_id, "s1")?.service_tier).toBeUndefined();
    expect(await act(() => panel.props!.onApplyCurrent!({ effort: "low" }))).toBe(true);
    expect(modes()).toEqual([{ service_tier: "priority" }, { effort: "low" }]);
    const fast = { ...getSession(codex.machine_id, "s1")!, service_tier: "priority" };
    upsertSession(fast);
    act(() => root.render(<AgentModelPill session={fast} />));
    await flush();
    expect(pill()!.querySelectorAll('[data-pill-speed="fast"] svg.fill-current path')).toHaveLength(1);
    expect(await act(() => panel.props!.onApplyCurrent!({ speed: "ultrafast" }))).toBe(true);
    expect(modes().at(-1)).toEqual({ service_tier: "ultrafast" });
    expect(pill()!.querySelector('[data-pill-speed="ultrafast"]')).toBeNull();
    const ultra = { ...fast, service_tier: "ultrafast" };
    upsertSession(ultra);
    act(() => root.render(<AgentModelPill session={ultra} />));
    await flush();
    expect(pill()!.querySelectorAll('[data-pill-speed="ultrafast"] svg.fill-current path')).toHaveLength(2);
    expect(pill()!.title).toContain(` · ${t("ui-modelpanel.speed_ultrafast")}`);
    expect(pill()!.getAttribute("aria-label")).toContain(` · ${t("ui-modelpanel.speed_ultrafast")}`);
    calls.length = 0;
    await act(() => panel.props!.onApplyCurrent!({ speed: "standard" }));
    expect(modes()).toEqual([{ service_tier: "default" }]);
    act(() => root.render(<AgentModelPill session={{ ...ultra, service_tier: "default" }} />));
    expect(pill()!.querySelector("[data-pill-speed]")).toBeNull();
  });

  it("a Codex terminal Agent that switches neither model nor effort: its mark first and once, and the pill opens the panel that says why (#5497, #6962)", async () => {
    const codex = agent({ agent: "codex", launcher: "cx", model: "gpt-5.5", effort: undefined });
    expect(sessionSwitches(codex)).toEqual({ model: false, effort: false });
    await show(codex);
    const button = pill()!;
    expect(button).not.toBeNull(); // a button, not a label: Codex explains
    expect(button.querySelectorAll("[data-agent-mark]")).toHaveLength(1);
    expect(button.querySelector("[data-agent-mark]")!.getAttribute("data-agent-mark")).toBe("codex");
    expect(button.querySelector("[data-pill-engine]")).toBe(button.firstElementChild!.firstElementChild);
    expect(button.querySelector('[data-agent-mark="letter"], [data-provider-mark]')).toBeNull();
    await open();
    expect(host.querySelector('[data-testid="panel"]')).not.toBeNull();
    expect(provider("cx").unavailable).toBeTruthy();
  });

  it("switches to another launcher with the next message on a hub that does it: pending at once, said, put back on a refusal (#5035)", async () => {
    Object.assign(getState(), { features: ["launcher_switch"] });
    const s = agent();
    await show(s);
    await open();
    expect(provider("claude").unavailable).toBeUndefined();
    expect(await act(() => panel.props!.onPick({ provider: "claude", model: "haiku", effort: "low" }))).toBe(true);
    expect(modes()).toEqual([{ launcher: "claude", from: "gateway", model: "haiku", effort: "low" }]);
    expect(getSession(s.machine_id, "s1")?.pending_switch).toMatchObject({ launcher: "claude", model: "haiku", effort: "low" });
    expect(toast).toHaveBeenCalledWith(t("chat-modelpanel.switch_scheduled", { target: "claude · Haiku 5.5" }), "ok");
    // the pill shows it waiting
    act(() => root.render(<AgentModelPill session={getSession(s.machine_id, "s1")!} />));
    await flush();
    expect(pill()!.getAttribute("data-model-pill")).toBe("pending");
    expect(pill()!.getAttribute("title")).toBe(t("chat-modelpill.current_and_next", { current: "Claude Code · Opus 5.5 · gateway · High", next: "Claude Code · Haiku 5.5 · Low" }));
    // a refusal puts it back as it was, with the hub's words
    calls.length = 0;
    modesAnswer = () => json({ error: "这个启动器用的是另一个配置目录", code: "switch_config_dir" }, 409);
    expect(await act(() => panel.props!.onPick({ provider: "claude", model: "opus" }))).toBe(false);
    expect(getSession(s.machine_id, "s1")?.pending_switch).toMatchObject({ launcher: "claude", model: "haiku" });
    expect(toast).toHaveBeenLastCalledWith(expect.stringContaining("这个启动器用的是另一个配置目录"), "error");
  });

  it("a pending switch shows on the pill from the session, its note in the panel, and the session's own launcher drops it", async () => {
    Object.assign(getState(), { features: ["launcher_switch"] });
    const s = agent({ pending_switch: { launcher: "claude", model: "haiku", at: "2026-11-12T10:00:00Z" } });
    await show(s);
    expect(calls.some((c) => c.url.includes("/launchers/claude/models"))).toBe(true); // the target's list names its model
    expect(pill()!.getAttribute("data-model-pill")).toBe("pending");
    expect(pill()!.textContent).toContain("Haiku 5.5");
    await open();
    expect(panel.props!.state.note).toBe(t("chat-modelpanel.pending_note", { provider: "claude" }));
    expect(panel.props!.state.pending).toMatchObject({ provider: "claude", model: "haiku" });
    expect(await act(() => panel.props!.onPick({ provider: "gateway", model: "opus", effort: "high" }))).toBe(true);
    expect(modes()).toEqual([{ launcher: "gateway", from: "gateway" }]);
    expect(getSession(s.machine_id, "s1")?.pending_switch).toBeUndefined();
  });

  it("keeps a launch-only advertised model display-only so picks and pending-switch cleanup still target the real session", async () => {
    Object.assign(getState(), { features: ["launcher_switch"] });
    const s = agent({ model: undefined, effort: "high", pending_switch: { launcher: "claude", model: "haiku", at: "2026-11-12T10:00:00Z" } });
    Object.assign(getState(), {
      launches: {
        "local-1": {
          id: "local-1",
          machineId: s.machine_id,
          cwd: s.cwd,
          launcher: "",
          displayLauncher: "gateway",
          displayEngine: "claude",
          displayModel: "opus",
          text: "",
          attachments: [],
          phase: "ready",
          session: s,
          uploaded: {},
          startOpId: "op-1",
          startPromptOpId: "op-2",
          messageId: "msg-1",
        },
      },
    });
    await show(s);
    await open();

    // Presentation reflects the advertised launch model while session.model is still unset.
    expect(panel.props!.state.current).toMatchObject({ provider: "gateway", model: "opus", effort: "high" });

    // Updating effort on the real session does not persist the display-only model into the store.
    expect(await act(() => panel.props!.onApplyCurrent!({ effort: "low" }))).toBe(true);
    expect(modes()).toEqual([{ effort: "low" }]);
    expect(getSession(s.machine_id, "s1")?.model).toBeUndefined();

    // Selecting the advertised model before the session reports a real model still clears pending_switch and sends the model request.
    calls.length = 0;
    const updated = getSession(s.machine_id, "s1")!;
    act(() => root.render(<AgentModelPill session={updated} />));
    await flush();
    expect(await act(() => panel.props!.onPick({ provider: "gateway", model: "opus", effort: "low" }))).toBe(true);
    expect(modes()).toEqual([{ launcher: "gateway", from: "gateway" }, { model: "opus" }]);
    expect(getSession(s.machine_id, "s1")?.pending_switch).toBeUndefined();
    expect(getSession(s.machine_id, "s1")?.model).toBe("opus");
  });

  it("sends the model request when picking the launch-advertised model without a pending switch, and rolls back to undefined on failure", async () => {
    const s = agent({ model: undefined, effort: "high" });
    Object.assign(getState(), {
      launches: {
        "local-2": {
          id: "local-2",
          machineId: s.machine_id,
          cwd: s.cwd,
          launcher: "",
          displayLauncher: "gateway",
          displayEngine: "claude",
          displayModel: "opus",
          text: "",
          attachments: [],
          phase: "ready",
          session: s,
          uploaded: {},
          startOpId: "op-1",
          startPromptOpId: "op-2",
          messageId: "msg-1",
        },
      },
    });
    await show(s);
    expect(pill()!.textContent).toContain("Opus 5.5");
    await open();
    expect(panel.props!.state.current).toMatchObject({ provider: "gateway", model: "opus", effort: "high" });

    modesAnswer = () => json({ error: "busy" }, 409);
    expect(await act(() => panel.props!.onPick({ provider: "gateway", model: "opus", effort: "high" }))).toBe(false);
    expect(modes()).toEqual([{ model: "opus" }]);
    expect(getSession(s.machine_id, "s1")?.model).toBeUndefined();
  });

  it("opens SessionModelPanel on a live terminal agy session and applies model and effort picks", async () => {
    const s = agent({
      agent: "agy",
      launcher: undefined,
      pane_id: "herdr:w1:p1",
      model: "gemini-4-neon-high",
      effort: "high",
      efforts: ["low", "medium", "high", "xhigh"],
      capabilities: { read: true, send: true, modes: false, model: true },
    });
    const agyCatalog = {
      agent: "agy",
      models: [
        { id: "gemini-4-neon-low", name: "Gemini 4 Neon (Low)", efforts: ["low", "medium", "high", "xhigh"], default_effort: "low" },
        { id: "gemini-4-neon-high", name: "Gemini 4 Neon (High)", efforts: ["low", "medium", "high", "xhigh"], default_effort: "high" },
      ],
    };
    const origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.includes("/launchers/agy/models")) {
        calls.push({ url, method: init?.method ?? "GET", body: init?.body ? JSON.parse(String(init.body)) : undefined });
        return new Response(JSON.stringify(agyCatalog), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return origFetch(input, init);
    }) as typeof fetch;

    await show(s);
    expect(pill()).not.toBeNull();
    expect(calls.some((c) => c.url.includes("/launchers/agy/models") && !c.url.includes("host=agy"))).toBe(true);
    await open();
    expect(panel.props!.state.current).toMatchObject({ provider: "agy", model: "gemini-4-neon-high", effort: "high" });
    expect(await act(() => panel.props!.onPick({ provider: "agy", model: "gemini-4-neon-low" }))).toBe(true);
    expect(modes()).toEqual([{ model: "gemini-4-neon-low" }]);
  });

  it("is a label where nothing switches (offline), and nothing at all without a model", async () => {
    await show(agent({ status: "offline", state: "offline" }));
    expect(pill()).toBeNull();
    expect(host.querySelector('[data-model-pill="label"]')!.textContent).toContain("opus 5.5");
    expect(calls.map((c) => c.url)).toEqual([expect.stringContaining("/launchers?")]); // a label needs only the launcher's title
    await show(agent({ status: "offline", state: "offline", model: undefined }));
    expect(host.innerHTML).toBe("");
  });
});
