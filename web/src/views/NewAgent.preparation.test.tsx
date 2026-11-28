// @vitest-environment jsdom
// Exercise the real component, launchAgent/deliverLaunch and REST client with synthetic, abort-ignoring HTTP.
import { act, useEffect, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyMachine, getState } from "../lib/store";
import { credentialOwner, handleUnauthorized, setToken } from "../lib/api";
import { onOpenNewAgent, openNewAgent, type NewAgentPreset, type WorkspaceTarget } from "../lib/new-agent";
import { resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import { refreshWorkspaceCatalog } from "../lib/workspace-catalog";
import { NewAgent } from "./NewAgent";
import { resetModelListsForTests } from "../lib/session-models";
import type { LauncherModels, PickerTier } from "../lib/launcher-models";
import { Toasts } from "../ui/atoms";
import { t } from "../i18n";

// no decoder in jsdom: a picture within the limits goes as it is, as lib/image-scale.ts sends one (#12973)
vi.mock("../lib/image-scale", () => ({ fitPicture: async (b: Blob) => ({ mime: b.type, data: "AQID" }) }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const before = { ...getState() };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
type Call = { url: string; body?: Record<string, unknown>; headers: Headers; signal?: AbortSignal | null };
type Pending = Call & { answer: (value: unknown, status?: number) => void; reject: (error: unknown) => void };
let calls: Call[];
let pending: Pending[];
let root: Root;
let mounted: boolean;
let host: HTMLDivElement;
let closes: number;
let configured: boolean;
let configuredProvider: string;
let launcherRows: import("../lib/types").Launcher[];
let launcherTiers: PickerTier[];
let launcherModels: Record<string, Omit<LauncherModels, "agent">>;
let modelsStatus: number;
let unauthorized: ReturnType<typeof vi.fn<() => void>>;
// the hub's workspace catalog (#7259); empty unless a test lists some
let catalogRows: Record<string, unknown>[] = [];
const flush = () => act(async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); });
const tick = (ms = 0) => act(async () => { await vi.advanceTimersByTimeAsync(0); await vi.dynamicImportSettled(); await vi.advanceTimersByTimeAsync(ms); });
const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === text)!;
const textarea = () => document.querySelector<HTMLTextAreaElement>("textarea")!;
const starts = () => calls.filter((c) => c.url.endsWith("/sessions"));
const messages = () => calls.filter((c) => c.url.endsWith("/messages"));
const worktree = { path: "/projects/demo-isolated", root: "/projects/demo", branch: "persona/demo" };
async function input(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  await act(async () => {
    const prototype = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const type = (text: string) => input(textarea(), text);
const click = (text: string) => act(async () => button(text).click());
function Harness() {
  const [open, setOpen] = useState(true);
  const [preset, setPreset] = useState<NewAgentPreset>();
  useEffect(() => onOpenNewAgent((next) => { setPreset(next); setOpen(true); }), []);
  return <><button onClick={() => setOpen(true)}>Reopen start</button><button onClick={() => setOpen(false)}>Close externally</button>
    <NewAgent open={open} preset={preset} onClose={() => { closes++; setOpen(false); }} /><Toasts /></>;
}
const workspace = (overrides: Partial<WorkspaceTarget> = {}): WorkspaceTarget => ({
  machineId: "computer", workspaceId: "nested-empty", host: "orca", cwd: "/projects/demo",
  label: "Empty nested workspace", owner: credentialOwner(), manager: "assistant", ...overrides,
});
async function target(next: WorkspaceTarget) {
  await act(async () => openNewAgent({ workspace: next }));
  await flush();
}
async function prepare() {
  await tick(401);
  const sw = document.querySelector<HTMLButtonElement>('[data-testid="new-agent-worktree"] [role="switch"]');
  expect(sw).not.toBeNull();
  if (sw!.getAttribute("aria-checked") !== "true") await act(async () => sw!.click());
  await click(t("newagent.start"));
  await flush();
  expect(starts()).toHaveLength(0);
  return pending.at(-1)!;
}
async function answer(request: Pending, value: unknown = worktree, status = 200) {
  await act(async () => request.answer(value, status));
  await flush();
  await tick();
}
async function attach(name: string, image = false) {
  const el = document.querySelector<HTMLInputElement>('input[type="file"]')!;
  await act(async () => {
    const file = new File(["Synthetic attachment"], name, { type: image ? "image/png" : "text/plain" });
    Object.defineProperty(file, "arrayBuffer", { value: async () => new TextEncoder().encode("Synthetic attachment").buffer });
    Object.defineProperty(el, "files", { configurable: true, value: [file] });
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

beforeEach(async () => {
  resetModelListsForTests();
  vi.useFakeTimers();
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  calls = []; pending = []; closes = 0; configured = false; configuredProvider = "claude"; launcherRows = [{ name: "claude", agent: "claude" }]; catalogRows = [];
  launcherTiers = [];
  launcherModels = {};
  modelsStatus = 200;
  let preview = 0;
  vi.stubGlobal("URL", class extends URL {
    static createObjectURL = vi.fn(() => `blob:synthetic-preview-${++preview}`);
    static revokeObjectURL = vi.fn();
  });
  localStorage.clear();
  resetManagerChoice();
  setToken("synthetic-credential-a");
  unauthorized = vi.fn(); handleUnauthorized(unauthorized);
  localStorage.setItem("new.machine", "computer");
  localStorage.setItem("new.cwd.computer", "/projects/demo");
  localStorage.setItem("new.cwd.second", "/projects/other");
  Object.assign(getState(), { loaded: true, machines: [
    { id: "computer", name: "Synthetic computer", online: true },
    { id: "second", name: "Second computer", online: true },
  ], sessions: {}, launches: {}, features: [], interactions: [], answered: {} });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const call: Call = { url, body: typeof init.body === "string" ? JSON.parse(init.body) : undefined, headers: new Headers(init.headers), signal: init.signal };
    calls.push(call);
    if (url.includes("/model-config/purposes/")) return configured
      ? json({ purpose: { id: "agent.new", name: "New agent", effective: { provider: `launcher.${configuredProvider}`, model: "synthetic-model", effort: "high" } }, providers: [] })
      : json({ error: "not found" }, 404);
    if (url.includes("/launchers/") && url.includes("/models")) {
      const launcher = launcherRows.find((l) => url.includes(`/launchers/${l.name}/`));
      if (modelsStatus !== 200) return json({ error: "Synthetic model list unavailable" }, modelsStatus);
      const catalog = launcherModels[launcher?.name ?? ""] ?? { models: [{ id: "synthetic-model", name: "Synthetic model", efforts: ["low", "high"] }] };
      const host = new URL(url, "http://localhost").searchParams.get("host");
      return json({ agent: launcher?.agent_type || launcher?.agent || "claude", ...catalog,
        supports_start_model: host === "herdr" || host === "orca" ? catalog.supports_start_model : undefined });
    }
    if (url.endsWith("/launchers")) return json({ launchers: launcherRows, tiers: launcherTiers });
    if (url.includes("/dirs?")) return json({ path: "/projects", entries: [] });
    if (url.endsWith("/worktree/status")) return json({ path: call.body!.cwd, dirty: 0, unmerged: 0 });
    if (url.endsWith("/worktree/add")) return new Promise<Response>((resolve, reject) => {
      pending.push({ ...call, answer: (value, status = 200) => resolve(json(value, status)), reject });
    });
    if (url.endsWith("/sessions")) return json({ session: { machine_id: call.body!.machine_id, session_id: "synthetic-started", title: "Synthetic started", status: "idle" } });
    if (url.includes("/files?")) return json({ path: "/uploads/submitted.txt" });
    if (url.endsWith("/workspaces")) return json({ workspaces: catalogRows });
    return json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  root = createRoot(host); mounted = true;
  await act(async () => root.render(<Harness />));
  await flush();
  await type("Submitted task");
});
afterEach(async () => {
  if (mounted) await act(async () => root.unmount());
  host.remove();
  Object.assign(getState(), before);
  handleUnauthorized(() => {});
  resetManagerChoice();
  localStorage.clear();
  vi.unstubAllGlobals(); vi.useRealTimers();
  history.replaceState({}, "", "/");
});

describe("New Agent preparation ownership", () => {
  it.each(["Cancel", "Escape", "outside", "external", "unmount"])("retires %s before an abort-ignoring success can create or send", async (dismissal) => {
    const request = await prepare();
    if (dismissal === "Cancel") await click(t("common.cancel"));
    if (dismissal === "Escape") await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    if (dismissal === "outside") await act(async () => document.querySelector('[role="dialog"]')!.parentElement!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    if (dismissal === "external") await click("Close externally");
    if (dismissal === "unmount") { await act(async () => root.unmount()); mounted = false; }
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    await answer(request);
    expect(starts()).toHaveLength(0); expect(messages()).toHaveLength(0);
    expect(request.signal?.aborted).toBe(true);
    expect(Object.keys(getState().launches)).toHaveLength(0);
    expect(localStorage.getItem("draft:new-agent")).toBe("Submitted task");
    expect(calls.some((c) => c.url.includes("/worktree/remove"))).toBe(false);
  });

  it.each(["success", "error", "rejection"])("keeps a reopened draft and its new pending request after old %s/finally", async (result) => {
    const old = await prepare();
    await click(t("common.cancel")); await click("Reopen start"); await flush();
    await type("Next task"); await attach("next.txt");
    const current = await prepare();
    expect(current).not.toBe(old);
    if (result === "rejection") { await act(async () => old.reject(new Error("Retired preparation failed"))); await flush(); }
    else await answer(old, result === "success" ? worktree : { error: "Retired preparation failed" }, result === "success" ? 200 : 500);
    expect(closes).toBe(1); expect(textarea().value).toBe("Next task");
    expect(localStorage.getItem("draft:new-agent")).toBe("Next task");
    expect(document.body.textContent).toContain("next.txt");
    expect(document.body.textContent).not.toContain("Retired preparation failed");
    expect(button(t("newagent.start")).disabled).toBe(true);
    expect(starts()).toHaveLength(0);
    await answer(current);
    expect(starts()).toHaveLength(1); expect(messages()).toHaveLength(1);
    expect(messages()[0].body!.text).toContain("Next task");
    expect(closes).toBe(2);
  });

  it.each(["machine", "folder", "machine manager", "default manager", "credential", "offline"])("retires preparation when the %s changes", async (change) => {
    const request = await prepare();
    if (change === "machine") await click("Second computer");
    if (change === "folder") await input(document.querySelector<HTMLInputElement>('input[spellcheck="false"]')!, "/projects/changed");
    if (change === "machine manager") await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: true, manager: "other-manager" }));
    if (change === "default manager") await act(async () => setRosterDefault("other-manager"));
    if (change === "credential") await act(async () => setToken("synthetic-credential-b"));
    if (change === "offline") await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: false }));
    await flush();
    await answer(request);
    expect(starts()).toHaveLength(0); expect(messages()).toHaveLength(0);
    expect(closes).toBe(0); expect(textarea().value).toBe("Submitted task");
    expect(request.signal?.aborted).toBe(true);
  });

  it("fences an old credential's 401 but preserves current 401 recovery and retry", async () => {
    const old = await prepare();
    expect(old.headers.get("Authorization")).toBe("Bearer synthetic-credential-a");
    await act(async () => setToken("synthetic-credential-b"));
    await answer(old, { error: "Old authorization expired" }, 401);
    expect(unauthorized).not.toHaveBeenCalled();
    expect(document.body.textContent).not.toContain("Old authorization expired");
    const current = await prepare();
    expect(current.headers.get("Authorization")).toBe("Bearer synthetic-credential-b");
    await answer(current, { error: "Current authorization expired" }, 401);
    expect(unauthorized).toHaveBeenCalledOnce();
    expect(document.querySelector('[role="alert"]')!.textContent).toContain("Current authorization expired");
    expect(button(t("newagent.start")).disabled).toBe(false);
    await answer(await prepare());
    expect(starts()).toHaveLength(1); expect(messages()).toHaveLength(1);
  });

  it("retries a current preparation error without losing the draft or its attachments", async () => {
    await attach("submitted.txt");
    const first = await prepare();
    await answer(first, { error: "Synthetic worktree conflict" }, 409);
    expect(button(t("newagent.start")).disabled).toBe(false);
    expect(document.querySelector('[role="alert"]')!.textContent).toContain("Synthetic worktree conflict");
    expect(textarea().value).toBe("Submitted task"); expect(document.body.textContent).toContain("submitted.txt");
    await answer(await prepare());
    expect(starts()).toHaveLength(1); expect(messages()).toHaveLength(1);
    expect(messages()[0].body!.text).toBe("Submitted task\n/uploads/submitted.txt");
    expect(localStorage.getItem("draft:new-agent")).toBeNull();
    await click("Reopen start");
    expect(textarea().value).toBe(""); expect(document.body.textContent).not.toContain("submitted.txt");
  });

  it("admits only one Start across repeated click and shortcut events", async () => {
    await tick(401);
    await act(async () => document.querySelector<HTMLButtonElement>('[data-testid="new-agent-worktree"] [role="switch"]')!.click());
    await act(async () => {
      button(t("newagent.start")).click();
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
      button(t("newagent.start")).click();
    });
    expect(pending).toHaveLength(1);
    await answer(pending[0]);
    expect(starts()).toHaveLength(1); expect(messages()).toHaveLength(1);
  });

  it.each(["different text", "edit away and back"])("submits its snapshot and keeps %s plus later attachments", async (edit) => {
    await attach("submitted.txt");
    const request = await prepare();
    await type("Later task");
    if (edit === "edit away and back") await type("Submitted task");
    await attach("later.txt");
    await answer(request);
    expect(starts()).toHaveLength(1);
    expect(starts()[0].body).toMatchObject({ machine_id: "computer", cwd: worktree.path, async: true });
    expect(messages()[0].body!.text).toBe("Submitted task\n/uploads/submitted.txt");
    const expected = edit === "different text" ? "Later task" : "Submitted task";
    expect(localStorage.getItem("draft:new-agent")).toBe(expected);
    await click("Reopen start");
    expect(textarea().value).toBe(expected);
    expect(document.body.textContent).toContain("later.txt");
    expect(document.body.textContent).not.toContain("submitted.txt");
    expect(calls.filter((c) => c.url.includes("/files?"))).toHaveLength(1);
  });

  it("keeps admitted image content after removal and gives launch previews their own lifetime", async () => {
    await attach("submitted.png", true);
    const original = document.querySelector<HTMLImageElement>('img[alt="submitted.png"]')!.src;
    const request = await prepare();
    await act(async () => document.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-attachmenttray.remove", { name: "submitted.png" })}"]`)!.click());
    expect(URL.revokeObjectURL).toHaveBeenCalledWith(original);
    await attach("later.png", true);
    await act(async () => request.answer(worktree));
    await flush();
    const launch = Object.values(getState().launches)[0];
    expect(launch.attachments[0].name).toBe("submitted.png");
    expect(launch.attachments[0].url).not.toBe(original);
    expect(URL.revokeObjectURL).not.toHaveBeenCalledWith(launch.attachments[0].url);
    await tick();
    // the scaler (lib/image-scale.ts) is a lazy chunk that loads in real time
    await act(async () => void (await vi.waitFor(() => expect(messages().length).toBeGreaterThan(0))));
    expect(messages()[0].body).toMatchObject({ text: "Submitted task", images: [{ mime: "image/png" }] });
    await click("Reopen start");
    expect(document.querySelector('img[alt="later.png"]')).not.toBeNull();
    expect(document.querySelector('img[alt="submitted.png"]')).toBeNull();
  });

  it("keeps the admitted model settings and host while later choices remain editable", async () => {
    configured = true;
    await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: true, engine_hosts: ["herdr", "orca"], engine_default: "herdr" }));
    await click(t("common.cancel")); await click("Reopen start"); await flush();
    await click("Orca");
    const request = await prepare();
    // the engine it opens in says where its worktree goes, and makes it
    expect(calls.filter((c) => c.url.endsWith("/worktree/status")).at(-1)?.body).toMatchObject({ kind: "orca" });
    expect(request.body).toMatchObject({ kind: "orca" });
    await act(async () => document.querySelector<HTMLButtonElement>('[data-testid="start-host"] button')!.click());
    // a model picked in the panel after admission changes the form, not the start on its way
    await act(async () => document.querySelector<HTMLButtonElement>("[data-testid=new-agent-model] [data-model-pill]")!.click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
    await act(async () => [...document.querySelectorAll<HTMLElement>("[data-panel-row]")].find((r) => r.dataset.panelRow === "model:claude\nsynthetic-model")!.click());
    await flush();
    expect(document.querySelector("[data-model-panel]")).toBeNull();
    await answer(request);
    expect(starts()).toHaveLength(1);
    expect(starts()[0].body).toMatchObject({ cwd: worktree.path, host: "orca" });
    // Following the shared purpose leaves model/effort resolution to the hub.
    expect(starts()[0].body!.launcher).toBeUndefined();
    expect(starts()[0].body!.model).toBeUndefined();
    expect(starts()[0].body!.effort).toBeUndefined();
    expect(messages()).toHaveLength(1);
  });

  it("starts directly through the existing launch flow and clears only the submitted content", async () => {
    await attach("submitted.txt");
    await click(t("newagent.start"));
    expect(closes).toBe(1);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    await tick();
    expect(pending).toHaveLength(0);
    expect(starts()).toHaveLength(1); expect(starts()[0].body).toMatchObject({ cwd: "/projects/demo", launcher: "claude", async: true });
    expect(messages()[0].body!.text).toBe("Submitted task\n/uploads/submitted.txt");
    expect(localStorage.getItem("draft:new-agent")).toBeNull();
  });
});

describe("New Agent exact workspace destinations", () => {
  it.each([
    { host: "orca", initial: "herdr", current: "herdr" },
    { host: "herdr", initial: "herdr", current: "orca" },
    { host: "herdr-core", initial: "herdr-core", current: "herdr-core" },
  ])("retains $host when the machine default goes from $initial to $current", async ({ host, initial, current }) => {
    await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: true, engine_hosts: ["herdr", "herdr-core", "orca"], engine_default: initial }));
    const selected = workspace({ host });
    await target(selected);
    const card = document.querySelector('[data-testid="new-agent-workspace"]')!;
    expect(card.textContent).toContain(selected.label);
    expect(card.textContent).toContain(selected.cwd);
    expect(card.textContent).not.toContain(selected.workspaceId);
    expect(card.textContent).not.toContain(selected.owner);
    expect(document.querySelector('input[placeholder="' + t("newagent.folder_placeholder") + '"]')).toBeNull();
    expect(document.querySelector('[data-testid="start-host"]')).toBeNull();
    expect(button("Second computer").disabled).toBe(true);
    await tick(401);
    expect(document.querySelector('[data-testid="new-agent-worktree"]')).toBeNull();
    expect(calls.filter((c) => c.url.endsWith("/worktree/status"))).toHaveLength(0);
    await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: true, engine_hosts: ["herdr", "herdr-core", "orca"], engine_default: current }));
    await click(t("newagent.start")); await tick();
    expect(starts()).toHaveLength(1);
    expect(starts()[0].body).toMatchObject({ machine_id: "computer", cwd: selected.cwd, workspace_id: selected.workspaceId, host, launcher: "claude", async: true });
    expect(pending).toHaveLength(0);
    expect(messages()[0].body!.text).toBe("Submitted task");
  });

  it("replaces the exact workspace identity when two workspaces share a directory", async () => {
    await target(workspace({ workspaceId: "parent", label: "Parent workspace" }));
    await target(workspace({ workspaceId: "nested-empty", label: "Empty nested workspace" }));
    expect(document.querySelector('[data-testid="new-agent-workspace"]')!.textContent).not.toContain("Parent workspace");
    await click(t("newagent.start")); await tick();
    expect(starts()).toHaveLength(1);
    expect(starts()[0].body).toMatchObject({ workspace_id: "nested-empty", host: "orca", cwd: "/projects/demo" });
  });

  it("uses the workspace's machine and directory over legacy preset fields", async () => {
    const selected = workspace({ machineId: "second", cwd: "/projects/child" });
    await act(async () => openNewAgent({ machineId: "computer", cwd: "/ignored", workspace: selected }));
    await flush();
    await click(t("newagent.start")); await tick();
    expect(starts()[0].body).toMatchObject({ machine_id: "second", cwd: selected.cwd, workspace_id: selected.workspaceId, host: "orca" });
  });

  it("refreshes Git eligibility when folder mode clears an unchanged directory's target", async () => {
    await target(workspace());
    await tick(401);
    expect(calls.filter((c) => c.url.endsWith("/worktree/status"))).toHaveLength(0);
    await click(t("newagent-workspace.choose_folder"));
    expect(document.querySelector('[data-testid="new-agent-workspace"]')).toBeNull();
    expect(button("Second computer").disabled).toBe(false);
    expect(document.querySelector<HTMLInputElement>('input[spellcheck="false"]')!.value).toBe("/projects/demo");
    await answer(await prepare());
    expect(starts()).toHaveLength(1);
    expect(starts()[0].body).toMatchObject({ cwd: worktree.path });
    expect(starts()[0].body).not.toHaveProperty("workspace_id");
    expect(starts()[0].body).not.toHaveProperty("host");
  });

  it.each(["owner", "manager", "credential change", "machine manager change", "default manager change"])("blocks a stale %s while retaining the draft", async (change) => {
    await target(workspace(change === "owner" ? { owner: "retired-owner" } : change === "manager" ? { manager: "retired-manager" } : {}));
    if (change === "credential change") await act(async () => setToken("synthetic-credential-b"));
    if (change === "machine manager change") await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: true, manager: "other-manager" }));
    if (change === "default manager change") await act(async () => setRosterDefault("other-manager"));
    await flush();
    expect(button(t("newagent.start")).disabled).toBe(true);
    expect(document.querySelector('[role="alert"]')!.textContent).toBe(t("newagent-workspace.owner_changed"));
    await act(async () => textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true })));
    await tick();
    expect(starts()).toHaveLength(0);
    expect(textarea().value).toBe("Submitted task");
  });

  it("keeps an offline destination selected instead of substituting another machine", async () => {
    await target(workspace({ machineId: "second" }));
    await act(async () => applyMachine({ id: "second", name: "Second computer", online: false }));
    await flush();
    expect(button(t("newagent.start")).disabled).toBe(true);
    expect(document.querySelector('[data-testid="new-agent-workspace"]')!.textContent).toContain("Empty nested workspace");
    expect(starts()).toHaveLength(0);
  });

  it.each(["credential", "manager"])("checks a changed %s synchronously before keyboard admission", async (change) => {
    await target(workspace());
    await act(async () => {
      if (change === "credential") setToken("synthetic-credential-b");
      else setRosterDefault("other-manager");
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
    });
    await tick();
    expect(starts()).toHaveLength(0);
    expect(localStorage.getItem("draft:new-agent")).toBe("Submitted task");
  });

  it("retires folder preparation before changing target without erasing the next draft or attachments", async () => {
    const old = await prepare();
    await target(workspace());
    await type("Next task"); await attach("next.txt");
    await answer(old);
    expect(old.signal?.aborted).toBe(true);
    expect(starts()).toHaveLength(0);
    expect(textarea().value).toBe("Next task");
    expect(localStorage.getItem("draft:new-agent")).toBe("Next task");
    expect(document.body.textContent).toContain("next.txt");
    expect(closes).toBe(0);
    await click(t("newagent.start")); await tick();
    expect(starts()).toHaveLength(1);
    expect(starts()[0].body).toMatchObject({ workspace_id: "nested-empty", host: "orca", cwd: "/projects/demo" });
    expect(messages()[0].body!.text).toBe("Next task\n/uploads/submitted.txt");
  });
});

describe("New Agent workspace list (#7259)", () => {
  const paseo = (id: string, extra: Record<string, unknown> = {}) => ({
    id: `computer/paseo/${id}`, machine_id: "computer", source: "paseo", source_id: id, cwd: `/projects/${id}`, title: id,
    project_id: "demo", project_name: "demo", workspace_kind: "git", ...extra,
  });
  async function list(rows: Record<string, unknown>[]) {
    catalogRows = rows;
    await act(async () => { await refreshWorkspaceCatalog(); });
    await flush();
  }
  const choices = () => [...document.querySelectorAll<HTMLButtonElement>("[data-workspace-choice]")];
  afterEach(async () => {
    catalogRows = [];
    await act(async () => { await refreshWorkspaceCatalog(); });
  });

  it("lists the computer's Paseo workspaces by project and starts the Agent in the one chosen", async () => {
    await list([
      paseo("fix-login", { branch: "fix-login" }),
      paseo("demo-main", { title: "demo", branch: "main", is_main: true }),
      paseo("notes", { project_id: undefined, project_name: undefined, workspace_kind: "folder" }),
      paseo("elsewhere", { machine_id: "second" }),
      { ...paseo("space"), source: "herdr" },
      paseo("gone", { available: false }),
    ]);
    const box = document.querySelector('[data-testid="new-agent-workspaces"]')!;
    expect([...box.querySelectorAll('[role="group"]')].map((g) => g.getAttribute("aria-label"))).toEqual(["demo", t("newagent-workspace.other")]);
    expect(choices().map((b) => b.textContent)).toEqual([
      `demo${t("newagent-workspace.kind_main")} · main/projects/demo-main`,
      `fix-login${t("newagent-workspace.kind_worktree")}/projects/fix-login`,
      `notes${t("newagent-workspace.kind_folder")}/projects/notes`,
    ]);
    expect(box.textContent).not.toContain("paseo");
    await act(async () => choices()[1].click());
    const card = document.querySelector('[data-testid="new-agent-workspace"]')!;
    expect(card.textContent).toContain("fix-login");
    expect(document.querySelector('[data-testid="new-agent-workspaces"]')).toBeNull();
    expect(document.querySelector('input[placeholder="' + t("newagent.folder_placeholder") + '"]')).toBeNull();
    await tick(401);
    // its repository can still give the Agent a worktree of its own (#8856), off until turned on
    expect(document.querySelector('[data-testid="new-agent-worktree"] [role="switch"]')!.getAttribute("aria-checked")).toBe("false");
    expect(button("Second computer").disabled).toBe(false);
    await click(t("newagent.start")); await tick();
    expect(starts()).toHaveLength(1);
    expect(starts()[0].body).toMatchObject({ machine_id: "computer", cwd: "/projects/fix-login", workspace_id: "fix-login", host: "paseo" });
    expect(pending).toHaveLength(0);
  });

  it("starts in a new worktree Paseo makes from the Paseo workspace chosen (#8856)", async () => {
    await list([paseo("demo-main", { title: "demo", branch: "main", is_main: true })]);
    await act(async () => choices()[0].click());
    const request = await prepare();
    // Paseo says where its worktree goes and makes it, from the workspace's folder
    expect(calls.filter((c) => c.url.endsWith("/worktree/status")).at(-1)?.body).toEqual({ cwd: "/projects/demo-main", kind: "paseo" });
    expect(request.body).toMatchObject({ cwd: "/projects/demo-main", kind: "paseo" });
    await answer(request, { path: "/paseo/demo/persona-submitted-task", branch: "persona/submitted-task", root: "/projects/demo-main", workspace_id: "made-for-it" });
    expect(starts()).toHaveLength(1);
    // into the workspace made for the worktree, not the one chosen
    expect(starts()[0].body).toMatchObject({ machine_id: "computer", cwd: "/paseo/demo/persona-submitted-task", workspace_id: "made-for-it", host: "paseo" });
  });

  it("starts in a new worktree Orca makes under the Orca workspace chosen (#17050)", async () => {
    await list([{ ...paseo("orca-row"), source: "orca" }]);
    await act(async () => choices()[0].click());
    const request = await prepare();
    // Orca says where its worktree goes and makes it, as a child of the workspace chosen
    expect(calls.filter((c) => c.url.endsWith("/worktree/status")).at(-1)?.body).toEqual({ cwd: "/projects/orca-row", kind: "orca" });
    expect(request.body).toMatchObject({ cwd: "/projects/orca-row", kind: "orca" });
    await answer(request, { path: "/orca/workspaces/demo/persona-submitted-task", branch: "persona/submitted-task", root: "/projects/orca-row", workspace_id: "made-for-it" });
    expect(starts()).toHaveLength(1);
    // into the worktree Orca made, not the one chosen
    expect(starts()[0].body).toMatchObject({ machine_id: "computer", cwd: "/orca/workspaces/demo/persona-submitted-task", workspace_id: "made-for-it", host: "orca" });
  });

  it("offers a chosen Orca workspace's worktree off, and a switch turned on does not carry to the next choice", async () => {
    await list([paseo("fix-login"), { ...paseo("orca-row"), source: "orca" }]);
    await act(async () => choices().find((b) => b.textContent?.includes("fix-login"))!.click());
    await tick(401);
    await act(async () => document.querySelector<HTMLButtonElement>('[data-testid="new-agent-worktree"] [role="switch"]')!.click());
    await click(t("newagent-workspace.choose_folder"));
    await act(async () => choices().find((b) => b.textContent?.includes("orca-row"))!.click());
    await tick(401);
    expect(document.querySelector('[data-testid="new-agent-worktree"] [role="switch"]')!.getAttribute("aria-checked")).toBe("false");
    await click(t("newagent.start")); await tick();
    expect(pending).toHaveLength(0);
    expect(starts()[0].body).toMatchObject({ cwd: "/projects/orca-row", workspace_id: "orca-row", host: "orca" });
  });

  it("goes back to a folder, and another computer drops the choice", async () => {
    await list([paseo("fix-login")]);
    await act(async () => choices()[0].click());
    await click(t("newagent-workspace.choose_folder"));
    expect(document.querySelector('[data-testid="new-agent-workspace"]')).toBeNull();
    expect(choices()).toHaveLength(1);
    await act(async () => choices()[0].click());
    await click("Second computer"); await flush();
    expect(document.querySelector('[data-testid="new-agent-workspace"]')).toBeNull();
    expect(document.querySelector('[data-testid="new-agent-workspaces"]')).toBeNull();
    await click(t("newagent.start")); await tick();
    expect(starts()[0].body).toMatchObject({ machine_id: "second", cwd: "/projects/other" });
    expect(starts()[0].body).not.toHaveProperty("workspace_id");
    expect(starts()[0].body).not.toHaveProperty("host");
  });

  it("shows no list when the computer has no Orca or Paseo workspace", async () => {
    await list([{ ...paseo("space"), source: "herdr" }, paseo("offline", { online: false })]);
    expect(document.querySelector('[data-testid="new-agent-workspaces"]')).toBeNull();
    expect(document.querySelector('input[placeholder="' + t("newagent.folder_placeholder") + '"]')).not.toBeNull();
  });
});

describe("New Agent with an omp launcher (#11964)", () => {
  const launcher = "omp";
  const model = "openai/gpt-5.2";
  const pill = () => document.querySelector<HTMLButtonElement>("[data-testid=new-agent-model] [data-model-pill]")!;
  const row = (id: string) => [...document.querySelectorAll<HTMLElement>("[data-panel-row]")].find((el) => el.dataset.panelRow === id);

  async function openPanel(name = launcher) {
    await act(async () => pill().click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
    const header = document.querySelector<HTMLButtonElement>(`[data-panel-launcher="${name}"][aria-expanded=false]`);
    if (header) await act(async () => header.click());
  }

  it.each(["typed", "listed", "preset", "launcher default"] as const)("sends the %s choice to the ordinary sessions endpoint", async (choice) => {
    await click("Close externally");
    launcherRows = [{ name: launcher, agent: "omp", source: "zshrc" }];
    launcherModels[launcher] = { supports_start_model: true, models: choice === "listed" ? [{ id: model, name: "Catalog model", efforts: ["high"] }] : [] };
    launcherTiers = [{ name: "Research", launcher, model, effort: "high" }];
    localStorage.setItem("new.mode.computer", "plan");
    resetModelListsForTests();
    await click("Reopen start"); await flush();
    await openPanel();
    if (choice === "typed") {
      await input(document.querySelector<HTMLInputElement>("[data-model-panel] input")!, model);
      const typed = row(`typed:${launcher}\n${model}`);
      expect(typed).toBeDefined();
      await act(async () => typed!.click());
      await openPanel();
      expect(row(`model:${launcher}\n${model}`)?.getAttribute("aria-pressed")).toBe("true");
      await act(async () => pill().click());
    } else {
      const rows = { preset: "preset:Research", listed: `model:${launcher}\n${model}`, "launcher default": `model:${launcher}\ndefault` };
      const pick = row(rows[choice]);
      expect(pick).toBeDefined();
      await act(async () => pick!.click());
    }
    await click(t("newagent.start")); await tick();
    expect(starts()).toHaveLength(1);
    const body = starts()[0].body!;
    expect(body).toMatchObject({ machine_id: "computer", launcher });
    if (choice === "launcher default") expect(body).not.toHaveProperty("model");
    else expect(body.model).toBe(model);
    for (const field of ["effort", "launcher_default_effort", "permission_mode", "ultracode", "work_session"]) {
      expect(body).not.toHaveProperty(field);
    }
  });

  it("drops a typed omp model when the user changes from herdr to another host", async () => {
    await click("Close externally");
    launcherRows = [{ name: launcher, agent: "omp", source: "zshrc" }];
    launcherModels[launcher] = { models: [], supports_start_model: true };
    await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: true, engine_hosts: ["herdr", "paseo"], engine_default: "herdr" }));
    resetModelListsForTests();
    await click("Reopen start"); await flush();
    await openPanel();
    await input(document.querySelector<HTMLInputElement>("[data-model-panel] input")!, model);
    const typed = row(`typed:${launcher}\n${model}`);
    expect(typed).toBeDefined();
    await act(async () => typed!.click());
    expect(pill().title).toContain(model);
    await click("Paseo"); await flush();
    expect(pill().title).not.toContain(model);
    await openPanel();
    await input(document.querySelector<HTMLInputElement>("[data-model-panel] input")!, model);
    expect(row(`typed:${launcher}\n${model}`)).toBeUndefined();
    await act(async () => pill().click());
    await click(t("newagent.start")); await tick();
    expect(starts()[0].body).toMatchObject({ launcher, host: "paseo" });
    expect(starts()[0].body).not.toHaveProperty("model");
  });

  it("checks the terminal host for omp even while a desktop launcher has its own destination", async () => {
    await click("Close externally");
    launcherRows = [{ name: launcher, agent: "omp", source: "zshrc" }, { name: "codex-app", agent: "codex", source: "codex_app" }];
    launcherModels[launcher] = { models: [], supports_start_model: true };
    launcherModels["codex-app"] = { models: [] };
    await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: true, engine_hosts: ["herdr", "paseo"], engine_default: "herdr" }));
    resetModelListsForTests();
    await click("Reopen start"); await flush();
    await click("Paseo"); await flush();
    await openPanel("codex-app");
    const desktop = row("model:codex-app\ndefault");
    expect(desktop).toBeDefined();
    await act(async () => desktop!.click());
    await openPanel();
    await input(document.querySelector<HTMLInputElement>("[data-model-panel] input")!, model);
    expect(row(`typed:codex-app\n${model}`)).toBeDefined();
    expect(row(`typed:${launcher}\n${model}`)).toBeUndefined();
  });

  it.each([
    ["older node with an empty catalog", 200, { models: [] }],
    ["unadvertised catalog entry", 200, { models: [{ id: model, name: "Listed" }] }],
    ["locked launcher", 200, { models: [{ id: model, name: "Listed" }], supports_start_model: true, locked: "Fixed by launcher" }],
    ["missing endpoint", 404, { models: [] }],
    ["failed catalog read", 503, { models: [] }],
  ] as const)("on %s offers its default and omits a preset's unsupported model", async (_node, status, catalog) => {
    await click("Close externally");
    launcherRows = [{ name: launcher, agent: "omp" }];
    launcherTiers = [{ name: "Research", launcher, model }];
    launcherModels[launcher] = { ...catalog, models: [...catalog.models] };
    modelsStatus = status;
    resetModelListsForTests();
    await click("Reopen start"); await flush();
    await openPanel();
    expect(row(`model:${launcher}\ndefault`)).toBeDefined();
    expect(row(`model:${launcher}\n${model}`)).toBeUndefined();
    const search = document.querySelector<HTMLInputElement>("[data-model-panel] input")!;
    await input(search, model);
    expect(row(`typed:${launcher}\n${model}`)).toBeUndefined();
    await input(search, "");
    await act(async () => row("preset:Research")!.click());
    expect(pill().title).not.toContain(model);
    await click(t("newagent.start")); await tick();
    expect(starts()[0].body).toMatchObject({ launcher });
    expect(starts()[0].body).not.toHaveProperty("model");
  });

  it("retires previously advertised omp support after a failed refresh", async () => {
    await click("Close externally");
    launcherRows = [{ name: launcher, agent: "omp" }];
    launcherModels[launcher] = { models: [], supports_start_model: true };
    resetModelListsForTests();
    await click("Reopen start"); await flush();
    await openPanel();
    await input(document.querySelector<HTMLInputElement>("[data-model-panel] input")!, model);
    await act(async () => row(`typed:${launcher}\n${model}`)!.click());
    expect(pill().title).toContain(model);
    modelsStatus = 503;
    await openPanel();
    expect(row(`model:${launcher}\n${model}`)).toBeUndefined();
    await act(async () => pill().click());
    expect(pill().title).not.toContain(model);
    await click(t("newagent.start")); await tick();
    expect(starts()[0].body).not.toHaveProperty("model");
  });

  it.each(["launcher default", "follow model settings"] as const)("shows and submits the %s when both refer to omp", async (choice) => {
    await click("Close externally");
    configured = true; configuredProvider = launcher;
    launcherRows = [{ name: launcher, agent: "omp", model: "omp-local-default" }];
    launcherModels[launcher] = { models: [], supports_start_model: true };
    resetModelListsForTests();
    await click("Reopen start"); await flush();
    expect(pill().title).toContain("synthetic-model");
    await openPanel();
    const local = row(`model:${launcher}\ndefault`)!;
    const follow = document.querySelector<HTMLElement>("[data-follow-row]")!;
    expect(local.textContent).toContain("omp-local-default");
    expect(local.textContent).not.toContain("synthetic-model");
    expect(follow.textContent).toContain("synthetic-model");
    await act(async () => (choice === "launcher default" ? local : follow).click());
    expect(pill().title).toContain(choice === "launcher default" ? "omp-local-default" : "synthetic-model");
    await click(t("newagent.start")); await tick();
    const body = starts()[0].body!;
    expect(body.launcher).toBe(choice === "launcher default" ? launcher : undefined);
    expect(body).not.toHaveProperty("model");
    expect(body).not.toHaveProperty("effort");
  });

  it("keeps a runtime-owned omp catalog and its advertised effort without enabling typed IDs", async () => {
    await click("Close externally");
    const runtime = "omp-provider";
    launcherRows = [{ name: runtime, agent: "omp", source: runtime, permission_modes: ["default"] }];
    launcherModels[runtime] = { models: [{ id: "runtime-model", name: "Runtime model", efforts: ["high"] }] };
    launcherTiers = [{ name: "Runtime", launcher: runtime, model: "runtime-model", effort: "high" }];
    resetModelListsForTests();
    await click("Reopen start"); await flush();
    await openPanel(runtime);
    expect(row(`model:${runtime}\nruntime-model`)).toBeDefined();
    const search = document.querySelector<HTMLInputElement>("[data-model-panel] input")!;
    await input(search, model);
    expect(row(`typed:${runtime}\n${model}`)).toBeUndefined();
    await input(search, "");
    await act(async () => row("preset:Runtime")!.click());
    expect(pill().title).toContain("Runtime model");
    await click(t("newagent.start")); await tick();
    expect(starts()[0].body).toMatchObject({ launcher: runtime, model: "runtime-model", effort: "high" });
    expect(starts()[0].body).not.toHaveProperty("permission_mode");
  });
});

describe("known terminal CLI text starts (#1842)", () => {
  it.each([false, true])("starts a text-only compatible alias inline (follow default: %s)", async (followDefault) => {
    await click("Close externally");
    launcherRows = [{ name: "work", agent_type: "traex" }];
    configured = followDefault;
    configuredProvider = "work";
    resetModelListsForTests();
    await click("Reopen start");
    await flush();
    await type("Inspect the project");
    const request = await prepare();
    await answer(request);
    expect(starts()).toHaveLength(1);
    expect(starts()[0].body).toMatchObject({ launcher: "work", agent_type: "traex", prompt: "Inspect the project" });
    expect(starts()[0].body).not.toHaveProperty("async");
    expect(messages()).toHaveLength(0);
  });
});

it.each([
  { kind: "traex", attachment: true, engine: "herdr", source: undefined },
  { kind: "claude", attachment: false, engine: "herdr", source: undefined },
  { kind: "agy", attachment: false, engine: "herdr", source: undefined },
  { kind: "omp", attachment: false, engine: "herdr", source: undefined },
  { kind: undefined, attachment: false, engine: "herdr", source: undefined },
  { kind: "traex", attachment: false, engine: "paseo", source: undefined },
  { kind: "traex", attachment: false, engine: "orca", source: undefined },
  { kind: "traex", attachment: false, engine: "herdr", source: "future-desktop" },
])("preserves existing negotiation for $kind/$engine/attachment=$attachment/source=$source", async ({ kind, attachment, engine, source }) => {
  await click("Close externally");
  launcherRows = [{ name: "work", agent_type: kind, source }];
  resetModelListsForTests();
  await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: true, engine_hosts: [engine], engine_default: engine }));
  await click("Reopen start"); await flush();
  await type("Preserved task");
  if (attachment) await attach("retained.txt");
  await tick(401);
  await click(t("newagent.start")); await tick();
  // Worktree preparation is not requested in this control; the existing start and message paths remain intact.
  expect(starts()).toHaveLength(1);
  expect(starts()[0].body).toMatchObject({ async: true, name_hint: "Preserved task" });
  expect(starts()[0].body).not.toHaveProperty("agent_type");
  expect(starts()[0].body).not.toHaveProperty("prompt");
  expect(messages()).toHaveLength(1);
  if (attachment) expect(messages()[0].body!.text).toContain("/uploads/submitted.txt");
});

it("preserves Codex inline creation with its advertised optional empty prompt", async () => {
  await click("Close externally");
  launcherRows = [{ name: "work", agent_type: "codex", prompt_optional: true }];
  resetModelListsForTests();
  await click("Reopen start"); await flush();
  await type(""); await tick(401);
  await click(t("newagent.start")); await tick();
  expect(starts()).toHaveLength(1);
  expect(starts()[0].body).toMatchObject({ agent_type: "codex" });
  expect(starts()[0].body).not.toHaveProperty("async");
  expect(starts()[0].body).not.toHaveProperty("prompt");
  expect(messages()).toHaveLength(0);
});

it("keeps an empty compatible CLI start on the existing asynchronous path", async () => {
  await click("Close externally");
  launcherRows = [{ name: "work", agent_type: "traex" }];
  resetModelListsForTests();
  await click("Reopen start"); await flush();
  await type(" "); await tick(401);
  await click(t("newagent.start")); await tick();
  expect(starts()).toHaveLength(1);
  expect(starts()[0].body).toMatchObject({ async: true });
  expect(starts()[0].body).not.toHaveProperty("agent_type");
  expect(starts()[0].body).not.toHaveProperty("prompt");
  expect(messages()).toHaveLength(0);
});
