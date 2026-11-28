// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ api: vi.fn(), owner: "first", change: () => {} }));
vi.mock("../lib/api", () => ({
  api: hub.api,
  credentialOwner: () => hub.owner,
  getToken: () => "fixture",
  onCredentialChange: (change: () => void) => { hub.change = change; },
  ApiError: class extends Error { constructor(readonly status: number) { super(); } },
}));

import { ApiError } from "../lib/api";
import { fontSettings, normalizeFonts, setBrowserFonts } from "./font-preference";
import { refreshSharedFonts, saveFontChoice } from "./font-sync";

const shared = normalizeFonts({ sans: { preset: "inter" }, mono: { preset: "jetbrains" } });

beforeEach(() => {
  localStorage.clear();
  hub.api.mockReset();
  hub.change();
});

describe("web font synchronization", () => {
  it("reads shared defaults, saves only the changed choice and keeps browser overrides local", async () => {
    hub.api.mockResolvedValueOnce(shared);
    await refreshSharedFonts();
    expect(fontSettings().fonts).toEqual(shared);
    hub.api.mockResolvedValueOnce({ ...shared, mono: { preset: "fira" } });
    expect(await saveFontChoice("mono", { preset: "fira" })).toBe(true);
    expect(hub.api).toHaveBeenLastCalledWith("/settings/web-fonts", { method: "PUT", json: { mono: { preset: "fira" } } });
    setBrowserFonts(true);
    expect(await saveFontChoice("sans", { preset: "serif" })).toBe(true);
    expect(hub.api).toHaveBeenCalledTimes(2);
    expect(fontSettings().fonts.sans.preset).toBe("serif");
    setBrowserFonts(false);
    expect(fontSettings().fonts.mono.preset).toBe("fira");
    expect(fontSettings().fonts.sans.preset).toBe("inter");
  });

  it("ignores a GET begun before a save and retains the acknowledged cache after a failed save", async () => {
    let respond!: (value: typeof shared) => void;
    hub.api.mockReturnValueOnce(new Promise((resolve) => { respond = resolve; }));
    const read = refreshSharedFonts();
    const saved = { ...shared, sans: { preset: "serif" } };
    hub.api.mockResolvedValueOnce(saved);
    await saveFontChoice("sans", saved.sans);
    respond(shared);
    await read;
    expect(fontSettings().fonts).toEqual(saved);
    hub.api.mockRejectedValueOnce(new Error("offline"));
    expect(await saveFontChoice("sans", { preset: "georgia" })).toBe(false);
    expect(fontSettings().fonts).toEqual(saved);
  });

  it("saves locally on an older hub and ignores a reply for a retired credential", async () => {
    hub.api.mockRejectedValueOnce(new ApiError(404, "not found", {}));
    await refreshSharedFonts();
    expect(await saveFontChoice("mono", { preset: "menlo" })).toBe(true);
    expect(hub.api).toHaveBeenCalledTimes(1);
    expect(fontSettings().browser).toBe(true);
    setBrowserFonts(false);
    let respond!: (value: typeof shared) => void;
    hub.api.mockReturnValueOnce(new Promise((resolve) => { respond = resolve; }));
    const read = refreshSharedFonts();
    hub.owner = "second";
    hub.change();
    respond(shared);
    await read;
    expect(fontSettings().fonts.mono.preset).toBe("default");
  });
});
