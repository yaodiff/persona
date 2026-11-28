// @vitest-environment jsdom
// Another manager's extension page (docs/assistant/managers.md "Clients"): the connectors it contributes, its channel and its
// upgrade are that manager's, not the default's of the same id; a link that names no manager is the current assistant's,
// and Settings › Connectors and Extensions list the current assistant's copies only.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), features: ["channels", "extension_markets"] }) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { t } from "../../i18n";
import { readExternalDirections, readExternalPolicy } from "../../features/external-sources/api";
import { chooseManager, managerHeaderFor, resetManagerChoice } from "../../lib/manager-choice";
import { setRoster, type ManagerList } from "../../lib/managers";
import type { ExtensionTrust } from "../../lib/tracks";
import { ChannelPage } from "./Channels";
import { Connectors } from "./Connectors";
import { Extensions } from "./Extensions";
import { ConnectorList } from "./HubConnector";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ROSTER = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "助手", online: true, builtin: true },
    { id: "m-1", name: "工作", online: true },
  ],
};
const DETAIL = {
  id: "example-wechat", name: "示例微信", kind: "process", status: "running", source: "installed", origin: "market:official/example-wechat", enabled: true,
  contributes: { channel: ["wechat"], connectors: ["wechat"] },
  auto_upgrade: false, upgrade: { market: "official", id: "example-wechat", from: "1.0.0", to: "1.1.0", state: "off" },
};
// the merged list: each manager's built-in copy, and the default's install from a market (a source with no word for it)
const LIST = [
  { id: "example-wechat", name: "示例微信", kind: "process", status: "running", source: "builtin", enabled: true, manager: "assistant" },
  { id: "example-gateway", name: "Example Gateway", kind: "quota", status: "running", source: "market", enabled: true, manager: "assistant" },
  { id: "example-wechat", name: "示例微信", kind: "process", status: "running", source: "builtin", enabled: true, manager: "m-1" },
];
const CHANNEL = { id: "wechat", name: "微信", kind: "im", source: "plugin", installed: true, enabled: true, state: "ready" };

let host: HTMLDivElement;
let root: Root;
let calls: { url: string; method: string; header: string | null; body?: string }[] = [];
let roster: (() => void) | null = null; // set: the roster's answer waits until it is called
let servedRoster: ManagerList;
let detail: Record<string, unknown> = DETAIL;
let grants: Record<string, ExtensionTrust>;

const reply = (body: unknown, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(body), headers: new Headers() });
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  calls = [];
  roster = null;
  servedRoster = ROSTER;
  detail = DETAIL;
  grants = {
    assistant: { extension: DETAIL.id, trust: "third_party", categories: ["finance"] },
    "m-1": { extension: DETAIL.id, trust: "owner", categories: [] },
  };
  resetManagerChoice();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const header = new Headers(init?.headers).get("X-Persona-Manager");
    calls.push({ url, method: init?.method ?? "GET", header, body: init?.body as string | undefined });
    if (url.endsWith("/managers")) {
      if (roster === null) return reply(servedRoster);
      return new Promise((done) => (roster = () => done(reply(servedRoster))));
    }
    if (/\/(plugins|extensions)\/example-wechat\/-\/\?entry_lists=1$/.test(url)) return reply(detail);
    if (url.endsWith("/assistant/extension-trust")) return reply({ items: [grants[header ?? "assistant"]] });
    if (url.endsWith(`/assistant/extension-trust/${DETAIL.id}`) && init?.method === "PUT") {
      const grant = { extension: DETAIL.id, ...JSON.parse(String(init.body)) };
      grants[header ?? "assistant"] = grant;
      return reply(grant);
    }
    if (url.endsWith("/connectors")) {
      // the merged list: each manager's copy of the connector, stamped
      return reply({ connectors: [
        { id: "wechat", name: "微信", status: "ok", account: "home@example.com", inbox: false, manager: "assistant" },
        { id: "wechat", name: "微信", status: "ok", account: "work@example.com", inbox: true, manager: "m-1" },
      ] });
    }
    if (/\/(plugins|extensions)$/.test(url)) return reply({ extensions: LIST });
    if (url.includes("/connectors/wechat/settings")) return reply({ id: "wechat", name: "微信", status: "ok", inbox: false });
    if (url.endsWith("/channels")) return reply({ channels: header === "m-1" ? [CHANNEL] : [] });
    if (url.endsWith("/channels/wechat")) return reply({ ...CHANNEL, capabilities: {}, bind: "none", waiting: { answers: 0, asks: 0 } });
    if (url.includes("/-/catalog")) return reply({ markets: [{ id: "official", name: "官方", official: true }], extensions: [{ id: "example-wechat", name: "示例微信", market: "official", version: "1.1.0", installed: "1.0.0" }] });
    return reply({ error: "not found" }, 404);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  setRoster(null);
  resetManagerChoice();
});

const button = (text: string) => [...host.querySelectorAll<HTMLElement>("button, [role=button], [role=switch]")].find((b) => b.textContent?.includes(text) || b.getAttribute("aria-label") === text);

describe("another manager's extension page", () => {
  it("shows and changes that manager's connector, and links to its connector and channel pages", async () => {
    setRoster(ROSTER);
    await import("../../features/external-sources/ExternalSourceSettings");
    const onChannel = vi.fn();
    const onConnector = vi.fn();
    act(() => root.render(<Extensions open="example-wechat@m-1" onOpen={() => {}} onChannel={onChannel} onConnector={onConnector} />));
    await flush();
    expect(calls.find((c) => /example-wechat\/-\/\?entry_lists=1$/.test(c.url))?.header).toBe("m-1");
    expect(calls.some((c) => c.url.includes("/external-source-bindings/summary") && c.header === "m-1")).toBe(true);
    // m-1's copy (inbox on), not the default's (off)
    const sw = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="加入收件箱"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    await act(async () => sw.click());
    await flush();
    const put = calls.find((c) => c.method === "PUT" && c.url.includes("/connectors/wechat/settings"))!;
    expect(put.header).toBe("m-1");
    act(() => button("连接器设置")!.click());
    expect(onConnector).toHaveBeenCalledWith("wechat", "m-1");
    // its channel is read from m-1 and opens as m-1's
    expect(calls.some((c) => c.url.endsWith("/channels") && c.header === "m-1")).toBe(true);
    act(() => button("在「设置 › 渠道」中设置「微信」")!.click());
    expect(onChannel).toHaveBeenCalledWith("wechat", "m-1");
  });

  it("upgrades that manager's copy: the market page names it on its requests and on the consent", async () => {
    setRoster(ROSTER);
    const onOpen = vi.fn();
    act(() => root.render(<Extensions open="example-wechat@m-1" onOpen={onOpen} />));
    await flush();
    act(() => button("升级")!.click());
    expect(onOpen).toHaveBeenLastCalledWith("-market/official/example-wechat@m-1");
    act(() => root.render(<Extensions open="-market/official/example-wechat@m-1" onOpen={onOpen} />));
    await flush();
    expect(calls.filter((c) => c.url.includes("/-/catalog")).map((c) => c.header)).toEqual(["m-1"]);
    expect(host.querySelector("[data-for-manager]")?.textContent).toContain("工作");
    // back goes to that manager's extension, not to the default's catalog
    act(() => button("扩展市场")?.click());
    expect(onOpen).toHaveBeenLastCalledWith("example-wechat@m-1");
  });

  it("names the default on trust reads and stamps its external source bindings with the default owner", async () => {
    setRoster(ROSTER);
    await import("../../features/external-sources/ExternalSourceSettings");
    act(() => root.render(<Extensions open="example-wechat" onOpen={() => {}} onChannel={() => {}} onConnector={() => {}} />));
    await flush();
    await readExternalPolicy("assistant", { provider: "wechat" }).catch(() => {});
    await readExternalDirections("assistant").catch(() => {});
    await act(async () => { await vi.dynamicImportSettled(); });
    const sw = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="加入收件箱"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(calls.some((c) => c.url.includes("/external-source-bindings/summary") && c.header === null)).toBe(true);
    expect(calls.some((c) => c.url.includes("/assistant/external-source-policies") && c.header === null)).toBe(true);
    expect(calls.some((c) => c.url.endsWith("/assistant/leaders") && c.header === null)).toBe(true);
    expect(calls.filter((c) => c.url.endsWith("/assistant/extension-trust")).map((c) => c.header)).toEqual(["assistant"]);
    const detail = calls.find((c) => /example-wechat\/-\/\?entry_lists=1$/.test(c.url));
    expect(detail?.header).toBe("assistant");
    expect(calls.filter((c) => c !== detail && !c.url.endsWith("/managers") && !c.url.endsWith("/assistant/extension-trust")).every((c) => !c.header)).toBe(true);
  });

  it("pins the default assistant on external-source reads even when another assistant is chosen", async () => {
    setRoster(ROSTER);
    act(() => chooseManager("m-1"));
    await import("../../features/external-sources/ExternalSourceSettings");
    act(() => root.render(<Extensions open="example-wechat@assistant" onOpen={() => {}} onChannel={() => {}} onConnector={() => {}} />));
    await flush();
    await readExternalPolicy("assistant", { provider: "wechat" }).catch(() => {});
    await readExternalDirections("assistant").catch(() => {});
    expect(calls.find((c) => c.url.includes("/external-source-bindings/summary"))?.header).toBe("assistant");
    expect(calls.find((c) => c.url.includes("/assistant/external-source-policies"))?.header).toBe("assistant");
    expect(calls.find((c) => c.url.endsWith("/assistant/leaders"))?.header).toBe("assistant");
  });

  it("edits the named assistant's grants in legacy Extensions while the sidebar stays on the default", async () => {
    servedRoster = { ...ROSTER, managers: ROSTER.managers.map((m) => ({ ...m, extension_info_access_scoped: m.id === "m-1" })) };
    setRoster(servedRoster);
    await act(async () => root.render(<Extensions open="example-wechat@m-1" onOpen={() => {}} />));
    const owner = host.querySelector<HTMLButtonElement>("[data-extension-info-access] [role=switch]")!;
    expect(owner?.getAttribute("aria-checked")).toBe("true");
    await act(async () => owner.click());
    expect(calls.filter(({ url }) => url.includes("/assistant/extension-trust")).map(({ method, header }) => [method, header]))
      .toEqual([["GET", "m-1"], ["PUT", "m-1"], ["GET", "m-1"]]);
    expect(grants["m-1"].trust).toBe("third_party");
    expect(grants.assistant.categories).toEqual(["finance"]);
  });

  it.each([undefined, false])("keeps trust default-only when the other assistant advertises %s", async (capability) => {
    servedRoster = { ...ROSTER, managers: ROSTER.managers.map((m) => ({ ...m, extension_info_access_scoped: capability })) };
    setRoster(servedRoster);
    chooseManager("m-1");
    await act(async () => root.render(<Extensions open="example-wechat@m-1" onOpen={() => {}} />));
    expect(host.querySelector("[data-extension-info-access]")).toBeNull();
    expect(calls.some(({ url }) => url.includes("/assistant/extension-trust"))).toBe(false);

    await act(async () => root.render(<Extensions open="example-wechat@assistant" onOpen={() => {}} />));
    const owner = host.querySelector<HTMLButtonElement>("[data-extension-info-access] [role=switch]")!;
    expect(owner?.getAttribute("aria-checked")).toBe("false");
    await act(async () => owner.click());
    expect(calls.filter(({ url }) => url.includes("/assistant/extension-trust")).map(({ method, header }) => [method, header]))
      .toEqual([["GET", "assistant"], ["PUT", "assistant"], ["GET", "assistant"]]);
  });

  it("shows what the default's extension may read, with live switches", async () => {
    setRoster(ROSTER);
    act(() => root.render(<Extensions open="example-wechat" onOpen={() => {}} onChannel={() => {}} onConnector={() => {}} />));
    await flush();
    const group = host.querySelector("[data-extension-info-access]")!;
    expect(group).not.toBeNull();
    const switches = [...group.querySelectorAll<HTMLButtonElement>('[role="switch"]')];
    expect(switches.length).toBeGreaterThan(0);
    expect(switches.every((sw) => !sw.disabled)).toBe(true);
  });

  it("a read-only extension's page shows what it may read without live switches", async () => {
    setRoster(ROSTER);
    detail = { ...DETAIL, source: "hub.json", read_only: true };
    act(() => root.render(<Extensions open="example-wechat" onOpen={() => {}} onChannel={() => {}} onConnector={() => {}} />));
    await flush();
    const switches = [...host.querySelectorAll<HTMLButtonElement>('[data-extension-info-access] [role="switch"]')];
    expect(switches.length).toBeGreaterThan(0);
    expect(switches.every((sw) => sw.disabled)).toBe(true);
  });

  it("opens a link that names no manager as the current assistant's copy", async () => {
    setRoster(ROSTER);
    act(() => chooseManager("m-1"));
    act(() => root.render(<Extensions open="example-wechat" onOpen={() => {}} />));
    await flush();
    const reads = calls.filter((c) => /example-wechat\/-\/\?entry_lists=1$/.test(c.url));
    expect(reads.length).toBeGreaterThan(0);
    expect(reads.every((c) => c.header === "m-1")).toBe(true);
    expect(host.textContent).toContain("示例微信");
  });

  it("installs from the market for the current assistant", async () => {
    setRoster(ROSTER);
    act(() => chooseManager("m-1"));
    act(() => root.render(<Extensions open="-market/official/example-wechat" onOpen={() => {}} />));
    await flush();
    const catalog = calls.filter((c) => c.url.includes("/-/catalog"));
    expect(catalog.length).toBeGreaterThan(0);
    expect(catalog.every((c) => c.header === "m-1")).toBe(true);
  });
});

describe("another manager's connector and channel pages", () => {
  it("shows that manager's copy of the connector, and names it on the page's requests while open", async () => {
    setRoster(ROSTER);
    act(() => root.render(<Connectors open="wechat@m-1" onOpen={() => {}} />));
    await flush();
    expect(host.textContent).toContain("work@example.com");
    expect(host.textContent).not.toContain("home@example.com");
    expect(managerHeaderFor("/connectors/wechat/settings")).toBe("m-1");
    act(() => root.render(<Connectors open="wechat" onOpen={() => {}} />));
    await flush();
    expect(host.textContent).toContain("home@example.com");
    expect(managerHeaderFor("/connectors/wechat/settings")).toBeUndefined();
  });

  it("reads another manager's channel with its name", async () => {
    setRoster(ROSTER);
    act(() => root.render(<ChannelPage id="wechat" manager="m-1" onBack={() => {}} onSettings={() => {}} />));
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(calls.find((c) => c.url.endsWith("/channels/wechat"))?.header).toBe("m-1");
  });
});

describe("a page opened by an address that names no assistant, or another one", () => {
  it("opens a connector and a channel without `@` as the current assistant's", async () => {
    setRoster(ROSTER);
    act(() => chooseManager("m-1"));
    act(() => root.render(<Connectors open="wechat" onOpen={() => {}} />));
    await flush();
    expect(host.textContent).toContain("work@example.com");
    expect(host.textContent).not.toContain("home@example.com");
    expect(managerHeaderFor("/connectors/wechat/settings")).toBe("m-1");
    act(() => root.render(<ChannelPage id="wechat" onBack={() => {}} onSettings={() => {}} />));
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(calls.find((c) => c.url.endsWith("/channels/wechat"))?.header).toBe("m-1");
  });

  it("names whose it is beside the title when it is another assistant's than the current one's, and nowhere else", async () => {
    setRoster(ROSTER);
    act(() => root.render(<Connectors open="wechat@m-1" onOpen={() => {}} />));
    await flush();
    expect(host.querySelector("[data-owner-mark=m-1]")?.textContent).toContain("工作");
    act(() => chooseManager("m-1"));
    await flush();
    expect(host.querySelector("[data-owner-mark]")).toBeNull();
    act(() => root.render(<Extensions open="example-wechat@assistant" onOpen={() => {}} />));
    await flush();
    expect(host.querySelector("[data-owner-mark=assistant]")?.textContent).toContain("助手");
  });
});

describe("Settings › Connectors' rows", () => {
  const rows = () => [...host.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.includes("微信"));

  it("lists the current assistant's copy only, with no assistant's name over it, and a row opens that copy", async () => {
    setRoster(ROSTER);
    const onOpen = vi.fn();
    act(() => root.render(<ConnectorList onOpen={onOpen} />));
    await flush();
    expect(rows()).toHaveLength(1);
    expect(host.textContent).not.toContain("助手");
    act(() => rows()[0].click());
    expect(onOpen).toHaveBeenLastCalledWith("wechat");
    // 工作 made current: its copy, opened by its name
    act(() => chooseManager("m-1"));
    await flush();
    expect(rows()).toHaveLength(1);
    expect(host.textContent).not.toContain("工作");
    act(() => rows()[0].click());
    expect(onOpen).toHaveBeenLastCalledWith("wechat@m-1");
  });

  it("says the current assistant is offline rather than listing nothing", async () => {
    setRoster({ ...ROSTER, managers: [...ROSTER.managers, { id: "m-2", name: "家里", online: false }] });
    act(() => chooseManager("m-2"));
    act(() => root.render(<ConnectorList onOpen={() => {}} />));
    await flush();
    expect(rows()).toHaveLength(0);
    expect(host.querySelector('[data-assistant-offline="m-2"]')?.textContent).toContain(t("settings-managers.offline_page_title", { name: "家里" }));
    // an online one: its list, no offline state
    act(() => chooseManager("m-1"));
    await flush();
    expect(host.querySelector("[data-assistant-offline]")).toBeNull();
  });

  it("one manager: no headings, a row opens by id", async () => {
    setRoster({ default: "assistant", managers: [ROSTER.managers[0]] });
    const onOpen = vi.fn();
    act(() => root.render(<ConnectorList onOpen={onOpen} />));
    await flush();
    expect(host.textContent).not.toContain("助手");
    act(() => rows()[0].click());
    expect(onOpen).toHaveBeenLastCalledWith("wechat");
  });
});

describe("Settings › Extensions' rows", () => {
  it("says the current assistant is offline rather than that nothing is installed", async () => {
    setRoster({ ...ROSTER, managers: [...ROSTER.managers, { id: "m-2", name: "家里", online: false }] });
    act(() => chooseManager("m-2"));
    act(() => root.render(<Extensions open={null} onOpen={() => {}} />));
    await flush();
    expect(host.querySelector('[data-assistant-offline="m-2"]')).not.toBeNull();
    expect(host.textContent).not.toContain(t("settings-extensions.empty_title"));
    act(() => chooseManager("m-1"));
    await flush();
    expect(host.querySelector("[data-assistant-offline]")).toBeNull();
  });

  const rows = () => [...host.querySelectorAll<HTMLElement>("[data-extension-row]")];
  // what follows a row's name: its source's word, if it has one
  const tags = (row: HTMLElement) => [...row.querySelector("span.truncate")!.parentElement!.children].slice(1).map((c) => c.textContent);

  it("lists the current assistant's copies only, each source with its word and none with an empty tag", async () => {
    setRoster(ROSTER);
    act(() => root.render(<Extensions open={null} onOpen={() => {}} />));
    await flush();
    expect(rows().map((r) => `${r.dataset.extensionRow}@${r.dataset.manager}`).sort()).toEqual(["example-gateway@assistant", "example-wechat@assistant"]);
    const byId = (id: string) => rows().find((r) => r.dataset.extensionRow === id)!;
    expect(tags(byId("example-wechat"))).toEqual([t("lib-extensions.source_builtin")]);
    expect(tags(byId("example-gateway"))).toEqual([]);
    act(() => chooseManager("m-1"));
    await flush();
    expect(rows().map((r) => [r.dataset.extensionRow, r.dataset.manager])).toEqual([["example-wechat", "m-1"]]);
  });
});
