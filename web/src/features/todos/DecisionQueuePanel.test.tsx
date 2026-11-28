// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo, TodoDecisionBatchItem, TodoDecisionBatchResponse } from "../../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const apiMock = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: unknown) => apiMock(path, init),
}));

const { initI18n } = await import("../../i18n");
const { submitTodoDecisionsBatch } = await import("../../lib/todos");
const { DecisionQueuePanel, decisionQueueCandidates } = await import("./DecisionQueuePanel");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = "2026-11-26T00:00:00Z";

function makeDecisionTodo(id: string, title: string, opts: { recommend?: string; options?: string[]; verdict?: string; question?: string } = {}): Todo {
  return {
    id,
    title,
    created_at: NOW,
    updated_at: NOW,
    ask: {
      outcome: "decide",
      verdict: opts.verdict ?? `Decide on ${title}`,
      question: opts.question ?? `Which approach for ${title}?`,
      recommend: opts.recommend,
      asked_at: NOW,
      options: (opts.options ?? ["Approve", "Reject"]).map((label) => ({ label })),
    },
  };
}

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

beforeEach(async () => {
  await initI18n("en");
  apiMock.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("DecisionQueuePanel (#15757)", () => {
  it("filters only open todos with active decide asks", () => {
    const items: Todo[] = [
      makeDecisionTodo("d1", "Ship v2"),
      { ...makeDecisionTodo("d2", "Already done"), done_at: NOW },
      {
        id: "d3",
        title: "Verify deploy",
        created_at: NOW,
        updated_at: NOW,
        ask: { outcome: "done", verdict: "Deployed", asked_at: NOW },
      },
      makeDecisionTodo("d4", "Pick color"),
    ];
    expect(decisionQueueCandidates(items).map((td) => td.id)).toEqual(["d1", "d4"]);
  });

  it("never pre-selects or auto-picks recommended options on open", async () => {
    const items = [
      makeDecisionTodo("t1", "Release PR", { recommend: "Ship now", options: ["Ship now", "Hold"] }),
      makeDecisionTodo("t2", "Database migration", { recommend: "Run online", options: ["Run online", "Schedule window"] }),
    ];
    const onSubmitBatch = vi.fn(async (): Promise<TodoDecisionBatchResponse> => ({
      results: [],
      succeeded: 0,
      failed: 0,
    }));

    await act(async () => {
      root.render(
        <DecisionQueuePanel open items={items} onClose={() => {}} onSubmitBatch={onSubmitBatch} />,
      );
    });
    await flush();

    // Recommended badges are shown as visual cues only; every option starts aria-pressed="false".
    const optionButtons = [...document.querySelectorAll<HTMLButtonElement>('[data-testid="decision-queue-option"]')];
    expect(optionButtons.length).toBe(4);
    for (const btn of optionButtons) {
      expect(btn.getAttribute("aria-pressed")).toBe("false");
    }
    expect(document.querySelectorAll('[data-testid="decision-queue-recommended-badge"]').length).toBe(2);

    // Ready count starts at 0 / 2 and submit button is disabled.
    expect(document.querySelector('[data-testid="decision-queue-ready-count"]')?.textContent).toContain("0 / 2");
    const submitBtn = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-submit"]');
    expect(submitBtn?.disabled).toBe(true);
    expect(onSubmitBatch).not.toHaveBeenCalled();
  });

  it("submits distinct per-item explicit options (Mode A) and supports toggling off", async () => {
    const items = [
      makeDecisionTodo("t1", "Release PR", { recommend: "Ship now", options: ["Ship now", "Hold"] }),
      makeDecisionTodo("t2", "Database migration", { options: ["Run online", "Schedule window"] }),
    ];
    const onRefresh = vi.fn();
    const onSubmitBatch = vi.fn(async (batch: readonly TodoDecisionBatchItem[]): Promise<TodoDecisionBatchResponse> => ({
      results: batch.map((b) => ({ todo_id: b.todo_id, ok: true })),
      succeeded: batch.length,
      failed: 0,
    }));

    await act(async () => {
      root.render(
        <DecisionQueuePanel open items={items} onClose={() => {}} onRefresh={onRefresh} onSubmitBatch={onSubmitBatch} />,
      );
    });
    await flush();

    const optHold = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-option"][data-todo-id="t1"][data-option="Hold"]')!;
    const optOnline = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-option"][data-todo-id="t2"][data-option="Run online"]')!;

    // Click and toggle off t1, then click again.
    await act(async () => optHold.click());
    expect(optHold.getAttribute("aria-pressed")).toBe("true");
    await act(async () => optHold.click());
    expect(optHold.getAttribute("aria-pressed")).toBe("false");
    await act(async () => optHold.click());
    await act(async () => optOnline.click());
    await flush();

    expect(document.querySelector('[data-testid="decision-queue-ready-count"]')?.textContent).toContain("2 / 2");
    const submitBtn = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-submit"]')!;
    expect(submitBtn.disabled).toBe(false);

    await act(async () => submitBtn.click());
    await flush();

    expect(onSubmitBatch).toHaveBeenCalledTimes(1);
    expect(onSubmitBatch).toHaveBeenCalledWith([
      { todo_id: "t1", option: "Hold" },
      { todo_id: "t2", option: "Run online" },
    ]);
    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(document.querySelectorAll('[data-testid="decision-queue-item"][data-settled="true"]').length).toBe(2);
  });

  it("supports multi-select shared custom reply (Mode B) both via apply button and direct submit", async () => {
    const items = [
      makeDecisionTodo("t1", "PR A", { options: ["Yes", "No"] }),
      makeDecisionTodo("t2", "PR B", { options: ["Yes", "No"] }),
      makeDecisionTodo("t3", "PR C", { options: ["Fast", "Safe"] }),
    ];
    const onSubmitBatch = vi.fn(async (batch: readonly TodoDecisionBatchItem[]): Promise<TodoDecisionBatchResponse> => ({
      results: batch.map((b) => ({ todo_id: b.todo_id, ok: true })),
      succeeded: batch.length,
      failed: 0,
    }));

    await act(async () => {
      root.render(
        <DecisionQueuePanel open items={items} onClose={() => {}} onSubmitBatch={onSubmitBatch} />,
      );
    });
    await flush();

    // Mode A on t3
    const optSafe = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-option"][data-todo-id="t3"][data-option="Safe"]')!;
    await act(async () => optSafe.click());

    // Mode B: check t1 and t2, type shared reply, and submit directly without clicking Apply first
    const check1 = document.querySelector<HTMLInputElement>('[data-testid="decision-queue-check"][data-todo-id="t1"]')!;
    const check2 = document.querySelector<HTMLInputElement>('[data-testid="decision-queue-check"][data-todo-id="t2"]')!;
    await act(async () => check1.click());
    await act(async () => check2.click());

    const input = document.querySelector<HTMLInputElement>('[data-testid="decision-queue-shared-reply"]')!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "Wait until Friday freeze ends");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush();

    expect(document.querySelector('[data-testid="decision-queue-ready-count"]')?.textContent).toContain("3 / 3");
    const submitBtn = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-submit"]')!;
    await act(async () => submitBtn.click());
    await flush();

    expect(onSubmitBatch).toHaveBeenCalledWith([
      { todo_id: "t1", reply: "Wait until Friday freeze ends" },
      { todo_id: "t2", reply: "Wait until Friday freeze ends" },
      { todo_id: "t3", option: "Safe" },
    ]);
  });

  it("handles partial failure by settling succeeded items and preserving failed items with server error", async () => {
    const items = [
      makeDecisionTodo("t1", "Task 1", { options: ["Approve", "Reject"] }),
      makeDecisionTodo("t2", "Task 2", { options: ["Approve", "Reject"] }),
    ];
    const onRefresh = vi.fn();
    const onSubmitBatch = vi.fn(async (): Promise<TodoDecisionBatchResponse> => ({
      results: [
        { todo_id: "t1", ok: true },
        { todo_id: "t2", ok: false, status: 409, error: "decision round is stale" },
      ],
      succeeded: 1,
      failed: 1,
    }));

    await act(async () => {
      root.render(
        <DecisionQueuePanel open items={items} onClose={() => {}} onRefresh={onRefresh} onSubmitBatch={onSubmitBatch} />,
      );
    });
    await flush();

    const opt1 = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-option"][data-todo-id="t1"][data-option="Approve"]')!;
    const opt2 = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-option"][data-todo-id="t2"][data-option="Reject"]')!;
    await act(async () => opt1.click());
    await act(async () => opt2.click());
    await flush();

    await act(async () => document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-submit"]')!.click());
    await flush();

    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[data-testid="decision-queue-item"][data-todo-id="t1"]')?.getAttribute("data-settled")).toBe("true");
    const failedRow = document.querySelector('[data-testid="decision-queue-item"][data-todo-id="t2"]')!;
    expect(failedRow.getAttribute("data-settled")).toBeNull();
    expect(failedRow.querySelector('[data-testid="decision-queue-item-error"]')?.textContent).toContain("decision round is stale");
    expect(document.querySelector('[data-testid="decision-queue-summary"]')?.textContent).toContain("1");
  });

  it("posts structured batch items to POST /todos/decisions/batch via submitTodoDecisionsBatch", async () => {
    apiMock.mockResolvedValueOnce({
      results: [{ todo_id: "t1", ok: true }],
      succeeded: 1,
      failed: 0,
    });
    const res = await submitTodoDecisionsBatch([{ todo_id: "t1", option: "Approve" }]);
    expect(apiMock).toHaveBeenCalledWith(
      "/todos/decisions/batch",
      expect.objectContaining({
        method: "POST",
        json: { items: [{ todo_id: "t1", option: "Approve" }] },
      }),
    );
    expect(res).toEqual({
      results: [{ todo_id: "t1", ok: true }],
      succeeded: 1,
      failed: 0,
    });
  });

  it("clusters decisions by server same-cause groups with group-select checkbox while preserving Mode A and Mode B (#16433)", async () => {
    const items = [
      makeDecisionTodo("t1", "Fix auth", { options: ["Proceed", "Hold"] }),
      makeDecisionTodo("t2", "Fix login", { options: ["Proceed", "Hold"] }),
      makeDecisionTodo("t3", "Update docs", { options: ["Merge", "Skip"] }),
    ];
    const onFetchGroups = vi.fn(async () => ({
      groups: [
        {
          key: "blocker:pr-99",
          cause_kind: "shared_blocker",
          label: "org/repo#99",
          url: "https://github.com/org/repo/pull/99",
          todo_ids: ["t1", "t2"],
          count: 2,
        },
        {
          key: "standalone:t3",
          cause_kind: "standalone",
          label: "Update docs",
          url: "https://user:pw@evil.example/pr/3",
          todo_ids: ["t3"],
          count: 1,
        },
      ],
      total_pending: 3,
    }));
    const onSubmitBatch = vi.fn(async (batch: readonly TodoDecisionBatchItem[]): Promise<TodoDecisionBatchResponse> => ({
      results: batch.map((b) => ({ todo_id: b.todo_id, ok: true })),
      succeeded: batch.length,
      failed: 0,
    }));

    await act(async () => {
      root.render(
        <DecisionQueuePanel
          open
          items={items}
          onClose={() => {}}
          onFetchGroups={onFetchGroups}
          onSubmitBatch={onSubmitBatch}
        />,
      );
    });
    await flush();

    expect(onFetchGroups).toHaveBeenCalledTimes(1);
    const groups = [...document.querySelectorAll('[data-testid="decision-queue-group"]')];
    expect(groups.length).toBe(2);
    expect(groups[0].getAttribute("data-cause-kind")).toBe("shared_blocker");
    expect(groups[0].querySelector('[data-testid="decision-queue-group-link"]')?.getAttribute("href")).toBe(
      "https://github.com/org/repo/pull/99",
    );
    expect(groups[1].querySelector('[data-testid="decision-queue-group-link"]')).toBeNull();
    expect(groups[0].querySelector('[data-testid="decision-queue-group-count"]')?.textContent).toBe("2");

    // Select the shared_blocker group via its group checkbox (Mode B) and pick an explicit option on t3 (Mode A).
    const groupCheck = document.querySelector<HTMLInputElement>(
      '[data-testid="decision-queue-group-select"][data-group-key="blocker:pr-99"]',
    )!;
    await act(async () => groupCheck.click());
    expect(document.querySelector<HTMLInputElement>('[data-testid="decision-queue-check"][data-todo-id="t1"]')?.checked).toBe(true);
    expect(document.querySelector<HTMLInputElement>('[data-testid="decision-queue-check"][data-todo-id="t2"]')?.checked).toBe(true);
    expect(document.querySelector<HTMLInputElement>('[data-testid="decision-queue-check"][data-todo-id="t3"]')?.checked).toBe(false);

    const optMerge = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-option"][data-todo-id="t3"][data-option="Merge"]')!;
    await act(async () => optMerge.click());

    const input = document.querySelector<HTMLInputElement>('[data-testid="decision-queue-shared-reply"]')!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "Wait for #99");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush();

    await act(async () => document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-submit"]')!.click());
    await flush();

    expect(onSubmitBatch).toHaveBeenCalledWith([
      { todo_id: "t1", reply: "Wait for #99" },
      { todo_id: "t2", reply: "Wait for #99" },
      { todo_id: "t3", option: "Merge" },
    ]);
  });

  it("gracefully falls back to flat list when GET /todos/decisions/groups fails (#16433)", async () => {
    const items = [makeDecisionTodo("t1", "PR A"), makeDecisionTodo("t2", "PR B")];
    const onFetchGroups = vi.fn(async () => {
      throw new Error("409 todos_v2_disabled");
    });

    await act(async () => {
      root.render(
        <DecisionQueuePanel open items={items} onClose={() => {}} onFetchGroups={onFetchGroups} />,
      );
    });
    await flush();

    expect(document.querySelectorAll('[data-testid="decision-queue-group"]').length).toBe(0);
    expect(document.querySelectorAll('[data-testid="decision-queue-item"]').length).toBe(2);
  });
});
