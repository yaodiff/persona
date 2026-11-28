// The phone's tab bar (views/PhoneTabBar.tsx), rendered statically (tests run in zh-Hans).
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

import { PhoneTabBarView, phoneTabOf } from "./PhoneTabBar";

describe("the phone's tab bar", () => {
  it("is on the four main places only", () => {
    expect(phoneTabOf({ name: "persona" })).toBe("assistant");
    expect(phoneTabOf({ name: "persona", sid: "main1" }, "main1")).toBe("assistant");
    expect(phoneTabOf({ name: "persona", sid: "other" }, "main1")).toBeUndefined();
    expect(phoneTabOf({ name: "agent-list" })).toBe("agents");
    expect(phoneTabOf({ name: "history", origin: "external" })).toBe("external");
    expect(phoneTabOf({ name: "history" })).toBeUndefined();
    expect(phoneTabOf({ name: "history", origin: "lark_agent" })).toBeUndefined();
    expect(phoneTabOf({ name: "settings" })).toBe("settings");
    expect(phoneTabOf({ name: "todos" } as never)).toBeUndefined();
    expect(phoneTabOf({ name: "home" })).toBeUndefined();
  });

  it("links the four places and marks the current one", () => {
    const html = renderToStaticMarkup(<PhoneTabBarView tab="external" waiting={0} />);
    expect(html).toMatch(/<a href="\/" aria-label="助手"/);
    expect(html).toMatch(/<a href="\/agents" aria-label="Agents"/);
    expect(html).toMatch(/<a href="\/history\/external" aria-current="page" aria-label="外部"/);
    expect(html).toMatch(/<a href="\/settings" aria-label="设置"/);
    expect(html.match(/<a /g)).toHaveLength(4);
    expect(html).toContain("data-phone-tabs");
    expect(html).toContain("lg:hidden");
  });

  it("carries the agents waiting for you on Agents, none at 0", () => {
    expect(renderToStaticMarkup(<PhoneTabBarView tab="assistant" waiting={2} />)).toContain('aria-label="Agents，2 个需要你"');
    expect(renderToStaticMarkup(<PhoneTabBarView tab="assistant" waiting={0} />)).not.toContain("需要你");
  });
});
