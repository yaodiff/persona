import { describe, expect, it } from "vitest";
import { opensAsOrcaChat, orcaChatSwitch, type DefaultOpens, type StartPick } from "./orca-chat";

const opens: DefaultOpens = { launcher: "orca-claude", display_name: "Orca chat · Claude Code", permission_modes: ["default", "acceptEdits", "auto", "bypassPermissions"] };
const keeps: StartPick = { launcher: "", tier: false, model: "", mode: "", ultracode: false };

describe("opensAsOrcaChat", () => {
  it("is the hub's answer while the start keeps the default", () => {
    expect(opensAsOrcaChat(opens, keeps)).toBe(opens);
    expect(opensAsOrcaChat(opens, { ...keeps, host: "orca", mode: "acceptEdits" })).toBe(opens);
  });

  it("is nothing without the hub's answer (the switch off, an older hub)", () => {
    expect(opensAsOrcaChat(undefined, keeps)).toBeNull();
    expect(opensAsOrcaChat(null, keeps)).toBeNull();
  });

  it("is nothing once the start no longer keeps the default", () => {
    for (const pick of [
      { launcher: "claude" },
      { tier: true },
      { model: "opus" },
      { host: "herdr" },
      { mode: "plan" },
      { ultracode: true },
      { serviceTier: "priority" },
    ] satisfies Partial<StartPick>[]) {
      expect(opensAsOrcaChat(opens, { ...keeps, ...pick }), JSON.stringify(pick)).toBeNull();
    }
  });

  it("is nothing for a mode the chat does not take", () => {
    const codex: DefaultOpens = { launcher: "orca-codex", permission_modes: ["default", "auto", "bypassPermissions"] };
    expect(opensAsOrcaChat(codex, { ...keeps, mode: "acceptEdits" })).toBeNull();
    expect(opensAsOrcaChat(codex, { ...keeps, mode: "auto" })).toBe(codex);
  });
});

describe("orcaChatSwitch", () => {
  it("reads the closed set, and anything else as no switch", () => {
    expect(orcaChatSwitch({ orca_chat_starts: "on" })).toBe(true);
    expect(orcaChatSwitch({ orca_chat_starts: "off" })).toBe(false);
    expect(orcaChatSwitch({})).toBeNull();
    // a state a later hub may add is not shown as on or off
    expect(orcaChatSwitch({ orca_chat_starts: "paused" as never })).toBeNull();
  });
});
