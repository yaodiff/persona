// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { intercepted, probeEdge, reloadForSignIn, RELOAD_GUARD_MS } from "./edge-auth";
import { dropImportLink, holdImportLink } from "./import-link";

const res = (status: number, type = "basic", ct = "application/json", redirected = false) => ({ status, type, redirected, headers: new Headers({ "Content-Type": ct }) }) as unknown as Response;
const store = () => {
  const m = new Map<string, string>();
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
};

describe("what a layer in front of the hub answers with", () => {
  it("recognises redirects, status 0, refusals and sign-in HTML", () => {
    expect(intercepted(res(0, "opaqueredirect"))).toBe(true);
    expect(intercepted(res(302))).toBe(true);
    expect(intercepted(res(200, "basic", "text/html; charset=utf-8"))).toBe(true);
    expect(intercepted(res(401, "basic", "text/html"))).toBe(true);
    expect(intercepted(res(200, "basic", "application/json", true))).toBe(true);
    expect(intercepted(res(511, "basic", "text/html"))).toBe(true);
  });
  it("leaves the hub's own answers alone", () => {
    expect(intercepted(res(200))).toBe(false);
    expect(intercepted(res(401))).toBe(false); // the hub's JSON 401 is the token's business
    expect(intercepted(res(500, "basic", "text/plain"))).toBe(false);
  });
});

describe("reloading for the sign-in", () => {
  it("reloads once, not again inside the guard window, and again after it", () => {
    const s = store();
    const reload = vi.fn();
    expect(reloadForSignIn(1_000_000, s, reload)).toBe(true);
    expect(reloadForSignIn(1_000_000 + 5_000, s, reload)).toBe(false);
    expect(reloadForSignIn(1_000_000 + RELOAD_GUARD_MS + 1, s, reload)).toBe(true);
    expect(reload).toHaveBeenCalledTimes(2);
  });
  it("puts a provider import link not opened yet back in the address bar first, so the reloaded page opens it (#5420)", () => {
    history.replaceState(null, "", "/settings?tab=models");
    const link = `${location.origin}/settings?tab=models#import=eyJr`;
    holdImportLink(link);
    const at: string[] = [];
    const reload = vi.fn(() => void at.push(location.href));
    // inside the guard window nothing reloads, and the address bar stays clean
    const s = store();
    s.setItem("persona.edge-reload", String(1_000_000));
    expect(reloadForSignIn(1_000_000 + 5_000, s, reload)).toBe(false);
    expect(location.hash).toBe("");
    expect(reloadForSignIn(1_000_000 + RELOAD_GUARD_MS + 1, s, reload)).toBe(true);
    expect(at).toEqual([link]);
    dropImportLink();
    history.replaceState(null, "", "/settings?tab=models");
    expect(reloadForSignIn(1_000_000 + 3 * RELOAD_GUARD_MS, s, reload)).toBe(true);
    expect(at[1]).toBe(`${location.origin}/settings?tab=models`);
  });
  it("never reloads when the guard cannot be kept", () => {
    const reload = vi.fn();
    const broken = { getItem: () => null, setItem: () => { throw new Error("full"); } };
    expect(reloadForSignIn(1, broken, reload)).toBe(false);
    expect(reload).not.toHaveBeenCalled();
  });
});

describe("probing after the socket failed", () => {
  it("does nothing when the network is down or the hub answers", async () => {
    expect(await probeEdge((async () => { throw new TypeError("offline"); }) as typeof fetch)).toBe(false);
    expect(await probeEdge((async () => res(200)) as typeof fetch)).toBe(false);
  });
  it("asks after the credential only when the hub itself answered (web usability audit item 38)", async () => {
    const answered = vi.fn(async () => {});
    await probeEdge((async () => { throw new TypeError("offline"); }) as typeof fetch, answered);
    expect(answered).not.toHaveBeenCalled();
    await probeEdge((async () => res(200)) as typeof fetch, answered);
    expect(answered).toHaveBeenCalledTimes(1);
    // the hub's own JSON refusal is the hub answering too
    await probeEdge((async () => res(401)) as typeof fetch, answered);
    expect(answered).toHaveBeenCalledTimes(2);
  });
  it("asks nothing more when a proxy in front answers for a hub that is down", async () => {
    const answered = vi.fn(async () => {});
    for (const status of [502, 503, 504]) {
      expect(await probeEdge((async () => res(status, "basic", "text/plain")) as typeof fetch, answered)).toBe(false);
    }
    expect(answered).not.toHaveBeenCalled();
  });
  it("asks without following redirects", async () => {
    const f = vi.fn(async (_u: RequestInfo | URL, _i?: RequestInit) => res(200));
    await probeEdge(f as unknown as typeof fetch);
    expect(f.mock.calls[0][1]).toMatchObject({ redirect: "manual" });
  });
});
