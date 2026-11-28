// @vitest-environment jsdom
// Theme color "Match avatar" (ui/look.ts): the persona store keeps the chosen persona's accent (views/assistant/Persona.tsx setPS),
// and the picker offers "Match avatar" only while that accent is there or it is already the choice.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const hub = vi.hoisted(() => ({ result: null as unknown, fail: false }));
vi.mock("../../lib/api", async (orig) => {
  const real = await orig<typeof import("../../lib/api")>();
  return {
    ...real,
    getToken: () => "",
    // An older hub (no /looks): the packages from /personas, the choice this browser's (Persona.test.tsx has /looks).
    api: vi.fn((path: string) =>
      hub.fail ? Promise.reject(new Error("offline")) : path === "/looks" ? Promise.reject(new real.ApiError(404, "not found", {})) : Promise.resolve(hub.result)),
  };
});
import { t } from "../../i18n";
import { applyFonts, fontPreference } from "../../ui/font-preference";
import { BUILTIN_PERSONA, type PersonaInfo } from "../../lib/persona";
import { applyLook, savedPersonaAccent, setAccentChoice, setGlassTranslucency, setPersonaAccent, setSurfaceStyle } from "../../ui/look";
import { choosePersona, refreshPersonas } from "../assistant/Persona";
import { FontRows, SurfaceStyleRow, ThemeColorPicker } from "./LookSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// preload() waits on <img> loads, which jsdom never fires
vi.stubGlobal("Image", class { onload: (() => void) | null = null; onerror: (() => void) | null = null; set src(_: string) { queueMicrotask(() => this.onload?.()); } });

const pack = (rev: string, accent?: string): PersonaInfo => ({
  id: "frog",
  rev,
  manifest: {
    format: 1, id: "frog", name: "奶蛙",
    avatar: { sizes: { "128": "avatar.png" } },
    dot: { states: { idle: { file: "idle.png" } } },
    ...(accent ? { colors: { light: { accent } } } : {}),
  },
});
const list = (...p: PersonaInfo[]) => ({ personas: p });

describe("the persona store keeps 跟随形象's accent", () => {
  beforeEach(() => {
    hub.fail = false;
  });

  it("saves the chosen pack's accent, keeps it through a failed load, and clears it for a built-in or a pack gone", async () => {
    hub.result = list(pack("1", "#94600A"));
    choosePersona("frog");
    await refreshPersonas();
    expect(savedPersonaAccent()).toEqual({ light: "#94600a", dark: "#94600a" });

    // new colors in the list: the accent follows
    hub.result = list(pack("2", "#27747F"));
    await refreshPersonas();
    expect(savedPersonaAccent()).toEqual({ light: "#27747f", dark: "#27747f" });

    // the hub out of reach: the last accent stays
    hub.fail = true;
    await refreshPersonas();
    expect(savedPersonaAccent()).toEqual({ light: "#27747f", dark: "#27747f" });
    hub.fail = false;

    // a built-in has no accent
    choosePersona(BUILTIN_PERSONA);
    expect(savedPersonaAccent()).toBeNull();
    choosePersona("frog");
    expect(savedPersonaAccent()).toEqual({ light: "#27747f", dark: "#27747f" });

    // the pack is gone from the hub's list
    hub.result = list();
    await refreshPersonas();
    expect(savedPersonaAccent()).toBeNull();
  });
});

describe("主题色's options", () => {
  let host: HTMLDivElement;
  let root: Root;
  const draw = async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => root.render(<ThemeColorPicker />));
    return [...host.querySelectorAll('[role="radio"]')];
  };
  beforeEach(() => {
    localStorage.clear();
    setPersonaAccent(null);
    setAccentChoice("default");
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
  });

  it("has no 跟随形象 while the persona brings no colors", async () => {
    const radios = await draw();
    expect(radios).toHaveLength(6);
    expect(radios.some((r) => r.getAttribute("aria-label")?.startsWith("跟随形象"))).toBe(false);
  });

  it("offers 跟随形象 when the persona brings an accent", async () => {
    setPersonaAccent({ light: "#94600a", dark: "#b47a0c" });
    const radios = await draw();
    expect(radios).toHaveLength(7);
    expect(radios[1]!.getAttribute("aria-label")).toMatch(/^跟随形象/);
  });

  it("keeps 跟随形象 in sight once chosen, saying this persona has none", async () => {
    setAccentChoice("persona");
    const radios = await draw();
    expect(radios).toHaveLength(7);
    expect(radios[1]!.getAttribute("aria-checked")).toBe("true");
    expect(radios[1]!.getAttribute("aria-label")).toBe("跟随形象，该形象未设置");
  });

  it("offers and applies 跟随形象 for an uploaded avatar with colors", async () => {
    const uploaded: PersonaInfo = {
      id: "u-demo0002",
      rev: "r1",
      manifest: {
        format: 1,
        id: "u-demo0002",
        name: "Mikan",
        avatar: { sizes: { "128": "avatar/128.png" } },
        dot: { states: { idle: { file: "dot/idle.png" } } },
        source: { kind: "upload", color: { seed: "#3A7BD5", auto: false } },
        colors: { light: { primary: "#3A7BD5", accent: "#2255A0" }, dark: { primary: "#3A7BD5", accent: "#6BA0E8" } },
      },
    };
    hub.result = list(uploaded);
    choosePersona("u-demo0002");
    await refreshPersonas();
    expect(savedPersonaAccent()).toEqual({ light: "#2255a0", dark: "#6ba0e8" });
    const radios = await draw();
    expect(radios).toHaveLength(7);
    const matchAvatar = radios[1] as HTMLButtonElement;
    expect(matchAvatar.getAttribute("aria-label")).toBe("跟随形象，Mikan");
    await act(async () => matchAvatar.click());
    expect(document.documentElement.dataset.accent).toBe("persona");
    expect(document.getElementById("persona-accent")?.textContent).toContain("--accent: #2255a0;");
  });
});

describe("Appearance font settings", () => {
  let host: HTMLDivElement;
  let root: Root;
  const select = async (name: string, value: string) => {
    const input = [...host.querySelectorAll("select")].find((el) => el.getAttribute("aria-label") === name)!;
    await act(async () => { input.value = value; input.dispatchEvent(new Event("change", { bubbles: true })); });
  };
  const button = (label: string) => [...host.querySelectorAll("button")].find((el) => el.textContent === label);
  const setFamily = async (family: string) => {
    const input = host.querySelector("input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, family);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };

  beforeEach(async () => {
    localStorage.clear();
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => ({
      measureText: () => ({ width: 100 }),
    }) as unknown as CanvasRenderingContext2D);
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => root.render(<FontRows />));
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    localStorage.clear();
    applyFonts();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("shows terminal guidance only for code and previews a custom font before applying it", async () => {
    expect(host.textContent?.match(new RegExp(t("ui-look.font_mono_hint"), "g"))).toHaveLength(1);
    expect(host.querySelector("pre code")?.textContent).toContain("const");
    await select(t("ui-look.font_sans"), "custom");
    expect(button(t("ui-look.font_browse"))).toBeUndefined();
    await setFamily("Unavailable Sample");
    expect(host.querySelector<HTMLElement>(".prose-j")!.style.fontFamily).toContain("Unavailable Sample");
    expect(host.textContent).toContain(t("ui-look.font_unavailable", { kind: t("ui-look.font_sans") }));
    expect(fontPreference().sans.preset).toBe("default");
    await act(async () => button(t("ui-look.font_apply"))!.click());
    expect(fontPreference().sans).toEqual({ preset: "custom", family: "Unavailable Sample" });
  });

  it("asks for installed fonts on click and leaves manual entry available after denial", async () => {
    const list = vi.fn().mockRejectedValueOnce(new Error("denied")).mockResolvedValueOnce([
      { family: "Example Mono" }, { family: "Example Mono" }, { family: "A;B" },
    ]);
    vi.stubGlobal("queryLocalFonts", list);
    await select(t("ui-look.font_mono"), "custom");
    expect(list).not.toHaveBeenCalled();
    await act(async () => button(t("ui-look.font_browse"))!.click());
    expect(host.textContent).toContain(t("ui-look.font_browse_unavailable"));
    expect(host.querySelector("input")).not.toBeNull();
    await act(async () => button(t("ui-look.font_browse"))!.click());
    const local = [...host.querySelectorAll("select")].at(-1)!;
    expect([...local.options].map((o) => o.value)).toEqual(["", "Example Mono"]);
    await select(t("ui-look.font_local_label", { kind: t("ui-look.font_mono") }), "Example Mono");
    expect(host.querySelector("input")!.value).toBe("Example Mono");
    expect(fontPreference().mono.preset).toBe("default");
  });
});

describe("Window style & glass translucency controls", () => {
  let host: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    localStorage.clear();
    setSurfaceStyle("default");
    setGlassTranslucency(75);
    applyLook();
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => root.render(<SurfaceStyleRow />));
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    localStorage.clear();
    applyLook();
  });

  it("hides the translucency slider under Default and shows + updates it under Liquid Glass", async () => {
    expect(host.querySelector('input[type="range"]')).toBeNull();
    const liquidGlassBtn = [...host.querySelectorAll("button")].find((b) => b.textContent === t("ui-look.surface_liquid_glass"))!;
    await act(async () => liquidGlassBtn.click());

    const slider = host.querySelector<HTMLInputElement>('input[type="range"]');
    expect(slider).not.toBeNull();
    expect(slider!.value).toBe("75");
    expect(slider!.step).toBe("5");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(slider!, "90");
      slider!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(document.documentElement.style.getPropertyValue("--glass-t")).toBe("0.90");
    expect(host.textContent).toContain("90%");

    const defaultBtn = [...host.querySelectorAll("button")].find((b) => b.textContent === t("ui-look.surface_default"))!;
    await act(async () => defaultBtn.click());
    expect(host.querySelector('input[type="range"]')).toBeNull();
  });
});
