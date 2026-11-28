import { describe, expect, it } from "vitest";
import { ago, connFacts, connPhase, connStatus, NO_LINK, phaseExplanation, phaseTitle, phaseTone } from "./connection";

const now = 1_000_000;

describe("connPhase: what the pill says once the socket dropped", () => {
  it("online hides it; no network wins, then a restarting hub, then still trying / given up", () => {
    expect(connPhase("online", { ...NO_LINK, networkUp: false })).toBe("online");
    expect(connPhase("offline", { ...NO_LINK, networkUp: false, restarting: true })).toBe("no_network");
    expect(connPhase("connecting", { ...NO_LINK, restarting: true })).toBe("restarting");
    expect(connPhase("connecting", NO_LINK)).toBe("connecting");
    expect(connPhase("offline", NO_LINK)).toBe("unreachable");
  });

  it("titles and explanations like the app's", () => {
    expect(phaseTitle("no_network")).toBe("无网络连接");
    expect(phaseTitle("restarting")).toBe("hub 正在更新…");
    expect(phaseTitle("connecting")).toBe("连接中…");
    expect(phaseTitle("unreachable")).toBe("无法连接 hub，正在重试…");
    expect(phaseExplanation("unreachable")).toContain("系统将持续自动重试");
  });
});

describe("connFacts", () => {
  it("the next try counts down, then 正在重试…; since when the page was last connected", () => {
    const link = { ...NO_LINK, nextTry: now + 4_200, lastOnline: now - 75_000 };
    expect(connFacts("unreachable", link, now)).toEqual(["5 秒后重试", "上次连接：1 分钟前"]);
    expect(connFacts("connecting", { ...link, nextTry: now - 10 }, now)).toEqual(["正在重试…", "上次连接：1 分钟前"]);
  });

  it("no network has no next try to promise; online says nothing; a page never connected has no 上次连接", () => {
    expect(connFacts("no_network", { ...NO_LINK, nextTry: now + 3_000, lastOnline: now - 5_000 }, now)).toEqual(["上次连接：5 秒前"]);
    expect(connFacts("online", { ...NO_LINK, nextTry: now + 3_000 }, now)).toEqual([]);
    expect(connFacts("connecting", { ...NO_LINK, nextTry: now + 1_000 }, now)).toEqual(["1 秒后重试"]);
  });

  it("ago: seconds, minutes, then the time of day", () => {
    expect(ago(now - 12_000, now)).toBe("12 秒前");
    expect(ago(now - 3 * 60_000, now)).toBe("3 分钟前");
    expect(ago(now - 2 * 3600_000, now)).toMatch(/\d{1,2}:\d{2}/);
  });

  it("ago: the time alone the same day, with the date once it was another day", () => {
    const evening = new Date(2026, 10, 5, 22, 30).getTime();
    expect(ago(new Date(2026, 10, 5, 14, 5).getTime(), evening)).toBe("14:05");
    expect(ago(new Date(2026, 10, 4, 14, 5).getTime(), evening)).toBe("11月4日 14:05");
  });
});

describe("the dot and the sidebar's footer follow the same phase as the pill", () => {
  it("phaseTone: red steady once given up, orange breathing (and marked busy for reduced motion) while trying or restarting, orange steady without network", () => {
    expect(phaseTone("unreachable")).toEqual({ color: "bg-red", breathing: false });
    expect(phaseTone("restarting")).toEqual({ color: "bg-orange busy-motion", breathing: true });
    expect(phaseTone("connecting")).toEqual({ color: "bg-orange busy-motion", breathing: true });
    expect(phaseTone("no_network")).toEqual({ color: "bg-orange", breathing: false });
    expect(phaseTone("online")).toEqual({ color: "bg-green", breathing: false });
  });

  it("connStatus: the computers online, else the pill's words for no network and a restart", () => {
    expect(connStatus("online", 2, 3)).toContain("2");
    expect(connStatus("no_network", 2, 3)).toBe("无网络连接");
    expect(connStatus("restarting", 2, 3)).toBe("hub 正在更新…");
    expect(connStatus("connecting", 0, 0)).not.toBe(connStatus("unreachable", 0, 0));
  });
});
