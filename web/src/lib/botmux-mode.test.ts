// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { BOTMUX_MODE_KEY, getBotmuxMode, readBotmuxMode, setBotmuxMode } from "./botmux-mode";

afterEach(() => localStorage.clear());

describe("botmux mode", () => {
  it("decodes by default", () => {
    expect(readBotmuxMode()).toBe("decode");
  });
  it("uses decoded format for an old hidden preference", () => {
    localStorage.setItem("persona.agentHideBotmux", "1");
    localStorage.setItem(BOTMUX_MODE_KEY, "hide");
    expect(readBotmuxMode()).toBe("decode");
  });
  it("persists the mode without removing visibility migration input", () => {
    localStorage.setItem("persona.agentHideBotmux", "1");
    setBotmuxMode("all");
    expect(getBotmuxMode()).toBe("all");
    expect(localStorage.getItem(BOTMUX_MODE_KEY)).toBe("all");
    setBotmuxMode("decode");
    expect(localStorage.getItem(BOTMUX_MODE_KEY)).toBe("decode");
    expect(localStorage.getItem("persona.agentHideBotmux")).toBe("1");
  });
  it("falls back when the localStorage getter throws", () => {
    const getter = vi.spyOn(globalThis, "localStorage", "get").mockImplementation(() => {
      throw new DOMException("Storage unavailable", "SecurityError");
    });
    try {
      expect(readBotmuxMode()).toBe("decode");
    } finally {
      getter.mockRestore();
    }
  });
});
