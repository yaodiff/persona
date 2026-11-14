// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "./types";

const fixture = vi.hoisted(() => ({
  state: { sessions: {} as Record<string, Session[]>, features: [] as string[], loaded: false },
  listeners: new Set<() => void>(),
  events: new Map<string, Set<(msg?: Record<string, unknown>) => void>>(),
  api: vi.fn(),
  owner: "first-login",
}));
vi.mock("./api", () => {
  class ApiError extends Error {
    constructor(readonly status: number, message: string, readonly body: Record<string, unknown> = {}) { super(message); }
    get code() { return typeof this.body.code === "string" ? this.body.code : undefined; }
  }
  return { api: fixture.api, credentialOwner: () => fixture.owner, ApiError, errorText: (e: unknown) => (e instanceof Error ? e.message : String(e)) };
});
vi.mock("./store", async () => {
  const { useSyncExternalStore } = await import("react");
  const subscribe = (listener: () => void) => {
    fixture.listeners.add(listener);
    return () => { fixture.listeners.delete(listener); };
  };
  return {
    getState: () => fixture.state,
    subscribe,
    useStore: <T,>(select: (state: typeof fixture.state) => T) => useSyncExternalStore(subscribe, () => select(fixture.state), () => select(fixture.state)),
    onHubEvent: (event: string, listener: (msg?: Record<string, unknown>) => void) => {
      if (!fixture.events.has(event)) fixture.events.set(event, new Set());
      fixture.events.get(event)!.add(listener);
      return () => { fixture.events.get(event)!.delete(listener); };
    },
  };
});
import { retryFullAgentSessions, useFullAgentSessions } from "./sessions";
import { ApiError } from "./api";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const row = (session_id: string, patch: Partial<Session> = {}): Session => ({
  machine_id: "demo", session_id, title: session_id, cwd: "/demo", status: "idle", ...patch,
});
const deferred = () => {
  let resolve!: (value: { sessions: Session[] }) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<{ sessions: Session[] }>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
function Probe() {
  const { sessions, loaded, error } = useFullAgentSessions();
  return <output data-loaded={loaded} data-error={error} data-states={sessions.map((s) => s.state).join(",")} data-titles={sessions.map((s) => s.title).join(",")}>{sessions.map((s) => s.session_id).join(",")}</output>;
}
const publish = (patch: Partial<typeof fixture.state>) => {
  fixture.state = { ...fixture.state, ...patch };
  fixture.listeners.forEach((listener) => listener());
};
const push = (event: string, msg?: Record<string, unknown>) => fixture.events.get(event)?.forEach((listener) => listener(msg));
const advance = async (ms: number) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); };
let host: HTMLDivElement;
let root: Root | undefined;
const mount = async () => { root = createRoot(host); await act(async () => { root!.render(<Probe />); }); };
const resolve = async (request: ReturnType<typeof deferred>, sessions: Session[]) => {
  await act(async () => { request.resolve({ sessions }); });
};
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  fixture.state = { sessions: {}, features: ["sessions_list", "sessions_lookup"], loaded: true };
  fixture.owner = "first-login";
  fixture.api.mockReset();
  host = document.createElement("div");
  document.body.append(host);
});
afterEach(async () => {
  await act(async () => { root?.unmount(); });
  root = undefined;
  host.remove();
  await advance(1_000);
  expect(vi.getTimerCount()).toBe(0);
  vi.useRealTimers();
  expect(fixture.listeners.size).toBe(0);
  expect([...fixture.events.values()].every((listeners) => listeners.size === 0)).toBe(true);
});

describe("complete session snapshots for the sidebar fallback", () => {
  it("supports static rendering without starting a full-list transport", () => {
    expect(renderToStaticMarkup(<Probe />)).toContain('data-loaded="false"');
    expect(fixture.api).not.toHaveBeenCalled();
    expect(fixture.listeners.size).toBe(0);
    expect([...fixture.events.values()].every((listeners) => listeners.size === 0)).toBe(true);
  });
  it("measures twenty same-commit handoffs with a synthetic full-list fixture", async () => {
    const sessions = Array.from({ length: 8_356 }, (_, i) => row(`agent-${i}`, { title: `Synthetic agent ${i} ${"x".repeat(800)}` }));
    const body = JSON.stringify({ sessions });
    const bytes = new TextEncoder().encode(body).byteLength;
    fixture.api.mockImplementation(async () => JSON.parse(body) as { sessions: Session[] });
    await mount();
    for (let i = 1; i <= 20; i++) {
      await act(async () => { root!.render(<Probe key={i} />); });
    }
    console.log(`Full-list handoffs: 20; rows: ${sessions.length}; bytes/GET: ${bytes}; GETs: ${fixture.api.mock.calls.length}; total bytes: ${bytes * fixture.api.mock.calls.length}`);
    expect(fixture.api).toHaveBeenCalledTimes(1);
    expect(host.querySelector("output")!.dataset.loaded).toBe("true");
  });
  it("shares an in-flight read, snapshot, and refresh deadline between simultaneous consumers", async () => {
    const initial = deferred(), refreshed = deferred();
    fixture.api.mockReturnValueOnce(initial.promise).mockReturnValueOnce(refreshed.promise);
    root = createRoot(host);
    await act(async () => { root!.render(<><Probe key="sidebar" /><Probe key="agents" /></>); });
    expect(fixture.api).toHaveBeenCalledTimes(1);
    expect([...fixture.events.values()].every((listeners) => listeners.size === 1)).toBe(true);
    await resolve(initial, [row("shared")]);
    expect([...host.querySelectorAll("output")].map((el) => el.textContent)).toEqual(["shared", "shared"]);
    await act(async () => { push("sessions_list"); push("session"); });
    await advance(1_000);
    await act(async () => { root!.render(<Probe key="agents" />); });
    await advance(1_999);
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await resolve(refreshed, [row("refreshed")]);
    expect(host.textContent).toBe("refreshed");
  });

  it("reuses a pending request across a same-commit handoff and StrictMode effects", async () => {
    const request = deferred();
    fixture.api.mockReturnValue(request.promise);
    root = createRoot(host);
    await act(async () => { root!.render(<StrictMode><Probe key="sidebar" /></StrictMode>); });
    const signal = fixture.api.mock.calls[0][1].signal as AbortSignal;
    await act(async () => { root!.render(<StrictMode><Probe key="agents" /></StrictMode>); });
    expect(fixture.api).toHaveBeenCalledTimes(1);
    expect(signal.aborted).toBe(false);
    await resolve(request, [row("handed-off")]);
    expect(host.textContent).toBe("handed-off");
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(1);
  });

  it("hands a completed snapshot to a new root during grace without extending the cooldown", async () => {
    fixture.api.mockResolvedValue({ sessions: [row("retained")] });
    await mount();
    await act(async () => { push("sessions_list"); root!.unmount(); root = undefined; });
    await advance(999);
    await mount();
    expect(host.textContent).toBe("retained");
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await advance(2_000);
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(fixture.api).toHaveBeenCalledTimes(2);
  });

  it.each(["sessions_list", "hello"])("remembers %s during grace without downloading for absent consumers", async (event) => {
    fixture.api.mockResolvedValueOnce({ sessions: [row("before")] }).mockResolvedValue({ sessions: [row("after")] });
    await mount();
    await act(async () => { root!.unmount(); root = undefined; push(event); });
    await advance(500);
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await mount();
    if (event !== "hello") {
      expect(host.textContent).toBe("before");
      await advance(2_500);
    }
    expect(fixture.api).toHaveBeenCalledTimes(2);
    expect(host.textContent).toBe("after");
  });

  it("releases the complete snapshot and all subscriptions at the finite grace deadline", async () => {
    const next = deferred();
    fixture.api.mockResolvedValueOnce({ sessions: [row("released")] }).mockReturnValueOnce(next.promise);
    await mount();
    await act(async () => { root!.unmount(); root = undefined; });
    await advance(999);
    expect([...fixture.events.values()].some((listeners) => listeners.size > 0)).toBe(true);
    await advance(1);
    expect(fixture.listeners.size).toBe(0);
    expect([...fixture.events.values()].every((listeners) => listeners.size === 0)).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
    await mount();
    expect(host.textContent).toBe("");
    expect(host.querySelector("output")!.dataset.loaded).toBe("false");
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await resolve(next, [row("fresh")]);
    expect(host.textContent).toBe("fresh");
  });

  it("fences a late request after grace expiry even if the new request has already completed", async () => {
    const stale = deferred();
    fixture.api.mockReturnValueOnce(stale.promise).mockResolvedValue({ sessions: [row("fresh")] });
    await mount();
    const signal = fixture.api.mock.calls[0][1].signal as AbortSignal;
    await act(async () => { root!.unmount(); root = undefined; });
    await advance(1_000);
    expect(signal.aborted).toBe(true);
    await mount();
    await resolve(stale, [row("stale")]);
    expect(host.textContent).toBe("fresh");
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(2);
  });

  it("fences a same-credential reset batched with reloading in the same commit", async () => {
    const stale = deferred();
    fixture.api.mockReturnValueOnce(stale.promise).mockResolvedValue({ sessions: [row("new-epoch")] });
    await mount();
    const signal = fixture.api.mock.calls[0][1].signal as AbortSignal;
    await act(async () => { publish({ loaded: false }); publish({ loaded: true }); });
    expect(signal.aborted).toBe(true);
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await resolve(stale, [row("old-epoch")]);
    expect(host.textContent).toBe("new-epoch");
  });

  it.each(["reset", "credential", "credential-without-publication"])("discards a retained snapshot on %s during grace", async (change) => {
    const next = deferred();
    fixture.api.mockResolvedValueOnce({ sessions: [row("private-old")] }).mockReturnValueOnce(next.promise);
    await mount();
    await act(async () => {
      root!.unmount(); root = undefined;
      if (change === "reset") { publish({ loaded: false }); publish({ loaded: true }); }
      else { fixture.owner = "second-login"; if (change === "credential") publish({ sessions: {} }); }
    });
    await mount();
    expect(host.textContent).toBe("");
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await resolve(next, [row("new-owner")]);
    expect(host.textContent).toBe("new-owner");
  });

  it("never exposes or accepts an old credential's pending response with consumers still mounted", async () => {
    const stale = deferred(), next = deferred();
    fixture.api.mockResolvedValueOnce({ sessions: [row("private-old")] }).mockReturnValueOnce(stale.promise).mockReturnValueOnce(next.promise);
    await mount();
    await act(async () => { push("hello"); });
    const signal = fixture.api.mock.calls[1][1].signal as AbortSignal;
    await act(async () => { fixture.owner = "second-login"; publish({ sessions: {} }); });
    expect(host.textContent).toBe("");
    expect(signal.aborted).toBe(true);
    expect(fixture.api).toHaveBeenCalledTimes(3);
    await resolve(next, [row("new-owner")]);
    await resolve(stale, [row("leaked")]);
    expect(host.textContent).toBe("new-owner");
  });

  it("keeps in-flight store edits and deletions overlaid through a handoff", async () => {
    const cached = row("cached"), removed = row("removed");
    fixture.state.sessions = { demo: [cached, removed] };
    const request = deferred();
    fixture.api.mockReturnValue(request.promise);
    await mount();
    await act(async () => {
      publish({ sessions: { demo: [row("cached", { title: "Edited locally", state: "idle" })] } });
      root!.render(<Probe key="agents" />);
    });
    await resolve(request, [row("cached", { title: "HTTP title", state: "unread" }), removed, row("tail")]);
    expect(host.textContent).toBe("cached,tail");
    expect(host.querySelector("output")!.dataset.titles).toBe("Edited locally,tail");
    expect(fixture.api).toHaveBeenCalledTimes(1);
  });

  it("resumes a transient failure after a handoff, with one shared retry for multiple consumers", async () => {
    const failed = deferred();
    fixture.api.mockReturnValueOnce(failed.promise).mockResolvedValue({ sessions: [row("recovered")] });
    await mount();
    await act(async () => { root!.unmount(); root = undefined; failed.reject(new Error("offline")); });
    await advance(500);
    root = createRoot(host);
    await act(async () => { root!.render(<><Probe /><Probe /></>); });
    expect(fixture.api).toHaveBeenCalledTimes(2);
    expect([...host.querySelectorAll("output")].map((el) => el.textContent)).toEqual(["recovered", "recovered"]);
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(2);
  });

  it("keeps an old hub's full store and does not fetch a second list", async () => {
    fixture.state = { sessions: { demo: [row("legacy"), row("assistant", { assistant: true })] }, features: [], loaded: true };
    await mount();
    expect(host.textContent).toBe("legacy");
    expect(host.querySelector("output")!.dataset.loaded).toBe("true");
    expect(fixture.api).not.toHaveBeenCalled();
    await act(async () => { publish({ sessions: { demo: [row("updated")] } }); });
    expect(host.textContent).toBe("updated");
  });

  it("loads complete rows beyond the first page and gates filter seeding on that answer", async () => {
    const request = deferred();
    fixture.api.mockReturnValue(request.promise);
    fixture.state.sessions = { demo: [row("bounded-page")] };
    await mount();
    expect(fixture.api).toHaveBeenCalledWith("/sessions?summary=0&limit=200", { signal: expect.any(AbortSignal) });
    expect(host.querySelector("output")!.dataset.loaded).toBe("false");
    const rows = Array.from({ length: 75 }, (_, i) => row(`agent-${i}`));
    await resolve(request, [...rows, row("assistant", { assistant: true })]);
    expect(host.textContent).toBe(rows.map((s) => s.session_id).join(","));
    expect(host.querySelector("output")!.dataset.loaded).toBe("true");
    expect(fixture.state.sessions.demo).toEqual([row("bounded-page")]);
  });

  it("reflects actions on fallback-only rows without replacing newer HTTP rows with old details", async () => {
    const cached = row("cached", { title: "Old cached title" });
    fixture.state.sessions = { demo: [cached] };
    fixture.api.mockResolvedValue({ sessions: [row("cached", { title: "Current title" }), row("tail", { state: "unread" })] });
    await mount();
    await act(async () => { publish({ sessions: { demo: [cached, row("tail", { state: "idle" })] } }); });
    expect(host.querySelector("output")!.dataset.states).toBe(",idle");
    expect(host.querySelector("output")!.dataset.titles).toBe("Current title,tail");
    await act(async () => { publish({ sessions: { demo: [cached, row("tail", { state: "idle", title: "Renamed" })] } }); });
    expect(host.querySelector("output")!.dataset.titles).toBe("Current title,Renamed");
  });

  it("waits for initial hub loading before requesting the fallback snapshot", async () => {
    fixture.state.loaded = false;
    fixture.api.mockResolvedValue({ sessions: [row("ready")] });
    await mount();
    expect(fixture.api).not.toHaveBeenCalled();
    await act(async () => { publish({ loaded: true }); });
    expect(host.textContent).toBe("ready");
    expect(fixture.api).toHaveBeenCalledTimes(1);
  });

  it.each(["sessions_list", "session", "sessions", "sessions_delta", "hello"])("refreshes on %s without clearing visible rows", async (event) => {
    const next = deferred();
    fixture.api.mockResolvedValueOnce({ sessions: [row("previous")] }).mockReturnValueOnce(next.promise);
    await mount();
    await act(async () => { push(event); });
    expect(host.textContent).toBe("previous");
    expect(fixture.api).toHaveBeenCalledTimes(event === "hello" ? 2 : 1);
    if (event !== "hello") await advance(3_000);
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await resolve(next, [row("updated")]);
    expect(host.textContent).toBe("updated");
  });

  it("bounds dense event bursts and reads the final change without postponing the trailing deadline", async () => {
    let revision = 0;
    fixture.api.mockImplementation(async () => ({ sessions: [row(`revision-${revision}`)] }));
    await mount();
    const events = ["sessions_list", "session", "sessions", "sessions_delta"];
    for (let i = 1; i <= 10; i++) {
      await advance(200);
      revision = i;
      await act(async () => { push(events[i % events.length]); });
    }
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await advance(999);
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(fixture.api).toHaveBeenCalledTimes(2);
    expect(host.textContent).toBe("revision-10");

    for (let i = 11; i <= 20; i++) {
      await advance(200);
      revision = i;
      await act(async () => { push(events[i % events.length]); });
    }
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await advance(999);
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(fixture.api).toHaveBeenCalledTimes(3);
    expect(host.textContent).toBe("revision-20");
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(3);
  });

  it("waits three seconds after slow requests finish before draining queued hints", async () => {
    const initial = deferred(), intermediate = deferred();
    fixture.api.mockReturnValueOnce(initial.promise).mockReturnValueOnce(intermediate.promise).mockResolvedValue({ sessions: [row("current")] });
    await mount();
    await advance(10_000);
    await act(async () => { push("sessions_list"); push("session"); push("sessions_delta"); });
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await resolve(initial, [row("initial")]);
    expect(host.textContent).toBe("initial");
    await advance(2_999);
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(fixture.api).toHaveBeenCalledTimes(2);

    await advance(5_000);
    await act(async () => { push("sessions_list"); push("sessions"); });
    await resolve(intermediate, [row("intermediate")]);
    expect(host.textContent).toBe("intermediate");
    await advance(2_999);
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(fixture.api).toHaveBeenCalledTimes(3);
    expect(host.textContent).toBe("current");
  });

  it("refreshes immediately on hello during cooldown without leaving a duplicate trailing read", async () => {
    fixture.api.mockResolvedValue({ sessions: [row("current")] });
    await mount();
    await act(async () => { push("sessions_list"); });
    await advance(1_000);
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await act(async () => { push("hello"); });
    expect(fixture.api).toHaveBeenCalledTimes(2);
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(2);
  });

  it("serializes hello during an in-flight read and immediately follows it with one fresh read", async () => {
    const pending = deferred();
    fixture.api.mockReturnValueOnce(pending.promise).mockResolvedValue({ sessions: [row("reconnected")] });
    await mount();
    await act(async () => { push("sessions_list"); push("hello"); push("hello"); push("sessions_delta"); });
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await resolve(pending, [row("before-reconnect")]);
    expect(fixture.api).toHaveBeenCalledTimes(2);
    expect(host.textContent).toBe("reconnected");
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(2);
  });

  it.each(["reset", "unmount"])("cancels a queued trailing read on %s", async (end) => {
    fixture.api.mockResolvedValue({ sessions: [row("current")] });
    await mount();
    await act(async () => { push("sessions_list"); });
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await act(async () => {
      if (end === "reset") publish({ loaded: false });
      else { root!.unmount(); root = undefined; }
    });
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(1);
  });

  it("keeps the last complete snapshot after a failed refresh", async () => {
    fixture.api.mockResolvedValueOnce({ sessions: [row("previous")] }).mockRejectedValueOnce(new Error("offline"));
    await mount();
    await act(async () => { push("sessions_list"); });
    await advance(3_000);
    expect(fixture.api).toHaveBeenCalledTimes(2);
    expect(host.textContent).toBe("previous");
    expect(host.querySelector("output")!.dataset.loaded).toBe("true");
  });

  it("says why an initial read failed, until a read loads it (audit item 4: the list stayed blank)", async () => {
    fixture.api.mockRejectedValueOnce(new Error("HTTP 500")).mockResolvedValueOnce({ sessions: [row("recovered")] });
    await mount();
    expect(host.querySelector("output")!.dataset.loaded).toBe("false");
    expect(host.querySelector("output")!.dataset.error).toBe("HTTP 500");
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(host.textContent).toBe("recovered");
    expect(host.querySelector("output")!.dataset.error).toBe("");
  });

  it("reads again at once on Retry, loading (no error) until the answer, and keeps one retry timer", async () => {
    const again = deferred();
    fixture.api.mockRejectedValueOnce(new Error("HTTP 500")).mockReturnValueOnce(again.promise);
    await mount();
    expect(host.querySelector("output")!.dataset.error).toBe("HTTP 500");
    await act(async () => { retryFullAgentSessions(); });
    expect(fixture.api).toHaveBeenCalledTimes(2);
    expect(host.querySelector("output")!.dataset.error).toBe("");
    expect(host.querySelector("output")!.dataset.loaded).toBe("false");
    await resolve(again, [row("recovered")]);
    expect(host.textContent).toBe("recovered");
    // the automatic retry it replaced does not read a third time
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(2);
  });

  it("leaves a loaded list's rows and no error when a later read fails", async () => {
    fixture.api.mockResolvedValueOnce({ sessions: [row("previous")] }).mockRejectedValueOnce(new Error("HTTP 500"));
    await mount();
    await act(async () => { push("sessions_list"); });
    await advance(3_000);
    expect(host.textContent).toBe("previous");
    expect(host.querySelector("output")!.dataset.error).toBe("");
  });

  it("never says an old hub's pushed list failed", async () => {
    fixture.state = { ...fixture.state, features: [], sessions: { demo: [row("pushed")] } };
    await mount();
    expect(host.textContent).toBe("pushed");
    expect(host.querySelector("output")!.dataset.error).toBe("");
    expect(fixture.api).not.toHaveBeenCalled();
  });

  it("retries an initial failure even when the hub sends no further hints", async () => {
    fixture.api.mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ sessions: [row("recovered")] });
    await mount();
    expect(host.querySelector("output")!.dataset.loaded).toBe("false");
    expect(fixture.api).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(host.textContent).toBe("recovered");
    expect(host.querySelector("output")!.dataset.loaded).toBe("true");
  });

  it("clears on reset and rejects a late response even with unchanged credentials", async () => {
    const stale = deferred(), next = deferred();
    fixture.api.mockResolvedValueOnce({ sessions: [row("previous")] }).mockReturnValueOnce(stale.promise).mockReturnValueOnce(next.promise);
    await mount();
    await act(async () => { push("sessions_list"); });
    await advance(3_000);
    const signal = fixture.api.mock.calls[1][1].signal as AbortSignal;
    await act(async () => { publish({ loaded: false }); });
    expect(signal.aborted).toBe(true);
    expect(host.textContent).toBe("");
    await resolve(stale, [row("stale")]);
    expect(host.textContent).toBe("");
    await act(async () => { publish({ loaded: true }); });
    await resolve(next, [row("new-login")]);
    expect(host.textContent).toBe("new-login");
  });

  it("disposes immediately when hello switches away from bounded transport", async () => {
    const request = deferred();
    fixture.api.mockReturnValue(request.promise);
    await mount();
    const signal = fixture.api.mock.calls[0][1].signal as AbortSignal;
    await act(async () => { publish({ features: [], sessions: { demo: [row("legacy")] } }); push("hello"); });
    expect(signal.aborted).toBe(true);
    expect(host.textContent).toBe("legacy");
    expect([...fixture.events.values()].every((listeners) => listeners.size === 0)).toBe(true);
    await resolve(request, [row("stale-bounded")]);
    expect(host.textContent).toBe("legacy");
    await advance(10_000);
    expect(fixture.api).toHaveBeenCalledTimes(1);
  });

  it("aborts and unsubscribes after the fallback grace expires", async () => {
    const request = deferred();
    fixture.api.mockReturnValue(request.promise);
    await mount();
    const signal = fixture.api.mock.calls[0][1].signal as AbortSignal;
    await act(async () => { root!.unmount(); root = undefined; });
    expect(signal.aborted).toBe(false);
    await advance(1_000);
    expect(signal.aborted).toBe(true);
    await resolve(request, [row("too-late")]);
    push("sessions_list");
    expect(fixture.api).toHaveBeenCalledTimes(1);
  });

  it("cancels a pending retry when the fallback unmounts", async () => {
    fixture.api.mockRejectedValue(new Error("offline"));
    await mount();
    await act(async () => { root!.unmount(); root = undefined; });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(fixture.api).toHaveBeenCalledTimes(1);
  });
});

// A paging hub (#3759): complete pages of up to 200 rows under `e<epoch>.<n>`, and `since` with every change after it.
function pagingHub(count: number) {
  type Change = { n: number; upsert: Session[]; remove: string[] };
  const hub = { rows: Array.from({ length: count }, (_, i) => row(`agent-${i}`)), epoch: 1, n: 1, log: [] as Change[], baselines: 0, deltas: 0, legacy: 0 };
  const revision = () => `e${hub.epoch}.${hub.n}`;
  fixture.api.mockImplementation(async (path: string) => {
    const [route, query = ""] = path.split("?");
    expect(route).toBe("/sessions");
    const q = new URLSearchParams(query);
    if (![...q.keys()].length) { hub.legacy++; return { sessions: hub.rows }; }
    expect([q.get("summary"), q.get("limit")]).toEqual(["0", "200"]);
    const since = q.get("since");
    if (since !== null) {
      hub.deltas++;
      const [epoch, n] = since.slice(1).split(".").map(Number);
      if (epoch !== hub.epoch) throw new ApiError(409, "expired", { code: "revision_expired" });
      const changes = new Map<string, Session | null>();
      for (const change of hub.log.filter((c) => c.n > n)) {
        for (const id of change.remove) changes.set(id, null);
        for (const s of change.upsert) changes.set(s.session_id, s);
      }
      const sessions = [...changes.values()].filter((s): s is Session => s !== null);
      const remove = [...changes].filter(([, s]) => s === null).map(([id]) => JSON.stringify(["demo", id]));
      return { sessions, remove, has_more: false, revision: revision(), total: hub.rows.length };
    }
    hub.baselines++;
    const start = Number(q.get("cursor") ?? 0), end = Math.min(start + 200, hub.rows.length);
    const more = end < hub.rows.length;
    return { sessions: hub.rows.slice(start, end), remove: [], has_more: more, ...(more ? { next_cursor: String(end) } : {}), revision: revision(), total: hub.rows.length };
  });
  /** One hint's worth of change: a row's new title, now and then a new session or one gone. */
  const change = (k: number) => {
    hub.n++;
    const upsert = [row(`agent-${k % 10}`, { title: `round ${k}` })];
    const remove: string[] = [];
    if (k === 7) upsert.push(row("started-7"));
    if (k === 11) remove.push(`agent-${count - 1}`);
    hub.rows = hub.rows.filter((s) => !remove.includes(s.session_id)).map((s) => upsert.find((u) => u.session_id === s.session_id) ?? s);
    hub.rows.push(...upsert.filter((u) => !hub.rows.some((s) => s.session_id === u.session_id)));
    hub.log.push({ n: hub.n, upsert, remove });
  };
  return { hub, revision, change };
}

describe("a complete copy on a paging hub", () => {
  const ids = () => host.querySelector("output")!.textContent!.split(",");

  it("reads the complete list once, then only what changed, through a minute of hints", async () => {
    const { hub, revision, change } = pagingHub(266);
    await mount();
    expect(hub.baselines).toBe(2);
    expect(ids()).toHaveLength(266);
    for (let k = 0; k < 120; k++) { // two hints a second while agents work
      await advance(500);
      change(k);
      await act(async () => { push("sessions_list", { revision: revision() }); });
    }
    await advance(3_000);
    expect(hub.baselines).toBe(2);
    expect(hub.legacy).toBe(0);
    expect(hub.deltas).toBeGreaterThan(10);
    expect(hub.deltas).toBeLessThanOrEqual(21); // reads stay three seconds apart
    expect(ids()).toHaveLength(266);
    expect(ids()).toContain("started-7");
    expect(ids()).not.toContain("agent-265");
    const titles = host.querySelector("output")!.dataset.titles!.split(",");
    expect(titles[ids().indexOf("agent-9")]).toBe("round 119");
  });

  it("starts no read for a hint of the revision it already holds", async () => {
    const { hub, revision, change } = pagingHub(3);
    await mount();
    await act(async () => { push("sessions_list", { revision: revision() }); });
    await advance(10_000);
    expect([hub.baselines, hub.deltas]).toEqual([1, 0]);
    change(1);
    await act(async () => { push("sessions_list", { revision: revision() }); });
    await advance(3_000);
    expect([hub.baselines, hub.deltas]).toEqual([1, 1]);
    expect(host.querySelector("output")!.dataset.titles).toContain("round 1");
  });

  it("rebuilds the copy when a hub restart expires its revision, then follows deltas again", async () => {
    const { hub, revision, change } = pagingHub(250);
    await mount();
    hub.epoch = 2;
    change(3);
    await act(async () => { push("hello"); });
    expect([hub.baselines, hub.deltas]).toEqual([4, 1]);
    expect(host.querySelector("output")!.dataset.titles).toContain("round 3");
    change(4);
    await act(async () => { push("sessions_list", { revision: revision() }); });
    await advance(3_000);
    expect([hub.baselines, hub.deltas]).toEqual([4, 2]);
    expect(host.querySelector("output")!.dataset.titles).toContain("round 4");
  });
});
