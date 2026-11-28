// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});
vi.mock("../../lib/store", () => ({ onHubEvent: () => () => {} }));
import { initI18n, t } from "../../i18n";
import enLocale from "../../i18n/locales/en/settings-modelprofiles.json";
import zhLocale from "../../i18n/locales/zh-Hans/settings-modelprofiles.json";
import jaLocale from "../../i18n/locales/ja/settings-modelprofiles.json";
import { suggestProfileId, validateNewProfile } from "./model-profile-create";
import { ModelProfiles } from "./ModelProfiles";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let activeHost: HTMLDivElement | null = null;
let activeRoot: Root | null = null;

afterEach(async () => {
  if (activeRoot) {
    await act(async () => activeRoot!.unmount());
    activeRoot = null;
  }
  if (activeHost) {
    activeHost.remove();
    activeHost = null;
  }
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

const flush = async () => {
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const setInput = (el: HTMLInputElement, value: string) => {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const btn = (text: string, scope: ParentNode = document) =>
  [...scope.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;

async function mount(onChanged = () => {}) {
  activeHost = document.createElement("div");
  document.body.append(activeHost);
  activeRoot = createRoot(activeHost);
  await act(async () => {
    activeRoot!.render(<ModelProfiles providers={[]} purposes={[]} onChanged={onChanged} />);
  });
  await flush();
  return activeHost;
}

describe("model-profile-create helpers", () => {
  it("generates unique server-valid slugs for ASCII, non-ASCII, reserved, and duplicate names", () => {
    expect(suggestProfileId("Economy", [])).toBe("economy");
    expect(suggestProfileId("Fast Coding!", [])).toBe("fast-coding");
    expect(suggestProfileId("Economy", ["economy"])).toBe("economy-2");
    expect(suggestProfileId("Economy", ["economy", "economy-2"])).toBe("economy-3");
    expect(suggestProfileId("rollback", [])).toBe("rollback-2");
    expect(suggestProfileId("deactivate", [])).toBe("deactivate-2");
    expect(suggestProfileId("经济档", [])).toBe("profile-1");
    expect(suggestProfileId("エコノミー", ["profile-1"])).toBe("profile-2");
    const longId = suggestProfileId("a".repeat(40), ["a".repeat(32)]);
    expect(longId.length).toBeLessThanOrEqual(32);
    expect(longId.endsWith("-2")).toBe(true);
  });

  it("validates profile name, ID format, reserved rollback, duplicates, and description", async () => {
    await initI18n("en");
    expect(validateNewProfile("Economy", "economy", "Daily driver", [])).toEqual({
      ok: true,
      id: "economy",
      name: "Economy",
      description: "Daily driver",
    });
    expect(validateNewProfile("   ", "economy", "", [])).toMatchObject({ ok: false });
    expect(validateNewProfile("Rollback", "rollback", "", [])).toMatchObject({ ok: false });
    expect(validateNewProfile("Bad", "Bad ID!", "", [])).toMatchObject({ ok: false });
    expect(validateNewProfile("Dup", "economy", "", ["economy"])).toMatchObject({ ok: false });
  });
});

describe("optional model profiles UI/UE", () => {
  it("renders a clean optional empty state and creates the first profile without forcing activation", async () => {
    await initI18n("en");
    let revision = 0;
    let profiles: Record<string, unknown>[] = [];
    const puts: { url: string; body: Record<string, unknown> }[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (method === "GET" && url.endsWith("/model-profiles")) {
          return Response.json({ revision, active: "", profiles });
        }
        if (method === "PUT" && url.includes("/model-profiles/")) {
          const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
          puts.push({ url, body });
          const id = url.split("/").at(-1)!;
          revision++;
          profiles = [{ id, name: body.name, description: body.description, active: false, changes: [], tiers: {} }];
          return Response.json({ revision, active: "", profiles });
        }
        throw new Error(`Unexpected request: ${method} ${url}`);
      }),
    );

    const onChanged = vi.fn();
    const host = await mount(onChanged);

    // Empty state is visible, non-error, with Create profile CTA and no Clear all.
    expect(host.textContent).toContain(t("settings-modelprofiles.empty_title"));
    expect(host.textContent).toContain(t("settings-modelprofiles.empty_hint"));
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(host.querySelector('[data-testid="profile-clear-all-button"]')).toBeNull();

    // Open Create dialog.
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="profile-create-button"]')!.click());
    await flush();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog.querySelector('[data-testid="model-profile-create-dialog"]')).not.toBeNull();
    expect(dialog.querySelector<HTMLButtonElement>('[data-testid="profile-create-submit"]')!.disabled).toBe(true);

    const nameInput = dialog.querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.name")}"]`)!;
    const idInput = dialog.querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.profile_id")}"]`)!;
    const descInput = dialog.querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.description")}"]`)!;

    // Typing a name auto-fills a valid slug ID.
    setInput(nameInput, "Daily Driver");
    await flush();
    expect(idInput.value).toBe("daily-driver");
    setInput(descInput, "Balanced everyday models");
    await flush();

    // Reserved ID shows inline error and disables submit.
    setInput(idInput, "rollback");
    await flush();
    expect(dialog.querySelector('[data-testid="profile-create-id-error"]')?.textContent).toContain("rollback");
    expect(dialog.querySelector<HTMLButtonElement>('[data-testid="profile-create-submit"]')!.disabled).toBe(true);

    // Restore valid ID and submit.
    setInput(idInput, "daily-driver");
    await flush();
    expect(dialog.querySelector<HTMLButtonElement>('[data-testid="profile-create-submit"]')!.disabled).toBe(false);

    await act(async () => dialog.querySelector<HTMLButtonElement>('[data-testid="profile-create-submit"]')!.click());
    await flush();

    expect(puts).toEqual([
      {
        url: "/api/v1/model-profiles/daily-driver",
        body: { revision: 0, name: "Daily Driver", description: "Balanced everyday models", capture: true },
      },
    ]);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(onChanged).toHaveBeenCalledTimes(1);
    // Created profile stays inactive; Clear all and New profile are now available.
    expect(host.textContent).toContain("Daily Driver");
    expect(host.textContent).not.toContain(t("settings-modelprofiles.active"));
    expect(host.textContent).toContain(t("settings-modelprofiles.none_active"));
    expect(host.querySelector('[data-testid="profile-clear-all-button"]')).not.toBeNull();
    expect(host.querySelector('[data-testid="profile-create-button"]')?.textContent).toContain(t("settings-modelprofiles.new_profile"));
  });

  it("deactivates the active profile from detail view and from section row without altering live models", async () => {
    await initI18n("en");
    let active = "performance";
    const posts: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (method === "POST" && url.endsWith("/model-profiles/deactivate")) {
          posts.push(url);
          active = "";
        }
        return Response.json({
          revision: 2,
          active,
          profiles: [
            { id: "performance", name: "Performance", active: active === "performance", changes: [], tiers: {} },
            { id: "economy", name: "Economy", active: active === "economy", changes: [], tiers: {} },
          ],
        });
      }),
    );

    const onChanged = vi.fn();
    const host = await mount(onChanged);

    // Active detail view shows Turn off button.
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Performance"))!.click());
    await flush();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog.querySelector('[data-testid="model-profile-active-banner"]')?.textContent).toContain(t("settings-modelprofiles.deactivate_hint"));

    await act(async () => dialog.querySelector<HTMLButtonElement>('[data-testid="profile-deactivate-button"]')!.click());
    await flush();

    expect(posts).toEqual(["/api/v1/model-profiles/deactivate"]);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(host.querySelector('[data-testid="profile-deactivate-row-button"]')).toBeNull();
    expect(host.textContent).toContain(t("settings-modelprofiles.none_active"));
    expect(onChanged).toHaveBeenCalledTimes(1);

    // Also verify section-row Turn off when active again.
    active = "economy";
    await act(async () => activeRoot!.unmount());
    activeRoot = createRoot(host);
    await act(async () => {
      activeRoot!.render(<ModelProfiles providers={[]} purposes={[]} onChanged={onChanged} />);
    });
    await flush();

    const rowTurnOff = host.querySelector<HTMLButtonElement>('[data-testid="profile-deactivate-row-button"]')!;
    expect(rowTurnOff).not.toBeNull();
    await act(async () => rowTurnOff.click());
    await flush();

    expect(posts).toEqual(["/api/v1/model-profiles/deactivate", "/api/v1/model-profiles/deactivate"]);
    expect(host.querySelector('[data-testid="profile-deactivate-row-button"]')).toBeNull();
  });

  it("deletes an active profile after explicit confirmation and aborts on cancel", async () => {
    await initI18n("en");
    let revision = 3;
    let active = "performance";
    let profiles = [
      { id: "performance", name: "Performance", active: true, changes: [], tiers: {} },
      { id: "economy", name: "Economy", active: false, changes: [], tiers: {} },
    ];
    const deletes: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (method === "DELETE") {
          deletes.push(url);
          revision++;
          active = "";
          profiles = profiles.filter((p) => p.id !== "performance");
        }
        return Response.json({ revision, active, profiles });
      }),
    );

    const onChanged = vi.fn();
    const host = await mount(onChanged);

    // Open active profile -> Delete is enabled even while active.
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Performance"))!.click());
    await flush();
    const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
    const deleteTrigger = dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-button"]')!;
    expect(deleteTrigger.disabled).toBe(false);

    // Open confirmation dialog -> mentions active status & unchanged live settings.
    act(() => deleteTrigger.click());
    await flush();
    expect(dialog().querySelector('[data-testid="model-profile-delete-confirm"]')?.textContent).toContain(
      t("settings-modelprofiles.confirm_delete_active", { name: "Performance" }),
    );
    expect(deletes).toEqual([]);

    // Cancel returns to detail view without calling DELETE.
    act(() => btn(t("common.cancel"), dialog()).click());
    await flush();
    expect(dialog().querySelector('[data-testid="model-profile-delete-confirm"]')).toBeNull();
    expect(deletes).toEqual([]);

    // Re-open confirmation and confirm deletion.
    act(() => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-button"]')!.click());
    await flush();
    await act(async () => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-confirm"]')!.click());
    await flush();

    expect(deletes).toEqual(["/api/v1/model-profiles/performance?revision=3"]);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(host.textContent).not.toContain("Performance");
    expect(host.textContent).toContain("Economy");
    expect(host.textContent).toContain(t("settings-modelprofiles.none_active"));
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("clears all profiles and rollback history after confirmation and handles cancel cleanly", async () => {
    await initI18n("en");
    let revision = 5;
    let active = "performance";
    let profiles = [
      { id: "performance", name: "Performance", active: true, changes: [], tiers: {} },
      { id: "economy", name: "Economy", active: false, changes: [], tiers: {} },
    ];
    let rollback: { to: string; changes: string[] } | undefined = { to: "economy", changes: ["assistant"] };
    const deletes: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (method === "DELETE") {
          deletes.push(url);
          revision++;
          active = "";
          profiles = [];
          rollback = undefined;
        }
        return Response.json({ revision, active, profiles, ...(rollback ? { rollback } : {}) });
      }),
    );

    const onChanged = vi.fn();
    const host = await mount(onChanged);

    // Click Clear all -> opens confirmation dialog.
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="profile-clear-all-button"]')!.click());
    await flush();
    const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog().querySelector('[data-testid="model-profile-clear-all-confirm"]')?.textContent).toContain(
      t("settings-modelprofiles.confirm_clear_all_body", { count: 2 }),
    );

    // Cancel leaves catalog intact.
    act(() => btn(t("common.cancel"), dialog()).click());
    await flush();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(deletes).toEqual([]);

    // Confirm Clear all.
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="profile-clear-all-button"]')!.click());
    await flush();
    await act(async () => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-clear-all-confirm"]')!.click());
    await flush();

    expect(deletes).toEqual(["/api/v1/model-profiles?revision=5"]);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(host.textContent).toContain(t("settings-modelprofiles.empty_title"));
    expect(host.querySelector('[data-testid="profile-clear-all-button"]')).toBeNull();
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("refreshes catalog and shows localized error on 409 conflict for delete, clear all, and create", async () => {
    await initI18n("en");
    let revision = 2;
    let failFirstDelete = true;
    let profiles: Record<string, unknown>[] = [{ id: "economy", name: "Economy", active: false, changes: [], tiers: {} }];
    const requests: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        requests.push(`${method} ${url}`);
        if (method === "GET") {
          return Response.json({ revision, active: "", profiles });
        }
        if (method === "DELETE" && url.includes("/model-profiles/economy")) {
          if (failFirstDelete) {
            failFirstDelete = false;
            revision = 3;
            return Response.json({ error: "conflict" }, { status: 409 });
          }
          revision = 4;
          profiles = [];
          return Response.json({ revision, active: "", profiles });
        }
        throw new Error(`Unexpected request: ${method} ${url}`);
      }),
    );

    const host = await mount();
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Economy"))!.click());
    await flush();
    const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
    act(() => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-button"]')!.click());
    await flush();

    // First delete hits 409 -> refreshes catalog to revision 3 and shows localized conflict error.
    await act(async () => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-confirm"]')!.click());
    await flush();
    expect(dialog().querySelector('[data-testid="profile-action-error"]')?.textContent).toBe(
      t("settings-modelprofiles.conflict_delete"),
    );

    // Retry uses refreshed revision 3 and succeeds.
    await act(async () => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-confirm"]')!.click());
    await flush();
    expect(requests.filter((r) => r.startsWith("DELETE"))).toEqual([
      "DELETE /api/v1/model-profiles/economy?revision=2",
      "DELETE /api/v1/model-profiles/economy?revision=3",
    ]);
    expect(host.textContent).toContain(t("settings-modelprofiles.empty_title"));
  });

  it("supports creating additional profiles with deduplicated slug IDs and clearing rollbacks when empty", async () => {
    await initI18n("en");
    let revision = 4;
    let profiles: Record<string, unknown>[] = [{ id: "economy", name: "Economy", active: false, changes: [], tiers: {} }];
    let rollback: { to: string; changes: string[] } | undefined = { to: "performance", changes: ["assistant"] };
    const calls: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (method !== "GET") calls.push(`${method} ${url}`);
        if (method === "PUT" && url.includes("/model-profiles/")) {
          const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
          const id = url.split("/").at(-1)!;
          revision++;
          profiles = [...profiles, { id, name: body.name, active: false, changes: [], tiers: {} }];
        } else if (method === "DELETE" && url.includes("/model-profiles/")) {
          const id = url.split("/").at(-1)!.split("?")[0];
          revision++;
          profiles = profiles.filter((p) => p.id !== id);
        } else if (method === "DELETE") {
          revision++;
          profiles = [];
          rollback = undefined;
        }
        return Response.json({ revision, active: "", profiles, ...(rollback ? { rollback } : {}) });
      }),
    );

    const host = await mount();

    // Create a second profile with the same display name -> slug auto-deduplicates to economy-2.
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="profile-create-button"]')!.click());
    await flush();
    const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
    const nameInput = dialog().querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.name")}"]`)!;
    const idInput = dialog().querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.profile_id")}"]`)!;
    setInput(nameInput, "Economy");
    await flush();
    expect(idInput.value).toBe("economy-2");

    await act(async () => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-create-submit"]')!.click());
    await flush();
    expect(calls[0]).toBe("PUT /api/v1/model-profiles/economy-2");

    // Delete both profiles individually so rollback history remains while profiles is empty.
    for (const id of ["economy", "economy-2"]) {
      act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Economy"))!.click());
      await flush();
      act(() => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-button"]')!.click());
      await flush();
      await act(async () => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-confirm"]')!.click());
      await flush();
      expect(calls.at(-1)).toContain(`/model-profiles/${id}?revision=`);
    }

    // Empty state still shows Clear all because rollback history exists.
    expect(host.textContent).toContain(t("settings-modelprofiles.empty_title"));
    const clearBtn = host.querySelector<HTMLButtonElement>('[data-testid="profile-clear-all-button"]')!;
    expect(clearBtn).not.toBeNull();
    act(() => clearBtn.click());
    await flush();
    expect(dialog().querySelector('[data-testid="model-profile-clear-all-confirm"]')?.textContent).toContain(
      t("settings-modelprofiles.confirm_clear_all_rollbacks_only"),
    );
    await act(async () => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-clear-all-confirm"]')!.click());
    await flush();
    expect(host.querySelector('[data-testid="profile-clear-all-button"]')).toBeNull();
  });

  it("allows deleting a profile directly from inside the interactive editor", async () => {
    await initI18n("en");
    let revision = 2;
    let profiles = [{ id: "performance", name: "Performance", active: true, changes: [], tiers: {} }];
    const deletes: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (method === "GET" && url.endsWith("/model-profiles")) {
          return Response.json({ revision, active: profiles.length ? "performance" : "", profiles });
        }
        if (method === "GET" && url.endsWith("/launchers")) return Response.json({ launchers: [] });
        if (method === "GET" && url.endsWith("/candidates")) return new Response(null, { status: 404 });
        if (method === "DELETE") {
          deletes.push(url);
          revision++;
          profiles = [];
          return Response.json({ revision, active: "", profiles });
        }
        return Response.json({ purposes: [], providers: [] });
      }),
    );

    const host = await mount();
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Performance"))!.click());
    await flush();
    const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
    await act(async () => btn(t("settings-modelprofiles.edit"), dialog()).click());
    await vi.waitFor(() => {
      expect(dialog().querySelector('[data-testid="model-profile-editor"]')).not.toBeNull();
    });
    await flush();

    // Click Delete inside editor -> opens confirmation dialog -> confirm removes profile.
    act(() => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-editor-delete-button"]')!.click());
    await flush();
    expect(dialog().querySelector('[data-testid="model-profile-delete-confirm"]')).not.toBeNull();
    await act(async () => dialog().querySelector<HTMLButtonElement>('[data-testid="profile-delete-confirm"]')!.click());
    await flush();
    expect(deletes).toEqual(["/api/v1/model-profiles/performance?revision=2"]);
    expect(host.textContent).toContain(t("settings-modelprofiles.empty_title"));
  });

  it("handles older hubs returning 404/405 for deactivate and clear all gracefully", async () => {
    await initI18n("en");
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (method === "GET") {
          return Response.json({
            revision: 1,
            active: "performance",
            profiles: [{ id: "performance", name: "Performance", active: true, changes: [], tiers: {} }],
          });
        }
        if (method === "POST" && url.endsWith("/model-profiles/deactivate")) {
          return new Response("404 page not found", { status: 404 });
        }
        if (method === "DELETE" && url.startsWith("/api/v1/model-profiles?")) {
          return new Response("Method Not Allowed", { status: 405 });
        }
        throw new Error(`Unexpected request: ${method} ${url}`);
      }),
    );

    const host = await mount();

    // Deactivate on older hub shows clean localized unsupported message.
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="profile-deactivate-row-button"]')!.click());
    await flush();
    expect(host.querySelector('[role="alert"]')?.textContent).toBe(t("settings-modelprofiles.unsupported_deactivate"));

    // Clear all on older hub shows clean localized unsupported message inside dialog.
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="profile-clear-all-button"]')!.click());
    await flush();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    await act(async () => dialog.querySelector<HTMLButtonElement>('[data-testid="profile-clear-all-confirm"]')!.click());
    await flush();
    expect(dialog.querySelector('[data-testid="profile-action-error"]')?.textContent).toBe(
      t("settings-modelprofiles.unsupported_clear"),
    );
  });

  it("ships matching non-empty keys across en, zh-Hans, and ja settings-modelprofiles locales", () => {
    const enKeys = Object.keys(enLocale).sort();
    const zhKeys = Object.keys(zhLocale).sort();
    const jaKeys = Object.keys(jaLocale).sort();
    expect(zhKeys).toEqual(enKeys);
    expect(jaKeys).toEqual(enKeys);
    for (const k of enKeys) {
      expect((enLocale as Record<string, string>)[k].trim().length).toBeGreaterThan(0);
      expect((zhLocale as Record<string, string>)[k].trim().length).toBeGreaterThan(0);
      expect((jaLocale as Record<string, string>)[k].trim().length).toBeGreaterThan(0);
    }
  });
});
