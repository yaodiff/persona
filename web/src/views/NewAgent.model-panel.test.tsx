// @vitest-environment jsdom
// "New agent"'s model choice in the model pill and its unified model panel (#5104, after Cindy's new-task composer): left
// alone it follows Model settings (nothing of the model is sent, the pill names what the settings run with the engine's
// mark); a model row sets the launcher, model and the effort it shows, and unchecks "Follow Model settings", which goes
// back; the picked model's flyout changes the effort ("Reset to recommended" clears it) and its engine pills pick the
// launcher that runs the same model with another engine; a pick goes along to another computer that has its launcher and
// falls back, saying so, on one that hasn't; a typed ID is sent; a model pick leaves the permission mode and folder alone;
// a list that can't be read is said under the box; a pick goes on past a computer whose launchers are still being read,
// and none of it to one whose launchers can't be read; a launcher that can't start here isn't asked for its list.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Launcher, Machine } from "../lib/types";

const state = vi.hoisted(() => ({ loaded: true, features: [] as string[], machines: [] as Machine[] }));
// one assistant: its computers are the store's (lib/current-assistant.ts reads the roster, which this store stand-in has none of)
vi.mock("../lib/current-assistant", async () => {
  const store = (await import("../lib/store")) as unknown as { useStore: <T>(select: (s: { machines: unknown[] }) => T) => T };
  return { useScopedMachines: () => store.useStore((s) => s.machines) };
});
vi.mock("../lib/store", () => ({
  getState: () => state,
  useStore: (selector: (value: typeof state) => unknown) => selector(state),
  hubHas: (features: string[], feature: string, unknown = false) => (features.length ? features.includes(feature) : unknown),
}));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));
const launched = vi.hoisted(() => [] as Record<string, unknown>[]);
vi.mock("../lib/new-agent", async (original) => ({ ...(await original<typeof import("../lib/new-agent")>()), launchAgent: (input: Record<string, unknown>) => { launched.push(input); return "local-1"; } }));

import { NewAgent } from "./NewAgent";
import { t } from "../i18n";
import type { PickerTier } from "../lib/launcher-models";
import { resetModelListsForTests } from "../lib/session-models";
import { refreshWorkspaceCatalog, type HostWorkspace } from "../lib/workspace-catalog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const EFFORTS = ["low", "medium", "high", "xhigh", "max"];
// the Mac has a launcher the Studio lacks; a gateway (relay) lists a model a Codex launcher lists too
const LAUNCHERS: Record<string, Launcher[]> = {
  // codex: this computer opens Codex with no first message too (#4961), so a start may leave it empty
  mac: [{ name: "claude", agent: "claude" }, { name: "relay", agent: "claude" }, { name: "codex", agent: "codex", prompt_optional: true }, { name: "only-mac", agent: "claude" }, { name: "agy", agent: "agy" }],
  studio: [{ name: "claude", agent: "claude" }, { name: "relay", agent: "claude" }],
  mini: [{ name: "claude", agent: "claude" }, { name: "relay", agent: "claude" }],
};
const LISTS: Record<string, unknown> = {
  claude: { agent: "claude", models: [{ id: "default", name: "Default", default: true, efforts: EFFORTS }, { id: "opus", name: "Opus 5.5", efforts: EFFORTS, default_effort: "high" }, { id: "sonnet", name: "Sonnet 5", efforts: EFFORTS }] },
  relay: { agent: "claude", models: [{ id: "default", name: "Default", default: true }, { id: "glm-5", name: "GLM 5", efforts: ["low", "medium", "high"] }, { id: "gpt-5.5", name: "GPT 5.5" }] },
  codex: { agent: "codex", models: [{ id: "default", name: "Default", default: true }, { id: "gpt-5.5", name: "GPT 5.5" }] },
  "only-mac": { agent: "claude", models: [{ id: "default", name: "Default", default: true }] },
  agy: { agent: "agy", models: [{ id: "example-high", name: "Example High", efforts: ["low", "high", "xhigh"], default_effort: "high" }] },
};
// Model settings › New agent: Claude Code's Opus at High
const PURPOSE = { purpose: { id: "agent.new", effective: { provider: "launcher.claude", model: "opus", effort: "high" } }, providers: [] };
let effective = PURPOSE.purpose.effective;
let failList = "";
let failLaunchersOf = "";
let workspaceRows: HostWorkspace[] = [];
let tiers: PickerTier[] = [];
let wideScreen = false;
const asked: string[] = [];

let host: HTMLDivElement;
let root: Root;
const flush = () => act(async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); });
const q = <T extends HTMLElement = HTMLElement>(sel: string) => document.body.querySelector<T>(sel);
const button = (text: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === text)!;
const radio = (name: string) => [...document.body.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent?.trim() === name)!;
const machineChip = (name: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === name && b.className.includes("h-8"))!;
const folder = () => q<HTMLInputElement>("input[placeholder]")!;
const pill = () => q<HTMLButtonElement>("[data-testid=new-agent-model] [data-model-pill]")!;
const panel = () => q("[data-model-panel]");
const flyout = () => q("[data-model-flyout]");
const follow = () => q("[data-follow-row]")!;
const row = (key: string) => [...document.body.querySelectorAll<HTMLElement>("[data-panel-row]")].find((r) => r.dataset.panelRow === key)!;

function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const key = (el: Element, k: string) => act(() => { el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })); });

beforeEach(() => {
  resetModelListsForTests();
  state.features = [];
  state.machines = [{ id: "mac", name: "Mac", online: true }, { id: "studio", name: "Studio", online: true }];
  launched.length = 0;
  effective = PURPOSE.purpose.effective;
  failList = "";
  failLaunchersOf = "";
  workspaceRows = [];
  tiers = [];
  wideScreen = false;
  asked.length = 0;
  localStorage.clear();
  localStorage.setItem("new.machine", "mac");
  localStorage.setItem("new.cwd.mac", "/code/app");
  localStorage.setItem("new.cwd.studio", "/code/studio-app");
  vi.stubGlobal("matchMedia", (q: string) => ({
    get matches() { return wideScreen && (q === "(any-pointer: fine)" || q === "(min-width: 768px)"); },
    media: q,
    addEventListener() {},
    removeEventListener() {},
  }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    asked.push(url);
    if (url.endsWith("/workspaces")) return json({ workspaces: workspaceRows });
    if (url.includes("/model-config/purposes/")) return json({ ...PURPOSE, purpose: { ...PURPOSE.purpose, effective } });
    const list = /\/machines\/([^/]+)\/launchers\/([^/?]+)\/models/.exec(url);
    if (list?.[2] === "agy" && new URL(url, "http://localhost").searchParams.get("host") === "herdr") {
      return json({ agent: "agy", models: [{ id: "local-high", name: "Local High", efforts: ["high"] }] });
    }
    if (list?.[2] === "agy" && new URL(url, "http://localhost").searchParams.get("host") === "orca") {
      return json({ agent: "agy", models: [{ id: "orca-high", name: "Orca High", efforts: ["high", "xhigh"] }] });
    }
    if (list) return decodeURIComponent(list[2]) === failList ? json({ error: "节点没有回应" }, 500) : json(LISTS[decodeURIComponent(list[2])] ?? { models: [] });
    const ls = /\/machines\/([^/]+)\/launchers/.exec(url);
    if (ls && decodeURIComponent(ls[1]) === failLaunchersOf) return json({ error: "节点没有回应" }, 500);
    if (ls) return json({ launchers: LAUNCHERS[decodeURIComponent(ls[1])] ?? [], default: { provider: "claude", model: "opus", effort: "high" }, tiers });
    return json({ path: "/code", entries: [] });
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

async function open() {
  await act(async () => root.render(<NewAgent open onClose={() => {}} />));
  await flush();
}

async function openPanel() {
  await act(async () => pill().click());
  await act(async () => vi.dynamicImportSettled());
  await flush();
  expect(panel()).not.toBeNull();
  // These caller tests inspect model actions; disclose the catalog first.
  for (const header of document.body.querySelectorAll<HTMLButtonElement>('[data-panel-launcher][aria-expanded="false"]')) act(() => header.click());
  await flush();
}

/** Opens the panel and picks a row by its key (`model:<launcher>\n<id>`); the panel closes once the form took it. */
async function pick(k: string) {
  await openPanel();
  act(() => row(k).click());
  await flush();
  expect(panel()).toBeNull();
}

/** Opens a row's setup flyout. */
async function customize(k: string) {
  act(() => row(k).querySelector<HTMLButtonElement>("[data-row-customize]")!.click());
  await flush();
  expect(flyout()).not.toBeNull();
}

async function slide(k: string) {
  key(flyout()!.querySelector('[role="slider"]')!, k);
  await flush();
}

const start = () => act(() => button("启动").click());

describe("New agent's model panel (#5104)", () => {
  it.each([false, true])("refreshes the open provider's catalog in place (wide: %s)", async (wide) => {
    wideScreen = wide;
    await open();
    await openPanel();
    if (wide) act(() => q<HTMLButtonElement>('[data-panel-side="agy"]')!.click());
    const providerName = "Antigravity CLI";
    const group = q(`[data-panel-section="provider"][aria-label="${providerName}"]`)!;
    expect(group).not.toBeNull();
    const refreshLabel = t("ui-modelpanel.refresh_provider_models", { provider: providerName });
    const refresh = () => group.querySelector<HTMLButtonElement>(`button[aria-label="${refreshLabel}"]`)!;
    const fetchBefore = vi.mocked(fetch).getMockImplementation()!;
    let answer!: (response: Response) => void;
    const reply = new Promise<Response>((resolve) => { answer = resolve; });
    const forced = vi.fn(() => reply);
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = new URL(String(input), "http://localhost");
      return url.pathname.endsWith("/launchers/agy/models") && url.searchParams.get("refresh") === "1"
        ? forced() : fetchBefore(input, init);
    });

    expect(group.textContent).toContain("Example");
    expect(asked.some((url) => url.includes("refresh=1"))).toBe(false);
    await act(async () => refresh().click());
    expect(forced).toHaveBeenCalledTimes(1);
    expect(refresh().disabled).toBe(true);
    expect(group.querySelector('[role="status"]')?.textContent).toBe(t("ui-modelpanel.loading"));
    expect(group.textContent).toContain("Example");

    await act(async () => answer(json({ agent: "agy", models: [{ id: "fresh", name: "Fresh model" }] })));
    expect(group.textContent).toContain("Fresh model");
    expect(group.textContent).not.toContain("Example");
    expect(refresh().disabled).toBe(false);
  });

  it("left alone, follows Model settings: nothing of the model is sent, the pill names what they run with the engine's mark", async () => {
    await open();
    expect(pill().title).toBe("Claude Code · Opus 5.5 · High");
    expect(pill().querySelector("[data-pill-engine]")!.getAttribute("data-pill-engine")).toBe("claude");
    await openPanel();
    expect(follow().getAttribute("aria-pressed")).toBe("true");
    expect(follow().textContent).toBe("跟随模型配置Claude Code · Opus 5.5 · High");
    // the row lit is the launcher's Default row, named after the model the settings run
    const lit = row("model:claude\ndefault");
    expect(lit.getAttribute("aria-pressed")).toBe("true");
    expect(lit.textContent).toContain("默认：Opus 5.5");
    act(() => pill().click());
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ machineId: "mac", cwd: "/code/app", launcher: "" });
    expect(launched[0].model).toBeUndefined();
    expect(launched[0].effort).toBeUndefined();
  });

  it("a model row sets the launcher, the model and the effort it shows, and unchecks Follow Model settings", async () => {
    await open();
    await openPanel();
    // the row says the effort a pick applies: none (the model's own), not the one running now (#12315)
    expect(row("model:claude\nsonnet").querySelector("[data-row-setup]")!.textContent).toBe("");
    act(() => row("model:claude\nsonnet").click());
    await flush();
    expect(panel()).toBeNull();
    // nothing sent for the effort: the pill says the one Model settings starts it with
    expect(pill().title).toBe("Claude Code · Sonnet 5 · High");
    await openPanel();
    expect(follow().getAttribute("aria-pressed")).toBe("false");
    expect(row("model:claude\nsonnet").getAttribute("aria-pressed")).toBe("true");
    expect(row("model:claude\ndefault").getAttribute("aria-pressed")).toBe("false");
    act(() => pill().click());
    start();
    expect(launched[0]).toMatchObject({ launcher: "claude", model: "sonnet" });
    expect(launched[0].effort).toBeUndefined();
  });

  it("Follow Model settings goes back to the default after a pick", async () => {
    await open();
    await pick("model:relay\nglm-5");
    expect(pill().title).toContain("GLM 5");
    await openPanel();
    act(() => follow().click());
    await flush();
    expect(panel()).toBeNull();
    expect(pill().title).toBe("Claude Code · Opus 5.5 · High");
    start();
    expect(launched[0]?.launcher).toBe("");
    expect(launched[0]?.model).toBeUndefined();
    expect(launched[0]?.effort).toBeUndefined();
  });

  it("sends the effort changed in the picked model's flyout", async () => {
    await open();
    await pick("model:relay\nglm-5");
    await openPanel();
    await customize("model:relay\nglm-5");
    expect(flyout()!.querySelector('[role="slider"]')!.getAttribute("aria-valuetext")).toBe("模型默认");
    // from none, ← lands on the lowest
    await slide("ArrowLeft");
    expect(pill().title).toBe("Claude Code · GLM 5 · relay · Low");
    expect(flyout()!.textContent).toContain("已自定义");
    act(() => pill().click());
    start();
    expect(launched[0]).toMatchObject({ launcher: "relay", model: "glm-5", effort: "low" });
  });

  it("Reset to recommended in the flyout clears the effort again", async () => {
    await open();
    await pick("model:relay\nglm-5");
    await openPanel();
    await customize("model:relay\nglm-5");
    await slide("ArrowLeft");
    act(() => button("恢复推荐").click());
    await flush();
    expect(pill().title).toBe("Claude Code · GLM 5 · relay");
    expect(flyout()!.textContent).toContain("推荐配置");
    expect(flyout()!.querySelector('[role="slider"]')!.getAttribute("aria-valuetext")).toBe("模型默认");
    act(() => pill().click());
    start();
    expect(launched[0]).toMatchObject({ launcher: "relay", model: "glm-5" });
    expect(launched[0].effort).toBeUndefined();
  });

  it.each([false, true])("sends the selected agy effort or uses the launcher default (reset=%s)", async (reset) => {
    await open();
    await pick("model:agy\nexample-high");
    await openPanel();
    await customize("model:agy\nexample-high");
    await slide("End");
    if (reset) {
      act(() => button("恢复推荐").click());
      await flush();
    }
    act(() => pill().click());
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ launcher: "agy", model: "example-high" });
    expect(launched[0].effort).toBe(reset ? undefined : "xhigh");
  });

  it("omits both overrides when the agy default is picked", async () => {
    await open();
    await pick("model:agy\ndefault");
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ launcher: "agy" });
    expect(launched[0].model).toBeUndefined();
    expect(launched[0].effort).toBeUndefined();
  });

  it.each(["default", "example-high"])("uses native agy effort for the %s row despite Model settings", async (model) => {
    effective = { provider: "launcher.agy", model: "example-high", effort: "xhigh" };
    await open();
    const rowKey = `model:agy\n${model}`;
    await pick(rowKey);
    if (model !== "default") await pick(rowKey); // pick the current named row again
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ launcher: "agy", launcherDefaultEffort: true });
    expect(launched[0].effort).toBeUndefined();
    expect(launched[0].model).toBe(model === "default" ? undefined : model);
  });

  it("switches from the default host's agy catalog to the explicitly selected herdr catalog", async () => {
    state.machines[0] = { ...state.machines[0], engine_hosts: ["orca", "herdr"], engine_default: "orca" };
    await open();
    await pick("model:agy\nexample-high");
    expect(asked.some((url) => url.endsWith("/launchers/agy/models"))).toBe(true);
    act(() => radio("herdr").click());
    await flush();
    await openPanel();
    expect(row("model:agy\nexample-high")).toBeUndefined();
    expect(row("model:agy\nlocal-high")).toBeDefined();
    expect(asked.some((url) => url.endsWith("/launchers/agy/models?host=herdr"))).toBe(true);
    act(() => row("model:agy\nlocal-high").click());
    await flush();
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ launcher: "agy", host: "herdr", model: "local-high" });
  });

  it.each(["selecting", "clearing"])("clears the AGY model, effort and preset when %s a workspace changes hosts", async (action) => {
    state.machines[0] = { ...state.machines[0], engine_hosts: ["herdr", "orca"], engine_default: "herdr" };
    workspaceRows = [{ id: "mac/orca/project", machine_id: "mac", source: "orca", source_id: "project", cwd: "/code/app", title: "Project" }];
    const clearing = action === "clearing";
    tiers = [{ name: "deep", launcher: "agy", model: clearing ? "orca-high" : "example-high", effort: "xhigh" }];
    await open();
    await act(async () => { await refreshWorkspaceCatalog(); });
    const selectWorkspace = async () => {
      await act(async () => q<HTMLButtonElement>("[data-workspace-choice]")!.click());
      expect(q('[data-testid="new-agent-workspace"]')).not.toBeNull();
    };
    if (clearing) await selectWorkspace();
    await pick("preset:deep");
    expect(q('[data-testid="new-agent-preset-hint"]')).not.toBeNull();
    expect(pill().title).toContain(clearing ? "Orca High" : "Example High");

    if (clearing) {
      await act(async () => button(t("newagent-workspace.choose_folder")).click());
      expect(q('[data-testid="new-agent-workspace"]')).toBeNull();
    } else {
      await selectWorkspace();
    }
    await flush();
    expect(q('[data-testid="new-agent-preset-hint"]')).toBeNull();
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ launcher: "agy", launcherDefaultEffort: true });
    expect(launched[0].host).toBe(clearing ? undefined : "orca");
    expect(launched[0].workspaceId).toBe(clearing ? undefined : "project");
    expect(launched[0].model).toBeUndefined();
    expect(launched[0].effort).toBeUndefined();
  });

  it("an engine pill in a model's flyout picks the launcher that runs the same model with that engine", async () => {
    await open();
    await openPanel();
    await customize("model:relay\ngpt-5.5");
    const engines = [...flyout()!.querySelectorAll<HTMLButtonElement>("[data-engine-choice]")];
    expect(engines.map((b) => [b.dataset.engineChoice, b.getAttribute("aria-pressed")])).toEqual([["claude", "true"], ["codex", "false"]]);
    expect(engines[1].title).toBe("通过 Codex 运行");
    act(() => engines[1].click());
    await flush();
    expect(panel()).toBeNull();
    expect(pill().title).toBe("Codex · GPT 5.5");
    expect(pill().querySelector("[data-pill-engine]")!.getAttribute("data-pill-engine")).toBe("codex");
    start();
    expect(launched[0]).toMatchObject({ launcher: "codex", model: "gpt-5.5" });
    expect(launched[0].effort).toBeUndefined();
  });

  it("keeps a pick on another computer that has its launcher", async () => {
    await open();
    await pick("model:relay\nglm-5");
    act(() => machineChip("Studio").click());
    await flush();
    expect(pill().title).toBe("Claude Code · GLM 5 · relay");
    expect(q("[data-testid=new-agent-dropped]")).toBeNull();
    start();
    expect(launched[0]).toMatchObject({ machineId: "studio", cwd: "/code/studio-app", launcher: "relay", model: "glm-5" });
    expect(launched[0].effort).toBeUndefined();
  });

  it("falls back to the default on a computer without the picked launcher, and says so under the box", async () => {
    await open();
    await pick("model:only-mac\ndefault");
    expect(pill().title).toContain("only-mac");
    act(() => machineChip("Studio").click());
    await flush();
    expect(q("[data-testid=new-agent-dropped]")!.textContent).toBe("only-mac 无法在 Studio 上运行，已改用这台电脑的默认设置。");
    expect(pill().title).toBe("Claude Code · Opus 5.5 · High");
    // a pick on this computer takes the note away
    await pick("model:claude\nsonnet");
    expect(q("[data-testid=new-agent-dropped]")).toBeNull();
    await pick("model:relay\ndefault");
    start();
    expect(launched[0]).toMatchObject({ machineId: "studio", launcher: "relay" });
    expect(launched[0].model).toBeUndefined();
  });

  it("a pick goes on to the computer after the next one when that one's launchers were still being read", async () => {
    state.machines = [...state.machines, { id: "mini", name: "Mini", online: true }];
    localStorage.setItem("new.cwd.mini", "/code/mini-app");
    await open();
    await pick("model:relay\nglm-5");
    act(() => machineChip("Studio").click());
    act(() => machineChip("Mini").click());
    await flush();
    expect(pill().title).toBe("Claude Code · GLM 5 · relay");
    start();
    expect(launched[0]).toMatchObject({ machineId: "mini", launcher: "relay", model: "glm-5" });
  });

  it("a computer whose launchers can't be read gets nothing of the model picked on the one before", async () => {
    failLaunchersOf = "studio";
    await open();
    await pick("model:claude\nsonnet");
    act(() => machineChip("Studio").click());
    await flush();
    start();
    expect(launched[0]).toMatchObject({ machineId: "studio", launcher: "" });
    expect(launched[0].model).toBeUndefined();
  });

  it("doesn't ask a launcher that can't start here for its list when the panel opens", async () => {
    LAUNCHERS.mac = [...LAUNCHERS.mac, { name: "broken", agent: "claude", health: { state: "blocked", kind: "auth" } }];
    try {
      await open();
      await openPanel();
      expect(asked.some((u) => u.includes("/launchers/relay/models"))).toBe(true);
      expect(asked.some((u) => u.includes("/launchers/broken/models"))).toBe(false);
      // still listed, its Default row off
      expect(row("model:broken\ndefault").getAttribute("aria-disabled")).toBe("true");
    } finally {
      LAUNCHERS.mac = LAUNCHERS.mac.filter((l) => l.name !== "broken");
    }
  });

  it("sends a model ID typed in the search (Enter picks its “Use …” row)", async () => {
    await open();
    await openPanel();
    const search = panel()!.querySelector<HTMLInputElement>("input")!;
    type(search, "acme/coder-9");
    expect(row("typed:claude\nacme/coder-9").textContent).toContain("使用「acme/coder-9」");
    key(search, "Enter");
    await flush();
    expect(panel()).toBeNull();
    start();
    expect(launched[0]).toMatchObject({ launcher: "claude", model: "acme/coder-9" });
  });

  it("leaves the permission mode and the folder alone when a model is picked", async () => {
    await open();
    act(() => radio("计划模式").click());
    type(folder(), "/code/other");
    await pick("model:claude\nsonnet");
    expect(radio("计划模式").getAttribute("aria-checked")).toBe("true");
    expect(folder().value).toBe("/code/other");
    start();
    expect(launched[0]).toMatchObject({ cwd: "/code/other", permissionMode: "plan", launcher: "claude", model: "sonnet" });
  });

  it("says under the box when the list of the launcher that runs can't be read, and still starts", async () => {
    failList = "claude";
    await open();
    expect(pill().dataset.modelPill).toBe("failed");
    expect(q("[data-testid=new-agent-models-failed]")!.textContent).toBe("无法加载模型列表，仍可使用默认模型启动。");
    start();
    expect(launched[0]).toMatchObject({ machineId: "mac", launcher: "" });
    expect(launched[0].model).toBeUndefined();
  });
});

describe("Codex reasoning effort in New Agent (#13764)", () => {
  const codexCatalog = {
    agent: "codex",
    models: [
      { id: "gpt-5.5", name: "GPT-5.5", default: true, efforts: ["minimal", "low", "medium", "high", "xhigh"], default_effort: "medium" },
      { id: "gpt-5.5-mini", name: "GPT-5.5 Mini", efforts: ["low", "high"], default_effort: "low" },
      { id: "gpt-5.5-plain", name: "GPT-5.5 Plain" },
    ],
  };

  afterEach(() => {
    LISTS.codex = { agent: "codex", models: [{ id: "default", name: "Default", default: true }, { id: "gpt-5.5", name: "GPT-5.5" }] };
  });

  it("keeps Codex effort unset by default and never leaks Claude permission modes", async () => {
    LISTS.codex = codexCatalog;
    localStorage.setItem("new.mode.mac", "plan");
    await open();
    await pick("model:codex\ngpt-5.5");
    expect(pill().title).toBe("Codex · GPT-5.5");
    expect(radio("计划模式")).toBeUndefined();
    await openPanel();
    await customize("model:codex\ngpt-5.5");
    expect(flyout()!.querySelector('[role="slider"]')!.getAttribute("aria-valuetext")).toBe("模型默认");
    act(() => pill().click());
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ launcher: "codex", agentType: "codex", inlinePrompt: true });
    expect(launched[0].model).toBeUndefined();
    expect(launched[0].effort).toBeUndefined();
    expect(launched[0].permissionMode).toBeUndefined();
  });

  it.each([false, true])("offers only reported Codex levels, sends explicit picks, and clears on Reset (reset=%s)", async (reset) => {
    LISTS.codex = codexCatalog;
    await open();
    await pick("model:codex\ngpt-5.5-mini");
    await openPanel();
    await customize("model:codex\ngpt-5.5-mini");
    const slider = flyout()!.querySelector('[role="slider"]')!;
    expect(slider.getAttribute("aria-valuemin")).toBe("0");
    expect(slider.getAttribute("aria-valuemax")).toBe("1");
    expect(slider.getAttribute("aria-valuetext")).toBe("模型默认");
    // End lands on the highest reported level ("high"), never an unreported level like "max".
    await slide("End");
    expect(pill().title).toBe("Codex · GPT-5.5 Mini · High");
    if (reset) {
      act(() => button("恢复推荐").click());
      await flush();
      expect(pill().title).toBe("Codex · GPT-5.5 Mini");
      expect(flyout()!.querySelector('[role="slider"]')!.getAttribute("aria-valuetext")).toBe("模型默认");
    }
    act(() => pill().click());
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ launcher: "codex", agentType: "codex", inlinePrompt: true, model: "gpt-5.5-mini" });
    expect(launched[0].effort).toBe(reset ? undefined : "high");
  });

  it("omits effort controls for Codex models with no reported levels and for typed Codex models", async () => {
    LISTS.codex = codexCatalog;
    await open();
    await pick("model:codex\ngpt-5.5-plain");
    await openPanel();
    await customize("model:codex\ngpt-5.5-plain");
    expect(flyout()!.querySelector('[role="slider"]')).toBeNull();
    // Typing an unlisted Codex model also offers no effort slider.
    const search = panel()!.querySelector<HTMLInputElement>("input")!;
    type(search, "acme/codex-custom");
    act(() => row("typed:codex\nacme/codex-custom").click());
    await flush();
    await openPanel();
    await customize("model:codex\nacme/codex-custom");
    expect(flyout()!.querySelector('[role="slider"]')).toBeNull();
    act(() => pill().click());
    start();
    expect(launched[0]).toMatchObject({ launcher: "codex", model: "acme/codex-custom" });
    expect(launched[0].effort).toBeUndefined();
  });

  it("shows only reported Codex levels in three-column mode and sends an explicit selection including the catalog default", async () => {
    LISTS.codex = codexCatalog;
    wideScreen = true;
    await open();
    await openPanel();
    act(() => document.body.querySelector<HTMLButtonElement>('[data-panel-side="codex"]')!.click());
    await flush();
    act(() => row("model:codex\ngpt-5.5").click());
    await flush();
    const levelButtons = [...document.body.querySelectorAll<HTMLButtonElement>("[data-panel-level]")];
    expect(levelButtons.map((b) => b.dataset.panelLevel)).toEqual(["minimal", "low", "medium", "high", "xhigh"]);
    // Unset: no level starts checked even though the catalog reports default_effort: "medium".
    expect(levelButtons.some((b) => b.getAttribute("aria-pressed") === "true")).toBe(false);
    // Explicitly selecting "medium" sends effort: "medium" to override Codex's config.toml.
    act(() => levelButtons.find((b) => b.dataset.panelLevel === "medium")!.click());
    await flush();
    expect(pill().title).toBe("Codex · GPT-5.5 · Medium");
    act(() => pill().click());
    start();
    expect(launched[0]).toMatchObject({ launcher: "codex", agentType: "codex", inlinePrompt: true, effort: "medium" });
  });

  it("preserves a Codex preset's reported effort while its model catalog loads", async () => {
    LISTS.codex = codexCatalog;
    tiers = [{ name: "codex-deep", launcher: "codex", model: "gpt-5.5-mini", effort: "high" }];
    await open();
    await pick("preset:codex-deep");
    expect(pill().title).toBe("Codex · GPT-5.5 Mini · High");
    start();
    expect(launched[0]).toMatchObject({ launcher: "codex", agentType: "codex", inlinePrompt: true, model: "gpt-5.5-mini", effort: "high" });
  });
});

describe("Codex start speed", () => {
  const catalog = LISTS.codex;
  const tiers = [{ id: "priority", name: "Fast" }, { id: "ultrafast", name: "Ultrafast" }];
  const speedButton = (id: string) => q<HTMLButtonElement>(`[data-panel-speed="${id}"]`)!;

  function offerSpeed(hub: boolean, node: boolean) {
    state.features = hub ? ["session_start_service_tier"] : [];
    LISTS.codex = {
      agent: "codex",
      supports_start_service_tier: node,
      models: [
        { id: "gpt-5.5", name: "GPT-5.5", default: true, service_tiers: tiers },
        { id: "gpt-5.5-mini", name: "GPT-5.5 Mini" },
      ],
    };
  }

  afterEach(() => { LISTS.codex = catalog; });

  it.each([[false, "fast", "priority"], [true, "ultrafast", "ultrafast"]] as const)(
    "applies the selected tier in the narrow flyout or wide footer (wide=%s)", async (wide, speed, tier) => {
      wideScreen = wide;
      offerSpeed(true, true);
      await open();
      if (!wide) {
        await pick("model:codex\ngpt-5.5");
        await openPanel();
        await customize("model:codex\ngpt-5.5");
      } else {
        await openPanel();
        act(() => q<HTMLButtonElement>('[data-panel-side="codex"]')!.click());
        await flush();
        act(() => row("model:codex\ngpt-5.5").click());
      }
      await act(async () => vi.dynamicImportSettled());
      await flush();
      expect(speedButton("standard").getAttribute("aria-pressed")).toBe("true");
      act(() => speedButton(speed).click());
      expect(speedButton(speed).getAttribute("aria-pressed")).toBe("true");
      if (!wide) {
        act(() => button("恢复推荐").click());
        expect(speedButton("standard").getAttribute("aria-pressed")).toBe("true");
        act(() => speedButton(speed).click());
      }
      act(() => pill().click());
      start();
      expect(launched[0]).toMatchObject({ launcher: "codex", serviceTier: tier });
    },
  );

  it.each([[false, true], [true, false]])("hides speed for older peers (hub=%s, node=%s)", async (hub, node) => {
    offerSpeed(hub, node);
    await open();
    await pick("model:codex\ngpt-5.5");
    await openPanel();
    await customize("model:codex\ngpt-5.5");
    expect(q("[data-panel-speed]")).toBeNull();
    act(() => pill().click());
    start();
    expect(launched[0].serviceTier).toBeUndefined();
  });

  it("clears a tier when the user switches to a model with no tiers", async () => {
    offerSpeed(true, true);
    await open();
    await pick("model:codex\ngpt-5.5");
    await openPanel();
    await customize("model:codex\ngpt-5.5");
    await act(async () => vi.dynamicImportSettled());
    act(() => speedButton("ultrafast").click());
    act(() => row("model:codex\ngpt-5.5-mini").click());
    await flush();
    start();
    expect(launched[0]).toMatchObject({ launcher: "codex", model: "gpt-5.5-mini" });
    expect(launched[0].serviceTier).toBeUndefined();
  });
});
