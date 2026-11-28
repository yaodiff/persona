// @vitest-environment jsdom
// Actual native/HTTP components and adapter with an injected transport; not live-backend evidence.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { UIStreamRequest, UIStreamSnapshot, View } from "../../lib/extension-wire";
import type { AppUIStreamPage } from "../../lib/extension-ui-app-stream";
const seam = vi.hoisted(() => ({
  entries: [] as Array<{ adapter: AppUIStreamPage; sent: UIStreamRequest[]; frame: (body: object) => void }>,
  views: [] as View[], draws: vi.fn(), compose: vi.fn(),
}));
vi.mock("../../lib/managers", () => ({ listManagers: async () => ({ default: "manager" }) }));
vi.mock("./ExtensionView", async (original) => {
  const mod = await original<typeof import("./ExtensionView")>();
  return { ...mod, composeAssistant: seam.compose };
});
vi.mock("../../lib/extension-ui-app-stream", async (original) => {
  const mod = await original<typeof import("../../lib/extension-ui-app-stream")>();
  return { ...mod, appUIStreamPage: (options: Parameters<typeof mod.appUIStreamPage>[0], manager: string, effect: Parameters<typeof mod.appUIStreamPage>[2], failed: Parameters<typeof mod.appUIStreamPage>[3]) => {
    let receive: (raw: string) => void = () => {};
    const sent: UIStreamRequest[] = [];
    const adapter = new mod.AppUIStreamPage(options, manager, {
      connected: () => true, send: (r) => { sent.push(r); }, frames: (fn) => { receive = fn; return () => {}; }, connection: () => () => {},
    }, effect, failed);
    seam.entries.push({ adapter, sent, frame: (body) => receive(JSON.stringify({ type: "extension_ui_stream", manager, ...body })) });
    return adapter;
  } };
});
vi.mock("../../lib/extension-ui", async (original) => ({
  ...(await original<typeof import("../../lib/extension-ui")>()),
  renderUI: async (_extension: string, ui: string) => {
    seam.draws(ui);
    return { sections: [{ items: [{ type: "text", text: `HTTP ${ui}` }, { type: "button", title: "HTTP next", action: { id: "next", push: { ui: "legacy-detail" } } }] }] };
  },
}));
import { openOverlay, overlayDepth } from "../../lib/back-close";
import { NativeExtensionUI } from "./NativeExtensionUI";
import { LegacyExtensionUI } from "./ExtensionView";
import charts from "../../../../schema/extension/examples/View.v6-charts.json";
import overview from "../../../../schema/extension/examples/View.overview-card.json";
let root: Root, host: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) });
  seam.entries = []; seam.views = []; seam.draws.mockClear(); seam.compose.mockClear();
  history.replaceState({}, "", "/extension/example/main");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); seam.entries.forEach((e) => e.adapter.dispose()); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
const nativeView = (): View => ({ sections: [{ key: "section", items: [{ type: "text", key: "text", text: "Native entry" }, { type: "button", key: "next", title: "Native next", action: { id: "next", push: { ui: "detail" } } }] }] });
function gapView(): View {
  const view = structuredClone(charts) as View;
  view.sections.forEach((s, si) => { s.key = `section-${si}`; s.items.forEach((i, ii) => { i.key = `item-${si}-${ii}`; }); });
  return view;
}
async function flushSnapshots() {
  await act(async () => {
    for (const [index, entry] of seam.entries.entries()) {
      if (entry.adapter.session.getState().snapshot || entry.adapter.session.getState().phase === "closed") continue;
      const request = entry.sent.find((r) => r.request === "open");
      if (!request) continue;
      const snapshot: UIStreamSnapshot = { protocol: 1, session: `session-${index}`, epoch: `epoch-${index}`, version: 1, root_key: "root", view: seam.views[index] ?? nativeView() };
      entry.frame({ kind: "snapshot", request_id: request.request_id, snapshot });
    }
  });
}
async function mount(bare = false) {
  await act(async () => root.render(<NativeExtensionUI extension="example" ui="main" bare={bare} legacy={(props) => <LegacyExtensionUI {...props} />} />));
  await flushSnapshots();
}
function top() { return [...host.children].findLast((node) => !(node as HTMLElement).hidden) as HTMLElement; }
async function click(text: string) {
  const button = [...top().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === text)!;
  expect(button).toBeDefined(); await act(async () => button.click()); await flushSnapshots();
}
describe("native navigation repairs", () => {
  it.each(["desktop", "browser"])("opens the first streamed view in the %s web renderer", async (hostKind) => {
    if (hostKind === "desktop") vi.stubGlobal("personaDesktop", { platform: "darwin" });
    await mount();
    expect(seam.entries).toHaveLength(1);
    const entry = seam.entries[0];
    expect.soft(entry.adapter.session.getState().error).toBeUndefined();
    expect(entry.sent).toHaveLength(1);
    expect(entry.sent).toMatchObject([{ request: "open", context: { platform: "web" } }]);
    expect(top().textContent).toContain("Native entry");
    expect(seam.draws).not.toHaveBeenCalled();
  });
  it("mounts a chart-gap HTTP page exactly once", async () => {
    seam.views = [gapView()]; await mount();
    expect(seam.draws).toHaveBeenCalledTimes(1);
    expect(top().textContent).toContain("HTTP main");
    expect(seam.entries[0].adapter.session.getState().phase).toBe("closed");
  });
  it("uses one combined native/legacy history stack and returns from legacy detail", async () => {
    seam.views = [nativeView(), gapView()]; await mount();
    await click("Native next");
    expect(history.state.extPage).toBe(2);
    expect(top().textContent).toContain("HTTP detail");
    await click("HTTP next");
    expect(history.state.extPage).toBe(3);
    expect(top().textContent).toContain("HTTP legacy-detail");
    // Legacy descendants never allocate native sessions or install competing popstate stacks.
    expect(seam.entries).toHaveLength(2);
    expect(seam.draws.mock.calls.map(([ui]) => ui)).toEqual(["detail", "legacy-detail"]);
    await act(async () => {
      const popped = new Promise<void>((resolve) => addEventListener("popstate", () => resolve(), { once: true }));
      history.back(); await popped;
    });
    expect(history.state.extPage).toBe(2);
    expect(top().textContent).toContain("HTTP detail");
    expect(top().textContent).not.toContain("HTTP legacy-detail");
    expect(seam.draws).toHaveBeenCalledTimes(2);
    await act(async () => {
      const popped = new Promise<void>((resolve) => addEventListener("popstate", () => resolve(), { once: true }));
      history.back(); await popped;
    });
    expect(top().textContent).toContain("Native entry");
  });
  it("replaces an open overlay's entry when pushing so closing the overlay does not undo the push", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const backSpy = vi.spyOn(history, "back");
    seam.views = [nativeView(), gapView()];
    await mount();
    const onOverlayPop = vi.fn();
    const closeOverlay = openOverlay(onOverlayPop);
    const lenAtOverlay = history.length;
    expect(overlayDepth()).toBe(1);

    await click("Native next");
    closeOverlay();
    await act(async () => { await vi.runAllTimersAsync(); });

    expect(backSpy).not.toHaveBeenCalled();
    expect(onOverlayPop).not.toHaveBeenCalled();
    expect(history.length).toBe(lenAtOverlay);
    expect(overlayDepth()).toBe(0);
    expect(history.state.extPage).toBe(2);
    expect(top().textContent).toContain("HTTP detail");
  });
  it.each(["local", "receipt"])("replaces the eighth page for a %s push without an extra history entry", async (source) => {
    await mount();
    for (let depth = 2; depth <= 8; depth++) await click("Native next");
    expect(host.children).toHaveLength(8);
    const historyLength = history.length;
    const old = seam.entries[7];
    if (source === "local") await click("Native next");
    else {
      const initial = old.adapter.session.getState().snapshot!;
      await act(async () => old.frame({ kind: "snapshot", snapshot: { ...initial, version: 2, view: { sections: [{ key: "section", items: [{ type: "button", key: "next", title: "Receipt next", action: { id: "next" } }] }] } } }));
      await act(async () => [...top().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Receipt next")!.click());
      const request = old.sent.at(-1)!;
      if (request.request !== "event") throw Error("event expected");
      await act(async () => {
        old.frame({ kind: "delta", delta: { protocol: 1, session: initial.session, epoch: initial.epoch, base_version: 2, version: 3, operations: [] } });
        old.frame({ kind: "receipt", request_id: request.request_id, receipt: { session: initial.session, epoch: initial.epoch, event_id: request.interaction.event_id, version: 3, status: "accepted", result: { push: { ui: "receipt-detail" } } } });
      });
      await flushSnapshots();
    }
    expect(host.children).toHaveLength(8);
    expect(history.length).toBe(historyLength);
    expect(history.state.extPage).toBe(8);
    expect(seam.entries).toHaveLength(9);
    expect(old.adapter.session.getState().phase).toBe("closed");
    expect(top().querySelector('[data-native-key="root"]')).not.toBeNull();
  });
  it("puts a page's own quick prompt from its native receipt into the main composer (#16697)", async () => {
    const composeView: View = { sections: [{ key: "section", items: [{ type: "button", key: "compose", title: "Compose", action: { id: "compose" } }] }] };
    seam.views = [composeView];
    await mount();
    const entry = seam.entries[0];
    const initial = entry.adapter.session.getState().snapshot!;
    await click("Compose");
    const request = entry.sent.find((r) => r.request === "event")!;
    if (request.request !== "event") throw Error("event expected");
    await act(async () => {
      entry.frame({ kind: "delta", delta: { protocol: 1, session: initial.session, epoch: initial.epoch, base_version: 1, version: 2, operations: [] } });
      entry.frame({ kind: "receipt", request_id: request.request_id, receipt: { session: initial.session, epoch: initial.epoch, event_id: request.interaction.event_id, version: 2, status: "accepted", result: { open_url: "persona://compose?prompt=today&text=What%20happened%20today%3F" } } });
      for (let i = 0; i < 12; i++) await Promise.resolve();
    });
    expect(seam.compose).toHaveBeenCalledWith("What happened today?");
  });
  it("keeps a bare full-page chart identical to non-bare rendering, including its value axis", async () => {
    vi.stubGlobal("ResizeObserver", class {
      constructor(private callback: (entries: Array<{ contentRect: { width: number } }>) => void) {}
      observe() { this.callback([{ contentRect: { width: 500 } }]); }
      disconnect() {}
    });
    const view = structuredClone(overview) as View;
    delete view.refresh_after;
    view.sections.forEach((s, si) => { s.key = `section-${si}`; s.items = s.items.filter((i) => i.type === "chart"); s.items.forEach((i, ii) => { i.key = `item-${si}-${ii}`; }); });
    seam.views = [view]; await mount(true);
    await act(async () => { await import("../extensions/ExtensionChart"); });
    const bareChart = top().querySelector("svg")!.outerHTML;
    expect(seam.draws).not.toHaveBeenCalled();
    await act(async () => root.render(<NativeExtensionUI extension="example" ui="main" bare={false} legacy={(props) => <LegacyExtensionUI {...props} />} />));
    expect(top().querySelector("svg")!.outerHTML).toBe(bareChart);
  });
});
