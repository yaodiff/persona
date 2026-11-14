// A browser whose credential the hub stopped taking while the page is open (removed in Settings › Devices, web usability
// audit item 38): the socket's handshake is refused, and a browser can't read why, so the page used to retry for about a
// minute as if the hub were out of reach. Now an attempt the hub refused asks once with an ordinary read; its 401 goes to
// the sign-in page (api's handleUnauthorized). A hub that is really out of reach, or a proxy's 5xx for a hub that is
// down, is not asked anything more.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connect, reset } from "./store";
import { handleUnauthorized } from "./api";

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor(readonly url: string) {
    FakeSocket.all.push(this);
  }
  send() {}
  close() {
    this.readyState = 3;
  }
  /** The handshake refused: closed without ever opening, so without a hello. */
  refuse() {
    this.readyState = 3;
    this.onclose?.({ code: 1006 });
  }
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
let answers: Record<string, () => Promise<Response>>;
let fetcher: ReturnType<typeof vi.fn>;
let signedOut: ReturnType<typeof vi.fn<() => void>>;
let before: () => void;
// the health probe and the credential's read (a socket closing before its hello also lets the direction tree be read)
const paths = () => fetcher.mock.calls.map((c) => new URL(String(c[0]), "http://hub").pathname).filter((p) => p === "/api/v1/health" || p === "/api/v1/machines");
const settle = async () => {
  for (let i = 0; i < 10; i++) await Promise.resolve();
  await vi.advanceTimersByTimeAsync(0);
};

beforeEach(() => {
  vi.useFakeTimers();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("location", { protocol: "http:", host: "hub", reload: () => {} });
  vi.stubGlobal("localStorage", { getItem: (k: string) => (k === "persona.token" ? "tok" : null), setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal("window", { setTimeout: (f: () => void, ms: number) => setTimeout(f, ms), clearTimeout: (t: number) => clearTimeout(t) });
  answers = {};
  fetcher = vi.fn((url: string) => (answers[new URL(url, "http://hub").pathname] ?? (() => Promise.resolve(json({}))))());
  vi.stubGlobal("fetch", fetcher);
  signedOut = vi.fn<() => void>();
  before = handleUnauthorized(signedOut);
});

afterEach(() => {
  handleUnauthorized(before);
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("a socket the hub refused", () => {
  it("goes to the sign-in page when the hub no longer takes this browser's credential", async () => {
    answers["/api/v1/health"] = () => Promise.resolve(json({ ok: true }));
    answers["/api/v1/machines"] = () => Promise.resolve(json({ error: "invalid token" }, 401));
    connect();
    FakeSocket.all.at(-1)!.refuse();
    await settle();
    expect(paths()).toEqual(["/api/v1/health", "/api/v1/machines"]);
    expect(signedOut).toHaveBeenCalledTimes(1);
  });

  it("keeps retrying, signed in, when the credential still works", async () => {
    answers["/api/v1/health"] = () => Promise.resolve(json({ ok: true }));
    answers["/api/v1/machines"] = () => Promise.resolve(json({ machines: [] }));
    connect();
    FakeSocket.all.at(-1)!.refuse();
    await settle();
    expect(paths()).toContain("/api/v1/machines");
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("asks nothing more when the hub is out of reach", async () => {
    answers["/api/v1/health"] = () => Promise.reject(new TypeError("Failed to fetch"));
    connect();
    FakeSocket.all.at(-1)!.refuse();
    await settle();
    expect(paths()).toEqual(["/api/v1/health"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("asks nothing more when a proxy in front answers 502 for a hub that is down", async () => {
    answers["/api/v1/health"] = () => Promise.resolve(new Response("Bad Gateway", { status: 502, headers: { "Content-Type": "text/plain" } }));
    connect();
    FakeSocket.all.at(-1)!.refuse();
    await settle();
    expect(paths()).toEqual(["/api/v1/health"]);
    expect(signedOut).not.toHaveBeenCalled();
  });
});
