// An extension's sign-in (#4507): its state in words, the buttons' words, what the list adds, the pages left out, the
// tools in "About", how a step is drawn, and the polling of a device code / QR step until it is done or another step.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ listeners: new Map<string, (msg: Record<string, unknown>) => void>(), features: ["extensions", "extension_auth"] }));
vi.mock("./store", async (original) => ({
  ...(await original<typeof import("./store")>()),
  getState: () => ({ features: hub.features }),
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    hub.listeners.set(type, l);
    return () => hub.listeners.delete(type);
  },
}));
const calls = vi.hoisted(() => [] as { path: string; init?: unknown }[]);
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  api: vi.fn(async (path: string, init?: unknown) => {
    calls.push({ path, init });
    return {};
  }),
}));

import {
  askSignIn, authLogout, authStateText, authStep, authStepView, authTone, canSignOut, isPolled, listAuthNote, needsSignIn, onAuthChange, pollAuth,
  pollDelay, safeLink, shownPages, signInAsked, signInLabel, takeSignIn, toolText, withAuth, type AuthPrompt, type ExtensionAuth,
} from "./extension-auth";

const auth = (state: ExtensionAuth["state"], over: Partial<ExtensionAuth> = {}): ExtensionAuth => ({ title: "飞书登录", state, ...over });

describe("extension sign-in words", () => {
  it("says the state, with the account or the reason", () => {
    expect(authStateText(auth("signed_out"))).toBe("未登录");
    expect(authStateText(auth("pending"))).toBe("等待确认");
    expect(authStateText(auth("signed_in", { account: "me@example.com" }))).toBe("已登录：me@example.com");
    expect(authStateText(auth("signed_in"))).toBe("已登录");
    expect(authStateText(auth("expired"))).toBe("已过期");
    expect(authStateText(auth("error", { message: "网络不通" }))).toBe("错误：网络不通");
    expect(authStateText(auth("unknown"))).toBe("扩展未运行");
    expect(authStateText(auth("weird"))).toBe("weird");
  });

  it("colors it and names the buttons", () => {
    expect([authTone(auth("signed_in")), authTone(auth("expired")), authTone(auth("error")), authTone(auth("unknown"))]).toEqual(["green", "orange", "red", "idle"]);
    expect(signInLabel(auth("signed_out"))).toBe("登录");
    expect(signInLabel(auth("pending"))).toBe("登录");
    expect(signInLabel(auth("signed_in"))).toBe("重新登录");
    expect(signInLabel(auth("expired"))).toBe("重新登录");
    expect(canSignOut(auth("signed_in"))).toBe(true);
    expect(canSignOut(auth("expired"))).toBe(false);
  });

  it("marks the list's unsigned extensions", () => {
    expect(listAuthNote(auth("signed_out"))).toBe(" · 未登录");
    expect(listAuthNote(auth("expired"))).toBe(" · 未登录");
    expect(listAuthNote(auth("signed_in"))).toBe("");
    expect(listAuthNote(auth("unknown"))).toBe("");
    expect(listAuthNote(undefined)).toBe("");
    expect(needsSignIn(auth("signed_out"))).toBe(true);
  });

  it("leaves the older apps' sign-in page out of the pages", () => {
    const pages = [{ id: "login" }, { id: "board" }];
    expect(shownPages(pages, auth("signed_out", { page: "login" })).map((p) => p.id)).toEqual(["board"]);
    expect(shownPages(pages, auth("signed_out")).map((p) => p.id)).toEqual(["login", "board"]);
    expect(shownPages(undefined, undefined)).toEqual([]);
  });

  it("lists a tool's version, the machine's own marked", () => {
    expect(toolText({ name: "node", version: "22.23.4", reused: true })).toBe("22.23.4 · 机器上已有");
    expect(toolText({ name: "example-cli", version: "0.59.0" })).toBe("0.59.0");
  });
});

describe("extension sign-in steps", () => {
  const qr = { size: 2, rows: ["10", "01"] };
  it("draws a step by its kind", () => {
    expect(authStepView({ step: "a", fields: [{ name: "code", label: "验证码" }] })).toBe("fields");
    expect(authStepView({ step: "a" })).toBe("unsupported");
    expect(authStepView({ step: "a", kind: "device_code", url: "https://example.com/d", user_code: "AB-CD", qr })).toBe("device_code");
    expect(authStepView({ step: "a", kind: "device_code", url: "https://example.com/d" })).toBe("device_code"); // no code: the link alone
    expect(authStepView({ step: "a", kind: "device_code", url: "javascript:alert(1)" })).toBe("unsupported");
    expect(authStepView({ step: "a", kind: "qr", url: "https://example.com/q", qr })).toBe("qr");
    expect(authStepView({ step: "a", kind: "qr", url: "https://example.com/q" })).toBe("qr_link");
    expect(authStepView({ step: "a", kind: "qr", url: "http://example.com/q" })).toBe("unsupported");
    expect(authStepView({ step: "a", kind: "voice" })).toBe("unsupported");
  });

  it("opens https links only", () => {
    expect(safeLink("https://example.com/x")).toBe("https://example.com/x");
    expect(safeLink("https://example.com/x?hint=me@example.com")).toBe("https://example.com/x?hint=me@example.com");
    expect(safeLink("https://good.example@evil.example/x")).toBe("");
    expect(safeLink("https://me:pw@example.com/x")).toBe("");
    expect(safeLink("https:/admin")).toBe("");
    expect(safeLink("https://@evil.example/x")).toBe("");
    expect(safeLink("https://good.example\\@evil.example/x")).toBe("");
    expect(safeLink("https://example.com/\x00x")).toBe("");
    expect(safeLink("  https://example.com/x  ")).toBe("https://example.com/x");
    expect(safeLink("http://example.com/x")).toBe("");
    expect(safeLink("not a url")).toBe("");
    expect(safeLink(undefined)).toBe("");
  });

  it("polls a code or QR step at its interval", () => {
    expect(isPolled({ step: "a", kind: "qr", qr })).toBe(true);
    expect(isPolled({ step: "a", kind: "qr", qr, done: true })).toBe(false);
    expect(isPolled({ step: "a", fields: [{ name: "x", label: "x" }] })).toBe(false);
    expect(isPolled(null)).toBe(false);
    expect(pollDelay({ kind: "device_code", interval: 2 })).toBe(2000);
    expect(pollDelay({ kind: "device_code" })).toBe(5000);
    expect(pollDelay({ kind: "qr" })).toBe(3000);
    expect(pollDelay({ kind: "qr", interval: 0.2 })).toBe(1000);
  });
});

describe("pollAuth", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());
  const first: AuthPrompt = { step: "wait", kind: "device_code", url: "https://example.com/d", user_code: "AB-CD", interval: 2 };

  it("asks the same step again every interval until it is done", async () => {
    const answers: AuthPrompt[] = [{ ...first, user_code: "EF-GH", interval: 4 }, { done: true, account: "me@example.com" }];
    const ask = vi.fn(async () => answers.shift()!);
    const seen: AuthPrompt[] = [];
    pollAuth(first, ask, (p) => seen.push(p));
    await vi.advanceTimersByTimeAsync(1999);
    expect(ask).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(ask).toHaveBeenCalledWith("wait");
    expect(seen[0].user_code).toBe("EF-GH"); // a new code on the same step is shown
    await vi.advanceTimersByTimeAsync(3999); // the new interval
    expect(ask).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(seen[1].done).toBe(true);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(ask).toHaveBeenCalledTimes(2); // done: no more
  });

  it("stops at another step, and when stopped", async () => {
    const ask = vi.fn(async () => ({ step: "password", fields: [{ name: "p", label: "p" }] }));
    const seen: AuthPrompt[] = [];
    pollAuth(first, ask, (p) => seen.push(p));
    await vi.advanceTimersByTimeAsync(10_000);
    expect(ask).toHaveBeenCalledTimes(1);
    expect(seen[0].step).toBe("password");

    const again = vi.fn(async () => first);
    const stop = pollAuth(first, again, () => {});
    await vi.advanceTimersByTimeAsync(2000);
    stop();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(again).toHaveBeenCalledTimes(1);
  });

  it("keeps asking after a failed ask, and says why", async () => {
    let n = 0;
    const ask = vi.fn(async () => {
      if (n++ === 0) throw new Error("hub away");
      return { done: true };
    });
    const errors: unknown[] = [];
    const seen: AuthPrompt[] = [];
    pollAuth(first, ask, (p) => seen.push(p), (e) => errors.push(e));
    await vi.advanceTimersByTimeAsync(4000);
    expect(errors).toHaveLength(1);
    expect(seen).toEqual([{ done: true }]);
  });

  it("does not poll a step with fields", async () => {
    const ask = vi.fn(async () => first);
    pollAuth({ step: "a", fields: [{ name: "x", label: "x" }] }, ask, () => {});
    await vi.advanceTimersByTimeAsync(60_000);
    expect(ask).not.toHaveBeenCalled();
  });
});

describe("extension sign-in with the hub", () => {
  it("asks the extension's routes", async () => {
    calls.length = 0;
    await authStep("cis", "", {});
    await authStep("cis", "wait");
    await authLogout("cis");
    expect(calls.map((c) => c.path)).toEqual(["/extensions/cis/-/auth/step", "/extensions/cis/-/auth/step", "/extensions/cis/-/auth/logout"]);
    expect(calls[1].init).toEqual({ method: "POST", json: { step: "wait", values: {} } });
  });

  it("takes the pushed state into what holds the extension", () => {
    const got: [string, ExtensionAuth][] = [];
    const from: string[] = [];
    const off = onAuthChange((id, a, manager) => (got.push([id, a]), from.push(manager)));
    hub.listeners.get("extension_auth")!({ type: "extension_auth", extension: "cis", auth: auth("signed_in", { account: "me" }) });
    hub.listeners.get("extension_auth")!({ type: "extension_auth", extension: "cis" }); // no state: ignored
    // another manager's extension of the same id: it says whose (the hub's stamp)
    hub.listeners.get("extension_auth")!({ type: "extension_auth", extension: "cis", manager: "m-1", auth: auth("expired") });
    off();
    expect(got).toEqual([["cis", auth("signed_in", { account: "me" })], ["cis", auth("expired")]]);
    expect(from).toEqual(["assistant", "m-1"]);
    const list = [{ id: "cis" }, { id: "other" }];
    expect(list.map((x) => withAuth(x, "cis", got[0][1]))).toEqual([{ id: "cis", auth: got[0][1] }, { id: "other" }]);
  });

  it("opens the sign-in once after an install asks for it", () => {
    askSignIn("cis");
    expect(signInAsked("cis")).toBe(true);
    expect(takeSignIn("cis")).toBe(true);
    expect(signInAsked("cis")).toBe(false);
    expect(takeSignIn("cis")).toBe(false);
  });
});
