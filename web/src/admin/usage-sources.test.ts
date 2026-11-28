import { describe, expect, it } from "vitest";
import { AdminError } from "./api";
import type { UsageSource } from "./types";
import { hubWords, keyRequired, needsKeyAgain, normalUrl, reasonText, sourceStatus, testText, typeLabel, validUrl } from "./usage-sources";

const NOW = Date.parse("2026-11-05T10:00:00Z");
const source = (o: Partial<UsageSource> = {}): UsageSource => ({ id: "s1", name: "Relay", type: "relay", url: "https://gateway.example.com", has_key: true, enabled: true, status: {}, ...o });

describe("how a source is doing", () => {
  it("is off, paused, not read yet, working or failing", () => {
    expect(sourceStatus(source({ enabled: false }), true, NOW)).toMatchObject({ tone: "gray", label: "已停用" });
    expect(sourceStatus(source(), false, NOW)).toMatchObject({ tone: "gray", label: "读取已关闭" });
    expect(sourceStatus(source(), true, NOW)).toMatchObject({ tone: "gray", label: "尚未读取" });
    const ok = sourceStatus(source({ status: { ok_at: "2026-11-05T09:58:00Z", seen: { accounts: 12, windows: 31, listed: 14, providers: { codex: 12 } } } }), true, NOW);
    expect(ok.tone).toBe("green");
    expect(ok.detail).toContain("12 个账号");
    expect(ok.detail).toContain("31 个窗口");
    const failing = sourceStatus(source({ status: { ok_at: "2026-11-05T08:00:00Z", error: "unauthorized" } }), true, NOW);
    expect(failing.tone).toBe("red");
    expect(failing.detail).toContain("Key 错误或已过期");
    expect(failing.detail).toContain("上次成功");
    expect(sourceStatus(source({ status: { error: "timeout" } }), true, NOW).detail).toBe("请求超时，未收到响应");
  });
  it("words every code the hub can give, and falls back to unreachable", () => {
    for (const code of ["unauthorized", "forbidden", "not_found", "unreachable", "timeout", "bad_response", "too_large", "redirect", "server_error", "bad_config"]) {
      expect(reasonText(code)).not.toBe("");
    }
    expect(reasonText("something new")).toBe(reasonText("unreachable"));
  });
  it("says what a relay read with an API key lacks when its route is missing (#12311)", () => {
    expect(reasonText("not_found", "relay_key")).toBe("此地址未提供 API Key 用量查询接口，请检查地址，或确认中转版本是否过旧");
    expect(reasonText("not_found", "relay")).toBe(reasonText("not_found"));
    expect(sourceStatus(source({ type: "relay_key", status: { error: "not_found" } }), true, NOW).detail).toBe(reasonText("not_found", "relay_key"));
  });
  it("says a key shares no allowances instead of counting zero", () => {
    const hidden = sourceStatus(source({ type: "relay_key", status: { ok_at: "2026-11-05T09:58:00Z", seen: { accounts: 0, windows: 0, listed: 0, providers: {}, quota_hidden: true } } }), true, NOW);
    expect(hidden.tone).toBe("green");
    expect(hidden.detail).toContain("此 Key 未获开放账号额度");
    expect(hidden.detail).not.toContain("0 个账号");
  });
});

describe("the types", () => {
  it("names the three and needs a key for both relay reads", () => {
    expect(typeLabel("relay")).toBe("中转（账号池）");
    expect(typeLabel("relay_key")).toBe("中转（API Key）");
    expect(typeLabel("generic")).toBe("通用格式");
    expect(keyRequired("relay")).toBe(true);
    expect(keyRequired("relay_key")).toBe(true);
    expect(keyRequired("generic")).toBe(false);
  });
});

describe("a test of the connection", () => {
  it("says how many accounts and windows it saw, by provider", () => {
    const r = testText({ ok: true, accounts: 12, windows: 31, listed: 14, providers: { claude: 4, codex: 8 } });
    expect(r.ok).toBe(true);
    expect(r.text).toBe("连接成功：12 个账号，31 个窗口 (codex 8 · claude 4)");
  });
  it("says so when it connected but nothing can be drawn", () => {
    expect(testText({ ok: true, accounts: 0, windows: 0, listed: 3, providers: {} })).toEqual({ ok: true, text: "连接成功，但暂无可显示的额度（网关共列出 3 个账号）" });
  });
  it("says why it did not", () => {
    expect(testText({ ok: false, code: "unauthorized" })).toEqual({ ok: false, text: "连接失败：Key 错误或已过期" });
    expect(testText({ ok: false, code: "not_found" }, "relay_key").text).toContain("未提供 API Key 用量查询接口");
  });
  it("says so when the relay shares no account allowances with the key", () => {
    expect(testText({ ok: true, accounts: 0, windows: 0, listed: 0, providers: {}, quota_hidden: true }, "relay_key")).toEqual({
      ok: true,
      text: "连接成功，但中转尚未对此 Key 开放账号额度。请先在中转端为该 Key 开放权限后重新测试。",
    });
  });
});

describe("the form", () => {
  it("takes an http(s) address without credentials", () => {
    expect(validUrl("https://gateway.example.com")).toBe(true);
    expect(validUrl(" http://192.0.2.5:8080/base ")).toBe(true);
    for (const bad of ["", "gateway.example.com", "ftp://gateway.example.com", "https://me:pw@gateway.example.com", "https://"]) expect(validUrl(bad)).toBe(false);
  });
  it("compares addresses as the hub keeps them", () => {
    expect(normalUrl("https://gateway.example.com/?a=1#x")).toBe("https://gateway.example.com");
    expect(needsKeyAgain({ url: "https://gateway.example.com", type: "relay" }, { url: "https://gateway.example.com/", type: "relay" })).toBe(false);
    expect(needsKeyAgain({ url: "https://gateway.example.com", type: "relay" }, { url: "https://other.example.com", type: "relay" })).toBe(true);
    expect(needsKeyAgain({ url: "https://gateway.example.com", type: "relay" }, { url: "https://gateway.example.com", type: "generic" })).toBe(true);
  });
  it("shows the hub's own words for the errors it words itself, and no other", () => {
    expect(hubWords(new AdminError(400, "地址不对", { code: "invalid_url" }))).toBe("地址不对");
    expect(hubWords(new AdminError(500, "boom", { code: "internal" }))).toBeNull();
    expect(hubWords(new Error("x"))).toBeNull();
  });
});
