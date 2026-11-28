// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { bytesBin, percent } from "../../lib/machine-detail";
import { whatIf, type OffloadInventory, type OffloadProvider, type UsageNode, type UsageReport } from "../../lib/usage-map";
import type { Machine } from "../../lib/types";
import { UsageMap } from "./UsageMap";
import { localMenu } from "./UsageMapMenu";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const GB = 1024 ** 3;
const NOW_S = Math.floor(Date.now() / 1000);
const DAYS = 86_400;

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

let host: HTMLDivElement;
let root: Root;

const flush = async (n = 8) => {
  for (let i = 0; i < n; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 2));
    });
  }
};

const click = (el: Element | null | undefined) => {
  if (!el) throw new Error("expected an element to click");
  act(() => void el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
};
const key = (el: Element | null | undefined, k: string, init: KeyboardEventInit = {}) => {
  if (!el) throw new Error("expected an element for the key");
  act(() => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...init })));
};
const contextMenu = (el: Element | null | undefined) => {
  if (!el) throw new Error("expected an element for the menu");
  act(() => void el.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
};
const box = (id: string) => [...document.querySelectorAll<HTMLElement>("[data-box]")].find((b) => b.dataset.box === id);
const testId = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`);
const menuItem = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) => b.textContent?.includes(label));

const mac: Machine = { id: "mac", name: "Studio", online: true, os: "darwin" };
const provider: OffloadProvider = { extension: "example", id: "cloud", name: "Example Cloud", kinds: ["file", "dir", "app"], restore: true };

const movies = (): UsageNode => ({
  id: "/Users/me/Movies",
  name: "Movies",
  path: "/Users/me/Movies",
  display: "~/Movies",
  kind: "dir",
  category: "personal",
  bytes: 60 * GB,
  used_at: NOW_S - 400 * DAYS,
  has_children: true,
  can_trash: true,
  can_offload: true,
  children: [
    { id: "/Users/me/Movies/old.mov", name: "old.mov", path: "/Users/me/Movies/old.mov", display: "~/Movies/old.mov", kind: "file", category: "personal", bytes: 50 * GB, used_at: NOW_S - 400 * DAYS, has_children: false, can_trash: true, can_offload: true },
    { id: "/Users/me/Movies/new.mov", name: "new.mov", path: "/Users/me/Movies/new.mov", display: "~/Movies/new.mov", kind: "file", category: "personal", bytes: 10 * GB, used_at: NOW_S - 2 * DAYS, has_children: false, can_trash: true, can_offload: true },
  ],
});

const overview = (state: "scanning" | "ready" = "ready"): UsageReport => ({
  machine_id: "mac",
  at: NOW_S,
  os: "darwin",
  path: "",
  state,
  partial: false,
  cached: false,
  home: "/Users/me",
  volume: { mount: "/", total_bytes: 245 * GB, used_bytes: 221 * GB, free_bytes: 24 * GB },
  node: {
    id: "group:root",
    name: "",
    kind: "root",
    category: "other",
    bytes: 245 * GB,
    has_children: true,
    children: [
      {
        id: "group:home",
        name: "",
        key: "home",
        kind: "group",
        category: "personal",
        bytes: 120 * GB,
        has_children: true,
        children: [
          movies(),
          { id: "/Users/me/Library/Caches/go-build", name: "go-build", path: "/Users/me/Library/Caches/go-build", display: "~/Library/Caches/go-build", kind: "dir", category: "cache", bytes: 6 * GB, has_children: true, can_trash: true, cache_id: "go-build", cache_in_use: false },
          { id: "/Users/me/Documents", name: "Documents", path: "/Users/me/Documents", display: "~/Documents", kind: "dir", category: "personal", bytes: 54 * GB, has_children: true, can_trash: false, can_offload: false, refusal: "standard_folder" },
        ],
      },
      ...(state === "ready"
        ? [
            {
              id: "group:apps",
              name: "",
              key: "apps",
              kind: "group",
              category: "apps",
              bytes: 101 * GB,
              has_children: true,
              children: [{ id: "/Applications/Example.app", name: "Example", path: "/Applications/Example.app", kind: "app", category: "apps", bytes: 101 * GB, has_children: true, can_trash: false, can_offload: true, refusal: "app_bundle" }],
            } as UsageNode,
          ]
        : []),
      { id: "group:free", name: "", key: "free", kind: "free", category: "free", bytes: 24 * GB, has_children: false },
    ],
  },
});

const inventory: OffloadInventory = {
  state: "ready",
  at: NOW_S,
  total_bytes: 30 * GB,
  items: [{ id: "i1", name: "Trip", kind: "dir", bytes: 30 * GB, origin_path: "/Users/me/Movies/Trip", restorable: true, offloaded_at: NOW_S - 30 * DAYS }],
};

type Call = { method: string; url: string; body?: Record<string, unknown> };
type Handler = (c: Call) => Response | undefined;

function hub(handlers: Handler[]) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      const c = { method, url, body };
      calls.push(c);
      for (const h of handlers) {
        const r = h(c);
        if (r) return Promise.resolve(r);
      }
      return Promise.resolve(new Response("not found", { status: 404 }));
    }),
  );
  return calls;
}

const usage = (reports: UsageReport[]): Handler => {
  const queue = [...reports];
  return (c) => (c.method === "GET" && /^\/api\/v1\/machines\/mac\/storage\/usage\?path=(&|$)/.test(c.url) ? json(queue.length > 1 ? queue.shift() : queue[0]) : undefined);
};
const providers = (list: OffloadProvider[] | null): Handler => (c) => (c.url.startsWith("/api/v1/extensions/-/offload") ? (list ? json({ providers: list }) : new Response("not found", { status: 404 })) : undefined);
const inv: Handler = (c) => (c.url.startsWith("/api/v1/extensions/example/-/offload/cloud/inventory") ? json(inventory) : undefined);
const folder: Handler = (c) => (c.url.startsWith("/api/v1/machines/mac/storage/usage?path=%2F") ? json({ ...overview(), path: "/Users/me/Movies", node: movies() }) : undefined);

function render(el: React.ReactElement) {
  act(() => root.render(el));
}

async function mapReady(handlers: Handler[], focus?: { mode: "reclaim"; seq: number }) {
  const calls = hub(handlers);
  render(<UsageMap machine={mac} pollMs={1} focus={focus} />);
  if (!focus) click(testId("usage-start"));
  await flush();
  return calls;
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

describe("UsageMap", () => {
  it("asks for nothing until asked, then polls while the computer scans and fills in", async () => {
    const calls = hub([usage([overview("scanning"), overview("scanning"), overview("ready")]), providers([]), folder]);
    render(<UsageMap machine={mac} pollMs={1} />);
    await flush(2);
    expect(calls).toEqual([]);
    click(testId("usage-start"));
    await flush();
    const asked = calls.filter((c) => /\/storage\/usage\?path=(&|$)/.test(c.url));
    expect(asked.length).toBe(3);
    expect(box("group:apps")).toBeTruthy();
    expect(box("group:free")).toBeTruthy();
  });

  it("zooms into a box, names the trail, and goes up one level on Escape", async () => {
    await mapReady([usage([overview()]), providers([]), folder]);
    click(box("group:home"));
    await flush();
    const crumbs = testId("usage-crumbs")!;
    expect(crumbs.textContent).toContain(t("machines-usagemap.kind_home"));
    expect(box("/Users/me/Movies")).toBeTruthy();
    expect(box("group:apps")).toBeUndefined();
    key(box("/Users/me/Movies"), "Escape");
    await flush(2);
    expect(box("group:apps")).toBeTruthy();
    expect(testId("usage-crumbs")!.textContent).not.toContain(t("machines-usagemap.kind_home"));
  });

  it("opens a folder not measured yet and fills it in", async () => {
    const calls = await mapReady([usage([overview()]), providers([]), folder]);
    click(box("group:home"));
    click(box("/Users/me/Movies"));
    await flush();
    expect(calls.some((c) => c.url.includes("path=%2FUsers%2Fme%2FMovies"))).toBe(true);
    expect(box("/Users/me/Movies/old.mov")).toBeTruthy();
  });

  it("moves between boxes with the arrow keys, one tab stop at a time", async () => {
    await mapReady([usage([overview()]), providers([]), folder]);
    const tabbable = [...document.querySelectorAll<HTMLElement>('[data-usage-side="local"] [data-box]')].filter((b) => b.tabIndex === 0);
    expect(tabbable.length).toBe(1);
    const first = tabbable[0];
    act(() => first.focus());
    key(first, "ArrowRight");
    expect(document.activeElement).not.toBe(first);
    expect((document.activeElement as HTMLElement).dataset.box).toBeTruthy();
  });

  it("selects with Space and shows the what-if numbers in the tray", async () => {
    await mapReady([usage([overview()]), providers([]), folder]);
    const freeBefore = parseFloat(box("group:free")!.style.width);
    key(box("/Users/me/Movies"), " ");
    // the free strip grows by what the selection would return
    expect(parseFloat(box("group:free")!.style.width)).toBeGreaterThan(freeBefore);
    const w = whatIf(overview().volume, [movies()]);
    expect(testId("usage-whatif")!.textContent).toBe(
      t("machines-usagemap.tray_free_after", { free: bytesBin(w.freeAfter), gain: bytesBin(w.freed), before: percent(w.pctBefore), after: percent(w.pctAfter) }),
    );
    expect(testId("usage-selection-rows")!.textContent).toContain("Movies");
    expect(box("/Users/me/Movies")!.dataset.ghost).toBeDefined();
    click(testId("usage-tray-clear"));
    expect(testId("usage-selection")).toBeNull();
  });

  it("moves to the trash, puts it back, then deletes permanently with its own confirmation", async () => {
    const trashResult = { dry_run: false, freed_bytes: 0, pending_bytes: 60 * GB, items: [{ path: "/Users/me/Movies", display: "~/Movies", bytes: 60 * GB, status: "trashed", trash_id: "t1" }] };
    // the node takes a trashed item out of its cached answers itself
    let gone = false;
    const without = (): UsageReport => {
      const r = overview();
      const home = r.node.children![0];
      home.children = home.children!.filter((c) => c.id !== "/Users/me/Movies");
      home.bytes -= 60 * GB;
      return r;
    };
    const calls = await mapReady([
      (c) => (/\/storage\/usage\?path=(&|$)/.test(c.url) ? json(gone ? without() : overview()) : undefined),
      providers([]),
      folder,
      (c) => {
        if (c.method !== "POST" || c.url !== "/api/v1/machines/mac/storage/trash") return undefined;
        gone = true;
        return json(trashResult);
      },
      (c) => {
        if (c.method !== "POST" || c.url !== "/api/v1/machines/mac/storage/trash/undo") return undefined;
        gone = false;
        return json({ restored: [{ path: "/Users/me/Movies" }] });
      },
      (c) => (c.method === "POST" && c.url === "/api/v1/machines/mac/storage/trash/purge" ? json({ freed_bytes: 60 * GB, available_after: 84 * GB, items: [{ trash_id: "t1", status: "purged" }] }) : undefined),
    ]);
    key(box("/Users/me/Movies"), "Delete");
    expect(testId("usage-trash-list")!.textContent).toContain("~/Movies");
    expect(document.body.textContent).toContain(t("machines-usagemap.trash_confirm_note", { size: bytesBin(60 * GB) }));
    click(testId("usage-trash-confirm"));
    await flush();
    expect(calls.find((c) => c.url.endsWith("/storage/trash"))!.body).toEqual({ paths: ["/Users/me/Movies"], confirm: true });
    expect(testId("usage-notice")!.textContent).toContain(t("machines-usagemap.trash_pending", { size: bytesBin(60 * GB) }));
    expect(box("/Users/me/Movies")).toBeUndefined();

    click(testId("usage-undo"));
    await flush();
    expect(calls.find((c) => c.url.endsWith("/trash/undo"))!.body).toEqual({ trash_ids: ["t1"] });
    expect(testId("usage-notice")!.textContent).toContain(t("machines-usagemap.trash_undone", { count: 1 }));

    key(box("/Users/me/Movies"), "Delete");
    click(testId("usage-trash-confirm"));
    await flush();
    click(testId("usage-purge"));
    expect(document.body.textContent).toContain(t("machines-usagemap.trash_purge_title"));
    click(testId("usage-purge-confirm"));
    await flush();
    expect(calls.find((c) => c.url.endsWith("/trash/purge"))!.body).toEqual({ trash_ids: ["t1"], confirm: true });
    expect(testId("usage-notice")!.textContent).toContain(t("machines-usagemap.trash_purged", { size: bytesBin(60 * GB) }));
  });

  it("offers Clean cache, not Trash, for a catalog cache, and cleans it with the existing call", async () => {
    const calls = await mapReady([usage([overview()]), providers([]), folder, (c) => (c.url === "/api/v1/machines/mac/cleanup" ? json({ freed_bytes: 6 * GB, items: [{ id: "go-build", bytes: 6 * GB, status: "in_use" }] }) : undefined)]);
    contextMenu(box("/Users/me/Library/Caches/go-build"));
    expect(menuItem(t("machines-usagemap.menu_trash"))).toBeUndefined();
    click(menuItem(t("machines-usagemap.menu_clean")));
    expect(document.body.textContent).toContain(t("machines-alerts.clean_confirm_title"));
    expect(calls.filter(c => c.url.endsWith("/cleanup"))).toHaveLength(0);
    click(testId("usage-clean-confirm"));
    await flush();
    expect(testId("usage-notice")!.textContent).toContain(t("machines-alerts.status_in_use"));
    expect(calls.find((c) => c.url.endsWith("/cleanup"))!.body).toEqual({ ids: ["go-build"] });
  });

  it("offloads after one confirmation when the tool is there", async () => {
    const calls = await mapReady([
      usage([overview()]),
      providers([provider]),
      inv,
      folder,
      (c) => (c.method === "POST" && c.url.endsWith("/offload/cloud/run") ? json({ job: "j1" }, 202) : undefined),
      (c) => (c.url.startsWith("/api/v1/extensions/example/-/offload/cloud/jobs/j1") ? json({ state: "done", done_bytes: 60 * GB, total_bytes: 60 * GB, freed_bytes: 60 * GB, items: [] }) : undefined),
    ]);
    key(box("/Users/me/Movies"), " ");
    click(testId("usage-tray-offload"));
    expect(document.body.textContent).toContain(t("machines-usagemap.offload_confirm_title", { provider: "Example Cloud" }));
    click(testId("usage-offload-confirm"));
    await flush();
    const runs = calls.filter((c) => c.url.endsWith("/run"));
    expect(runs.map((r) => r.body)).toEqual([{ machine: "mac", action: "offload", paths: ["/Users/me/Movies"], confirm: true }]);
    expect(testId("usage-job")!.dataset.state).toBe("done");
    expect(testId("usage-job")!.textContent).toContain(t("machines-usagemap.offload_job_done", { size: bytesBin(60 * GB) }));
  });

  it("asks a second time to install the tool, naming it, the computer and where it comes from", async () => {
    let installed = false;
    const calls = await mapReady([
      usage([overview()]),
      providers([provider]),
      inv,
      folder,
      (c) => {
        if (c.method !== "POST" || !c.url.endsWith("/offload/cloud/run")) return undefined;
        if (c.body?.install) {
          installed = true;
          return json({ job: "j2" }, 202);
        }
        return json({ error: "needs its tool", code: "tool_install_required", tool: { name: "example-cli", title: "Example CLI", installed: false, source: "Downloaded from the provider, installed in ~/.local/bin." } }, 409);
      },
      (c) => (c.url.includes("/jobs/j2") ? json({ state: "running", phase: "install", done_bytes: 0, total_bytes: 60 * GB, items: [] }) : undefined),
    ]);
    contextMenu(box("/Users/me/Movies"));
    click(menuItem(t("machines-usagemap.menu_offload", { provider: "Example Cloud" })));
    click(testId("usage-offload-confirm"));
    await flush();
    expect(installed).toBe(false);
    expect(document.body.textContent).toContain(t("machines-usagemap.offload_install_confirm_title", { tool: "Example CLI" }));
    expect(document.body.textContent).toContain(t("machines-usagemap.offload_install_confirm_body", { provider: "Example Cloud", tool: "Example CLI", machine: "Studio" }));
    expect(testId("usage-install-source")!.textContent).toBe("Downloaded from the provider, installed in ~/.local/bin.");
    click(testId("usage-install-confirm"));
    await flush();
    const runs = calls.filter((c) => c.url.endsWith("/run")).map((r) => r.body);
    expect(runs[1]).toEqual({ machine: "mac", action: "offload", paths: ["/Users/me/Movies"], install: true, confirm: true });
    expect(testId("usage-job")!.textContent).toContain(t("machines-usagemap.offload_phase_install"));
  });

  it("draws what is offloaded on the other side, links it to its origin and restores it", async () => {
    const calls = await mapReady([
      usage([overview()]),
      providers([provider]),
      inv,
      folder,
      (c) => (c.method === "POST" && c.url.endsWith("/offload/cloud/run") ? json({ job: "r1" }, 202) : undefined),
      (c) => (c.url.includes("/jobs/r1") ? json({ state: "done", done_bytes: 30 * GB, total_bytes: 30 * GB, items: [] }) : undefined),
    ]);
    const cloudBox = [...document.querySelectorAll<HTMLElement>('[data-usage-side="cloud"] [data-box]')].find((b) => b.dataset.box?.endsWith("i1"));
    expect(cloudBox).toBeTruthy();
    // the badge on the folder the item came from
    expect(box("/Users/me/Movies")!.getAttribute("aria-label")).toContain(t("machines-usagemap.cloud_badge", { count: 1 }));
    act(() => void cloudBox!.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    act(() => cloudBox!.focus());
    expect(testId("usage-ribbons")).toBeTruthy();
    contextMenu(cloudBox);
    click(menuItem(t("machines-usagemap.menu_restore")));
    expect(testId("usage-restore-list")!.textContent).toContain("Trip");
    click(testId("usage-restore-confirm"));
    await flush();
    expect(calls.find((c) => c.url.endsWith("/run"))!.body).toEqual({ machine: "mac", action: "restore", items: ["i1"], confirm: true });
    expect(testId("usage-job")!.textContent).toContain(t("machines-usagemap.offload_job_restored"));
  });

  it("shows the quiet empty state and no Offload without a provider", async () => {
    await mapReady([usage([overview()]), providers(null), folder]);
    expect(testId("usage-cloud-empty")!.textContent).toContain(t("machines-usagemap.cloud_empty"));
    expect(testId("usage-cloud-settings")).toBeTruthy();
    contextMenu(box("/Users/me/Movies"));
    expect(document.querySelectorAll('[role="menuitem"]').length).toBeGreaterThan(0);
    const offloadWords = t("machines-usagemap.menu_offload", { provider: "§" }).split("§")[0].trim();
    expect([...document.querySelectorAll('[role="menuitem"]')].some((b) => b.textContent?.includes(offloadWords))).toBe(false);
  });

  it("says when the computer or the hub is too old", async () => {
    hub([(c) => (c.url.includes("/storage/usage") ? json({ error: "old", code: "node_outdated" }, 409) : undefined), providers([])]);
    render(<UsageMap machine={mac} pollMs={1} />);
    click(testId("usage-start"));
    await flush();
    expect(testId("usage-error")!.dataset.kind).toBe("node_outdated");
  });

  it("starts at once in reclaim mode, marks the suggestions and selects one from its chip", async () => {
    const calls = await mapReady([usage([overview()]), providers([provider]), inv, folder], { mode: "reclaim", seq: 1 });
    expect(calls.some((c) => c.url.includes("/storage/usage"))).toBe(true);
    const reclaim = testId("usage-reclaim")!;
    expect(reclaim.textContent).toContain(bytesBin(24 * GB));
    const chips = [...reclaim.querySelectorAll<HTMLElement>("[data-suggestion]")];
    expect(chips.map((c) => c.dataset.suggestion)).toEqual(["/Users/me/Movies", "/Users/me/Library/Caches/go-build"]);
    expect(box("/Users/me/Movies")!.dataset.suggested).toBeDefined();
    click(chips[0]);
    expect(testId("usage-selection-rows")!.textContent).toContain("Movies");
  });
});

describe("the map's menu rules", () => {
  const h = { zoom() {}, toggle() {}, trash() {}, offload() {}, clean() {}, copy() {}, restore() {}, showOffloaded() {} };
  const node = (extra: Partial<UsageNode>): UsageNode => ({ id: "/u/x", name: "x", path: "/u/x", kind: "dir", category: "personal", bytes: GB, has_children: false, ...extra });

  it("disables Trash with the refusal's reason", () => {
    const items = localMenu(node({ can_trash: false, refusal: "credentials" }), { selected: false, providers: [], offloadedCount: 0 }, h);
    const trash = items.find((i) => i.key === "trash")!;
    expect(trash.disabled).toBe(true);
    expect(trash.detail).toBe(t("machines-usagemap.refusal_credentials"));
  });
  it("offers Offload per provider only when one is there, and only for kinds it takes", () => {
    expect(localMenu(node({ can_offload: true }), { selected: false, providers: [], offloadedCount: 0 }, h).some((i) => i.key.startsWith("offload"))).toBe(false);
    const files = { ...provider, kinds: ["file"] };
    const off = localMenu(node({ can_offload: true }), { selected: false, providers: [files], offloadedCount: 0 }, h).find((i) => i.key.startsWith("offload"))!;
    expect(off.disabled).toBe(true);
    expect(off.detail).toBe(t("machines-usagemap.menu_not_taken", { provider: "Example Cloud" }));
  });
  it("cleans a catalog cache instead of trashing it, unless a tool uses it", () => {
    const items = localMenu(node({ cache_id: "npm-cache", can_trash: true, cache_in_use: true }), { selected: false, providers: [provider], offloadedCount: 0 }, h);
    expect(items.some((i) => i.key === "trash")).toBe(false);
    expect(items.find((i) => i.key === "clean")!.disabled).toBe(true);
    expect(items.find((i) => i.key.startsWith("offload"))!.disabled).toBe(true);
  });
});
