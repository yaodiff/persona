// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  clearModelProfiles,
  deactivateModelProfile,
  deleteModelProfile,
} from "./model-profiles";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("model-profiles removal and deactivate API clients", () => {
  it("POSTs /api/v1/model-profiles/deactivate with optional manager header", async () => {
    const calls: { url: string; method?: string; manager: string | null }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const headers = new Headers(init?.headers);
        calls.push({ url: String(input), method: init?.method, manager: headers.get("X-Persona-Manager") });
        return Response.json({ revision: 2, active: "", profiles: [{ id: "economy", name: "Economy", changes: [] }] });
      }),
    );

    const res = await deactivateModelProfile("work");
    expect(res.active).toBe("");
    expect(calls).toEqual([{ url: "/api/v1/model-profiles/deactivate", method: "POST", manager: "work" }]);

    await deactivateModelProfile();
    expect(calls[1]).toEqual({ url: "/api/v1/model-profiles/deactivate", method: "POST", manager: null });
  });

  it("DELETEs /api/v1/model-profiles/{id} with encoded id and optional revision query", async () => {
    const calls: { url: string; method?: string; manager: string | null }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const headers = new Headers(init?.headers);
        calls.push({ url: String(input), method: init?.method, manager: headers.get("X-Persona-Manager") });
        return Response.json({ revision: 4, active: "", profiles: [] });
      }),
    );

    await deleteModelProfile("economy", 3, "assistant");
    await deleteModelProfile("my_profile", 0);
    await deleteModelProfile("legacy", undefined);

    expect(calls).toEqual([
      { url: "/api/v1/model-profiles/economy?revision=3", method: "DELETE", manager: "assistant" },
      { url: "/api/v1/model-profiles/my_profile?revision=0", method: "DELETE", manager: null },
      { url: "/api/v1/model-profiles/legacy", method: "DELETE", manager: null },
    ]);
  });

  it("DELETEs /api/v1/model-profiles with optional revision query", async () => {
    const calls: { url: string; method?: string; manager: string | null }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const headers = new Headers(init?.headers);
        calls.push({ url: String(input), method: init?.method, manager: headers.get("X-Persona-Manager") });
        return Response.json({ revision: 5, active: "", profiles: [] });
      }),
    );

    await clearModelProfiles(4, "work");
    await clearModelProfiles(0);
    await clearModelProfiles();

    expect(calls).toEqual([
      { url: "/api/v1/model-profiles?revision=4", method: "DELETE", manager: "work" },
      { url: "/api/v1/model-profiles?revision=0", method: "DELETE", manager: null },
      { url: "/api/v1/model-profiles", method: "DELETE", manager: null },
    ]);
  });
});
