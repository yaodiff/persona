// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";

vi.mock("../../i18n", () => ({ t: (key: string) => key }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLDivElement | undefined;
let baseTag: HTMLBaseElement | undefined;
const originalPath = location.pathname + location.search;
const todo = { id: "closed task/?&#+", title: "Example closed task" };

afterEach(() => {
  if (root) act(() => root!.unmount());
  host?.remove();
  baseTag?.remove();
  history.replaceState(null, "", originalPath);
  root = undefined;
  vi.restoreAllMocks();
});

const clicks: { name: string; init: MouseEventInit; native: boolean }[] = [
  { name: "plain", init: {}, native: false },
  { name: "Ctrl", init: { ctrlKey: true }, native: true },
  { name: "Cmd", init: { metaKey: true }, native: true },
  { name: "Shift", init: { shiftKey: true }, native: true },
  { name: "middle", init: { button: 1 }, native: true },
];

for (const base of ["", "/proxy/persona"]) {
  for (const cancelled of [false, true]) {
    it.each(clicks)(`preserves $name navigation at ${base || "root"} (cancelled: ${cancelled})`, async ({ init, native }) => {
      baseTag = document.createElement("base");
      baseTag.href = `${base}/`;
      document.head.append(baseTag);
      history.replaceState(null, "", `${base}/s/example/agent`);
      vi.resetModules();
      const { default: ClosedTaskNotice } = await import("./ClosedTaskNotice");
      host = document.createElement("div");
      document.body.append(host);
      root = createRoot(host);
      act(() => root!.render(<ClosedTaskNotice todo={{ ...todo, cancelled }} writable />));
      const link = host.querySelector<HTMLAnchorElement>("a")!;
      const destination = new URL(link.href);
      expect(destination.pathname).toBe(`${base}/todos`);
      expect([...destination.searchParams]).toEqual([["id", todo.id]]);

      const before = location.href;
      let preventedByNotice: boolean | undefined;
      // Observe React's handling, then stop jsdom's unsupported native navigation.
      const observe = (event: MouseEvent) => {
        preventedByNotice = event.defaultPrevented;
        event.preventDefault();
      };
      document.addEventListener("click", observe);
      try {
        act(() => link.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init })));
      } finally {
        document.removeEventListener("click", observe);
      }
      expect(preventedByNotice).toBe(!native);
      if (native) expect(location.href).toBe(before);
      else {
        expect(location.pathname).toBe(`${base}/todos`);
        expect([...new URLSearchParams(location.search)]).toEqual([["id", todo.id]]);
      }
    });
  }
}
