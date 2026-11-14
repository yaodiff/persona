// @vitest-environment jsdom
// How much data the live screen's video may use in the browser (#6558), as the iPhone's ScreenVideoBudget and
// ScreenMobileData: the caps without tiers, the choice with them, what the browser says about its network, and the
// question asked at most twice while nothing is chosen.
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ASKS,
  budgetOf,
  chooseMobileData,
  chosenMobileData,
  currentMobileData,
  markMobileDataAsked,
  MOBILE_KBPS,
  onMobileData,
  readNetwork,
  SAVER_KBPS,
  shouldAskMobileData,
  watchNetwork,
  WIFI_KBPS,
  type Network,
} from "./data-use";

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

const connection = (type?: string, saveData = false) => Object.assign(new EventTarget(), { type, saveData });
const net = (n: Partial<Network>): Network => ({ saveData: false, cellular: false, phone: false, ...n });

describe("the network as the browser tells it", () => {
  it("reads mobile data, Data Saver and a phone; no answer stays unknown", () => {
    expect(readNetwork({ connection: connection("cellular"), coarse: false, shortSide: 1080 })).toEqual({ saveData: false, cellular: true, phone: false });
    expect(readNetwork({ connection: connection("wifi", true), coarse: false, shortSide: 1080 })).toEqual({ saveData: true, cellular: false, phone: false });
    expect(readNetwork({ connection: connection("unknown"), coarse: true, shortSide: 390 }).cellular).toBeNull();
    // Safari and Firefox: no navigator.connection at all
    expect(readNetwork({ connection: null, coarse: true, shortSide: 390 })).toEqual({ saveData: false, cellular: null, phone: true });
    expect(readNetwork({ connection: null, coarse: true, shortSide: 820 }).phone).toBe(false); // a tablet
    expect(readNetwork({ connection: null, coarse: false, shortSide: 390 }).phone).toBe(false); // a narrow desktop window
  });

  it("hears the browser's change events, and nothing where it says nothing", () => {
    const c = connection("wifi");
    const heard = vi.fn();
    const stop = watchNetwork(heard, { connection: c, coarse: false, shortSide: 1080 });
    c.dispatchEvent(new Event("change"));
    stop();
    c.dispatchEvent(new Event("change"));
    expect(heard).toHaveBeenCalledTimes(1);
    expect(() => watchNetwork(heard, { connection: null, coarse: false, shortSide: 1080 })()).not.toThrow();
  });
});

describe("the cap", () => {
  it("without tiers is the iPhone's: Data Saver 1.5, mobile data 4 (a phone's unknown network too), else 12 Mbit/s", () => {
    expect(budgetOf(net({ saveData: true, cellular: true }), null)).toEqual({ maxKbps: SAVER_KBPS, fps: 60 });
    expect(budgetOf(net({ cellular: true }), null)).toEqual({ maxKbps: MOBILE_KBPS, fps: 60 });
    expect(budgetOf(net({ cellular: null, phone: true }), null)).toEqual({ maxKbps: MOBILE_KBPS, fps: 60 });
    expect(budgetOf(net({ cellular: null }), null)).toEqual({ maxKbps: WIFI_KBPS, fps: 60 });
    expect(budgetOf(net({ cellular: false, phone: true }), null)).toEqual({ maxKbps: WIFI_KBPS, fps: 60 }); // a phone on Wi-Fi
  });

  it("with tiers follows the choice on mobile data: Data Saver 1.5 at one pixel a point, Clearer 4; the browser's Data Saver always saves", () => {
    expect(budgetOf(net({ cellular: true }), "saver")).toEqual({ maxKbps: SAVER_KBPS, fps: 60, maxScale: 1 });
    expect(budgetOf(net({ cellular: true }), "clearer")).toEqual({ maxKbps: MOBILE_KBPS, fps: 60 });
    expect(budgetOf(net({ saveData: true }), "clearer")).toEqual({ maxKbps: SAVER_KBPS, fps: 60, maxScale: 1 });
    expect(budgetOf(net({ cellular: false }), "saver")).toEqual({ maxKbps: WIFI_KBPS, fps: 60 }); // Wi-Fi: as clear as it goes
    expect(budgetOf(net({ cellular: null, phone: true }), "saver")).toEqual({ maxKbps: MOBILE_KBPS, fps: 60 }); // not known to be mobile data
  });

  it("offers the choice only on mobile data the browser names, with its Data Saver off", () => {
    expect([net({ cellular: true }), net({ cellular: true, saveData: true }), net({ cellular: null, phone: true }), net({})].map(onMobileData)).toEqual([true, false, false, false]);
  });
});

describe("the choice", () => {
  it("starts on Data Saver, asks at most twice unanswered, and an answer is kept and stops the asking", () => {
    expect([chosenMobileData(), currentMobileData(), shouldAskMobileData()]).toEqual([null, "saver", true]);
    for (let i = 0; i < ASKS; i++) markMobileDataAsked();
    expect(shouldAskMobileData()).toBe(false);
    localStorage.clear();
    chooseMobileData("clearer");
    expect([chosenMobileData(), currentMobileData(), shouldAskMobileData()]).toEqual(["clearer", "clearer", false]);
  });

  it("works with storage off: Data Saver, and asked again", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(() => chooseMobileData("clearer")).not.toThrow();
    expect([currentMobileData(), shouldAskMobileData()]).toEqual(["saver", true]);
  });
});
