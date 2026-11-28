import { describe, expect, it } from "vitest";
import { blockReason, initialLauncher, launcherBlock, NOT_IN_TERMINAL, usableFirst } from "./launcher-health";
import { launcherBlockedReason, launcherErrorText } from "./machine-alerts";
import type { Launcher } from "./types";

// A launcher's health on the computer "New agent" is for (#1643, docs/server/launcher-health.md): only a blocked record
// grays it out, with a reason for its class; an older hub sends none and nothing changes.
const l = (name: string, health?: Launcher["health"]): Launcher => ({ name, ...(health ? { health } : {}) });

describe("launcherBlock", () => {
  it("is nothing for an older hub, a working launcher, or one the hub is still retrying", () => {
    expect(launcherBlock(l("claude"))).toBeNull();
    expect(launcherBlock(undefined)).toBeNull();
    expect(launcherBlock(l("claude", { state: "ok", checked_at: "2026-11-09T08:00:00Z" }))).toBeNull();
    expect(launcherBlock(l("a", { state: "failing", kind: "timeout" }))).toBeNull();
    expect(launcherBlock(l("a", { state: "repairing", kind: "auth" }))).toBeNull();
  });

  it("says a launcher the terminal can't run is not available there", () => {
    const b = launcherBlock(l("work_gw", { state: "blocked", kind: NOT_IN_TERMINAL, error: "  bash: work_gw: command not found\n" }))!;
    expect(b.notInTerminal).toBe(true);
    expect(b.reason).toBe("无法在该电脑的终端中使用");
    expect(b.title).toBe("终端中不可用");
    expect(b.detail).toBe("bash: work_gw: command not found");
  });

  it("strips terminal escapes from the detail an older hub sends", () => {
    const b = launcherBlock(l("work_gw", {
      state: "blocked",
      kind: NOT_IN_TERMINAL,
      error: "\u001b[31mbash: work_gw: command not found\u001b[0m\n",
    }))!;
    expect(b.detail).toBe("bash: work_gw: command not found");
    const noise = launcherBlock(l("a", {
      state: "blocked",
      kind: "auth",
      error: "Restart Zsh to retry gitstatus initialization: \u001b[32mexec zsh\u001b[39m \u001b]1337;CurrentDir=/Users/dev\u0007 Error: 401",
    }))!;
    expect(noise.detail).not.toContain("\u001b");
    expect(noise.detail).toContain("Error: 401");
  });

  it("gives other blocked classes their own line, and a generic one for a class it doesn't know", () => {
    const kinds = ["auth", "model_route", "cli_outdated", "timeout", "other"];
    const reasons = kinds.map((kind) => launcherBlock(l("a", { state: "blocked", kind }))!.reason);
    expect(new Set(reasons).size).toBe(kinds.length);
    expect(reasons).not.toContain(blockReason(NOT_IN_TERMINAL));
    expect(launcherBlock(l("a", { state: "blocked", kind: "from_a_newer_hub" }))!.reason).toBe(blockReason("other"));
    const none = launcherBlock(l("a", { state: "blocked", error: "   " }))!;
    expect(none.kind).toBe("");
    expect(none.notInTerminal).toBe(false);
    expect(none.detail).toBeUndefined();
    expect(none.title).toBe("该电脑上不可用");
  });
});

describe("launcher record text", () => {
  it("strips escapes before a line or a reason is shown", () => {
    expect(launcherErrorText({ error: "  \u001b[1mfatal: boom\u001b[0m  " })).toBe("fatal: boom");
    expect(launcherErrorText({})).toBe("");
    expect(launcherBlockedReason({ blocked_reason: "\u001b[33mnot available in this terminal\u001b[0m" })).toBe("not available in this terminal");
    expect(launcherBlockedReason({})).toBe("");
  });
});

describe("initialLauncher", () => {
  const blocked = l("work_gw", { state: "blocked", kind: NOT_IN_TERMINAL });
  const list = [blocked, l("claude", { state: "ok" }), l("sonnetcc")];

  it("keeps the last one used unless this computer can't start it now", () => {
    expect(initialLauncher("sonnetcc", list)).toBe("sonnetcc");
    expect(initialLauncher("work_gw", list)).toBe("claude");
    expect(initialLauncher("gone", list)).toBe("claude");
    expect(initialLauncher(null, list)).toBe("claude");
  });

  it("still has one to start with when every launcher is blocked", () => {
    const all = [blocked, l("team_gw", { state: "blocked", kind: "auth" })];
    expect(initialLauncher("team_gw", all)).toBe("team_gw");
    expect(initialLauncher(undefined, all)).toBe("work_gw");
    expect(initialLauncher("x", [])).toBe("");
  });
});

describe("usableFirst", () => {
  it("puts the blocked ones after the rest, each group in the hub's order", () => {
    const list = [l("work_gw", { state: "blocked", kind: NOT_IN_TERMINAL }), l("claude", { state: "ok" }), l("team_gw", { state: "blocked", kind: "auth" }), l("slow", { state: "failing" })];
    expect(usableFirst(list).map((x) => x.name)).toEqual(["claude", "slow", "work_gw", "team_gw"]);
    expect(usableFirst([])).toEqual([]);
  });
});
