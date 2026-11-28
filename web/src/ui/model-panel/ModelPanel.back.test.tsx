// @vitest-environment jsdom
// Back and the model panel on a narrow screen (#11442): Back closes it (its flyout with it) and stays where it opened;
// closed from the page it takes its history entry back off; in New Agent's sheet a pick leaves the sheet open for the
// next Back; Configure models goes on only once the panel's entry is off, so the page it opens replaces the sheet's.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { PanelProvider, PanelState } from "../../lib/model-panel";

const media = vi.hoisted(() => {
  const m = { narrow: true, phone: false };
  const g = globalThis as Record<string, unknown>;
  // a precise pointer; the widths are read live (lib/viewport.ts keeps one list per query)
  g.matchMedia = (q: string) => ({
    get matches() {
      if (q === "(max-width: 1023px)") return m.narrow;
      if (q === "(max-width: 639px)") return m.phone;
      return q === "(any-pointer: fine)";
    },
    media: q,
    addEventListener() {},
    removeEventListener() {},
  });
  Element.prototype.scrollIntoView ??= () => {};
  return m;
});

const navigated = vi.hoisted(() => [] as { to: string; depth: number }[]);
vi.mock("../../lib/router", async (orig) => {
  const { overlayDepth } = await import("../../lib/back-close");
  return { ...(await orig<typeof import("../../lib/router")>()), navigate: (to: string) => navigated.push({ to, depth: overlayDepth() }) };
});

const { ModelPanel } = await import("./ModelPanel");
const { Dialog } = await import("../atoms");
const { onOverlayEntry, overlayDepth } = await import("../../lib/back-close");
const { initI18n } = await import("../../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const claude: PanelProvider = {
  id: "claude",
  name: "Claude",
  engine: "claude",
  models: [
    { id: "opus", name: "Opus 5.5", efforts: ["low", "medium", "high"], defaultEffort: "high" },
    { id: "sonnet", name: "Sonnet 5", efforts: ["low", "medium", "high"], defaultEffort: "medium" },
  ],
};
const state: PanelState = { current: { provider: "claude", model: "opus", effort: "high" }, recommended: ["claude"] };

let host: HTMLDivElement;
let root: Root;
let anchor: HTMLButtonElement;

beforeAll(async () => {
  await initI18n("zh-Hans");
});

beforeEach(() => {
  media.narrow = true;
  media.phone = false;
  localStorage.clear();
  navigated.length = 0;
  anchor = document.createElement("button");
  document.body.append(anchor);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  anchor.remove();
  await settle();
});

const flush = () => act(async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); });
// back-close's 0 ms timer, then the Back it causes (jsdom's popstate comes after a task)
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 30))));
const goBack = () => act(async () => {
  const popped = new Promise((r) => addEventListener("popstate", r, { once: true }));
  history.back();
  await popped;
});
const panel = () => document.body.querySelector("[data-model-panel]");
const flyout = () => document.body.querySelector("[data-model-flyout]");
const row = (text: string) => [...document.body.querySelectorAll<HTMLElement>("[data-panel-row]")].find((r) => r.textContent?.includes(text))!;
const configure = () => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "配置模型")!;

/** The panel as a pill shows it: open until its onClose. `sheet`: inside a Dialog, as New Agent's is, and opened from
 *  its pill once the sheet is up (the sheet takes the focus as it opens, which closes a panel already there). */
function Harness({ sheet, onSheetClose, onConfigure }: { sheet?: boolean; onSheetClose?: () => void; onConfigure?: (close: () => void) => void }) {
  const [open, setOpen] = useState(!sheet);
  const [dialog, setDialog] = useState(true);
  const closeSheet = () => {
    setDialog(false);
    onSheetClose?.();
  };
  const body = open && (
    <ModelPanel
      anchor={anchor}
      onClose={() => setOpen(false)}
      providers={[claude]}
      state={state}
      scope="agent"
      onPick={async () => true}
      onConfigure={onConfigure && (() => onConfigure(closeSheet))}
    />
  );
  if (!sheet) return body;
  return (
    <Dialog open={dialog} onClose={closeSheet} title="新建 Agent">
      <button type="button" data-pill onClick={() => setOpen(true)} />
      {body}
    </Dialog>
  );
}
const mount = async (props: Parameters<typeof Harness>[0] = {}) => {
  act(() => root.render(<Harness {...props} />));
  await flush();
  if (!props.sheet) return;
  await settle();
  act(() => document.body.querySelector<HTMLButtonElement>("[data-pill]")!.click());
  await flush();
};

describe("Back with the model panel open (#11442)", () => {
  it("closes it with its flyout in one Back on a narrow screen, staying on the page", async () => {
    const at = location.href;
    await mount();
    expect(onOverlayEntry()).toBe(true);
    act(() => row("Sonnet").querySelector<HTMLButtonElement>("[data-row-customize]")!.click());
    expect(flyout()).not.toBeNull();
    await goBack();
    expect(panel()).toBeNull();
    expect(flyout()).toBeNull();
    expect(location.href).toBe(at);
    expect(onOverlayEntry()).toBe(false);
  });

  it("closed from the page (Esc, a press outside), takes its entry back off", async () => {
    await mount();
    act(() => void dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
    expect(panel()).toBeNull();
    await act(async () => void (await vi.waitFor(() => expect(onOverlayEntry()).toBe(false), { timeout: 1000, interval: 10 })));

    act(() => root.render(<></>));
    await mount();
    expect(onOverlayEntry()).toBe(true);
    act(() => void document.body.dispatchEvent(new Event("pointerdown", { bubbles: true })));
    expect(panel()).toBeNull();
    await act(async () => void (await vi.waitFor(() => expect(onOverlayEntry()).toBe(false), { timeout: 1000, interval: 10 })));
  });

  it("in New Agent's sheet, a pick closes only the panel; the next Back closes the sheet", async () => {
    media.phone = true;
    const onSheetClose = vi.fn();
    await mount({ sheet: true, onSheetClose });
    expect(overlayDepth()).toBe(2);
    act(() => row("Sonnet").click());
    await flush();
    expect(panel()).toBeNull();
    await settle();
    expect(overlayDepth()).toBe(1);
    expect(onSheetClose).not.toHaveBeenCalled();
    expect(document.body.querySelector('[role="dialog"]')).not.toBeNull();
    await goBack();
    expect(onSheetClose).toHaveBeenCalledTimes(1);
    expect(onOverlayEntry()).toBe(false);
  });

  it("Configure models goes on once the panel's entry is off: the sheet's is the one its page replaces", async () => {
    media.phone = true;
    const depths: number[] = [];
    await mount({ sheet: true, onConfigure: (closeSheet) => (depths.push(overlayDepth()), closeSheet()) });
    act(() => configure().click());
    expect(panel()).toBeNull();
    expect(depths).toEqual([]);
    await settle();
    expect(depths).toEqual([1]);
  });

  it("Configure models without a caller's own opens Settings once the panel's entry is off", async () => {
    await mount();
    act(() => configure().click());
    expect(navigated).toEqual([]);
    await settle();
    expect(navigated).toEqual([{ to: "/settings?tab=models#settings-providers", depth: 0 }]);
  });

  it("adds no entry on a wide screen, where Configure models goes at once", async () => {
    media.narrow = false;
    const before = history.state;
    await mount();
    expect(panel()).not.toBeNull();
    expect(history.state).toBe(before);
    act(() => configure().click());
    expect(navigated).toEqual([{ to: "/settings?tab=models#settings-providers", depth: 0 }]);
  });
});
