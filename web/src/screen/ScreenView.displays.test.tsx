// @vitest-environment jsdom
// The live screen of a multi-display Mac (#5685): the bar offers its displays, the one chosen is cropped out and stays
// chosen for this Mac (a new page, the displays changing), and a Mac with one display gets the page as it was.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";

type FakeRFB = EventTarget & { canvas: HTMLCanvasElement };
const rfbs = vi.hoisted(() => [] as FakeRFB[]);
const status = vi.hoisted(() => ({ displays: [] as unknown[], asked: 0 }));
vi.mock("@novnc/novnc", () => ({
  // noVNC puts a canvas in its target and sizes it to the framebuffer.
  default: class extends EventTarget {
    viewOnly = true;
    canvas = document.createElement("canvas");
    constructor(target: HTMLElement) {
      super();
      target.append(this.canvas);
      rfbs.push(this as unknown as FakeRFB);
    }
    sendCredentials() {}
    disconnect() {
      this.canvas.remove();
    }
  },
}));
vi.mock("../views/theme", () => ({}));
vi.mock("./aes", () => ({ ensureSubtleAES: () => {} }));
vi.mock("../lib/api", async (actual) => ({
  ...(await actual<typeof import("../lib/api")>()),
  api: async (path: string) => {
    if (path === "/machines") return { machines: [{ id: "macbook", name: "MacBook", online: true }] };
    status.asked++;
    return { enabled: true, screen_sharing: true, displays: status.displays };
  },
}));
import { ScreenView } from "./ScreenView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The mock hub's MacBook (MOCK_SCREEN_DISPLAYS=2:node): two displays side by side, points at a 2× scale.
const TWO = [{ id: 1, x: 0, y: 0, width: 240, height: 150, main: true }, { id: 2, x: 240, y: 0, width: 240, height: 150 }];

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const open = async (embedded = false) => {
  act(() => root.render(<ScreenView target={{ mid: "macbook", token: "tok", embedded, name: "MacBook", keep: "", unlock: false }} />));
  await flush();
};
const framebuffer = async (w: number, h: number) => {
  const c = rfbs.at(-1)!.canvas;
  c.width = w;
  c.height = h;
  await flush();
};
// live; the Displays menu, a lazy component, appears a moment later
const connect = async () => {
  act(() => void rfbs.at(-1)!.dispatchEvent(new CustomEvent("connect", { detail: {} })));
  await flush();
};
const button = () => host.querySelector<HTMLButtonElement>('[data-testid="screen-displays"]');
const screen = () => host.querySelector<HTMLElement>('[data-testid="screen"]')!;
const rows = () => [...document.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')];
const menuAppears = () => vi.waitFor(async () => {
  await act(async () => {});
  expect(button()).not.toBeNull();
}, { timeout: 3000 });

// Preload the chunk; each mounted page still commits its own import callback.
// Positive cases therefore wait for the actual button rather than another fixed flush.
beforeAll(async () => {
  await import("./DisplayMenu");
});

beforeEach(() => {
  rfbs.length = 0;
  status.displays = TWO;
  status.asked = 0;
  // the room below the bar; jsdom lays nothing out
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ left: 0, top: 0, right: 480, bottom: 400, width: 480, height: 400, x: 0, y: 0, toJSON() {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("ScreenView on a Mac with two displays", () => {
  it("offers its displays and shows the one chosen, again on the next page", async () => {
    await open();
    await framebuffer(960, 300);
    await connect();
    await menuAppears();
    expect(button()?.getAttribute("aria-label")).toBe(t("screen-displays.displays_current", { current: t("screen-displays.all_displays") }));
    expect(screen().style.width).toBe("");

    act(() => button()!.click());
    expect(rows().map((r) => [r.textContent, r.getAttribute("aria-checked")])).toEqual([
      [t("screen-displays.all_displays"), "true"],
      [t("screen-displays.display_n", { n: 1 }), "false"],
      [t("screen-displays.display_n", { n: 2 }), "false"],
    ]);
    act(() => rows()[2].click());
    await flush();

    // display 2 of 960 × 300 in 480 × 400: drawn 1:1, centered in the height, the framebuffer half off to the left
    const clip = screen().parentElement!;
    expect([clip.style.left, clip.style.top, clip.style.width, clip.style.height]).toEqual(["0px", "50px", "480px", "300px"]);
    expect([screen().style.left, screen().style.top, screen().style.width, screen().style.height]).toEqual(["-480px", "0px", "960px", "300px"]);
    expect(button()?.getAttribute("aria-label")).toBe(t("screen-displays.displays_current", { current: t("screen-displays.display_n", { n: 2 }) }));

    // a new page for the same Mac comes back on display 2, from its first live frame: the menu came while it connected
    act(() => root.unmount());
    root = createRoot(host);
    await open();
    await framebuffer(960, 300);
    act(() => void rfbs.at(-1)!.dispatchEvent(new CustomEvent("connect", { detail: {} })));
    expect(screen().style.left).toBe("-480px");
    await flush();
    await menuAppears();
    act(() => button()!.click());
    expect(rows().map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "false", "true"]);
    act(() => rows()[0].click());
    await flush();
    expect(screen().style.width).toBe("");
  });

  // Unplugged, display 2 is not there: all of the picture shows, and it comes back once the Mac has it again.
  it("keeps display 2 chosen while it is gone", async () => {
    await open();
    await framebuffer(960, 300);
    await connect();
    await menuAppears();
    act(() => button()!.click());
    act(() => rows()[2].click());
    await flush();

    act(() => button()!.click()); // the menu open as the display goes
    await flush();
    let asked = status.asked;
    status.displays = [];
    await framebuffer(480, 300);
    expect(status.asked - asked, "a framebuffer of another size asks for the displays again").toBe(1);
    expect(button()).toBeNull();
    expect(rows(), "the menu goes with its button").toEqual([]);
    expect(screen().style.width).toBe("");

    status.displays = TWO;
    asked = status.asked;
    await framebuffer(960, 300);
    expect(status.asked - asked).toBe(1);
    expect(screen().style.left).toBe("-480px");
    act(() => button()!.click());
    expect(rows().map((r) => r.getAttribute("aria-checked")), "one click opens it again").toEqual(["false", "false", "true"]);
  });
});

describe("ScreenView keeping up with the Mac", () => {
  // Two displays of one size swapped in System Settings: the framebuffer keeps its size, so only opening the menu asks.
  it("asks for the displays again as the menu opens and follows them", async () => {
    await open();
    await framebuffer(960, 300);
    await connect();
    await menuAppears();
    act(() => button()!.click());
    act(() => rows()[2].click());
    await flush();
    expect(screen().style.left).toBe("-480px");

    status.displays = [{ ...TWO[1], x: 0 }, { ...TWO[0], x: 240 }];
    const asked = status.asked;
    act(() => button()!.click());
    await flush();
    expect(status.asked - asked).toBe(1);
    expect(screen().style.left, "display 2 is now the left one").toBe("0px");
    expect(rows().map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
  });

  // An app's page has no bar to choose in: what this browser kept must not crop it.
  it("shows every display on a page an app embeds", async () => {
    localStorage.setItem("persona.screen.display.macbook", "display-2");
    await open(true);
    await framebuffer(960, 300);
    await connect();
    expect(screen().style.width).toBe("");
    expect(button()).toBeNull();
  });
});

describe("ScreenView on a Mac with one display", () => {
  it("offers no choice and draws the whole picture", async () => {
    status.displays = [];
    await open();
    await framebuffer(480, 300);
    await connect();
    expect(button()).toBeNull();
    expect(screen().className).toContain("inset-0");
    expect(screen().style.width).toBe("");
  });

  it("offers no choice for an arrangement that does not fit the picture", async () => {
    await open();
    await framebuffer(480, 300); // the node says two displays, Screen Sharing sends one
    await connect();
    expect(button()).toBeNull();
  });
});
