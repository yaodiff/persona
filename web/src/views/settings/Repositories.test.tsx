// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
import { initI18n } from "../../i18n";
import { credentialOwner, handleUnauthorized, setToken } from "../../lib/api";
import { Toasts } from "../../ui/atoms";
import { Repositories } from "./Repositories";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Call = { path: string; init: RequestInit; answer: (body: unknown, status?: number) => void };
let root: Root, host: HTMLDivElement, calls: Call[];
const identity = { provider: "gitlab", instance_url: "https://gitlab.com", path: "team/sub/sample", canonical_url: "https://gitlab.com/team/sub/sample" };
const binding = { ...identity, id: "repo_sample", name: "Sample", aliases: ["demo"], purpose: "Public-safe sample", verified_at: "2026-11-12T00:00:00Z" };
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
  (b.textContent?.trim() === label || (["Edit", "Unbind"].includes(label) && b.textContent?.startsWith(`${label} `))) && !b.closest("[data-closing]"))!;
const click = async (label: string) => act(async () => { expect(button(label), label).toBeTruthy(); button(label).click(); });
const answer = async (call: Call, body: unknown, status = 200) => act(async () => call.answer(body, status));
const input = async (label: string, value: string) => act(async () => {
  const field = [...document.querySelectorAll<HTMLInputElement>("input")].find((node) => node.labels?.[0]?.textContent === label)!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, value);
  field.dispatchEvent(new Event("input", { bubbles: true }));
});
const render = async (manager = "assistant") => act(async () => root.render(<><Repositories key={manager} manager={manager} owner={credentialOwner()} /><Toasts /></>));
const open = async (rows: unknown[] = []) => { await render(); await click("Manage repositories"); await answer(calls[0], { manager: "assistant", repositories: rows }); };

beforeEach(async () => {
  await initI18n("en"); setToken("synthetic-repository-session"); calls = [];
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  vi.stubGlobal("fetch", vi.fn((url: RequestInfo | URL, init: RequestInit = {}) => new Promise<Response>((resolve, reject) => {
    init.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    calls.push({
      path: String(url).split("/api/v1")[1], init,
      answer: (body, status = 200) => resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })),
    });
  })));
});
afterEach(async () => {
  act(() => { document.querySelectorAll<HTMLButtonElement>('[aria-live="polite"] button[aria-label]').forEach((b) => b.click()); });
  act(() => root.unmount()); host.remove(); document.body.innerHTML = ""; handleUnauthorized(() => {}); vi.unstubAllGlobals(); setToken(""); await initI18n("zh-Hans");
});

it("loads only on demand and previews locally before existence-check/save", async () => {
  await render(); expect(calls).toHaveLength(0);
  await click("Manage repositories"); expect(text()).toContain("Loading");
  await answer(calls[0], { manager: "assistant", repositories: [] });
  expect(text()).toContain("No repositories bound");
  await click("Bind repository");
  await input("Repository URL", "git@" + "gitlab.com:team/sub/sample.git");
  expect(button("Check existence and save").disabled).toBe(false);
  expect(calls).toHaveLength(1);
  expect(text()).toContain(identity.path); expect(text()).toContain("Parsed locally; existence not yet checked");
  await click("Check existence and save");
  expect(text()).toContain("Checking repository existence");
  expect(new Headers(calls[1].init.headers).get("X-Persona-Manager")).toBe("assistant");
  await answer(calls[1], { manager: "assistant", repository: binding });
  expect(text()).toContain("Repository saved"); expect(text()).toContain(binding.purpose);
});

it("immediately replaces local identity and clears invalid input without preview requests", async () => {
  await open(); await click("Bind repository");
  await input("Repository URL", "https://github.com/example/project/pull/5581?diff=split#review");
  expect(text()).toContain("https://github.com/example/project");
  expect(button("Check existence and save").disabled).toBe(false);
  await input("Repository URL", "https://github.com/example/other");
  expect(document.querySelector('[role="dialog"]')!.textContent).not.toContain("example/project");
  await input("Repository URL", "https://token@" + "github.com/example/other");
  expect(button("Check existence and save").disabled).toBe(true);
  expect(document.querySelector('[data-repository-preview]')).toBeNull();
  expect(calls).toHaveLength(1);
});

it("submits the canonical preview and leaves a failed check unsaved with its draft", async () => {
  await open(); await click("Bind repository");
  await input("Repository URL", "git@" + "github.com:Example/Project.git");
  await click("Check existence and save");
  expect(JSON.parse(calls[1].init.body as string)).toMatchObject({url:"https://github.com/example/project",provider:"github"});
  await answer(calls[1], { code: "repository_timeout", error: "Repository check timed out; nothing was saved." }, 422);
  expect(text()).toContain("Repository check timed out; nothing was saved.");
  expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeTruthy();
  expect(document.querySelector<HTMLInputElement>('input')!.value).toBe("git@" + "github.com:Example/Project.git");
  expect(text()).toContain("No repositories bound");
});

it("edits descriptive metadata and restores an unbound record with its single-use receipt", async () => {
  await open([binding]); await click("Edit"); await input("Name (optional)", "Renamed");
  await click("Save changes");
  expect(calls[1].init.method).toBe("PUT");
  expect(JSON.parse(calls[1].init.body as string).url).toBe(identity.canonical_url);
  await answer(calls[1], { manager: "assistant", repository: { ...binding, name: "Renamed" } });
  await click("Unbind"); expect(text()).toContain("does not delete or change the remote repository");
  await click("Unbind repository");
  expect(calls[2].init.method).toBe("DELETE");
  await answer(calls[2], { manager: "assistant", receipt: "synthetic-undo" });
  expect(text()).toContain("No repositories bound");
  await click("Undo");
  expect(JSON.parse(calls[3].init.body as string)).toEqual({ receipt: "synthetic-undo" });
  await answer(calls[3], { manager: "assistant", repository: binding });
  expect(text()).toContain(binding.purpose);
});

it("fences late reads and 401s when the manager changes", async () => {
  let unauthorized = 0; handleUnauthorized(() => { unauthorized++; });
  await render(); await click("Manage repositories");
  await render("work"); await click("Manage repositories");
  await answer(calls[1], { manager: "work", repositories: [] });
  await answer(calls[0], {}, 401);
  expect(unauthorized).toBe(0); expect(text()).not.toContain(binding.purpose);
  expect(new Headers(calls[1].init.headers).get("X-Persona-Manager")).toBe("work");
});

it("rejects an out-of-scope or malformed success instead of showing an empty state", async () => {
  await render(); await click("Manage repositories");
  await answer(calls[0], { manager: "work", repositories: [binding] });
  expect(text()).toContain("Could not load repositories"); expect(text()).not.toContain("No repositories bound");
});

it("bounds a stalled save and ignores its late response after a new draft", async () => {
  await open(); await click("Bind repository"); await input("Repository URL", identity.canonical_url);
  const savedNotices = text().split("Repository saved").length;
  vi.useFakeTimers();
  try {
    await click("Check existence and save");
    await act(async () => { await vi.advanceTimersByTimeAsync(13_000); });
    expect(text()).toContain("The change is unconfirmed. Refresh repositories before trying again.");
    expect(document.querySelector<HTMLInputElement>("input")!.value).toBe(identity.canonical_url);
    expect(calls[1].init.signal?.aborted).toBe(true);
    await click("Cancel");
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    await click("Bind repository"); await input("Repository URL", "https://github.com/example/new");
    await answer(calls[1], {manager:"assistant",repository:binding});
    expect(document.querySelector<HTMLInputElement>("input")!.value).toBe("https://github.com/example/new");
    expect(text().split("Repository saved").length).toBe(savedNotices);
  } finally { vi.useRealTimers(); }
});

it("does not accept a late save for a previous manager", async () => {
  await open(); await click("Bind repository"); await input("Repository URL", identity.canonical_url);
  const savedNotices = text().split("Repository saved").length;
  await click("Check existence and save"); await render("work");
  await answer(calls[1], {manager:"assistant",repository:binding});
  expect(text().split("Repository saved").length).toBe(savedNotices);
  expect(text()).not.toContain(binding.purpose);
});

it("requires the saved identity to match the canonical preview", async () => {
  await open(); await click("Bind repository"); await input("Repository URL", identity.canonical_url);
  await click("Check existence and save");
  await answer(calls[1], { manager: "assistant", repository: { ...binding, path: "team/other" } });
  expect(text()).toContain("The change is unconfirmed. Refresh repositories before trying again.");
  expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeTruthy();
  expect(document.querySelector('[data-repository]')).toBeNull();
});
