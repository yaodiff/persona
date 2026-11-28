// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import type { SharedBlocker } from "../lib/todo-shared-blockers";
import { SharedBlockersStrip } from "./SharedBlockersStrip";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  await initI18n("zh-Hans");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("SharedBlockersStrip (#14728, #17938)", () => {
  const blockers: SharedBlocker[] = [
    {
      key: "todo:todo_e8acf635da201b7a",
      kind: "todo",
      todo_id: "todo_e8acf635da201b7a",
      label: "治理 Steam 扩展全链路",
      todo_ids: ["t1", "t2"],
      count: 2,
    },
    {
      key: "pull_request:https://github.com/nwverse/persona/pull/1446",
      kind: "pull_request",
      label: "nwverse/persona#1446",
      url: "https://github.com/nwverse/persona/pull/1446",
      todo_ids: ["t2", "t3"],
      count: 2,
    },
  ];

  it("renders updated '公共卡点' heading in zh-Hans, readable internal todo blockers, and separate open-todo action", async () => {
    const onSelect = vi.fn();
    const onOpenTodo = vi.fn();
    await act(async () => {
      root.render(
        <SharedBlockersStrip
          blockers={blockers}
          selectedKey={null}
          onSelect={onSelect}
          onOpenTodo={onOpenTodo}
          openableTodoIds={new Set(["todo_e8acf635da201b7a", "t1", "t2", "t3"])}
        />,
      );
    });

    const region = host.querySelector<HTMLElement>("[data-testid=todos-shared-blockers]")!;
    expect(region.getAttribute("aria-label")).toBe("公共卡点");
    expect(region.textContent).toContain("公共卡点 · 2");
    expect(region.textContent).not.toContain("公共外部卡点");
    expect(region.textContent).toContain("治理 Steam 扩展全链路");
    expect(region.textContent).not.toContain("todo_e8acf635da201b7a");

    // Clicking the chip filters; clicking the sibling open button opens the prerequisite todo
    const chips = [...host.querySelectorAll<HTMLButtonElement>("[data-testid=shared-blocker-chip]")];
    expect(chips).toHaveLength(2);
    expect(chips[0].dataset.blockerKind).toBe("todo");
    await act(async () => chips[0].click());
    expect(onSelect).toHaveBeenCalledWith("todo:todo_e8acf635da201b7a");

    const openTodoBtn = host.querySelector<HTMLButtonElement>("[data-testid=shared-blocker-open-todo]")!;
    expect(openTodoBtn).toBeTruthy();
    expect(openTodoBtn.closest("[data-testid=shared-blocker-chip]")).toBeNull();
    await act(async () => openTodoBtn.click());
    expect(onOpenTodo).toHaveBeenCalledWith("todo_e8acf635da201b7a");
  });

  it("omits open-todo button when the prerequisite todo is not in openableTodoIds, and renders en/ja titles cleanly", async () => {
    await initI18n("ja");
    await act(async () => {
      root.render(
        <SharedBlockersStrip
          blockers={[blockers[0]]}
          selectedKey={null}
          onSelect={() => {}}
          onOpenTodo={() => {}}
          openableTodoIds={new Set(["t1", "t2"])}
        />,
      );
    });
    expect(host.textContent).toContain(t("todos.shared_blockers_title", { count: 1 }));
    expect(host.querySelector("[data-testid=shared-blocker-open-todo]")).toBeNull();
  });
});
