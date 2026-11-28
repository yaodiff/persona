// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { t } from "../i18n";
import type { Todo } from "../lib/todos";
import { TodoCalendar } from "./TodoCalendar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("only offers a download when the displayed range and Show done selection contain deadlines", () => {
  window.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
  const host = document.createElement("div");
  const root = createRoot(host);
  const stamp = "2026-11-01T12:00:00Z";
  const todos: Todo[] = [{ id: "finished", title: "Send invitation", due_date: "2026-11-01", done_at: stamp, created_at: stamp, updated_at: stamp }];
  const draw = (day: number, showDone: boolean) => {
    act(() => root.render(<TodoCalendar todos={todos} lookup={() => undefined} mode="day" anchor={new Date(2026, 10, day).getTime()}
      showDone={showDone} onMode={vi.fn()} onAnchor={vi.fn()} onShowDone={vi.fn()} onOpen={vi.fn()} onAdd={vi.fn()} onShowList={vi.fn()} />));
    const button = [...host.querySelectorAll("button")].find(el => el.textContent === t("todos.calendar_download"));
    expect(button).toBeDefined();
    return button!.disabled;
  };
  try {
    expect(draw(2, true)).toBe(true);
    expect(draw(1, false)).toBe(true);
    expect(draw(1, true)).toBe(false);
  } finally {
    act(() => root.unmount());
  }
});
