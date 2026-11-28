// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { setPairedToken, setToken } from "../../lib/api";
import { chooseManager, MANAGER_HEADER, resetManagerChoice } from "../../lib/manager-choice";
import { forgetProviderNames } from "../../lib/provider-names";
import type { Run, Task } from "../../lib/routines";
import { Routines } from "./Routines";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const task: Task = { id: "task-1", title: "Daily summary", prompt: "Summarize the day", repeat: "daily", time: "09:00", enabled: true };
const run: Run = { id: "run-1", kind: "task", ref_id: task.id, title: "Completed summary", due: "2026-11-05T09:00:00Z", at: "2026-11-05T09:00:00Z", status: "ok" };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const failure = () => json({ error: "run history unavailable" }, 500);
// errorText explains a 5xx and keeps the hub's own text after it (#3728)
const failedText = /无法加载运行记录：.*run history unavailable/;

let runsReply: () => Response | Promise<Response>;
let tasksReply: () => Response;
let taskUpdate: ((init?: RequestInit) => Response) | undefined;
let providers: unknown[];
let catalogReplies: Record<string, () => Response | Promise<Response>>;
// the hub is unreachable: every request, a change or a read, answers 502
let down: boolean;
let calls: string[];
let skipCalls: { path: string; method: string; manager: string | null; body?: unknown }[];
let skipReply: () => Response | Promise<Response>;
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  resetManagerChoice();
  setToken("routine-test");
  calls = [];
  skipCalls = [];
  skipReply = () => json({ error: "unexpected skip" }, 400);
  down = false;
  runsReply = failure;
  tasksReply = () => json({ tasks: [task] });
  taskUpdate = undefined;
  providers = [];
  catalogReplies = {};
  forgetProviderNames();
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push(path);
    if (down) return Promise.resolve(json({ error: "hub unreachable" }, 502));
    if (path.startsWith(`/api/v1/assistant/tasks/${task.id}/skip-next`)) {
      skipCalls.push({
        path, method: init?.method ?? "GET", manager: new Headers(init?.headers).get(MANAGER_HEADER),
        body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      });
      return Promise.resolve(skipReply());
    }
    if (path === "/api/v1/assistant") return Promise.resolve(json({ conversations: [] }));
    if (path === "/api/v1/assistant/tasks") return Promise.resolve(tasksReply());
    if (path === `/api/v1/assistant/tasks/${task.id}` && taskUpdate) return Promise.resolve(taskUpdate(init));
    if (path === "/api/v1/assistant/runs") return Promise.resolve(runsReply());
    if (path === "/api/v1/model-config") return Promise.resolve(json({ providers }));
    const machine = /^\/api\/v1\/machines\/([^/]+)\/launchers/.exec(path)?.[1];
    if (machine && catalogReplies[machine]) return Promise.resolve(catalogReplies[machine]());
    if (path.startsWith("/api/v1/machines/box/launchers")) return Promise.resolve(json({ launchers: [{ name: "assistant", display_name: "Assistant", agent: "claude" }, { name: "shared", agent: "claude" }] }));
    if (path.startsWith("/api/v1/machines/remote/launchers")) return Promise.resolve(json({ launchers: [{ name: "shared", agent: "codex" }] }));
    throw new Error(`Unexpected request: ${path}`);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetManagerChoice();
  setToken("");
  vi.unstubAllGlobals();
});

const render = () => act(async () => root.render(<Routines onToggleSidebar={() => {}} />));
const rowOf = (title: string) => [...host.querySelectorAll<HTMLElement>('[role="button"]')].find((el) => el.getAttribute("aria-label")?.includes(title));
const history = () => host.querySelector("aside")!;
const retry = () => [...history().querySelectorAll("button")].find((b) => b.textContent === "重试");
const refresh = () => act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="刷新"]')!.click());
// lets a refused change and the reload after it finish (each reply's body is read on a later task)
const flush = () => act(async () => {
  for (let i = 0; i < 10; i++) await new Promise((resolve) => setTimeout(resolve, 0));
});

describe("routine run history loading", () => {
  it("uses green success marks for completed runs without coloring failed-run surfaces", async () => {
    runsReply = () => json({ runs: [run, { ...run, id: "failed-run", title: "Failed summary", status: "failed" }] });
    await render();
    const marks = [...history().querySelectorAll('[role="img"]')];
    expect(marks.some((mark) => mark.classList.contains("bg-green-tint") && mark.classList.contains("text-green"))).toBe(true);
    expect(marks.some((mark) => mark.classList.contains("bg-field") && mark.classList.contains("text-red"))).toBe(true);
  });

  it("shows actual legacy task execution fallback beside successful timing, not eligibility alone", async () => {
    const fallbackRun = { ...run, compatibility_fallback: "legacy_task_execution", session_id: "fallback-chat" };
    const ordinaryRun = { ...run, id: "ordinary-run", title: "Ordinary summary", session_id: "ordinary-chat" };
    runsReply = () => json({ runs: [fallbackRun, ordinaryRun] });
    await render();

    const line = (title: string) => [...history().querySelectorAll<HTMLElement>("button")].find((el) => el.textContent?.includes(title))!;
    expect(line(fallbackRun.title).textContent).toContain("准时");
    expect(line(fallbackRun.title).textContent).toContain("使用旧版任务执行方式");
    expect(line(ordinaryRun.title).textContent).toContain("准时");
    expect(line(ordinaryRun.title).textContent).not.toContain("使用旧版任务执行方式");
  });

  it("shows a failed request's reason and retry while keeping the tasks visible", async () => {
    await render();

    expect(host.textContent).toContain(task.title);
    expect(history().textContent).toMatch(failedText);
    expect(history().textContent).not.toContain("加载中");
    expect(history().textContent).not.toContain("暂无运行记录");
    expect(retry()).toBeDefined();
  });

  it("shows successful history independently of a failed task request", async () => {
    tasksReply = () => json({ error: "tasks unavailable" }, 500);
    runsReply = () => json({ runs: [run] });
    await render();

    expect(host.textContent).toContain("无法加载定时任务");
    expect(host.textContent).toContain("tasks unavailable");
    expect(history().textContent).toContain(run.title);
    expect(history().textContent).not.toContain("无法加载运行记录");
    expect(retry()).toBeUndefined();
  });

  it.each([
    { name: "nonempty", runs: [run], expected: run.title },
    { name: "empty", runs: [] as Run[], expected: "暂无运行记录" },
  ])("retries both requests and recovers to $name history", async ({ runs, expected }) => {
    await render();
    expect(retry()).toBeDefined();
    runsReply = () => json({ runs });

    await act(async () => retry()!.click());

    expect(calls.filter((path) => path === "/api/v1/assistant/tasks")).toHaveLength(2);
    expect(calls.filter((path) => path === "/api/v1/assistant/runs")).toHaveLength(2);
    expect(host.textContent).toContain(task.title);
    expect(history().textContent).toContain(expected);
    expect(history().textContent).not.toMatch(failedText);
    expect(history().textContent).not.toContain("加载中");
    expect(retry()).toBeUndefined();
  });

  it("shows a refresh failure after earlier success and clears it when refreshed successfully", async () => {
    runsReply = () => json({ runs: [run] });
    await render();
    expect(history().textContent).toContain(run.title);

    runsReply = failure;
    await refresh();

    expect(host.textContent).toContain(task.title);
    expect(history().textContent).toMatch(failedText);
    expect(history().textContent).toContain(run.title);
    expect(history().textContent).not.toContain("加载中");
    expect(retry()).toBeDefined();

    const recovered = { ...run, id: "run-2", title: "Latest summary" };
    runsReply = () => json({ runs: [recovered] });
    await refresh();

    expect(history().textContent).toContain(recovered.title);
    expect(history().textContent).not.toMatch(failedText);
    expect(retry()).toBeUndefined();
  });

  it("ignores an older request's failure after a newer refresh succeeded", async () => {
    let finish!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    runsReply = () => pending;
    await render();
    expect(history().textContent).toContain("加载中");
    expect(rowOf(task.title!)).toBeDefined();

    runsReply = () => json({ runs: [run] });
    await refresh();
    expect(history().textContent).toContain(run.title);

    await act(async () => finish(failure()));

    expect(history().textContent).toContain(run.title);
    expect(history().textContent).not.toMatch(failedText);
    expect(retry()).toBeUndefined();
  });
});

it.each([
  { repeat: "interval" as const, title: "", body: "", label: "定时任务" },
  { repeat: "cron" as const, title: "", body: "", label: "定时任务" },
  { repeat: "once" as const, title: "", body: "", label: "定时跟进" },
  { repeat: "interval" as const, title: "Explicit title", body: "Review notes", label: "Explicit title" },
  { repeat: "cron" as const, title: "", body: "Review notes", label: "Review notes" },
])("shows $label for the $repeat task with a selected reference", async ({ repeat, title, body, label }) => {
  const selected = '<persona-context kind="skill" ref="review">Review</persona-context>';
  tasksReply = () => json({ tasks: [{
    ...task, repeat, title, prompt: `${selected}\n\n${body}`, prompt_selections: true,
    at: "2099-01-01T09:00:00Z", every_minutes: 30, cron: "@hourly",
  }] });
  runsReply = () => json({ runs: [] });

  await render();

  expect(rowOf(label)?.textContent).toContain(label);
});

it("pauses a routine without changing its saved selections or execution settings", async () => {
  const prompt = '<persona-context kind="skill" ref="review">Review</persona-context>\n\nReview notes';
  const selected: Task = { ...task, prompt, prompt_selections: true, execution_machine_id: "box", cwd: "/work" };
  tasksReply = () => json({ tasks: [selected] });
  runsReply = () => json({ runs: [] });
  let update: RequestInit | undefined;
  taskUpdate = (init) => {
    update = init;
    return json({ task: { ...selected, enabled: false } });
  };

  await render();
  const toggle = host.querySelector<HTMLButtonElement>('[role="switch"]')!;
  await act(async () => toggle.click());
  await flush();

  expect(update?.method).toBe("PUT");
  expect(JSON.parse(String(update?.body))).toEqual({
    title: task.title, structured_prompt: prompt, repeat: "daily", cron: "", session_id: "",
    time: task.time, weekdays: [], enabled: false, every_minutes: 0, permission_mode: "",
  });
  expect(toggle.getAttribute("aria-checked")).toBe("false");

  await act(async () => root.render(null));
  down = true;
  await render();
  expect(host.querySelector('[role="switch"]')?.getAttribute("aria-checked")).toBe("false");
  expect(host.textContent).toContain("hub unreachable");
});

describe("a change the hub refuses while the list cannot be read again", () => {
  const switchOf = () => host.querySelector<HTMLButtonElement>('[role="switch"]')!;

  it.each([
    { name: "off", enabled: true },
    { name: "on", enabled: false },
  ])("puts the switch back when turning a routine $name fails", async ({ enabled }) => {
    tasksReply = () => json({ tasks: [{ ...task, enabled }] });
    runsReply = () => json({ runs: [] });
    await render();
    expect(switchOf().getAttribute("aria-checked")).toBe(String(enabled));

    down = true;
    await act(async () => switchOf().click());
    await flush();

    expect(calls).toContain(`/api/v1/assistant/tasks/${task.id}`);
    // the reload after the failure ran and failed too
    expect(calls.filter((path) => path === "/api/v1/assistant/tasks")).toHaveLength(2);
    expect(history().textContent).toContain("无法加载运行记录");
    expect(switchOf().getAttribute("aria-checked")).toBe(String(enabled));
  });

  it("brings a one-off back when running it now fails", async () => {
    const once: Task = { id: "once-1", title: "Call the bank", prompt: "Remind me to call the bank", repeat: "once", at: "2099-01-01T09:00:00Z", enabled: true };
    tasksReply = () => json({ tasks: [task, once] });
    runsReply = () => json({ runs: [] });
    await render();
    const row = rowOf(once.title)!;
    expect(row).toBeDefined();

    down = true;
    act(() => void row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
    const run = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((el) => el.textContent?.includes("立即运行"));
    expect(run).toBeDefined();
    await act(async () => run!.click());
    await flush();

    expect(calls).toContain(`/api/v1/assistant/tasks/${once.id}/run`);
    expect(calls.filter((path) => path === "/api/v1/assistant/tasks")).toHaveLength(2);
    expect(history().textContent).toContain("无法加载运行记录");
    expect(rowOf(once.title)).toBeDefined();
  });
});

describe("what a task runs with (#5029)", () => {
  it.each([
    { name: "empty", launchers: [], expected: "shared" },
    { name: "missing", launchers: [{ name: "other" }], expected: "shared" },
    { name: "present", launchers: [{ name: "shared", display_name: "Remote alias", agent: "codex" }], expected: "Remote alias" },
    { name: "failed", launchers: null, expected: "Other computer alias" },
  ])("uses the execution computer after its $name catalog response", async ({ name, launchers, expected }) => {
    providers = [{ id: "launcher.shared", name: "Other computer alias", agent: "claude" }];
    tasksReply = () => json({ tasks: [
      { ...task, execution: { machine_id: "remote", launcher: "shared", model: "saved-model" } },
      { ...task, id: "other", title: "Another task", execution: { machine_id: "box", launcher: "assistant" } },
    ] });
    runsReply = () => json({ runs: [] });
    let finish!: (response: Response) => void;
    catalogReplies.remote = () => new Promise<Response>((resolve) => { finish = resolve; });
    if (name === "present") catalogReplies.box = () => new Promise<Response>(() => {});
    await render();
    await flush();
    const row = rowOf(task.title)!;
    expect(row.textContent).toContain("Other computer alias");
    await act(async () => finish(launchers ? json({ launchers }) : json({ error: "unavailable" }, 503)));
    await flush();
    expect(row.textContent).toContain(expected + " ·");
    if (launchers) expect(row.textContent).not.toContain("Other computer alias");
  });

  it("resolves backend metadata on each execution computer rather than sharing the same alias across computers", async () => {
    tasksReply = () => json({ tasks: [
      { ...task, id: "local", title: "Local task", execution: { machine_id: "box", launcher: "shared", model: "local-model", effort: "high" } },
      { ...task, id: "remote", title: "Remote task", execution: { machine_id: "remote", launcher: "shared", model: "remote-model", effort: "high" } },
    ] });
    runsReply = () => json({ runs: [] });
    await render();
    await flush();
    expect(host.textContent).toContain("shared · claude · local-model · high");
    expect(host.textContent).toContain("shared · codex · remote-model · high");
    expect(calls.filter((path) => path.startsWith("/api/v1/machines/box/launchers"))).toHaveLength(1);
    expect(calls.filter((path) => path.startsWith("/api/v1/machines/remote/launchers"))).toHaveLength(1);
  });

  it("shows the full effective configuration for inherited tasks and keeps older rows without it usable", async () => {
    providers = [{ id: "launcher.assistant", name: "Assistant", agent: "claude" }];
    tasksReply = () => json({ tasks: [{ ...task, execution: { machine_id: "box", launcher: "assistant", model: "opus", effort: "high" } }, { ...task, id: "legacy", title: "Legacy task" }] });
    runsReply = () => json({ runs: [] });
    await render();
    await flush();
    const row = [...host.querySelectorAll<HTMLElement>('[role="button"]')].find((el) => el.textContent?.includes(task.title))!;
    expect(row.textContent).toContain("Assistant · claude · opus · high");
    expect(row.querySelector<HTMLElement>('span[title*="opus"]')?.title).toContain("Assistant · claude · opus · high");
    expect(host.textContent).toContain("Legacy task");
    expect(host.textContent).not.toContain("undefined");
  });

  it("names its launcher by the provider's display name with the ID in the tooltip; an rc alias by its ID", async () => {
    providers = [{ id: "launcher.relay-example-com", name: "我的中转 站", kind: "launcher" }];
    tasksReply = () => json({ tasks: [{ ...task, launcher: "relay-example-com" }, { ...task, id: "task-2", title: "Weekly review", launcher: "cc_glm" }] });
    runsReply = () => json({ runs: [] });
    await render();
    await flush();
    const line = (title: string) => [...host.querySelectorAll<HTMLElement>('[role="button"]')].find((el) => el.textContent?.includes(title))!.querySelector<HTMLElement>("div.truncate > span.truncate")!;
    expect([line("Daily summary").textContent, line("Daily summary").title]).toEqual(["我的中转 站", "relay-example-com"]);
    expect([line("Weekly review").textContent, line("Weekly review").hasAttribute("title")]).toEqual(["cc_glm", false]);
  });
});

describe("skipping one scheduled occurrence", () => {
  const due = "2099-01-01T13:23:00.123456789Z";
  const token = "test-token+1";
  const skipPath = `/api/v1/assistant/tasks/${task.id}/skip-next`;
  const scheduled = { ...task, can_skip_next: true, next_run: due };
  const marked: Task = { ...scheduled, can_skip_next: undefined, skip_next_at: due, skip_next_token: token };
  const action = (key: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    .find((item) => item.textContent?.startsWith(t(key)));
  const showActions = () => act(async () => {
    const name = t("assistant-routines.menu_actions", { title: task.title });
    [...host.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.getAttribute("aria-label") === name)!.click();
  });
  const choose = (key: string) => act(async () => action(key)!.click());

  it("selects and cancels the exact occurrence while leaving Run now available", async () => {
    let listed: Task = scheduled;
    tasksReply = () => json({ tasks: [listed] });
    runsReply = () => json({ runs: [] });
    await render();
    await showActions();
    expect(action("assistant-routines.menu_run")).toBeDefined();
    expect(action("assistant-routines.menu_skip_next")).toBeDefined();
    skipReply = () => {
      listed = marked;
      return json({ task: listed });
    };
    await choose("assistant-routines.menu_skip_next");
    await flush();
    expect(skipCalls).toEqual([{ path: skipPath, method: "POST", manager: "assistant", body: { at: due } }]);
    expect(rowOf(task.title)?.textContent).toContain("将跳过");
    expect(history().textContent).not.toContain(t("assistant-routines.section_upcoming"));

    await act(async () => rowOf(task.title)!.dispatchEvent(new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true, cancelable: true })));
    expect(action("assistant-routines.menu_run")).toBeDefined();
    expect(action("assistant-routines.menu_skip_next")).toBeUndefined();
    skipReply = () => {
      listed = scheduled;
      return json({ task: listed });
    };
    await choose("assistant-routines.menu_cancel_skip");
    await flush();
    expect(skipCalls.at(-1)).toEqual({
      path: `${skipPath}?at=${encodeURIComponent(due)}&token=${encodeURIComponent(token)}`, method: "DELETE", manager: "assistant", body: undefined,
    });
    expect(rowOf(task.title)?.textContent).not.toContain("将跳过");
    expect(history().textContent).toContain(t("assistant-routines.section_upcoming"));
  });

  it("does not cancel a replacement skip from an outdated open menu", async () => {
    let listed = marked;
    tasksReply = () => json({ tasks: [listed] });
    runsReply = () => json({ runs: [] });
    await render();
    await showActions();

    listed = { ...marked, can_skip_next: false, skip_next_token: "replacement" };
    await refresh();
    await choose("assistant-routines.menu_cancel_skip");
    await flush();

    expect(skipCalls).toEqual([]);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain(t("assistant-routines.skip_conflict"));
    expect(rowOf(task.title)?.textContent).toContain("将跳过");
    await showActions();
    expect(action("assistant-routines.menu_cancel_skip")).toBeDefined();
  });

  it("refreshes a stale target after 409 without repeating the write", async () => {
    let listed: Task = scheduled;
    tasksReply = () => json({ tasks: [listed] });
    runsReply = () => json({ runs: [] });
    skipReply = () => {
      listed = { ...scheduled, next_run: "2099-01-02T13:23:00Z" };
      return json({ error: "stale occurrence" }, 409);
    };
    await render();
    await showActions();
    await choose("assistant-routines.menu_skip_next");
    await flush();
    expect(skipCalls).toHaveLength(1);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain(t("assistant-routines.skip_conflict"));
    expect(rowOf(task.title)?.textContent).not.toContain("将跳过");
    expect(calls.filter((path) => path === "/api/v1/assistant/tasks")).toHaveLength(2);
  });

  it.each(["manager", "credential"])("retires a pending result when the %s changes", async (change) => {
    chooseManager("work");
    let nextTask = scheduled;
    tasksReply = () => json({ tasks: [nextTask] });
    runsReply = () => json({ runs: [] });
    let finish!: (reply: Response) => void;
    skipReply = () => new Promise<Response>((resolve) => { finish = resolve; });
    await render();
    await showActions();
    await choose("assistant-routines.menu_skip_next");
    expect(skipCalls[0].manager).toBe("work");

    nextTask = { ...scheduled, title: "Other schedule" };
    await act(async () => change === "manager" ? chooseManager("other") : setPairedToken("routine-test"));
    await flush();
    await act(async () => finish(json({ error: "stale occurrence" }, 409)));
    await flush();

    expect(rowOf("Other schedule")).toBeDefined();
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(skipCalls).toHaveLength(1);
  });

  it("keeps legacy task controls and identifies owner skips in run history", async () => {
    const skipped = { ...run, id: "skip-user", status: "skipped", owner_skipped: true };
    runsReply = () => json({ runs: [skipped] });
    await render();
    expect(history().textContent).toContain(t("lib-routines.status_owner_skipped"));
    const row = rowOf(task.title)!;
    await act(async () => row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
    expect(action("assistant-routines.menu_run")).toBeDefined();
    expect(action("assistant-routines.menu_skip_next")).toBeUndefined();
    expect(action("assistant-routines.menu_cancel_skip")).toBeUndefined();
  });
});
