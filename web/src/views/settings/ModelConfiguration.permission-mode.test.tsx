// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("./ConfigurationManager", () => ({ ConfigurationManager: ({ children }: { children: (id: string) => unknown }) => children("assistant") }));
vi.mock("./ModelProfiles", () => ({ ModelProfiles: () => null }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { BYPASS_WARNING, modeTitle } from "../../lib/session-actions";
import { agyModels, claudeModels, codexModels, neon } from "../../lib/launcher-model-binding.fixture";
import type { ModelBinding, ModelProvider, ModelPurpose } from "../../lib/model-config";
import { getState, reset } from "../../lib/store";
import { ModelConfiguration } from "./ModelConfiguration";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const PROVIDERS: ModelProvider[] = [
  { id: "launcher.claude", name: "Claude Code", kind: "launcher", agent: "claude" },
  { id: "launcher.codex", name: "Codex", kind: "launcher", agent: "codex" },
  { id: "launcher.traex", name: "TRAE CLI", kind: "launcher", agent: "traex" },
  { id: "launcher.agy", name: "Antigravity", kind: "launcher", agent: "agy" },
  { id: "launcher.omp", name: "omp", kind: "launcher", agent: "omp" },
  { id: "launcher.grok", name: "Grok", kind: "launcher", agent: "paseo", permission_modes: ["default", "bypassPermissions"] },
  { id: "launcher.messages", name: "Direct API", kind: "api", api: "messages", model: "api-model", has_key: true },
];

let agents: ModelPurpose;
let lark: ModelPurpose;
let inbox: ModelPurpose;
let triage: ModelPurpose;
let writes: { path: string; body: Record<string, unknown> }[] = [];
let host: HTMLDivElement;
let root: Root;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };

function makeAgents(binding: ModelBinding = { provider: "launcher.claude", permission_mode: "acceptEdits" }): ModelPurpose {
  return {
    id: "agent.new", name: "Agents", group: "Agents", kind: "launcher", launch: "engine", category_id: "agent.new",
    override: binding, effective: { provider: "launcher.claude", ...binding }, revision: 3,
    fields: ["provider", "model", "effort", "permission_mode"],
    provider_ids: ["launcher.claude", "launcher.codex", "launcher.traex", "launcher.agy", "launcher.omp", "launcher.grok"],
  };
}

function makeLark(override: ModelBinding = {}, revision = 5): ModelPurpose {
  const effective = override.provider && override.provider !== agents.effective.provider
    ? override
    : { ...agents.effective, ...override };
  return {
    id: "lark.agent", name: "Lark /agent", group: "Agents", kind: "launcher", launch: "headless",
    parent: "agent.new", category_id: "agent.new",
    override, effective, revision, inherited_from: override.provider ? "" : "Agents",
    fields: ["provider", "model", "effort", "permission_mode"],
    provider_ids: ["launcher.claude", "launcher.codex", "launcher.agy", "launcher.omp", "launcher.grok"],
  };
}

function makeInbox(override: ModelBinding = {}, revision = 4): ModelPurpose {
  return { ...makeLark(override, revision), id: "inbox.agent", name: "AI twin runs" };
}

function makeTriage(): ModelPurpose {
  return {
    id: "inbox.triage", name: "Sorting", group: "Sorting", kind: "api", category_id: "inbox.triage",
    override: { provider: "launcher.messages", model: "api-model" },
    effective: { provider: "launcher.messages", model: "api-model" },
    revision: 2, fields: ["provider", "model", "effort"], provider_ids: ["launcher.messages"],
  };
}

async function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const path = String(input).split("?")[0];
  if (path === "/api/v1/model-profiles") return json({ error: "not found" }, 404);
  if (path === "/api/v1/model-config") return json({ purposes: [agents, lark, inbox, triage], providers: PROVIDERS });
  if (path.endsWith("/models")) {
    const providerId = decodeURIComponent(path.split("/").slice(-2)[0] ?? "");
    const provider = PROVIDERS.find((p) => p.id === providerId);
    if (provider?.agent === "traex") return json({ agent: "traex", models: [{ id: "example-fast", name: "Example Fast", efforts: ["medium", "xhigh"] }] });
    const catalog = [agyModels, claudeModels, codexModels].find((list) => list.agent === provider?.agent);
    return json(catalog ?? { agent: provider?.agent ?? "claude", models: [{ id: "default", name: "Default", default: true }] });
  }
  const purpose = [agents, lark, inbox, triage].find((p) => path === `/api/v1/model-config/purposes/${p.id}`);
  if (purpose && init?.method === "PATCH") {
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    writes.push({ path, body });
    const next: ModelBinding = { ...purpose.override };
    for (const k of ["provider", "model", "effort", "permission_mode"] as const) {
      if (k in body) {
        if (body[k] == null) delete next[k];
        else next[k] = String(body[k]);
      }
    }
    if (purpose.id === "lark.agent") lark = makeLark(next, purpose.revision + 1);
    else if (purpose.id === "inbox.agent") inbox = makeInbox(next, purpose.revision + 1);
    else if (purpose.id === "agent.new") agents = makeAgents(next);
    return json({ purpose: purpose.id === "lark.agent" ? lark : purpose.id === "inbox.agent" ? inbox : agents, providers: PROVIDERS });
  }
  if (purpose) return json({ purpose, providers: PROVIDERS });
  if (path.startsWith("/api/v1/model-config/purposes/")) return json({ error: "not found" }, 404);
  return json({ error: `unexpected ${path}` }, 500);
}

const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label)!;
const permSelect = () => dialog().querySelector<HTMLSelectElement>("[data-permission-mode-select]");
const providerSelect = () => dialog().querySelector<HTMLSelectElement>("select")!;
const modelPicker = () => dialog().querySelector<HTMLButtonElement>("button[aria-expanded]")!;
const effortSelect = () => [...dialog().querySelectorAll("select")]
  .find((el) => el.closest("label")?.querySelector("span")?.textContent === t("settings-modelconfiguration.effort"))!;

async function render() {
  function Harness() {
    const [open, onOpen] = useState<string | null>(null);
    return <ModelConfiguration open={open} onOpen={onOpen} onProviders={() => {}} />;
  }
  await act(async () => root.render(<Harness />));
  await flush();
}

async function openPurpose(id: string) {
  await act(async () => host.querySelector<HTMLButtonElement>(`[data-model-purpose="${id}"] button`)!.click());
  await flush();
}

async function selectValue(el: HTMLSelectElement, value: string) {
  await act(async () => {
    el.value = value;
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}

async function pickModel(name: string) {
  await vi.waitFor(() => expect(modelPicker()).not.toBeNull());
  await act(async () => modelPicker().click());
  const row = [...dialog().querySelectorAll<HTMLButtonElement>('[role="option"]')].find((el) => el.textContent?.startsWith(name))!;
  await act(async () => row.click());
}

beforeEach(async () => {
  await initI18n("en");
  reset();
  Object.assign(getState(), { features: [], machines: [], sessions: {}, assistant: undefined });
  agents = makeAgents();
  lark = makeLark();
  inbox = makeInbox();
  triage = makeTriage();
  writes = [];
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  reset();
  await initI18n("zh-Hans");
});

describe("ModelConfiguration unified permission mode", () => {
  it("shows permission mode for launcher purposes, inherits parent mode, warns on bypass, saves and resets", async () => {
    await render();
    await openPurpose("lark.agent");
    const select = permSelect()!;
    expect(select).not.toBeNull();
    // Default option shows inherited mode from Agents
    expect(select.options[0].textContent).toContain(modeTitle("acceptEdits"));
    expect([...select.options].map((o) => o.value)).toEqual(["", "default", "acceptEdits", "plan", "auto", "bypassPermissions"]);

    await selectValue(select, "bypassPermissions");
    expect(dialog().textContent).toContain(BYPASS_WARNING);
    await act(async () => button(t("common.save"), dialog()).click());
    await flush();
    expect(writes[0]).toEqual({
      path: "/api/v1/model-config/purposes/lark.agent",
      body: { revision: 5, provider: null, model: null, effort: null, permission_mode: "bypassPermissions" },
    });

    // Resetting clears permission_mode back to null
    await openPurpose("lark.agent");
    await act(async () => button(t("settings-modelconfiguration.follow_agents"), dialog()).click());
    await flush();
    expect(writes[1].body).toEqual({ revision: 6, provider: null, model: null, effort: null, permission_mode: null });
  });

  it("keeps the AI twin on default instead of inheriting Agents bypass, then saves and resets its own choice", async () => {
    agents = makeAgents({ provider: "launcher.claude", permission_mode: "bypassPermissions" });
    inbox = makeInbox();
    await render();
    await openPurpose("inbox.agent");
    let select = permSelect()!;
    expect(select.options[0].textContent).toContain(modeTitle("default"));
    expect(select.options[0].textContent).not.toContain(modeTitle("bypassPermissions"));
    expect([...select.options].map((o) => o.value)).toEqual(["", "default", "acceptEdits", "auto", "bypassPermissions"]);

    await selectValue(select, "bypassPermissions");
    expect(dialog().textContent).toContain(BYPASS_WARNING);
    await act(async () => button(t("common.save"), dialog()).click());
    await flush();
    expect(writes[0]).toEqual({
      path: "/api/v1/model-config/purposes/inbox.agent",
      body: { revision: 4, provider: null, model: null, effort: null, permission_mode: "bypassPermissions" },
    });

    await openPurpose("inbox.agent");
    await act(async () => button(t("settings-modelconfiguration.follow_agents"), dialog()).click());
    await flush();
    expect(writes[1].body).toEqual({ revision: 5, provider: null, model: null, effort: null, permission_mode: null });
    await openPurpose("inbox.agent");
    select = permSelect()!;
    expect(select.options[0].textContent).toContain(modeTitle("default"));
    expect(select.options[0].textContent).not.toContain(modeTitle("bypassPermissions"));
  });

  it("treats a legacy AI-twin Plan override as default and does not offer Plan again", async () => {
    inbox = makeInbox({ permission_mode: "plan" });
    await render();
    await openPurpose("inbox.agent");
    const select = permSelect()!;
    expect(select.value).toBe("");
    expect([...select.options].map((o) => o.value)).toEqual(["", "default", "acceptEdits", "auto", "bypassPermissions"]);
    expect(dialog().textContent).toContain(modeTitle("default"));
  });

  it("filters permission modes by provider capability and hides selector on omp and direct API purposes", async () => {
    await render();
    // Direct API purpose never renders permission mode selector
    await openPurpose("inbox.triage");
    expect(permSelect()).toBeNull();
    await act(async () => button(t("common.cancel"), dialog()).click());
    await flush();

    // Launcher purpose: omp hides permission mode; Grok lists only its supported modes; Codex/AGY show all 5
    await openPurpose("agent.new");
    await selectValue(providerSelect(), "launcher.omp");
    expect(permSelect()).toBeNull();

    await selectValue(providerSelect(), "launcher.grok");
    expect([...permSelect()!.options].map((o) => o.value)).toEqual(["", "default", "bypassPermissions"]);

    await selectValue(providerSelect(), "launcher.agy");
    expect([...permSelect()!.options].map((o) => o.value)).toEqual(["", "default", "acceptEdits", "plan", "auto", "bypassPermissions"]);

    await selectValue(providerSelect(), "launcher.codex");
    expect([...permSelect()!.options].map((o) => o.value)).toEqual(["", "default", "acceptEdits", "plan", "auto", "bypassPermissions"]);
  });
});

describe("Model configuration model and effort", () => {
  async function save() {
    await act(async () => button(t("common.save"), dialog()).click());
    await flush();
  }

  it("saves the picked variant and offers only the family's effort levels", async () => {
    agents = makeAgents({ provider: "launcher.agy" });
    await render();
    await openPurpose("agent.new");
    await pickModel("Gemini 4 Neon");
    expect(modelPicker().textContent).toBe(`Gemini 4 Neon${neon}-medium`);
    expect(effortSelect().value).toBe("medium");
    expect([...effortSelect().options].slice(1).map((option) => option.textContent)).toEqual(["Low", "Medium", "High", "XHigh"]);
    await selectValue(effortSelect(), "xhigh");
    await save();
    expect(writes[0].body).toMatchObject({ model: `${neon}-xhigh`, effort: null });
  });

  it.each([["", "medium"], ["high", "high"]])("loads the saved variant and clears standalone effort %j on Save", async (effort, level) => {
    agents = makeAgents({ provider: "launcher.agy", model: `${neon}-medium`, effort });
    await render();
    await openPurpose("agent.new");
    await vi.waitFor(() => expect(modelPicker()?.textContent).toBe(`Gemini 4 Neon${neon}-${level}`));
    expect(effortSelect().value).toBe(level);
    await save();
    expect(writes[0].body).toMatchObject({ model: `${neon}-${level}`, effort: null });
  });

  it("turns an effort-only change on a child purpose into a sibling variant", async () => {
    agents = makeAgents({ provider: "launcher.agy", model: `${neon}-medium` });
    lark = makeLark();
    await render();
    await openPurpose("lark.agent");
    expect(modelPicker()?.textContent).toContain(t("ui-modelpicker.default"));
    expect(effortSelect().value).toBe("");
    expect(effortSelect().options[0].textContent).toContain("Medium");
    await selectValue(effortSelect(), "high");
    await save();
    expect(writes[0].body).toMatchObject({ provider: null, model: `${neon}-high`, effort: null });
  });

  it.each([
    ["claude", "Sonnet", "sonnet", "high"],
    ["codex", "GPT-5.5", "gpt-5.5", "xhigh"],
    ["traex", "Example Fast", "example-fast", "xhigh"],
  ])("writes %s model and effort separately", async (agent, name, model, effort) => {
    agents = makeAgents({ provider: `launcher.${agent}` });
    await render();
    await openPurpose("agent.new");
    await pickModel(name);
    await selectValue(effortSelect(), effort);
    await save();
    expect(writes[0].body).toMatchObject({ model, effort });
  });

  it("saves the Inbox TRAE model, effort, and Full access mode", async () => {
    inbox = { ...makeInbox(), provider_ids: [...(makeInbox().provider_ids ?? []), "launcher.traex"] };
    await render();
    await openPurpose("inbox.agent");
    await selectValue(providerSelect(), "launcher.traex");
    await pickModel("Example Fast");
    await selectValue(effortSelect(), "xhigh");
    const modes = dialog().querySelector<HTMLSelectElement>("[data-permission-mode-select]")!;
    expect([...modes.options].map((option) => option.value)).toEqual(["", "default", "acceptEdits", "auto", "bypassPermissions"]);
    await selectValue(modes, "bypassPermissions");
    await save();
    expect(writes[0].body).toMatchObject({ provider: "launcher.traex", model: "example-fast", effort: "xhigh", permission_mode: "bypassPermissions" });
  });

  it("keeps a legacy Inbox TRAE binding visible as unavailable and allows switching away", async () => {
    inbox = { ...makeInbox({ provider: "launcher.traex", model: "example-fast", effort: "xhigh" }), status: "Provider unavailable" };
    await render();
    await openPurpose("inbox.agent");
    expect(providerSelect().selectedOptions[0].textContent).toContain("TRAE CLI");
    expect(dialog().textContent).toContain("Provider unavailable");
    expect(modelPicker()).toBeNull();
    expect(button(t("common.save"), dialog()).disabled).toBe(true);
    await selectValue(providerSelect(), "launcher.codex");
    expect([...providerSelect().options].map((o) => o.value)).not.toContain("launcher.traex");
    await save();
    expect(writes[0].body).toMatchObject({ provider: "launcher.codex", model: null, effort: null });
  });

  it("disables effort when a known model offers none", async () => {
    agents = makeAgents({ provider: "launcher.agy" });
    await render();
    await openPurpose("agent.new");
    await pickModel("Gemini 4 Flash");
    expect(effortSelect().disabled).toBe(true);
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.api_effort_unsupported"));
  });
});
