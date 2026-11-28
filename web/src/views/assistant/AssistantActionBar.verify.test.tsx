// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { getFeedState, loadFeed, type FeedItem, type TaskPlan } from "../../lib/feed";
import { loadPages } from "../../lib/lazy-page";
import { reset, refresh, refreshTodos } from "../../lib/store";
import type { Todo } from "../../lib/todos";
import type { Session } from "../../lib/types";
import { DirectionCountsText } from "../DirectionMarks";
import { AssistantActionBar } from "./Tasks";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
beforeAll(() => loadPages(), 60_000);

const at = "2026-11-25T13:00:00Z";
const now = new Date("2026-11-25T13:20:00Z");
const conversation = "main-chat";

let todos: Todo[];
let sessions: Session[];
let host: HTMLDivElement;
let root: Root;
let calls: { url: string; method: string }[];

beforeEach(async () => {
  await initI18n("en");
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(now);
  calls = [];
  todos = [];
  sessions = [];
  const plan: TaskPlan = { id: "p1", conversation, state: "started", created_at: at, tasks: [] };
  const feed: FeedItem[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = String(url);
    calls.push({ url: path, method: init?.method ?? "GET" });
    if (init?.method === "POST" || init?.method === "PATCH") {
      return new Response(JSON.stringify({ ok: true, todo: { ...(todos[0] ?? {}), done_at: now.toISOString() } }), { headers: { "Content-Type": "application/json" } });
    }
    const body = path.includes("/push/ln-mode") ? { enabled: false }
      : path.includes("/feed") ? { items: feed, plans: { [plan.id]: plan }, task_cards: [] }
      : path.includes("/todos") ? { todos }
      : path.includes("/sessions") ? { sessions }
      : path.includes("/interactions") ? { interactions: [] }
      : /\/assistant(\?|$)/.test(path) ? { machine_id: "hub", main: conversation, conversations: [] }
      : {};
    return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  vi.useRealTimers();
  act(() => root.unmount());
  host.remove();
  reset();
  delete getFeedState().feeds[conversation];
  delete getFeedState().taskCards[conversation];
  vi.unstubAllGlobals();
});

const entry = (key: string) => host.querySelector<HTMLButtonElement>(`[data-assistant-actionbar] [data-tray-entry="${key}"]`);

describe("To verify (user_check) visual layering and one-click Pass", () => {
  it("keeps user_check in calm To verify capsule with one-click Accept/Pass and leaves red Needs you at 0", async () => {
    todos = [
      {
        id: "uc-ask",
        title: "Read-only investigation A",
        origin: conversation,
        created_at: at,
        updated_at: at,
        agents: [{ machine_id: "m", session_id: "s1", linked_at: at }],
        supervision: { supervisor: "lead-a", state: "user_check", reviews: [{ at, verdict: "accepted", note: "Findings ready" }] },
        ask: { outcome: "done", verdict: "Findings ready", asked_at: at },
        view: { phase: "ready", section: "just_finished", reason: "finished_unseen", rank: 0 },
      },
      {
        id: "uc-no-ask",
        title: "Read-only investigation B",
        origin: conversation,
        created_at: at,
        updated_at: at,
        agents: [{ machine_id: "m", session_id: "s1", linked_at: at }],
        supervision: { supervisor: "lead-a", state: "user_check", reviews: [{ at, verdict: "accepted", note: "Summary verified" }] },
        view: { phase: "ready", section: "just_finished", reason: "finished_unseen", rank: 0 },
      },
      {
        id: "sup-503",
        title: "Supervised 503 task",
        origin: conversation,
        created_at: at,
        updated_at: at,
        agents: [{ machine_id: "m", session_id: "s-err", linked_at: at }],
        supervision: { supervisor: "lead-a", state: "", reviews: [] },
        view: { phase: "working", section: "working", reason: "persona_working", rank: 0 },
      },
    ];
    sessions = [
      { machine_id: "m", session_id: "s1", title: "Done run", cwd: "/app", state: "idle", status: "idle", updated_at: at },
      { machine_id: "m", session_id: "s-err", title: "503 run", cwd: "/app", state: "idle", status: "idle", updated_at: at, turn_error: "503 Service Unavailable", turn_error_kind: "transient" },
    ];

    await act(async () => {
      await refresh();
      await loadFeed(conversation);
      await refreshTodos();
    });
    act(() => root.render(<StrictMode><AssistantActionBar conversation={conversation} /></StrictMode>));

    // Red Needs you has 0 items; To verify has 2 items in calm non-attention styling.
    expect(entry("needs")?.textContent).toBe(t("assistant-tasks.tray_needs_you"));
    const verifyBtn = entry("verify");
    expect(verifyBtn).not.toBeNull();
    expect(verifyBtn!.textContent).toBe(`${t("assistant-tasks.tray_verify")}2`);
    expect(verifyBtn!.querySelector(".bg-attention")).toBeNull();
    expect(verifyBtn!.querySelector(".bg-orange")).not.toBeNull();

    // Opening To verify shows both rows cleanly without red attention text, and expanding exposes one-click actions.
    act(() => verifyBtn!.click());
    const tray = host.querySelector("[data-tray-verify]")!;
    expect(tray).not.toBeNull();
    expect(tray.querySelector(".text-attention")).toBeNull();
    const rows = [...tray.querySelectorAll<HTMLButtonElement>("button[data-needs-row]")];
    expect(rows).toHaveLength(2);

    // Expand uc-ask -> AcceptRow one-click action. Current main loads the acceptance card through the lazy
    // NeedsYouPart (TaskDecision) chunk, so resolve it and poll until the accept row commits.
    await act(async () => {
      rows[0].click();
      await import("./TaskDecision");
    });
    await expect.poll(() => tray.querySelectorAll('[data-brief-row="accept"]')).toHaveLength(1);

    // Expand uc-no-ask -> VerifyPassRow one-click Pass button
    await act(async () => { rows[1].click(); });
    const passBtn = [...tray.querySelectorAll<HTMLButtonElement>('[data-verify-pass-row="uc-no-ask"] button')].find((b) => b.textContent === t("assistant-tasks.verify_pass"));
    expect(passBtn).toBeTruthy();
    await act(async () => passBtn!.click());
    expect(calls.some((c) => c.method === "PATCH" && c.url.includes("/todos/uc-no-ask"))).toBe(true);
  });

  it("renders DirectionCountsText with red Needs you and calm secondary To verify separately", () => {
    act(() => root.render(
      <div>
        <div data-case="verify-only"><DirectionCountsText direction={{ running: false, counts: { working: 1, needs_you: 0, stuck: 0, review: 6, user_check: 6 } }} /></div>
        <div data-case="mixed"><DirectionCountsText direction={{ running: true, counts: { working: 1, needs_you: 1, stuck: 0, review: 6, user_check: 6 } }} /></div>
      </div>,
    ));

    const verifyOnly = host.querySelector('[data-case="verify-only"]')!;
    expect(verifyOnly.textContent).toContain(t("sidebar.leader_user_check", { count: 6 }));
    expect(verifyOnly.textContent).not.toContain(t("sidebar.leader_needs_you", { count: 1 }));
    expect(verifyOnly.querySelector(".text-attention")).toBeNull();

    const mixed = host.querySelector('[data-case="mixed"]')!;
    expect(mixed.querySelector(".text-attention")?.textContent).toBe(t("sidebar.leader_needs_you", { count: 1 }));
    expect(mixed.querySelector("[data-direction-user-check]")?.textContent).toBe(t("sidebar.leader_user_check", { count: 6 }));
  });
});
