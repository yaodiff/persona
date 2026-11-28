// @vitest-environment jsdom
// A decision in the Needs you tray is answered on its row (#3804): the question, the background, what each option
// leads to, the options as buttons with the recommended one first, what happens by default, "Other…", and the answers
// that are no option in the ⋯ menu. An ask without options keeps the row it had.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { getFeedState, loadFeed, setRoute, type FeedItem, type TaskPlan } from "../../lib/feed";
import { clock } from "../../lib/format";
import { setAgentOpenMode } from "../../lib/agent-open";
import { closePanel, getPanel } from "../../lib/panel";
import { decisionLaterKey } from "../../lib/assistant-entries";
import { storedValue } from "../../lib/compatibility";
import { getQuotes, parseQuote, setQuotes, splitQuote, storeDraft, withQuoteBlock } from "../../lib/drafts";
import { getState, putTodo, refresh, refreshTodos, reset } from "../../lib/store";
import type { Todo, TodoAsk } from "../../lib/todos";
import { AssistantActionBar } from "./Tasks";
import * as atoms from "../../ui/atoms";

// where a click went as a page (set to Full page, #5096); the side panel says where it went otherwise (getPanel)
const opened = () => location.pathname + location.search;

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const at = "2026-11-10T08:00:00Z";
const deadline = "2026-11-11T06:00:00Z";
const conversation = "decision-demo";
const plan: TaskPlan = { id: "decision-plan", conversation, state: "started", created_at: at, tasks: [{ title: "Demo release", prompt: "Demo", cwd: "/demo", machine: "demo", todo_id: "decision-task" }] };
const feed: FeedItem[] = [
  { id: "decision-plan-event", conversation, at, kind: "plan", ref: plan.id },
  { id: "decision-task-event", conversation, at, kind: "task", ref: "decision-task" },
];
const decision: TodoAsk = {
  outcome: "decide",
  verdict: "Needs your decision: release to everyone now, or to 10% first",
  recommend: "10% first",
  asked_at: at,
  question: "Release to everyone now, or to 10% first?",
  why_owner: "priority",
  options: [
    { label: "10% first", leads_to: "one more day, a bad build reaches few people" },
    { label: "Everyone now", leads_to: "ships today, a bad build reaches all" },
    { label: "Hold the release", leads_to: "nothing ships this week" },
  ],
  cost_of_waiting: "the release slips a day",
  background: "From the main conversation, Nov 10 · merged and checked in staging",
  deadline,
  default: "taken",
  responses: ["decide_yourself", "not_what_i_asked", "explain_first"],
};

let ask: TodoAsk | undefined;
let todo: Todo;
let host: HTMLDivElement;
let root: Root;
let posts: { path: string; body: unknown }[];
let status: number;
let turnError: boolean;
let needsInput: boolean;
let postGate: Promise<void> | undefined;
let listStatus: number;
let assistantStatus: number;
let assistantMain: string;
let refusal: Record<string, unknown> | undefined; // a POST's error body other than the offline one

async function mount() {
  todo = { id: "decision-task", title: "Demo release", origin: conversation, plan_id: plan.id, created_at: at, updated_at: at,
    agents: [{ machine_id: "demo", session_id: "worker", linked_at: at }], supervision: { supervisor: conversation, state: "needs_user", reviews: [{ at, verdict: "needs_user", note: "The example checks passed; release approval is still needed." }] }, decision_review: { review_at: at }, ask };
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = String(url);
    if (init?.method === "POST") {
      posts.push({ path, body: JSON.parse(String(init.body)) });
      if (postGate) await postGate;
      if (status !== 200) return new Response(JSON.stringify(refusal ?? { error: "The computer is offline" }), { status, headers: { "Content-Type": "application/json" } });
      if (path.endsWith("/decision-dismiss")) {
        todo = { ...todo, decision_review: { review_at: at, dismissal_id: "dismiss-demo" } };
        return new Response(JSON.stringify(todo.decision_review), { headers: { "Content-Type": "application/json" } });
      }
      if (path.endsWith("/decision-undo-dismiss")) {
        todo = { ...todo, decision_review: { review_at: at } };
        return new Response(JSON.stringify(todo.decision_review), { headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify(refusal ?? { ok: true }), { headers: { "Content-Type": "application/json" } });
    }
    if (path.includes("/todos") && listStatus !== 200) return new Response("{}", { status: listStatus });
    if (path.endsWith("/assistant") && assistantStatus !== 200) return new Response("{}", { status: assistantStatus });
    const body = path.includes("/feed") ? { items: feed, plans: { [plan.id]: plan }, task_cards: [{ id: `plan:${plan.id}:0`, plan_id: plan.id, index: 0, todo_id: todo.id, at, updated_at: at, state: "accepted" }] }
      : path.includes("/todos") ? { todos: [todo] }
      : path.endsWith("/assistant") ? { machine_id: "new-chat-node", main_machine_id: "main-node", main: assistantMain, conversations: [] }
      : path.includes("/sessions") ? { sessions: [{ machine_id: "demo", session_id: "worker", title: "Demo agent", state: needsInput ? "needs_you" : "idle", status: "idle", updated_at: at, ...(turnError ? { turn_error: "Synthetic transient error", turn_error_kind: "transient" } : {}) }] } : {};
    return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  }));
  await act(async () => {
    await refresh();
    await loadFeed(conversation);
    await refreshTodos();
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<AssistantActionBar conversation={conversation} />));
  act(() => host.querySelector<HTMLButtonElement>('[data-tray-entry="needs"]')!.click());
  await act(async () => void (await import("./TaskDecision"))); // the row's code arrives with the first decision
  // the row is folded to a line until opened (#5569)
  await act(async () => host.querySelector<HTMLButtonElement>('button[data-needs-row="task:decision-task"]')?.click());
  // The row is a lazy chunk: React.lazy suspends once on its first render even when the module is in, so the row is
  // drawn a little after the click, as late as the machine is slow. Each poll runs in act so React's retry can flush.
  if (ask?.options?.length) {
    await vi.waitFor(async () => {
      await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
      expect(row().querySelector("[data-decision-row]")).not.toBeNull();
    }, { timeout: 5000, interval: 20 });
  }
}

beforeEach(() => {
  setRoute(conversation, null);
  ask = decision;
  posts = [];
  status = 200;
  turnError = false;
  needsInput = false;
  postGate = undefined;
  listStatus = 200;
  assistantStatus = 200;
  assistantMain = "main-chat";
  refusal = undefined;
});
afterEach(() => {
  act(() => root.unmount());
  setRoute("main-chat", null);
  setQuotes("main-node/main-chat", []);
  setQuotes("persona-new", []);
  storeDraft("main-node/main-chat", "");
  host.remove();
  history.replaceState(null, "", "/");
  setAgentOpenMode("panel");
  closePanel();
  reset();
  delete getFeedState().plans[plan.id];
  delete getFeedState().feeds[conversation];
  delete getFeedState().taskCards[conversation];
  delete getFeedState().later[decisionLaterKey(todo)];
  vi.unstubAllGlobals();
});

const row = () => host.querySelector<HTMLElement>('[data-needs-row="task:decision-task"]')!;
const optionButtons = () => [...row().querySelectorAll<HTMLElement>("[data-decision-option]")].map((s) => s.closest("button")!);
const menuRows = () => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")];
const openMenu = () => act(() => row().querySelector<HTMLButtonElement>(`button[aria-label="${t("assistant-decisionrow.more")}"]`)!.click());
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });

describe("a decision in the Needs you tray", () => {
  it("opens the existing main composer with a decision quote and clears an earlier task reply route", async () => {
    await mount();
    const key = "main-node/main-chat";
    const question = decision.recommend!; // even words equal to an option can be discussed there
    storeDraft(key, question);
    act(() => setRoute("main-chat", todo.id, at));
    const open = [...row().querySelectorAll("button")].find((button) => button.textContent === t("assistant-decisionrow.ask_in_main_chat"));
    expect(open).toBeDefined();
    assistantStatus = 503;
    await act(async () => open!.click());
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(getQuotes(key).list).toHaveLength(0);
    expect(getFeedState().routes["main-chat"]).toEqual({ todoId: todo.id, askedAt: at });
    expect(storedValue(`draft:${key}`)).toBe(question);
    assistantStatus = 200;
    await act(async () => { open!.click(); open!.click(); });
    expect(posts).toHaveLength(0); // attaching a quote leaves the send to the owner
    expect(opened()).toBe("/persona/main-chat");
    expect(getFeedState().routes["main-chat"]).toBeUndefined();
    expect(getQuotes(key).list).toHaveLength(1);
    expect(storedValue(`draft:${key}`)).toBe(question);
    const split = splitQuote(withQuoteBlock(getQuotes(key).list, question))!;
    const quoted = parseQuote(split.quote);
    expect(split.rest).toBe(question);
    expect(quoted.task).toEqual({ title: todo.title, id: todo.id });
    expect(quoted.text).toContain(decision.question);
    for (const option of decision.options!) {
      expect(quoted.text).toContain(`${option.label}: ${option.leads_to}`);
    }
    expect(quoted.text).toContain(t("assistant-brief.recommend", { option: question }));
    expect(quoted.text).toContain(t("assistant-decisionrow.ask_discussion_context"));
    expect(getState().todos.find((value) => value.id === todo.id)?.ask).toEqual(decision);
    expect(row()).toBeNull(); // the discussion has the full chat viewport
    act(() => host.querySelector<HTMLButtonElement>('[data-tray-entry="needs"]')!.click());
    await act(async () => host.querySelector<HTMLButtonElement>('button[data-needs-row="task:decision-task"]')?.click());
    await vi.waitFor(async () => {
      await act(async () => {});
      expect(row()?.querySelector("[data-decision-row]")).toBeTruthy();
    });
    expect(optionButtons().every((button) => !button.disabled)).toBe(true);
  });

  it("attaches the quote to the main starter when the current assistant has no main chat yet", async () => {
    assistantMain = "";
    await mount();
    const open = [...row().querySelectorAll("button")].find((button) => button.textContent === t("assistant-decisionrow.ask_in_main_chat"))!;
    await act(async () => open.click());
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(opened()).toBe("/");
    expect(getQuotes("persona-new").list[0]?.task?.id).toBe(todo.id);
    expect(posts).toHaveLength(0);
  });

  it("opens a legacy manual-resume option on its terminal without answering the decision", async () => {
    ask = { ...decision, terminal_option: decision.options![0].label, resume_terminal: { machine_id: "demo", session_id: "worker" } };
    await mount();
    act(() => optionButtons()[0].click());
    await flush();
    expect(opened()).toMatch(/\/s\/demo\/worker\b.*view=term/);
    expect(posts).toHaveLength(0);
  });

  it("offers a separate terminal link alongside a replacement decision", async () => {
    ask = { ...decision, resume_terminal: { machine_id: "demo", session_id: "worker" } };
    await mount();
    const open = [...row().querySelectorAll("button")].find((button) => button.textContent === t("assistant-decisionrow.open_terminal"))!;
    act(() => open.click());
    expect(opened()).toMatch(/\/s\/demo\/worker\b.*view=term/);
    expect(posts).toHaveLength(0);
  });

  it("shows the question, the background, what each option leads to and the default, with the recommended option first", async () => {
    await mount();
    expect(row().querySelector("[data-decision-row]")).not.toBeNull();
    expect(row().querySelector("[data-decision-question]")!.textContent).toBe(decision.question);
    expect(row().querySelector("[data-decision-background]")!.textContent).toBe(decision.background);
    const leads = [...row().querySelectorAll("[data-decision-leads]")].map((li) => li.textContent);
    expect(leads[0]).toBe("10% first" + t("chat-interactions.recommended") + t("assistant-decisionrow.leads_to", { leads: "one more day, a bad build reaches few people" }));
    expect(leads[1]).toBe(t("assistant-decisionrow.leads", { option: "Everyone now", leads: "ships today, a bad build reaches all" }));
    expect(row().querySelector('[data-decision-default="taken"]')!.textContent).toBe(t("assistant-decisionrow.default_at", { time: clock(deadline) }));
    expect(row().querySelector("[data-decision-cost]")!.textContent).toBe(t("assistant-decisionrow.cost", { cost: "the release slips a day" }));
    const buttons = optionButtons();
    expect(buttons.map((b) => b.textContent)).toEqual(["10% first", "Everyone now", "Hold the release"]);
    expect(buttons[0].className).toContain("bg-ink"); // the one solid button
    expect(buttons[1].className).not.toContain("bg-ink");
    for (const b of row().querySelectorAll("button")) expect(b.className).not.toMatch(/whitespace-normal|flex-wrap/);
    expect(row().textContent).toContain(t("assistant-decisionrow.another"));
    // the fixed answers are in the menu, not buttons on the row
    expect(row().textContent).not.toContain(t("assistant-decisionrow.decide_yourself"));
    openMenu();
    expect(menuRows().map((r) => r.textContent)).toEqual([
      t("assistant-decisionrow.decide_yourself") + t("assistant-decisionrow.decide_yourself_detail"),
      t("assistant-decisionrow.not_what_i_asked") + t("assistant-decisionrow.not_what_i_asked_detail"),
      t("assistant-decisionrow.explain_first") + t("assistant-decisionrow.explain_first_detail"),
    ]);
  });

  it("says an irreversible decision is never taken by default", async () => {
    ask = { ...decision, why_owner: "irreversible", default: "never", deadline: undefined };
    await mount();
    expect(row().querySelector('[data-decision-default="never"]')!.textContent).toBe(t("assistant-decisionrow.default_never"));
  });

  it("takes two presses for an option of an irreversible decision", async () => {
    ask = { ...decision, why_owner: "irreversible", default: "never", deadline: undefined };
    await mount();
    const [first] = optionButtons();
    act(() => first.click());
    await flush();
    expect(posts).toEqual([]);
    expect(row().querySelector("[data-decision-armed]")!.textContent).toBe(t("assistant-decisionrow.confirm", { option: "10% first" }));
    act(() => optionButtons()[0].click());
    await flush();
    expect(posts).toEqual([{ path: expect.stringContaining("/todos/decision-task/accept"), body: { asked_at: at, option: "10% first" } }]);
  });

  it("posts the option chosen with the round, once, and says it was answered", async () => {
    await mount();
    const [, second] = optionButtons();
    act(() => second.click());
    act(() => second.click());
    await flush();
    expect(posts).toEqual([{ path: expect.stringContaining("/todos/decision-task/accept"), body: { asked_at: at, option: "Everyone now" } }]);
    expect(row().querySelector("[data-decision-done]")!.textContent).toBe(t("assistant-decisionrow.chosen", { option: "Everyone now" }));
    expect(optionButtons()).toEqual([]);
  });

  it("records the decision when the hub says no Agent received it", async () => {
    refusal = { delivered: "none", ok: true };
    await mount();
    act(() => optionButtons()[0].click());
    await flush();
    expect(row().querySelector("[data-decision-done]")!.textContent).toBe(t("assistant-brief.recorded_not_delivered"));
    expect(optionButtons()).toEqual([]);
  });

  it("posts a fixed response from the menu", async () => {
    await mount();
    openMenu();
    act(() => menuRows()[1].click());
    await flush();
    expect(posts).toEqual([{ path: expect.stringContaining("/todos/decision-task/accept"), body: { asked_at: at, response: "not_what_i_asked" } }]);
    expect(row().querySelector("[data-decision-done]")!.textContent).toBe(t("assistant-decisionrow.responded"));
  });

  it("keeps the buttons when the answer did not go, and says a round already answered is handled", async () => {
    status = 502;
    await mount();
    act(() => optionButtons()[0].click());
    await flush();
    expect(posts).toHaveLength(1);
    expect(row().querySelector("[data-decision-done]")).toBeNull();
    expect(optionButtons()[0].hasAttribute("disabled")).toBe(false);
    status = 409;
    act(() => optionButtons()[0].click());
    await flush();
    expect(row().querySelector("[data-decision-done]")!.textContent).toBe(t("assistant-brief.handled"));
  });

  // A dialog in the Agent's terminal kept the answer from it (#7343): 423 dialog_open, which is not "handled". The row
  // keeps its buttons and says so itself, with the dialog's first line and the way to that terminal; while the hub
  // answered it 409, the row read it as an answered round and showed nothing, so the press seemed to do nothing.
  it("says a dialog in the Agent's terminal blocks the answer, and opens that terminal", async () => {
    setAgentOpenMode("page");
    status = 423;
    refusal = { code: "dialog_open", delivered: "none", retryable: false, error: "a dialog is open", dialog: "Approaching rate limits", machine_id: "demo", session_id: "worker" };
    const toast = vi.spyOn(atoms, "toast");
    await mount();
    act(() => optionButtons()[0].click());
    await flush();
    expect(posts).toHaveLength(1);
    expect(row().querySelector("[data-decision-done]")).toBeNull();
    const blocked = row().querySelector<HTMLElement>("[data-decision-blocked]")!;
    expect(blocked.textContent).toContain(t("assistant-decisionrow.dialog_open", { dialog: "Approaching rate limits" }));
    expect(blocked.textContent).toContain("Approaching rate limits");
    expect(toast).not.toHaveBeenCalled();
    expect(optionButtons()[0].hasAttribute("disabled")).toBe(false);
    const open = [...blocked.querySelectorAll("button")].find((b) => b.textContent === t("assistant-decisionrow.open_terminal"))!;
    act(() => open.click());
    expect(opened()).toMatch(/\/s\/demo\/worker\b.*view=term/);
    // pressed again once the dialog is gone: the notice goes and the answer is taken
    status = 200;
    act(() => optionButtons()[0].click());
    await flush();
    expect(row().querySelector("[data-decision-blocked]")).toBeNull();
    expect(row().querySelector("[data-decision-done]")).not.toBeNull();
    toast.mockRestore();
  });

  // A hub that answered the refusal 409 (#7350 before 423) is not taken for an answered round either: its code says why.
  it("says a dialog blocks it without its line or a terminal to open when the hub did not say them", async () => {
    status = 409;
    refusal = { code: "dialog_open", delivered: "none", error: "a dialog is open" };
    await mount();
    act(() => optionButtons()[0].click());
    await flush();
    const blocked = row().querySelector<HTMLElement>("[data-decision-blocked]")!;
    expect(blocked.textContent).toBe(t("assistant-decisionrow.dialog_open_unnamed"));
    expect(blocked.querySelector("button")).toBeNull();
    expect(row().querySelector("[data-decision-done]")).toBeNull();
  });

  it("opens the originating conversation with this decision routed to its composer from Other…", async () => {
    await mount();
    const other = [...row().querySelectorAll("button")].find((b) => b.textContent === t("assistant-decisionrow.another"))!;
    act(() => other.click());
    expect(opened()).toBe("/persona/decision-demo");
    expect(getFeedState().routes[conversation]).toEqual({ todoId: "decision-task", askedAt: at });
    expect(getPanel()).toBeNull();
    expect(posts).toEqual([]);
  });

  it("opens the free-text answer rather than the Agent page from Other…, set to Full page (#5096)", async () => {
    setAgentOpenMode("page");
    await mount();
    const other = [...row().querySelectorAll("button")].find((b) => b.textContent === t("assistant-decisionrow.another"))!;
    act(() => other.click());
    expect(getPanel()).toBeNull();
    expect(opened()).toBe("/persona/decision-demo");
    expect(getFeedState().routes[conversation]).toEqual({ todoId: "decision-task", askedAt: at });
  });

  it("keeps the task-opening fallback when a decision has no originating conversation", async () => {
    await mount();
    await act(async () => putTodo({ ...todo, origin: undefined }));
    const other = [...row().querySelectorAll("button")].find((b) => b.textContent === t("assistant-decisionrow.another"))!;
    act(() => other.click());
    expect(getPanel()).toMatchObject({ kind: "task", todo: "decision-task" });
    expect(opened()).toBe("/");
    expect(getFeedState().routes[conversation]).toBeUndefined();
  });

  it("says it went back to Persona while a decision answered with a fixed response still waits", async () => {
    ask = { ...decision, response: "explain_first", default: "never", deadline: undefined };
    await mount();
    const row = host.querySelector("[data-decision-row]")!;
    expect(row.querySelector("[data-decision-response]")?.textContent).toBe(t("assistant-decisionrow.responded"));
    expect(row.querySelector("[data-decision-option]")).not.toBeNull(); // the options can still be chosen
  });

  it("defers only this decision round without writing or completing the task", async () => {
    await mount();
    const actions = row().querySelector("[data-decision-actions]")!;
    expect([...actions.querySelectorAll("button")].map((b) => b.textContent)).toEqual([
      t("assistant-task-actions.later"), t("assistant-task-actions.dismiss_draft"), t("assistant-decisionrow.ask_in_main_chat"),
    ]);
    act(() => actions.querySelector<HTMLButtonElement>("button")!.click());
    expect(row()).toBeNull();
    expect(posts).toEqual([]);
    const next = "2026-11-11T08:00:00Z";
    await act(async () => putTodo({ ...todo, supervision: { ...todo.supervision!, reviews: [{ at: next, verdict: "needs_user" }] }, decision_review: { review_at: next }, ask: { ...decision, asked_at: next } }));
    expect(row()).not.toBeNull();
  });

  it("dismisses the exact review, never accepts an option or completes the task", async () => {
    await mount();
    const buttons = row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button");
    const firstOption = optionButtons()[0];
    await act(async () => { buttons[1].click(); firstOption.click(); buttons[0].click(); });
    expect(posts).toEqual([{ path: expect.stringContaining("/todos/decision-task/decision-dismiss"), body: { review_at: at } }]);
    expect(row()).toBeNull();
    expect(todo.done_at).toBeUndefined();
    expect(todo.ask).toEqual(decision);
    expect(todo.supervision?.state).toBe("needs_user");
  });

  it("keeps dismiss and later on a legacy review without inventing acceptance options", async () => {
    ask = undefined;
    await mount();
    expect(row().querySelector("[data-decision-legacy]")?.textContent).toContain("release approval is still needed");
    expect(row().querySelector("[data-decision-option]")).toBeNull();
    expect(row().textContent).toContain(t("assistant-decisionrow.missing_options"));
    const buttons = row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button");
    await act(async () => buttons[1].click());
    expect(posts).toEqual([{ path: expect.stringContaining("/decision-dismiss"), body: { review_at: at } }]);
    expect(row()).toBeNull();
  });

  it("does not let a stale dismissal projection hide a newly pushed review", async () => {
    await mount();
    await act(async () => row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button")[1].click());
    const next = "2026-11-11T09:00:00Z";
    await act(async () => putTodo({ ...todo, supervision: { ...todo.supervision!, reviews: [{ at: next, verdict: "needs_user" }] }, ask: { ...decision, asked_at: next } }));
    // a new round of the same task asks folded again (#5569)
    expect(row()).not.toBeNull();
    expect(row().querySelector("[data-decision-option]")).toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>('button[data-needs-row="task:decision-task"]')?.click());
    expect(row().querySelector("[data-decision-option]")).not.toBeNull();
  });

  it("allows retry after dismiss fails and blocks dismissal after option acceptance starts", async () => {
    status = 503;
    await mount();
    const dismiss = row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button")[1];
    await act(async () => dismiss.click());
    expect(row()).not.toBeNull();
    expect(dismiss.disabled).toBe(false);
    status = 200;
    await act(async () => { optionButtons()[0].click(); dismiss.click(); });
    expect(posts.map((p) => p.path.split("/").at(-1))).toEqual(["decision-dismiss", "accept"]);
  });

  it.each(["legacy", "structured"])("optimistically hides an interrupted %s review through stale reads, confirmation, refresh and Undo", async (kind) => {
    if (kind === "legacy") ask = undefined;
    turnError = true;
    await mount();
    const toast = vi.spyOn(atoms, "toast");
    let finish!: () => void;
    postGate = new Promise<void>((resolve) => { finish = resolve; });
    const deferredKey = decisionLaterKey(todo);
    act(() => row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button")[1].click());
    expect(row()).toBeNull();
    expect(getFeedState().later[deferredKey]).toBe(true);
    await act(async () => refreshTodos());
    expect(row()).toBeNull();
    await act(async () => { finish(); await Promise.resolve(); });
    expect(getFeedState().later[deferredKey]).toBeFalsy();
    expect(row()).toBeNull();
    await act(async () => refreshTodos());
    expect(row()).toBeNull();
    expect(todo.done_at).toBeUndefined();
    const undo = toast.mock.calls.find(([text]) => text === t("assistant-decisionrow.dismissed"))?.[2];
    expect(undo).toBeDefined();
    await act(async () => undo!.run());
    expect(row()).not.toBeNull();
    expect(posts.map((p) => p.path.split("/").at(-1))).toEqual(["decision-dismiss", "decision-undo-dismiss"]);
    toast.mockRestore();
  });

  it.each(["legacy", "structured"])("restores an optimistically hidden interrupted %s review only on failure", async (kind) => {
    if (kind === "legacy") ask = undefined;
    turnError = true;
    await mount();
    const toast = vi.spyOn(atoms, "toast");
    let finish!: () => void;
    postGate = new Promise<void>((resolve) => { finish = resolve; });
    const deferredKey = decisionLaterKey(todo);
    act(() => row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button")[1].click());
    expect(row()).toBeNull();
    status = 503;
    await act(async () => { finish(); await Promise.resolve(); });
    expect(row()).not.toBeNull();
    expect(getFeedState().later[deferredKey]).toBeFalsy();
    const dismiss = row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button")[1];
    expect(dismiss.disabled).toBe(false);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("The computer is offline"), "error");
    status = 200;
    await act(async () => dismiss.click());
    expect(row()).toBeNull();
    expect(posts).toHaveLength(2);
    toast.mockRestore();
  });

  it.each(["legacy", "structured"])("Later also hides an interrupted %s review, but not a new round", async (kind) => {
    if (kind === "legacy") ask = undefined;
    turnError = true;
    await mount();
    act(() => row().querySelector("[data-decision-actions]")!.querySelector<HTMLButtonElement>("button")!.click());
    expect(row()).toBeNull();
    await act(async () => refreshTodos());
    expect(row()).toBeNull();
    expect(posts).toEqual([]);
    const next = "2026-11-11T08:00:00.123456789Z";
    await act(async () => putTodo({ ...todo, supervision: { ...todo.supervision!, reviews: [{ at: next, verdict: "needs_user" }] }, decision_review: { review_at: next }, ask: ask && { ...ask, asked_at: next } }));
    expect(row()).not.toBeNull();
  });

  it("keeps a successful dismissal hidden even if its follow-up list read fails", async () => {
    turnError = true;
    await mount();
    listStatus = 503;
    await act(async () => row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button")[1].click());
    expect(row()).toBeNull();
    expect(getFeedState().later[decisionLaterKey(todo)]).toBeFalsy();
  });

  it("keeps real Agent input visible and finishes loading after dismissal succeeds", async () => {
    needsInput = true;
    await mount();
    await act(async () => row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button")[1].click());
    expect(row()).not.toBeNull();
    const dismiss = row().querySelector("[data-decision-actions]")!.querySelectorAll<HTMLButtonElement>("button")[1];
    expect(dismiss.disabled).toBe(false);
    expect(dismiss.querySelector("[role=status]")).toBeNull();
  });

  it("leaves an ask without options as it was: one Take button, no decision row", async () => {
    ask = { outcome: "decide", verdict: decision.verdict, recommend: "10% first", asked_at: at };
    await mount();
    expect(row().querySelector("[data-decision-row]")).toBeNull();
    expect(row().querySelector('[data-brief-row="accept"]')).not.toBeNull();
    expect(row().textContent).toContain(t("assistant-brief.take"));
    expect(row().querySelector("[data-decision-option]")).toBeNull();
  });

  it.each([
    { name: "GitHub PR", url: "https://github.com/nwverse/persona/pull/14659", label: "PR #14659" },
    { name: "GitLab MR", url: "https://gitlab.example.com/team/app/-/merge_requests/1468", label: "MR !1468" },
    { name: "GitHub Issue", url: "https://github.com/nwverse/persona/issues/14659", label: "Issue #14659" },
    { name: "generic external URL", url: "https://docs.example.com/specs/rollout", label: t("assistant-decisionrow.open_link_host", { host: "docs.example.com" }) },
  ])("renders a concise external evidence link for $name", async ({ url, label }) => {
    ask = { ...decision, link: url };
    await mount();
    const link = row().querySelector<HTMLAnchorElement>('[data-decision-link="external"]')!;
    expect(link).not.toBeNull();
    expect(link.href).toBe(url);
    expect(link.target).toBe("_blank");
    expect(link.rel).toBe("noopener noreferrer");
    expect(link.title).toBe(url);
    expect(link.textContent).toBe(label);
    expect(link.getAttribute("aria-label")).toBe(t("assistant-decisionrow.open_link_aria", { label }));
  });

  it("navigates safely in-app for a same-origin Persona link without answering the decision", async () => {
    const internal = `${location.origin}/s/demo/worker?view=chat`;
    ask = { ...decision, link: internal };
    await mount();
    const link = row().querySelector<HTMLAnchorElement>('[data-decision-link="internal"]')!;
    expect(link).not.toBeNull();
    expect(link.target).toBe("");
    expect(link.rel).toBe("");
    expect(link.textContent).toBe(t("assistant-decisionrow.open_link"));
    act(() => link.click());
    expect(opened()).toBe("/s/demo/worker?view=chat");
    expect(posts).toHaveLength(0);
  });

  it.each([
    { name: "same-origin self-link", url: `${location.origin}/todos/decision-task` },
    { name: "self-link with trailing slash", url: "https://hub.example.com/todos/decision-task/" },
    { name: "javascript scheme", url: "javascript:alert(1)" },
    { name: "data scheme", url: "data:text/html,hi" },
    { name: "non-URL text", url: "not-a-valid-url" },
  ])("omits invalid or redundant self-link: $name", async ({ url }) => {
    ask = { ...decision, link: url };
    await mount();
    expect(row().querySelector("[data-decision-link]")).toBeNull();
  });
});
