// @vitest-environment jsdom
/// <reference types="node" />
// Audit item 79: today's date in the calendar's week head and month is a filled badge whose number stays readable as
// small text (WCAG AA 4.5:1) in light and dark: the design system's filled accent (`--accent-ink` under `--surface`, the
// accent button's pair), not white on the lighter `--accent` (2.9:1 dark, 3.6:1 light).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("../styles.css?raw", () => ({ default: "" }));
import { contrast } from "../ui/extension-theme";
import { TodoCalendar } from "./TodoCalendar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const css = readFileSync(join(__dirname, "../styles.css"), "utf8");
function tokens(scheme: "light" | "dark"): Record<string, string> {
  const block = (scheme === "light" ? /:root\s*\{([^}]+)\}/ : /\.dark\s*\{([^}]+)\}/).exec(css)?.[1] ?? "";
  return { ...Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-f]{3,8})\s*;/gi)].map((m) => [m[1], m[2]])), white: "#ffffff" };
}

let host: HTMLDivElement;
let root: Root;
const today = new Date(2026, 10, 14, 9).getTime();

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(today);
  window.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

const draw = (mode: "week" | "month") =>
  act(() =>
    root.render(
      <TodoCalendar todos={[]} lookup={() => undefined} mode={mode} anchor={today} showDone={false} onMode={() => {}} onAnchor={() => {}} onShowDone={() => {}} onOpen={() => {}} onAdd={() => {}} onShowList={() => {}} onMove={async () => {}} />,
    ),
  );

describe("today's badge on the calendar", () => {
  for (const mode of ["week", "month"] as const) {
    it(`${mode}: its number keeps 4.5:1 on its fill in light and dark`, () => {
      draw(mode);
      const badge = host.querySelector<HTMLElement>("[data-today]");
      expect(badge?.textContent).toBe("14");
      const classes = badge!.className.split(/\s+/);
      const fill = classes.find((c) => c.startsWith("bg-"))!.slice(3);
      const ink = classes.find((c) => /^text-(?!\[)/.test(c))!.slice(5);
      for (const scheme of ["light", "dark"] as const) {
        const t = tokens(scheme);
        expect(t[fill], `--${fill}`).toBeTruthy();
        expect(t[ink], `--${ink}`).toBeTruthy();
        expect(contrast(t[ink], t[fill]), `${scheme}: ${ink} on ${fill}`).toBeGreaterThanOrEqual(4.5);
      }
    });
  }
});
