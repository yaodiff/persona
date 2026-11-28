// @vitest-environment jsdom
// Top-level Spaces section in the Web sidebar (#17108): hidden at 0 spaces, shows spaces with conversation counts,
// inline conversation list, one-click new conversation, and quick SPACE.md editor without leaving the current page.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const hub = vi.hoisted(() => ({
  tracks: [] as Array<{ slug: string; title: string; icon?: string; description?: string; archived?: boolean }>,
  conversations: {} as Record<string, Array<{ session_id: string; title: string; updated_at?: string }>>,
  mdContent: "# Reading\n\nNotes for reading.\n",
}));

vi.mock("../lib/api", async (orig) => {
  const real = await orig<typeof import("../lib/api")>();
  const toTrack = (x: { slug: string; title: string; icon?: string; description?: string; archived?: boolean }) => ({
    slug: x.slug,
    kind: "space",
    title: x.title,
    icon: x.icon ?? "layers",
    description: x.description ?? "",
    default_categories: [],
    template: "space",
    builtin: false,
    archived: Boolean(x.archived),
    revision: 1,
    config: {
      schema: 1,
      template: "space",
      context: "track",
      default_categories: [],
      tag_focus: [],
      rhythms: [],
      attention: { push_cap: "P0", deep_default: "quick", enter_mode: "none" },
      supervision: "none",
      usage: [],
      assembly: "track",
      capture_target: true,
      channels: [],
      sidebar: { group: "spaces" },
    },
  });
  return {
    ...real,
    api: (path: string, init?: { method?: string }) => {
      const method = init?.method ?? "GET";
      if (path === "/assistant/tracks" && method === "GET") {
        return Promise.resolve({ tracks: hub.tracks.map(toTrack) });
      }
      if (path.startsWith("/assistant?space=")) {
        const slug = decodeURIComponent(path.slice("/assistant?space=".length));
        return Promise.resolve({
          machine_id: "mac-1",
          conversations: (hub.conversations[slug] ?? []).map((c) => ({
            machine_id: "mac-1",
            session_id: c.session_id,
            title: c.title,
            space: slug,
            status: "idle",
            state: "idle",
            cwd: "",
            assistant: true,
            updated_at: c.updated_at ?? "2026-11-27T08:00:00Z",
          })),
        });
      }
      if (path.endsWith("/space-md") && method === "GET") {
        return Promise.resolve({ content: hub.mdContent, digest: "d1", state: "" });
      }
      return Promise.resolve({});
    },
  };
});

import { t } from "../i18n";
import type { Route } from "../lib/router";
import { resetTracksForTest } from "../lib/tracks";
import { SidebarSpaces } from "./SidebarSpaces";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 10; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const noopReg = () => () => {};
const noopHover = () => {};

beforeEach(() => {
  hub.tracks = [];
  hub.conversations = {};
  localStorage.clear();
  resetTracksForTest();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("SidebarSpaces", () => {
  it("renders nothing when there are 0 active spaces", async () => {
    hub.tracks = [{ slug: "old", title: "Archived", archived: true }];
    act(() => root.render(<SidebarSpaces route={{ name: "persona" }} reg={noopReg} hover={noopHover} activeKey="persona" go={() => () => {}} />));
    await flush();
    expect(host.textContent).toBe("");
    expect(host.querySelector("[data-sidebar-spaces]")).toBeNull();
  });

  it("shows spaces with conversation counts, inline chats, new-chat link, and SPACE.md editor", async () => {
    hub.tracks = [
      { slug: "reading", title: "Reading", icon: "book", description: "Books and notes" },
      { slug: "empty-space", title: "Empty Space" },
    ];
    hub.conversations = {
      reading: [
        { session_id: "c1", title: "Book list", updated_at: "2026-11-27T09:00:00Z" },
        { session_id: "c2", title: "Chapter notes", updated_at: "2026-11-27T08:00:00Z" },
      ],
      "empty-space": [],
    };
    const navigated: Route[] = [];
    const go = (to: Route) => (e: React.MouseEvent) => {
      e.preventDefault();
      navigated.push(to);
    };

    act(() => root.render(<SidebarSpaces route={{ name: "track", slug: "reading", sid: "c1" }} reg={noopReg} hover={noopHover} activeKey="track:reading" go={go} />));
    await flush();

    expect(host.querySelector("[data-sidebar-spaces]")).not.toBeNull();
    expect(host.textContent).toContain("Reading");
    expect(host.textContent).toContain("Book list");
    expect(host.textContent).toContain("Chapter notes");

    // Conversation count badge on the space row
    const readingRow = host.querySelector('[data-sidebar-space="reading"]')!;
    expect(readingRow.textContent).toContain("2");

    // Clicking a space conversation navigates directly to /tracks/<slug>/<sid>
    const chatLink = [...readingRow.querySelectorAll("a")].find((a) => a.textContent?.includes("Chapter notes"))!;
    expect(chatLink.getAttribute("href")).toBe("/tracks/reading/c2");
    act(() => chatLink.click());
    expect(navigated[0]).toEqual({ name: "track", slug: "reading", sid: "c2" });

    // Clicking the space's quick new-chat button navigates to /tracks/<slug>/new
    const newChatLink = readingRow.querySelector(`a[aria-label="${t("spaces.new_chat_in", { name: "Reading" })}"]`) as HTMLAnchorElement;
    expect(newChatLink).not.toBeNull();
    expect(newChatLink.getAttribute("href")).toBe("/tracks/reading/new");
    act(() => newChatLink.click());
    expect(navigated[1]).toEqual({ name: "track", slug: "reading", fresh: true });

    // Clicking the SPACE.md button opens the editor dialog inline without leaving the page
    const mdBtn = readingRow.querySelector(`button[aria-label="${t("spaces.edit_md_for", { name: "Reading" })}"]`) as HTMLButtonElement;
    expect(mdBtn).not.toBeNull();
    act(() => mdBtn.click());
    await vi.waitFor(() => {
      expect(document.querySelector("textarea")?.value).toContain("Notes for reading.");
    });
    expect(document.body.textContent).toContain(t("spaces.md_title"));
  });
});
