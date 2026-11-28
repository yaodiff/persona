// Domain spaces on the web (#13500): which tracks the sidebar lists, the slug a title suggests, the words a failed
// SPACE.md write maps to, the addresses of a space's pages and the requests the client sends.
import { beforeEach, describe, expect, it, vi } from "vitest";

const calls = vi.hoisted(() => [] as { path: string; init?: { method?: string; json?: unknown } }[]);
vi.mock("./api", async (orig) => ({
  ...(await orig<typeof import("./api")>()),
  api: (path: string, init?: { method?: string; json?: unknown }) => {
    calls.push({ path, init });
    if (path === "/assistant/extension-trust") return Promise.resolve({ items: [{ extension: "notes", trust: "owner", categories: [] }] });
    return Promise.resolve({});
  },
}));

import { api, ApiError } from "./api";
import type { ScopedAssistantClient } from "./scoped-assistant-api";
import { listScopedExtensionTrust, setScopedExtensionTrust } from "./scoped-extension-trust";
import { MODULES } from "../modules";
import { sidebarActiveKey } from "./persona-nav";
import { href, parse } from "./router";
import {
  changeConversationCategories,
  changeTrack,
  createTrack,
  formSlug,
  listExtensionTrust,
  listTracks,
  sidebarSpaces,
  spaceIcon,
  spaceMdError,
  startSpaceConversation,
  suggestSlug,
  TracksUnsupported,
  trustOf,
  validSlug,
  writeSpaceMd,
  type Track,
} from "./tracks";
// the health module, when this run has it (the core-only lane, #13932, runs with no module)
const HEALTH = MODULES.some((m) => m.id === "health");

const track = (slug: string, over: Partial<Track> = {}): Track => ({
  slug,
  kind: "space",
  title: slug,
  icon: "",
  description: "",
  default_categories: [],
  template: "space",
  builtin: false,
  archived: false,
  revision: 1,
  config: { schema: 1, template: "space", context: "track", default_categories: [], tag_focus: [], rhythms: [], attention: { push_cap: "P0", deep_default: "quick", enter_mode: "none" }, supervision: "none", usage: [], assembly: "track", capture_target: true, channels: [], sidebar: { group: "spaces" } },
  ...over,
});

beforeEach(() => {
  calls.length = 0;
});

describe("the sidebar's spaces", () => {
  it("lists the spaces group, leaving out the health space, archived tracks and other groups", () => {
    const list = [
      track("health", { builtin: true, template: "health" }),
      track("family", { title: "Family" }),
      track("old", { archived: true }),
      track("work", { config: { ...track("x").config, sidebar: { group: "directions" } } }),
      track("money", { title: "Money" }),
    ];
    expect(sidebarSpaces(list).map((x) => x.slug)).toEqual(["family", "money"]);
  });

  it("orders by the kind's sidebar order, then by title", () => {
    const first = track("zeta", { title: "Zeta", config: { ...track("x").config, sidebar: { group: "spaces", order: -1 } } });
    expect(sidebarSpaces([track("b", { title: "B" }), track("a", { title: "A" }), first]).map((x) => x.slug)).toEqual(["zeta", "a", "b"]);
  });

  it("draws a known icon token, else layers", () => {
    expect(spaceIcon(track("a", { icon: "wallet" }))).toBe("wallet");
    expect(spaceIcon(track("a", { icon: "heart.fill" }))).toBe("layers");
    expect(spaceIcon(track("a", { config: { ...track("x").config, sidebar: { group: "spaces", icon: "book" } } }))).toBe("book");
    expect(spaceIcon(undefined)).toBe("layers");
  });
});

describe("slugs", () => {
  it("checks a slug as the server does", () => {
    expect(validSlug("family")).toBe(true);
    expect(validSlug("a")).toBe(false);
    expect(validSlug("9lives")).toBe(false);
    expect(validSlug("Family")).toBe(false);
    expect(validSlug("health")).toBe(false);
    expect(validSlug("tracks")).toBe(false);
    expect(validSlug("x".repeat(33))).toBe(false);
  });

  it("keeps /tracks/new for the form", () => {
    expect(validSlug("new")).toBe(true);
    expect(formSlug("new")).toBe(false);
  });

  it("suggests one from a title", () => {
    expect(suggestSlug("Family & Friends")).toBe("family-friends");
    expect(suggestSlug("Café notes")).toBe("cafe-notes");
    expect(suggestSlug("2026 Taxes")).toBe("taxes");
    expect(suggestSlug("家人")).toBe("space");
    expect(suggestSlug("家人", ["space"])).toBe("space-2");
    expect(suggestSlug("Health")).toBe("health-2");
    expect(suggestSlug("New")).toBe("new-2");
    for (const s of [suggestSlug("x".repeat(80)), suggestSlug("Family"), suggestSlug("家人")]) expect(formSlug(s)).toBe(true);
  });
});

describe("SPACE.md errors", () => {
  it("tells a stale base, a managed block touched, a missing directory and an offline computer apart", () => {
    const err = (status: number) => new ApiError(status, "x", {});
    expect(spaceMdError(err(409))).toBe("stale");
    expect(spaceMdError(err(400))).toBe("managed");
    expect(spaceMdError(err(404))).toBe("missing");
    expect(spaceMdError(err(503))).toBe("offline");
    expect(spaceMdError(err(500))).toBe("other");
    expect(spaceMdError(new TypeError("Failed to fetch"))).toBe("other");
  });
});

describe("addresses", () => {
  it("routes a space's pages and keeps /health for the health space", () => {
    expect(parse("/tracks/family")).toEqual({ name: "track", slug: "family", sid: undefined });
    expect(parse("/tracks/family/new")).toEqual({ name: "track", slug: "family", fresh: true });
    expect(parse("/tracks/family/c1")).toEqual({ name: "track", slug: "family", sid: "c1" });
    expect(parse("/tracks/new")).toEqual({ name: "track-new" });
    // the health module's page (src/modules/health), registered by the module list; a space's page without it
    if (HEALTH) {
      expect(parse("/tracks/health")).toEqual({ name: "module", module: "health", base: "health", path: [] });
      expect(parse("/tracks/health/new")).toEqual({ name: "module", module: "health", base: "health", path: ["new"] });
    }
    expect(href({ name: "track", slug: "family" })).toBe("/tracks/family");
    expect(href({ name: "track", slug: "family", fresh: true })).toBe("/tracks/family/new");
    expect(href({ name: "track", slug: "family", sid: "c 1" })).toBe("/tracks/family/c%201");
    expect(href({ name: "track-new" })).toBe("/tracks/new");
  });

  it("lights the space's own row", () => {
    expect(sidebarActiveKey({ name: "track", slug: "family", sid: "c1" })).toBe("track:family");
    expect(sidebarActiveKey({ name: "track-new" })).toBe("track-new");
    if (HEALTH) expect(sidebarActiveKey({ name: "module", module: "health", base: "health", path: [] })).toBe("health");
  });
});

describe("the client", () => {
  it("takes an answer without a list for an assistant without tracks", async () => {
    await expect(listTracks()).rejects.toBeInstanceOf(TracksUnsupported);
  });

  it("sends each request to its route", async () => {
    await createTrack({ slug: "family", kind: "space", title: "Family", icon: "users", description: "" });
    await changeTrack("family", { revision: 3, archived: true });
    await writeSpaceMd("family", "hello", "abc");
    await startSpaceConversation("family", { prompt: "hi" });
    await changeConversationCategories("c1", { remove: ["finance"] });
    expect(calls.map((c) => [c.init?.method ?? "GET", c.path, c.init?.json])).toEqual([
      ["POST", "/assistant/tracks", { slug: "family", kind: "space", title: "Family", icon: "users", description: "" }],
      ["PUT", "/assistant/tracks/family", { revision: 3, archived: true }],
      ["PUT", "/assistant/tracks/family/space-md", { content: "hello", base_digest: "abc" }],
      ["POST", "/assistant/conversations?space=family", { prompt: "hi", space: "family" }],
      ["PUT", "/assistant/conversations/c1/categories", { remove: ["finance"] }],
    ]);
  });

  it("reads an extension's trust, a third party's with no grant without a row", async () => {
    const client: ScopedAssistantClient = { managerId: "assistant", key: "notes", request: api, subscribe: () => () => {} };
    const items = (await listScopedExtensionTrust(client))!;
    expect(await listExtensionTrust()).toEqual(items);
    expect(trustOf(items, "notes")).toEqual({ trust: "owner", categories: [] });
    expect(trustOf(items, "weather")).toEqual({ trust: "third_party", categories: [] });
    await setScopedExtensionTrust(client, "notes", { trust: "third_party", categories: ["finance"] });
    expect(calls.map(({ path, init }) => [path, init?.method ?? "GET", init?.json])).toEqual([
      ["/assistant/extension-trust", "GET", undefined],
      ["/assistant/extension-trust", "GET", undefined],
      ["/assistant/extension-trust/notes", "PUT", { trust: "third_party", categories: ["finance"] }],
    ]);
  });
});
