// @vitest-environment jsdom
// The knowledge base with no entry open (#16462): beside the list, the detail side keeps a top bar of the list's bar's
// height with the same line under it, so the line runs across the page as on Inbox and To-dos. An open entry draws its
// own bar there instead.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { lists, searches } from "./cache";

// The page's reads never answer: the bars are drawn before anything is read.
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: () => new Promise(() => {}),
}));

import { Knowledge } from "./Knowledge";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  lists.clear();
  searches.clear();
  Element.prototype.scrollTo = vi.fn() as unknown as Element["scrollTo"];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  lists.clear();
  searches.clear();
});

const classes = (el: Element | null | undefined) => (el?.getAttribute("class") ?? "").split(/\s+/);
const height = (el: Element | null | undefined) => classes(el).filter((c) => /^h-/.test(c));

it("draws a bar of the list's height over the empty detail side, with the same line under it", async () => {
  await act(async () => root.render(<Knowledge onToggleSidebar={() => {}} />));
  const [list, detail] = Array.from(host.firstElementChild!.children);
  const listBar = list!.querySelector("header");
  const detailBar = detail!.querySelector("[data-testid=kb-empty-detail-bar]");
  expect(listBar).not.toBeNull();
  expect(detailBar).not.toBeNull();
  // the bar is the detail side's first thing, over its empty state
  expect(detail!.firstElementChild?.firstElementChild).toBe(detailBar);
  expect(detail!.textContent).toContain(t("knowledge-kbdetail.empty_title"));
  expect(height(detailBar)).toEqual(height(listBar));
  expect(height(detailBar)).toEqual(["h-14"]);
  for (const c of ["shrink-0", "border-b", "border-line"]) expect(classes(detailBar)).toContain(c);
  // a bar that says nothing: not read out, and not a second header
  expect(detailBar!.getAttribute("aria-hidden")).toBe("true");
  expect(detailBar!.textContent).toBe("");
  expect(host.querySelectorAll("header")).toHaveLength(1);
});
