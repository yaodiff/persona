import { describe, expect, it } from "vitest";
import { bypassNote, bypassRestarts, leavePlanTo, modeConfirmation, modeMenuItems, pickerIcon } from "./header-mode";
import { listedModeNow, listedModesOf } from "./session-actions";
import { t } from "../i18n";
import type { Session } from "./types";

// The Agent header's permission mode (#3956), the rules of iOS SessionModes (#3719, HeaderModeTests) and Android #3957.
const claude = (permission_mode?: string, extra: Partial<Session> = {}): Session =>
  ({ machine_id: "m", session_id: "s", agent: "claude", status: "idle", permission_mode, ...extra }) as Session;

describe("header mode", () => {
  it("draws each mode with the composer picker's symbol (#5324)", () => {
    expect(["default", "acceptEdits", "plan", "auto", "bypassPermissions"].map(pickerIcon)).toEqual(["palm", "code", "list", "sparkles", "warning"]);
    // a mode not reported yet, however it comes, keeps the raised hand; the label names no mode (#5756)
    expect([undefined, "", null].map(pickerIcon)).toEqual(["palm", "palm", "palm"]);
  });

  it("asks first into Allow all, and out of it where the agent can't come back", () => {
    expect(modeConfirmation(claude("default", { bypass_available: true }), "bypassPermissions")).toBe("allow");
    expect(modeConfirmation(claude("bypassPermissions"), "plan")).toBe("leave");
    // it can come back: no question
    expect(modeConfirmation(claude("bypassPermissions", { bypass_available: true }), "plan")).toBeNull();
    expect(modeConfirmation(claude("default"), "plan")).toBeNull();
    expect(modeConfirmation(claude("plan"), "plan")).toBeNull();
    // an assistant conversation, and Antigravity, switch without asking
    expect(modeConfirmation(claude("default", { assistant: true }), "bypassPermissions")).toBeNull();
    expect(modeConfirmation(claude("bypassPermissions", { agent: "agy" }), "plan")).toBeNull();
  });

  it("offers Allow all only where the agent can take it, and says why not elsewhere", () => {
    const picked: string[] = [];
    const rows = (s: Session, locked?: boolean) => modeMenuItems(s, (m) => picked.push(m), { locked });
    const without = rows(claude("default"));
    const bypass = without.find((r) => r.key === "bypassPermissions")!;
    expect(bypass.disabled).toBe(true);
    expect(bypass.detail).toBeTruthy();
    expect(without.find((r) => r.key === "default")?.checked).toBe(true);
    const offered = rows(claude("default", { bypass_available: true })).find((r) => r.key === "bypassPermissions")!;
    expect(offered.disabled).toBeFalsy();
    expect(offered.danger && offered.confirm).toBeTruthy();
    // in it with no way back: listed, checked, and every other row asks first
    const inIt = rows(claude("bypassPermissions"));
    expect(inIt.find((r) => r.key === "bypassPermissions")?.checked).toBe(true);
    expect(inIt.filter((r) => r.key !== "bypassPermissions").every((r) => r.confirm)).toBe(true);
    // not running: nothing can be picked, and no reason row for Allow all
    const locked = rows(claude("plan", { status: "offline" }), true);
    expect(locked.every((r) => r.disabled)).toBe(true);
    expect(locked.some((r) => r.key === "bypassPermissions")).toBe(false);
    without.find((r) => r.key === "plan")!.onSelect();
    expect(picked).toEqual(["plan"]);
  });
});

// A session of an agent that is not Claude Code lists the modes it takes (#8261, Paseo's Grok, OpenCode, Codex): its own
// approval ("Launcher default": no mode reported is it) and Full access, a switch both ways; only going into Full access asks.
describe("the modes a session lists", () => {
  const grok = (permission_mode?: string, extra: Partial<Session> = {}): Session => claude(permission_mode, {
    agent: "paseo", agent_label: "Grok", source_kind: "runtime", capabilities: { read: true, send: true, modes: false, permission_modes: ["default", "bypassPermissions"] }, ...extra });

  it("are offered alone, by these names, the one it runs in checked", () => {
    expect(listedModesOf(grok())).toEqual(["default", "bypassPermissions"]);
    const rows = modeMenuItems(grok(), () => {});
    expect(rows.map((r) => [r.key, r.label, r.checked])).toEqual([["default", "跟随启动器", true], ["bypassPermissions", "完全访问", false]]);
    expect(modeMenuItems(grok("bypassPermissions"), () => {}).map((r) => r.checked)).toEqual([false, true]);
    expect(listedModeNow(grok("plan"), ["default", "bypassPermissions"])).toBe("default");
  });

  it("ask first only into Full access, and leave it freely", () => {
    const into = modeMenuItems(grok(), () => {}).find((r) => r.key === "bypassPermissions")!;
    expect(into.danger && into.confirm).toBeTruthy();
    const out = modeMenuItems(grok("bypassPermissions"), () => {}).find((r) => r.key === "default")!;
    expect(out.confirm).toBeUndefined();
    expect(modeConfirmation(grok("bypassPermissions"), "default")).toBeNull();
    expect(modeMenuItems(grok(), () => {}, { locked: true }).every((r) => r.disabled)).toBe(true);
  });

  it("are none where Claude Code's chips stand, for a Persona conversation, or with fewer than two", () => {
    expect(listedModesOf(claude("default", { capabilities: { modes: true, permission_modes: ["default", "bypassPermissions"] } }))).toBeUndefined();
    expect(listedModesOf(grok(undefined, { assistant: true }))).toBeUndefined();
    expect(listedModesOf(grok(undefined, { capabilities: { read: true, modes: false, permission_modes: ["default"] } }))).toBeUndefined();
    expect(listedModesOf(grok(undefined, { capabilities: { read: true, modes: false } }))).toBeUndefined();
  });
});

// Full access for a terminal Claude Code started without a bypass flag (#6476): the node restarts the session in it once
// its turn is over, so the row is offered, says so under its name, and still asks first.
describe("Full access by a restart", () => {
  const pane = (permission_mode = "default", extra: Partial<Session> = {}) => claude(permission_mode, { pane_id: "w1:p1", ...extra });
  const bypassRow = (s: Session, assistant?: boolean) => modeMenuItems(s, () => {}, { assistant }).find((r) => r.key === "bypassPermissions")!;

  it("is offered to a terminal Claude Code that cannot reach it with shift+tab, saying it restarts the session", () => {
    const s = pane();
    expect(bypassRestarts(s)).toBe(true);
    const row = bypassRow(s);
    expect(row.disabled).toBeFalsy();
    expect(row.detail).toBe(t("chat-sessiondialogs.bypass_restart_detail"));
    expect(row.confirm).toBe(t("chat-sessiondialogs.bypass_confirm"));
    expect(row.confirmDetail).toContain(t("chat-sessiondialogs.bypass_restart_detail"));
    expect(row.danger).toBe(true);
    // working: still offered, the restart waits for the turn to end
    expect(bypassRow(pane("default", { status: "working", state: "working" } as Partial<Session>)).disabled).toBeFalsy();
  });

  it("says a restart waits once picked", () => {
    const row = bypassRow(pane("acceptEdits", { bypass_pending: true }));
    expect(row.detail).toBe(t("chat-sessiondialogs.bypass_pending"));
    expect(bypassNote(pane("acceptEdits", { bypass_pending: true }))).toBe(t("chat-sessiondialogs.bypass_pending"));
  });

  it("needs no restart where shift+tab reaches it, nor for the agents the node cannot restart", () => {
    expect(bypassRestarts(pane("default", { bypass_available: true }))).toBe(false);
    expect(bypassNote(pane("default", { bypass_available: true }))).toBe("");
    expect(bypassRestarts(pane("bypassPermissions"))).toBe(false);
    // Antigravity switches itself; a session with no pane, Codex and a read-only source have no restart
    for (const s of [pane("default", { agent: "agy" }), claude("default"), pane("default", { agent: "codex" }), pane("default", { source_kind: "codex_app_server", capabilities: { send: true, terminal: true } } as Partial<Session>)]) {
      expect(bypassRestarts(s)).toBe(false);
    }
    expect(bypassRow(claude("default")).disabled).toBe(true);
  });

  it("tells a Persona conversation that Full access applies from its next message", () => {
    expect(bypassNote(claude("default", { assistant: true }), true)).toBe(t("chat-sessiondialogs.bypass_next_message"));
    expect(bypassRow(claude("default", { assistant: true }), true).detail).toBe(t("chat-sessiondialogs.bypass_next_message"));
    expect(bypassNote(claude("bypassPermissions", { assistant: true }), true)).toBe("");
  });

  it("omits unavailable Auto for an Antigravity session whose capabilities restrict permission_modes (#17239)", () => {
    const noAuto = ["default", "acceptEdits", "plan", "bypassPermissions"];
    const agyAssistant = claude("bypassPermissions", {
      agent: "agy",
      assistant: true,
      source_kind: "runtime",
      capabilities: { read: true, send: true, modes: true, permission_modes: noAuto },
    });
    const agyAgent = claude("acceptEdits", {
      agent: "agy",
      source_kind: "runtime",
      capabilities: { read: true, send: true, modes: true, permission_modes: noAuto },
    });
    expect(modeMenuItems(agyAssistant, () => {}, { assistant: true }).map((r) => r.key)).toEqual(noAuto);
    expect(modeMenuItems(agyAgent, () => {}).map((r) => r.key)).toEqual(noAuto);
    expect(modeMenuItems(agyAgent, () => {})[0].label).toBe(t("lib-session-actions.mode_default"));
    expect(leavePlanTo(agyAgent, "auto")).toBe("default");
  });
});
