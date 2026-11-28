// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import enCatalog from "../../i18n/locales/en/machines-storage.json";
import zhCatalog from "../../i18n/locales/zh-Hans/machines-storage.json";
import jaCatalog from "../../i18n/locales/ja/machines-storage.json";
import type { Machine } from "../../lib/types";
import { StoragePanel } from "./StoragePanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const GB = 1024 ** 3;
const MB = 1024 ** 2;

const json = (v: unknown, status = 200) =>
  new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
};

function selectComputer(mid: string) {
  const picker = host.querySelector("select")!;
  act(() => {
    picker.value = mid;
    picker.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

const click = (el: Element | null) => {
  if (!el) throw new Error("expected clickable element");
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
};

const machines: Machine[] = [
  { id: "devbox", name: "DevBox", online: true, os: "linux" },
  { id: "offbox", name: "OfflineBox", online: false, os: "linux" },
];

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("StoragePanel", () => {
  it("loads per-mount capacity and inode warnings without automatically scanning hotspots", async () => {
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        calls.push(url);
        if (url === "/api/v1/machines/devbox/storage") {
          return Promise.resolve(
            json({
              machine_id: "devbox",
              at: 1000,
              os: "linux",
              supported: true,
              collected_ms: 1,
              mounts: [
                {
                  mount: "/",
                  device: "/dev/vda2",
                  fs_type: "ext4",
                  used_bytes: 40 * GB,
                  avail_bytes: 60 * GB,
                  total_bytes: 100 * GB,
                  used_percent: 40,
                  inodes_total: 1000000,
                  inodes_used: 920000,
                  inodes_free: 80000,
                  inodes_percent: 92,
                },
                {
                  mount: "/data00",
                  device: "/dev/vdb",
                  fs_type: "ext4",
                  used_bytes: 450 * GB,
                  avail_bytes: 50 * GB,
                  total_bytes: 500 * GB,
                  used_percent: 90,
                  inodes_total: 2000000,
                  inodes_used: 200000,
                  inodes_free: 1800000,
                  inodes_percent: 10,
                },
              ],
            }),
          );
        }
        return Promise.resolve(json({ error: "unexpected" }, 500));
      }),
    );

    act(() => root.render(<StoragePanel machines={machines} />));
    await flush();

    expect(calls).toEqual(["/api/v1/machines/devbox/storage"]);
    expect(host.querySelector('[data-mount="/"]')).not.toBeNull();
    expect(host.querySelector('[data-mount="/data00"]')).not.toBeNull();
    expect(host.querySelectorAll('[data-testid="inode-warning"]')).toHaveLength(1);
    expect(host.querySelector('[data-testid="storage-hotspots-idle"]')).not.toBeNull();
  });

  it("runs on-demand hotspot scan and enforces preview before confirmed selective cleanup", async () => {
    const cleanupCalls: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/v1/machines/devbox/storage") {
          return Promise.resolve(
            json({
              machine_id: "devbox",
              at: 1000,
              os: "linux",
              supported: true,
              collected_ms: 1,
              mounts: [
                {
                  mount: "/",
                  device: "/dev/vda2",
                  fs_type: "ext4",
                  used_bytes: 50 * GB,
                  avail_bytes: 50 * GB,
                  total_bytes: 100 * GB,
                  used_percent: 50,
                  inodes_total: 1000000,
                  inodes_used: 100000,
                  inodes_free: 900000,
                  inodes_percent: 10,
                },
              ],
            }),
          );
        }
        if (url === "/api/v1/machines/devbox/storage/hotspots") {
          return Promise.resolve(
            json({
              machine_id: "devbox",
              at: 1001,
              os: "linux",
              partial: false,
              duration_ms: 42,
              categories: [
                {
                  id: "pip-cache",
                  path: "~/.cache/pip",
                  bytes: 500 * MB,
                  reclaimable_bytes: 500 * MB,
                  status: "ok",
                  cleanable: true,
                  cleanup_target_id: "pip-cache",
                },
                {
                  id: "persona-logs",
                  path: "~/.persona/logs",
                  bytes: 120 * MB,
                  reclaimable_bytes: 120 * MB,
                  status: "ok",
                  cleanable: true,
                  cleanup_target_id: "persona-logs",
                },
                {
                  id: "user-cache",
                  path: "~/.cache",
                  bytes: 2 * GB,
                  status: "ok",
                  cleanable: false,
                },
              ],
            }),
          );
        }
        if (url === "/api/v1/machines/devbox/storage/cleanup" && init?.method === "POST") {
          const body = JSON.parse(String(init.body)) as { targets: string[]; dry_run?: boolean; confirm?: boolean };
          cleanupCalls.push(body);
          if (body.dry_run) {
            return Promise.resolve(
              json({
                machine_id: "devbox",
                dry_run: true,
                estimated_bytes: 620 * MB,
                freed_bytes: 0,
                items: [
                  { target_id: "pip-cache", path: "~/.cache/pip", bytes: 500 * MB, status: "preview" },
                  { target_id: "persona-logs", path: "~/.persona/logs", bytes: 120 * MB, status: "preview" },
                ],
              }),
            );
          }
          return Promise.resolve(
            json({
              machine_id: "devbox",
              dry_run: false,
              estimated_bytes: 620 * MB,
              freed_bytes: 620 * MB,
              items: [
                { target_id: "pip-cache", path: "~/.cache/pip", bytes: 500 * MB, status: "cleaned" },
                { target_id: "persona-logs", path: "~/.persona/logs", bytes: 120 * MB, status: "cleaned" },
              ],
            }),
          );
        }
        return Promise.resolve(json({ error: "not found" }, 404));
      }),
    );

    act(() => root.render(<StoragePanel machines={machines} />));
    await flush();

    // Explicit hotspot scan trigger
    click(host.querySelector('[data-testid="storage-scan-hotspots"]'));
    await flush();

    expect(host.querySelector('[data-testid="storage-hotspots-list"]')).not.toBeNull();
    expect(host.querySelector('[data-target-checkbox="pip-cache"]')).not.toBeNull();
    expect(host.querySelector('[data-target-checkbox="persona-logs"]')).not.toBeNull();

    // Preview cleanup first (dry_run: true)
    click(host.querySelector('[data-testid="storage-preview-cleanup"]'));
    await flush();

    expect(cleanupCalls).toEqual([{ targets: ["pip-cache", "persona-logs"], dry_run: true }]);
    expect(document.body.querySelector('[data-testid="storage-preview-list"]')).not.toBeNull();

    // Confirm cleanup inside dialog (dry_run: false, confirm: true)
    click(document.body.querySelector('[data-testid="storage-confirm-cleanup"]'));
    await flush();

    expect(cleanupCalls).toEqual([
      { targets: ["pip-cache", "persona-logs"], dry_run: true },
      { targets: ["pip-cache", "persona-logs"], dry_run: false, confirm: true },
    ]);
    expect(host.querySelector('[data-testid="storage-cleanup-result"]')).not.toBeNull();
  });

  it("shows node_outdated notice when node does not implement machine.storage", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(json({ error: "old node", code: "node_outdated" }, 409))),
    );

    act(() => root.render(<StoragePanel machines={machines} />));
    await flush();

    expect(host.querySelector('[data-testid="storage-mounts-error"]')).not.toBeNull();
  });

  it("keeps en, zh-Hans, and ja machines-storage catalogs in sync", () => {
    const enKeys = Object.keys(enCatalog).sort();
    expect(Object.keys(zhCatalog).sort()).toEqual(enKeys);
    expect(Object.keys(jaCatalog).sort()).toEqual(enKeys);
  });

  it("aborts and ignores stale hotspot scan responses when switching machines", async () => {
    const twoOnline: Machine[] = [
      { id: "alpha", name: "AlphaBox", online: true, os: "linux" },
      { id: "beta", name: "BetaBox", online: true, os: "linux" },
    ];
    let resolveHotspots!: (res: Response) => void;
    let hotspotsSignal: AbortSignal | undefined;

    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/v1/machines/alpha/storage" || url === "/api/v1/machines/beta/storage") {
          const mid = url.includes("beta") ? "beta" : "alpha";
          return Promise.resolve(
            json({ machine_id: mid, at: 1000, os: "linux", supported: true, collected_ms: 1, mounts: [] }),
          );
        }
        if (url === "/api/v1/machines/alpha/storage/hotspots") {
          hotspotsSignal = init?.signal ?? undefined;
          return new Promise<Response>((resolve) => {
            resolveHotspots = resolve;
          });
        }
        return Promise.resolve(json({ error: "unexpected" }, 500));
      }),
    );

    act(() => root.render(<StoragePanel machines={twoOnline} />));
    await flush();

    // Start scan on alpha (in-flight)
    click(host.querySelector('[data-testid="storage-scan-hotspots"]'));
    await flush();
    expect(hotspotsSignal).toBeDefined();
    expect(hotspotsSignal?.aborted).toBe(false);

    // Switch to beta while alpha scan is still pending
    selectComputer("beta");
    await flush();
    expect(hotspotsSignal?.aborted).toBe(true);

    // Even if the stale alpha response resolves late, beta must remain clean/idle
    resolveHotspots(
      json({
        machine_id: "alpha",
        at: 1001,
        os: "linux",
        partial: false,
        duration_ms: 20,
        categories: [
          {
            id: "pip-cache",
            path: "~/.cache/pip",
            bytes: 500 * MB,
            reclaimable_bytes: 500 * MB,
            status: "ok",
            cleanable: true,
            cleanup_target_id: "pip-cache",
          },
        ],
      }),
    );
    await flush();

    expect(host.querySelector('[data-testid="storage-hotspots-idle"]')).not.toBeNull();
    expect(host.querySelector('[data-testid="storage-hotspots-list"]')).toBeNull();
    expect(host.querySelector('[data-target-checkbox="pip-cache"]')).toBeNull();
  });

  it("aborts and ignores stale cleanup preview responses when switching machines", async () => {
    const twoOnline: Machine[] = [
      { id: "alpha", name: "AlphaBox", online: true, os: "linux" },
      { id: "beta", name: "BetaBox", online: true, os: "linux" },
    ];
    let resolvePreview!: (res: Response) => void;
    let previewSignal: AbortSignal | undefined;

    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/v1/machines/alpha/storage" || url === "/api/v1/machines/beta/storage") {
          const mid = url.includes("beta") ? "beta" : "alpha";
          return Promise.resolve(
            json({ machine_id: mid, at: 1000, os: "linux", supported: true, collected_ms: 1, mounts: [] }),
          );
        }
        if (url === "/api/v1/machines/alpha/storage/hotspots") {
          return Promise.resolve(
            json({
              machine_id: "alpha",
              at: 1001,
              os: "linux",
              partial: false,
              duration_ms: 10,
              categories: [
                {
                  id: "pip-cache",
                  path: "~/.cache/pip",
                  bytes: 500 * MB,
                  reclaimable_bytes: 500 * MB,
                  status: "ok",
                  cleanable: true,
                  cleanup_target_id: "pip-cache",
                },
              ],
            }),
          );
        }
        if (url === "/api/v1/machines/alpha/storage/cleanup" && init?.method === "POST") {
          previewSignal = init.signal ?? undefined;
          return new Promise<Response>((resolve) => {
            resolvePreview = resolve;
          });
        }
        return Promise.resolve(json({ error: "unexpected" }, 500));
      }),
    );

    act(() => root.render(<StoragePanel machines={twoOnline} />));
    await flush();

    // Scan hotspots on alpha first
    click(host.querySelector('[data-testid="storage-scan-hotspots"]'));
    await flush();

    // Start preview on alpha (in-flight)
    click(host.querySelector('[data-testid="storage-preview-cleanup"]'));
    await flush();
    expect(previewSignal).toBeDefined();
    expect(previewSignal?.aborted).toBe(false);

    // Switch to beta while preview is still pending
    selectComputer("beta");
    await flush();
    expect(previewSignal?.aborted).toBe(true);

    // Late resolution of alpha preview must not open the confirm dialog on beta
    resolvePreview(
      json({
        machine_id: "alpha",
        dry_run: true,
        estimated_bytes: 500 * MB,
        freed_bytes: 0,
        items: [{ target_id: "pip-cache", path: "~/.cache/pip", bytes: 500 * MB, status: "preview" }],
      }),
    );
    await flush();

    expect(document.body.querySelector('[data-testid="storage-preview-list"]')).toBeNull();
    expect(host.querySelector('[data-testid="storage-hotspots-idle"]')).not.toBeNull();
  });

  it("clears previous machine mounts immediately on machine switch and when selected machine is offline", async () => {
    const mixed: Machine[] = [
      { id: "alpha", name: "AlphaBox", online: true, os: "linux" },
      { id: "beta", name: "BetaBox", online: true, os: "linux" },
      { id: "offbox", name: "OfflineBox", online: false, os: "linux" },
    ];
    let rejectBeta!: (err: unknown) => void;

    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/v1/machines/alpha/storage") {
          return Promise.resolve(
            json({
              machine_id: "alpha",
              at: 1000,
              os: "linux",
              supported: true,
              collected_ms: 1,
              mounts: [
                {
                  mount: "/alpha-disk",
                  device: "/dev/vda1",
                  fs_type: "ext4",
                  used_bytes: 20 * GB,
                  avail_bytes: 80 * GB,
                  total_bytes: 100 * GB,
                  used_percent: 20,
                },
              ],
            }),
          );
        }
        if (url === "/api/v1/machines/beta/storage") {
          return new Promise<Response>((_, reject) => {
            rejectBeta = reject;
          });
        }
        return Promise.resolve(json({ error: "unexpected" }, 500));
      }),
    );

    act(() => root.render(<StoragePanel machines={mixed} />));
    await flush();

    expect(host.querySelector('[data-mount="/alpha-disk"]')).not.toBeNull();

    // Switch to online beta while beta's fetch is still pending: alpha's mount must vanish immediately
    selectComputer("beta");
    await flush();
    expect(host.querySelector('[data-mount="/alpha-disk"]')).toBeNull();

    // Even if beta's fetch fails, alpha's mount must not reappear
    rejectBeta(new Error("boom"));
    await flush();
    expect(host.querySelector('[data-mount="/alpha-disk"]')).toBeNull();
    expect(host.querySelector('[data-testid="storage-mounts-error"]')).not.toBeNull();

    // Switch back to alpha, verify mount returns, then switch to offline machine
    selectComputer("alpha");
    await flush();
    expect(host.querySelector('[data-mount="/alpha-disk"]')).not.toBeNull();

    const alphaOffline: Machine[] = [
      { id: "alpha", name: "AlphaBox", online: false, os: "linux" },
      { id: "beta", name: "BetaBox", online: true, os: "linux" },
      { id: "offbox", name: "OfflineBox", online: false, os: "linux" },
    ];
    act(() => root.render(<StoragePanel machines={alphaOffline} />));
    await flush();
    expect(host.querySelector('[data-mount="/alpha-disk"]')).toBeNull();
    expect(host.textContent).toContain("BetaBox");
    expect(host.querySelector("select")).toBeNull();
    act(() => root.render(<StoragePanel machines={mixed.filter((m) => !m.online)} />));
    await flush();
    expect(host.querySelector('[data-testid="storage-mounts-error"]')).not.toBeNull();
  });

  it("freezes confirmed cleanup targets to the previewed set and invalidates the dialog on rescan or selection change", async () => {
    const cleanupCalls: Array<{ targets: string[]; dry_run?: boolean; confirm?: boolean }> = [];
    let scanCount = 0;

    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/v1/machines/devbox/storage") {
          return Promise.resolve(
            json({ machine_id: "devbox", at: 1000, os: "linux", supported: true, collected_ms: 1, mounts: [] }),
          );
        }
        if (url === "/api/v1/machines/devbox/storage/hotspots") {
          scanCount++;
          const categories =
            scanCount === 1
              ? [
                  {
                    id: "pip-cache",
                    path: "~/.cache/pip",
                    bytes: 300 * MB,
                    reclaimable_bytes: 300 * MB,
                    status: "ok",
                    cleanable: true,
                    cleanup_target_id: "pip-cache",
                  },
                ]
              : [
                  {
                    id: "pip-cache",
                    path: "~/.cache/pip",
                    bytes: 300 * MB,
                    reclaimable_bytes: 300 * MB,
                    status: "ok",
                    cleanable: true,
                    cleanup_target_id: "pip-cache",
                  },
                  {
                    id: "persona-backups",
                    path: "~/.persona/backups",
                    bytes: 900 * MB,
                    reclaimable_bytes: 900 * MB,
                    status: "ok",
                    cleanable: true,
                    cleanup_target_id: "persona-backups",
                  },
                ];
          return Promise.resolve(
            json({
              machine_id: "devbox",
              at: 1000 + scanCount,
              os: "linux",
              partial: false,
              duration_ms: 15,
              categories,
            }),
          );
        }
        if (url === "/api/v1/machines/devbox/storage/cleanup" && init?.method === "POST") {
          const body = JSON.parse(String(init.body)) as { targets: string[]; dry_run?: boolean; confirm?: boolean };
          cleanupCalls.push(body);
          return Promise.resolve(
            json({
              machine_id: "devbox",
              dry_run: Boolean(body.dry_run),
              estimated_bytes: 300 * MB,
              freed_bytes: body.dry_run ? 0 : 300 * MB,
              items: body.targets.map((t) => ({
                target_id: t,
                bytes: 300 * MB,
                status: body.dry_run ? "preview" : "cleaned",
              })),
            }),
          );
        }
        return Promise.resolve(json({ error: "unexpected" }, 500));
      }),
    );

    act(() => root.render(<StoragePanel machines={machines} />));
    await flush();

    // Scan 1 -> only pip-cache selected -> open dry-run preview dialog
    click(host.querySelector('[data-testid="storage-scan-hotspots"]'));
    await flush();
    click(host.querySelector('[data-testid="storage-preview-cleanup"]'));
    await flush();
    expect(document.body.querySelector('[data-testid="storage-preview-list"]')).not.toBeNull();
    expect(cleanupCalls).toEqual([{ targets: ["pip-cache"], dry_run: true }]);

    // Triggering "Scan again" while the confirm dialog is open must close/invalidate it
    click(host.querySelector('[data-testid="storage-scan-hotspots"]'));
    await flush();
    expect(document.body.querySelector('[data-testid="storage-preview-list"]')).toBeNull();
    expect(host.querySelector('[data-target-checkbox="persona-backups"]')).not.toBeNull();

    // Uncheck persona-backups, open preview for pip-cache only, then confirm -> only pip-cache is cleaned
    click(host.querySelector('[data-target-checkbox="persona-backups"]'));
    await flush();
    click(host.querySelector('[data-testid="storage-preview-cleanup"]'));
    await flush();
    expect(document.body.querySelector('[data-testid="storage-preview-list"]')).not.toBeNull();

    // Toggling a checkbox while dialog is open must also close/invalidate the dialog
    click(host.querySelector('[data-target-checkbox="persona-backups"]'));
    await flush();
    expect(document.body.querySelector('[data-testid="storage-preview-list"]')).toBeNull();

    // Re-uncheck persona-backups, preview pip-cache, and confirm
    click(host.querySelector('[data-target-checkbox="persona-backups"]'));
    await flush();
    click(host.querySelector('[data-testid="storage-preview-cleanup"]'));
    await flush();
    click(document.body.querySelector('[data-testid="storage-confirm-cleanup"]'));
    await flush();

    expect(cleanupCalls).toEqual([
      { targets: ["pip-cache"], dry_run: true },
      { targets: ["pip-cache"], dry_run: true },
      { targets: ["pip-cache"], dry_run: true },
      { targets: ["pip-cache"], dry_run: false, confirm: true },
    ]);
  });
});
