// @vitest-environment jsdom
// Health with no module registered (the core-only case): its stored conversations, reports and memory stay readable
// in the default panel, with no writer. Registering a minimal enabled descriptor also checks the other switch state.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({
  calls: [] as { path: string; method: string }[],
  composer: 0,
  chatReadOnly: undefined as boolean | undefined,
  tracksSupported: true,
}));

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

vi.mock("../../lib/api", async (orig) => {
  const real = await orig<typeof import("../../lib/api")>();
  const track = {
    slug: "health", kind: "health", title: "Health", icon: "heart", description: "Your health history", default_categories: ["health"], template: "health", builtin: true, archived: false, revision: 1,
    config: { schema: 1, template: "health", context: "track", default_categories: ["health"], tag_focus: [], rhythms: [], attention: { push_cap: "P0", deep_default: "quick", enter_mode: "none" }, supervision: "none", usage: [], assembly: "track", capture_target: true, channels: [], sidebar: { group: "spaces" } },
  };
  return {
    ...real,
    api: (path: string, init?: { method?: string }) => {
      const method = init?.method ?? "GET";
      hub.calls.push({ path, method });
      if (path === "/assistant/tracks") return Promise.resolve(hub.tracksSupported ? { tracks: [track] } : {});
      if (path === "/assistant?space=health") return Promise.resolve({ machine_id: "mac", conversations: [{ machine_id: "mac", session_id: "c1", title: "Sleep review", space: "health", updated_at: "2026-11-20T08:00:00Z" }] });
      if (path === "/machines/mac/health/reports") return Promise.resolve({ reports: [{ name: "annual", title: "Annual checkup", path: "/reports/annual.pdf", record_path: "/reports/annual.md", size: 1200, modified_at: "2026-11-20T08:00:00Z" }] });
      if (path === "/assistant/memory?space=health") return Promise.resolve({ files: [{ name: "MEMORY.md", content: "- [Sleep pattern](sleep.md) — nightly routine" }, { name: "sleep.md", content: "---\ndescription: nightly routine\n---\nUsually wakes at 07:00." }] });
      if (path.startsWith("/machines/mac/file?path=")) return Promise.resolve({ text: "The stored report summary." });
      if (path === "/assistant/tracks/health/space-md") return Promise.resolve({ content: "# Health", digest: "d1", state: "" });
      return Promise.resolve({});
    },
  };
});
vi.mock("../chat/ChatView", () => ({ ChatView: ({ readOnly, headerActions }: { readOnly?: boolean; headerActions?: React.ReactNode }) => {
  hub.chatReadOnly = readOnly;
  return <><div data-testid="archive-chat" data-readonly={readOnly}>Stored transcript</div>{headerActions}</>;
} }));
vi.mock("../chat/ReadOnlyChatView", () => ({ ReadOnlyChatView: ({ headerActions }: { headerActions?: React.ReactNode }) => {
  hub.chatReadOnly = true;
  return <><div data-testid="archive-chat" data-readonly>Stored transcript</div>{headerActions}</>;
} }));
vi.mock("../chat/Composer", () => ({ Composer: () => {
  hub.composer++;
  return <div data-testid="composer" />;
} }));

import { registerModule, resetModulesForTest } from "../../core/module-host";
import { healthReadOnlyRoute } from "../../lib/health-archive";
import { resetTracksForTest } from "../../lib/tracks";
import { HealthArchiveLink } from "./HealthArchiveLink";
import { Space } from "./Space";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};
const button = (text: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.includes(text))!;

beforeEach(() => {
  hub.calls.length = 0;
  hub.composer = 0;
  hub.chatReadOnly = undefined;
  hub.tracksSupported = true;
  resetModulesForTest();
  resetTracksForTest();
  localStorage.clear();
  history.replaceState(null, "", "/");
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  resetModulesForTest();
  resetTracksForTest();
});

describe("Health's default-panel archive", () => {
  it("reads an old conversation list, report and memory without any write entry or request, even without the tracks API", async () => {
    hub.tracksSupported = false;
    act(() => root.render(<Space slug="health" fresh onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("h1")?.textContent).toBe("Health");
    expect(host.querySelector('[data-testid="health-readonly-note"]')?.textContent).toBe(t("spaces.health_read_only"));
    expect(host.textContent).toContain("Sleep review");
    expect(host.textContent).toContain("Annual checkup");
    expect(host.textContent).toContain("Sleep pattern");
    expect(hub.composer).toBe(0);
    expect(host.textContent).not.toContain(t("spaces.new_chat"));
    expect(host.textContent).not.toContain(t("spaces.md_edit"));
    // A built-in space has no SPACE.md, so asking for one would always 404 (server/internal/assistant/track_space.go):
    // the archive draws no card and makes no request (the 404 a core-only hub logged before, #16243).
    expect(host.querySelector('[data-testid="space-md"]')).toBeNull();
    expect(hub.calls.some((c) => c.path === "/assistant/tracks/health/space-md")).toBe(false);

    await act(async () => button("Annual checkup").click());
    await flush();
    expect(document.body.textContent).toContain("The stored report summary.");
    expect([...document.querySelectorAll("a")].some((a) => a.textContent?.includes(t("spaces.report_open")))).toBe(true);
    expect(document.body.textContent).not.toContain("Delete for good");
    await act(async () => button(t("spaces.close")).click());
    await act(async () => button("Sleep pattern").click());
    await flush();
    expect(document.body.textContent).toContain("Usually wakes at 07:00.");
    expect(hub.calls.filter((c) => c.method !== "GET")).toEqual([]);
  });

  it("opens a stored conversation in read-only mode and keeps history navigation but not New or category writes", async () => {
    act(() => root.render(<Space slug="health" sid="c1" onToggleSidebar={() => {}} />));
    await flush();
    expect(hub.chatReadOnly).toBe(true);
    expect(host.querySelector('[data-testid="archive-chat"]')?.textContent).toBe("Stored transcript");
    expect(host.querySelector(`button[aria-label="${t("spaces.history")}"]`)).not.toBeNull();
    expect(host.querySelector(`button[aria-label="${t("spaces.new_chat")}"]`)).toBeNull();
    expect(hub.calls.filter((c) => c.method !== "GET")).toEqual([]);
  });

  it("keeps old routes and the All chats read entry without a module, and removes both as soon as Health is enabled", async () => {
    expect(healthReadOnlyRoute(["health", "c1"])).toEqual({ name: "track", slug: "health", sid: "c1" });
    expect(healthReadOnlyRoute(["tracks", "health"])).toEqual({ name: "track", slug: "health", fresh: true });
    act(() => root.render(<HealthArchiveLink />));
    await flush();
    expect(host.querySelector('[data-testid="health-archive-entry"] a')?.getAttribute("href")).toBe("/tracks/health/new");
    registerModule({ id: "health", title: () => "Health", description: () => "Health", load: async () => ({}) });
    act(() => root.render(<HealthArchiveLink />));
    await flush();
    expect(healthReadOnlyRoute(["health"])).toBeNull();
    expect(host.querySelector('[data-testid="health-archive-entry"]')).toBeNull();
  });
});
