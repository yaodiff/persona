// @vitest-environment jsdom
// The side panel's terminal while the hub link drops: the hub forgets who drives, so the page must not go on saying it
// does (keys would be dropped) until the hub grants it again.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({
  open: true,
  conn: "online",
  sent: [] as Record<string, unknown>[],
  machines: [] as { id: string; name: string; online: boolean }[],
  listeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>(),
}));

vi.mock("../../../lib/store", () => ({
  getState: () => ({ features: ["terminal_stream", "terminal_control"], conn: hub.conn, machines: hub.machines }),
  isOpen: () => hub.open,
  send: (msg: Record<string, unknown>) => {
    if (hub.open) hub.sent.push(msg); // the real store drops what it cannot send
  },
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    const set = hub.listeners.get(type) ?? new Set();
    set.add(l);
    hub.listeners.set(type, set);
    return () => set.delete(l);
  },
}));

const typed = vi.hoisted(() => ({ onData: null as ((d: string) => void) | null }));

vi.mock("./xterm", () => {
  const disposable = { dispose() {} };
  class Terminal {
    options: Record<string, unknown>;
    cols = 80;
    rows = 24;
    element: HTMLElement | undefined;
    modes = { applicationCursorKeysMode: false };
    constructor(options: Record<string, unknown>) {
      this.options = { ...options };
    }
    loadAddon() {}
    open(host: HTMLElement) {
      this.element = host;
    }
    onData(cb: (d: string) => void) {
      typed.onData = cb;
      return disposable;
    }
    onResize() {
      return disposable;
    }
    onWriteParsed() {
      return disposable;
    }
    onSelectionChange() {
      return disposable;
    }
    attachCustomKeyEventHandler() {}
    attachCustomWheelEventHandler() {}
    resize(cols: number, rows: number) {
      this.cols = cols;
      this.rows = rows;
    }
    reset() {}
    write() {}
    focus() {}
    blur() {}
    hasSelection() {
      return false;
    }
    clearSelection() {}
    dispose() {}
  }
  class FitAddon {
    proposeDimensions() {
      return { cols: 100, rows: 30 };
    }
  }
  class SearchAddon {
    onDidChangeResults() {
      return disposable;
    }
  }
  return { Terminal, FitAddon, SearchAddon, ClipboardAddon: class {}, WebLinksAddon: class {}, matchHerdrWidths: () => {} };
});

vi.mock("./cjk", () => ({ cjkFontStack: () => Promise.resolve(null), terminalFontReady: () => Promise.resolve() }));

import { TerminalFeed } from "./feed";
import { t } from "../../../i18n";

const TICK_MS = 250;

function emit(type: string, msg: Record<string, unknown>) {
  hub.listeners.get(type)?.forEach((l) => l(msg));
}

function inputs() {
  return hub.sent.filter((m) => m.type === "terminal_input" && "text" in m);
}

describe("the side panel's terminal when the hub link drops", () => {
  let feed: TerminalFeed;

  beforeEach(() => {
    vi.useFakeTimers();
    hub.open = true;
    hub.conn = "online";
    hub.sent = [];
    hub.machines = [];
    hub.listeners.clear();
    typed.onData = null;
    localStorage.clear();
    globalThis.ResizeObserver ??= class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  });

  afterEach(() => {
    feed?.dispose();
    vi.useRealTimers();
  });

  it("stops driving until the hub grants control again", async () => {
    feed = new TerminalFeed("m1", "s1");
    const host = document.createElement("div");
    await feed.mount(host, document.createElement("div"));
    expect(feed.snapshot().mode).toBe("stream"); // mount ticks once: it subscribes asking to drive
    expect(hub.sent.some((m) => m.type === "terminal_subscribe" && m.control)).toBe(true);

    emit("terminal_control", { machine_id: "m1", session_id: "s1", state: "active" });
    expect(feed.snapshot().control).toBe("active");
    expect(feed.driving).toBe(true);

    hub.open = false;
    hub.conn = "offline";
    vi.advanceTimersByTime(TICK_MS);
    expect(feed.snapshot().failure).not.toBeNull();
    expect(feed.snapshot().control).not.toBe("active");
    expect(feed.driving).toBe(false);

    hub.sent = [];
    hub.open = true; // the socket is back before the hub's hello: still nothing goes in
    typed.onData!("ls\r");
    feed.pressBare("ctrl+c");
    expect(inputs()).toEqual([]);

    hub.conn = "online";
    emit("hello", {});
    const sub = hub.sent.find((m) => m.type === "terminal_subscribe");
    expect(sub?.control).toEqual({ cols: 100, rows: 30 }); // it still asks to drive
    expect(feed.driving).toBe(false);

    emit("terminal_control", { machine_id: "m1", session_id: "s1", state: "active" });
    expect(feed.snapshot().control).toBe("active");
    expect(feed.driving).toBe(true);
    typed.onData!("ls\r");
    expect(inputs()).toEqual([{ type: "terminal_input", machine_id: "m1", session_id: "s1", text: "ls\r" }]);
  });
  it("stops driving when the hub says hello again before a tick sees the link drop", async () => {
    feed = new TerminalFeed("m1", "s1");
    await feed.mount(document.createElement("div"), document.createElement("div"));
    emit("terminal_control", { machine_id: "m1", session_id: "s1", state: "active" });
    expect(feed.driving).toBe(true);

    // The socket dropped and came back within one tick: no tick saw it down, only the hub's hello tells.
    hub.sent = [];
    emit("hello", {});
    expect(feed.snapshot().control).toBe("starting");
    expect(feed.driving).toBe(false);
    const sub = hub.sent.find((m) => m.type === "terminal_subscribe");
    expect(sub?.control).toEqual({ cols: 100, rows: 30 }); // it still asks to drive
    typed.onData!("ls\r");
    feed.pressBare("ctrl+c");
    expect(inputs()).toEqual([]);

    emit("terminal_control", { machine_id: "m1", session_id: "s1", state: "active" });
    expect(feed.driving).toBe(true);
    typed.onData!("ls\r");
    expect(inputs()).toEqual([{ type: "terminal_input", machine_id: "m1", session_id: "s1", text: "ls\r" }]);
  });
});

// A takeover the hub never answers (it went with a node connection that dropped: a deploy restarted the node) must not
// leave the page on "Taking over…" for good: within ten seconds it says why, and asking again makes the hub ask the
// node again (#6327).
describe("the terminal when nothing answers its takeover", () => {
  let feed: TerminalFeed;
  const types = () => hub.sent.map((m) => m.type);
  const subscribes = () => hub.sent.filter((m) => m.type === "terminal_subscribe");

  beforeEach(() => {
    vi.useFakeTimers();
    hub.open = true;
    hub.conn = "online";
    hub.sent = [];
    hub.machines = [{ id: "m1", name: "Mac Studio", online: true }];
    hub.listeners.clear();
    localStorage.clear();
    globalThis.ResizeObserver ??= class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  });

  afterEach(() => {
    feed?.dispose();
    vi.useRealTimers();
  });

  it("gives up after ten seconds, says why, and subscribes afresh", async () => {
    feed = new TerminalFeed("m1", "pane:w1:p3");
    await feed.mount(document.createElement("div"), document.createElement("div"));
    expect(feed.snapshot().control).toBe("starting");
    expect(subscribes()).toHaveLength(1);

    vi.advanceTimersByTime(9_750);
    expect(feed.snapshot().failure).toBeNull(); // still within the wait: no reason yet
    hub.sent = [];
    vi.advanceTimersByTime(500);
    expect(feed.snapshot().failure).toBe(t("lib-terminal.not_answering", { name: "Mac Studio" }));
    expect(feed.snapshot().loaded).toBe(true); // the reason, not a spinner
    expect(feed.snapshot().control).toBe("starting"); // still taking it over between tries, not "View only"
    expect(types()).toEqual(["terminal_unsubscribe"]); // the hub drops it: the next one has it ask the node again

    vi.advanceTimersByTime(1_250); // the backoff's first second
    expect(subscribes()).toHaveLength(1);
    expect(subscribes()[0].control).toEqual({ cols: 100, rows: 30 });
    expect(subscribes()[0].epoch).toBeUndefined(); // from a whole frame
    expect(feed.snapshot().control).toBe("starting");

    emit("terminal_control", { machine_id: "m1", session_id: "pane:w1:p3", state: "active" });
    emit("terminal", { machine_id: "m1", session_id: "pane:w1:p3", epoch: "e1", seq: 1, full: true, cols: 100, rows: 30, data: btoa("$ ") });
    expect(feed.snapshot().failure).toBeNull();
    expect(feed.driving).toBe(true);
    hub.sent = [];
    vi.advanceTimersByTime(20_000);
    expect(types()).not.toContain("terminal_unsubscribe"); // answered: an idle shell that sends nothing is not given up on
  });

  it("Retry before any frame drops the unanswered subscription first", async () => {
    feed = new TerminalFeed("m1", "pane:w1:p3");
    await feed.mount(document.createElement("div"), document.createElement("div"));
    hub.sent = [];
    feed.retry();
    expect(types()).toEqual(["terminal_unsubscribe", "terminal_subscribe"]);
  });

  it("subscribes again as soon as the computer is back online", async () => {
    hub.machines = [{ id: "m1", name: "Mac Studio", online: false }];
    feed = new TerminalFeed("m1", "pane:w1:p3");
    await feed.mount(document.createElement("div"), document.createElement("div"));
    for (let i = 0; i < 4; i++) {
      // the hub answers each at once: its node is gone (the backoff grows: 1, 2, 4, 8 seconds)
      emit("terminal", { machine_id: "m1", session_id: "pane:w1:p3", error: "m1: offline" });
      vi.advanceTimersByTime(2 ** i * 1000 + 250);
    }
    emit("terminal", { machine_id: "m1", session_id: "pane:w1:p3", error: "m1: offline" });
    expect(feed.snapshot().failure).toBe(t("lib-terminal.offline", { name: "Mac Studio" })); // in the page's words, not the hub's
    expect(feed.snapshot().control).toBe("starting"); // it takes the terminal over once the computer is back
    hub.sent = [];
    vi.advanceTimersByTime(1_000);
    expect(subscribes()).toHaveLength(0); // the next try is 15 seconds away

    hub.machines = [{ id: "m1", name: "Mac Studio", online: true }];
    vi.advanceTimersByTime(250);
    expect(subscribes()).toHaveLength(1);
    expect(subscribes()[0].control).toEqual({ cols: 100, rows: 30 });
  });
});
