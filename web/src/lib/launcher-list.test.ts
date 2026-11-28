import { afterEach, describe, expect, it, vi } from "vitest";
import { builtinRows, loadLaunchers, LaunchersTimeout, rcFilesOf, rcGroups, syncStateText, type ListedLauncher } from "./launcher-list";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const rc = (name: string, files?: string[], extra: Partial<ListedLauncher> = {}): ListedLauncher => ({ name, source: "zshrc", rc_files: files, ...extra });

describe("rcGroups", () => {
  it("puts an older hub's launchers (no rc_files) under ~/.zshrc", () => {
    expect(rcFilesOf({})).toEqual(["~/.zshrc"]);
    const g = rcGroups([rc("cc"), rc("cx")]);
    expect(g).toEqual([{ files: "~/.zshrc", launchers: [rc("cc"), rc("cx")] }]);
  });

  it("groups by file, joins several files, and leaves out hidden, managed, built-in and PATH ones", () => {
    const list: ListedLauncher[] = [
      rc("a", ["~/.zshrc"]),
      rc("b", ["~/.bashrc"]),
      rc("c", ["~/.zshrc", "~/.bashrc"]),
      rc("d", ["~/.bashrc", "~/.zshrc"]),
      rc("e", ["~/.bashrc"], { hidden: true }),
      { name: "m", source: "persona" },
      { name: "claude", source: "builtin", machines: ["m1"] },
      { name: "p", source: "path", machines: ["m1"] },
    ];
    expect(rcGroups(list).map((g) => [g.files, g.launchers.map((l) => l.name)])).toEqual([
      ["~/.bashrc", ["b"]],
      ["~/.zshrc", ["a"]],
      ["~/.bashrc / ~/.zshrc", ["c", "d"]],
    ]);
  });
});

describe("builtinRows", () => {
  it("is one row per machine with its version and whether it answered", () => {
    const rows = builtinRows([
      rc("a"),
      { name: "claude", source: "builtin", machines: ["m1", "m2", "m3"], versions: { m1: "2.1.0", m3: "2.0.9" }, stale: ["m3"] },
    ]);
    expect(rows).toEqual([
      { name: "claude", machine: "m1", version: "2.1.0", stale: false },
      { name: "claude", machine: "m2", version: undefined, stale: false },
      { name: "claude", machine: "m3", version: "2.0.9", stale: true },
    ]);
  });

  it("is empty on an older hub", () => {
    expect(builtinRows([rc("a"), { name: "m", source: "persona" }])).toEqual([]);
  });

  it("leaves a hidden built-in claude to the hidden ones", () => {
    expect(builtinRows([{ name: "claude", source: "builtin", machines: ["m1"], hidden: true }])).toEqual([]);
  });
});

describe("syncStateText", () => {
  const now = Date.parse("2026-11-05T10:00:00Z");
  const before = "2026-11-05T09:55:00Z";

  it("names the file a synced machine's list came from", () => {
    expect(syncStateText({ machine: "m1", state: "ok", rc_file: "~/.bashrc" }, now)).toEqual({ label: "已同步", detail: "~/.bashrc", tone: "green" });
  });

  it("says an offline machine shows its last list, or that there is none", () => {
    const s = syncStateText({ machine: "m1", state: "offline", synced_at: before }, now);
    expect(s.label).toBe("离线");
    expect(s.detail).toMatch(/^当前显示上次同步的列表（.*5.*）$/);
    expect(syncStateText({ machine: "m1", state: "offline" }, now).detail).toBe("从未同步");
  });

  it("marks a timeout and a failure", () => {
    expect(syncStateText({ machine: "m1", state: "timeout", synced_at: before }, now)).toMatchObject({ label: "超时", tone: "orange" });
    expect(syncStateText({ machine: "m1", state: "timeout" }, now).detail).toBe("从未同步");
    expect(syncStateText({ machine: "m1", state: "error", error: "zsh: not found" }, now)).toEqual({ label: "失败", detail: "zsh: not found", tone: "red" });
  });
});

describe("loadLaunchers", () => {
  it("returns the list and the sync state, and works without sync (older hubs)", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "jd_test" });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ launchers: [rc("a")] }), { status: 200 })));
    expect(await loadLaunchers()).toEqual({ launchers: [rc("a")], sync: undefined });
  });

  it("gives up after the deadline instead of loading forever", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "jd_test" });
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))),
      ),
    );
    const err = await loadLaunchers(20).catch((e) => e);
    expect(err).toBeInstanceOf(LaunchersTimeout);
    expect(err.message).toContain("未响应");
  });
});
