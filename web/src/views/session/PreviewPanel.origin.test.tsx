// @vitest-environment jsdom
// Direct isolated-origin iframe preview, automatic stream fallback, and manual mode switching (docs/web/preview.md).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import enPreview from "../../i18n/locales/en/preview.json";
import zhPreview from "../../i18n/locales/zh-Hans/preview.json";
import jaPreview from "../../i18n/locales/ja/preview.json";

const mockCtx = vi.hoisted(() => ({
  features: ["preview"] as string[],
  grantCalls: [] as { mid: string; port: number; path?: string }[],
  openCalls: [] as { mid: string; port: number }[],
  grantHandler: null as null | ((mid: string, body: { port: number; path?: string }, attempt: number) => Promise<unknown>),
}));

vi.mock("../../lib/viewport", async (original) => ({
  ...(await original<typeof import("../../lib/viewport")>()),
  hasFinePointer: () => true,
}));

vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string, init?: { method?: string; json?: unknown }) => {
      if (path.includes("/preview/servers")) {
        return {
          servers: [
            { name: "web", port: 5173, listening: true },
            { name: "storybook", port: 6006, listening: true },
          ],
        };
      }
      const openMatch = path.match(/\/machines\/([^/]+)\/ports\/(\d+)\/open$/);
      if (openMatch) {
        const port = Number(openMatch[2]);
        mockCtx.openCalls.push({ mid: decodeURIComponent(openMatch[1]), port });
        return { port, listening: true };
      }
      const grantMatch = path.match(/\/machines\/([^/]+)\/preview\/grant$/);
      if (grantMatch) {
        const mid = decodeURIComponent(grantMatch[1]);
        const body = (init?.json ?? {}) as { port: number; path?: string };
        mockCtx.grantCalls.push({ mid, port: body.port, path: body.path });
        const attempt = mockCtx.grantCalls.length;
        if (mockCtx.grantHandler) return await mockCtx.grantHandler(mid, body, attempt);
        const origin = `https://p-${body.port}.preview.example.test`;
        return {
          url: `${origin}/__persona_preview_auth?code=c${attempt}`,
          origin,
          expires_at: "2026-11-28T13:00:00Z",
        };
      }
      throw new real.ApiError(404, "not found", {});
    }),
  };
});

vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => {
      const state = {
        ...real.getState(),
        features: mockCtx.features,
        sessions: { m1: [{ machine_id: "m1", session_id: "s1", workspace_id: "w1", cwd: "/project" }] },
        machines: [{ id: "m1", name: "box", hostname: "studio", online: true }],
      } as unknown as ReturnType<typeof real.getState>;
      return select(state);
    },
  };
});

import { ApiError } from "../../lib/api";
import { t } from "../../i18n";
import { DIRECT_LOAD_TIMEOUT_MS } from "../../lib/preview";
import { PreviewPanel } from "./PreviewPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
  static instances: FakeSocket[] = [];
  static get last(): FakeSocket | null {
    return FakeSocket.instances[FakeSocket.instances.length - 1] ?? null;
  }
  url: string;
  binaryType = "";
  readyState = 0;
  sent: Record<string, unknown>[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  closed = false;
  constructor(url: string) {
    this.url = url;
    FakeSocket.instances.push(this);
  }
  send(data: string) {
    this.sent.push(JSON.parse(data));
  }
  close() {
    this.closed = true;
  }
  open() {
    act(() => {
      this.readyState = FakeSocket.OPEN;
      this.onopen?.();
    });
  }
  receiveJson(msg: Record<string, unknown>) {
    act(() => this.onmessage?.({ data: JSON.stringify(msg) }));
  }
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  mockCtx.features = ["preview", "preview_origin"];
  mockCtx.grantCalls = [];
  mockCtx.openCalls = [];
  mockCtx.grantHandler = null;
  FakeSocket.instances = [];
  sessionStorage.clear();
  vi.stubGlobal("devicePixelRatio", 2);
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("fine"), media: query, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("ResizeObserver", class {
    constructor(private cb: ResizeObserverCallback) {}
    observe() {
      this.cb([{ contentRect: { width: 640, height: 800 } } as ResizeObserverEntry], this as unknown as ResizeObserver);
    }
    unobserve() {}
    disconnect() {}
  });
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ drawImage: vi.fn() } as unknown as CanvasRenderingContext2D);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  vi.useRealTimers();
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => await Promise.resolve());
};

async function mountPanel(port = 5173, path = "/") {
  act(() => root.render(<PreviewPanel mid="m1" sid="s1" cwd="/project" port={port} path={path} onClose={() => {}} />));
  await flush();
}

function getIframe() {
  return host.querySelector<HTMLIFrameElement>("iframe[data-preview-iframe]");
}

function getCanvas() {
  return host.querySelector<HTMLCanvasElement>("[data-preview-canvas]");
}

function fireIframeLoad(iframe: HTMLIFrameElement) {
  act(() => void iframe.dispatchEvent(new Event("load")));
}

function sendUnreachable(origin: string, source: MessageEventSource | null, code: string) {
  act(() => {
    window.dispatchEvent(new MessageEvent("message", {
      origin,
      source,
      data: { persona_preview: "unreachable", code },
    }));
  });
}

describe("PreviewPanel isolated preview origin mode", () => {
  it("keeps exact localized wording for manual switch and automatic fallback across en, zh-Hans, and ja", () => {
    expect((enPreview as Record<string, string>).mode_stream).toBe("View through the computer");
    expect((zhPreview as Record<string, string>).mode_stream).toBe("通过电脑查看");
    expect((jaPreview as Record<string, string>).mode_stream).toBe("コンピュータ経由で表示");

    expect((enPreview as Record<string, string>).mode_direct).toBe("View directly");
    expect((zhPreview as Record<string, string>).mode_direct).toBe("直接查看");
    expect((jaPreview as Record<string, string>).mode_direct).toBe("直接表示");

    expect((enPreview as Record<string, string>).direct_fallback).toBe("Couldn't show the page directly, so it's shown through the computer.");
    expect((zhPreview as Record<string, string>).direct_fallback).toBe("无法直接显示这个页面，已改为通过电脑显示。");
    expect((jaPreview as Record<string, string>).direct_fallback).toBe("このページを直接表示できなかったため、コンピュータ経由で表示しています。");
  });

  it("stays on browser stream unchanged when preview_origin is absent", async () => {
    mockCtx.features = ["preview"];
    await mountPanel(5173, "/");

    expect(mockCtx.grantCalls).toHaveLength(0);
    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
    expect(FakeSocket.instances).toHaveLength(1);
    expect(host.querySelector(`button[aria-label="${t("preview.back")}"]`)).not.toBeNull();
    expect(host.querySelector(`button[aria-label="${t("preview.forward")}"]`)).not.toBeNull();
    expect(host.querySelector(`button[aria-label="${t("preview.more")}"]`)).toBeNull();
  });

  it("defaults to direct iframe mode when preview_origin is present with safe sandbox, no-referrer, and hidden history buttons", async () => {
    await mountPanel(5173, "/");

    expect(mockCtx.grantCalls).toEqual([{ mid: "m1", port: 5173, path: "/" }]);
    expect(FakeSocket.instances).toHaveLength(0);
    expect(getCanvas()).toBeNull();

    const iframe = getIframe();
    expect(iframe).not.toBeNull();
    expect(iframe!.getAttribute("src")).toBe("https://p-5173.preview.example.test/__persona_preview_auth?code=c1");
    expect(iframe!.getAttribute("referrerpolicy")).toBe("no-referrer");

    const sandboxTokens = (iframe!.getAttribute("sandbox") ?? "").split(/\s+/).filter(Boolean);
    expect(sandboxTokens).toContain("allow-scripts");
    expect(sandboxTokens).toContain("allow-same-origin");
    expect(sandboxTokens.some((tok) => tok.startsWith("allow-top-navigation"))).toBe(false);

    // Cross-origin history buttons are hidden (not faked via parent history).
    expect(host.querySelector(`button[aria-label="${t("preview.back")}"]`)).toBeNull();
    expect(host.querySelector(`button[aria-label="${t("preview.forward")}"]`)).toBeNull();
    // Mode switch lives in the secondary menu, not as a top-level button.
    expect(host.querySelector(`button[aria-label="${t("preview.more")}"]`)).not.toBeNull();
    expect(host.textContent).not.toContain(t("preview.mode_stream"));

    // Desktop / Phone sizing works in direct iframe mode as well.
    const phoneBtn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("preview.size_desktop")}"]`)!;
    act(() => phoneBtn.click());
    await flush();
    const phoneFrame = host.querySelector<HTMLElement>("[data-preview-phone-frame]");
    expect(phoneFrame).not.toBeNull();
    expect(getIframe()!.style.width).toBe("390px");
    expect(getIframe()!.style.height).toBe("844px");
  });

  it("navigates same-port paths directly, reloads without replaying the one-time code, and re-grants when switching ports", async () => {
    await mountPanel(5173, "/");
    const first = getIframe()!;
    fireIframeLoad(first);

    // Same-port path navigation swaps src directly without another grant.
    const addr = host.querySelector<HTMLInputElement>("[data-preview-address]")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(addr, "/settings?tab=2");
      addr.dispatchEvent(new Event("input", { bubbles: true }));
      addr.form!.requestSubmit();
    });
    await flush();
    expect(mockCtx.grantCalls).toHaveLength(1);
    const afterNav = getIframe()!;
    expect(afterNav.getAttribute("src")).toBe("https://p-5173.preview.example.test/settings?tab=2");
    fireIframeLoad(afterNav);

    // Reload mounts a fresh iframe at the resolved origin+path (never reusing __persona_preview_auth?code=...).
    const reloadBtn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("preview.reload")}"]`)!;
    act(() => reloadBtn.click());
    await flush();
    const afterReload = getIframe()!;
    expect(afterReload).not.toBe(afterNav);
    expect(afterReload.getAttribute("src")).toBe("https://p-5173.preview.example.test/settings?tab=2");
    expect(mockCtx.grantCalls).toHaveLength(1);
    fireIframeLoad(afterReload);

    // Navigating to a different localhost port triggers a fresh grant for that port.
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(addr, "http://localhost:6006/story");
      addr.dispatchEvent(new Event("input", { bubbles: true }));
      addr.form!.requestSubmit();
    });
    await flush();
    expect(mockCtx.grantCalls).toHaveLength(2);
    expect(mockCtx.grantCalls[1]).toEqual({ mid: "m1", port: 6006, path: "/story" });
    expect(getIframe()!.getAttribute("src")).toBe("https://p-6006.preview.example.test/__persona_preview_auth?code=c2");
  });

  it("falls back to stream on grant failure, shows notice, and remembers fallback for that machine+port in the panel", async () => {
    mockCtx.grantHandler = async (_mid, body) => {
      if (body.port === 5173) throw new ApiError(502, "bad gateway", { code: "bad_gateway" });
      const origin = `https://p-${body.port}.preview.example.test`;
      return { url: `${origin}/__persona_preview_auth?code=ok`, origin };
    };
    await mountPanel(5173, "/");

    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
    expect(FakeSocket.instances).toHaveLength(1);
    const notice = host.querySelector("[data-preview-bar='notice']");
    expect(notice).not.toBeNull();
    expect(notice!.textContent).toContain(t("preview.direct_fallback"));

    // Switch to another port (6006) -> tries direct mode cleanly.
    const serversBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("preview.servers")))!;
    act(() => serversBtn.click());
    await flush();
    const sbItem = [...document.querySelectorAll<HTMLElement>("[role='menuitemradio']")].find((el) => el.textContent?.includes("storybook"))!;
    act(() => sbItem.click());
    await flush();
    expect(getIframe()).not.toBeNull();
    expect(mockCtx.grantCalls.filter((c) => c.port === 6006)).toHaveLength(1);

    // Switch back to 5173 -> remembered fallback stays on stream without calling grant again.
    act(() => serversBtn.click());
    await flush();
    const webItem = [...document.querySelectorAll<HTMLElement>("[role='menuitemradio']")].find((el) => el.textContent?.includes("web"))!;
    act(() => webItem.click());
    await flush();
    expect(mockCtx.grantCalls.filter((c) => c.port === 5173)).toHaveLength(1);
    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
  });

  it.each(["port_gone", "offline", "timeout", "too_many_connections", "port_not_allowed", "bad_gateway"])(
    "falls back to stream on verified unreachable postMessage (%s)",
    async (code) => {
      await mountPanel(5173, "/");
      const iframe = getIframe()!;
      sendUnreachable("https://p-5173.preview.example.test", iframe.contentWindow, code);
      await flush();

      expect(getIframe()).toBeNull();
      expect(getCanvas()).not.toBeNull();
      expect(FakeSocket.instances).toHaveLength(1);
      expect(host.querySelector("[data-preview-bar='notice']")?.textContent).toContain(t("preview.direct_fallback"));
    },
  );

  it("retries grant once on auth_failed and stays direct if retry succeeds, or falls back if auth_failed repeats", async () => {
    await mountPanel(5173, "/");
    const first = getIframe()!;
    expect(mockCtx.grantCalls).toHaveLength(1);

    // First auth_failed triggers one re-grant without falling back yet.
    sendUnreachable("https://p-5173.preview.example.test", first.contentWindow, "auth_failed");
    await flush();
    expect(mockCtx.grantCalls).toHaveLength(2);
    expect(FakeSocket.instances).toHaveLength(0);
    const second = getIframe()!;
    expect(second.getAttribute("src")).toBe("https://p-5173.preview.example.test/__persona_preview_auth?code=c2");

    // Second auth_failed falls back immediately without granting a third time.
    sendUnreachable("https://p-5173.preview.example.test", second.contentWindow, "auth_failed");
    await flush();
    expect(mockCtx.grantCalls).toHaveLength(2);
    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
    expect(FakeSocket.instances).toHaveLength(1);
    expect(host.querySelector("[data-preview-bar='notice']")?.textContent).toContain(t("preview.direct_fallback"));
  });

  it("falls back if the auth_failed retry grant itself fails", async () => {
    mockCtx.grantHandler = async (_mid, body, attempt) => {
      if (attempt >= 2) throw new ApiError(401, "unauthorized", { code: "auth_failed" });
      const origin = `https://p-${body.port}.preview.example.test`;
      return { url: `${origin}/__persona_preview_auth?code=c1`, origin };
    };
    await mountPanel(5173, "/");
    const first = getIframe()!;
    sendUnreachable("https://p-5173.preview.example.test", first.contentWindow, "auth_failed");
    await flush();

    expect(mockCtx.grantCalls).toHaveLength(2);
    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
    expect(FakeSocket.instances).toHaveLength(1);
    expect(host.querySelector("[data-preview-bar='notice']")?.textContent).toContain(t("preview.direct_fallback"));
  });

  it("ignores unreachable messages from another origin or another window", async () => {
    await mountPanel(5173, "/");
    const iframe = getIframe()!;
    const decoy = document.createElement("iframe");
    document.body.append(decoy);
    try {
      // Wrong origin, right source window -> ignored.
      sendUnreachable("https://evil.example.test", iframe.contentWindow, "port_gone");
      // Right origin, wrong source window -> ignored.
      sendUnreachable("https://p-5173.preview.example.test", decoy.contentWindow, "port_gone");
      // Right origin, parent window as source -> ignored.
      sendUnreachable("https://p-5173.preview.example.test", window, "auth_failed");
      await flush();

      expect(getIframe()).toBe(iframe);
      expect(getCanvas()).toBeNull();
      expect(FakeSocket.instances).toHaveLength(0);
      expect(mockCtx.grantCalls).toHaveLength(1);
    } finally {
      decoy.remove();
    }
  });

  it("falls back to stream if the iframe does not load before the timeout", async () => {
    vi.useFakeTimers();
    await mountPanel(5173, "/");
    expect(getIframe()).not.toBeNull();
    expect(FakeSocket.instances).toHaveLength(0);

    act(() => {
      vi.advanceTimersByTime(DIRECT_LOAD_TIMEOUT_MS - 100);
    });
    expect(getIframe()).not.toBeNull();
    expect(FakeSocket.instances).toHaveLength(0);

    act(() => {
      vi.advanceTimersByTime(200);
    });
    await flush();
    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
    expect(FakeSocket.instances).toHaveLength(1);
    expect(host.querySelector("[data-preview-bar='notice']")?.textContent).toContain(t("preview.direct_fallback"));
  });

  it("allows manual switching to stream and back to direct from the preview bar secondary menu", async () => {
    await mountPanel(5173, "/");
    fireIframeLoad(getIframe()!);

    // Open secondary menu -> "View through the computer"
    const moreBtn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("preview.more")}"]`)!;
    act(() => moreBtn.click());
    await flush();
    const toStream = [...document.querySelectorAll<HTMLElement>("[role='menuitem']")].find((el) => el.textContent?.includes(t("preview.mode_stream")))!;
    expect(toStream).toBeDefined();
    act(() => toStream.click());
    await flush();

    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
    expect(FakeSocket.instances).toHaveLength(1);
    // Manual switch does not show the automatic fallback error notice.
    expect(host.querySelector("[data-preview-bar='notice']")).toBeNull();

    // Open secondary menu again -> "View directly"
    const moreBtn2 = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("preview.more")}"]`)!;
    act(() => moreBtn2.click());
    await flush();
    const toDirect = [...document.querySelectorAll<HTMLElement>("[role='menuitem']")].find((el) => el.textContent?.includes(t("preview.mode_direct")))!;
    expect(toDirect).toBeDefined();
    act(() => toDirect.click());
    await flush();

    expect(getIframe()).not.toBeNull();
    expect(getCanvas()).toBeNull();
    expect(mockCtx.grantCalls).toHaveLength(2);
    expect(FakeSocket.instances[0].closed).toBe(true);
  });

  it("switches to stream mode with an explanation and runs Pick element when clicked in direct mode", async () => {
    await mountPanel(5173, "/");
    fireIframeLoad(getIframe()!);

    const pickBtn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("preview.pick")}"]`)!;
    expect(pickBtn.disabled).toBe(false);
    act(() => pickBtn.click());
    await flush();

    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
    expect(host.querySelector("[data-preview-bar='notice']")?.textContent).toContain(t("preview.switched_for_pick"));

    const sock = FakeSocket.last!;
    expect(sock).not.toBeNull();
    sock.open();
    sock.receiveJson({ t: "ready", browser: "Chrome", w: 640, h: 800 });
    await flush();
    expect(sock.sent).toContainEqual({ t: "pick", on: true });
    expect(host.querySelector("[data-preview-picking]")).not.toBeNull();
  });

  it("switches to stream mode with an explanation and captures a screenshot when clicked in direct mode", async () => {
    await mountPanel(5173, "/");
    fireIframeLoad(getIframe()!);

    const shotBtn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("preview.screenshot")}"]`)!;
    expect(shotBtn.disabled).toBe(false);
    act(() => shotBtn.click());
    await flush();
    const visibleItem = [...document.querySelectorAll<HTMLElement>("[role='menuitem']")].find((el) => el.textContent?.includes(t("preview.shot_view")))!;
    expect(visibleItem).toBeDefined();
    act(() => visibleItem.click());
    await flush();

    expect(getIframe()).toBeNull();
    expect(getCanvas()).not.toBeNull();
    expect(host.querySelector("[data-preview-bar='notice']")?.textContent).toContain(t("preview.switched_for_shot"));

    const sock = FakeSocket.last!;
    expect(sock).not.toBeNull();
    sock.open();
    sock.receiveJson({ t: "ready", browser: "Chrome", w: 640, h: 800 });
    sock.receiveJson({ t: "page", url: "http://localhost:5173/", title: "App", loading: false, back: false, forward: false });
    await flush();
    expect(sock.sent).toContainEqual({ t: "shot", id: 1, full: false });
  });
});
