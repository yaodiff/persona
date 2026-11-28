// Pins and mutes are optimistic and go to the hub as a whole PUT (#4190): a refresh() sent before a change does not
// put the old value back, and a failed change does not undo a later one.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../ui/atoms", () => ({ toast: vi.fn() }));

import { flipMute, flipPin, interruptAgent } from "./agent-actions";
import { chooseManager, pinManager } from "./manager-choice";
import { getState, refresh, reset } from "./store";
import type { Session } from "./types";

const agent = (id: string): Session => ({ machine_id: "m", session_id: id, cwd: "/projects/demo", title: id, status: "idle" });

interface Held {
  method: string;
  path: string;
  answer: (body: unknown, status?: number) => void;
}

/** GET /pins, GET /mutes and every PUT wait until the test answers them; other paths answer at once. */
function heldHub(): Held[] {
  const held: Held[] = [];
  vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    const path = url.replace(/^.*\/api\/v1/, "").split("?")[0]!;
    const method = init?.method ?? "GET";
    if (path !== "/pins" && path !== "/mutes" && !path.endsWith("/interrupt")) return Promise.resolve(new Response("{}", { status: 200 }));
    return new Promise<Response>((r) => held.push({ method, path, answer: (body, status = 200) => r(new Response(JSON.stringify(body), { status })) }));
  }));
  return held;
}

const find = (held: Held[], method: string, path: string, nth = 0) => held.filter((h) => h.method === method && h.path === path)[nth]!;
const settle = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  reset();
  vi.stubGlobal("localStorage", { getItem: () => "mock-token", setItem: () => {}, removeItem: () => {} });
});

afterEach(() => {
  vi.unstubAllGlobals();
  reset();
});

it("shares a pending interrupt for one Agent, delivers its failure to both callers and permits a retry", async () => {
  const requests = heldHub();
  const first = interruptAgent("m", "a");
  const second = interruptAgent("m", "a");
  await settle();
  expect(requests).toHaveLength(1);
  requests[0].answer({ state: "unknown" });
  const results = await Promise.allSettled([first, second]);
  expect(results.map((result) => result.status)).toEqual(["rejected", "rejected"]);
  const retry = interruptAgent("m", "a");
  await settle();
  expect(requests).toHaveLength(2);
  requests[1].answer({ state: "ok" });
  await retry;
});

it("shares a pending interrupt across a switch of the displayed assistant when the route stays the same", async () => {
  const requests = heldHub();
  const first = interruptAgent("m", "a");
  chooseManager("another-assistant");
  try {
    const second = interruptAgent("m", "a");
    await settle();
    expect(requests).toHaveLength(1);
    requests[0].answer({ state: "ok" });
    await Promise.all([first, second]);
  } finally {
    chooseManager(null);
  }
});

it.each(["manager", "credential"])("separates pending interrupts when the %s changes", async (scope) => {
  const requests = heldHub();
  const first = interruptAgent("m", "a");
  const release = scope === "manager" ? pinManager("/sessions/m/a", "next") : () => {};
  if (scope === "credential") vi.stubGlobal("localStorage", { getItem: () => "another-mock-token", setItem() {}, removeItem() {} });
  try {
    const next = interruptAgent("m", "a");
    await settle();
    expect(requests).toHaveLength(2);
    requests.forEach((request) => request.answer({ state: "ok" }));
    const results = await Promise.allSettled([first, next]);
    expect(results.map((result) => result.status)).toEqual(["rejected", "fulfilled"]);
  } finally {
    release();
  }
});

describe("pins and mutes: an older answer does not undo a newer change", () => {
  it("a refresh() on its way when an agent is pinned does not unpin it", async () => {
    const held = heldHub();
    const slow = refresh();
    await settle();
    const pin = flipPin(agent("a"));
    await settle();
    find(held, "PUT", "/pins").answer({});
    await pin;
    find(held, "GET", "/pins").answer({ spaces: [], sessions: [] }); // what the hub had before the pin
    find(held, "GET", "/mutes").answer({ spaces: [], sessions: [], allowed: [] });
    await slow;
    expect(getState().loaded).toBe(true);
    expect(getState().pins.sessions).toEqual(["m/a"]);
  });

  it("a refresh() on its way when an agent is muted does not unmute it", async () => {
    const held = heldHub();
    const slow = refresh();
    await settle();
    const mute = flipMute(agent("a"));
    await settle();
    find(held, "PUT", "/mutes").answer({});
    await mute;
    find(held, "GET", "/pins").answer({ spaces: [], sessions: [] });
    find(held, "GET", "/mutes").answer({ spaces: [], sessions: [], allowed: [] });
    await slow;
    expect(getState().mutes.sessions).toEqual(["m/a"]);
  });

  it("a failed pin does not take away a later pin that went through", async () => {
    const held = heldHub();
    const first = flipPin(agent("a"));
    const second = flipPin(agent("b"));
    await settle();
    find(held, "PUT", "/pins", 1).answer({});
    await second;
    find(held, "PUT", "/pins", 0).answer({ error: "busy" }, 503);
    await first;
    expect([...getState().pins.sessions].sort()).toEqual(["m/a", "m/b"]);
  });

  it("a failed mute does not take away a later mute that went through", async () => {
    const held = heldHub();
    const first = flipMute(agent("a"));
    const second = flipMute(agent("b"));
    await settle();
    find(held, "PUT", "/mutes", 1).answer({});
    await second;
    find(held, "PUT", "/mutes", 0).answer({ error: "busy" }, 503);
    await first;
    expect(getState().mutes.sessions).toEqual(["m/a", "m/b"]);
  });

  it("the last change failing still puts back what was before it", async () => {
    const held = heldHub();
    const pin = flipPin(agent("a"));
    await settle();
    find(held, "PUT", "/pins").answer({ error: "busy" }, 503);
    await pin;
    expect(getState().pins.sessions).toEqual([]);
  });

  it("a refresh() with no change meanwhile takes the hub's pins and mutes", async () => {
    const held = heldHub();
    const r = refresh();
    await settle();
    find(held, "GET", "/pins").answer({ spaces: [], sessions: ["m/elsewhere"] });
    find(held, "GET", "/mutes").answer({ spaces: [], sessions: ["m/quiet"], allowed: [] });
    await r;
    expect(getState().pins.sessions).toEqual(["m/elsewhere"]);
    expect(getState().mutes.sessions).toEqual(["m/quiet"]);
  });
});

describe("pins and mutes: a refresh() or failure that outlives the page's own changes", () => {
  it("a refresh() sent while a pin's PUT is still out does not unpin it, whichever the hub handled first", async () => {
    const held = heldHub();
    const pin = flipPin(agent("a"));
    await settle();
    const r = refresh();
    await settle();
    find(held, "GET", "/pins").answer({ spaces: [], sessions: [] }); // the hub read before it wrote
    find(held, "GET", "/mutes").answer({ spaces: [], sessions: [], allowed: [] });
    await r;
    find(held, "PUT", "/pins").answer({});
    await pin;
    expect(getState().pins.sessions).toEqual(["m/a"]);
  });

  it("a refresh() of the old login that lands after reset() does not bring its pins or mutes back", async () => {
    const held = heldHub();
    const r = refresh();
    await settle();
    reset();
    find(held, "GET", "/pins").answer({ spaces: [], sessions: ["m/old-login"] });
    find(held, "GET", "/mutes").answer({ spaces: [], sessions: ["m/old-login"], allowed: [] });
    await r;
    expect(getState().pins.sessions).toEqual([]);
    expect(getState().mutes.sessions).toEqual([]);
  });

  it("a mute that fails after reset() does not put the old login's mutes back", async () => {
    const held = heldHub();
    const r = refresh();
    await settle();
    find(held, "GET", "/pins").answer({ spaces: [], sessions: [] });
    find(held, "GET", "/mutes").answer({ spaces: [], sessions: ["m/old-login"], allowed: [] });
    await r;
    const mute = flipMute(agent("a"));
    await settle();
    reset();
    find(held, "PUT", "/mutes").answer({ error: "busy" }, 503);
    await mute;
    expect(getState().mutes.sessions).toEqual([]);
  });
});
