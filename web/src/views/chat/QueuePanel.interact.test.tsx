// @vitest-environment jsdom
// The queued messages at the top of the composer, as cindy's pending queue (#5327): each row's actions and the hub
// requests they make, reordering from the handle by keyboard and by dragging, the fold past four rows, Send now at one
// press, and relayed rows that only show. The markup checks are in ChatDock.test.tsx.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import type { Session } from "../../lib/types";
import { QueuePanel } from "./QueuePanel";

vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: vi.fn() }));
const calls = () => vi.mocked(api).mock.calls.map(([path, init]) => [path, (init as { json?: unknown } | undefined)?.json]);

const session = { machine_id: "m", session_id: "s", agent: "claude", state: "working", cwd: "/tmp/x", queued: ["第一条", "第二条", "第三条"] } as unknown as Session;

let host: HTMLDivElement;
let root: Root;
const render = (ui: React.ReactNode) => act(() => root.render(ui));

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  vi.mocked(api).mockReset();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

const rows = () => [...host.querySelectorAll<HTMLElement>("[data-queue-row]")];
const texts = () => rows().map((r) => r.querySelector("[data-queue-row-body]")!.textContent);
const within = (row: number, sel: string) => rows()[row].querySelector<HTMLElement>(sel)!;
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const key = (el: Element, k: string) => act(() => el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
const settle = () => act(() => new Promise((r) => setTimeout(r, 0)));

describe("QueuePanel row actions", () => {
  it("pulls a message back to edit or deletes it, re-queueing the others in order", async () => {
    vi.mocked(api).mockResolvedValue({ queue: ["第一条", "第二条", "第三条"] });
    const withdrawn: string[] = [];
    render(<QueuePanel session={session} draftKey="qp-edit" canEdit onWithdrawn={(t) => void withdrawn.push(t)} />);
    click(within(1, "[data-queue-edit]"));
    await settle();
    click(within(2, "[data-queue-delete]"));
    await settle();
    expect(calls()).toEqual([
      ["/sessions/m/s/queue", { keep: [0, 2] }],
      ["/sessions/m/s/queue", { keep: [0, 1] }],
    ]);
    expect(withdrawn).toEqual(["第二条", "第三条"]);
  });

  it("sends every queued message at one press, with no dialog; its tooltip and description say what happens", async () => {
    vi.mocked(api).mockResolvedValue({});
    render(<QueuePanel session={session} draftKey="qp-send" canEdit />);
    const send = within(2, "[data-send-now]");
    expect(send.getAttribute("aria-label")).toBe("现在就把排队的消息全部发给 Claude，不用等到下一步");
    expect(document.getElementById(send.getAttribute("aria-describedby")!)!.textContent).toContain("Claude 会马上收到");
    expect(send.title).toBe("现在就把排队的消息全部发给 Claude，不用等到下一步\nClaude 会马上收到，不用等到下一步。它正在跑的命令会转到后台继续；正在写的回复会带上你的消息重新开始。");
    click(send);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(calls()).toEqual([["/sessions/m/s/queue/send-now", {}]]);
    await settle();
  });

  it("says in the Persona chat's words what sending now does", () => {
    const persona = { ...session, agent: undefined, assistant: true, queued: ["我的"] } as unknown as Session;
    render(<QueuePanel session={persona} draftKey="qp-persona-send" canEdit persona="Nova" />);
    const send = within(0, "[data-send-now]");
    expect(send.getAttribute("aria-label")).toBe("现在就把排队的消息全部发给 Nova，不用等到下一步");
    expect(document.getElementById(send.getAttribute("aria-describedby")!)!.textContent).toContain("Nova 会马上收到");
  });

  it("shows a spinner where the busy row's actions were and takes no other action meanwhile", async () => {
    let answer: (v: unknown) => void = () => {};
    vi.mocked(api).mockReturnValue(new Promise((r) => (answer = r)));
    render(<QueuePanel session={session} draftKey="qp-busy" canEdit />);
    click(within(0, "[data-queue-delete]"));
    expect(rows()[0].querySelector("[data-queue-actions]")).toBeNull();
    expect(within(1, "[data-queue-delete]").hasAttribute("disabled")).toBe(true);
    click(within(1, "[data-queue-delete]"));
    expect(api).toHaveBeenCalledTimes(1);
    answer({ queue: session.queued });
    await settle();
    expect(within(1, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);
  });

  it("collapses the relayed rows into one line, which opens to their read-only rows (#4342, #6394)", () => {
    const persona = { ...session, agent: undefined, assistant: true, queued: ["我的", "来自「日报」：汇总"], queued_relayed: 1 } as unknown as Session;
    render(<QueuePanel session={persona} draftKey="qp-relayed" canEdit persona="Nova" />);
    // collapsed: the owner's row stays, the relayed one is only the summary line
    expect(rows()).toHaveLength(1);
    const summary = host.querySelector<HTMLElement>("[data-queue-relayed-summary]")!;
    expect(summary.textContent).toContain("还有 1 条别的对话在排队");
    expect(summary.getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-queue-relayed-list]")).toBeNull();
    // the user's own row has nothing to move past
    expect(rows()[0].querySelector("[data-queue-handle]")).toBeNull();
    expect(rows()[0].querySelector("[data-send-now]")).not.toBeNull();
    // open: the relayed row appears, still with no handle and no actions, and the note says where it came from
    click(summary);
    expect(summary.getAttribute("aria-expanded")).toBe("true");
    expect(rows()).toHaveLength(2);
    expect(rows()[1].querySelector("button")).toBeNull();
    expect(rows()[1].querySelector("[data-queue-note]")!.textContent).toBe("其他对话");
    click(summary);
    expect(host.querySelector("[data-queue-relayed-list]")).toBeNull();
    expect(rows()).toHaveLength(1);
  });
});

describe("QueuePanel reorder", () => {
  it("moves a message with ↑ ↓ Home End on its handle, keeping the new order until the hub pushes it", async () => {
    vi.mocked(api).mockResolvedValue({});
    render(<QueuePanel session={session} draftKey="qp-keys" canEdit />);
    const handle = within(2, "[data-queue-handle]");
    handle.focus();
    key(handle, "ArrowUp");
    expect(calls()).toEqual([["/sessions/m/s/queue", { keep: [0, 2, 1] }]]);
    // shown in its new place at once, and the focus stays on it
    expect(texts()).toEqual(["第一条", "第三条", "第二条"]);
    expect(document.activeElement).toBe(handle);
    // nothing else moves until the hub has it
    key(handle, "ArrowUp");
    expect(api).toHaveBeenCalledTimes(1);
    await settle();
    render(<QueuePanel session={{ ...session, queued: ["第一条", "第三条", "第二条"] }} draftKey="qp-keys" canEdit />);
    key(within(1, "[data-queue-handle]"), "Home");
    key(within(0, "[data-queue-handle]"), "End");
    expect(calls().slice(1)).toEqual([["/sessions/m/s/queue", { keep: [1, 0, 2] }]]);
    await settle();
    render(<QueuePanel session={{ ...session, queued: ["第三条", "第一条", "第二条"] }} draftKey="qp-keys" canEdit />);
    key(within(0, "[data-queue-handle]"), "End");
    key(within(2, "[data-queue-handle]"), "ArrowDown");
    expect(calls().slice(2)).toEqual([["/sessions/m/s/queue", { keep: [1, 2, 0] }]]);
  });

  it("goes back to the hub's order when the reorder fails", async () => {
    vi.mocked(api).mockRejectedValue(new Error("nope"));
    render(<QueuePanel session={session} draftKey="qp-fail" canEdit />);
    key(within(0, "[data-queue-handle]"), "ArrowDown");
    expect(texts()).toEqual(["第二条", "第一条", "第三条"]);
    await settle();
    expect(texts()).toEqual(["第一条", "第二条", "第三条"]);
  });

  it("drags a message by its handle to where the pointer lets go", async () => {
    vi.mocked(api).mockResolvedValue({});
    render(<QueuePanel session={session} draftKey="qp-drag" canEdit />);
    // rows 24 px tall from the top of the page
    rows().forEach((r, i) => (r.getBoundingClientRect = () => ({ top: i * 24, height: 24, bottom: i * 24 + 24, left: 0, right: 300, width: 300, x: 0, y: i * 24, toJSON: () => ({}) })));
    const pointer = (type: string, y: number, at: EventTarget = window) => act(() => void at.dispatchEvent(new PointerEvent(type, { clientY: y, pointerId: 1, button: 0, bubbles: true })));
    pointer("pointerdown", 12, within(0, "[data-queue-handle]"));
    pointer("pointermove", 40);
    expect(texts()).toEqual(["第二条", "第一条", "第三条"]);
    pointer("pointermove", 70);
    expect(texts()).toEqual(["第二条", "第三条", "第一条"]);
    expect(api).not.toHaveBeenCalled();
    pointer("pointerup", 70);
    expect(calls()).toEqual([["/sessions/m/s/queue", { keep: [1, 2, 0] }]]);
    await settle();
    // let go where it started: nothing sent
    render(<QueuePanel session={{ ...session, queued: ["第二条", "第三条", "第一条"] }} draftKey="qp-drag" canEdit />);
    rows().forEach((r, i) => (r.getBoundingClientRect = () => ({ top: i * 24, height: 24, bottom: i * 24 + 24, left: 0, right: 300, width: 300, x: 0, y: i * 24, toJSON: () => ({}) })));
    pointer("pointerdown", 36, within(1, "[data-queue-handle]"));
    pointer("pointermove", 30);
    pointer("pointerup", 30);
    expect(api).toHaveBeenCalledTimes(1);
  });
});

describe("QueuePanel fold", () => {
  it("shows three of five or more with N more, opens to all and folds back", () => {
    render(<QueuePanel session={{ ...session, queued: ["一", "二", "三", "四", "五", "六"] }} draftKey="qp-fold" canEdit />);
    expect(texts()).toEqual(["一", "二", "三"]);
    const fold = host.querySelector<HTMLButtonElement>("[data-queue-fold]")!;
    expect(fold.textContent).toBe("还有 3 条");
    click(fold);
    expect(texts()).toEqual(["一", "二", "三", "四", "五", "六"]);
    expect(fold.textContent).toBe("收起");
    expect(fold.getAttribute("aria-expanded")).toBe("true");
    click(fold);
    expect(texts()).toHaveLength(3);
  });

  it("opens when a keyboard move takes a row past the fold, so it stays in view", () => {
    vi.mocked(api).mockResolvedValue({});
    render(<QueuePanel session={{ ...session, queued: ["一", "二", "三", "四", "五"] }} draftKey="qp-fold-move" canEdit />);
    key(within(0, "[data-queue-handle]"), "End");
    expect(calls()).toEqual([["/sessions/m/s/queue", { keep: [1, 2, 3, 4, 0] }]]);
    expect(texts()).toEqual(["二", "三", "四", "五", "一"]);
  });
});
