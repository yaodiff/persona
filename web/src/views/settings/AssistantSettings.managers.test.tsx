// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("./ModelProfiles", () => ({ ModelProfiles: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }, { id: "work", name: "Work" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({ useStore: () => [], refreshAssistant: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { useState } from "react";
import { ModelConfiguration } from "./ModelConfiguration";
import { AssistantSettings } from "./AssistantSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
let modes: Record<string, string>;
let workspacePreferences: Record<string, string>;
let writes: { manager: string; body: Record<string, unknown> }[];
let pendingRead: ((r: Response) => void) | undefined;
const json = (value: unknown) => new Response(JSON.stringify(value));
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const select = (key: string) => host.querySelector<HTMLSelectElement>(`select[aria-label="${t(key)}"]`)!;
async function choose(key: string, value: string) {
  await act(async () => { const node = select(key); node.value = value; node.dispatchEvent(new Event("change", { bubbles: true })); });
  await flush();
}
// the editor follows the current assistant (Settings › "<name>'s settings" has no picker of its own)
async function current(id: string) {
  await act(async () => chooseManager(id));
  await flush();
}
const permission = "settings-assistantsettings.default_permissions";
// Check ownership and preference gating independently of loading the optional editor.
beforeAll(async () => { await import("../../ui/WorkspaceNaming"); });
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=assistant");
  resetManagerChoice(); setRosterDefault("assistant");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  modes = { assistant: "plan", work: "auto" }; workspacePreferences = { assistant: "prefer-existing", work: "prefer-new" }; writes = []; pendingRead = undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    const manager = new Headers(init?.headers).get("X-Persona-Manager")!;
    expect(["assistant", "work"]).toContain(manager);
    if (path.endsWith("/assistant")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)); writes.push({ manager, body });
        if (body.default_permission_mode) modes[manager] = body.default_permission_mode;
        if (body.task_workspace_preference) workspacePreferences[manager] = body.task_workspace_preference;
      }
      return json({ manager, workspace_naming_version: "workspace-naming/v1", default_permission_mode: modes[manager], task_workspace_preference: workspacePreferences[manager], notes: manager });
    }
    if (path.endsWith("/tasks")) return json({ tasks: [] });
    if (path.endsWith("/memory")) return json({ files: [] });
    throw new Error(`Unexpected path: ${path}`);
  }));
});
afterEach(async () => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<AssistantSettings />)); await flush(); }
it("saves workspace preferences on the current assistant and reloads them independently", async () => {
  const key = "settings-assistantsettings.workspace_preference";
  await render();
  expect(select(key).value).toBe("prefer-existing");
  await choose(key, "prefer-new");
  await current("work");
  expect(select(key).value).toBe("prefer-new");
  await choose(key, "prefer-existing");
  await current("assistant");
  expect(select(key).value).toBe("prefer-new");
  expect(writes).toEqual([
    { manager: "assistant", body: { task_workspace_preference: "prefer-new" } },
    { manager: "work", body: { task_workspace_preference: "prefer-existing" } },
  ]);
});
it("reads and saves the current assistant's, the default named explicitly, and the other's after a switch", async () => {
  await render(); expect(select(permission).value).toBe("plan");
  // no picker of its own: the current assistant's settings
  expect(host.querySelector(`select[aria-label="${t("settings-assistantsettings.configuration_target")}"]`)).toBeNull();
  await current("work"); expect(select(permission).value).toBe("auto");
  expect(new URLSearchParams(location.search).get("manager")).toBeNull();
  // its model links open the models page, which is the current assistant's too
  expect(host.querySelector<HTMLAnchorElement>('a[href*="purpose=agent.new"]')!.getAttribute("href")).toBe("/settings?tab=models&purpose=agent.new#settings-model-config");
  expect([...host.querySelectorAll<HTMLAnchorElement>("a")].map((a) => a.getAttribute("href"))).toContain("/settings?tab=models&purpose=assistant#settings-model-config");
  await choose(permission, "bypassPermissions");
  await current("assistant"); await choose(permission, "default");
  await current("work"); expect(select(permission).value).toBe("bypassPermissions");
  expect(writes).toEqual([
    { manager: "work", body: { default_permission_mode: "bypassPermissions" } },
    { manager: "assistant", body: { default_permission_mode: "default" } },
  ]);
});
it("a late read of the assistant it left cannot replace the new one's", async () => {
  const real = globalThis.fetch;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith("/assistant") && new Headers(init?.headers).get("X-Persona-Manager") === "assistant") {
      return new Promise<Response>((resolve) => { pendingRead = resolve; });
    }
    return real(input, init);
  }));
  await render(); await current("work");
  await act(async () => pendingRead!(json({ manager: "assistant", default_permission_mode: "plan" })));
  await flush(); expect(select(permission).value).toBe("auto");
  expect(writes).toEqual([]);
});
it("an old server without manager identity cannot accept a new default permission write", async () => {
  const real = globalThis.fetch;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).endsWith("/assistant") ? Promise.resolve(json({ notes: "old" })) : real(input, init)));
  await render(); expect(select(permission).disabled).toBe(true); expect(writes).toEqual([]);
});

it("pins model configuration reads and edits to the current assistant", async () => {
  const requests: { manager: string; method: string; path: string }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    const manager = new Headers(init?.headers).get("X-Persona-Manager")!;
    requests.push({ manager, method: init?.method ?? "GET", path });
    const purpose = { id: "agent.new", name: `${manager} Agents`, group: "Agents", kind: "launcher", revision: 1, fields: ["model"], override: {}, effective: { model: manager } };
    return json(path.endsWith("/model-config") ? { purposes: [purpose], providers: [] } : { purpose, providers: [] });
  }));
  function Harness() { const [open, onOpen] = useState<string | null>(null); return <ModelConfiguration open={open} onOpen={onOpen} onProviders={() => {}} />; }
  await act(async () => root.render(<Harness />)); await flush();
  await current("work");
  expect(host.querySelector('[data-model-purpose="agent.new"]')!.textContent).toContain("work");
  await act(async () => host.querySelector<HTMLButtonElement>('[data-model-purpose="agent.new"] button')!.click()); await flush();
  await act(async () => [...document.querySelectorAll("button")].find((b) => b.textContent === t("common.save"))!.click()); await flush();
  expect(requests.filter((r) => r.method === "PATCH")).toEqual([{ manager: "work", method: "PATCH", path: "/api/v1/model-config/purposes/agent.new" }]);
  expect(requests.every((r) => ["assistant", "work"].includes(r.manager))).toBe(true);
});

it("keeps a pending save and leaving-page notes pinned to their original owner", async () => {
  const real = globalThis.fetch;
  let finish: ((r: Response) => void) | undefined;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "PUT" && String(init.body).includes("default_permission_mode")) {
      const manager = new Headers(init.headers).get("X-Persona-Manager")!;
      writes.push({ manager, body: JSON.parse(String(init.body)) });
      return new Promise<Response>((resolve) => { finish = resolve; });
    }
    return real(input, init);
  }));
  await render();
  const notes = host.querySelector("textarea")!;
  act(() => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(notes, "draft for personal"); notes.dispatchEvent(new Event("input", { bubbles: true })); });
  await choose(permission, "bypassPermissions");
  await current("work");
  await act(async () => finish!(json({ manager: "assistant", default_permission_mode: "bypassPermissions" })));
  await flush();
  expect(select(permission).value).toBe("auto");
  expect(writes).toEqual([
    { manager: "assistant", body: { default_permission_mode: "bypassPermissions" } },
    { manager: "assistant", body: { notes: "draft for personal" } },
  ]);
});

const namingEntry = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "Workspace naming");
it("keeps the supported naming section beside Starting tasks for either preference", async () => {
  await render();
  const section = host.querySelector('[data-settings-group="workspace-naming"]')!;
  const dispatch = select("settings-assistantsettings.workspace_preference").closest("section")!;
  expect(section.contains(namingEntry()!)).toBe(true);
  expect(dispatch.contains(section)).toBe(false);
  await choose("settings-assistantsettings.workspace_preference", "prefer-new");
  expect(section.contains(namingEntry()!)).toBe(true);
  await choose("settings-assistantsettings.workspace_preference", "prefer-existing");
  expect(namingEntry()!.getAttribute("aria-expanded")).toBe("false");
  expect(writes.every(({ body }) => Object.keys(body).length === 1 && "task_workspace_preference" in body)).toBe(true);
});
it.each([undefined, "unknown"])("hides naming for missing or unknown capability %s", async (version) => {
  const real = globalThis.fetch;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).endsWith("/assistant")
    ? Promise.resolve(json({ manager: "assistant", workspace_naming_version: version, task_workspace_preference: "prefer-new" })) : real(input, init)));
  await render(); expect(namingEntry()).toBeUndefined();
});
it("waits for matching manager confirmation and preserves preference after a failed save", async () => {
  const real = globalThis.fetch;
  let finish: ((r: Response) => void) | undefined;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).endsWith("/assistant")
    ? new Promise<Response>((resolve) => { finish = resolve; }) : real(input, init)));
  await render(); expect(namingEntry()).toBeUndefined();
  await act(async () => finish!(json({ manager: "other", workspace_naming_version: "workspace-naming/v1", task_workspace_preference: "prefer-new" })));
  await flush(); expect(namingEntry()).toBeUndefined();
  await current("work"); expect(namingEntry()).toBeUndefined();
  await act(async () => finish!(json({ manager: "work", task_workspace_preference: "prefer-existing" })));
  await flush(); await choose("settings-assistantsettings.workspace_preference", "prefer-new");
  expect(namingEntry()).toBeUndefined();
  await act(async () => finish!(new Response("{}", { status: 503 })));
  await flush(); expect(namingEntry()).toBeUndefined();
  expect(select("settings-assistantsettings.workspace_preference").value).toBe("prefer-existing");
});
