// @vitest-environment jsdom
import { webcrypto } from "node:crypto";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { credentialOwner, setToken } from "../lib/api";
import { getState } from "../lib/store";
import { setRosterDefault } from "../lib/manager-choice";
import { deliverLaunch, launchAgent, onOpenNewAgent } from "../lib/new-agent";
import { NewWorktree } from "./NewWorktree";
import type { WorktreeRequest } from "../lib/worktree";
import { t } from "../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const info = { root: "/projects/demo", repoName: "Demo", path: "/worktrees", branch: "feature/current", composer: { project: "Demo", base_ref: "origin/main", refs: ["origin/main", "feature/existing"], presets: [], capabilities: ["create", "branch_override", "source.github", "sparse_presets", "folder_parent"] } };
const rows = [
  { id: "qualified-source-a", machine_id: "computer", source: "orca", source_id: "source-a", cwd: "/projects/demo", title: "Main checkout", project_id: "demo", project_name: "Demo", workspace_kind: "git" },
  { id: "qualified-source-b", machine_id: "computer", source: "orca", source_id: "source-b", cwd: "/projects/demo-b", title: "Other checkout", project_id: "demo", project_name: "Demo", workspace_kind: "git" },
  { id: "qualified-parent", machine_id: "computer", source: "orca", source_id: "folder-parent", cwd: "/collections", title: "Collection folder", project_id: "collection", project_name: "Collection", workspace_kind: "folder" },
];
let root: Root; let host: HTMLDivElement; let request: WorktreeRequest;
let fetcher: ReturnType<typeof vi.fn<(url: string, init: { body: string; headers: HeadersInit }) => Promise<Response>>>; let closes: ReturnType<typeof vi.fn<() => void>>;
let calls: { url: string; body?: Record<string, unknown>; headers: Headers }[];
const created = { workspace_id: "actual-new", path: "/worktrees/generated-actual", branch: "dev/actual", terminal: "pane:runtime-created-tab" };
const flush = async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 12)); }); };
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label)!;
const field = (label: string) => {
  const el = [...document.querySelectorAll("label")].find((l) => l.textContent === label)!;
  return document.getElementById(el.htmlFor) as HTMLInputElement;
};
const click = async (label: string) => { await act(async () => button(label).click()); await flush(); };
async function input(label: string, value: string) {
  await act(async () => {
    const el = field(label); const proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : el.tagName === "SELECT" ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event(el.tagName === "SELECT" ? "change" : "input", { bubbles: true }));
  });
}
async function mount() { await act(async () => root.render(<NewWorktree request={request} onClose={closes} />)); await flush(); }
const creates = () => calls.filter((c) => c.url.endsWith("/worktree/create"));

beforeEach(() => {
  vi.stubGlobal("crypto", webcrypto); localStorage.clear(); sessionStorage.clear();
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
  Element.prototype.scrollIntoView = vi.fn();
  setToken(`synthetic-component-${Math.random()}`); setRosterDefault("assistant");
  Object.assign(getState(), { loaded: true, launches: {}, sessions: {}, machines: [{ id: "computer", name: "Development computer", online: true, engine_hosts: ["orca"], engine_default: "orca" }] });
  closes = vi.fn(); calls = [];
  request = { target: { mid: "computer", workspaceId: "source-a", host: "orca", cwd: "/projects/demo", label: "Main checkout", owner: credentialOwner(), manager: "assistant" },
    parent: { machineId: "computer", workspaceId: "folder-parent", host: "orca", cwd: "/collections", label: "Collection folder", owner: credentialOwner(), manager: "assistant" }, changed: vi.fn(), onCreated: vi.fn() };
  fetcher = vi.fn(async (url, init) => {
    calls.push({ url: String(url), body: init?.body ? JSON.parse(init.body) : undefined, headers: new Headers(init?.headers) });
    if (String(url).endsWith("/workspaces")) return response({ workspaces: rows });
    if (String(url).endsWith("/worktree/prepare")) return response(info);
    if (String(url).endsWith("/worktree/create")) return response(created);
    return response({});
  }); vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); host.remove(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it("creates without an Agent by default and opens the explicit M3 stage using acknowledged IDs/path", async () => {
  const launch = vi.fn(); const stop = onOpenNewAgent(launch); await mount();
  await input(t("worktree-composer.name"), "Feature UI");
  await input(t("worktree-composer.branch"), "feature/existing");
  await click(t("worktree-composer.create"));
  expect(creates()).toHaveLength(1); expect(launch).not.toHaveBeenCalled();
  expect(creates()[0].body).toMatchObject({ workspace_id: "source-a", parent_workspace_id: "folder-parent", name: "Feature UI", base_ref: "origin/main", branch_override: "feature/existing" });
  expect(document.body.textContent).toContain("/worktrees/generated-actual");
  expect(request.onCreated).toHaveBeenCalledWith(created, expect.objectContaining({ workspaceId: "actual-new", cwd: "/worktrees/generated-actual", host: "orca" }));
  await click(t("worktree-composer.start_agent"));
  expect(launch).toHaveBeenCalledWith({ workspace: expect.objectContaining({ workspaceId: "actual-new", cwd: "/worktrees/generated-actual", owner: credentialOwner(), manager: "assistant", host: "orca" }) });
  expect(creates()).toHaveLength(1); expect(closes).toHaveBeenCalledOnce(); stop();
});

it("a Paseo workspace: the basic form in Paseo's folder, the source as its own parent, and an Agent is what opens the result", async () => {
  const paseoRow = { id: "qualified-paseo", machine_id: "computer", source: "paseo", source_id: "wks_0000000000000001", cwd: "/projects/demo", title: "main",
    project_id: "prj_demo", project_name: "Demo", workspace_kind: "git", is_main: true };
  const paseoCreated = { workspace_id: "wks_0000000000000002", path: "/paseo/worktrees/abcd1234/feature-child", branch: "feature/child" };
  Object.assign(getState(), { machines: [{ id: "computer", name: "Development computer", online: true, engine_hosts: ["paseo"], engine_default: "paseo" }] });
  const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => {
    if (String(url).endsWith("/workspaces")) return Promise.resolve(response({ workspaces: [paseoRow] }));
    if (String(url).endsWith("/worktree/prepare")) {
      calls.push({ url: String(url), body: JSON.parse(init.body), headers: new Headers(init.headers) });
      return Promise.resolve(response({ root: "/projects/demo", repoName: "Demo", branch: "main", path: "/paseo/worktrees/abcd1234", automatic_name: true }));
    }
    if (String(url).endsWith("/worktree/create")) {
      calls.push({ url: String(url), body: JSON.parse(init.body), headers: new Headers(init.headers) });
      return Promise.resolve(response(paseoCreated));
    }
    return original(url, init);
  });
  const source = { machineId: "computer", workspaceId: paseoRow.source_id, host: "paseo", cwd: "/projects/demo", label: "main", owner: credentialOwner(), manager: "assistant" };
  request = { target: { mid: "computer", workspaceId: paseoRow.source_id, host: "paseo", cwd: "/projects/demo", label: "main", owner: credentialOwner(), manager: "assistant" },
    source, parent: source, changed: vi.fn(), onCreated: vi.fn() };
  const launch = vi.fn(); const stop = onOpenNewAgent(launch); await mount();
  expect(calls.find((c) => c.url.endsWith("/worktree/prepare"))?.body).toEqual({ workspace_id: paseoRow.source_id, kind: "paseo" });
  expect(document.body.textContent).not.toContain(t("worktree-composer.legacy")); // Orca's update notice is not Paseo's
  await input(t("worktree.branch_label"), "feature/child"); await click(t("worktree-composer.create"));
  expect(creates()[0].body).toEqual({ workspace_id: paseoRow.source_id, kind: "paseo", branch: "feature/child", path: "/paseo/worktrees/abcd1234/feature-child" });
  expect(document.body.textContent).toContain(paseoCreated.path);
  expect(button(t("worktree-composer.open"))).toBeUndefined();
  await click(t("worktree-composer.start_agent"));
  expect(launch).toHaveBeenCalledWith({ workspace: expect.objectContaining({ workspaceId: paseoCreated.workspace_id, cwd: paseoCreated.path, host: "paseo" }) });
  stop();
});

it("close/reopen during a create retains its result and never overwrites a separate new draft", async () => {
  let answer!: (value: Response) => void;
  const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => {
    if (String(url).endsWith("/worktree/create")) { calls.push({ url: String(url), body: JSON.parse(init.body), headers: new Headers(init.headers) }); return new Promise((resolve) => { answer = resolve; }); }
    return original(url, init);
  });
  await mount(); await click(t("worktree-composer.create"));
  await click(t("common.cancel")); await act(async () => root.render(null));
  const oldRequest = request;
  request = { ...request, target: { ...request.target, workspaceId: "source-b", label: "Other checkout", cwd: "/projects/demo-b" } };
  await mount(); await input(t("worktree-composer.name"), "New draft stays");
  await act(async () => answer(response(created))); await flush();
  expect(field(t("worktree-composer.name")).value).toBe("New draft stays");
  expect(oldRequest.onCreated).not.toHaveBeenCalled();
  await act(async () => root.render(null)); request = oldRequest; await mount();
  expect(document.body.textContent).toContain("/worktrees/generated-actual"); expect(creates()).toHaveLength(1);
  expect(request.onCreated).toHaveBeenCalledWith(created, expect.objectContaining({ workspaceId: "actual-new" }));
});

it("rejects stale prepare and provider responses without wiping name/branch or a new source selection", async () => {
  let oldPrepare!: (value: Response) => void; let oldSearch!: (value: Response) => void;
  const original = fetcher.getMockImplementation()!; let deferPrepare = false;
  fetcher.mockImplementation((url, init) => {
    if (deferPrepare && String(url).endsWith("/worktree/prepare") && JSON.parse(init.body).workspace_id === "source-a") return new Promise((resolve) => { oldPrepare = resolve; });
    if (String(url).endsWith("/worktree/source")) return new Promise((resolve) => { oldSearch = resolve; });
    return original(url, init);
  });
  await mount(); await input(t("worktree-composer.name"), "My name"); await input(t("worktree-composer.branch"), "feature/intended");
  await input(t("worktree-composer.search_source"), "query"); await click(t("worktree-composer.search"));
  await input(t("worktree-composer.git_source"), JSON.stringify(["assistant", "computer", "orca", "source-b"])); await flush();
  deferPrepare = true;
  await input(t("worktree-composer.git_source"), JSON.stringify(["assistant", "computer", "orca", "source-a"])); await flush();
  await input(t("worktree-composer.git_source"), JSON.stringify(["assistant", "computer", "orca", "source-b"])); await flush();
  await act(async () => { oldPrepare(response({ ...info, composer: { ...info.composer, base_ref: "stale-base" } })); oldSearch(response({ items: [{ type: "issue", number: 1, title: "Stale source", url: "https://github.com/example/demo/issues/1" }] })); }); await flush();
  expect(field(t("worktree-composer.name")).value).toBe("My name"); expect(field(t("worktree-composer.branch")).value).toBe("feature/intended");
  expect(field(t("worktree-composer.base")).value).toBe("origin/main"); expect(document.body.textContent).not.toContain("Stale source");
  await click(t("worktree-composer.create")); expect(creates()[0].body?.workspace_id).toBe("source-b");
});

it("never shows pending/unknown as success and reuses the same request after reopening", async () => {
  let attempts = 0; const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => {
    if (String(url).endsWith("/worktree/create")) { calls.push({ url: String(url), body: JSON.parse(init.body), headers: new Headers(init.headers) }); attempts++; return Promise.resolve(response(attempts === 1 ? { state: "pending" } : created, attempts === 1 ? 202 : 200)); }
    if (String(url).includes("/ops/")) return Promise.resolve(response({ state: "done" }));
    return original(url, init);
  });
  await mount(); await click(t("worktree-composer.create"));
  expect(document.body.textContent).toContain(t("worktree-composer.pending")); expect(request.changed).not.toHaveBeenCalled();
  await act(async () => root.render(null)); await mount();
  await click(t("worktree-composer.check_result"));
  expect(creates()[1].headers.get("X-Persona-Op-Id")).toBe(creates()[0].headers.get("X-Persona-Op-Id"));
  expect(creates()[1].body).toEqual(creates()[0].body); expect(request.changed).toHaveBeenCalled();
});

it("blocks synchronous keyboard submit after credentials or manager ownership changes", async () => {
  await mount();
  await act(async () => { setToken("different-owner"); document.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
  expect(creates()).toHaveLength(0); expect(document.body.textContent).toContain(t("worktree.owner_changed"));
});

it("keeps the basic old-node branch/path fallback and exposes no advanced controls", async () => {
  request.parent = undefined;
  const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => String(url).endsWith("/worktree/prepare") ? Promise.resolve(response({ root: "/projects/demo", repoName: "Demo", branch: "main", path: "/worktrees" })) : original(url, init));
  await mount(); expect(document.body.textContent).not.toContain(t("worktree-composer.github"));
  expect(document.body.textContent).toContain(t("worktree-composer.legacy")); // an Orca adapter an update would make advanced
  await input(t("worktree.branch_label"), "feature/basic"); await click(t("worktree-composer.create"));
  expect(creates()[0].body).toEqual({ workspace_id: "source-a", kind: "orca", branch: "feature/basic", path: "/worktrees/feature-basic" });
});

it("offers an explicit basic prepare after advanced runtime incompatibility, preserving choices until explicitly cleared", async () => {
  const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => {
    if (String(url).endsWith("/worktree/prepare")) {
      const body = JSON.parse(init.body); calls.push({ url: String(url), body, headers: new Headers(init.headers) });
      return Promise.resolve(body.composer ? response({ error: t("worktree-composer.update_composer"), code: "internal" }, 502)
        : response({ root: "/projects/demo", repoName: "Demo", branch: "main", path: "/worktrees" }));
    }
    return original(url, init);
  });
  await mount(); expect(creates()).toHaveLength(0);
  await click(t("worktree-composer.use_basic"));
  expect(calls.filter((c) => c.url.endsWith("/worktree/prepare")).map((c) => c.body?.composer)).toEqual([true, false]);
  // The caller's explicit parent remains an unsupported choice until the user clears it.
  await input(t("worktree.branch_label"), "feature/basic"); await click(t("worktree-composer.create"));
  expect(creates()).toHaveLength(0);
  await click(t("worktree-composer.clear_advanced")); await click(t("worktree-composer.create"));
  expect(creates()[0].body).toEqual({ workspace_id: "source-a", kind: "orca", branch: "feature/basic", path: "/worktrees/feature-basic" });
});

it("never offers basic downgrade for permission errors", async () => {
  const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => String(url).endsWith("/worktree/prepare") ? Promise.resolve(response({ error: "Full device required" }, 403)) : original(url, init));
  await mount(); expect(document.body.textContent).toContain("Full device required");
  expect(button(t("worktree-composer.use_basic"))).toBeUndefined(); expect(creates()).toHaveLength(0);
});

it("retains real provider results while showing partial provider errors and saves a validated sparse preset without resetting name or base", async () => {
  const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => {
    if (String(url).endsWith("/worktree/source")) return Promise.resolve(response({ items: [{ type: "issue", number: 42, title: "Real issue", url: "https://github.com/example/demo/issues/42" }], errors: { prs: { message: "PR search unavailable" } }, warning: "Some sources unavailable" }));
    if (String(url).endsWith("/worktree/preset")) { calls.push({ url: String(url), body: JSON.parse(init.body), headers: new Headers(init.headers) }); return Promise.resolve(response({ preset: { id: "validated-preset", name: "UI", directories: ["web", "assets"] } })); }
    return original(url, init);
  });
  await mount(); await input(t("worktree-composer.name"), "Kept draft"); await input(t("worktree-composer.base"), "feature/existing");
  await click(t("worktree-composer.search")); expect(document.body.textContent).toContain("PR search unavailable");
  await click("#42 · Real issue");
  await input(t("worktree-composer.preset_name"), "UI"); await input(t("worktree-composer.directories"), "web\nassets"); await click(t("worktree-composer.save_preset"));
  expect(field(t("worktree-composer.name")).value).toBe("Kept draft"); expect(field(t("worktree-composer.base")).value).toBe("feature/existing");
  expect(calls.find((c) => c.url.endsWith("/worktree/preset"))?.body).toEqual({ workspace_id: "source-a", kind: "orca", name: "UI", directories: "web\nassets" });
  await click(t("worktree-composer.create")); expect(creates()[0].body).toMatchObject({ source_id: "issue:42", source_url: "https://github.com/example/demo/issues/42", sparse_preset_id: "validated-preset", name: "Kept draft", base_ref: "feature/existing" });
});

it("retries only the optional M3 launch after failure while retaining the acknowledged creation receipt", async () => {
  let failLaunch = true; let launchId = "";
  const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => {
    if (String(url).endsWith("/sessions")) {
      calls.push({ url: String(url), body: JSON.parse(init.body), headers: new Headers(init.headers) });
      return Promise.resolve(failLaunch ? response({ error: "Launcher unavailable", delivered: "none" }, 409) : response({ session: { machine_id: "computer", session_id: "new-agent", cwd: created.path, launcher: "custom-codex", status: "idle", state: "idle" } }));
    }
    return original(url, init);
  });
  const stop = onOpenNewAgent((preset) => {
    launchId = launchAgent({ machineId: preset.workspace!.machineId, cwd: preset.workspace!.cwd, host: preset.workspace!.host,
      workspaceId: preset.workspace!.workspaceId, launcher: "custom-codex", model: "chosen-model", effort: "high", text: "First message", attachments: [] });
  });
  await mount(); await click(t("worktree-composer.create")); await click(t("worktree-composer.start_agent")); await flush();
  expect(getState().launches[launchId].phase).toBe("failed");
  failLaunch = false; await act(async () => deliverLaunch(launchId)); await flush();
  expect(getState().launches[launchId].phase).toBe("ready");
  const starts = calls.filter((c) => c.url.endsWith("/sessions"));
  expect(starts).toHaveLength(2);
  expect(starts[1].body).toMatchObject({ workspace_id: "actual-new", host: "orca", cwd: "/worktrees/generated-actual", launcher: "custom-codex", model: "chosen-model", effort: "high" });
  expect(calls.find((c) => c.url.endsWith("/messages"))?.body?.text).toBe("First message");
  expect(creates()).toHaveLength(1);
  await act(async () => root.render(null)); await mount();
  expect(document.body.textContent).toContain("/worktrees/generated-actual"); expect(creates()).toHaveLength(1); stop();
});

it("hides and omits explicit branch overrides on runtimes that advertise only advanced creation", async () => {
  const original = fetcher.getMockImplementation()!;
  fetcher.mockImplementation((url, init) => String(url).endsWith("/worktree/prepare") ? Promise.resolve(response({ ...info, composer: { ...info.composer, capabilities: ["create", "folder_parent"] } })) : original(url, init));
  await mount();
  expect([...document.querySelectorAll("label")].some((label) => label.textContent === t("worktree-composer.branch"))).toBe(false);
  expect(document.body.textContent).not.toContain(t("worktree-composer.github"));
  await click(t("worktree-composer.create"));
  expect(creates()[0].body).toMatchObject({ composer: true, base_ref: "origin/main" });
  expect(creates()[0].body).not.toHaveProperty("branch_override");
});
