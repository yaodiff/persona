// @vitest-environment jsdom
// On a phone, Settings' top bar holds the one way back from an item's page (audit item 64, Settings.tsx itemParent), so
// the page under it draws no Back of its own: a channel, the market's catalog, one of its extensions, the list of markets
// and a page an extension draws. Wider, each keeps its own.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { setToken } from "../../lib/api";

vi.mock("../../lib/channels", async (original) => ({
  ...(await original<typeof import("../../lib/channels")>()),
  channelDetail: () => Promise.resolve({ id: "demo", name: "Demo", kind: "im", source: "builtin", installed: true, enabled: true, state: "connected", capabilities: {}, bind: "none", features: [], waiting: { answers: 0, asks: 0 } }),
}));
import { ChannelPage } from "./Channels";
import { Extensions } from "./Extensions";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  setToken("fixture-token");
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://hub.test").pathname.replace(/^\/api\/v1/, "");
    if (path.endsWith("/catalog")) return Promise.resolve(json({ markets: [{ id: "example-market", name: "Example market" }], extensions: [{ market: "example-market", id: "example", name: "Example calendar" }] }));
    if (path.endsWith("/markets")) return Promise.resolve(json({ markets: [{ id: "example-market", name: "Example market", spec: "github:example/market" }] }));
    if (/^\/(extensions|plugins)\/example\/-\/$/.test(path)) return Promise.resolve(json({ id: "example", name: "Example calendar", ui: [{ id: "main", title: "Main" }] }));
    return Promise.resolve(json({}));
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});
const draw = async (ui: React.ReactNode) => {
  await act(async () => root.render(ui));
  for (let i = 0; i < 10; i++) await act(async () => { await new Promise((done) => setTimeout(done, 0)); });
};
/** The page's own Back: a quiet button with the back chevron, named for where it goes. */
const ownBack = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.trim() === label);

describe("an item's page under a phone's top bar", () => {
  it.each([true, false])("a channel's page draws its own Back only without the top bar's (headerBack %s)", async (headerBack) => {
    await draw(<ChannelPage id="demo" onBack={() => {}} onSettings={() => {}} headerBack={headerBack} />);
    expect(host.textContent).toContain("Demo");
    expect(ownBack(t("settings-channels.title"))).toHaveLength(headerBack ? 0 : 1);
  });

  it.each([
    { open: "-market", label: () => t("settings-extensionmarket.extensions") },
    { open: "-market/example-market/example", label: () => t("settings-extensionmarket.market_title") },
    { open: "-markets", label: () => t("settings-extensionmarket.market_title") },
    { open: "example/ui/main", label: () => "Example calendar" },
  ])("extension page $open draws its own Back only without the top bar's", async ({ open, label }) => {
    await draw(<Extensions open={open} headerBack onOpen={() => {}} />);
    expect(ownBack(label())).toHaveLength(0);
    await draw(<Extensions key="wide" open={open} onOpen={() => {}} />);
    expect(ownBack(label())).toHaveLength(1);
  });
});
