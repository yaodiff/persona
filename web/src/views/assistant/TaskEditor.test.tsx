// @vitest-environment jsdom
// The scheduled task editor's words: a new task says what is missing only once the user has changed something, its
// time hints name the time zone the hub reads them in (as the list's footer does), and its hints use everyday words
// and the permission option's real name.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { blankTask, withRepeat, type Task } from "../../lib/routines";
import { getState } from "../../lib/store";
import { Toasts } from "../../ui/atoms";
import { TaskEditor } from "./TaskEditor";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement;
let root: Root;
const assistantBefore = getState().assistant;

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json({ error: "not found" }, 404))));
  Object.assign(getState(), { assistant: { machine_id: "server", timezone: "Europe/Paris", conversations: [] } });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  Object.assign(getState(), { assistant: assistantBefore });
  vi.unstubAllGlobals();
});

const open = (task: Task) => act(async () => root.render(<><TaskEditor task={task} onClose={() => {}} onSaved={() => {}} onDeleted={() => {}} /><Toasts /></>));
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const button = (text: string) => [...dialog().parentElement!.querySelectorAll("button")].find((b) => b.textContent === text)!;
const add = () => button("添加");
const title = () => dialog().querySelector<HTMLInputElement>("input:not([type])")!;
const stateNote = () => dialog().querySelector<HTMLTextAreaElement>("details textarea")!;
const type = (el: HTMLInputElement | HTMLTextAreaElement, value: string) => act(() => {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});

describe("the scheduled task editor", () => {
  it("opens a blank task with a grey Add and no error, and says what is missing once something changed", async () => {
    await open(blankTask());
    expect(add().disabled).toBe(true);
    expect(document.body.textContent).not.toContain("请填写标题");

    type(dialog().querySelector("textarea")!, "看看今天的日程");
    expect(document.body.textContent).toContain("请填写标题");
    expect(add().disabled).toBe(true);

    type(dialog().querySelector<HTMLInputElement>("input:not([type])")!, "早报");
    expect(document.body.textContent).not.toContain("请填写标题");
    expect(add().disabled).toBe(false);
  });

  it("names the hub's time zone for a daily time and a cron expression, not 'your time zone'", async () => {
    await open(blankTask());
    expect(dialog().textContent).toContain("按 Europe/Paris 时区");
    expect(dialog().textContent).not.toContain("你所在的时区");

    act(() => root.unmount());
    root = createRoot(host);
    await open(withRepeat(blankTask(), "cron", new Date()));
    expect(dialog().textContent).toContain("按 Europe/Paris 时区计算");
    expect(dialog().textContent).not.toContain("你的时区");
  });

  it("says 'your time zone' only when the hub doesn't name one", async () => {
    Object.assign(getState(), { assistant: { machine_id: "server", conversations: [] } });
    await open(blankTask());
    expect(dialog().textContent).toContain("按你所在的时区");
  });

  it("hints in everyday words, naming the permission option as the menu above it does", async () => {
    await open(blankTask());
    const text = dialog().textContent ?? "";
    expect(text).not.toContain("schedule_followup");
    const options = [...dialog().querySelectorAll("option")].map((o) => o.textContent);
    expect(options).toContain("自动审批");
    expect(text).toContain("可选择「自动审批」");
    expect(text).not.toContain("「自动」");
  });

  const taskWithNote = (): Task => ({
    ...blankTask(), id: "scheduled", title: "早报", prompt: "看看今天的日程",
    state_note: { text: "older note", revision: 1, updated_at: "2026-11-14T12:00:00Z" },
  });

  it("keeps a run's newer note when saving other fields after a failed Run now", async () => {
    const task = taskWithNote();
    const newerNote = { text: "newer note", revision: 2, updated_at: "2026-11-14T13:00:00Z" };
    const puts: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname;
      if (path.endsWith("/tasks/scheduled") && init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        puts.push(body);
        return json({ task: { ...task, title: body.title, state_note: newerNote } });
      }
      if (path.endsWith("/tasks/scheduled/run")) return json({ error: "Already running" }, 409);
      return json({ error: "not found" }, 404);
    }));

    await open(task);
    type(title(), "新早报");
    await act(async () => button("立即运行").click());
    expect(stateNote().value).toBe("newer note");
    type(title(), "工作日早报");
    await act(async () => button("保存").click());

    expect(puts.map((body) => body.title)).toEqual(["新早报", "工作日早报"]);
    for (const body of puts) {
      expect(body).not.toHaveProperty("state_text");
      expect(body).not.toHaveProperty("state_revision");
    }
  });

  it("keeps edits made during a pending save and takes the returned state note", async () => {
    const task = { ...taskWithNote(), model_revision: 1 };
    const newerNote = { text: "newer note", revision: 2, updated_at: "2026-11-14T13:00:00Z" };
    const puts: Record<string, unknown>[] = [];
    let answer!: (response: Response) => void;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname;
      if (path.endsWith("/tasks/scheduled") && init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        puts.push(body);
        if (puts.length === 1) return new Promise<Response>((resolve) => { answer = resolve; });
        return json({ task: { ...task, title: body.title, model_revision: 2, state_note: newerNote } });
      }
      if (path.endsWith("/tasks/scheduled/run")) return json({ error: "Already running" }, 409);
      return json({ error: "not found" }, 404);
    }));

    await open(task);
    type(title(), "Submitted title");
    act(() => button("立即运行").click());
    expect(puts).toHaveLength(1);
    type(title(), "Still editing");
    await act(async () => answer(json({ task: { ...task, title: "Submitted title", model_revision: 2, state_note: newerNote } })));
    expect(title().value).toBe("Still editing");
    expect(stateNote().value).toBe("newer note");
    await act(async () => button("保存").click());
    expect(puts[1]).toMatchObject({ title: "Still editing" });
    expect(puts[1]).not.toHaveProperty("state_text");
    expect(puts[1]).not.toHaveProperty("state_revision");
  });

  it("saves a changed note with its original revision and keeps the draft if the hub rejects it", async () => {
    const puts: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") {
        puts.push(JSON.parse(String(init.body)));
        return json({ error: "任务状态笔记已更新。请重新打开编辑器后再试。", code: "task_state_changed" }, 409);
      }
      return json({ error: "not found" }, 404);
    }));

    await open(taskWithNote());
    type(stateNote(), "manual note");
    await act(async () => button("保存").click());

    expect(puts).toHaveLength(1);
    expect(puts[0]).toMatchObject({ state_text: "manual note", state_revision: 1 });
    expect(puts[0]).not.toHaveProperty("state_note");
    expect(stateNote().value).toBe("manual note");
    expect(document.body.textContent).toContain("任务状态笔记已更新。请重新打开编辑器后再试。");
  });
});

// A scheduled task's own start grant (#8045): the editor lists the kinds its runs start without asking, each removable
// (DELETE …/start-grant with that kind, the task as the hub then has it), and shows nothing while there are none.
describe("the scheduled task editor's start grant", () => {
  const machinesBefore = getState().machines;
  const granted = (kinds: string[]): Task => ({
    ...withRepeat(blankTask(), "daily", new Date(2026, 0, 2, 8)),
    id: "t1",
    title: "每周周报",
    prompt: "整理这周的笔记",
    time: "09:00",
    start_grant: { kinds, granted_at: "2026-11-01T09:00:00Z" },
  });
  const rows = () => [...dialog().querySelectorAll<HTMLElement>("[data-start-grant-kind]")];
  afterEach(() => Object.assign(getState(), { machines: machinesBefore }));

  it("shows nothing for a task without one", async () => {
    await open(granted([]));
    expect(dialog().querySelector("[data-start-grant]")).toBeNull();
    expect(dialog().textContent).not.toContain("无需确认即可开始");
  });

  it("lists each kind by computer, folder and access, and Remove takes out only that one", async () => {
    Object.assign(getState(), { machines: [{ id: "m1", name: "工作机" }] });
    const deletes: { url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "DELETE") {
        deletes.push({ url, body: JSON.parse(String(init.body)) });
        return json({ task: granted(["m1|/home/me/notes|read"]) });
      }
      return json({ error: "not found" }, 404);
    }));

    await open(granted(["m1|/home/me/notes|edit", "m1|/home/me/notes|read"]));
    expect(dialog().textContent).toContain("无需确认即可开始");
    expect(dialog().textContent).toContain("修改任务指令或运行位置将清空此列表");
    expect(rows().map((r) => r.textContent)).toEqual(["工作机 · /home/me/notes可编辑文件移除", "工作机 · /home/me/notes只读移除"]);

    const remove = rows()[0].querySelector("button")!;
    expect(remove.getAttribute("aria-label")).toBe("移除 工作机 · /home/me/notes");
    await act(async () => remove.click());
    for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });

    expect(deletes).toHaveLength(1);
    expect(deletes[0].url).toContain("/assistant/tasks/t1/start-grant");
    expect(deletes[0].body).toEqual({ kind: "m1|/home/me/notes|edit" });
    expect(rows().map((r) => r.textContent)).toEqual(["工作机 · /home/me/notes只读移除"]);
  });

  it("names a kind whose computer and folder can't be told apart by the whole place", async () => {
    await open(granted(["old|id|/srv/app|edit"]));
    expect(rows().map((r) => r.textContent)).toEqual(["old|id|/srv/app可编辑文件移除"]);
  });
});
