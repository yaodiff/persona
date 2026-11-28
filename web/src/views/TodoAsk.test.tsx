// @vitest-environment jsdom
// What a to-do waits on from the user (#2056, TodoAsk.tsx): the to-do's row, its page and the main chat's task card show
// the ask the hub put on it (GET /todos `ask`), and one tap answers it with the round it was made for.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { getQuotes, removeQuote, storeDraft, storedPageDraft, withQuoteBlock } from "../lib/drafts";
import { registerComposer } from "../lib/links";
import { ApiError } from "../lib/api";
import { getState } from "../lib/store";
import { boardSectionOf } from "../lib/task-board";
import { askTake, askTone, awaitsReview, openAsk, sortTodos, type Todo, type TodoAsk } from "../lib/todos";
import { getFeedState, setRoute } from "../lib/feed";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const api = vi.hoisted(() => vi.fn());
vi.mock("../lib/api", async (orig) => ({ ...(await orig<typeof import("../lib/api")>()), api }));
const navigate = vi.hoisted(() => vi.fn());
vi.mock("../lib/router", async (orig) => ({ ...(await orig<typeof import("../lib/router")>()), navigate }));
const toast = vi.hoisted(() => vi.fn());
vi.mock("../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../ui/atoms")>()), toast }));

const feed = await import("../lib/feed");
const store = await import("../lib/store");
const { TodoAskLine, TodoAskPanel } = await import("./TodoAsk");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ASKED = "2026-11-09T06:30:00.123456789Z";
const resendAction = expect.objectContaining({ label: expect.any(String), run: expect.any(Function) });
const now = new Date().toISOString();
const decide: TodoAsk = { outcome: "decide", verdict: "🤔 需要你决定：两个方案各有取舍", recommend: "先发预发环境", asked_at: ASKED };
const todo = (o: Partial<Todo> = {}): Todo => ({ id: "t1", title: "上线新登录页", created_at: now, updated_at: now, origin: "conv1", ask: decide, ...o });

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

let root: Root;
let host: HTMLDivElement;
// what GET /todos answers after a take: the to-do with nothing waiting
let listed: Todo[] = [];
beforeEach(() => {
  api.mockReset();
  navigate.mockReset();
  toast.mockReset();
  listed = [todo({ ask: undefined })];
  api.mockImplementation(async (path: string) => (path === "/todos" ? { todos: listed } : {}));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  seed(before);
  vi.restoreAllMocks();
});

const flush = () => act(async () => {
  for (let i = 0; i < 5; i++) await Promise.resolve();
});
/** Long enough for an accept's tries (none wait under Vitest) and what follows them. */
const settle = async () => {
  for (let i = 0; i < 6; i++) await flush();
};
const drawList = async (todos: Todo[], selectedId?: string) => {
  seed({ todos, todosLoaded: true, sessions: {}, interactions: [] });
  act(() => root.render(<TodosView selectedId={selectedId} />));
  await flush();
};
const askRow = () => host.querySelector<HTMLElement>("[data-testid=todo-ask]");
const askButton = (within: ParentNode = host) => within.querySelector<HTMLButtonElement>("[data-testid=todo-ask] button");
const accepts = () => api.mock.calls.filter(([path]) => String(path).endsWith("/accept"));

describe("an ask, as the hub sends it", () => {
  it("is open only with a verdict and a round", () => {
    expect(openAsk(todo())).toEqual(decide);
    expect(openAsk(todo({ ask: undefined }))).toBeUndefined();
    expect(openAsk(todo({ ask: { verdict: "", asked_at: ASKED } }))).toBeUndefined();
    expect(openAsk(todo({ ask: { verdict: "✅ 已上线", asked_at: "" } }))).toBeUndefined();
  });

  it("takes a decision's recommendation, acknowledges a conclusion, accepts a deployed to-do", () => {
    expect(askTake(decide)).toBe("take");
    expect(askTake({ ...decide, recommend: undefined })).toBeUndefined(); // nothing for one tap to take
    expect(askTake({ outcome: "cannot", verdict: "❌ 做不了", asked_at: ASKED })).toBe("ack");
    expect(askTake({ outcome: "done", verdict: "✅ 做完了", asked_at: ASKED })).toBe("ack");
    expect(askTake({ verdict: "✅ 做完了，已上线", asked_at: ASKED })).toBe("accept");
    expect([decide, { ...decide, outcome: "cannot" }, { ...decide, outcome: undefined }].map(askTone)).toEqual(["attention", "red", "green"]);
  });

  it("puts a stopped owner decision with Waiting for you, ahead of what runs", () => {
    const asked = todo();
    expect(awaitsReview(asked, "follow_up")).toBe(false);
    expect(awaitsReview(asked, "working")).toBe(false); // its agent is at it again
    expect(awaitsReview(todo({ ask: undefined }), "follow_up")).toBe(false);
    expect(boardSectionOf({ todo: asked, status: "follow_up" })).toBe("waiting");
    const review = todo({ ask: { ...decide, outcome: undefined } });
    expect(awaitsReview(review, "follow_up")).toBe(true);
    expect(boardSectionOf({ todo: review, status: "follow_up" })).toBe("review");
    const working = todo({ id: "t2", ask: undefined, created_at: new Date(Date.now() + 1000).toISOString(), agents: [{ machine_id: "m1", session_id: "s1", linked_at: now }] });
    // its agent working (t2), and the asked one's stopped and seen (t1)
    const lookup = (_m: string, s: string) => ({ machine_id: "m1", session_id: s, status: s === "s1" ? "working" : "idle", state: s === "s1" ? "working" : "idle" }) as never;
    expect(sortTodos([working, { ...asked, agents: [{ machine_id: "m1", session_id: "s0", linked_at: now }] }], lookup).map((t) => t.id)).toEqual(["t1", "t2"]);
  });
});

describe("a to-do's row", () => {
  it("says what it waits on and the recommendation, with OK", async () => {
    await drawList([todo()]);
    const row = askRow()!;
    expect(row.dataset.ask).toBe("decide");
    expect(row.textContent).toContain("需要你决定：两个方案各有取舍");
    expect(row.textContent).toContain("推荐：先发预发环境");
    expect(askButton()!.textContent).toBe("采纳");
    expect(askButton()!.getAttribute("aria-label")).toContain("上线新登录页");
  });

  it("OK takes it with its round, without opening the to-do", async () => {
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(accepts()).toEqual([["/todos/t1/accept", { method: "POST", json: { asked_at: ASKED } }]]);
    expect(host.querySelector("[data-testid=todo-ask-panel]")).toBeNull();
    expect(api.mock.calls.some(([path]) => path === "/todos")).toBe(true); // read again: the ask is gone
    expect(askRow()).toBeNull();
    expect(toast).toHaveBeenCalledWith("已采用推荐方案", "ok");
  });

  it("settles a recorded decision whose Agent is unavailable", async () => {
    listed = [todo()]; // keep the settled row visible after refresh
    api.mockImplementation(async (path: string) => path.endsWith("/accept") ? { ok: true, delivered: "none" } : path === "/todos" ? { todos: listed } : {});
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(host.querySelector("[data-ask-done=recorded]")?.textContent).toBe("已记录。没能送到 Agent，将由督办接手转交");
    expect(toast).toHaveBeenCalledWith("已记录。没能送到 Agent，将由督办接手转交", "ok");
  });

  it("an earlier round already answered says so and reads the to-dos again", async () => {
    listed = [todo()]; // the hub still lists it until the read comes back
    api.mockImplementation(async (path: string) => {
      if (path.endsWith("/accept")) throw new ApiError(409, "already handled", {});
      return path === "/todos" ? { todos: listed } : {};
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(host.querySelector("[data-ask-done=handled]")?.textContent).toBe("已处理过");
    expect(api.mock.calls.some(([path]) => path === "/todos")).toBe(true);
  });

  it("a recommendation that did not reach the agent leaves OK to try again", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.endsWith("/accept")) throw new ApiError(502, "推荐的方案没送到 Agent：电脑离线", {});
      return {};
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(askButton()!.disabled).toBe(false);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("推荐的方案没送到 Agent：电脑离线"), "error", resendAction);
  });

  // A restarting hub's empty 502/503 or a dropped connection is no answer (#17431): the same round is tried again a
  // couple of times (the hub takes a round once), and only then does the owner see why, with OK and Resend still there.
  it("tries an accept again through an empty gateway answer, and takes it", async () => {
    let tries = 0;
    api.mockImplementation(async (path: string) => {
      if (!path.endsWith("/accept")) return path === "/todos" ? { todos: [] } : {};
      if (++tries < 3) throw new ApiError(502, "HTTP 502", { error: "" }, false);
      return { ok: true };
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await settle();
    expect(accepts()).toHaveLength(3);
    expect(accepts().every(([, init]) => (init as { json: { asked_at: string } }).json.asked_at === ASKED)).toBe(true);
    expect(toast).toHaveBeenCalledWith("已采用推荐方案", "ok");
  });

  it("keeps the choice after the tries run out, says why, and Resend sends that round once more", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.endsWith("/accept")) throw new ApiError(503, "HTTP 503", { error: "" }, false);
      return {};
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await settle();
    expect(accepts()).toHaveLength(3);
    expect(askButton()!.disabled).toBe(false);
    const [text, tone, action] = toast.mock.calls.at(-1)! as [string, string, { label: string; run: () => void }];
    expect(text).toContain(t("lib-api.hub_unavailable", { detail: "HTTP 503" }));
    expect(tone).toBe("error");
    expect(action.label).toBe(t("todos.ask_resend"));
    api.mockImplementation(async (path: string) => path.endsWith("/accept") ? { ok: true } : path === "/todos" ? { todos: [] } : {});
    act(() => { action.run(); action.run(); });
    await flush();
    expect(accepts()).toHaveLength(4);
    expect(toast).toHaveBeenCalledWith("已采用推荐方案", "ok");
  });

  it("never tries again an accept the hub refused with its own reason", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.endsWith("/accept")) throw new ApiError(502, "回答没发出去：电脑离线", { code: "accept_not_taken", error: "回答没发出去：电脑离线", delivered: "none" });
      return {};
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(accepts()).toHaveLength(1);
    expect(toast).toHaveBeenCalledWith(t("todos.ask_failed", { error: "回答没发出去：电脑离线" }), "error", resendAction);
  });

  it("a newer round offers OK again after the last one was handled", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.endsWith("/accept")) throw new ApiError(409, "already handled", {});
      return path === "/todos" ? { todos: [todo()] } : {};
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(askButton()).toBeNull();
    seed({ todos: [todo({ ask: { ...decide, asked_at: "2026-11-09T09:00:00Z", recommend: "直接上线" } })] });
    act(() => root.render(<TodosView />));
    await flush();
    expect(askButton()?.textContent).toBe("采纳");
    expect(askRow()!.textContent).toContain("推荐：直接上线");
  });

  it("a decision with nothing recommended has no button; a conclusion has Got it, a deployed to-do Accept", async () => {
    await drawList([
      // distinct titles: to-dos with the same title share one row (#3156)
      todo({ ask: { ...decide, recommend: undefined } }),
      todo({ id: "t2", title: "接入开放接口", ask: { outcome: "cannot", verdict: "❌ 做不了：接口不开放", asked_at: ASKED } }),
      todo({ id: "t3", title: "上线新版本", ask: { verdict: "✅ 做完了，已上线", asked_at: ASKED } }),
    ]);
    const byOutcome = Object.fromEntries([...host.querySelectorAll<HTMLElement>("[data-testid=todo-ask]")].map((el) => [el.dataset.ask, el]));
    expect(askButton(byOutcome.decide)).toBeNull();
    expect(askButton(byOutcome.cannot)!.textContent).toBe("知道了");
    expect(askButton(byOutcome.deployed)!.textContent).toBe("通过");
  });

  it("renders merged-not-deployed verification copy with Pass action intact when outcome is empty", async () => {
    const mergedAsk: TodoAsk = { verdict: "✅ 做完了：已合未部署，按下面验收", asked_at: ASKED };
    expect(askTake(mergedAsk)).toBe("accept");
    await drawList([todo({ ask: mergedAsk })]);
    const row = askRow()!;
    expect(row.dataset.ask).toBe("deployed");
    expect(row.textContent).toContain("✅ 做完了：已合未部署，按下面验收");
    expect(row.textContent).not.toContain("已部署，按下面验收");
    expect(askButton()!.textContent).toBe("通过");
    act(() => askButton()!.click());
    await flush();
    expect(accepts()).toEqual([["/todos/t1/accept", { method: "POST", json: { asked_at: ASKED } }]]);
  });
});

describe("a to-do's page", () => {
  it("follows up in the origin composer with a removable task quote and the draft intact", async () => {
    const origin = "direction";
    const key = "assistant-computer/direction";
    const ask = { verdict: "Please verify", asked_at: ASKED, summary: "The route is step-free." };
    const task = todo({ origin, ask, supervision: { supervisor: "main", state: "user_check" } });
    seed({ assistant: { machine_id: "assistant-computer", main: "main", conversations: [] } });
    storeDraft(key, "My existing question");
    setRoute(origin, "earlier-decision", ASKED);
    act(() => root.render(<TodoAskPanel todo={task} />));
    const action = () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("assistant-taskpanel.follow_up"));
    await act(async () => action()!.click());
    expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: origin });
    const focus = vi.fn();
    const unregister = registerComposer({ key, focus, send: vi.fn() });
    expect(focus).toHaveBeenCalledOnce();
    unregister();
    const quotes = getQuotes(key).list;
    expect(quotes).toHaveLength(1);
    expect(quotes[0]).toMatchObject({ task: { id: task.id, title: task.title }, text: ask.summary });
    expect(withQuoteBlock(quotes, storedPageDraft(key))).toContain(task.id);
    removeQuote(key, quotes[0].id!);
    expect(getQuotes(key).list).toEqual([]);
    expect(storedPageDraft(key)).toBe("My existing question");
    expect(getFeedState().routes[origin]).toBeUndefined();
    expect(accepts()).toEqual([]);
    expect(host.querySelector("[data-ask-done]")).toBeNull();
    act(() => root.render(<TodoAskPanel todo={{ ...task, origin: undefined }} />));
    expect(action()).toBeUndefined();
    act(() => root.render(<TodoAskPanel todo={{ ...task, supervision: undefined }} />));
    expect(action()).toBeUndefined();
    storeDraft(key, "");
  });

  it("shows the verification summary and direct deliverable, with an empty fallback on older hubs", () => {
    const summary = "Both routes have step-free access.";
    const link = "https://docs.example.com/routes";
    const ask = { verdict: "Please verify the result", asked_at: ASKED };
    act(() => root.render(<TodoAskPanel todo={todo({ ask: { ...ask, summary, link } })} />));
    expect(host.textContent).toContain(summary);
    expect(host.querySelector("a")?.href).toBe(link);
    for (const legacy of [ask, { ...ask, link: location.origin + "/s/node/worker" }]) {
      act(() => root.render(<TodoAskPanel todo={todo({ ask: legacy })} />));
      expect(host.textContent).toContain(ask.verdict);
      expect(host.textContent).not.toContain(summary);
      expect(host.querySelector("a")).toBeNull();
    }
  });

  it("lists parked work outside Done and counts it under the badge's name", async () => {
    await drawList([todo({
      seen_at: new Date(Date.now() + 1000).toISOString(),
      ask: { outcome: "done", verdict: "The previous turn ended.", asked_at: now },
      supervision: {
        supervisor: "conv1", state: "parked",
        parked: { reason: "Waiting on another team", waiting_on: "someone_else", until: new Date(Date.now() + 86400_000).toISOString(), count: 1 },
      },
    })]);
    expect(host.querySelector('[data-section="parked"]')?.textContent).toContain("上线新登录页");
    expect(host.querySelector('[data-section="done"]')?.textContent ?? "").not.toContain("上线新登录页");
    expect(host.querySelector("header")?.textContent).toContain("已搁置 1");
    expect(host.querySelector("header")?.textContent).toContain("暂无进展 0");
  });

  it("shows the ask in full with its button and the conversation to answer it in", async () => {
    await drawList([todo()], "t1");
    const panel = host.querySelector<HTMLElement>("[data-testid=todo-ask-panel]")!;
    expect(panel.textContent).toContain("需要你"); // the state name (lib/state-labels.ts)
    expect(panel.textContent).toContain("推荐：先发预发环境");
    const buttons = [...panel.querySelectorAll("button")].map((b) => b.textContent);
    expect(buttons).toEqual(["采用推荐", "前往对话回答"]);
    act(() => [...panel.querySelectorAll("button")][1].click());
    expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "conv1" });
    expect(getFeedState().routes.conv1).toEqual({ todoId: "t1", askedAt: ASKED });
    setRoute("conv1", null);
  });

  it("a decision with nothing recommended says where to answer it", async () => {
    await drawList([todo({ ask: { ...decide, recommend: undefined } })], "t1");
    const panel = host.querySelector<HTMLElement>("[data-testid=todo-ask-panel]")!;
    expect(panel.textContent).toContain("暂无推荐方案");
    expect([...panel.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["前往对话回答"]);
  });

  it("shows nothing while the to-do waits on nothing", async () => {
    await drawList([todo({ ask: undefined })], "t1");
    expect(host.querySelector("[data-testid=todo-ask-panel]")).toBeNull();
  });
});

describe("controlled to-do ask callbacks", () => {
  it("uses every supplied callback without the default feed, router, store, API, or toast", async () => {
    const accept = vi.spyOn(feed, "acceptBrief").mockRejectedValue(new Error("unexpected default accept"));
    const route = vi.spyOn(feed, "setRoute").mockImplementation(() => {});
    const refresh = vi.spyOn(store, "refreshTodos").mockResolvedValue();
    const onAccept = vi.fn().mockResolvedValue("handled");
    const onOpenConversation = vi.fn();
    const onRefresh = vi.fn();
    const onNotice = vi.fn();
    const item = todo();
    act(() => root.render(
      <TodoAskPanel
        todo={item}
        onAccept={onAccept}
        onOpenConversation={onOpenConversation}
        onRefresh={onRefresh}
        onNotice={onNotice}
      />,
    ));
    await flush();
    const panel = host.querySelector<HTMLElement>("[data-testid=todo-ask-panel]")!;
    act(() => panel.querySelector("button")!.click());
    await flush();
    expect(onAccept).toHaveBeenCalledWith("t1", ASKED);
    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(onNotice).toHaveBeenCalledWith("已处理过", "ok");
    expect(host.querySelector("[data-ask-done=handled]")).not.toBeNull();

    const conversation = [...panel.querySelectorAll("button")].find((button) => button.textContent === "前往对话回答")!;
    act(() => conversation.click());
    expect(onOpenConversation).toHaveBeenCalledWith(item, decide);
    expect(accept).not.toHaveBeenCalled();
    expect(route).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
    expect(api).not.toHaveBeenCalled();
  });

  it("reports a controlled failure and leaves the row retryable without default fallbacks", async () => {
    const accept = vi.spyOn(feed, "acceptBrief").mockRejectedValue(new Error("unexpected default accept"));
    const refresh = vi.spyOn(store, "refreshTodos").mockResolvedValue();
    const onAccept = vi.fn().mockRejectedValue(new Error("panel offline"));
    const onRefresh = vi.fn();
    const onNotice = vi.fn();
    act(() => root.render(<TodoAskLine todo={todo()} onAccept={onAccept} onRefresh={onRefresh} onNotice={onNotice} />));
    await flush();
    act(() => askButton()!.click());
    await flush();
    expect(onAccept).toHaveBeenCalledWith("t1", ASKED);
    expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("panel offline"), "error", resendAction);
    expect(onRefresh).not.toHaveBeenCalled();
    expect(askButton()!.disabled).toBe(false);
    expect(accept).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
    expect(api).not.toHaveBeenCalled();
  });

  it("refreshes after an ordinary controlled accept", async () => {
    const accept = vi.spyOn(feed, "acceptBrief").mockRejectedValue(new Error("unexpected default accept"));
    const refresh = vi.spyOn(store, "refreshTodos").mockResolvedValue();
    const onAccept = vi.fn().mockResolvedValue("accepted");
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    const onNotice = vi.fn();
    act(() => root.render(<TodoAskLine todo={todo()} onAccept={onAccept} onRefresh={onRefresh} onNotice={onNotice} />));
    await flush();
    act(() => askButton()!.click());
    await flush();
    expect(onAccept).toHaveBeenCalledWith("t1", ASKED);
    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(onNotice).toHaveBeenCalledWith("已采用推荐方案", "ok");
    expect(host.querySelector("[data-ask-done=taken]")).not.toBeNull();
    expect(accept).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
    expect(api).not.toHaveBeenCalled();
  });

  it("uses a supplied refresh with default accept after ordinary success", async () => {
    const accept = vi.spyOn(feed, "acceptBrief").mockResolvedValue("accepted");
    const refresh = vi.spyOn(store, "refreshTodos").mockResolvedValue();
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    act(() => root.render(<TodoAskLine todo={todo()} onRefresh={onRefresh} />));
    await flush();
    act(() => askButton()!.click());
    await flush();
    expect(accept).toHaveBeenCalledWith("t1", ASKED);
    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(refresh).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith("已采用推荐方案", "ok");
    expect(host.querySelector("[data-ask-done=taken]")).not.toBeNull();
  });

  it("recovers when controlled accept throws before returning a promise", async () => {
    const onAccept = vi.fn(() => { throw new Error("synchronous offline"); });
    const onNotice = vi.fn();
    act(() => root.render(<TodoAskLine todo={todo()} onAccept={onAccept} onNotice={onNotice} />));
    await flush();
    act(() => askButton()!.click());
    await flush();
    expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("synchronous offline"), "error", resendAction);
    expect(askButton()!.disabled).toBe(false);
    expect(toast).not.toHaveBeenCalled();
  });

  it("reports a controlled refresh failure without reopening the answered round", async () => {
    const onAccept = vi.fn().mockResolvedValue("accepted");
    const onRefresh = vi.fn().mockRejectedValue(new Error("refresh offline"));
    const onNotice = vi.fn();
    act(() => root.render(<TodoAskLine todo={todo()} onAccept={onAccept} onRefresh={onRefresh} onNotice={onNotice} />));
    await flush();
    act(() => askButton()!.click());
    await flush();
    expect(onNotice).toHaveBeenNthCalledWith(1, "已采用推荐方案", "ok");
    expect(onNotice).toHaveBeenNthCalledWith(2, expect.stringContaining("refresh offline"), "error");
    expect(host.querySelector("[data-ask-done=taken]")).not.toBeNull();
    expect(askButton()).toBeNull();
    expect(toast).not.toHaveBeenCalled();
  });
});

describe("default to-do ask compatibility", () => {
  it("falls back to acceptBrief, setRoute, navigate, refreshTodos, and toast without callbacks", async () => {
    const accept = vi.spyOn(feed, "acceptBrief").mockResolvedValue("handled");
    const route = vi.spyOn(feed, "setRoute").mockImplementation(() => {});
    const refresh = vi.spyOn(store, "refreshTodos").mockResolvedValue();
    act(() => root.render(<TodoAskPanel todo={todo()} />));
    await flush();
    const panel = host.querySelector<HTMLElement>("[data-testid=todo-ask-panel]")!;
    act(() => panel.querySelector("button")!.click());
    await flush();
    expect(accept).toHaveBeenCalledWith("t1", ASKED);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith("已处理过", "ok");

    const conversation = [...panel.querySelectorAll("button")].find((button) => button.textContent === "前往对话回答")!;
    act(() => conversation.click());
    expect(route).toHaveBeenCalledWith("conv1", "t1", ASKED);
    expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "conv1" });
  });
});
