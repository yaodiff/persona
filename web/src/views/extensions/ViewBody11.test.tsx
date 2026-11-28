// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { initI18n, t } from "../../i18n";
import { ExtensionOwnerScope } from "../../lib/extension-owner";
import type { UIAction, UIItem, UIView } from "../../lib/extension-ui";
import { readView } from "../../lib/extension-ui";
import { forgetSceneStores, SceneCanvas } from "./ViewBody11";
import { INERT, ViewSections } from "./view-sections-lazy";

const example = JSON.parse(readFileSync("../schema/extension/examples/View.v11-scene-flat.json", "utf8"));
const scene = example.sections[0].items[0] as UIItem;
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); }); };
let host: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  await initI18n("en");
  forgetSceneStores();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 800 });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 450 });
  Object.defineProperty(HTMLElement.prototype, "setPointerCapture", { configurable: true, value: vi.fn() });
  const gradient = { addColorStop: vi.fn() };
  const context = new Proxy({}, {
    get: (target, key) => {
      if (key === "createLinearGradient" || key === "createRadialGradient") return () => gradient;
      if (!(key in target)) (target as Record<PropertyKey, unknown>)[key] = vi.fn();
      return (target as Record<PropertyKey, unknown>)[key];
    },
    set: (target, key, value) => { (target as Record<PropertyKey, unknown>)[key] = value; return true; },
  });
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(context as CanvasRenderingContext2D);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete (HTMLElement.prototype as { setPointerCapture?: unknown }).setPointerCapture;
});

function render(item: UIItem, onPress: (a: UIAction) => void, compact = false) {
  act(() => root.render(<ExtensionOwnerScope owner="test"><SceneCanvas item={item} compact={compact} busy="" onPress={onPress} /></ExtensionOwnerScope>));
}
const stage = () => host.querySelector<HTMLElement>("[data-scene-stage]")!;
const click = async (element: Element) => { await act(async () => { (element as HTMLElement).click(); }); await flush(); };
const pointer = (type: string, x: number, y: number, id = 1) => {
  const event = new MouseEvent(type, { bubbles: true, clientX: x, clientY: y });
  Object.defineProperty(event, "pointerId", { value: id });
  return event;
};

describe("UI 11 SceneCanvas", () => {
  it("reads a scene and its pin actions from a wire view", () => {
    const parsed = readView(example).sections[0].items[0];
    expect(parsed.type).toBe("scene");
    expect(parsed.shapes).toEqual(scene.shapes);
    expect(parsed.shapes!.some((shape) => shape.kind === "box" && shape.wall)).toBe(true);
    expect(parsed.pins![0].action).toEqual(scene.pins![0].action);
    expect(readView({ sections: [{ items: [{ type: "scene", alt: "empty", shapes: [] }, { type: "text", text: "kept" }] }] }).sections[0].items.map((i) => i.type)).toEqual(["text"]);
  });

  it("reads alt, labels and pins in source order and runs a pin action", async () => {
    const press = vi.fn();
    render(scene, press);
    await flush();
    const alt = host.querySelector("[data-scene-alt]")!;
    const labels = [...host.querySelectorAll("[data-scene-label]")];
    const pins = [...host.querySelectorAll("[data-scene-pin]")];
    expect(alt.textContent).toBe(scene.alt);
    expect(labels.map((x) => x.textContent)).toEqual(["Living room", "Bedroom"]);
    expect(pins.map((x) => x.getAttribute("data-scene-pin"))).toEqual(["light.living", "light.bedroom", "climate.living"]);
    expect(alt.compareDocumentPosition(labels[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(labels[1].compareDocumentPosition(pins[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(host.querySelectorAll("[data-scene-canvas]")).toHaveLength(1);
    await click(pins[0]);
    expect(press).toHaveBeenCalledWith(expect.objectContaining({ id: "open", values: { entity: "light.living" } }));
  });

  it("keeps a touched camera across redraws and follows a new default only after Reset", async () => {
    const press = vi.fn();
    render(scene, press);
    await flush();
    await act(async () => { stage().dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true })); });
    expect(stage().dataset.cameraYaw).toBe("230");
    act(() => root.render(<ExtensionOwnerScope owner="test"><SceneCanvas item={{ ...scene, caption: "changed" }} busy="" onPress={press} /></ExtensionOwnerScope>));
    await flush();
    expect(stage().dataset.cameraYaw).toBe("230");
    const reset = [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === t("extensions-viewbody.scene_reset"))!;
    await click(reset);
    expect(stage().dataset.cameraYaw).toBe("225");
    act(() => root.render(<ExtensionOwnerScope owner="test"><SceneCanvas item={{ ...scene, camera: { yaw: 100 } }} busy="" onPress={press} /></ExtensionOwnerScope>));
    await flush();
    expect(stage().dataset.cameraYaw).toBe("100");
  });

  it("orbits with a pointer and zooms with the wheel", async () => {
    render(scene, vi.fn());
    await flush();
    await act(async () => {
      stage().dispatchEvent(pointer("pointerdown", 100, 100));
      stage().dispatchEvent(pointer("pointermove", 130, 120));
      stage().dispatchEvent(pointer("pointerup", 130, 120));
    });
    expect(stage().dataset.cameraYaw).toBe("238.5");
    expect(stage().dataset.cameraPitch).toBe("41");
    const wheel = new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: -100 });
    await act(async () => { stage().dispatchEvent(wheel); });
    expect(wheel.defaultPrevented).toBe(true);
    const wheelZoom = Number(stage().dataset.cameraZoom);
    expect(wheelZoom).toBeGreaterThan(1);
    await act(async () => {
      stage().dispatchEvent(pointer("pointerdown", 100, 100, 2));
      stage().dispatchEvent(pointer("pointerdown", 200, 100, 3));
      stage().dispatchEvent(pointer("pointermove", 240, 100, 3));
      stage().dispatchEvent(pointer("pointerup", 240, 100, 3));
      stage().dispatchEvent(pointer("pointerup", 100, 100, 2));
    });
    expect(Number(stage().dataset.cameraZoom)).toBeGreaterThan(wheelZoom);
  });

  it("opens a compact still preview as an interactive full-screen scene", async () => {
    const press = vi.fn();
    render(scene, press, true);
    await flush();
    const opener = host.querySelector<HTMLButtonElement>("button[aria-label='Full screen']")!;
    expect(host.querySelector("[data-scene-pin='light.living']")?.tagName).toBe("SPAN");
    await click(opener);
    const dialog = document.querySelector("[role='dialog']");
    expect(dialog).not.toBeNull();
    await click(document.querySelector("[data-scene-pin='light.living']")!);
    expect(press).toHaveBeenCalledOnce();
    await click(document.querySelector("button[aria-label='Close']")!);
    expect(document.querySelector("[role='dialog']")).toBeNull();
  });

  it("loads through ViewSections only when a scene is present", async () => {
    const view: UIView = { sections: [{ items: [scene] }] };
    await act(async () => { root.render(<ViewSections view={view} {...INERT} />); });
    await act(async () => { await vi.dynamicImportSettled(); });
    await flush();
    expect(host.querySelector("[data-scene-canvas]")).not.toBeNull();
  });
});
