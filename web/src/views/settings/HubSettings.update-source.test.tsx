// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import type { HubUpdateStatus } from "../../lib/hub-update";

let current: HubUpdateStatus;
let checked: HubUpdateStatus | Promise<HubUpdateStatus> | undefined;
let onCheck: ((opts?: { json?: { preview?: boolean } }) => HubUpdateStatus | Promise<HubUpdateStatus>) | undefined;
let onSettings: ((opts?: { json?: { source?: HubUpdateStatus["source"] } }) => HubUpdateStatus | Promise<HubUpdateStatus>) | undefined;

const updateApi = vi.fn(async (path: string, opts?: { json?: { source?: HubUpdateStatus["source"]; preview?: boolean } }) => {
  switch (path) {
    case "/hub/update": return current;
    case "/hub/update/check": return onCheck ? onCheck(opts) : checked;
    case "/hub/update/backups": return { backups: [] };
    case "/hub/update/history": return { history: [] };
    case "/hub/update/settings":
      return onSettings ? onSettings(opts) : { ...current, source: opts?.json?.source, last_check: undefined, latest: undefined };
    default: return {};
  }
});
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (...args: Parameters<typeof updateApi>) => updateApi(...args),
}));
await initI18n("en");
const { HubSettings } = await import("./HubSettings");

let root: Root;
let host: HTMLDivElement;

const released = "2026-11-20T15:04:05Z";
const latest = { version: "v1.0.0", build: 202611201504, released, newer: false, bad: false };
const status = (o: Partial<HubUpdateStatus>): HubUpdateStatus => ({
  version: "web-dev-aaaaaaaaaaaa-202611251445", build: 202611251445, channel: "release", source: "release",
  can_update: true, state: "idle", busy: [], development: { configured: true, branch: "main" },
  auto: true, auto_notice: false, confirmed: true, update_url: "https://updates.example.com/dl", update_url_from: "build",
  last_check: "2026-11-25T15:00:00Z", latest, ...o,
});

const updateCard = () => host.querySelector('[data-settings-group="hub-update"]')!;
const versionCard = () => host.querySelector('[data-settings-group="hub-version"]')!;
const button = (label: string) => [...host.querySelectorAll("button")].find(b => b.textContent === label);

const mount = async (s: HubUpdateStatus) => {
  current = s;
  await act(async () => root.render(<HubSettings />));
};

beforeEach(async () => {
  await initI18n("en");
  checked = undefined;
  onCheck = undefined;
  onSettings = undefined;
  updateApi.mockClear();
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("Settings › Hub update source", () => {
  it("keeps the cached version visible during the opening preview and starts the latest target", async () => {
    let finish!: (s: HubUpdateStatus) => void;
    checked = new Promise(resolve => { finish = resolve; });
    const offer = status({ supports_preview: true, source: "web-dev", latest: { ...latest, commit: "b".repeat(40), newer: true } });
    await mount(offer);
    expect(host.textContent).toContain("bbbbbbbbbbbb");
    expect(button("Checking…")).toBeDefined();
    expect(updateApi).toHaveBeenCalledWith("/hub/update/check", { method: "POST", json: { preview: true } });
    await act(async () => finish({ ...offer, latest: { ...offer.latest!, commit: "c".repeat(40) } }));
    expect(host.textContent).toContain("cccccccccccc");
    current = { ...offer, state: "resolving" };
    await act(async () => button("Update hub")!.click());
    expect(updateApi).toHaveBeenCalledWith("/hub/update", { method: "POST", json: { target: "latest" } });
    expect(host.textContent).toContain("Checking the latest version before updating");
    expect(host.querySelector('[aria-label="Update pipeline"]')).toBeNull();
    expect(updateApi.mock.calls.filter(([path]) => path === "/hub/update/check")).toHaveLength(1);
  });

  it.each([
    ["web-dev", "available", false], ["web-dev", "building", true], ["web-dev", "resolving", true],
    ["release", "available", true], ["beta", "available", true],
  ] as const)("skips opening preview for %s / %s / support %s", async (source, state, supports_preview) => {
    await mount(status({ source, state, supports_preview }));
    expect(updateApi.mock.calls.some(([path]) => path === "/hub/update/check")).toBe(false);
  });

  it.each(["release", "beta"] as const)("shows structured ahead guidance and target version after checking from a newer web-dev build (%s)", async source => {
    const version = source === "beta" ? "v1.1.0-beta.1" : latest.version;
    await mount(status({ source, last_check: undefined, latest: undefined }));
    checked = status({ source, latest: { ...latest, version } });
    await act(async () => button("Check for updates")!.click());

    expect(updateApi).toHaveBeenCalledWith("/hub/update/check", { method: "POST" });
    expect(host.querySelector('[data-settings-row="hub-channel"]')?.textContent).toContain("Web development");
    expect(updateCard().textContent).toContain(version);
    expect(updateCard().querySelector("time")?.getAttribute("datetime")).toBe(released);
    expect(updateCard().querySelector('[data-update-guidance="neutral"]')).not.toBeNull();
    expect(updateCard().textContent).toContain("Running a newer Web development build");
    expect(updateCard().textContent).toContain("Downgrades aren't supported to protect database and state compatibility.");
    expect(updateCard().textContent).toContain("Keep this source selected to update automatically or manually once a newer official release is published.");
    expect(updateCard().textContent).not.toContain("Up to date");
    expect(button("Update hub")).toBeUndefined();
    expect(button("Check for updates")?.disabled).toBe(false);
  });

  it("auto-checks read-only after switching update source and displays the target channel status immediately", async () => {
    let finishCheck!: (s: HubUpdateStatus) => void;
    checked = new Promise(resolve => { finishCheck = resolve; });
    await mount(status({ version: "web-dev-aaaaaaaaaaaa-202611251445", source: "web-dev", supports_preview: false }));

    const picker = host.querySelector<HTMLSelectElement>('select[aria-label="Update source"]')!;
    await act(async () => { picker.value = "beta"; picker.dispatchEvent(new Event("change", { bubbles: true })); });

    expect(updateApi).toHaveBeenCalledWith("/hub/update/settings", { method: "PUT", json: { source: "beta" } });
    expect(updateApi).toHaveBeenCalledWith("/hub/update/check", { method: "POST", json: { preview: true } });
    expect(button("Checking…")).toBeDefined();
    expect(updateCard().textContent).toContain("Checking…");
    expect(updateCard().textContent).not.toContain("Up to date");
    expect(picker.disabled).toBe(false);

    await act(async () => finishCheck(status({
      source: "beta",
      latest: { ...latest, version: "v1.2.0-beta.1" },
    })));
    expect(updateCard().textContent).toContain("v1.2.0-beta.1");
    expect(updateCard().querySelector("time")?.getAttribute("datetime")).toBe(released);
    expect(updateCard().textContent).toContain("Running a newer Web development build");
    expect(button("Check for updates")?.disabled).toBe(false);
  });

  it("ignores stale out-of-order check results and errors when switching source again mid-check", async () => {
    let resolveBeta!: (s: HubUpdateStatus) => void;
    let resolveRelease!: (s: HubUpdateStatus) => void;
    let checkCalls = 0;
    onCheck = () => new Promise<HubUpdateStatus>((resolve) => {
      checkCalls++;
      if (checkCalls === 1) resolveBeta = resolve;
      else resolveRelease = resolve;
    });
    await mount(status({ version: latest.version, build: latest.build, source: "release" }));

    const picker = host.querySelector<HTMLSelectElement>('select[aria-label="Update source"]')!;
    await act(async () => { picker.value = "beta"; picker.dispatchEvent(new Event("change", { bubbles: true })); });
    expect(checkCalls).toBe(1);
    expect(button("Checking…")).toBeDefined();

    // Switch back to release while the beta auto-check is still in flight.
    await act(async () => { picker.value = "release"; picker.dispatchEvent(new Event("change", { bubbles: true })); });
    expect(checkCalls).toBe(2);
    expect(button("Checking…")).toBeDefined();

    // Stale beta check resolves first and must be ignored without clearing checking state.
    await act(async () => resolveBeta(status({
      version: latest.version, build: latest.build, source: "beta",
      latest: { ...latest, version: "v9.9.9-beta.1", build: 202612010000, newer: true },
    })));
    expect(updateCard().textContent).not.toContain("v9.9.9-beta.1");
    expect(button("Checking…")).toBeDefined();

    // Current release check resolves and updates the card.
    await act(async () => resolveRelease(status({
      version: latest.version, build: latest.build, source: "release",
      latest: { ...latest, version: "v1.0.0", build: latest.build, newer: false },
    })));
    expect(updateCard().textContent).toContain("v1.0.0");
    expect(updateCard().textContent).toContain("Up to date");
    expect(button("Check for updates")?.disabled).toBe(false);
  });

  it("keeps Up to date beside the matching release and its publication time", async () => {
    await mount(status({ version: latest.version, build: latest.build, latest: { ...latest, notes: "Release notes" } }));
    const card = updateCard();
    const date = new Intl.DateTimeFormat("en", { dateStyle: "medium", timeStyle: "short" }).format(new Date(released));
    expect(card.textContent).toContain("Up to date");
    expect(card.textContent).toContain(latest.version);
    expect(card.querySelector("time")?.textContent).toBe(`Released ${date}`);
    expect(card.textContent).not.toContain("Release notes");
    expect(card.textContent).not.toContain("update.url");
  });

  it("offers a newer release with its notes and publication time", async () => {
    await mount(status({ build: 202611191504, latest: { ...latest, newer: true, notes: "Release notes" } }));
    const card = updateCard();
    expect(card.textContent).toContain(`New version ${latest.version}`);
    expect(card.querySelector("time")?.getAttribute("datetime")).toBe(released);
    expect(card.textContent).toContain("Release notes");
    expect(button("Update hub")?.disabled).toBe(false);
  });

  it("shows the stopped source callout in place of Up to date", async () => {
    await mount(status({ version: "v1.1.0", source_behind: true }));
    const text = updateCard().textContent;
    expect(updateCard().querySelector('[data-update-guidance="caution"]')).not.toBeNull();
    expect(text).toContain("Update source may have stopped");
    expect(text).toContain("https://updates.example.com/dl");
    expect(text).toContain("update.url");
    expect(text).toContain(latest.version);
    expect(updateCard().querySelector("time")?.getAttribute("datetime")).toBe(released);
    expect(text).not.toContain("Downgrades aren't supported");
    expect(text).not.toContain("Up to date");
  });

  it.each([
    {
      label: "private_no_source",
      override: {
        can_update: false,
        why_not: "Private network mode is on. Set update.url in hub.json to an internal release URL and restart the hub.",
        latest: undefined,
        last_check: undefined,
      },
      expectedNextStep: "switch back to an available update source above",
    },
    {
      label: "no_source without dev",
      override: {
        can_update: false,
        development: undefined,
        why_not: "No release update source is configured. Set update.url in hub.json and restart the hub.",
        latest: undefined,
        last_check: undefined,
      },
      expectedNextStep: "restart the hub to enable update checks",
    },
    {
      label: "bad_source",
      override: {
        can_update: false,
        development: undefined,
        why_not: "hub.json’s update.url can’t be used: only https:// and file:// addresses work",
        latest: undefined,
        last_check: undefined,
      },
      expectedNextStep: "configuring a valid update.url in hub.json",
    },
    {
      label: "dev build",
      override: {
        channel: "dev",
        can_update: false,
        development: undefined,
        why_not: "Development build (dev)",
        latest: undefined,
        last_check: undefined,
      },
      expectedNextStep: "Install an official release build on the hub machine",
    },
  ])("shows structured cannot-update guidance and disables Check for $label", async ({ override, expectedNextStep }) => {
    await mount(status(override));
    const card = updateCard();
    expect(card.querySelector('[data-update-guidance="caution"]')).not.toBeNull();
    expect(card.textContent).toContain("Updates aren't available for this source");
    expect(card.textContent).toContain(override.why_not);
    expect(card.textContent).toContain(expectedNextStep);
    expect(versionCard().textContent).toContain("Checking isn't available for this source.");
    const checkBtn = button("Check for updates")!;
    expect(checkBtn.disabled).toBe(true);
    await act(async () => checkBtn.click());
    expect(updateApi.mock.calls.some(([path]) => path === "/hub/update/check")).toBe(false);
  });

  it("does not auto-check when switching to a source whose status cannot check", async () => {
    await mount(status({ version: "web-dev-aaaaaaaaaaaa-202611251445", source: "web-dev", supports_preview: false }));
    onSettings = () => status({
      source: "release",
      can_update: false,
      why_not: "Private network mode is on. Set update.url in hub.json to an internal release URL and restart the hub.",
      latest: undefined,
      last_check: undefined,
    });
    const picker = host.querySelector<HTMLSelectElement>('select[aria-label="Update source"]')!;
    await act(async () => { picker.value = "release"; picker.dispatchEvent(new Event("change", { bubbles: true })); });

    expect(updateApi).toHaveBeenCalledWith("/hub/update/settings", { method: "PUT", json: { source: "release" } });
    expect(updateApi.mock.calls.some(([path]) => path === "/hub/update/check")).toBe(false);
    expect(updateCard().textContent).toContain("Private network mode is on.");
    expect(button("Check for updates")?.disabled).toBe(true);
  });

  it("keeps Check enabled on Docker hubs even though self-update is unavailable", async () => {
    await mount(status({ channel: "docker", can_update: false, development: undefined, latest: undefined, last_check: undefined }));
    expect(versionCard().textContent).not.toContain("Checking isn't available for this source.");
    expect(button("Check for updates")?.disabled).toBe(false);
  });

  it.each([undefined, "0001-01-01T00:00:00Z"])("shows the version without a publication time when released is %s", async value => {
    await mount(status({ latest: { ...latest, released: value } }));
    expect(updateCard().textContent).toContain(latest.version);
    expect(updateCard().querySelector("time")).toBeNull();
  });

  it("keeps the stopped-source warning beside a failed-check error", async () => {
    await mount(status({ version: "v1.1.0", source_behind: true, error: "Fixture check failed" }));
    const text = updateCard().textContent;
    expect(text).toContain("update.url");
    expect(text).toContain("Fixture check failed");
    expect(text).not.toContain("Up to date");
  });

  it.each([
    { lang: "en", aheadTitle: "Running a newer Web development build", cannotTitle: "Updates aren't available for this source", checkHint: "Checking isn't available for this source." },
    { lang: "zh-Hans", aheadTitle: "当前 Web 开发版高于此更新来源", cannotTitle: "当前来源暂无法更新", checkHint: "当前更新来源暂不支持检查更新。" },
    { lang: "ja", aheadTitle: "現在の Web開発版 はこの更新元より新しいバージョンです", cannotTitle: "この更新元からはアップデートできません", checkHint: "この更新元ではアップデートを確認できません。" },
  ])("renders localized ahead and cannot guidance in $lang", async ({ lang, aheadTitle, cannotTitle, checkHint }) => {
    await initI18n(lang);
    await mount(status({ source: "release" }));
    expect(updateCard().textContent).toContain(aheadTitle);

    await act(async () => root.unmount());
    root = createRoot(host);
    await mount(status({ can_update: false, why_not: "server reason", latest: undefined, last_check: undefined }));
    expect(updateCard().textContent).toContain(cannotTitle);
    expect(updateCard().textContent).toContain("server reason");
    expect(versionCard().textContent).toContain(checkHint);
  });
});
