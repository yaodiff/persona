import { describe, expect, it } from "vitest";
import {
  AckTracker,
  SURFACE_MS,
  canReconnect,
  colourReports,
  dimLine,
  endStatus,
  endText,
  fullBytes,
  fullGrid,
  inHerdrClient,
  otherMode,
  parseMode,
  parsePreference,
  pickMode,
  schemeModeReport,
  schemeReport,
  slideFrom,
  startMode,
  surfacesReducer,
  type AckScheduler,
  type Surfaces,
} from "./terminal-mode";

describe("which mode a terminal opens in", () => {
  it("is 完整界面 until 设置 says otherwise, 单窗格 where the hub cannot", () => {
    expect(parsePreference(null)).toBe("full");
    expect(parsePreference("nonsense")).toBe("full");
    expect(parsePreference("pane")).toBe("pane");
    expect(parsePreference("last")).toBe("last");
    expect(startMode("full", null, true)).toBe("full");
    expect(startMode("full", null, false)).toBe("pane");
    expect(startMode("pane", "full", true)).toBe("pane");
  });
  it("follows the last choice only when 设置 says 记住上次", () => {
    expect(startMode("last", "pane", true)).toBe("pane");
    expect(startMode("last", "full", true)).toBe("full");
    expect(startMode("last", null, true)).toBe("full");
    expect(startMode("full", "pane", true)).toBe("full");
  });
  it("reads only the two names back", () => {
    expect(parseMode("pane")).toBe("pane");
    expect(parseMode("full")).toBe("full");
    expect(parseMode("last")).toBeNull();
    expect(parseMode(null)).toBeNull();
  });
  it("lets a terminal's own switch win, and keeps a machine that failed on 单窗格", () => {
    const base = { supported: true, noFull: false, pref: "full", last: null } as const;
    expect(pickMode({ ...base })).toBe("full");
    expect(pickMode({ ...base, own: "pane" })).toBe("pane");
    expect(pickMode({ ...base, pref: "pane", own: "full" })).toBe("full");
    expect(pickMode({ ...base, noFull: true })).toBe("pane");
    expect(pickMode({ ...base, noFull: true, own: "full" })).toBe("full"); // asked for by hand
    expect(pickMode({ ...base, supported: false, own: "full" })).toBe("pane"); // an old hub has no "Full view" to open
    expect(otherMode("pane")).toBe("full");
    expect(otherMode("full")).toBe("pane");
  });
  it("offers 完整界面 only for what herdr's client can show", () => {
    expect(inHerdrClient("sid-1", "w2:p1")).toBe(true); // an agent in a herdr pane
    expect(inHerdrClient("pane:w2:p1", undefined)).toBe(true); // a herdr shell
    expect(inHerdrClient("sid-1", undefined)).toBe(true); // nothing says it is another host's: as before
    expect(inHerdrClient("sid-2", "botmux:id1")).toBe(false); // a botmux session, in botmux's own tmux
    expect(inHerdrClient("pane:botmux:id1", undefined)).toBe(false); // the same, by its pane's address
    expect(inHerdrClient("pane:orca:t1:l1", undefined)).toBe(false);
    expect(inHerdrClient("sid-3", "herdr-core:t1")).toBe(false);
  });
});

describe("the switch between the surfaces", () => {
  const rest: Surfaces = { shown: "pane", leaving: null, from: null };
  it("sends the shown one out and the chosen one in", () => {
    const s = surfacesReducer(rest, { type: "choose", mode: "full" });
    expect(s).toEqual({ shown: "full", leaving: "pane", from: "pane" });
    expect(surfacesReducer(s, { type: "done" })).toEqual({ shown: "full", leaving: null, from: "pane" });
  });
  it("changes nothing when the shown one is chosen again", () => {
    expect(surfacesReducer(rest, { type: "choose", mode: "pane" })).toBe(rest);
    expect(surfacesReducer(rest, { type: "done" })).toBe(rest);
  });
  it("can switch back before the first has finished fading", () => {
    const s1 = surfacesReducer(rest, { type: "choose", mode: "full" });
    expect(surfacesReducer(s1, { type: "choose", mode: "pane" })).toEqual({ shown: "pane", leaving: "full", from: "full" });
  });
  it("adopts a mode without moving anything", () => {
    expect(surfacesReducer(rest, { type: "adopt", mode: "full" })).toEqual({ shown: "full", leaving: null, from: null });
    expect(surfacesReducer(rest, { type: "adopt", mode: "pane" })).toBe(rest);
  });
  it("slides 完整界面 in from the right and 单窗格 from the left, in the time of the page's other transitions", () => {
    expect(slideFrom("full")).toBe(1);
    expect(slideFrom("pane")).toBe(-1);
    expect(SURFACE_MS).toBeGreaterThanOrEqual(150);
    expect(SURFACE_MS).toBeLessThanOrEqual(300);
  });
});

/** A scheduler the test winds by hand. */
function clock() {
  let now = 0, next = 1;
  const timers = new Map<number, { at: number; fn: () => void }>();
  const scheduler: AckScheduler = {
    set: (fn, ms) => (timers.set(next, { at: now + ms, fn }), next++),
    clear: (id) => void timers.delete(id),
  };
  return {
    scheduler,
    advance(ms: number) {
      now += ms;
      for (const [id, t] of [...timers]) if (t.at <= now) (timers.delete(id), t.fn());
    },
    pending: () => timers.size,
  };
}

describe("acknowledging what xterm has parsed", () => {
  it("tells the total once 64 KB more have been parsed", () => {
    const told: number[] = [];
    const c = clock();
    const ack = new AckTracker((n) => told.push(n), 64 << 10, 250, c.scheduler);
    ack.noteParsed(30 << 10);
    ack.noteParsed(30 << 10);
    expect(told).toEqual([]);
    ack.noteParsed(5 << 10);
    expect(told).toEqual([65 << 10]);
    ack.noteParsed(64 << 10);
    expect(told).toEqual([65 << 10, 129 << 10]); // cumulative, not per message
  });
  it("tells what is left 250 ms after the first byte it has not told", () => {
    const told: number[] = [];
    const c = clock();
    const ack = new AckTracker((n) => told.push(n), 64 << 10, 250, c.scheduler);
    ack.noteParsed(100);
    c.advance(200);
    ack.noteParsed(50); // does not push the time back
    c.advance(49);
    expect(told).toEqual([]);
    c.advance(1);
    expect(told).toEqual([150]);
    expect(c.pending()).toBe(0);
    c.advance(1000);
    expect(told).toEqual([150]); // nothing more parsed, nothing more said
  });
  it("says nothing after it is let go", () => {
    const told: number[] = [];
    const c = clock();
    const ack = new AckTracker((n) => told.push(n), 64 << 10, 250, c.scheduler);
    ack.noteParsed(10);
    ack.dispose();
    c.advance(1000);
    expect(told).toEqual([]);
  });
});

describe("answering the TUI's questions about its terminal", () => {
  it("reports the colour scheme the way CSI ? 997 does", () => {
    expect(schemeReport(true)).toBe("\x1b[?997;1n");
    expect(schemeReport(false)).toBe("\x1b[?997;2n");
    expect(schemeModeReport(true)).toBe("\x1b[?2031;1$y");
    expect(schemeModeReport(false)).toBe("\x1b[?2031;2$y");
  });
  it("reports foreground and background as 16-bit rgb, from #rgb and #rrggbb", () => {
    expect(colourReports("#112233", "#aabbcc")).toBe("\x1b]10;rgb:1111/2222/3333\x1b\\\x1b]11;rgb:aaaa/bbbb/cccc\x1b\\");
    expect(colourReports("#fff", "#000")).toBe("\x1b]10;rgb:ffff/ffff/ffff\x1b\\\x1b]11;rgb:0000/0000/0000\x1b\\");
  });
  it("reports nothing for a colour it cannot read", () => {
    expect(colourReports("rgb(1,2,3)", "#000000")).toBe("");
    expect(colourReports("#000000", "")).toBe("");
  });
});

describe("how a view ends", () => {
  it("separates refusals from ends that can be tried again", () => {
    expect(endStatus({ code: "forbidden", error: "forbidden: …" })).toBe("forbidden");
    expect(endStatus({ unsupported: true, error: "unsupported: …" })).toBe("unsupported");
    expect(endStatus({ code: "too_many" })).toBe("error");
    expect(endStatus({ code: "bad_request" })).toBe("error");
    expect(endStatus({ error: "herdr client exited: 1" })).toBe("closed");
    expect(endStatus({ code: "offline", error: "电脑离线" })).toBe("closed");
    expect(canReconnect("closed")).toBe(true);
    expect(canReconnect("error")).toBe(true);
    expect(canReconnect("unsupported")).toBe(false);
    expect(canReconnect("forbidden")).toBe(false);
    expect(canReconnect("live")).toBe(false);
  });
  it("says why in words", () => {
    expect(endText({ code: "forbidden" })).toContain("仅支持查看");
    expect(endText({ unsupported: true })).toContain("不支持");
    expect(endText({ code: "too_many" })).toContain("太多");
    expect(endText({ code: "offline" })).toBe("电脑离线");
    expect(endText({ error: "herdr client exited: 1" })).toBe("herdr client exited: 1");
    expect(endText({})).toBe("完整界面已断开");
  });
  it("writes the reason dim, on its own line, without control characters of its own", () => {
    expect(dimLine("电脑离线")).toBe("\r\n\x1b[2m电脑离线\x1b[0m\r\n");
    expect(dimLine("a\x1b[31mb\r\nc")).toBe("\r\n\x1b[2ma [31mb  c\x1b[0m\r\n");
  });
});

describe("the grid and the bytes", () => {
  it("opens a view only at a grid worth it", () => {
    expect(fullGrid({ cols: 120, rows: 40 })).toEqual({ cols: 120, rows: 40 });
    expect(fullGrid({ cols: 120.9, rows: 40.2 })).toEqual({ cols: 120, rows: 40 });
    expect(fullGrid({ cols: 10, rows: 40 })).toBeNull(); // a panel folded away
    expect(fullGrid({ cols: 120, rows: 2 })).toBeNull();
    expect(fullGrid({ cols: 501, rows: 40 })).toBeNull();
    expect(fullGrid({ cols: NaN, rows: 40 })).toBeNull();
    expect(fullGrid(null)).toBeNull();
  });
  it("decodes herdr_data and refuses what is not base64", () => {
    expect([...fullBytes(btoa("hi"))!]).toEqual([104, 105]);
    expect(fullBytes("***")).toBeNull();
    expect(fullBytes(5)).toBeNull();
    expect(fullBytes(undefined)).toBeNull();
  });
});
