// @vitest-environment jsdom
// The live screen's picture switcher (#3954, #6558): the bar offers Video / VNC — but only when this Mac's node
// advertises the hardware video channel and this browser decodes it, so no Mac shows a control it can't use — and, for
// video, a quality bias once the helper and this page both have quality tiers. The choice is kept per Mac in this
// browser. A page an app embeds has no bar to choose in. How much data the video may use follows the network, and with
// tiers on mobile data the user's choice, asked on a card and kept in ⋯, as on the iPhone.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { webTiers, webVideo } from "./video";

type FakeRFB = EventTarget & { canvas: HTMLCanvasElement };
const rfbs = vi.hoisted(() => [] as FakeRFB[]);
const status = vi.hoisted(() => ({ video: null as unknown }));
// The video chunk (#6558): what the browser decodes, and whether the helper said it has tiers.
const chunk = vi.hoisted(() => ({
  probe: { codecs: ["hevc", "h264"] } as { codecs: string[]; why?: string },
  tiers: false,
  budget: null as { maxKbps: number; fps: number } | null, // the cap the channel was last given (it starts the stream)
  tierBudget: null as { maxKbps: number; fps: number; maxScale?: number } | null, // the tiers' cap, with the choice
  quality: null as string | null,
  pageTiers: null as boolean | null, // the page drives the tiers
}));
vi.mock("./VideoPicture", async () => {
  const { useEffect } = await import("react");
  return {
    probeHere: async () => chunk.probe,
    VideoChannel: ({ onState, budget, tierBudget, quality, tiers }: {
      onState: (s: unknown) => void;
      budget: { maxKbps: number; fps: number };
      tierBudget: { maxKbps: number; fps: number; maxScale?: number };
      quality: string;
      tiers: boolean;
    }) => {
      chunk.budget = budget;
      chunk.tierBudget = tierBudget;
      chunk.quality = quality;
      chunk.pageTiers = tiers;
      useEffect(() => onState({ showing: false, fallback: null, reading: {}, codec: null, size: null, tiers: chunk.tiers, decoderFailed: false }), [onState]);
      return null;
    },
    FallbackLine: () => null,
    MobileDataCard: ({ onChoose, onClose }: { onChoose: (m: string) => void; onClose: () => void }) => (
      <div data-testid="screen-mobile-data">
        <button type="button" data-testid="screen-mobile-data-clearer" onClick={() => onChoose("clearer")} />
        <button type="button" data-testid="screen-mobile-data-close" onClick={onClose} />
      </div>
    ),
  };
});
vi.mock("@novnc/novnc", () => ({
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
  api: async (path: string) =>
    path === "/machines"
      ? { machines: [{ id: "macbook", name: "MacBook", online: true }] }
      : { enabled: true, screen_sharing: true, displays: [], screen_video: status.video },
}));
import { ScreenView } from "./ScreenView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const open = async (embedded = false) => {
  act(() => root.render(<ScreenView target={{ mid: "macbook", token: "tok", embedded, name: "MacBook", keep: "", unlock: false }} />));
  await flush();
};
// live; the picture menu, a lazy component, appears a moment later
const connect = async () => {
  act(() => void rfbs.at(-1)!.dispatchEvent(new CustomEvent("connect", { detail: {} })));
  await flush();
};
const button = () => host.querySelector<HTMLButtonElement>('[data-testid="screen-video"]');
const rows = () => [...document.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')];
const menuAppears = () => vi.waitFor(async () => {
  await act(async () => {});
  expect(button()).not.toBeNull();
}, { timeout: 3000 });

// Preload the chunk; each mounted page still commits its own import callback.
// Positive cases therefore wait for the actual button rather than another fixed flush.
beforeAll(async () => {
  await import("./ScreenMenu");
});

beforeEach(() => {
  rfbs.length = 0;
  status.video = { version: 1, codecs: ["hevc", "h264"] };
  webVideo.plays = true; // the switcher as it will be once the browser plays video (not yet: see the last test)
  chunk.probe = { codecs: ["hevc", "h264"] };
  chunk.tiers = false;
  chunk.budget = null;
  chunk.tierBudget = null;
  chunk.quality = null;
  chunk.pageTiers = null;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  webVideo.plays = false;
  webTiers.supported = false;
  delete (navigator as { connection?: unknown }).connection;
  act(() => root.unmount());
  host.remove();
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("the picture switcher when the Mac advertises video", () => {
  it("offers Video / VNC and keeps the choice per Mac; no Quality until both sides have tiers", async () => {
    await open();
    await connect();
    await menuAppears();
    // Video by default where it plays, as on the iPhone, until a choice is kept.
    expect(button()?.getAttribute("aria-label")).toBe(t("screen-video.picture_current", { current: t("screen-video.picture_video") }));

    act(() => button()!.click());
    expect(rows().map((r) => [r.textContent, r.getAttribute("aria-checked")])).toEqual([
      [t("screen-video.picture_video"), "true"],
      [t("screen-video.picture_vnc"), "false"],
    ]);

    act(() => rows()[1].click()); // choose VNC
    await flush();
    expect(localStorage.getItem("persona.screen.picture.macbook")).toBe("vnc");
    expect(button()?.getAttribute("aria-label")).toBe(t("screen-video.picture_current", { current: t("screen-video.picture_vnc") }));

    act(() => button()!.click());
    act(() => rows()[0].click()); // and Video again
    await flush();
    expect(localStorage.getItem("persona.screen.picture.macbook")).toBe("video");

    act(() => button()!.click());
    expect(rows().map((r) => r.getAttribute("aria-checked")), "Video checked, and no Quality: a choice that would do nothing").toEqual([
      "true", // Video
      "false", // VNC
    ]);
  });

  it("adds Quality for video once the helper and this page both have tiers", async () => {
    webTiers.supported = true;
    chunk.tiers = true;
    localStorage.setItem("persona.screen.picture.macbook", "video");
    await open();
    await connect();
    await menuAppears();
    act(() => button()!.click());
    expect(rows().map((r) => r.getAttribute("aria-checked")), "Video checked, the quality bias defaults to Auto").toEqual([
      "true", // Video
      "false", // VNC
      "true", // Auto
      "false", // Sharper
      "false", // Smoother
    ]);
    expect([chunk.pageTiers, chunk.quality], "the channel drives the tiers, on Auto").toEqual([true, "auto"]);
    act(() => rows()[3].click()); // Sharper
    await flush();
    expect(localStorage.getItem("persona.screen.quality.macbook")).toBe("sharper");
    expect(chunk.quality, "Quality goes to the running stream").toBe("sharper");
  });

  it("comes back on the kept picture on the next page", async () => {
    localStorage.setItem("persona.screen.picture.macbook", "video");
    await open();
    await connect();
    await menuAppears();
    expect(button()?.getAttribute("aria-label")).toBe(t("screen-video.picture_current", { current: t("screen-video.picture_video") }));
    act(() => button()!.click());
    expect(rows(), "Video and VNC").toHaveLength(2);
  });
});

describe("the picture switcher otherwise", () => {
  it("is not offered when this browser decodes neither codec (a page over plain http, say)", async () => {
    chunk.probe = { codecs: [], why: "insecure" };
    await open();
    await connect();
    await flush();
    expect(button()).toBeNull();
  });

  it("is not offered when the Mac does not advertise the video channel", async () => {
    status.video = null; // an older node, or no helper
    await open();
    await connect();
    expect(button(), "no control a Mac can't use").toBeNull();
  });

  it("is not offered for version 0", async () => {
    status.video = { version: 0 };
    await open();
    await connect();
    expect(button()).toBeNull();
  });

  it("is not offered on a page an app embeds", async () => {
    await open(true);
    await connect();
    expect(button()).toBeNull();
  });

  it("is not offered while this browser can't play video, even for a Mac that advertises it", async () => {
    webVideo.plays = false; // as shipped until the web's decoder lands
    await open();
    await connect();
    expect(button(), "the web keeps RFB and shows no Video it can't play").toBeNull();
  });
});

describe("data use", () => {
  // what the browser says about its network (Chrome's navigator.connection); none in jsdom, as in Safari
  const network = (type: string, saveData = false) => {
    const c = Object.assign(new EventTarget(), { type, saveData });
    Object.defineProperty(navigator, "connection", { value: c, configurable: true });
    return c;
  };
  const card = () => host.querySelector('[data-testid="screen-mobile-data"]');
  const live = async () => {
    await open();
    await connect();
    await menuAppears();
  };

  it("without tiers is the iPhone's cap, sent again when the network changes; no card and no choice in ⋯", async () => {
    const c = network("cellular");
    await live();
    expect(chunk.budget).toEqual({ maxKbps: 4_000, fps: 60 });
    c.type = "wifi";
    act(() => void c.dispatchEvent(new Event("change")));
    await flush();
    expect(chunk.budget).toEqual({ maxKbps: 12_000, fps: 60 });
    expect(card()).toBeNull();
    act(() => button()!.click());
    expect(rows(), "Video and VNC only").toHaveLength(2);
  });

  it("with tiers on mobile data asks on the card, on Data Saver until answered; Clearer is kept for every computer and sits in ⋯", async () => {
    webTiers.supported = true;
    chunk.tiers = true;
    network("cellular");
    await live();
    await vi.waitFor(() => expect(card()).not.toBeNull());
    // the stream starts on the cap without tiers (they aren't known before hello); the tiers take Data Saver
    expect([chunk.budget, chunk.tierBudget]).toEqual([{ maxKbps: 4_000, fps: 60 }, { maxKbps: 1_500, fps: 60, maxScale: 1 }]);
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="screen-mobile-data-clearer"]')!.click());
    await flush();
    expect([card(), localStorage.getItem("persona.screen.mobileData"), chunk.tierBudget]).toEqual([null, "clearer", { maxKbps: 4_000, fps: 60 }]);
    act(() => button()!.click());
    expect(rows().map((r) => [r.textContent?.startsWith(t("screen-video.mobile_data_clearer")) || r.textContent?.startsWith(t("screen-video.mobile_data_saver")), r.getAttribute("aria-checked")]).slice(-2)).toEqual([
      [true, "true"], // Clearer
      [true, "false"], // Data Saver
    ]);
    act(() => rows().at(-1)!.click()); // back to Data Saver from ⋯
    await flush();
    expect([localStorage.getItem("persona.screen.mobileData"), chunk.tierBudget]).toEqual(["saver", { maxKbps: 1_500, fps: 60, maxScale: 1 }]);
  });

  it("asks at most twice: closed unanswered it stays on Data Saver and comes back once more", async () => {
    webTiers.supported = true;
    chunk.tiers = true;
    network("cellular");
    localStorage.setItem("persona.screen.mobileData.asked", "1");
    await live();
    await vi.waitFor(() => expect(card()).not.toBeNull());
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="screen-mobile-data-close"]')!.click());
    await flush();
    expect([card(), localStorage.getItem("persona.screen.mobileData"), chunk.tierBudget]).toEqual([null, null, { maxKbps: 1_500, fps: 60, maxScale: 1 }]);
    act(() => root.unmount());
    root = createRoot(host);
    await live(); // asked twice now
    await flush();
    expect(card()).toBeNull();
  });

  it("asks nothing on Wi-Fi, or with the browser's Data Saver on (always saving)", async () => {
    webTiers.supported = true;
    chunk.tiers = true;
    network("cellular", true);
    await live();
    await flush();
    expect([card(), chunk.budget, chunk.tierBudget]).toEqual([null, { maxKbps: 1_500, fps: 60 }, { maxKbps: 1_500, fps: 60, maxScale: 1 }]);
    act(() => button()!.click());
    expect(rows(), "Video, VNC and Quality: no mobile-data choice").toHaveLength(5);
  });
});
