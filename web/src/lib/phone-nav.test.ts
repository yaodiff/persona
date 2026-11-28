// Settings › Advanced › Appearance › Phone navigation (lib/phone-nav.ts): Top switch is the default and stores nothing.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  const kept = new Map<string, string>();
  (globalThis as Record<string, unknown>).localStorage = {
    getItem: (k: string) => kept.get(k) ?? null,
    setItem: (k: string, v: string) => void kept.set(k, v),
    removeItem: (k: string) => void kept.delete(k),
    clear: () => kept.clear(),
  };
});

import { PHONE_NAV_KEY, phoneNavMode, setPhoneNavMode } from "./phone-nav";

describe("phone navigation", () => {
  beforeEach(() => localStorage.clear());

  it("is the top switch until chosen otherwise, and an unknown value reads as it", () => {
    expect(phoneNavMode()).toBe("switch");
    localStorage.setItem(PHONE_NAV_KEY, "something-else");
    expect(phoneNavMode()).toBe("switch");
  });

  it("keeps the tab bar in this browser and stores nothing for the default", () => {
    setPhoneNavMode("tabs");
    expect(localStorage.getItem(PHONE_NAV_KEY)).toBe("tabs");
    expect(phoneNavMode()).toBe("tabs");
    setPhoneNavMode("switch");
    expect(localStorage.getItem(PHONE_NAV_KEY)).toBeNull();
    expect(phoneNavMode()).toBe("switch");
  });
});
