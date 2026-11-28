// @vitest-environment jsdom
// A routine's `brief` item (Brief.tsx): the fixed part of a morning or evening run, drawn in the conversation, with an
// Accept button per task to verify that posts the round it was made for.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/api";
import { briefBody, askRow } from "../../lib/brief-rows";
import { type BriefRow, type FeedItem } from "../../lib/feed";
import type { Todo, TodoAsk } from "../../lib/todos";

const api = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api }));
const toast = vi.hoisted(() => vi.fn());
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast }));
import { FeedLineView, NeedsYouBody } from "./Tasks";
import { AcceptRow, meta } from "./Brief";
import { getState, reset } from "../../lib/store";
import { loadPages } from "../../lib/lazy-page";
import { setAgentOpenMode } from "../../lib/agent-open";
import { handoffFrom } from "../../lib/handoff";
import { closePanel, getPanel } from "../../lib/panel";
import { t } from "../../i18n";

// the week's setup block is its own chunk (BriefSetup.tsx): in before any test draws it
await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  api.mockReset();
  toast.mockReset();
  api.mockResolvedValue({});
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const ASKED = "2026-11-06T01:02:03.123456789Z";
const body = (over: Record<string, unknown> = {}): Record<string, unknown> => ({
  title: "早报",
  at: "2026-11-07T01:00:00Z",
  hours: 12,
  waiting: [{ id: "ix1", title: "先发预发还是直接上线？", from: "来自 Agent「登录修复」", link: "https://hub.example/s/box/ag1", waited_s: 4800, nudged: true }],
  failed: [{ kind: "task", id: "todo_f", title: "修登录崩溃", at: "2026-11-07T00:10:00Z", why: "machine offline" }, { kind: "routine", id: "patrol", title: "巡检", at: "2026-11-07T00:20:00Z", skipped: true }],
  failed_total: 2,
  done: [{ id: "todo_d", title: "整理周报", at: "2026-11-06T22:00:00Z" }],
  done_total: 4,
  accept: [{ id: "todo_v1", title: "改设置页", detail: "设置页多了一个开关", from: "来自某个 Agent", waited_s: 7200, asked_at: ASKED }],
  ...over,
});
const askedLater = (n = 1): Record<string, unknown>[] => [
  { id: `lt_${n}`, title: "新接口用 snake_case 还是 camelCase？", from: "来自 Agent「网络层」", link: "https://hub.example/s/box/ag7", waited_s: 93600 },
  { id: `lt_${n + 1}`, title: "图标要不要换？", waited_s: 3600 },
];
const item = (b: Record<string, unknown> = body()): FeedItem => ({ id: "f1", conversation: "c1", at: "2026-11-07T01:00:05Z", kind: "brief", ref: "morning", body: b });

function draw(it: FeedItem) {
  act(() => root.render(<FeedLineView item={it} />));
}
function drawDecision(row: BriefRow) {
  act(() => root.render(<AcceptRow row={row} stacked />));
}
const rows = (kind: string) => [...host.querySelectorAll(`[data-brief-row=${kind}]`)];

describe("a brief item", () => {
  it("shows the provider's facts and report as text and skips unreadable contributions", () => {
    const provider = { extension_id: "example", contribution_id: "library", title: "Library" };
    draw(item(body({ contributions: [
      { ...provider, facts: ["Two books due", null], sections: [
        { title: "Due today", body: "<b>Field guide</b>\nReturn before noon" }, { title: "Incomplete" }, null,
      ] },
      { ...provider, contribution_id: "empty", title: "Empty" }, null, { facts: ["Unknown provider"] },
    ] })));
    const section = [...host.querySelectorAll("section")].find((s) => s.textContent?.includes("Library"));
    expect(section?.textContent).toContain("Two books due");
    expect(section?.textContent).toContain("Due today");
    expect(section?.textContent).toContain("<b>Field guide</b>");
    expect(section?.querySelector("b")).toBeNull();
    expect(host.textContent).not.toMatch(/Incomplete|Empty|Unknown provider/);
    draw(item());
    expect(host.textContent).not.toContain("Library");
  });

  it("draws the sections: what waits, the last hours (failed first), what is to be verified", () => {
    draw(item());
    const text = host.textContent ?? "";
    for (const want of ["早报", "待你处理（1）", "先发预发还是直接上线？", "已等待 1 小时 20 分", "已催促", "过去 12 小时：已完成 4 · 未成功 2", "修登录崩溃", "未能启动", "machine offline", "巡检", "已跳过", "整理周报", "待验收（1）", "改设置页", "设置页多了一个开关"]) {
      expect(text, want).toContain(want);
    }
    expect(text.indexOf("修登录崩溃")).toBeLessThan(text.indexOf("整理周报"));
    expect(host.querySelector<HTMLAnchorElement>("a[href='https://hub.example/s/box/ag1']")).not.toBeNull();
    expect(rows("accept")).toHaveLength(1);
    expect(text).toContain("还有 3 项"); // 4 done + 2 failed in all, 3 listed
  });

  it("draws a link only when it is a web address", () => {
    draw(item(body({
      waiting: [
        { id: "ix1", title: "一个问题", waited_s: 60, link: "javascript:alert(1)" },
        { id: "ix2", title: "另一个", waited_s: 60, link: "https://hub.example/s/box/ag2" },
        { id: "ix3", title: "带凭据", waited_s: 60, link: "https://user:pass@hub.example/s/box/ag3" },
        { id: "ix4", title: "反斜杠", waited_s: 60, link: "https://evil.example\\@hub.example/s/box/ag4" },
      ],
    })));
    expect([...host.querySelectorAll("a")].map((a) => a.getAttribute("href"))).toEqual(["https://hub.example/s/box/ag2"]);
  });

  it("draws what is in progress last, with its stage bar and how many more there are", () => {
    draw(item(body({ progress: [{ id: "todo_w1", title: "重构设置页", stage: "develop" }, { id: "todo_w2", title: "整理文档" }], progress_total: 3 })));
    const text = host.textContent ?? "";
    expect(text).toContain("进行中（3）");
    expect(text.indexOf("待验收（1）")).toBeLessThan(text.indexOf("进行中（3）"));
    expect(rows("progress")).toHaveLength(2);
    expect(rows("progress")[0].querySelector("ol[aria-label]")).not.toBeNull(); // the stage bar
    expect(rows("progress")[1].querySelector("ol[aria-label]")).toBeNull(); // no stage known: no bar
    expect(text).toContain("还有 1 项");
    draw(item()); // nothing running: no section
    expect(host.textContent).not.toContain("进行中");
  });

  it("says so when nothing is there, and does not say that nothing waits when the cards were not read", () => {
    draw(item(body({ waiting: [], failed: [], failed_total: 0, done: [], done_total: 0, accept: [] })));
    expect(host.textContent).toContain("待你处理（0）");
    expect(host.querySelectorAll("[data-brief-section]")).toHaveLength(3);
    expect(host.textContent).not.toContain("还有");
    draw(item(body({ waiting: [], waiting_unknown: true })));
    expect(host.textContent).toContain("暂未读取待你处理的卡片");
    expect(host.textContent).not.toContain("待你处理（0）");
  });

  it("draws the questions that can wait after what is to be verified and before what is in progress, with how many more", () => {
    draw(item(body({ later: askedLater(), later_total: 3, progress: [{ id: "todo_w1", title: "重构设置页" }], progress_total: 1 })));
    const text = host.textContent ?? "";
    for (const want of ["可稍后处理（3）", "新接口用 snake_case 还是 camelCase？", "已等待 26 小时", "来自 Agent「网络层」", "图标要不要换？", "还有 1 项"]) {
      expect(text, want).toContain(want);
    }
    expect(text.indexOf("待验收（1）")).toBeLessThan(text.indexOf("可稍后处理（3）"));
    expect(text.indexOf("可稍后处理（3）")).toBeLessThan(text.indexOf("进行中（1）"));
    expect(text).not.toContain("还有 1 项，前往任务页查看"); // the queue's rest is not on the Tasks page
    expect(rows("later")).toHaveLength(2);
    expect(host.querySelector<HTMLAnchorElement>("a[href='https://hub.example/s/box/ag7']")).not.toBeNull(); // the Agent's chat
  });

  describe("Open on a waiting or later row that names its chat (#12055)", () => {
    const open = (title: string) => host.querySelector<HTMLButtonElement>(`button[title="${t("assistant-brief.open_row", { title })}"]`)!;
    const waiting = { id: "ix1", title: "先发预发还是直接上线？", link: "https://hub.example/s/box/ag1", waited_s: 60, machine_id: "box", session_id: "ag1" };
    const later = { id: "lt_1", title: "图标要不要换？", link: "https://hub.example/s/box/ag7", waited_s: 60, machine_id: "box", session_id: "ag7" };
    afterEach(() => {
      setAgentOpenMode("panel");
      closePanel();
      reset();
      history.replaceState(null, "", "/");
    });

    it("opens the Agent in the app, not the hub's link in a new tab: the side panel by default", () => {
      history.replaceState(null, "", "/persona/c1");
      draw(item(body({ waiting: [waiting], later: [later], later_total: 1 })));
      expect(host.querySelector("a[href^='https://hub.example/']")).toBeNull();
      act(() => open(waiting.title).click());
      expect(getPanel()).toEqual({ kind: "agent", mid: "box", sid: "ag1" });
      act(() => open(later.title).click());
      expect(getPanel()).toEqual({ kind: "agent", mid: "box", sid: "ag7" });
      expect(location.pathname).toBe("/persona/c1");
    });

    it("set to Full page, opens the Agent's page with the way back to this conversation, as the task rows do", () => {
      setAgentOpenMode("page");
      history.replaceState(null, "", "/persona/c1");
      draw(item(body({ waiting: [waiting] })));
      act(() => open(waiting.title).click());
      expect(getPanel()).toBeNull();
      expect(location.pathname).toBe("/s/box/ag1");
      expect(handoffFrom()).toEqual({ path: "/persona/c1" });
    });

    it("opens a card waiting in a Persona conversation as that conversation's page", () => {
      Object.assign(getState(), { assistant: { ...getState().assistant!, machine_id: "box", main: "main-1", conversations: [{ machine_id: "box", session_id: "c9", title: "Trip", cwd: "", status: "idle" }] } });
      history.replaceState(null, "", "/persona/c1");
      draw(item(body({ waiting: [{ ...waiting, session_id: "c9", link: "https://hub.example/persona/c9" }] })));
      act(() => open(waiting.title).click());
      expect(getPanel()).toBeNull();
      expect(location.pathname).toBe("/persona/c9");
    });
  });

  it("has no such section without questions, as for a hub that does not know them", () => {
    draw(item());
    expect(host.textContent).not.toContain("可稍后处理");
    expect(rows("later")).toHaveLength(0);
    draw(item(body({ later: "x", later_total: 5 }))); // not a list: nothing to draw
    expect(rows("later")).toHaveLength(0);
    expect(host.textContent).not.toContain("可稍后处理");
  });

  it("Handled posts the question's id, then says it is marked", async () => {
    draw(item(body({ later: askedLater(), later_total: 2 })));
    const btn = [...host.querySelectorAll("button")].filter((b) => b.textContent === "已处理")[0];
    await act(async () => btn.click());
    expect(api).toHaveBeenCalledWith("/later/lt_1/handled", { method: "POST" });
    expect(host.querySelectorAll("[data-brief-marked=marked]")).toHaveLength(1);
    expect(host.querySelector("[data-brief-marked=marked]")?.textContent).toBe("已标记为已处理");
    expect([...host.querySelectorAll("button")].filter((b) => b.textContent === "已处理")).toHaveLength(1); // the other one is still there
  });

  it("a question that is gone (409) says it was handled; another failure keeps the button for a retry", async () => {
    draw(item(body({ later: askedLater(), later_total: 2 })));
    api.mockRejectedValueOnce(new ApiError(409, "已经处理过了", {}));
    await act(async () => [...host.querySelectorAll("button")].filter((b) => b.textContent === "已处理")[0].click());
    expect(host.querySelector("[data-brief-marked=handled]")?.textContent).toBe("已处理过");

    draw(item(body({ later: askedLater(3), later_total: 2 }))); // other ids: a new row, not the one that was marked
    api.mockRejectedValueOnce(new ApiError(500, "boom", {}));
    await act(async () => [...host.querySelectorAll("button")].filter((b) => b.textContent === "已处理")[0].click());
    expect(host.querySelector("[data-brief-marked]")).toBeNull();
    expect([...host.querySelectorAll("button")].filter((b) => b.textContent === "已处理" && !b.disabled)).toHaveLength(2);
  });

  it("draws nothing for a body that is not one", () => {
    draw({ ...item(), body: undefined });
    expect(host.querySelector("[data-testid=brief-card]")).toBeNull();
    draw(item(body({ waiting: null, failed: "x", done: undefined, accept: 7 })));
    expect(host.querySelector("[data-testid=brief-card]")).not.toBeNull(); // lists it does not understand are empty ones
  });

  it("Accept posts the round it was made for, then shows the task as accepted", async () => {
    draw(item());
    const btn = [...host.querySelectorAll("button")].find((b) => b.textContent === "通过")!;
    await act(async () => btn.click());
    expect(api).toHaveBeenCalledWith("/todos/todo_v1/accept", { method: "POST", json: { asked_at: ASKED } });
    expect(host.querySelector("[data-brief-accepted=accepted]")?.textContent).toBe("已验收");
    expect([...host.querySelectorAll("button")].some((b) => b.textContent === "通过")).toBe(false);
  });

  it("says when the decision is recorded for the supervisor because no Agent can take it", async () => {
    api.mockResolvedValueOnce({ ok: true, delivered: "none" });
    drawDecision({ id: "todo_stopped", title: "Demo rollout", outcome: "decide", recommend: "Stage it", waited_s: 60, asked_at: ASKED });
    const take = [...rows("accept")[0].querySelectorAll("button")].find((b) => b.textContent === "采用推荐")!;
    await act(async () => take.click());
    expect(host.querySelector("[data-brief-accepted=recorded]")?.textContent).toBe("已记录。没能送到 Agent，将由督办接手转交");
  });

  it("a round that is over (409) says it was handled; another failure keeps the button for a retry", async () => {
    draw(item());
    api.mockRejectedValueOnce(new ApiError(409, "已经处理过了", {}));
    await act(async () => [...host.querySelectorAll("button")].find((b) => b.textContent === "通过")!.click());
    expect(host.querySelector("[data-brief-accepted=handled]")?.textContent).toBe("已处理过");

    draw(item(body({ accept: [{ id: "todo_v2", title: "另一件", waited_s: 60, asked_at: ASKED }] })));
    api.mockRejectedValueOnce(new ApiError(500, "boom", {}));
    await act(async () => [...host.querySelectorAll("button")].find((b) => b.textContent === "通过")!.click());
    expect(host.querySelector("[data-brief-accepted]")).toBeNull();
    expect([...host.querySelectorAll("button")].some((b) => b.textContent === "通过" && !b.disabled)).toBe(true);
  });

  it("draws each row's verdict as the hub wrote it; a conclusion's button says Got it, a decision's takes the recommendation", async () => {
    const accept = [
      { id: "todo_v1", title: "改设置页", detail: "设置页多了一个开关", verdict: "✅ 做完了：已部署，按下面验收", waited_s: 60, asked_at: ASKED },
      { id: "todo_c1", title: "调研直连", verdict: "⛔ 做不了：对方接口不对外开放", outcome: "cannot", waited_s: 60, asked_at: ASKED },
      { id: "todo_d1", title: "上线方式", verdict: "🤔 要你拍板：先灰度还是直接全量", outcome: "decide", recommend: "先灰度 10%", waited_s: 60, asked_at: ASKED },
      { id: "todo_d2", title: "没推荐的", verdict: "🤔 要你拍板：用哪个库", outcome: "decide", waited_s: 60, asked_at: ASKED },
    ];
    draw(item(body({ accept })));
    const [deployed, cannot, decide, none] = rows("accept");
    expect(deployed.querySelector("[data-brief-verdict]")?.textContent).toBe("✅ 做完了：已部署，按下面验收");
    expect(deployed.textContent).toContain("设置页多了一个开关");
    expect([...deployed.querySelectorAll("button")].map((b) => b.textContent)).toContain("通过");
    expect(cannot.querySelector("[data-brief-verdict=cannot]")?.textContent).toBe("⛔ 做不了：对方接口不对外开放");
    expect([...cannot.querySelectorAll("button")].map((b) => b.textContent)).toContain("知道了");
    expect(decide.textContent).toContain("推荐：先灰度 10%");
    const take = [...decide.querySelectorAll("button")].find((b) => b.textContent === "采用推荐")!;
    await act(async () => take.click());
    expect(api).toHaveBeenCalledWith("/todos/todo_d1/accept", { method: "POST", json: { asked_at: ASKED } });
    expect(decide.querySelector("[data-brief-accepted=accepted]")?.textContent).toBe("已采用推荐方案");
    expect(none.textContent).toContain("用哪个库");
    expect([...none.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["打开"]); // a historical snapshot cannot finish a newer run
    expect(host.textContent).not.toContain("直接完成");
  });

  it("preserves non-deployed verification verdicts and Pass action on brief accept rows", async () => {
    const accept = [
      { id: "todo_m1", title: "合入待部署", detail: "PR 已合并", verdict: "✅ 做完了：已合未部署，按下面验收", waited_s: 60, asked_at: ASKED },
      { id: "todo_n1", title: "本地文档更新", detail: "补齐说明", verdict: "✅ 做完了：请验收结果", waited_s: 60, asked_at: ASKED },
    ];
    draw(item(body({ accept })));
    const [merged, neutral] = rows("accept");
    expect(merged.querySelector("[data-brief-verdict]")?.textContent).toBe("✅ 做完了：已合未部署，按下面验收");
    expect(merged.textContent).not.toContain("已部署，按下面验收");
    expect(neutral.querySelector("[data-brief-verdict]")?.textContent).toBe("✅ 做完了：请验收结果");
    const pass = [...merged.querySelectorAll("button")].find((b) => b.textContent === "通过")!;
    await act(async () => pass.click());
    expect(api).toHaveBeenCalledWith("/todos/todo_m1/accept", { method: "POST", json: { asked_at: ASKED } });
  });

  it("offers direct completion in live Pending even without a recommendation", () => {
    drawDecision({ id: "todo_no_recommendation", title: "Synthetic decision", outcome: "decide", waited_s: 60, asked_at: ASKED });
    expect([...rows("accept")[0].querySelectorAll("button")].map((b) => b.textContent)).toEqual(["打开", "直接完成"]);
  });

  it("finishes a decision using PATCH only, then clears its ask and shows the completion receipt", async () => {
    const ask = { outcome: "decide" as const, verdict: "Choose the rollout", recommend: "Use a staged rollout", asked_at: ASKED };
    const todo = { id: "todo_finish", title: "Demo rollout", created_at: ASKED, updated_at: ASKED, ask };
    const before = getState().todos;
    getState().todos = [todo];
    const finished = { ...todo, done_at: ASKED, ask: undefined };
    // JSON omits cleared optional fields, as the hub does.
    api.mockImplementation(async (path, opts) => opts?.method === "PATCH" ? { todo: JSON.parse(JSON.stringify(finished)) } : path === "/todos" ? { todos: [finished] } : {});
    try {
      drawDecision({ ...todo, ...ask, waited_s: 60 });
      const buttons = [...rows("accept")[0].querySelectorAll("button")];
      expect(buttons.map((b) => b.textContent)).toEqual(["打开", "直接完成", "采用推荐"]);
      expect(buttons[1].classList).toContain("shadow-btn");
      expect(buttons[2].classList).toContain("bg-ink");
      await act(async () => buttons[1].click());
      expect(api.mock.calls.filter(([, opts]) => opts?.method && opts.method !== "GET")).toEqual([["/todos/todo_finish", { method: "PATCH", json: { done: true } }]]);
      expect(getState().todos[0].done_at).toBe(ASKED);
      expect(getState().todos[0].ask).toBeUndefined();
      expect(host.querySelector("[data-brief-accepted=finished]")?.textContent).toBe("已完成，不再交由 Agent 处理");
      expect(toast).toHaveBeenCalledWith("已完成，不再交由 Agent 处理", "ok");
      expect([...rows("accept")[0].querySelectorAll("button")].map((b) => b.textContent)).toEqual(["打开"]);
    } finally {
      getState().todos = before;
    }
  });

  it("keeps both decision actions disabled during completion, rejects a mixed double-click, and lets a failure retry", async () => {
    drawDecision({ id: "todo_finish", title: "Demo rollout", outcome: "decide", recommend: "Stage it", waited_s: 60, asked_at: ASKED });
    let reject!: (e: unknown) => void;
    api.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; }));
    const buttons = [...rows("accept")[0].querySelectorAll("button")];
    act(() => { buttons[1].click(); buttons[2].click(); buttons[1].click(); });
    expect(api).toHaveBeenCalledTimes(1);
    expect(buttons[1].disabled).toBe(true);
    expect(buttons[2].disabled).toBe(true);
    await act(async () => reject(new ApiError(500, "Synthetic save failure", {})));
    expect(host.querySelector("[data-brief-accepted]")).toBeNull();
    expect(buttons[1].disabled).toBe(false);
    expect(buttons[2].disabled).toBe(false);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic save failure"), "error");
    api.mockRejectedValueOnce(new ApiError(500, "Synthetic retry failure", {}));
    await act(async () => buttons[1].click());
    expect(api).toHaveBeenCalledTimes(2);
  });

  it("does not finish while recommendation delivery is in flight", async () => {
    drawDecision({ id: "todo_finish", title: "Demo rollout", outcome: "decide", recommend: "Stage it", waited_s: 60, asked_at: ASKED });
    api.mockImplementationOnce(() => new Promise(() => {}));
    const buttons = [...rows("accept")[0].querySelectorAll("button")];
    act(() => { buttons[2].click(); buttons[1].click(); });
    expect(api.mock.calls).toEqual([["/todos/todo_finish/accept", { method: "POST", json: { asked_at: ASKED } }]]);
    expect(buttons[1].disabled).toBe(true);
  });

  it("has no Accept button for a task that has no round to name", () => {
    draw(item(body({ accept: [{ id: "todo_v3", title: "没有轮次", waited_s: 60 }] })));
    expect([...host.querySelectorAll("button")].find((b) => b.textContent === "通过")?.disabled).toBe(true);
  });

  it("draws the week's recommended setup as the hub worded it, and only opens settings", () => {
    draw(item(body({ setup: [
      { id: "triage_wake", setting: "agents.triage.wake", count: 3, text: "Session triage, \"Wake on schedule\": 3 times …" },
      { id: "leaders", setting: "assistant.leaders", count: 12, minutes: 31, text: "Leader conversations: 12 of the main chat's rounds (31 min) …" },
    ] })));
    const setup = rows("setup");
    expect(setup.map((r) => r.querySelector("div")?.textContent)).toEqual([
      "Session triage, \"Wake on schedule\": 3 times …",
      "Leader conversations: 12 of the main chat's rounds (31 min) …",
    ]);
    // Only a mechanism with a settings page in this app gets a button, and the button calls nothing on the hub.
    expect(setup.map((r) => r.querySelectorAll("button").length)).toEqual([1, 0]);
    act(() => setup[0].querySelector("button")!.click());
    expect(api).not.toHaveBeenCalled();
    expect(location.search).toBe("?tab=persona");
  });

  it("opens the background supervisor's model purpose from the lower-tier line, and changes nothing", () => {
    draw(item(body({ setup: [
      { id: "supervisor_tier", setting: "assistant.supervise", count: 24, text: "A lower tier for the background supervisor: … 24 acceptances on big-model …" },
    ] })));
    const setup = rows("setup");
    expect(setup.map((r) => r.querySelector("div")?.textContent)).toEqual(["A lower tier for the background supervisor: … 24 acceptances on big-model …"]);
    expect(setup[0].querySelectorAll("button").length).toBe(1);
    act(() => setup[0].querySelector("button")!.click());
    expect(api).not.toHaveBeenCalled();
    expect(location.search).toBe("?tab=models&purpose=assistant.supervise");
  });

  it("has no setup section on a briefing without the block", () => {
    draw(item(body()));
    expect(rows("setup")).toEqual([]);
  });
});

// A row's meta line is "waited · nudged · from" and only text goes into it: a to-do's `ask.from` is an object (where the
// task came from), and a hub row's fields are not checked by TypeScript, so a field of another type once read
// "[object Object]" in the Needs you tray.
describe("a row's meta line", () => {
  const ASK: TodoAsk = {
    outcome: "decide", verdict: "🤔 要你拍板：旧接口留不留", recommend: "先合入", asked_at: ASKED,
    from: { conversation: "c1", title: "发版群", main: true, at: ASKED, message: "今天能发吗", by_assistant: false, started_by: "me" },
  };

  it("never carries a to-do's ask.from into its accept row", () => {
    const row = askRow({ id: "todo_1", title: "检查 PR" }, ASK, Date.parse(ASKED) + 12.5 * 3_600_000);
    expect(row.from).toBeUndefined();
    drawDecision(row);
    const meta = host.querySelector("[data-brief-meta]")!.textContent!;
    expect(meta).toBe("已等待 12 小时 30 分");
    expect(host.textContent).not.toContain("[object Object]");
    expect(host.querySelector("[data-brief-recommend]")!.textContent).toContain("先合入");
  });

  it("drops a wire field that is not text before any row draws it", () => {
    const wire = body({
      accept: [{ id: "todo_v1", title: "改设置页", from: { conversation: "c1" }, detail: ["x"], verdict: 3, recommend: {}, waited_s: 60, nudged: {}, asked_at: ASKED }, { title: "no id" }, "nope"],
      waiting: [{ id: "ix1", title: "问题", from: 7, waited_s: "soon" }],
    });
    const got = briefBody(item(wire))!;
    expect(got.accept).toEqual([{ id: "todo_v1", title: "改设置页", waited_s: 60, asked_at: ASKED }]);
    expect(got.waiting).toEqual([{ id: "ix1", title: "问题", waited_s: 0 }]);
    draw(item(wire));
    expect(host.textContent).not.toContain("[object Object]");
  });

  // The tray of a web build from before #3403 drew `{ id, title, waited_s, ...todo.ask }`: the live row read
  // "已等 38 小时 3 分 · [object Object]" (#11794). That row does not compile any more; the tray builds it with askRow.
  it("cannot be handed a to-do's ask spread into a row: the live row's shape is a type error", () => {
    const waited_s = 38 * 3600 + 3 * 60;
    // @ts-expect-error -- ask.from is an object, BriefRow.from is text: spreading the ask into a row does not compile
    const spread: BriefRow = { id: "todo_ce", title: "搭 persona PR watcher", waited_s, ...ASK };
    expect(typeof spread.from).toBe("object");
    const row = askRow({ id: "todo_ce", title: "搭 persona PR watcher" }, ASK, Date.parse(ASKED) + waited_s * 1000);
    expect(meta(row)).toBe("已等待 38 小时 3 分");
    drawDecision(row);
    expect(host.querySelector("[data-brief-meta]")!.textContent).toBe("已等待 38 小时 3 分");
    expect(host.textContent).not.toContain("[object Object]");
  });

  it("keeps the fields a row is declared with", () => {
    const got = briefBody(item(body()))!;
    expect(got.waiting[0]).toEqual({ id: "ix1", title: "先发预发还是直接上线？", from: "来自 Agent「登录修复」", link: "https://hub.example/s/box/ag1", waited_s: 4800, nudged: true });
    // the chat a waiting or later row came from, which Open opens in the app (#12055)
    expect(briefBody(item(body({ waiting: [{ id: "ix2", title: "x", waited_s: 1, machine_id: "box", session_id: "ag1" }] })))!.waiting[0]).toMatchObject({ machine_id: "box", session_id: "ag1" });
    expect(briefBody(item(body({ waiting: [{ id: "ix3", title: "x", waited_s: 1, machine_id: 7, session_id: {} }] })))!.waiting[0]).toEqual({ id: "ix3", title: "x", waited_s: 1 });
  });
});

describe("NeedsYouBody open acceptance ask", () => {
  it("renders the active verification summary and PR in both the live tray and briefing", async () => {
    const summary = "Both routes have step-free access.";
    const link = "https://example.com/travel/routes/pull/7";
    const todo: Todo = {
      id: "trip", title: "Review the route", created_at: ASKED, updated_at: ASKED,
      supervision: { supervisor: "main", state: "user_check", reviews: [] },
      ask: { verdict: "Done: please verify the result", summary, link, asked_at: ASKED },
    };
    await import("./TaskDecision");
    await act(async () => {
      root.render(<NeedsYouBody item={{ kind: "task", key: "task:trip", todo, status: "needs_you", asks: [], countsOpen: true }} />);
    });
    await vi.waitFor(() => expect(host.querySelector("a")?.href).toBe(link));
    expect(host.textContent).toContain(summary);
    expect(host.querySelector("a")?.textContent).toBe("PR #7");
    draw(item(body({ accept: [{ id: todo.id, title: todo.title, detail: summary, evidence_link: link, asked_at: ASKED }] })));
    expect(host.querySelector('[data-brief-row="accept"] a')?.getAttribute("href")).toBe(link);
  });

  it("loads AcceptRow lazily through NeedsYouPart with row attributes and Accept action intact", async () => {
    const todo: Todo = {
      id: "todo_lazy_ask",
      title: "验证懒加载验收卡",
      created_at: ASKED,
      updated_at: ASKED,
      ask: { verdict: "✅ 做完了：请验收", asked_at: ASKED },
    };
    await import("./TaskDecision");
    await act(async () => {
      root.render(<NeedsYouBody item={{ kind: "task", key: "task:todo_lazy_ask", todo, status: "needs_you", asks: [], countsOpen: true }} />);
    });
    await vi.waitFor(async () => {
      await act(async () => { await Promise.resolve(); });
      expect(host.querySelector('[data-brief-row="accept"]')).not.toBeNull();
    });
    const wrap = host.querySelector<HTMLElement>('[data-needs-row="task:todo_lazy_ask"]')!;
    expect(wrap).not.toBeNull();
    expect(wrap.getAttribute("data-counts-open")).toBe("true");
    expect(wrap.textContent).toContain("验证懒加载验收卡");
    expect(wrap.textContent).toContain("✅ 做完了：请验收");
    const pass = [...wrap.querySelectorAll("button")].find((b) => b.textContent === "通过")!;
    await act(async () => pass.click());
    expect(api).toHaveBeenCalledWith("/todos/todo_lazy_ask/accept", { method: "POST", json: { asked_at: ASKED } });
    expect(wrap.querySelector("[data-brief-accepted=accepted]")?.textContent).toBe("已验收");
  });
});
