// @vitest-environment jsdom
// The queued messages at the top of the composer, as cindy's pending queue (#5327): each row's actions and the hub
// requests they make, reordering from the handle by keyboard and by dragging, the fold past four rows, Send now at one
// press, and relayed rows that only show. The markup checks are in ChatDock.test.tsx.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import { onDraftEvent, type DraftEvent } from "../../lib/drafts";
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
    render(<QueuePanel session={session} draftKey="qp-edit" canEdit />);
    click(within(1, "[data-queue-edit]"));
    await settle();
    render(<QueuePanel session={{ ...session, queued: ["第一条", "第三条"] }} draftKey="qp-edit" canEdit />);
    click(within(1, "[data-queue-delete]"));
    await settle();
    expect(calls()).toEqual([
      ["/sessions/m/s/queue", { keep: [0, 2] }],
      ["/sessions/m/s/queue", { keep: [0] }],
    ]);
  });

  it("hides deleted rows immediately, hides the whole panel when deleting the last item, and rolls back on failure", async () => {
    let rejectDelete!: (e: Error) => void;
    vi.mocked(api).mockReturnValueOnce(new Promise((_r, rej) => { rejectDelete = rej; }));
    render(<QueuePanel session={{ ...session, queued: ["唯一一条"] }} draftKey="qp-del-last" canEdit />);
    expect(rows()).toHaveLength(1);
    click(within(0, "[data-queue-delete]"));
    // Hidden immediately at 0ms before HTTP resolves, while keeping rollback active on failure.
    expect(rows()).toHaveLength(0);
    expect(host.querySelector("[data-queue-panel]")).toBeNull();
    rejectDelete(new Error("boom"));
    await settle();
    expect(texts()).toEqual(["唯一一条"]);

    let resolveDelete!: (v: unknown) => void;
    vi.mocked(api).mockReturnValueOnce(new Promise((r) => { resolveDelete = r; }));
    render(<QueuePanel session={session} draftKey="qp-del-multi" canEdit />);
    click(within(1, "[data-queue-delete]"));
    expect(texts()).toEqual(["第一条", "第三条"]);
    expect(rows().every((r) => [...r.querySelectorAll("button")].every((b) => b.hasAttribute("disabled") || b.getAttribute("aria-disabled") === "true"))).toBe(true);
    resolveDelete({ queue: ["第一条", "第二条", "第三条"] });
    await settle();
    // Stays hidden after 200 while waiting for the pushed queue update.
    expect(texts()).toEqual(["第一条", "第三条"]);
  });

  it("hides pulled-back and send-now rows once accepted, respecting runtime send-now scope", async () => {
    let resolvePull!: (v: unknown) => void;
    vi.mocked(api).mockReturnValueOnce(new Promise((r) => { resolvePull = r; }));
    render(<QueuePanel session={session} draftKey="qp-pull-hide" canEdit />);
    click(within(1, "[data-queue-edit]"));
    // Spinner shows while pulling back; row disappears as soon as pull-back succeeds.
    expect(texts()).toEqual(["第一条", "第二条", "第三条"]);
    resolvePull({ queue: ["第一条", "第二条", "第三条"] });
    await settle();
    expect(texts()).toEqual(["第一条", "第三条"]);

    // Non-runtime Send now hides all own rows upon acceptance.
    let resolveSend!: (v: unknown) => void;
    vi.mocked(api).mockReturnValueOnce(new Promise((r) => { resolveSend = r; }));
    render(<QueuePanel session={{ ...session, queued: ["甲", "乙"] }} draftKey="qp-sendnow-all" canEdit />);
    click(within(0, "[data-send-now]"));
    expect(texts()).toEqual(["甲", "乙"]);
    resolveSend({});
    await settle();
    expect(rows()).toHaveLength(0);

    // Runtime Send now sends only the first queued message, so remaining rows stay visible.
    let resolveRuntimeSend!: (v: unknown) => void;
    vi.mocked(api).mockReturnValueOnce(new Promise((r) => { resolveRuntimeSend = r; }));
    const runtime: Session = { ...session, agent: "agy", source_kind: "runtime", capabilities: { queue_send_now: true }, queued: ["先发", "保留"] };
    render(<QueuePanel session={runtime} draftKey="qp-sendnow-rt" canEdit />);
    click(within(0, "[data-send-now]"));
    resolveRuntimeSend({});
    await settle();
    expect(texts()).toEqual(["保留"]);
  });

  it("hides a withdrawn in-flight optimistic row immediately and still deletes once confirmed", async () => {
    vi.mocked(api).mockResolvedValue({ queue: ["确认后"] });
    const cancelQueued = vi.fn(() => false);
    render(
      <QueuePanel
        session={{ ...session, queued: [] }}
        draftKey="qp-withdraw"
        canEdit
        optimistic={[{ id: "opt-1", raw: "确认后" }]}
        cancelQueued={cancelQueued}
      />,
    );
    expect(texts()).toEqual(["确认后"]);
    click(within(0, "[data-queue-delete]"));
    expect(cancelQueued).toHaveBeenCalledWith("opt-1");
    expect(rows()).toHaveLength(0);
    expect(host.querySelector("[data-queue-panel]")).toBeNull();

    // Server confirms opt-1 with matching candidate: queued update arrives, triggers delete, and remains hidden.
    render(
      <QueuePanel
        session={{ ...session, queued: ["确认后"] }}
        draftKey="qp-withdraw"
        canEdit
        pending={[{ id: "opt-1", raw: "确认后", before: [] }]}
        optimistic={[]}
        cancelQueued={cancelQueued}
      />,
    );
    await settle();
    expect(calls()).toEqual([["/sessions/m/s/queue", { keep: [] }]]);
    expect(rows()).toHaveLength(0);
  });

  it("drops optimistic hold when own count shrinks via queued_relayed even if queued strings stay unchanged", async () => {
    vi.mocked(api).mockResolvedValue({ queue: ["第一条", "第二条"] });
    render(<QueuePanel session={{ ...session, queued: ["第一条", "第二条"] }} draftKey="qp-own-shrink" canEdit />);
    // Delete index 0 so holdOrder is [1] (an index that becomes out-of-bounds once own shrinks to 1).
    click(within(0, "[data-queue-delete]"));
    await settle();
    expect(texts()).toEqual(["第二条"]);

    // Hub increases queued_relayed to 1 while `queued` strings stay identical.
    render(<QueuePanel session={{ ...session, queued: ["第一条", "第二条"], queued_relayed: 1 }} draftKey="qp-own-shrink" canEdit />);
    expect(texts()).toEqual(["第一条"]);
    expect(within(0, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);
  });

  it("sends every queued message at one press, with no dialog; its tooltip and description say what happens", async () => {
    vi.mocked(api).mockResolvedValue({});
    render(<QueuePanel session={session} draftKey="qp-send" canEdit />);
    const send = within(2, "[data-send-now]");
    expect(send.getAttribute("aria-label")).toBe("立即将所有排队消息发送给 Claude，无需等待下一步");
    expect(document.getElementById(send.getAttribute("aria-describedby")!)!.textContent).toContain("Claude 将立即收到这些消息");
    expect(send.title).toBe("立即将所有排队消息发送给 Claude，无需等待下一步\nClaude 将立即收到这些消息，无需等待下一步。正在运行的命令会转至后台继续执行，正在生成的回复则会结合你的消息重新开始。");
    click(send);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(calls()).toEqual([["/sessions/m/s/queue/send-now", {}]]);
    await settle();
  });

  it.each([[true, true], [false, true], [true, false], [false, false]])("respects runtime queue capabilities (edit %s, steer %s)", (queue_edit, queue_send_now) => {
    const runtime: Session = { ...session, agent: "agy", source_kind: "runtime", capabilities: { queue_edit, queue_send_now } };
    render(<QueuePanel session={runtime} draftKey="qp-runtime-capabilities" canEdit />);
    expect(Boolean(rows()[0].querySelector("[data-queue-edit]"))).toBe(queue_edit);
    expect(Boolean(rows()[0].querySelector("[data-send-now]"))).toBe(queue_send_now);
  });

  it("says in the Persona chat's words what sending now does", () => {
    const persona = { ...session, agent: undefined, assistant: true, queued: ["我的"] } as unknown as Session;
    render(<QueuePanel session={persona} draftKey="qp-persona-send" canEdit persona="Nova" />);
    const send = within(0, "[data-send-now]");
    expect(send.getAttribute("aria-label")).toBe("立即将所有排队消息发送给 Nova，无需等待下一步");
    expect(document.getElementById(send.getAttribute("aria-describedby")!)!.textContent).toContain("Nova 将立即收到这些消息");
  });

  it("explains that a runtime assistant stops its turn and starts only the next queued message", () => {
    const runtime: Session = { ...session, agent: "agy", assistant: true, source_kind: "runtime", capabilities: { queue_send_now: true } };
    render(<QueuePanel session={runtime} draftKey="qp-runtime-assistant-send" canEdit persona="Nova" />);
    const send = within(0, "[data-send-now]");
    const tip = "停止 Nova 当前轮次，立即开始处理下一条排队消息";
    const body = "Nova 将停止当前轮次，开始处理下一条排队消息，其余消息仍按原顺序排队。";
    expect(send.getAttribute("aria-label")).toBe(tip);
    expect(document.getElementById(send.getAttribute("aria-describedby")!)!.textContent).toBe(body);
    expect(send.title).toBe(`${tip}\n${body}`);
  });

  it("shows a spinner where the busy row's actions were and takes no other action meanwhile", async () => {
    let answer: (v: unknown) => void = () => {};
    vi.mocked(api).mockReturnValue(new Promise((r) => (answer = r)));
    render(<QueuePanel session={session} draftKey="qp-busy" canEdit />);
    click(within(0, "[data-queue-edit]"));
    expect(rows()[0].querySelector("[data-queue-actions]")).toBeNull();
    expect(within(1, "[data-queue-delete]").hasAttribute("disabled")).toBe(true);
    click(within(1, "[data-queue-delete]"));
    expect(api).toHaveBeenCalledTimes(1);
    answer({ queue: session.queued });
    await settle();
    // Row 0 is held out after edit succeeded; row 1 becomes the new index 0 and unlocks cleanly.
    expect(texts()).toEqual(["第二条", "第三条"]);
    render(<QueuePanel session={{ ...session, queued: ["第二条", "第三条"] }} draftKey="qp-busy" canEdit />);
    expect(within(0, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);
  });

  it("collapses the relayed rows into one line, which opens to their read-only rows (#4342, #6394)", () => {
    const persona = { ...session, agent: undefined, assistant: true, queued: ["我的", "来自「日报」：汇总"], queued_relayed: 1 } as unknown as Session;
    render(<QueuePanel session={persona} draftKey="qp-relayed" canEdit persona="Nova" />);
    // collapsed: the owner's row stays, the relayed one is only the summary line
    expect(rows()).toHaveLength(1);
    const summary = host.querySelector<HTMLElement>("[data-queue-relayed-summary]")!;
    expect(summary.textContent).toContain("1 条消息来自其他对话 · 对话空闲后送达");
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
    expect(rows()[1].querySelector("[data-queue-note]")).toBeNull();
    expect(rows()[1].querySelector("[data-queue-from]")!.textContent).toBe("日报");
    click(summary);
    expect(host.querySelector("[data-queue-relayed-list]")).toBeNull();
    expect(rows()).toHaveLength(1);
  });

  it("only shows a runtime Agent's queue: the node takes no edit, reorder or Send now there (#8142)", () => {
    // a Claude Code Agent on Paseo: its queue is Claude Code's, read off its transcript
    const paseo = { ...session, source_kind: "runtime", source_instance_id: "paseo", launcher: "my-claude" } as unknown as Session;
    render(<QueuePanel session={paseo} draftKey="qp-runtime" canEdit />);
    expect(texts()).toEqual(["第一条", "第二条", "第三条"]);
    for (const r of rows()) expect(r.querySelector("button")).toBeNull();
    expect(host.querySelector("[data-queue-explain]")).toBeNull();
  });

  it("renders queued terminal agy follow-ups with Send now and no edit actions (#14871)", async () => {
    vi.mocked(api).mockResolvedValue({});
    const agy = { ...session, agent: "agy", queued: ["Check the linter..."] } as unknown as Session;
    const candidate = {
      id: "c1",
      raw: "Check the linter output carefully",
      images: [{ mime: "image/png", data: "AAAA" }],
      before: [],
    };
    render(<QueuePanel session={agy} draftKey="qp-agy" canEdit pending={[candidate]} />);
    expect(texts()).toEqual(["Check the linter..."]);
    expect(rows()[0].querySelector("[data-queue-edit]")).toBeNull();
    expect(rows()[0].querySelector("[data-queue-delete]")).toBeNull();
    expect(rows()[0].querySelector("[data-queue-photo-thumb]")).not.toBeNull();
    const send = within(0, "[data-send-now]");
    click(send);
    expect(calls()).toEqual([["/sessions/m/s/queue/send-now", {}]]);
    await settle();
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

// jsdom has no layout: a cut line is simulated by the getters of scrollWidth / clientWidth
describe("QueuePanel relayed group", () => {
  const persona = (queued: string[], relayed: number) => ({ ...session, agent: undefined, assistant: true, queued, queued_relayed: relayed }) as unknown as Session;
  const show = (queued: string[], relayed: number) => render(<QueuePanel session={persona(queued, relayed)} draftKey="qp-rg" canEdit persona="Nova" />);
  const group = () => host.querySelector<HTMLElement>("[data-queue-relayed-group]")!;
  const summary = () => host.querySelector<HTMLElement>("[data-queue-relayed-summary]")!;
  const ownList = () => host.querySelector('[role="list"].px-1.pt-1\\.5');
  const toggle = (row: number) => within(row, "[data-queue-relayed-toggle]");
  const body = (row: number) => within(row, "[data-queue-row-body]");

  const proto = HTMLElement.prototype;
  const saved = { sw: Object.getOwnPropertyDescriptor(proto, "scrollWidth"), cw: Object.getOwnPropertyDescriptor(proto, "clientWidth") };
  const cutLines = (cut: boolean) => {
    Object.defineProperty(proto, "scrollWidth", { configurable: true, get: () => (cut ? 300 : 100) });
    Object.defineProperty(proto, "clientWidth", { configurable: true, get: () => 100 });
  };
  afterEach(() => {
    for (const [k, d] of [["scrollWidth", saved.sw], ["clientWidth", saved.cw]] as const) {
      if (d) Object.defineProperty(proto, k, d);
      else delete (proto as unknown as Record<string, unknown>)[k];
    }
  });

  it("renders nothing without a queue", () => {
    render(<QueuePanel session={{ ...session, queued: [] }} draftKey="qp-rg" canEdit />);
    expect(host.innerHTML).toBe("");
  });

  it("draws no relayed group for one own message", () => {
    render(<QueuePanel session={{ ...session, queued: ["只有一条"] }} draftKey="qp-rg" canEdit />);
    expect(rows()).toHaveLength(1);
    expect(host.querySelector("[data-queue-relayed-group]")).toBeNull();
    expect(host.querySelector("[data-queue-relayed-summary]")).toBeNull();
  });

  it("draws neither an own list nor a hairline when only relayed messages are queued", () => {
    show(["来自「主对话」：帮我看下日程", "来自「日报」：汇总"], 2);
    expect(ownList()).toBeNull();
    expect(host.querySelectorAll('[role="list"]')).toHaveLength(0);
    expect(group().className).not.toContain("border-t");
    expect(summary().textContent).toBe("2 条消息来自其他对话 · 对话空闲后送达");
    expect(summary().querySelector("svg")).not.toBeNull();
    click(summary());
    expect(rows()).toHaveLength(2);
    expect(rows().map((r) => r.querySelector("[data-queue-from]")!.textContent)).toEqual(["主对话", "日报"]);
    expect(texts()).toEqual(["帮我看下日程", "汇总"]);
    expect(host.textContent).not.toContain("来自「主对话」：");
    expect(rows()[0].getAttribute("aria-label")).toBe("第 1 条排队消息：来自「主对话」 · 帮我看下日程 · 对话空闲后送达");
    expect(rows()[0].querySelector("[data-queue-note]")).toBeNull();
  });

  it("shows no-wake relays arriving with the next message in an idle chat", () => {
    const idle = { ...persona(["来自「日报」（不唤醒）：备忘"], 1), state: "idle" } as Session;
    render(<QueuePanel session={idle} draftKey="qp-rg" canEdit persona="Nova" />);
    expect(summary().textContent).toBe("1 条消息来自其他对话 · 随下一轮消息送达");
    expect(rows()).toHaveLength(0);
    click(summary());
    expect(summary().textContent).toBe("1 条消息来自其他对话 · 随下一轮消息送达");
    expect(within(0, "[data-queue-from]").textContent).toBe("日报");
    expect(texts()).toEqual(["备忘"]);
    expect(rows()[0].getAttribute("aria-label")).toBe("第 1 条排队消息：来自「日报」 · 备忘 · 随下一轮消息送达");
    expect(rows()[0].querySelector("button")).toBeNull();
    expect(host.textContent).not.toContain("对话空闲后送达");
  });

  it("shows per-row delivery notes for a mixed relay group", () => {
    show(["来自「日报」（不唤醒）：备忘", "来自「主对话」：帮我看下日程", "督办消息"], 3);
    expect(summary().textContent).toBe("3 条消息来自其他对话");
    click(summary());
    expect(texts()).toEqual(["备忘", "帮我看下日程", "督办消息"]);
    expect(rows().map((row) => row.querySelector("[data-queue-note]")?.textContent)).toEqual([
      "随下一轮消息送达", "对话空闲后送达", "对话空闲后送达",
    ]);
    expect(rows().map((row) => row.getAttribute("aria-label"))).toEqual([
      "第 1 条排队消息：来自「日报」 · 备忘 · 随下一轮消息送达",
      "第 2 条排队消息：来自「主对话」 · 帮我看下日程 · 对话空闲后送达",
      "第 3 条排队消息：其他对话 · 督办消息 · 对话空闲后送达",
    ]);
  });

  it("keeps the own rows' actions and a hairline above the relayed group with several of each", () => {
    show(["我的一", "我的二", "来自「主对话」：甲", "来自「日报」：乙"], 2);
    expect(ownList()).not.toBeNull();
    expect(group().className).toContain("border-t border-line");
    expect(summary().textContent).toContain("2 条消息来自其他对话");
    click(summary());
    expect(rows()).toHaveLength(4);
    for (const i of [0, 1]) {
      expect(within(i, "[data-queue-edit]")).not.toBeNull();
      expect(within(i, "[data-queue-delete]")).not.toBeNull();
    }
    for (const i of [2, 3]) {
      expect(rows()[i].querySelector("button")).toBeNull();
      expect(rows()[i].querySelector("[data-queue-relayed-toggle]")).toBeNull();
    }
  });

  it("shows a relayed line that is not a conversation_send as its raw text, with no from label", () => {
    show(["日报的通知", "督办消息"], 2);
    click(summary());
    expect(host.querySelector("[data-queue-from]")).toBeNull();
    expect(texts()).toEqual(["日报的通知", "督办消息"]);
    expect(rows()[0].getAttribute("aria-label")).toBe("第 1 条排队消息：其他对话 · 日报的通知 · 对话空闲后送达");
    expect(rows()[0].querySelector("[data-queue-note]")).toBeNull();
  });

  it("has no send icon on a relayed row", () => {
    show(["来自「主对话」：甲"], 1);
    click(summary());
    expect(rows()[0].querySelector("svg")).toBeNull();
  });

  it("opens a cut relayed line in place with its toggle, and closes it again", () => {
    cutLines(true);
    show(["来自「主对话」：一段很长很长的话"], 1);
    click(summary());
    expect(toggle(0).getAttribute("aria-expanded")).toBe("false");
    expect(toggle(0).getAttribute("aria-label")).toBe("展开全文");
    expect(body(0).className).toContain("truncate");
    click(toggle(0));
    expect(toggle(0).getAttribute("aria-expanded")).toBe("true");
    expect(toggle(0).getAttribute("aria-label")).toBe("收起");
    expect(body(0).className).not.toContain("truncate");
    expect(body(0).className).toContain("whitespace-pre-wrap");
    click(toggle(0));
    expect(toggle(0).getAttribute("aria-expanded")).toBe("false");
    expect(body(0).className).toContain("truncate");
  });

  it("toggles a cut relayed line by a click on the row, unless text is selected", () => {
    cutLines(true);
    show(["来自「主对话」：一段很长很长的话"], 1);
    click(summary());
    click(body(0));
    expect(toggle(0).getAttribute("aria-expanded")).toBe("true");
    click(body(0));
    expect(toggle(0).getAttribute("aria-expanded")).toBe("false");
    const sel = vi.spyOn(window, "getSelection").mockReturnValue({ toString: () => "很长" } as unknown as Selection);
    click(body(0));
    sel.mockRestore();
    expect(toggle(0).getAttribute("aria-expanded")).toBe("false");
  });

  it("measures again on closing, so a line opened while cut and widened to fit keeps no toggle", () => {
    cutLines(true);
    show(["来自「主对话」：一段很长很长的话"], 1);
    click(summary());
    click(toggle(0));
    expect(toggle(0).getAttribute("aria-expanded")).toBe("true");
    cutLines(false); // the composer widened while the line was open
    expect(toggle(0).getAttribute("aria-expanded")).toBe("true");
    click(toggle(0));
    expect(host.querySelector("[data-queue-relayed-toggle]")).toBeNull();
    expect(body(0).className).toContain("truncate");
    click(body(0));
    expect(body(0).className).toContain("truncate");
  });

  it("offers no toggle when the relayed line fits", () => {
    cutLines(false);
    show(["来自「主对话」：短"], 1);
    click(summary());
    expect(host.querySelector("[data-queue-relayed-toggle]")).toBeNull();
    click(body(0));
    expect(body(0).className).toContain("truncate");
  });
});

describe("QueuePanel status, always-visible Send now and photo thumbnails (#10871)", () => {
  const photo = (name: string) => `/Users/me/.persona/uploads/2026-11-20/${name}.png`;
  const withPhotos = (...names: string[]) => `[Image #1]\n<attached_images>\n${names.map((n) => `path: ${photo(n)}`).join("\n")}\n</attached_images>`;
  const errorImg = (img: HTMLImageElement) => act(() => void img.dispatchEvent(new Event("error")));

  it("shows the queued status header in full layout and keeps Send now visible without hover on the first row", () => {
    render(<QueuePanel session={session} draftKey="qp-10871-status" canEdit />);
    const header = host.querySelector<HTMLElement>("[data-queue-header]");
    expect(header).not.toBeNull();
    expect(header!.textContent).toContain("已排队，尚未发送");

    const firstRow = rows()[0];
    const firstActions = firstRow.querySelector<HTMLElement>("[data-queue-actions]")!;
    const firstSendNow = firstRow.querySelector<HTMLButtonElement>("[data-send-now]")!;
    expect(firstActions.className).not.toContain("opacity-0");
    expect(firstSendNow.className).not.toContain("opacity-0");
    expect(firstSendNow.textContent).toContain("立即发送");
    expect(firstSendNow.title).toContain("立即将所有排队消息发送给 Claude");

    const secondActions = rows()[1].querySelector<HTMLElement>("[data-queue-actions]")!;
    expect(secondActions.className).toContain("opacity-0");
  });

  it("omits the full-layout status header in compact mode", () => {
    render(<QueuePanel session={session} draftKey="qp-10871-compact" canEdit compact />);
    expect(host.querySelector("[data-queue-header]")).toBeNull();
  });

  it("renders thumbnails for server-saved photo paths and falls back from thumb to inline download to icon", () => {
    const raw = withPhotos("shot1");
    render(<QueuePanel session={{ ...session, queued: [raw] }} draftKey="qp-10871-thumb" canEdit />);
    const img = rows()[0].querySelector<HTMLImageElement>("[data-queue-photo-thumb]")!;
    expect(img).not.toBeNull();
    expect(img.src).toContain("/file/thumb?");
    expect(img.src).toContain(encodeURIComponent(photo("shot1")));

    // first error: fall back to inline file download
    errorImg(img);
    const inlineImg = rows()[0].querySelector<HTMLImageElement>("[data-queue-photo-thumb]")!;
    expect(inlineImg.src).toContain("/file/download?");
    expect(inlineImg.src).toContain("inline=1");

    // second error: fall back gracefully to photo icon
    errorImg(inlineImg);
    expect(rows()[0].querySelector("[data-queue-photo-thumb]")).toBeNull();
    expect(rows()[0].querySelector("[data-queue-photo-fallback] svg")).not.toBeNull();
  });

  it("uses local preview data URLs when freshly queued and collapses more than three photos into a +N badge", () => {
    const dataUrl = "data:image/png;base64,iVBORw0KGgo=";
    const fivePhotos = withPhotos("p1", "p2", "p3", "p4", "p5");
    render(
      <QueuePanel
        session={{ ...session, queued: [fivePhotos] }}
        draftKey="qp-10871-multi"
        canEdit
        pending={[{ id: "local", raw: fivePhotos, before: [], images: [{ mime: "image/png", data: "iVBORw0KGgo=" }] }]}
      />,
    );
    const thumbs = [...rows()[0].querySelectorAll<HTMLImageElement>("[data-queue-photo-thumb]")];
    expect(thumbs).toHaveLength(3);
    expect(thumbs[0].getAttribute("src")).toBe(dataUrl);
    expect(thumbs[1].getAttribute("src")).toContain(encodeURIComponent(photo("p2")));
    expect(thumbs[2].getAttribute("src")).toContain(encodeURIComponent(photo("p3")));
    const overflow = rows()[0].querySelector<HTMLElement>("[data-queue-photo-overflow]");
    expect(overflow?.textContent).toBe("+2");
  });
});

describe("QueuePanel pull-back lifecycle and cross-session isolation", () => {
  it("still restores text and images to the originating draft when unmounting after the last queued item empties before HTTP resolves", async () => {
    let resolvePull!: (v: unknown) => void;
    vi.mocked(api).mockReturnValue(new Promise((r) => { resolvePull = r; }));
    const draftEvents: DraftEvent[] = [];
    const unsub = onDraftEvent("qp-unmount-last", (e) => draftEvents.push(e));
    try {
      const single = { ...session, queued: ["最后一条排队消息"] } as unknown as Session;
      render(<QueuePanel session={single} draftKey="qp-unmount-last" canEdit />);
      click(within(0, "[data-queue-edit]"));
      expect(calls()).toEqual([["/sessions/m/s/queue", { keep: [] }]]);

      // Queue-emptying push unmounts QueuePanel while the composer for qp-unmount-last stays mounted.
      render(null);
      expect(host.innerHTML).toBe("");

      resolvePull({
        queue: ["最后一条排队消息"],
        images: [[{ mime: "image/png", data: "AAAA" }]],
      });
      await settle();

      expect(draftEvents).toEqual([
        { type: "append", text: "最后一条排队消息" },
        { type: "image", image: { name: "photo-1.png", mime: "image/png", data: "AAAA" } },
      ]);
    } finally {
      unsub();
    }
  });

  it("never restores into session B or unlocks session A2 on cross-session / ABA switches while an edit pull-back is in flight", async () => {
    let resolveA1!: (v: unknown) => void;
    let resolveA2!: (v: unknown) => void;
    vi.mocked(api)
      .mockReturnValueOnce(new Promise((r) => { resolveA1 = r; }))
      .mockReturnValueOnce(new Promise((r) => { resolveA2 = r; }));

    const eventsA: DraftEvent[] = [];
    const eventsB: DraftEvent[] = [];
    const unsubA = onDraftEvent("qp-aba-a", (e) => eventsA.push(e));
    const unsubB = onDraftEvent("qp-aba-b", (e) => eventsB.push(e));
    try {
      const sessionA = { ...session, machine_id: "m-a", session_id: "s-a", queued: ["A 第一条", "A 第二条"] } as unknown as Session;
      const sessionB = { ...session, machine_id: "m-b", session_id: "s-b", queued: ["B 第一条"] } as unknown as Session;

      // Start pull-back in session A (request #1 stays pending).
      render(<QueuePanel session={sessionA} draftKey="qp-aba-a" canEdit />);
      click(within(0, "[data-queue-edit]"));
      expect(within(1, "[data-queue-delete]").hasAttribute("disabled")).toBe(true);

      // Switch to session B while A1 is in flight: B is interactive and unlocked.
      render(<QueuePanel session={sessionB} draftKey="qp-aba-b" canEdit />);
      expect(within(0, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);

      // ABA: return to session A and start delete #2 on row 1.
      render(<QueuePanel session={sessionA} draftKey="qp-aba-a" canEdit />);
      expect(within(1, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);
      click(within(1, "[data-queue-delete]"));
      expect(within(0, "[data-queue-edit]").hasAttribute("disabled")).toBe(true);

      // Stale A1 edit resolves late: restores strictly to A's draft (never B) and does NOT unlock A2.
      resolveA1({
        queue: ["A 第一条", "A 第二条"],
        images: [[{ mime: "image/png", data: "BBBB" }]],
      });
      await settle();

      expect(eventsB).toEqual([]);
      expect(eventsA).toEqual([
        { type: "append", text: "A 第一条" },
        { type: "image", image: { name: "photo-1.png", mime: "image/png", data: "BBBB" } },
      ]);
      expect(within(0, "[data-queue-edit]").hasAttribute("disabled")).toBe(true);

      // Active A2 delete resolves: row 1 remains held out until the pushed queue arrives, then unlocks cleanly.
      resolveA2({ queue: ["A 第一条", "A 第二条"] });
      await settle();
      expect(texts()).toEqual(["A 第一条"]);
      render(<QueuePanel session={{ ...sessionA, queued: ["A 第一条"] }} draftKey="qp-aba-a" canEdit />);
      expect(within(0, "[data-queue-edit]").hasAttribute("disabled")).toBe(false);
    } finally {
      unsubA();
      unsubB();
    }
  });
});
