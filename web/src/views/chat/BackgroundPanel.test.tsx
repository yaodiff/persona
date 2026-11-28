// @vitest-environment jsdom
// An Agent's Background tasks side panel (#5421, after cindy's Background tasks tab): Running (the /loop, the running
// workflow runs oldest first, the background tasks) and Completed (ended runs and retained command outcomes newest
// first); a row's kind mark, status mark, title and "status · progress · duration · tokens · tool uses"; a run's detail
// with Back; a command's status, runtime, full command and output in its own detail, before and after it ends; Stop for
// the loop that asks first; "No background tasks in this session yet" when there is nothing.
import { act } from "react";
import { Blob as NodeBlob } from "node:buffer";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia = ((query: string) => ({
    matches: query.includes("reduce"),
    media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
});

import { backgroundPanelOf, provideBackgroundChat, type BackgroundPanel as Asked } from "../../lib/background-panel";
import { setToken } from "../../lib/api";
import { closePanel, getPanel, openPanel, usePanel } from "../../lib/panel";
import { getSession, reset, upsertSession } from "../../lib/store";
import { emptyTaskIndex } from "../../lib/tasks";
import type { BackgroundTask, LoopInfo, Session, Workflow } from "../../lib/types";
import { BackgroundPanel, backgroundSections } from "./BackgroundPanel";
import { SessionOverview } from "./SessionOverview";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = Date.parse("2026-11-12T10:00:00Z");
const at = (s: number) => new Date(T0 + s * 1000).toISOString();

const run = (task: string, over: Partial<Workflow> = {}): Workflow => ({
  task_id: task,
  name: `run-${task}`,
  summary: `What ${task} is for`,
  status: "running",
  running: true,
  tool_use_id: `call-${task}`,
  phases: ["Scan"],
  phase: "Scan",
  phase_stats: [{ title: "Scan", started: 5, done: 4, running: 1 }],
  agents_started: 5,
  agents_done: 4,
  agents_running: 1,
  started_at: at(-600),
  tokens: 12_300,
  calls: 7,
  agents: [
    { id: `${task}-a1`, session_id: `s@${task}-a1`, label: "scan:1", phase: "Scan", running: false, status: "done", started_at: at(-500), ended_at: at(-400) },
    { id: `${task}-a2`, session_id: `s@${task}-a2`, label: "scan:2", phase: "Scan", running: true, status: "running", started_at: at(-300) },
  ],
  ...over,
});
const ended = (task: string, end: number, over: Partial<Workflow> = {}) =>
  run(task, { status: "completed", running: false, ended_at: at(end), agents_running: 0, agents_done: 5, phase_stats: [{ title: "Scan", started: 5, done: 5, running: 0 }], ...over });

const loop: LoopInfo = { kind: "interval", prompt: "/loop 30m check the deploy", interval_seconds: 1800, next_at: at(240), reason: "watching the release" };
const shell: BackgroundTask = { id: "b1", kind: "shell", description: "Run the test suite", command: "npm test -- --watch=false", output_file: "/tmp/out/b1.log", started_at: at(-65) };
const sub: BackgroundTask = { id: "b2", kind: "agent", description: "Review the change", started_at: at(-30) };

let host: HTMLDivElement;
let root: Root;
const closed = vi.fn();
const outerKey = vi.fn();

beforeEach(() => {
  setToken("synthetic-background-owner");
  vi.stubGlobal("Blob", NodeBlob);
  vi.stubGlobal("fetch", vi.fn(async () => new Response("Sample output\n", {
    status: 206, headers: { "Content-Type": "text/plain", "Content-Range": "bytes 0-13/14" },
  })));
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  vi.setSystemTime(T0);
  closed.mockClear();
  outerKey.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  await flush();
  host.remove();
  closePanel();
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const put = (over: Partial<Session>) => act(() => upsertSession({ machine_id: "m", session_id: "s", cwd: "/work", status: "working", state: "working", ...over } as Session));
const draw = (opened: Asked = { kind: "background", mid: "m", sid: "s" }) => act(() => root.render(<div onKeyDown={outerKey}><BackgroundPanel mid="m" sid="s" opened={opened} onClose={closed} /></div>));
const q = (sel: string) => host.querySelector<HTMLElement>(sel);
const all = (sel: string) => [...host.querySelectorAll<HTMLElement>(sel)];
const flush = async () => { await act(async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); }); };
/** The rows in order, by what they are. */
const rows = () => all("[data-bg-loop], [data-bg-run], [data-bg-task]").map((el) => el.dataset.bgRun ?? el.dataset.bgTask ?? "loop");
const meta = (sel: string) => q(`${sel} [data-bg-meta]`)!.textContent;

describe("the list", () => {
  it("says there is nothing yet, under its header", () => {
    put({});
    draw();
    expect(q("header")!.textContent).toContain("后台任务");
    expect(q("[data-background-empty]")!.textContent).toBe("当前会话暂无后台任务。");
    expect(q("[data-background-list]")).toBeNull();
  });

  it("lists Running (the loop, runs oldest start first, background tasks) and Completed (newest end first)", () => {
    put({
      loop,
      background: [shell, sub],
      workflows: [
        run("w-new", { started_at: at(-60) }),
        ended("w-early", -3000),
        run("w-old", { started_at: at(-900) }),
        ended("w-late", -100),
        ended("w-gone", -2 * 86_400), // ended over a day ago: the node no longer keeps it
      ],
    });
    draw();
    const headings = all("h3");
    expect(headings.map((h) => h.textContent)).toEqual(["运行中", "已完成"]);
    expect(headings[0]!.className).toContain("uppercase");
    expect(all("section").map((s) => s.getAttribute("aria-labelledby"))).toEqual(headings.map((h) => h.id));
    expect(rows()).toEqual(["loop", "w-old", "w-new", "b1", "b2", "w-late", "w-early"]);
    expect(q('[data-background-section="completed"]')!.querySelectorAll("[data-bg-run]")).toHaveLength(2);
  });

  it("lists retained command outcomes newest first without counting them as running", () => {
    put({
      background: [shell],
      background_ended: [
        { ...shell, id: "old", status: "stopped", started_at: at(-300), ended_at: at(-200) },
        { ...shell, id: "new", status: "failed", exit_code: 2, started_at: at(-50), ended_at: at(-10) },
        { ...shell, id: "middle", status: "completed", exit_code: 0, started_at: at(-80), ended_at: at(-20) },
      ],
    });
    draw();
    expect(rows()).toEqual(["b1", "new", "middle", "old"]);
    expect(q('[data-background-section="running"] [data-bg-task="b1"]')).not.toBeNull();
    expect(q('[data-bg-task="new"] [data-bg-status]')!.getAttribute("data-bg-status")).toBe("failed");
    expect(q('[data-bg-task="middle"] [data-bg-status]')!.getAttribute("data-bg-status")).toBe("completed");
    expect(q('[data-bg-task="old"] [data-bg-status]')!.getAttribute("data-bg-status")).toBe("stopped");
    expect(meta('[data-bg-task="middle"]')).toBe("已完成 · 1m");
  });

  it("says a run's status, agents, duration, tokens and tool uses, with its kind and status marks", () => {
    put({ workflows: [run("w1"), ended("w2", -100, { status: "failed", agents_failed: 1, agents_done: 4 })] });
    draw();
    const live = q('[data-bg-run="w1"]')!;
    expect(live.querySelector("[data-bg-kind]")!.getAttribute("data-bg-kind")).toBe("workflow");
    expect(live.querySelector("[data-bg-status]")!.getAttribute("data-bg-status")).toBe("running");
    expect(live.querySelector("button")!.textContent).toContain("run-w1");
    expect(meta('[data-bg-run="w1"]')).toBe("运行中 · 4/5 Agent · 10m · 12.3k tokens · 7 次工具调用");
    const failed = q('[data-bg-run="w2"] [data-bg-status]')!;
    expect(failed.getAttribute("data-bg-status")).toBe("failed");
    expect(failed.className).toContain("text-red");
    expect(meta('[data-bg-run="w2"]')).toMatch(/^失败 · 5\/5 Agent · /);
  });

  it("counts a running one's time each second, and moves a run to Completed when it ends", () => {
    put({ workflows: [run("w1", { started_at: at(-5) })] });
    draw();
    expect(q('[data-bg-run="w1"] [data-bg-elapsed]')!.textContent).toBe("5s");
    act(() => vi.advanceTimersByTime(2000));
    expect(q('[data-bg-run="w1"] [data-bg-elapsed]')!.textContent).toBe("7s");
    put({ workflows: [ended("w1", 0, { started_at: at(-5) })] });
    expect(q('[data-background-section="running"]')).toBeNull();
    expect(q('[data-background-section="completed"] [data-bg-run="w1"]')).not.toBeNull();
  });
});

describe("a background task", () => {
  it("returns to Session overview instead of the unvisited Background tasks list", async () => {
    put({ background: [shell] });
    const session = getSession("m", "s")!;
    function OverviewAndPanel() {
      const opened = backgroundPanelOf(usePanel(), "m", "s");
      return <>
        <SessionOverview id="overview" session={session} machineName="Machine" items={[]} tasks={emptyTaskIndex()} children={[]} hidden={opened?.from === "overview"} />
        {opened && <BackgroundPanel mid="m" sid="s" opened={opened} onClose={closePanel} />}
      </>;
    }
    act(() => root.render(<OverviewAndPanel />));
    const overview = q("[data-session-overview]")!;
    const scroller = overview.querySelector<HTMLElement>(".overflow-y-auto")!;
    scroller.scrollTop = 120;
    const open = () => {
      act(() => overview.querySelector<HTMLElement>('[data-bg-task="b1"] [data-bg-row-button]')!.click());
      return flush();
    };
    await open();
    expect(getPanel()).toEqual({ kind: "background", mid: "m", sid: "s", task: "b1", from: "overview" });
    expect(overview.hidden).toBe(true);
    const back = q('[data-background-panel] button[aria-label="返回"], [data-session-overview] button[aria-label="返回"]');
    expect(back).not.toBeNull();
    expect(document.activeElement).toBe(back);
    act(() => back!.click());
    expect(q("[data-session-overview]")).toBe(overview);
    expect(overview.hidden).toBe(false);
    expect(scroller.scrollTop).toBe(120);
    expect(getPanel()).toBeNull();
    expect(q("[data-background-panel]")).toBeNull();

    await open();
    act(() => q("[data-background-panel] header button")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
    expect(q("[data-session-overview]")).toBe(overview);
    expect(overview.hidden).toBe(false);
    expect(scroller.scrollTop).toBe(120);
    expect(getPanel()).toBeNull();
    expect(q("[data-background-panel]")).toBeNull();
  });

  it("keeps list-originated Back and Escape on the Background tasks list", async () => {
    put({ background: [shell] });
    draw();
    const listed = { kind: "background", mid: "m", sid: "s" } as const;
    for (const key of ["Back", "Escape"]) {
      act(() => q('[data-bg-task="b1"] [data-bg-row-button]')!.click());
      await flush();
      expect(getPanel()).toEqual({ ...listed, task: "b1" });
      if (key === "Back") act(() => q('button[aria-label="返回"]')!.click());
      else act(() => q("header button")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
      expect(q("[data-background-list]")).not.toBeNull();
      expect(getPanel()).toEqual(listed);
    }
  });

  it("opens output inside Background tasks, retaining Open as file and a separate copy action", () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    put({ background: [shell, sub] });
    const opened: Asked = { kind: "background", mid: "m", sid: "s" };
    act(() => openPanel(opened));
    draw(opened);
    const row = q('[data-bg-task="b1"]')!;
    expect(row.querySelector("[data-bg-kind]")!.getAttribute("data-bg-kind")).toBe("terminal");
    expect(meta('[data-bg-task="b1"]')).toBe("运行中 · 1m 5s");
    const cmd = row.querySelector<HTMLElement>("[data-bg-command]")!;
    expect(cmd.title).toBe("npm test -- --watch=false");
    const copy = row.querySelector<HTMLElement>('button[aria-label="复制命令"]')!;
    expect(copy).not.toBeNull();
    act(() => copy.click());
    expect(writeText).toHaveBeenCalledWith("npm test -- --watch=false");
    expect(getPanel()).toEqual(opened);
    act(() => row.querySelector<HTMLElement>("[data-bg-row-button]")!.click());
    expect(getPanel()).toEqual({ ...opened, task: "b1" });
    expect(q('[data-bg-command-detail="b1"]')).not.toBeNull();
    act(() => q('[data-bg-open-file]')!.click());
    expect(getPanel()).toEqual({ kind: "file", mid: "m", path: "/tmp/out/b1.log", cwd: "/work" });
  });

  it("includes the command in the output-opening row without nesting the copy button", () => {
    put({ background: [shell] });
    draw();
    const row = q('[data-bg-task="b1"]')!;
    const button = row.querySelector<HTMLElement>("[data-bg-row-button]")!;
    expect(button.querySelector("[data-bg-command]")?.textContent).toBe(shell.command);
    expect(button.querySelector("button")).toBeNull();
    expect(row.querySelector('button[aria-label="复制命令"]')).not.toBeNull();
  });

  it("is a plain row for a subagent with no output to open", () => {
    put({ background: [sub] });
    draw();
    const row = q('[data-bg-task="b2"]')!;
    expect(row.querySelector("[data-bg-kind]")!.getAttribute("data-bg-kind")).toBe("agent");
    expect(row.querySelector("button")).toBeNull();
    expect(row.textContent).toContain("Review the change");
  });

  it("opens a running Monitor before it has an output file, and switches to its output in place (#10852)", async () => {
    const monitor: BackgroundTask = { id: "b3", kind: "monitor", description: "Watch the build log", command: "tail -f build.log | grep --line-buffered error", started_at: at(-20) };
    put({ background: [shell, monitor, sub] });
    const opened: Asked = { kind: "background", mid: "m", sid: "s" };
    act(() => openPanel(opened));
    draw(opened);
    const row = q('[data-bg-task="b3"]')!;
    expect(row.querySelector("[data-bg-kind]")!.getAttribute("data-bg-kind")).toBe("monitor");
    act(() => row.querySelector<HTMLElement>("[data-bg-row-button]")!.click()); await flush();
    expect(getPanel()).toEqual({ ...opened, task: "b3" });
    const detail = q('[data-bg-command-detail="b3"]')!;
    expect(detail.querySelector('[role="status"]')!.textContent).toBe("运行中");
    expect(q("[data-command-runtime]")!.textContent).toBe("20s");
    expect(q("[data-command-full]")!.textContent).toBe(monitor.command);
    expect(q("[data-command-output]")!.textContent).toBe("暂无输出。");
    expect(q("[data-bg-open-file]")).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(q('button[aria-label="返回"]'));
    // The reader unfolds the command and moves on to Copy; then the Monitor's first event names its file.
    act(() => [...detail.querySelectorAll("button")].find((b) => b.textContent === "显示完整命令")!.click());
    const copy = detail.querySelector<HTMLElement>('button[aria-label="复制命令"]')!;
    act(() => copy.focus());
    put({ background: [shell, { ...monitor, output_file: "/tmp/out/b3.log" }, sub] }); await flush();
    expect(q('[data-bg-command-detail="b3"]')).toBe(detail);
    expect(q("[data-command-output] pre")!.textContent).toBe("Sample output\n");
    expect(String(vi.mocked(fetch).mock.calls[0]![0])).toContain("path=%2Ftmp%2Fout%2Fb3.log");
    expect(document.activeElement).toBe(copy);
    expect([...detail.querySelectorAll("button")].some((b) => b.textContent === "收起")).toBe(true);
    act(() => q("[data-bg-open-file]")!.click());
    expect(getPanel()).toEqual({ kind: "file", mid: "m", path: "/tmp/out/b3.log", cwd: "/work" });
  });

  it("reopens an ended Monitor that never named an output file from Completed, saying it had no output", async () => {
    const monitor: BackgroundTask = { id: "b3", kind: "monitor", description: "Watch the build log", command: "tail -f build.log", started_at: at(-90), status: "completed", ended_at: at(-30) };
    put({ background: [shell], background_ended: [monitor] });
    draw();
    act(() => q('[data-background-section="completed"] [data-bg-task="b3"] [data-bg-row-button]')!.click()); await flush();
    expect(q('[data-bg-command-detail="b3"] [role="status"]')!.textContent).toBe("已完成");
    expect(q("[data-command-runtime]")!.textContent).toBe("1m");
    expect(q("[data-command-output]")!.textContent).toBe("无输出。");
    expect(q("[data-bg-open-file]")).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
    // A command's detail is as before: it reads its output and offers it as a file.
    act(() => q('button[aria-label="返回"]')!.click());
    act(() => q('[data-bg-task="b1"] [data-bg-row-button]')!.click()); await flush();
    expect(q("[data-command-output] pre")!.textContent).toBe("Sample output\n");
    expect(q("[data-bg-open-file]")).not.toBeNull();
  });

  it("returns to the same scroll position and row focus with Back", async () => {
    put({ background: [shell, sub] }); draw();
    q("[data-background-list]")!.scrollTop = 280;
    act(() => q('[data-bg-task="b1"] [data-bg-row-button]')!.click());
    await flush();
    expect(document.activeElement).toBe(q('button[aria-label="返回"]'));
    act(() => q('button[aria-label="返回"]')!.click());
    expect(q("[data-background-list]")!.scrollTop).toBe(280);
    expect(document.activeElement).toBe(q('[data-bg-task="b1"] [data-bg-row-button]'));
  });

  it("keeps the last output on read failure and offers a translated Retry action", async () => {
    put({ background: [shell] }); draw({ kind: "background", mid: "m", sid: "s", task: "b1" }); await flush();
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ error: "Synthetic unavailable" }), { status: 502 }));
    act(() => vi.advanceTimersByTime(2000)); await flush();
    expect(q("[data-command-output] pre")!.textContent).toBe("Sample output\n");
    const retry = all('[role="alert"] button').find(button => button.textContent === "重试");
    expect(retry).toBeDefined();
    act(() => retry!.click()); await flush();
    expect(q('[role="alert"]')).toBeNull();
    expect(q("[data-command-output] pre")!.textContent).toBe("Sample output\n");
  });

  it("honors an explicit command ask and ignores a missing one that arrives later", async () => {
    put({ background: [shell] }); draw({ kind: "background", mid: "m", sid: "s", task: "b1" });
    await flush();
    expect(q('[data-bg-command-detail="b1"]')).not.toBeNull();
    draw({ kind: "background", mid: "m", sid: "s", task: "b9" });
    put({ background: [shell, { ...shell, id: "b9" }] });
    expect(q("[data-bg-command-detail]")).toBeNull();
    expect(q('[data-bg-task="b9"]')).not.toBeNull();
  });

  it("updates an open command to its retained outcome and reopens it from Completed", async () => {
    put({ background: [shell] });
    draw({ kind: "background", mid: "m", sid: "s", task: "b1" }); await flush();
    const beforeEnd = vi.mocked(fetch).mock.calls.length;
    put({ background: [], background_ended: [{ ...shell, status: "completed", exit_code: 0, ended_at: at(0) }] });
    await flush();
    expect(q('[data-bg-command-detail="b1"]')).not.toBeNull();
    expect(q('[role="status"]')!.textContent).toBe("已完成");
    expect(q("[data-command-exit-code]")!.textContent).toBe("退出码 0");
    expect(q("[data-command-runtime]")!.textContent).toBe("1m 5s");
    expect(vi.mocked(fetch).mock.calls.length).toBe(beforeEnd + 1);
    act(() => vi.advanceTimersByTime(6000)); await flush();
    expect(vi.mocked(fetch).mock.calls.length).toBe(beforeEnd + 1);
    act(() => q('button[aria-label="返回"]')!.click());
    expect(q('[data-background-section="completed"] [data-bg-task="b1"]')).not.toBeNull();
    act(() => q('[data-bg-task="b1"] [data-bg-row-button]')!.click()); await flush();
    expect(q('[data-bg-command-detail="b1"] [role="status"]')!.textContent).toBe("已完成");
    expect(q("[data-command-runtime]")!.textContent).toBe("1m 5s");
  });

  it("prefers a retained outcome over a stale running row and stops polling", async () => {
    put({ background: [shell], background_ended: [{ ...shell, status: "completed", exit_code: 0, ended_at: at(0) }] });
    draw();
    expect(q('[data-background-section="running"]')).toBeNull();
    expect(q('[data-background-section="completed"] [data-bg-task="b1"]')).not.toBeNull();
    draw({ kind: "background", mid: "m", sid: "s", task: "b1" }); await flush();
    expect(q('[data-bg-command-detail="b1"] [role="status"]')!.textContent).toBe("已完成");
    const reads = vi.mocked(fetch).mock.calls.length;
    act(() => vi.advanceTimersByTime(6000)); await flush();
    expect(vi.mocked(fetch).mock.calls.length).toBe(reads);
  });

  it("keeps the latest command and output path after it leaves the running list, with one last read", async () => {
    put({ background: [shell] }); draw({ kind: "background", mid: "m", sid: "s", task: "b1" }); await flush();
    const updated = { ...shell, description: "Latest command title", command: "printf updated", output_file: "/tmp/updated.log" };
    put({ background: [updated] }); await flush();
    const beforeEnd = vi.mocked(fetch).mock.calls.length;
    put({ background: [] }); await flush();
    expect(q("[data-bg-command-detail]")!.textContent).toContain("Latest command title");
    expect(q('[role="status"]')!.textContent).toBe("已不在运行列表中");
    expect(vi.mocked(fetch).mock.calls.length).toBe(beforeEnd + 1);
    act(() => vi.advanceTimersByTime(6000)); await flush();
    expect(vi.mocked(fetch).mock.calls.length).toBe(beforeEnd + 1);
    act(() => q("[data-bg-open-file]")!.click());
    expect(getPanel()).toEqual({ kind: "file", mid: "m", cwd: "/work", path: "/tmp/updated.log" });
  });

  it("handles Escape from the outer header as Back, then lets the list's Escape reach the outer panel", async () => {
    put({ background: [shell] }); draw();
    act(() => q('[data-bg-task="b1"] [data-bg-row-button]')!.click()); await flush();
    const key = () => new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    act(() => q("header button")!.dispatchEvent(key()));
    expect(q("[data-background-list]")).not.toBeNull(); expect(outerKey).not.toHaveBeenCalled();
    act(() => q("header button")!.dispatchEvent(key()));
    expect(outerKey).toHaveBeenCalledOnce();
  });

  it("keeps detail open on IME Escape and while a menu or dialog owns Escape", async () => {
    put({ background: [shell] }); draw({ kind: "background", mid: "m", sid: "s", task: "b1" }); await flush();
    const ime = new KeyboardEvent("keydown", { key: "Escape", keyCode: 229, bubbles: true, cancelable: true });
    act(() => q("header button")!.dispatchEvent(ime));
    expect(ime.defaultPrevented).toBe(false); expect(outerKey).not.toHaveBeenCalled();
    for (const role of ["menu", "dialog"]) {
      const modal = document.createElement("div"); modal.setAttribute("role", role); document.body.append(modal);
      try { act(() => q("header button")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))); }
      finally { modal.remove(); }
      expect(q('[data-bg-command-detail="b1"]')).not.toBeNull();
    }
  });

  it.each(["owner", "manager", "session"])("retires the selected detail on %s change", async (scope) => {
    put({ background: [shell], manager: "manager-a" }); draw({ kind: "background", mid: "m", sid: "s", task: "b1" }); await flush();
    if (scope === "owner") act(() => setToken("synthetic-other-owner"));
    else if (scope === "manager") put({ background: [shell], manager: "manager-b" });
    else act(() => reset());
    expect(q("[data-bg-command-detail]")).toBeNull();
    expect(q("[data-bg-open-file]")).toBeNull();
    if (scope === "owner") expect(closed).toHaveBeenCalledOnce();
  });

  it.each(["owner", "manager", "session"])("fences a list row's stale copy and open handlers on %s change", (scope) => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    put({ background: [shell] }); draw();
    const copy = q('[data-bg-task="b1"] button[aria-label="复制命令"]')!;
    const row = q('[data-bg-task="b1"] [data-bg-row-button]')!;
    act(() => {
      if (scope === "owner") setToken("synthetic-other-owner");
      else if (scope === "manager") upsertSession({ machine_id: "m", session_id: "s", background: [shell], manager: "other-manager" } as Session);
      else reset();
      expect(copy.isConnected).toBe(true); copy.click(); row.click();
    });
    expect(writeText).not.toHaveBeenCalled(); expect(getPanel()).toBeNull();
  });
});

describe("the loop", () => {
  it("names its task, how often and when it wakes next, and stops only once asked twice", () => {
    const stopLoop = vi.fn();
    const take = provideBackgroundChat("m", "s", { stopLoop });
    put({ loop });
    draw();
    const row = q("[data-bg-loop]")!;
    expect(row.querySelector("[data-bg-kind]")!.getAttribute("data-bg-kind")).toBe("loop");
    expect(row.textContent).toContain("check the deploy");
    expect(meta("[data-bg-loop]")).toMatch(/^每 30 分钟 · 下次唤醒 \d\d:\d\d（还有 4 分钟）$/);
    const stop = row.querySelector<HTMLElement>('button[aria-label="停止循环"]')!;
    act(() => stop.click());
    expect(stopLoop).not.toHaveBeenCalled();
    const confirm = q("[data-bg-loop-confirm]")!;
    expect(confirm.textContent).toContain("不再安排下一次唤醒");
    act(() => [...confirm.querySelectorAll("button")].find((b) => b.textContent === "取消")!.click());
    expect(q("[data-bg-loop-confirm]")).toBeNull();
    act(() => stop.click());
    act(() => [...q("[data-bg-loop-confirm]")!.querySelectorAll("button")].find((b) => b.textContent === "停止循环")!.click());
    expect(stopLoop).toHaveBeenCalledTimes(1);
    expect(q("[data-bg-loop-confirm]")).toBeNull();
    act(() => take());
  });

  it("offers no Stop while no chat page shows the session, and offers it once one does (#5421 review)", () => {
    put({ loop });
    draw();
    expect(q('[data-bg-loop] button[aria-label="停止循环"]')).toBeNull();
    const stopLoop = vi.fn();
    let take = () => {};
    act(() => void (take = provideBackgroundChat("m", "s", { stopLoop })));
    expect(q('[data-bg-loop] button[aria-label="停止循环"]')).not.toBeNull();
    act(() => take());
    expect(q('[data-bg-loop] button[aria-label="停止循环"]')).toBeNull();
  });

  it("says Loop when it has no task in words", () => {
    put({ loop: { kind: "dynamic", prompt: "/loop" } });
    draw();
    expect(q("[data-bg-loop]")!.textContent).toContain("循环");
  });
});

describe("a run's detail", () => {
  it("opens from its row, with Back, the run's mark and name over its card, and Back gives the row the focus", () => {
    put({ workflows: [run("w1"), run("w2", { started_at: at(-30) })] });
    draw();
    act(() => q('[data-bg-run="w2"] [data-bg-row-button]')!.click());
    const detail = q('[data-background-detail="w2"]')!;
    expect(q("[data-background-list]")).toBeNull();
    const back = detail.querySelector<HTMLElement>('button[aria-label="返回"]')!;
    expect(document.activeElement).toBe(back);
    expect(detail.querySelector("h3")!.textContent).toBe("run-w2");
    expect(detail.querySelector<HTMLElement>('[data-workflow-card="running"]')!.dataset.workflowTask).toBe("w2");
    expect(detail.querySelectorAll("[data-phase]")).toHaveLength(1);
    act(() => back.click());
    expect(q("[data-background-detail]")).toBeNull();
    expect(document.activeElement).toBe(q('[data-bg-run="w2"] [data-bg-row-button]'));
  });

  it("opens at the run the panel was asked for, every time it is asked; a new ask without one is the list", () => {
    put({ workflows: [run("w1"), run("w2")] });
    draw({ kind: "background", mid: "m", sid: "s", run: "w1" });
    expect(q('[data-background-detail="w1"]')).not.toBeNull();
    act(() => q('button[aria-label="返回"]')!.click());
    expect(q("[data-background-list]")).not.toBeNull();
    draw({ kind: "background", mid: "m", sid: "s", run: "w1" }); // a card asks again: a new object
    expect(q('[data-background-detail="w1"]')).not.toBeNull();
    draw({ kind: "background", mid: "m", sid: "s", run: "w2" });
    expect(q('[data-background-detail="w2"]')).not.toBeNull();
    draw({ kind: "background", mid: "m", sid: "s" });
    expect(q("[data-background-list]")).not.toBeNull();
  });

  it("clears an overview detail's origin when its run disappears, so the next detail returns to the list", async () => {
    put({ workflows: [run("w1"), run("w2")] });
    const session = getSession("m", "s")!;
    function OverviewAndPanel() {
      const opened = backgroundPanelOf(usePanel(), "m", "s");
      return <>
        <SessionOverview id="overview" session={session} machineName="Machine" items={[]} tasks={emptyTaskIndex()} children={[]} hidden={opened?.from === "overview"} />
        {opened && <BackgroundPanel mid="m" sid="s" opened={opened} onClose={closePanel} />}
      </>;
    }
    act(() => root.render(<OverviewAndPanel />));
    act(() => openPanel({ kind: "background", mid: "m", sid: "s", run: "w1", from: "overview" }));
    await flush();
    const overview = q("[data-session-overview]")!;
    expect(overview.hidden).toBe(true);
    expect(document.activeElement).toBe(q('[data-background-detail="w1"] button[aria-label="返回"]'));

    put({ workflows: [run("w2")] });
    await flush();
    const listed = { kind: "background", mid: "m", sid: "s" } as const;
    expect(getPanel()).toEqual(listed);
    expect(overview.hidden).toBe(false);
    expect(q("[data-background-list]")).not.toBeNull();

    act(() => q('[data-background-panel] [data-bg-run="w2"] [data-bg-row-button]')!.click());
    await flush();
    expect(q('[data-background-detail="w2"]')).not.toBeNull();
    act(() => q('[data-background-detail="w2"] button[aria-label="返回"]')!.click());
    expect(q("[data-background-list]")).not.toBeNull();
    expect(q("[data-background-panel]")).not.toBeNull();
    expect(getPanel()).toEqual(listed);
  });

  it("falls back to the list when the run is gone from the session", () => {
    put({ workflows: [run("w1")] });
    draw({ kind: "background", mid: "m", sid: "s", run: "w9" });
    expect(q("[data-background-detail]")).toBeNull();
    expect(q('[data-bg-run="w1"]')).not.toBeNull();
  });

  it("drops an ask for a run it does not have: the run coming later does not take the list away (#5421 review)", () => {
    put({ workflows: [run("w1")] });
    draw({ kind: "background", mid: "m", sid: "s", run: "w9" });
    put({ workflows: [run("w1"), run("w9")] });
    expect(q("[data-background-detail]")).toBeNull();
    expect(q('[data-bg-run="w9"]')).not.toBeNull();
  });

  it("shows the call in the chat only while the chat page can, and says so to it", () => {
    put({ workflows: [run("w1")] });
    draw({ kind: "background", mid: "m", sid: "s", run: "w1" });
    expect(q("[data-workflow-jump]")).toBeNull();
    const jumpToCall = vi.fn();
    // a page that comes while the detail shows: the detail offers it without being drawn again for it
    let take = () => {};
    act(() => void (take = provideBackgroundChat("m", "s", { stopLoop: () => {}, jumpToCall })));
    act(() => q("[data-workflow-jump]")!.click());
    expect(jumpToCall).toHaveBeenCalledWith("call-w1");
    expect(closed).not.toHaveBeenCalled(); // wide: the chat is beside the panel
    act(() => take());
    expect(q("[data-workflow-jump]")).toBeNull();
  });
});

describe("backgroundSections", () => {
  it("is empty for no session", () => {
    expect(backgroundSections(undefined, T0)).toEqual({ loop: undefined, running: [], tasks: [], completed: [], completedTasks: [] });
  });
});
