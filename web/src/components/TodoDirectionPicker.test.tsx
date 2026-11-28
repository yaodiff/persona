// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TodoDirectionPicker } from "./TodoDirectionPicker";
import { t } from "../i18n";
import type { ReorganizeDirectionOption, TodoReorganizeRequest } from "../lib/todo-reorganize";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const DIRECTIONS: readonly ReorganizeDirectionOption[] = [
  { id: "dir-platform", label: "Platform", active: true },
  { id: "dir-growth", label: "Growth", active: true },
];

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("TodoDirectionPicker (#16138)", () => {
  it("defaults include_children to true with subtask count when childCount > 0 and submits include_children=true", async () => {
    const onApply = vi.fn(async (_req: TodoReorganizeRequest) => {});
    const onClose = vi.fn();

    await act(async () => {
      root.render(
        <TodoDirectionPicker
          todoId="goal_1"
          currentDirectionId="dir-platform"
          currentDirectionLabel="Platform"
          childCount={3}
          directions={DIRECTIONS}
          onApply={onApply}
          onClose={onClose}
        />,
      );
    });
    await flush();

    const checkbox = host.querySelector<HTMLInputElement>("[data-testid=todo-direction-include-children]")!;
    expect(checkbox).toBeTruthy();
    expect(checkbox.checked).toBe(true);
    expect(checkbox.closest("label")?.textContent).toContain(t("todos.direction_include_children", { count: 3 }));

    const select = host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-direction-select]")!;
    await act(async () => {
      select.value = "dir-growth";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();

    await act(async () => {
      host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-direction-apply]")!.click();
    });
    await flush();

    expect(onApply).toHaveBeenCalledWith({
      ids: ["goal_1"],
      direction_id: "dir-growth",
      include_children: true,
      transfer_supervision: false,
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("allows unchecking include_children to move only the parent goal", async () => {
    const onApply = vi.fn(async (_req: TodoReorganizeRequest) => {});
    const onClose = vi.fn();

    await act(async () => {
      root.render(
        <TodoDirectionPicker
          todoId="goal_1"
          currentDirectionId="dir-platform"
          currentDirectionLabel="Platform"
          childCount={2}
          directions={DIRECTIONS}
          onApply={onApply}
          onClose={onClose}
        />,
      );
    });
    await flush();

    const checkbox = host.querySelector<HTMLInputElement>("[data-testid=todo-direction-include-children]")!;
    expect(checkbox.checked).toBe(true);

    await act(async () => checkbox.click());
    await flush();
    expect(checkbox.checked).toBe(false);

    const select = host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-direction-select]")!;
    await act(async () => {
      select.value = "dir-growth";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();

    await act(async () => {
      host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-direction-apply]")!.click();
    });
    await flush();

    expect(onApply).toHaveBeenCalledWith({
      ids: ["goal_1"],
      direction_id: "dir-growth",
      include_children: false,
      transfer_supervision: false,
    });
  });

  it("omits include_children checkbox when childCount is 0 and sends include_children=false", async () => {
    const onApply = vi.fn(async (_req: TodoReorganizeRequest) => {});
    const onClose = vi.fn();

    await act(async () => {
      root.render(
        <TodoDirectionPicker
          todoId="leaf_1"
          currentDirectionId="dir-platform"
          currentDirectionLabel="Platform"
          childCount={0}
          directions={DIRECTIONS}
          onApply={onApply}
          onClose={onClose}
        />,
      );
    });
    await flush();

    expect(host.querySelector("[data-testid=todo-direction-include-children]")).toBeNull();

    const select = host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-direction-select]")!;
    await act(async () => {
      select.value = "dir-growth";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();

    await act(async () => {
      host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-direction-apply]")!.click();
    });
    await flush();

    expect(onApply).toHaveBeenCalledWith({
      ids: ["leaf_1"],
      direction_id: "dir-growth",
      include_children: false,
      transfer_supervision: false,
    });
  });

  it("shows honest inherited hint and allows pinning explicitly, overriding, or clearing an inherited direction (#16939)", async () => {
    const onApply = vi.fn(async (_req: TodoReorganizeRequest) => {});
    const onClose = vi.fn();

    await act(async () => {
      root.render(
        <TodoDirectionPicker
          todoId="inherited_1"
          currentDirectionId="dir-platform"
          currentDirectionLabel="Platform"
          currentDirectionInherited={true}
          childCount={0}
          directions={DIRECTIONS}
          onApply={onApply}
          onClose={onClose}
        />,
      );
    });
    await flush();

    // Displays inherited hint and pre-selects the resolved direction
    expect(host.querySelector("[data-testid=todo-detail-direction-inherited]")?.textContent).toContain("Platform");
    const select = host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-direction-select]")!;
    expect(select.value).toBe("dir-platform");

    // Apply is enabled so the user can explicitly pin the inherited direction
    const applyBtn = host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-direction-apply]")!;
    expect(applyBtn.disabled).toBe(false);

    await act(async () => applyBtn.click());
    await flush();
    expect(onApply).toHaveBeenCalledWith({
      ids: ["inherited_1"],
      direction_id: "dir-platform",
      include_children: false,
      transfer_supervision: false,
    });
  });
});
