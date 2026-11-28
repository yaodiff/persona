// @vitest-environment jsdom
// The card of an answer the assistant gave for the user loads its own chunk on first render. A failed load (offline,
// or an old chunk gone after a deploy) keeps a Retry in its slot and must not reach the page boundary around the chat.
import { act, Component, type ComponentType, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const reported = vi.hoisted(() => vi.fn());
vi.mock("../../lib/error-reports", () => ({ reportBoundary: reported }));
import { DelegatedCardFrame } from "./Interactions";
import { lazyPage, type LazyPageComponent } from "../../lib/lazy-page";
import { t } from "../../i18n";
import type { DelegatedAnswer } from "../../lib/delegate";
import type { Interaction } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const answer = (id: string): DelegatedAnswer => ({
  ix: { id, machine_id: "m", session_id: "s", kind: "question" } as Interaction,
  answers: { q: "Yes" },
  reason: "",
  at: 1,
});
type CardComponent = ComponentType<{ d: DelegatedAnswer }>;
const failure = () => new TypeError("Failed to fetch dynamically imported module");

/** Stands for PageBoundary: anything reaching it replaced the chat. */
class Page extends Component<{ children: ReactNode }, { error: unknown }> {
  state = { error: null as unknown };
  static getDerivedStateFromError(error: unknown) {
    return { error };
  }
  render() {
    return this.state.error ? <p data-page-error /> : this.props.children;
  }
}

let host: HTMLDivElement;
let root: Root;

async function render(Lazy: LazyPageComponent<{ d: DelegatedAnswer }>, d: DelegatedAnswer, key = 0) {
  await act(async () => {
    root.render(
      <Page>
        <DelegatedCardFrame key={key} d={d}><Lazy d={d} /></DelegatedCardFrame>
        <p data-sibling>Another card</p>
      </Page>,
    );
    await Promise.resolve();
  });
}

const retry = () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("app.retry"))!;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reported.mockClear();
  vi.unstubAllGlobals();
});

describe("a lazy delegated-answer card", () => {
  it("contains a failed import in its slot and retries it there", async () => {
    const error = failure();
    const Loaded = () => <button type="button">Changed answer card</button>;
    const load = vi.fn<() => Promise<CardComponent>>().mockRejectedValueOnce(error).mockResolvedValue(Loaded);
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      await render(lazyPage(load), answer("d_retry"));
      await vi.waitFor(() => expect(host.querySelector('[role="alert"]')?.textContent).toContain(t("chat-fulltext.failed")), { timeout: 20_000 });
      expect(host.querySelector("[data-page-error]")).toBeNull();
      expect(host.querySelector("[data-sibling]")).not.toBeNull();
      expect(reported).toHaveBeenCalledWith(error, "delegated_card", true, expect.anything());
      act(() => retry().click());
      await vi.waitFor(() => expect(host.textContent).toContain("Changed answer card"));
      expect(load).toHaveBeenCalledTimes(2);
      expect(host.querySelector('[role="alert"]')).toBeNull();
    } finally {
      consoleError.mockRestore();
    }
  });

  it("reloads once, not in a loop, when the browser keeps returning the failed chunk", async () => {
    const d = answer("d_reload");
    const load = vi.fn<() => Promise<CardComponent>>().mockRejectedValue(failure());
    const reload = vi.fn();
    vi.stubGlobal("location", { reload });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      await render(lazyPage(load), d, 1);
      await vi.waitFor(() => expect(host.querySelector('[role="alert"]')).not.toBeNull());
      act(() => retry().click());
      await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
      // A real reload makes a fresh boundary but keeps sessionStorage.
      await render(lazyPage(load), d, 2);
      await vi.waitFor(() => expect(host.querySelector('[role="alert"]')).not.toBeNull());
      act(() => retry().click());
      await vi.waitFor(() => expect(reported).toHaveBeenCalledTimes(4));
      expect(reload).toHaveBeenCalledTimes(1);
      expect(host.querySelector("[data-page-error]")).toBeNull();
      expect(host.querySelector("[data-sibling]")).not.toBeNull();
    } finally {
      consoleError.mockRestore();
    }
  });
});
