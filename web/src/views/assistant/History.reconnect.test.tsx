// @vitest-environment jsdom
// "All chats" after the hub comes back: a read that failed while it was away is read again on the next hello (and on a
// hello the hub could not resume), its error has Retry, and the error is not drawn over a list of chats the page has from
// elsewhere: one line says the list may be missing some, with Retry.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "../../lib/types";

const hub = vi.hoisted(() => ({
  fetched: [] as Session[],
  state: { assistant: null as unknown, sessions: {} as Record<string, unknown[]> },
  listeners: new Set<() => void>(),
  hello: new Set<(msg: Record<string, unknown>) => void>(),
  /** The hub is away: the list read fails. */
  down: false,
  reads: 0,
}));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      if (path === "/assistant?archived=1") {
        hub.reads++;
        if (hub.down) throw new TypeError("Failed to fetch");
        return { conversations: hub.fetched };
      }
      throw new real.ApiError(404, "not found", {});
    }),
  };
});
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  const { useSyncExternalStore } = await import("react");
  return {
    ...real,
    refreshAssistant: async () => {},
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T =>
      useSyncExternalStore(
        (l) => (hub.listeners.add(l), () => void hub.listeners.delete(l)),
        () => select({ ...real.getState(), ...hub.state } as ReturnType<typeof real.getState>),
      ),
    onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
      if (type !== "hello") return () => {};
      hub.hello.add(l);
      return () => void hub.hello.delete(l);
    },
  };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { History } from "./History";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

const conv = (sid: string, p: Partial<Session> = {}): Session => ({
  machine_id: "server", session_id: sid, title: `对话 ${sid}`, cwd: "/home/me/.persona/assistant", status: "idle", assistant: true,
  updated_at: "2026-11-01T09:00:00Z", ...p,
} as Session);

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const mount = async () => {
  root = createRoot(host);
  act(() => root.render(<History onToggleSidebar={() => {}} />));
  await flush();
};
const hello = async (msg: Record<string, unknown> = { type: "hello" }) => {
  await act(async () => hub.hello.forEach((l) => l(msg)));
  await flush();
};
const retryButton = () => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重试");
const failedBlock = () => host.textContent?.includes("读不到对话") ?? false;
const partialLine = () => host.querySelector('[role="status"]')?.textContent ?? "";
const archivedHeading = () => [...host.querySelectorAll("button[aria-expanded]")].find((b) => b.textContent?.includes("已归档"))?.textContent ?? "";

beforeEach(() => {
  hub.down = true;
  hub.reads = 0;
  hub.hello.clear();
  hub.fetched = [conv("main"), conv("old", { archived: true } as Partial<Session>), conv("keep")];
  hub.state = { assistant: { machine_id: "server", main: "main", conversations: [] }, sessions: {} };
  host = document.createElement("div");
  document.body.append(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("All chats when its read failed", () => {
  it("says so with Retry when there is nothing else to show, and Retry reads it again", async () => {
    await mount();
    expect(failedBlock()).toBe(true);
    expect(retryButton()).toBeDefined();
    hub.down = false;
    await act(async () => retryButton()!.click());
    await flush();
    expect(hub.reads).toBe(2);
    expect(failedBlock()).toBe(false);
    expect(host.textContent).toContain("对话 keep");
  });

  it("puts focus on the first chat when Retry works, as its button goes with the error", async () => {
    await mount();
    act(() => retryButton()!.focus());
    expect(document.activeElement?.textContent?.trim()).toBe("重试");

    // still down: Retry stays, and so does focus
    await act(async () => retryButton()!.click());
    await flush();
    expect(document.activeElement).toBe(retryButton());

    hub.down = false;
    await act(async () => retryButton()!.click());
    await flush();
    expect(failedBlock()).toBe(false);
    const first = host.querySelector("a[href]");
    expect(first?.textContent).toContain("主对话"); // the main chat, pinned on top
    expect(document.activeElement).toBe(first);
  });

  it("leaves focus alone when it is somewhere else once Retry works", async () => {
    hub.state = { assistant: { machine_id: "server", main: "main", conversations: [conv("main"), conv("keep")] }, sessions: {} };
    await mount();
    const search = host.querySelector<HTMLInputElement>("input")!;
    hub.down = false;
    await act(async () => {
      retryButton()!.click();
      search.focus();
    });
    await flush();
    expect(partialLine()).toBe("");
    expect(document.activeElement).toBe(search);
  });

  it("reads it again on the next hello once the hub is back, and drops the error", async () => {
    await mount();
    expect(failedBlock()).toBe(true);
    hub.down = false;
    await hello({ type: "hello", resumed: true }); // even a resumed stream: the failed read is not replayed
    expect(hub.reads).toBe(2);
    expect(failedBlock()).toBe(false);
    expect(archivedHeading()).toContain("已归档（1）");
  });

  it("does not draw the error over the chats the page has from elsewhere, but says some may be missing", async () => {
    hub.state = { assistant: { machine_id: "server", main: "main", conversations: [conv("main"), conv("keep")] }, sessions: {} };
    await mount();
    expect(failedBlock()).toBe(false);
    expect(host.textContent).toContain("对话 keep");
    expect(partialLine()).toContain("有些对话没读到");
    expect(retryButton()).toBeDefined();
    expect(archivedHeading()).toBe("");

    hub.down = false;
    await hello();
    expect(partialLine()).toBe("");
    expect(archivedHeading()).toContain("已归档（1）");
  });
});

describe("All chats after a good read", () => {
  it("reads again only on a hello the hub could not resume", async () => {
    hub.down = false;
    await mount();
    expect(hub.reads).toBe(1);
    await hello({ type: "hello", resumed: true });
    expect(hub.reads).toBe(1);
    await hello({ type: "hello" });
    expect(hub.reads).toBe(2);
  });
});
