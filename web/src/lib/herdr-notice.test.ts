import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { HerdrWatch, herdrFailure, herdrFailureText, herdrWatched, type HerdrApi, type HerdrState } from "./herdr-notice";

const m = (id: string, online = true, engine?: string) => ({ id, online, engine });
const state = (running: boolean, known = true): HerdrState => ({ autostart: false, running, known });

/** A hub answering each machine's row from `rows`; a function throws or answers per call. */
function fakeHub(rows: Record<string, HerdrState | (() => HerdrState)>, start?: (id: string) => HerdrState) {
  const reads: string[] = [];
  const starts: string[] = [];
  const hub: HerdrApi = {
    read: async (id) => {
      reads.push(id);
      const r = rows[id];
      if (!r) throw new ApiError(404, "404 page not found", {});
      return typeof r === "function" ? r() : r;
    },
    start: async (id) => {
      starts.push(id);
      if (!start) throw new Error("no start");
      return start(id);
    },
  };
  return { hub, reads, starts, rows };
}

describe("herdrWatched", () => {
  it("only online machines on classic herdr", () => {
    expect(herdrWatched(m("a"))).toBe(true); // no engine: herdr
    expect(herdrWatched(m("a", true, "herdr-both"))).toBe(true);
    expect(herdrWatched(m("a", true, "herdr-core"))).toBe(false);
    expect(herdrWatched(m("a", true, "orca"))).toBe(false);
    expect(herdrWatched(m("a", false))).toBe(false);
  });

  it("follows the engine set, not only its default (#17037)", () => {
    const set = (hosts: string[], def: string) => ({ ...m("a", true, def), engine_hosts: hosts, engine_default: def });
    expect(herdrWatched(set(["herdr", "orca"], "orca"))).toBe(true); // herdr in the set, Orca the default
    expect(herdrWatched(set(["herdr-core", "orca"], "orca"))).toBe(false);
    expect(herdrWatched(set([], ""))).toBe(false); // no terminal engine
    expect(herdrWatched({ ...set(["herdr", "orca"], "orca"), online: false })).toBe(false);
  });
});

describe("HerdrWatch", () => {
  it("a row only when known not running; Herdr App is not asked", async () => {
    const f = fakeHub({ down: state(false), up: state(true), unsure: state(false, false) });
    const w = new HerdrWatch(f.hub);
    await w.refresh([m("down"), m("up"), m("unsure"), m("app", true, "herdr-core")]);
    expect([...w.snapshot().stopped]).toEqual(["down"]);
    expect(f.reads).not.toContain("app");
  });

  it("a lost read keeps the row, an older hub is not asked again, offline drops it", async () => {
    let fail = false;
    const f = fakeHub({ down: () => { if (fail) throw new ApiError(502, "node did not answer", {}); return state(false); } });
    const w = new HerdrWatch(f.hub);
    await w.refresh([m("down"), m("old")]);
    fail = true;
    await w.refresh([m("down"), m("old")]);
    expect([...w.snapshot().stopped]).toEqual(["down"]);
    expect(f.reads.filter((x) => x === "old")).toHaveLength(1);
    await w.refresh([m("down", false)]);
    expect(w.snapshot().stopped.size).toBe(0);
  });

  it("a start takes the row away even while the hub still says down, until it says running", async () => {
    const f = fakeHub({ mac: state(false) }, () => state(true));
    const w = new HerdrWatch(f.hub);
    await w.refresh([m("mac")]);
    const going = w.start("mac");
    expect(w.snapshot().starting.has("mac")).toBe(true);
    await going;
    expect(w.snapshot().stopped.size).toBe(0);
    expect(w.snapshot().starting.size).toBe(0);
    await w.refresh([m("mac")]); // a hub that only knew node.info
    expect(w.snapshot().stopped.size).toBe(0);
    f.rows.mac = state(true);
    await w.refresh([m("mac")]);
    f.rows.mac = state(false);
    await w.refresh([m("mac")]);
    expect([...w.snapshot().stopped]).toEqual(["mac"]);
    expect(f.starts).toEqual(["mac"]);
  });

  it("a failed start says why in words and keeps the row; the request id is only in the detail", async () => {
    const f = fakeHub({ mac: state(false) }, () => {
      throw new ApiError(502, "herdr server stopped right after starting: exit status 1", {}, true, "req-42");
    });
    const w = new HerdrWatch(f.hub);
    await w.refresh([m("mac")]);
    await w.start("mac");
    const failure = w.snapshot().failures.get("mac")!;
    expect(failure.text).toBe("herdr 启动后立即退出（退出码 1）");
    expect(failure.text).not.toContain("req-42");
    expect(failure.detail).toContain("exit status 1");
    expect(failure.detail).toContain("req-42");
    expect([...w.snapshot().stopped]).toEqual(["mac"]);
  });

  it("a read that changes nothing tells nobody", async () => {
    const f = fakeHub({ down: state(false) });
    const w = new HerdrWatch(f.hub);
    let told = 0;
    w.subscribe(() => told++);
    await w.refresh([m("down")]);
    await w.refresh([m("down")]);
    expect(told).toBe(1);
  });
});

describe("herdrFailureText", () => {
  it("the known failures in words, anything else as it came", () => {
    expect(herdrFailureText("herdr server stopped right after starting: signal: killed")).toBe("herdr 启动后立即被终止（killed）");
    expect(herdrFailureText("herdr server stopped right after starting: exited")).toBe("herdr 启动后立即退出");
    expect(herdrFailureText("herdr is not installed (no herdr command in PATH or the login shell)")).toMatch(/^这台电脑未安装 herdr/);
    expect(herdrFailureText("herdr did not answer on /tmp/h.sock within 20s")).toBe("herdr 已启动，但在 20 秒内未响应");
    expect(herdrFailureText("unknown method herdr.server.start")).toContain("版本过旧");
    expect(herdrFailureText("MacBook: machine is offline")).toBe("这台电脑当前离线");
    expect(herdrFailureText("context deadline exceeded")).toMatch(/^herdr 启动耗时较长/);
    expect(herdrFailureText("这台电脑没有 herdr")).toBe("这台电脑没有 herdr");
    expect(herdrFailure(new Error("boom")).text).toBe("boom");
  });
});
