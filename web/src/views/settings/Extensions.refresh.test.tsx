// @vitest-environment jsdom
// Settings › Extensions after an install, upgrade or uninstall finishes while it is open (the hub's auto-upgrade runs as
// an operation every app sees, or another device does it): the list reads the extensions again, and an extension's page
// reads itself again, keeping a draft being typed; an uninstall goes back to the list.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HubExtension, HubExtensionDetail } from "../../lib/extensions";

const hub = vi.hoisted(() => ({
  listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(),
  list: [] as unknown[],
  detail: null as unknown,
  listReads: 0,
  detailReads: 0,
  /** When set, detail reads wait here (in the order they were asked) instead of answering at once. */
  held: null as ((d: unknown) => void)[] | null,
  saved: null as unknown,
  saveHeld: null as ((d: unknown) => void)[] | null,
  saveSent: [] as unknown[],
}));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  listExtensionsInfo: () => {
    hub.listReads++;
    return Promise.resolve({ extensions: hub.list, canInstall: false, markets: false });
  },
  extensionDetail: () => {
    hub.detailReads++;
    const held = hub.held;
    return held ? new Promise((r) => held.push(r)) : Promise.resolve(hub.detail);
  },
  setExtensionConfig: (_id: string, config: unknown) => {
    hub.saveSent.push(config);
    return hub.saveHeld ? new Promise((r) => hub.saveHeld!.push(r)) : Promise.resolve(hub.saved);
  },
}));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { setToken } from "../../lib/api";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let seq = 0;
let login = 0;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
/** The hub pushes extension_operation: one of id ended as state. */
const pushOp = async (extension: string, kind: "install" | "upgrade" | "uninstall", state = "succeeded") => {
  const at = new Date(Date.now() + ++seq).toISOString();
  const operation = { id: `op-${seq}`, kind, extension, state, steps: [], retryable: false, started_at: at, updated_at: at };
  await act(async () => hub.listeners.get("extension_operation")?.forEach((l) => l({ type: "extension_operation", operation })));
  await flush();
};
const ext = (over: Partial<HubExtension>): HubExtension => ({ id: "example-ext", name: "Example", kind: "process", status: "running", source: "installed", enabled: true, ...over });

beforeEach(() => {
  setToken(`token-${++login}`); // unsaved edits outlive the page (#2800): a new credential drops the case before's
  hub.listReads = 0;
  hub.detailReads = 0;
  hub.held = null;
  hub.saved = null;
  hub.saveHeld = null;
  hub.saveSent = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("the list after an operation", () => {
  it("reads the extensions again when one succeeds, and drops the upgrade note", async () => {
    hub.list = [ext({ upgrade: { state: "needs_permission", market: "m", id: "example-ext", to: "2.0.0" } as HubExtension["upgrade"] })];
    act(() => root.render(<Extensions open={null} onOpen={() => {}} />));
    await flush();
    expect(hub.listReads).toBe(1);
    expect(host.textContent).toContain("新版本待确认");

    hub.list = [ext({})];
    await pushOp("example-ext", "upgrade");
    expect(hub.listReads).toBe(2);
    expect(host.textContent).not.toContain("新版本待确认");
  });

  it("does not read again for an operation still running, and reads once for several at once", async () => {
    hub.list = [ext({})];
    act(() => root.render(<Extensions open={null} onOpen={() => {}} />));
    await flush();
    await pushOp("example-ext", "upgrade", "running");
    expect(hub.listReads).toBe(1);
    await act(async () => {
      window.dispatchEvent(new Event("persona:extensions-changed"));
      window.dispatchEvent(new Event("persona:extensions-changed"));
    });
    await flush();
    expect(hub.listReads).toBe(2);
  });
});

describe("an extension's page after an operation", () => {
  const detail = (over: Partial<HubExtensionDetail>): HubExtensionDetail => ({
    ...(ext({}) as HubExtensionDetail),
    form: { fields: [{ name: "greeting", type: "string", title: "Greeting" }] } as HubExtensionDetail["form"],
    config: { greeting: "hello" },
    ...over,
  });
  const input = () => host.querySelector<HTMLInputElement>("input[type=text], input:not([type])")!;
  const typeInto = (el: HTMLInputElement, value: string) => {
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };

  it("reads itself again when an upgrade of it succeeds, keeping a draft being typed", async () => {
    hub.detail = detail({});
    act(() => root.render(<Extensions open="example-ext" onOpen={() => {}} />));
    await flush();
    expect(hub.detailReads).toBe(1);
    expect(host.textContent).not.toContain("Network access");
    typeInto(input(), "typed");
    expect(host.textContent).toContain("还有 1 项待保存");

    hub.detail = detail({ permissions: [{ key: "net", title: "Network access" }] as HubExtensionDetail["permissions"] });
    await pushOp("other-ext", "upgrade");
    expect(hub.detailReads).toBe(1);
    await pushOp("example-ext", "upgrade");
    expect(hub.detailReads).toBe(2);
    expect(host.textContent).toContain("Network access");
    expect(input().value).toBe("typed");
  });

  it("takes the new settings when nothing is being typed", async () => {
    hub.detail = detail({});
    act(() => root.render(<Extensions open="example-ext" onOpen={() => {}} />));
    await flush();
    hub.detail = detail({ config: { greeting: "after" } });
    await pushOp("example-ext", "upgrade");
    expect(input().value).toBe("after");
  });

  it("a read asked for before the one shown does not put the older detail back", async () => {
    hub.held = [];
    act(() => root.render(<Extensions open="example-ext" onOpen={() => {}} />));
    await flush();
    const [mount] = hub.held;
    await pushOp("example-ext", "upgrade");
    const [, again] = hub.held;
    expect(hub.detailReads).toBe(2);
    await act(async () => again(detail({ version: "2.0.0", config: { greeting: "after" } })));
    await flush();
    expect(input().value).toBe("after");
    await act(async () => mount(detail({ version: "1.0.0", config: { greeting: "before" } })));
    await flush();
    expect(input().value).toBe("after");
    expect(host.textContent).toContain("2.0.0");
    expect(host.textContent).not.toContain("1.0.0");
  });

  it("a read still out when the settings are saved does not put the old settings back", async () => {
    hub.detail = detail({});
    act(() => root.render(<Extensions open="example-ext" onOpen={() => {}} />));
    await flush();
    hub.held = [];
    await pushOp("example-ext", "upgrade");
    const [again] = hub.held;
    typeInto(input(), "typed");
    hub.saved = detail({ config: { greeting: "typed" } });
    const save = [...host.querySelectorAll("button")].find((b) => b.textContent === "保存")!;
    await act(async () => save.click());
    const [, preflight] = hub.held;
    await act(async () => preflight(detail({ config: { greeting: "hello" } })));
    await flush();
    expect(save.isConnected).toBe(false);
    await act(async () => again(detail({ config: { greeting: "hello" } })));
    await flush();
    expect(input().value).toBe("typed");
  });

  it("reconciles a refresh requested while an older save is in flight", async () => {
    hub.detail = detail({ version: "1.0.0" });
    act(() => root.render(<Extensions open="example-ext" onOpen={() => {}} />));
    await flush();
    typeInto(input(), "typed");
    hub.saveHeld = [];
    const save = [...host.querySelectorAll("button")].find((b) => b.textContent === "保存")!;
    await act(async () => save.click());
    await flush();
    const [answerSave] = hub.saveHeld;
    hub.detail = detail({ version: "2.0.0", config: { greeting: "typed" } });
    await pushOp("example-ext", "upgrade");
    expect(host.textContent).toContain("1.0.0");
    await act(async () => answerSave(detail({ version: "1.0.0", config: { greeting: "typed" } })));
    await flush();
    expect(host.textContent).toContain("2.0.0");
    expect(host.textContent).not.toContain("1.0.0");
  });

  it("a successful save keeps edits made while it was pending as an unsaved draft", async () => {
    const numeric = {
      form: { fields: [{ name: "greeting", type: "string", title: "Greeting" }, { name: "retries", type: "integer", title: "Retries" }] } as HubExtensionDetail["form"],
      config: { greeting: "hello", retries: 1 },
    };
    hub.detail = detail(numeric);
    act(() => root.render(<Extensions open="example-ext" onOpen={() => {}} />));
    await flush();
    const retries = host.querySelector<HTMLInputElement>("input[inputmode=numeric]")!;
    typeInto(retries, "02");
    hub.saveHeld = [];
    const save = [...host.querySelectorAll("button")].find((b) => b.textContent === "保存")!;
    await act(async () => save.click());
    await flush();
    expect(hub.saveSent).toEqual([{ greeting: "hello", retries: 2 }]);

    typeInto(retries, "03");
    expect(retries.value).toBe("03");
    const [answerSave] = hub.saveHeld;
    await act(async () => answerSave(detail({ ...numeric, config: { greeting: "authoritative", retries: 2 } })));
    await flush();

    expect(retries.value).toBe("03");
    expect(input().value).toBe("authoritative");
    expect(host.textContent).toContain("还有 1 项待保存");
    expect(hub.saveSent).toHaveLength(1);
  });

  it("keeps an edit back to the previous value while the first save is pending", async () => {
    hub.detail = detail({});
    await act(async () => root.render(<Extensions open="example-ext" onOpen={() => {}} />));
    typeInto(input(), "submitted");
    hub.saveHeld = [];
    const save = [...host.querySelectorAll("button")].find((b) => b.textContent === "保存")!;
    await act(async () => save.click());
    typeInto(input(), "hello");
    const [finish] = hub.saveHeld;
    await act(async () => finish(detail({ config: { greeting: "submitted" } })));
    expect(input().value).toBe("hello");
    expect(host.textContent).toContain("还有 1 项待保存");
    expect(hub.saveSent).toEqual([{ greeting: "submitted" }]);
  });

  it("goes back to the list when it is uninstalled", async () => {
    hub.detail = detail({});
    const onOpen = vi.fn();
    act(() => root.render(<Extensions open="example-ext" onOpen={onOpen} />));
    await flush();
    await pushOp("example-ext", "uninstall");
    expect(onOpen).toHaveBeenCalledWith(null);
  });
});
