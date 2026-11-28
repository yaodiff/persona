// @vitest-environment jsdom
// The start page in the drawn app (#9071): the sidebar gives way to the main chat when the page this load opened on is
// not there once the hub has said which pages there are, and Settings › General › Start page lists the pages, saves a
// choice to the hub and says why a chosen page does not open.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("../ui/atoms", async (original) => ({ ...(await original<typeof import("../ui/atoms")>()), toast: vi.fn() }));
import { toast } from "../ui/atoms";
import { ApiError, errorText } from "../lib/api";
import { t } from "../i18n";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class Socket {
  static OPEN = 1;
  static last: Socket;
  readyState = 1;
  onopen?: () => void;
  onmessage?: (event: { data: string }) => void;
  constructor(public url: string) {
    Socket.last = this;
  }
  send() {}
  close() {
    this.readyState = 3;
  }
  push(value: unknown) {
    this.onmessage?.({ data: JSON.stringify(value) });
  }
}

const TODAY = "p:example-today/today";
const todayPlace = { extension: "example-today", extension_name: "Example Today", id: "today", title: "Today", placement: "nav_page", icon: "calendar" };
const otherPlace = { extension: "example-notes", extension_name: "Notes", id: "notes", title: "Notes", placement: "nav_page" };
let places: unknown[];
let navigation: { start?: string } | "missing";
let puts: unknown[];
let starredIndex: Promise<number> | undefined;
let host: HTMLDivElement;
let unmount = () => {};

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
  localStorage.setItem("persona.token", "test-token");
  localStorage.setItem("persona.starredMessages", "1"); // Starred is off until switched on in Settings
  history.replaceState(null, "", "/");
  places = [todayPlace, otherPlace];
  navigation = {};
  puts = [];
  starredIndex = undefined;
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = new URL(String(input), location.href);
      if (url.pathname === "/api/v1/assistant/starred") {
        const status = starredIndex ? await starredIndex : 404;
        return Response.json(status === 200 ? { refs: [], can_write: true } : { error: "starred index unavailable" }, { status });
      }
      if (url.pathname === "/api/v1/extensions/-/ui") return Response.json({ ui: places });
      if (url.pathname === "/api/v1/navigation") {
        if (navigation === "missing") return Response.json({ error: "not found" }, { status: 404 });
        if (init?.method === "PUT") {
          const body = JSON.parse(String(init.body)) as { start: string };
          puts.push(body);
          navigation = { start: body.start };
        }
        return Response.json(navigation);
      }
      return Response.json({});
    }),
  );
  host = document.createElement("div");
  document.body.append(host);
});

afterEach(async () => {
  unmount();
  host.remove();
  vi.unstubAllGlobals();
});

// Each test imports the sidebar (or the settings group) afresh (resetModules): cold, under a full parallel run, the first
// took more than vitest's 5 s, so the describes allow 30 s, as SidebarCollapse.test.tsx does.
/** A fresh load with `stored` as this browser's copy: opened as the app opens, then `ui` drawn and the hub's hello. */
async function open(stored: string | undefined, ui: "sidebar" | "settings", features = ["extensions", "extension_ui_slots"]) {
  if (stored !== undefined) localStorage.setItem("persona.startPage", stored);
  // the fresh modules' catalog, as src/i18n/test-setup.ts loads it for the first ones
  await (await import("../i18n")).initI18n("zh-Hans");
  const { act, createElement } = await import("react");
  const { createRoot } = await import("react-dom/client");
  const page = await import("../lib/start-page");
  const store = await import("../lib/store");
  const router = await import("../lib/router");
  page.openStartPage();
  const node =
    ui === "sidebar"
      ? createElement((await import("./Sidebar")).Sidebar, { route: router.parse(location.pathname), onSearch() {}, onNewAgent() {} })
      : createElement((await import("./settings/StartPage")).StartPageGroup);
  const root = createRoot(host);
  act(() => root.render(node));
  unmount = () => {
    act(() => root.unmount());
    store.reset();
  };
  const flush = async () => {
    for (let i = 0; i < 8; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
  };
  const hello = async () => {
    store.connect();
    act(() => Socket.last.push({ type: "hello", features }));
    await flush();
  };
  return { act, page, router, store, hello, flush };
}

describe("opening on a page that is not there", { timeout: 30_000 }, () => {
  it("gives way to the main chat once the hub lists the extension pages without it", async () => {
    places = [otherPlace];
    const { hello } = await open(TODAY, "sidebar");
    expect(location.pathname).toBe("/extension/example-today/today");
    await hello();
    expect(location.pathname).toBe("/");
    // the choice stays: the next opening tries it again
    expect(localStorage.getItem("persona.startPage")).toBe(TODAY);
  });

  it("stays on Today when the hub lists it", async () => {
    const { hello } = await open(TODAY, "sidebar");
    await hello();
    expect(location.pathname).toBe("/extension/example-today/today");
  });

  it("gives way on a hub with no extension pages at all", async () => {
    const { hello } = await open(TODAY, "sidebar", []);
    await hello();
    expect(location.pathname).toBe("/");
  });

  it("leaves the user where they went before the hub answered", async () => {
    places = [otherPlace];
    const { hello, router, act } = await open(TODAY, "sidebar");
    act(() => router.navigate({ name: "inbox" }));
    await hello();
    expect(location.pathname).toBe("/inbox");
  });

  it.each([200, 404, 500])("settles a Starred start page only after a definitive index answer (HTTP %i)", async (status) => {
    let resolve = (_status: number) => {};
    starredIndex = new Promise<number>((done) => { resolve = done; });
    const { hello, flush } = await open("starred", "sidebar", ["starred_messages"]);
    await hello();
    await vi.waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([path]) => String(path).includes("/assistant/starred?index=1"))).toBe(true));
    expect(location.pathname).toBe("/starred");
    resolve(status);
    await flush();
    expect(location.pathname).toBe(status === 404 ? "/" : "/starred");
    expect(localStorage.getItem("persona.startPage")).toBe("starred");
  });

  it("gives way when the hub does not advertise Starred", async () => {
    const { hello } = await open("starred", "sidebar", []);
    await hello();
    expect(location.pathname).toBe("/");
  });

  it("keeps a built-in page, which is always there", async () => {
    const { hello } = await open("routines", "sidebar");
    expect(location.pathname).toBe("/routines");
    await hello();
    expect(location.pathname).toBe("/routines");
  });
});

describe("Settings › General › Start page", { timeout: 30_000 }, () => {
  const select = () => host.querySelector<HTMLSelectElement>("select")!;
  const options = () => [...select().options].map((o) => ({ value: o.value, text: o.textContent, disabled: o.disabled }));

  it("offers the main chat first and selected by default, then the sidebar's pages, extensions' too", async () => {
    const { hello } = await open(undefined, "settings");
    await hello();
    expect(select().value).toBe("");
    const list = options();
    expect(list[0]).toEqual({ value: "", text: "主对话", disabled: false });
    expect(list.map((o) => o.value)).toContain("home");
    expect(list.map((o) => o.value)).not.toContain("settings");
    expect(list.find((o) => o.value === TODAY)?.text).toBe("Today · Example Today");
    expect(host.textContent).toContain("将在你的所有设备间同步。");
  });

  it("saves a choice to the hub", async () => {
    const { hello, act, page } = await open(undefined, "settings");
    await hello();
    act(() => {
      select().value = TODAY;
      select().dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    expect(puts).toEqual([{ start: TODAY }]);
    expect(page.startPage()).toBe(TODAY);
    expect(select().value).toBe(TODAY);
  });

  it("puts the previous choice back and reports a refused save", async () => {
    navigation = { start: "home" };
    const { hello, act, page } = await open("home", "settings");
    await hello();
    vi.mocked(fetch).mockImplementation(async () => Response.json({ error: "database down" }, { status: 503 }));
    act(() => {
      select().value = TODAY;
      select().dispatchEvent(new Event("change", { bubbles: true }));
    });
    const error = new ApiError(503, "database down", { error: "database down" });
    await vi.waitFor(() => expect(vi.mocked(toast)).toHaveBeenCalledWith(t("settings.start_page_failed", { error: errorText(error) }), "error"));
    expect(page.startPage()).toBe("home");
    expect(select().value).toBe("home");
  });

  it("says why a chosen page does not open, and keeps it chosen", async () => {
    navigation = { start: "p:gone/page" };
    const { hello } = await open("p:gone/page", "settings");
    await hello();
    expect(select().value).toBe("p:gone/page");
    expect(options().at(-1)).toEqual({ value: "p:gone/page", text: "当前不可用的页面", disabled: true });
    expect(host.textContent).toContain("你选择的页面当前不可用，在其恢复前，Persona 将默认打开主对话。");
  });

  it("says the choice stays in this browser on a hub without the route", async () => {
    navigation = "missing";
    const { hello } = await open(undefined, "settings");
    await hello();
    expect(host.textContent).toContain("仅保存在当前浏览器中");
  });
});
