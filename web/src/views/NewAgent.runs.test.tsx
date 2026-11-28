// @vitest-environment jsdom
// Compare runs (#8916): New agent's Runs choice with the real sheet, runner, launchAgent/deliverLaunch and REST client over
// synthetic HTTP whose worktree/add answers wait for the test.
import { act, useEffect, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyMachine, getState } from "../lib/store";
import { handleUnauthorized, setToken } from "../lib/api";
import { onOpenNewAgent, type NewAgentPreset } from "../lib/new-agent";
import { resetManagerChoice } from "../lib/manager-choice";
import { getGroups, resetGroupsCache, runStatus } from "../lib/compare-runs";
import { resetRunner, retryRun, startCompare } from "../lib/compare-runner";
import { NewAgent } from "./NewAgent";
import { resetModelListsForTests } from "../lib/session-models";
import { t } from "../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const before = { ...getState() };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
type Call = { url: string; body?: Record<string, unknown> };
type Pending = Call & { answer: (value: unknown, status?: number) => void };
let calls: Call[];
let pending: Pending[];
let root: Root;
let host: HTMLDivElement;
let closes: number;
let started: number;
const flush = () => act(async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); });
const tick = (ms = 0) => act(async () => {
  await vi.advanceTimersByTimeAsync(0);
  await vi.dynamicImportSettled();
  await vi.advanceTimersByTimeAsync(ms);
});
const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === text);
const textarea = () => document.querySelector<HTMLTextAreaElement>("textarea")!;
const adds = () => calls.filter((c) => c.url.endsWith("/worktree/add"));
const starts = () => calls.filter((c) => c.url.endsWith("/sessions"));
const runsGroup = () => document.querySelector('[data-new-agent-runs] [role="radiogroup"]');
async function type(text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea(), text);
    textarea().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const click = (el: HTMLElement | undefined | null) => act(async () => el!.click());
function Harness() {
  const [open, setOpen] = useState(true);
  const [preset, setPreset] = useState<NewAgentPreset>();
  useEffect(() => onOpenNewAgent((next) => { setPreset(next); setOpen(true); }), []);
  return <NewAgent open={open} preset={preset} onClose={() => { closes++; setOpen(false); }} />;
}
async function worktreeOn() {
  await tick(401);
  const sw = document.querySelector<HTMLButtonElement>('[data-testid="new-agent-worktree"] [role="switch"]');
  expect(sw).not.toBeNull();
  await click(sw);
}
async function answer(request: Pending, value: unknown, status = 200) {
  await act(async () => request.answer(value, status));
  await flush();
  await tick();
  await flush();
}
const made = (n: number) => ({ path: `/projects/demo-run-${n}`, root: "/projects/demo", branch: `persona/submitted-task-${n}` });

beforeEach(async () => {
  resetModelListsForTests();
  resetGroupsCache();
  resetRunner();
  vi.useFakeTimers();
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  calls = []; pending = []; closes = 0; started = 0;
  vi.stubGlobal("URL", class extends URL {
    static createObjectURL = vi.fn(() => "blob:synthetic-preview");
    static revokeObjectURL = vi.fn();
  });
  localStorage.clear();
  resetManagerChoice();
  setToken("synthetic-credential-a");
  handleUnauthorized(() => {});
  localStorage.setItem("new.machine", "computer");
  localStorage.setItem("new.cwd.computer", "/projects/demo");
  Object.assign(getState(), { loaded: true, machines: [{ id: "computer", name: "Synthetic computer", online: true }], sessions: {}, launches: {}, features: [], interactions: [], answered: {} });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const call: Call = { url, body: typeof init.body === "string" ? JSON.parse(init.body) : undefined };
    calls.push(call);
    if (url.includes("/model-config/purposes/")) return json({ error: "not found" }, 404);
    if (url.includes("/launchers/") && url.includes("/models")) return json({ agent: "claude", models: [{ id: "synthetic-model", name: "Synthetic model", efforts: ["low", "high"] }] });
    if (url.endsWith("/launchers")) return json({ launchers: [{ name: "claude", agent: "claude" }] });
    if (url.includes("/dirs?")) return json({ path: "/projects", entries: [] });
    if (url.endsWith("/worktree/status")) return json({ path: call.body!.cwd, dirty: 0, unmerged: 0 });
    if (url.endsWith("/worktree/add")) return new Promise<Response>((resolve) => pending.push({ ...call, answer: (value, status = 200) => resolve(json(value, status)) }));
    if (url.endsWith("/sessions")) {
      started++;
      return json({ session: { machine_id: call.body!.machine_id, session_id: `synthetic-run-${started}`, title: "Synthetic run", cwd: call.body!.cwd, status: "idle" } });
    }
    if (url.endsWith("/workspaces")) return json({ workspaces: [] });
    return json({});
  }));
  history.replaceState({}, "", "/");
  host = document.createElement("div"); document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<Harness />));
  await flush();
  await type("Submitted task");
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  Object.assign(getState(), before);
  resetManagerChoice();
  resetRunner();
  resetGroupsCache();
  localStorage.clear();
  vi.unstubAllGlobals(); vi.useRealTimers();
  history.replaceState({}, "", "/");
});

describe("New agent's Runs (#8916)", () => {
  it("offers Runs only with the worktree switch on, and with 1 starts exactly as before", async () => {
    await tick(401);
    expect(runsGroup()).toBeNull();
    await worktreeOn();
    const radios = [...runsGroup()!.querySelectorAll<HTMLButtonElement>('[role="radio"]')];
    expect(radios.map((r) => r.textContent)).toEqual(["1", "2", "3", "4"]);
    expect(radios[0].getAttribute("aria-checked")).toBe("true");
    await click(button(t("newagent.start")));
    await flush();
    expect(adds()).toHaveLength(1);
    expect(adds()[0].body).not.toHaveProperty("branch");
    await answer(pending[0], made(1));
    expect(starts()).toHaveLength(1);
    // the one Agent's conversation opened, as ever (its local page, then the started session's)
    expect(location.pathname).toBe("/s/computer/synthetic-run-1");
    expect(getGroups()).toHaveLength(0);
  });

  it("keeps the document title hint in memory for every run", async () => {
    const nameHint = "team/brief r9";
    await act(async () => {
      startCompare({
        mid: "computer", cwd: "/projects/demo", nameHint: "review", branch: "review", count: 2,
        spec: { launcher: "claude", nameHint }, intoWorkspace: false, text: "Private team document", attachments: [],
      });
    });
    for (let n = 1; n <= 2; n++) {
      await answer(pending[n - 1], made(n));
      expect(starts()[n - 1].body?.name_hint).toBe(nameHint);
    }
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain(nameHint);
  });

  it("closes at once on the runs' page, then makes the worktrees one at a time and starts each run", async () => {
    await worktreeOn();
    await click(runsGroup()!.querySelectorAll<HTMLButtonElement>('[role="radio"]')[2]);
    expect(document.body.textContent).toContain(t("newagent.runs_hint", { count: 3 }));
    await click(button(t("newagent.start_runs", { count: 3 })));
    // the same frame: the sheet is gone, the page is the group's, and no request has been answered
    expect(closes).toBe(1);
    const [group] = getGroups();
    expect(location.pathname).toBe(`/compare/${group.id}`);
    expect(group.runs.map((r) => r.branch)).toEqual(["persona/submitted-task-1", "persona/submitted-task-2", "persona/submitted-task-3"]);
    expect(localStorage.getItem("draft:new-agent")).toBeNull();
    await flush();
    expect(adds()).toHaveLength(1);
    for (let n = 1; n <= 3; n++) {
      expect(adds()[n - 1].body).toMatchObject({ cwd: "/projects/demo", branch: `persona/submitted-task-${n}` });
      await answer(pending[n - 1], made(n));
      expect(starts().at(-1)!.body).toMatchObject({ machine_id: "computer", cwd: `/projects/demo-run-${n}`, launcher: "claude" });
    }
    expect(starts()).toHaveLength(3);
    // the page stayed: no run's start opened its conversation
    expect(location.pathname).toBe(`/compare/${group.id}`);
    expect(getGroups()[0].runs.map((r) => r.phase)).toEqual(["launched", "launched", "launched"]);
  });

  it("goes on past a failed worktree, and retries that run alone", async () => {
    await worktreeOn();
    await click(runsGroup()!.querySelectorAll<HTMLButtonElement>('[role="radio"]')[1]);
    await click(button(t("newagent.start_runs", { count: 2 })));
    await flush();
    await answer(pending[0], { error: "Synthetic worktree refusal" }, 500);
    const id = getGroups()[0].id;
    const ctx = () => ({ sessions: getState().sessions, launches: getState().launches, active: false, holds: true });
    expect(runStatus(getGroups()[0].runs[0], getGroups()[0], ctx())).toMatchObject({ kind: "failed", retry: "run" });
    expect(adds()).toHaveLength(2);
    await answer(pending[1], made(2));
    expect(starts()).toHaveLength(1);
    await act(async () => retryRun(id, 1));
    await flush();
    expect(adds()).toHaveLength(3);
    expect(adds()[2].body).toMatchObject({ branch: "persona/submitted-task-1" });
    await answer(pending[2], made(1));
    expect(starts()).toHaveLength(2);
    expect(getGroups()[0].runs.map((r) => r.phase)).toEqual(["launched", "launched"]);
  });

  it("stops the rest when the computer goes offline, without starting anything", async () => {
    await worktreeOn();
    await click(runsGroup()!.querySelectorAll<HTMLButtonElement>('[role="radio"]')[1]);
    await click(button(t("newagent.start_runs", { count: 2 })));
    await flush();
    await act(async () => applyMachine({ id: "computer", name: "Synthetic computer", online: false } as never));
    await answer(pending[0], made(1));
    expect(getGroups()[0].runs[1]).toMatchObject({ phase: "failed", error: t("lib-compare-runs.offline") });
    expect(adds()).toHaveLength(1);
  });

  it("is not offered for a start handed in for one Agent (Inbox's New agent…)", async () => {
    await act(async () => { (await import("../lib/new-agent")).openNewAgent({ prompt: "Handed", onStarted: () => {} }); });
    await flush();
    await worktreeOn();
    expect(runsGroup()).toBeNull();
  });
});
