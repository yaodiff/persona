// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => vi.fn());
const toast = vi.hoisted(() => vi.fn());
const events = vi.hoisted(() => ({} as Record<string, ((event: Record<string, unknown>) => void)[]>));
vi.mock("../../lib/store", async (orig) => ({ ...(await orig<typeof import("../../lib/store")>()), onHubEvent: (type: string, fn: (event: Record<string, unknown>) => void) => {
  (events[type] ??= []).push(fn);
  return () => { events[type] = events[type].filter((cb) => cb !== fn); };
} }));
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast }));
import { ApiError } from "../../lib/api";
import { assistantEntries, messageWaits } from "../../lib/assistant-entries";
import { cardDismissed, dismissMessage, getFeedState, later, loadFeed, sendMessage, undoDismissMessage, useFeed, type FeedItem } from "../../lib/feed";
import { chooseManager, noteConversation, resetManagerChoice } from "../../lib/manager-choice";
import { reset } from "../../lib/store";
import { FeedLineView } from "./Tasks";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
const BASE_DRAFT: FeedItem = { id: "demo-draft", conversation: "demo-main", at: "2026-11-10T08:00:00Z", kind: "message", ref: "demo-inbox", body: { draft: "The synthetic checks are ready.", text: "Are the checks ready?" } };
let draft: FeedItem;
let dismissed: FeedItem;
const push = (item: FeedItem, manager = "assistant") => events.assistant_feed.forEach((fn) => fn({ item, manager }));
const needs = () => assistantEntries({ conversation: draft.conversation, todos: [], sessions: [], interactions: [], plans: {}, feed: getFeedState().feeds[draft.conversation] ?? [], deferred: getFeedState().later }).needsYou;
const button = (name: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === name)!;
function Pending() {
  const feed = useFeed((s) => s.feeds[draft.conversation] ?? []);
  return <>{feed.filter((it) => messageWaits(it)).map((item) => <FeedLineView key={item.id} item={item} />)}</>;
}

beforeEach(() => {
  draft = { ...BASE_DRAFT, body: { ...BASE_DRAFT.body } };
  dismissed = { ...draft, body: { ...draft.body, dismissed_at: "2026-11-10T08:01:00Z", dismissal_id: "demo-receipt" } };
  api.mockReset();
  toast.mockReset();
  reset();
  resetManagerChoice();
  Object.assign(getFeedState(), { feeds: { [draft.conversation]: [draft] }, later: {}, busy: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetManagerChoice();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("dismiss a task-progress reply draft", () => {
  it("offers one primary Send and secondary Later / Don't handle, with Undo", async () => {
    api.mockResolvedValueOnce({ item: dismissed, dismissal_id: "demo-receipt" });
    await act(async () => {
      root.render(<Pending />);
      await import("./MessageDraft");
    });
    await vi.waitFor(async () => {
      await act(async () => {});
      expect(button("发送")).toBeDefined();
    });
    expect(button("发送").className).toContain("bg-accent");
    expect(button("稍后").className).not.toContain("bg-accent");
    expect(button("不处理").className).not.toContain("bg-accent");
    await act(async () => button("不处理").click());
    expect(api.mock.calls).toEqual([["/assistant/feed/demo-draft/dismiss", { method: "POST", json: { draft: draft.body!.draft }, headers: { "X-Persona-Manager": "assistant" } }]]);
    expect(needs()).toHaveLength(0);
    expect(host.querySelector("textarea")).toBeNull();
    expect(toast).toHaveBeenCalledWith("已设为不处理，草稿未发送", "ok", expect.objectContaining({ label: "撤销" }));
    const undo = toast.mock.calls[0][2];
    api.mockResolvedValueOnce({ item: draft });
    await act(async () => undo.run());
    expect(api.mock.calls[1]).toEqual(["/assistant/feed/demo-draft/undo-dismiss", { method: "POST", json: { dismissal_id: "demo-receipt" }, headers: { "X-Persona-Manager": "assistant" } }]);
    expect(host.querySelector("textarea")).not.toBeNull();
    expect(needs()).toHaveLength(1);
    expect(cardDismissed(getFeedState().feeds[draft.conversation][0])).toBeUndefined();
  });

  it("shares synchronous admission with Send and never sends a dismissed stale card", async () => {
    let finish!: (value: unknown) => void;
    api.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    const first = dismissMessage(draft);
    await Promise.all([sendMessage(draft, "Do not send this"), dismissMessage(draft)]);
    expect(api).toHaveBeenCalledTimes(1);
    expect(getFeedState().busy[draft.id]).toBe(true);
    finish({ item: dismissed, dismissal_id: "demo-receipt" });
    await first;
    await sendMessage(draft, "Do not send this either");
    expect(api).toHaveBeenCalledTimes(1);
    expect(getFeedState().busy[draft.id]).toBe(false);
    expect(cardDismissed(getFeedState().feeds[draft.conversation][0])).toEqual({ at: "2026-11-10T08:01:00Z", id: "demo-receipt" });
  });

  it("does not dismiss while Send is admitted", async () => {
    let finish!: (value: unknown) => void;
    api.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    const sending = sendMessage(draft, "Send this once");
    expect(await dismissMessage(draft)).toBeUndefined();
    expect(api).toHaveBeenCalledTimes(1);
    finish({});
    await sending;
    expect(api.mock.calls[0][0]).toContain("/reply");
  });

  it("keeps the card on failure and permits retry without modifying the task", async () => {
    api.mockRejectedValueOnce(new ApiError(409, "Synthetic stale draft", {}));
    await act(async () => {
      root.render(<Pending />);
      await import("./MessageDraft");
    });
    await act(async () => button("不处理").click());
    expect(needs()).toHaveLength(1);
    expect(button("不处理").disabled).toBe(false);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic stale draft"), "error");
    expect(getFeedState().feeds[draft.conversation][0]).toEqual(draft);
    api.mockResolvedValueOnce({ item: dismissed, dismissal_id: "demo-receipt" });
    await act(async () => button("不处理").click());
    expect(needs()).toHaveLength(0);
    expect(api.mock.calls.every(([path]) => path.endsWith("/dismiss"))).toBe(true);
  });

  it("keeps a dismissed card gone after reload while new same-ref progress is eligible", async () => {
    Object.assign(getFeedState(), { feeds: {} });
    const next: FeedItem = { ...draft, id: "demo-new-progress", at: "2026-11-10T08:02:00Z", body: { ...draft.body, draft: "New progress is ready." } };
    api.mockResolvedValueOnce({ items: [dismissed, next] });
    await loadFeed(draft.conversation);
    expect(needs().map((it) => it.key)).toEqual(["message:demo-new-progress"]);
    later(dismissed.id);
    expect(needs().map((it) => it.key)).toEqual(["message:demo-new-progress"]);
  });

  it("routes Dismiss and Undo to the card's manager even after selecting another assistant", async () => {
    noteConversation(draft.conversation, "demo-owner");
    chooseManager("demo-other");
    api.mockImplementation(async (path: string) => path.endsWith("/undo-dismiss") ? { item: draft } : path.endsWith("/dismiss") ? { item: dismissed, dismissal_id: "demo-receipt" } : {});
    await dismissMessage(draft);
    await undoDismissMessage(draft, "demo-receipt");
    expect(api.mock.calls.filter(([path]) => path.startsWith("/assistant/feed/")).map(([, opts]) => opts.headers)).toEqual([{ "X-Persona-Manager": "demo-owner" }, { "X-Persona-Manager": "demo-owner" }]);
  });

  it("Later hides only this card locally, not a later draft for the same Inbox reference", () => {
    const next = { ...draft, id: "demo-next-local", body: { ...draft.body, draft: "A later progress draft" } };
    Object.assign(getFeedState(), { feeds: { [draft.conversation]: [draft, next] } });
    later(draft.id);
    expect(needs().map((it) => it.key)).toEqual(["message:demo-next-local"]);
    expect(cardDismissed(draft)).toBeUndefined();
    expect(api).not.toHaveBeenCalled();
  });

  it("renders a persisted dismissal as not sent, without an editor or actions", async () => {
    await act(async () => root.render(<FeedLineView item={dismissed} />));
    expect(host.textContent).toContain("已设为不处理，草稿未发送");
    expect(host.querySelector("textarea")).toBeNull();
    expect(host.querySelector("button")).toBeNull();
    expect(messageWaits(dismissed)).toBe(false);
  });

  it.each([false, true])("a delayed feed read cannot roll back an accepted decision (Undo=%s)", async (undo) => {
    let finish!: (value: unknown) => void;
    const initial = undo ? dismissed : draft;
    Object.assign(getFeedState(), { feeds: { [draft.conversation]: [initial] } });
    api.mockImplementation((path: string) => path.includes("/feed?") ? new Promise((resolve) => { finish = resolve; }) : Promise.resolve(undo ? { item: draft } : { item: dismissed, dismissal_id: "demo-receipt" }));
    const reading = loadFeed(draft.conversation);
    await Promise.resolve();
    if (undo) await undoDismissMessage(initial, "demo-receipt");
    else await dismissMessage(initial);
    finish({ items: [initial] });
    await reading;
    expect(needs()).toHaveLength(undo ? 1 : 0);
    expect(Boolean(cardDismissed(getFeedState().feeds[draft.conversation][0]))).toBe(!undo);
  });

  it("a late Undo response cannot erase a newer sent receipt", async () => {
    Object.assign(getFeedState(), { feeds: { [draft.conversation]: [dismissed] } });
    let finish!: (value: unknown) => void;
    api.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    const restoring = undoDismissMessage(dismissed, "demo-receipt");
    const sent = { ...draft, body: { ...draft.body, sent_at: "2026-11-10T08:03:00Z", sent: "Sent from another client" } };
    push(sent);
    finish({ item: draft });
    expect(await restoring).toBe(false);
    expect(getFeedState().feeds[draft.conversation][0]).toEqual(sent);
    expect(needs()).toHaveLength(0);
  });

  it("a late Dismiss response cannot erase a newer filed receipt", async () => {
    let finish!: (value: unknown) => void;
    api.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    const hiding = dismissMessage(draft);
    const filed = { ...draft, body: { ...draft.body, filed_at: "2026-11-10T08:03:00Z" } };
    push(filed);
    finish({ item: dismissed, dismissal_id: "demo-receipt" });
    await hiding;
    expect(getFeedState().feeds[draft.conversation][0]).toEqual(filed);
    expect(needs()).toHaveLength(0);
  });

  it("names the default owner explicitly through the real REST transport after a manager switch", async () => {
    noteConversation(draft.conversation, "assistant");
    chooseManager("demo-other");
    api.mockImplementation(async (path: string) => path.endsWith("/dismiss") ? { item: dismissed, dismissal_id: "demo-receipt" } : {});
    await dismissMessage(draft);
    const [path, opts] = api.mock.calls.find(([path]) => path.endsWith("/dismiss"))!;
    const fetcher = vi.fn(async (_url: string, _init?: RequestInit) => ({ ok: true, status: 200, headers: new Headers(), text: async () => JSON.stringify({ item: dismissed, dismissal_id: "demo-receipt" }) }));
    vi.stubGlobal("fetch", fetcher);
    const real = await vi.importActual<typeof import("../../lib/api")>("../../lib/api");
    await real.api(path, opts);
    expect(new Headers(fetcher.mock.calls[0][1]?.headers).get("X-Persona-Manager")).toBe("assistant");
  });

  it("captures a WebSocket-only card's manager before selection changes", async () => {
    push({ ...draft, body: { ...draft.body } }, "demo-realtime-owner");
    chooseManager("demo-other");
    api.mockImplementation(async (path: string) => path.endsWith("/undo-dismiss") ? { item: draft } : path.endsWith("/dismiss") ? { item: dismissed, dismissal_id: "demo-receipt" } : {});
    await dismissMessage(draft);
    await undoDismissMessage(draft, "demo-receipt");
    expect(api.mock.calls.filter(([path]) => path.startsWith("/assistant/feed/")).map(([, opts]) => opts.headers)).toEqual([{ "X-Persona-Manager": "demo-realtime-owner" }, { "X-Persona-Manager": "demo-realtime-owner" }]);
  });

  it("keeps a newer pending card when Undo is refused; no optimistic resurrection", async () => {
    Object.assign(getFeedState(), { feeds: { [draft.conversation]: [dismissed] } });
    api.mockRejectedValueOnce(new ApiError(409, "Synthetic newer progress", {}));
    await expect(undoDismissMessage(draft, "demo-receipt")).rejects.toThrow("Synthetic newer progress");
    expect(needs()).toHaveLength(0);
    expect(getFeedState().feeds[draft.conversation][0]).toEqual(dismissed);
    expect(getFeedState().busy[draft.id]).toBe(false);
  });
});
