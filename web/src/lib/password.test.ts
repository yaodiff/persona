import { afterEach, describe, expect, it, vi } from "vitest";
import { UNREVOKED_KEY } from "./auth";
import { loginWithPassword, PakeClient, passwordSuite, readNativeLogin, registerPassword } from "./password";

class PakeWorker {
  onmessage?: (e: { data: unknown }) => void;
  onerror?: () => void;
  terminated = false;
  postMessage({ id, input }: { id: number; input: { op: string } }) {
    queueMicrotask(() => this.onmessage?.({ data: { id, message: `blinded-${input.op}` } }));
  }
  terminate() { this.terminated = true; }
}

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("password protocol transport", () => {
  it("sends only protocol messages during registration and login", async () => {
    vi.stubGlobal("isSecureContext", true);
    vi.stubGlobal("Worker", PakeWorker);
    const password = "never-send-this-password";
    const adminBodies: unknown[] = [];
    await registerPassword(password, "revision", false, async <T>(path: string, _method: "POST", body: unknown) => {
      adminBodies.push(body);
      return (path.endsWith("/start") ? { session: "r", message: "server-registration", identity: "hub", suite: passwordSuite } : {}) as T;
    });
    const fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ session: "s", message: "server-proof", identity: "hub", suite: passwordSuite })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: "jd_device", id: "dev_device", hub_id: "hub" })));
    vi.stubGlobal("fetch", fetch);
    expect(await loginWithPassword(password, null)).toMatchObject({ token: "jd_device" });
    const bodies = [...adminBodies, ...fetch.mock.calls.map((c) => JSON.parse(c[1].body))];
    expect(JSON.stringify(bodies)).not.toContain(password);
    for (const body of bodies) expect(body).not.toHaveProperty("password");
    expect(fetch.mock.calls[0][1]).toMatchObject({ redirect: "error", cache: "no-store", credentials: "omit" });
  });

  it("rejects insecure contexts before starting password work", async () => {
    vi.stubGlobal("isSecureContext", false);
    const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
    await expect(loginWithPassword("password", null)).rejects.toThrow("HTTPS");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("validates the native authorization request without accepting redirect destinations", () => {
    const state = "S".repeat(43), challenge = "C".repeat(43);
    const href = `https://hub.example/login/device#state=${state}&challenge=${challenge}&kind=phone&name=Phone`;
    expect(readNativeLogin(href)).toEqual({ state, challenge, kind: "phone", name: "Phone" });
    expect(() => readNativeLogin(href.replace(challenge, "invalid"))).toThrow();
    for (const key of ["state", "challenge", "kind", "name"]) {
      expect(() => readNativeLogin(`${href}&${key}=duplicate`)).toThrow();
    }
    expect(readNativeLogin("https://hub.example/")).toBeNull();
  });

  it("terminates stalled password computation and rejects further work", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("isSecureContext", true);
    class StalledWorker extends PakeWorker { postMessage() {} }
    vi.stubGlobal("Worker", StalledWorker);
    const client = new PakeClient();
    const result = expect(client.call("login-start", { password: "test" })).rejects.toThrow("超时");
    await vi.advanceTimersByTimeAsync(120_000);
    await result;
    await expect(client.call("login-start")).rejects.toThrow("已结束");
  });

  it("sends replaces for a previous browser token and clears the unrevoked marker", async () => {
    vi.stubGlobal("isSecureContext", true);
    vi.stubGlobal("Worker", PakeWorker);
    const store = new Map<string, string>([[UNREVOKED_KEY, "dev_old_web"]]);
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => { store.set(k, v); },
      removeItem: (k: string) => { store.delete(k); },
    });
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ session: "s", message: "server-proof", identity: "hub", suite: passwordSuite })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: "jd_new", id: "dev_new", hub_id: "hub" })));
    vi.stubGlobal("fetch", fetch);

    await loginWithPassword("secret-pass", null);
    const startBody = JSON.parse(fetch.mock.calls[0][1].body);
    expect(startBody.replaces).toBe("dev_old_web");
    expect(store.has(UNREVOKED_KEY)).toBe(false);
  });

  it("propagates 400 from /start instead of silently retrying without replaces", async () => {
    vi.stubGlobal("isSecureContext", true);
    vi.stubGlobal("Worker", PakeWorker);
    const store = new Map<string, string>([[UNREVOKED_KEY, "dev_old_web"]]);
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => { store.set(k, v); },
      removeItem: (k: string) => { store.delete(k); },
    });
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: "invalid request" }), { status: 400 }));
    vi.stubGlobal("fetch", fetch);

    await expect(loginWithPassword("secret-pass", null)).rejects.toThrow("invalid request");
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(JSON.parse(fetch.mock.calls[0][1].body).replaces).toBe("dev_old_web");
    expect(store.get(UNREVOKED_KEY)).toBe("dev_old_web");
  });

  it("omits replaces when replaces option is false for older hubs", async () => {
    vi.stubGlobal("isSecureContext", true);
    vi.stubGlobal("Worker", PakeWorker);
    const store = new Map<string, string>([[UNREVOKED_KEY, "dev_old_web"]]);
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => { store.set(k, v); },
      removeItem: (k: string) => { store.delete(k); },
    });
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ session: "s", message: "server-proof", identity: "hub", suite: passwordSuite })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: "jd_new", id: "dev_new", hub_id: "hub" })));
    vi.stubGlobal("fetch", fetch);

    expect(await loginWithPassword("secret-pass", null, { replaces: false })).toMatchObject({ id: "dev_new" });
    expect(JSON.parse(fetch.mock.calls[0][1].body)).not.toHaveProperty("replaces");
    expect(store.has(UNREVOKED_KEY)).toBe(false);
  });
});
