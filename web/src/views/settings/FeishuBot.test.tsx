// @vitest-environment jsdom
// The Feishu bot dialog keeps App ID / App Secret edits that are not saved yet: flipping a switch, pairing and the
// pairing poll only take the hub's other fields, and only "verify and save" puts the hub's credentials back in the form.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connectorState, type HubConnector } from "../../lib/connectors";
import type { FeishuBotStatus } from "../../lib/feishu-bot";

type Call = { resolve: (s: FeishuBotStatus) => void; reject?: (error: Error) => void };
const calls = vi.hoisted(() => ({ status: [] as Call[], update: [] as (Call & { body: unknown })[], unpair: [] as Call[] }));
const connectorList = vi.hoisted(() => ({ value: [] as HubConnector[], calls: 0, pending: null as Promise<HubConnector[]> | null }));
const connectorLogin = vi.hoisted(() => vi.fn());
const setConnector = vi.hoisted(() => vi.fn());
const bindChannel = vi.hoisted(() => vi.fn());
vi.mock("../../lib/channels", async (original) => ({
  ...(await original<typeof import("../../lib/channels")>()),
  bindChannel: (...args: unknown[]) => bindChannel(...args),
  forgetChannels: () => {},
}));
vi.mock("../../lib/feishu-bot", async (original) => ({
  ...(await original<typeof import("../../lib/feishu-bot")>()),
  botStatus: () => new Promise<FeishuBotStatus>((resolve, reject) => calls.status.push({ resolve, reject })),
  updateBot: (body: unknown) => new Promise<FeishuBotStatus>((resolve, reject) => calls.update.push({ resolve, reject, body })),
  pairBot: () => new Promise<FeishuBotStatus>(() => {}),
  unpairBot: () => new Promise<FeishuBotStatus>((resolve, reject) => calls.unpair.push({ resolve, reject })),
}));
vi.mock("../../lib/connectors", async (original) => ({
  ...(await original<typeof import("../../lib/connectors")>()),
  listConnectors: () => {
    connectorList.calls++;
    return connectorList.pending ?? Promise.resolve(connectorList.value);
  },
  connectorLogin: (...args: unknown[]) => connectorLogin(...args),
  setConnector: (...args: unknown[]) => setConnector(...args),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: () => {} }));
import { t } from "../../i18n";
import { FeishuBot } from "./FeishuBot";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const saved = (over: Partial<FeishuBotStatus> = {}): FeishuBotStatus => ({
  enabled: true,
  app_id: "cli_saved",
  domain: "https://open.feishu.cn",
  has_secret: true,
  from_config: false,
  features: { main: true, cards: true, all_cards: false, pushes: false, sync: false, agents: false, reports: true, board: false },
  connected: true,
  owner: null,
  pairing: null,
  ...over,
});

let host: HTMLDivElement;
let root: Root;
let visibility: DocumentVisibilityState = "visible";
const show = (v: DocumentVisibilityState) =>
  act(async () => {
    visibility = v;
    document.dispatchEvent(new Event("visibilitychange"));
  });
const input = (label: string) => document.body.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
const type = (label: string, value: string) =>
  act(() => {
    const el = input(label);
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const switches = () => [...document.body.querySelectorAll<HTMLButtonElement>('[role="switch"]')];
// The credentials form is folded under Advanced (#5818): open it before reading or typing in it.
const advancedToggle = () =>
  [...document.body.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) =>
    [t("settings-feishubot.advanced_other_app"), t("settings-feishubot.advanced_have_app")].includes(b.textContent ?? ""),
  );
const advanced = () => act(async () => advancedToggle()!.click());

// Draws the row, answers its status read, and opens the dialog.
async function open(status: FeishuBotStatus) {
  act(() => root.render(<FeishuBot />));
  await act(async () => calls.status[0].resolve(status));
  await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
}

beforeEach(() => {
  calls.status.length = 0;
  calls.update.length = 0;
  calls.unpair.length = 0;
  connectorList.value = [];
  connectorList.calls = 0;
  connectorList.pending = null;
  connectorLogin.mockReset();
  setConnector.mockReset();
  bindChannel.mockReset();
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  Reflect.deleteProperty(document, "visibilityState");
  vi.useRealTimers();
});

describe("Feishu bot dialog", () => {
  it("combines the bot and lark-cli into one Lark row", async () => {
    const onOpen = vi.fn();
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "ok", account: "me" };
    act(() => root.render(<FeishuBot larkcli={larkcli} onOpen={onOpen} />));
    await act(async () => calls.status[0].resolve(saved()));

    const row = [...host.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.lark_name")));
    expect(row).toBeDefined();
    expect(row?.textContent).toContain("cli_saved");
    expect(row?.closest("section")?.textContent).toContain(t("settings-feishubot.combined_footer"));
    expect(row?.querySelector("img")?.getAttribute("alt")).toBe("");
  });

  it("keeps the original group when lark-cli is absent", async () => {
    act(() => root.render(<FeishuBot />));
    await act(async () => calls.status[0].resolve(saved()));

    expect(host.textContent).toContain(t("settings-feishubot.footer"));
    expect(host.textContent).not.toContain(t("settings-feishubot.read_section"));
    expect(host.textContent).not.toContain(t("settings-feishubot.combined_footer"));
    expect(host.querySelectorAll("button")).toHaveLength(1);
  });

  it("shows lark-cli in the bot dialog and opens its connector page from More settings", async () => {
    const onOpen = vi.fn();
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "ok", account: "me" };
    act(() => root.render(<FeishuBot larkcli={larkcli} onOpen={onOpen} />));
    await act(async () => calls.status[0].resolve(saved()));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

    expect(document.body.textContent).toContain(t("settings-feishubot.read_section"));
    const more = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.read_more")));
    await act(async () => more!.click());

    expect(onOpen).toHaveBeenCalledWith("larkcli");
  });

  it("loads lark-cli for a URL-opened dialog when the page has not passed it yet", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "ok", account: "me" };
    connectorList.value = [larkcli];
    act(() => root.render(<FeishuBot />));
    await act(async () => calls.status[0].resolve(saved()));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
    await act(async () => {});

    const row = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.lark_name")));
    expect(row).toBeDefined();
    expect(connectorList.calls).toBe(1);
  });

  it("uses the lark-cli warning as the combined row's status", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "no_permission", account: "me", missing_scopes: ["im:chat:read"] };
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));

    const row = [...host.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.lark_name")))!;
    expect(row.textContent).toContain(t("settings-feishubot.read_status_prefix", { status: connectorState(larkcli) }));
    expect([...row.querySelectorAll<HTMLElement>("*")].some((element) => element.classList.contains("bg-orange-tint"))).toBe(true);
  });

  it("orders read-as-me, then the bot's controls, with the credentials folded under Advanced last", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "ok", account: "me", capabilities: ["login"] };
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

    const headings = () => [...document.querySelectorAll<HTMLElement>("[role=dialog] section h2")].map((x) => x.textContent);
    expect(headings()).not.toContain(t("settings-feishubot.credentials"));
    expect(headings().indexOf(t("settings-feishubot.read_section"))).toBeGreaterThanOrEqual(0);
    expect(headings().indexOf(t("settings-feishubot.read_section"))).toBeLessThan(headings().indexOf(t("settings-feishubot.bot_section")));
    // An app is bound: no create step, and Advanced offers another app, folded.
    expect(document.body.textContent).not.toContain(t("settings-feishubot.create_start"));
    expect(advancedToggle()?.textContent).toBe(t("settings-feishubot.advanced_other_app"));
    expect(advancedToggle()?.getAttribute("aria-expanded")).toBe("false");
    await advanced();
    expect(headings().indexOf(t("settings-feishubot.bot_section"))).toBeLessThan(headings().indexOf(t("settings-feishubot.credentials")));
    // Signed in: the section is titled for the identity, and signing in again is the quiet secondary action.
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(dialog.textContent).toContain(t("settings-feishubot.read_as_you"));
    const again = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.read_reauthorize")));
    expect(again).toBeDefined();
    expect([...dialog.querySelectorAll("button")].some((button) => button.textContent === t("settings-feishubot.read_authorize"))).toBe(false);
  });

  it("switches reading as you off from the Lark page, the same switch as the connector page", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark · my messages", status: "ok", account: "me", enabled: true };
    setConnector.mockResolvedValue({ ...larkcli, enabled: false });
    connectorList.value = [{ ...larkcli, enabled: false }];
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

    const sw = document.querySelector<HTMLButtonElement>(`[role=dialog] [role="switch"][aria-label="${t("settings-feishubot.read_section")}"]`)!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    await act(async () => sw.click());
    expect(setConnector).toHaveBeenCalledWith("larkcli", { enabled: false });
  });

  it("turns on every comment for documents I own from the Lark page, keeping the other options", async () => {
    const options = { inbox_all_chats: ["oc_example"], documents: [], wiki_spaces: [], owned_all_comments: false, triage_rules: "rule" };
    const larkcli: HubConnector = { id: "larkcli", name: "Lark · my messages", status: "ok", account: "me", enabled: true, options };
    setConnector.mockResolvedValue({ ...larkcli, options: { ...options, owned_all_comments: true } });
    connectorList.value = [larkcli];
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

    const sw = document.querySelector<HTMLButtonElement>(`[role=dialog] [role="switch"][aria-label="${t("settings-hubconnector.lark_docs_owned")}"]`)!;
    expect(sw.getAttribute("aria-checked")).toBe("false");
    await act(async () => sw.click());
    expect(setConnector).toHaveBeenCalledWith("larkcli", { options: { ...options, owned_all_comments: true } });
  });

  it("starts lark-cli authorization inline and renders a device-code step", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "not_signed_in", capabilities: ["login"] };
    connectorLogin.mockResolvedValue({ step: "device", kind: "device_code", user_code: "ABC123", url: "https://example.test/authorize" });
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

    const authorize = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.read_authorize")))!;
    await act(async () => authorize.click());
    expect(connectorLogin).toHaveBeenCalledWith("larkcli", "", {});
    expect(document.body.textContent).toContain("ABC123");
  });

  it("draws the device-code step's QR code when the hub sends one, the link kept as the secondary way (#5818)", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "not_signed_in", capabilities: ["login"] };
    connectorLogin.mockResolvedValue({
      step: "device", kind: "device_code", user_code: "ABC123", url: "https://example.test/authorize?user_code=ABC123", qr: { size: 2, rows: ["10", "01"] },
    });
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
    const authorize = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.read_authorize")))!;
    await act(async () => authorize.click());

    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(dialog.querySelector(`svg[role=img][aria-label="${t("settings-hubconnector.qr_label")}"]`)).not.toBeNull();
    expect(dialog.textContent).toContain(t("settings-hubconnector.scan_to_authorize"));
    expect(dialog.textContent).toContain("ABC123");
  });

  it("with no app anywhere, reading Lark waits for the app; once it is created, authorizing starts by itself (#5818)", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "not_signed_in", capabilities: ["login"] };
    connectorList.value = [larkcli];
    const larkcliStatus = (over: object) => ({ profile: "", same_app: false, mode: "default" as const, choice: "", profiles: [], ...over });
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ app_id: "", has_secret: false, connected: false, larkcli: larkcliStatus({}) })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(dialog.textContent).toContain(t("settings-feishubot.read_needs_app"));
    expect([...dialog.querySelectorAll("button")].some((b) => b.textContent?.includes(t("settings-feishubot.read_authorize")))).toBe(false);

    // The scan is confirmed: the bot has its app, lark-cli follows it, and the authorization starts without a press.
    bindChannel.mockResolvedValueOnce({ done: true, account: "owner" });
    connectorLogin.mockResolvedValue({ step: "device", kind: "device_code", user_code: "ABC123", url: "https://example.test/authorize?user_code=ABC123" });
    const start = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("settings-feishubot.create_start"))!;
    await act(async () => start.click());
    await act(async () => calls.status[1].resolve(saved({ owner: { name: "owner", paired_at: 1 }, larkcli: larkcliStatus({ mode: "bot", app_id: "cli_saved", same_app: true }) })));
    expect(dialog.textContent).not.toContain(t("settings-feishubot.read_needs_app"));
    expect(connectorLogin).toHaveBeenCalledWith("larkcli", "", {});
    expect(dialog.textContent).toContain("ABC123");
  });

  it.each(["before", "after"])("shows the paired bot when the connector list returns 503 %s the bot status", async (order) => {
    vi.useFakeTimers();
    const larkcli: HubConnector = { id: "larkcli", name: "Lark", status: "not_signed_in", capabilities: ["login"] };
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ app_id: "", has_secret: false, connected: false })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
    bindChannel.mockResolvedValueOnce({ step: "register", kind: "qr", interval: 5, qr: { size: 2, rows: ["10", "01"] } });
    bindChannel.mockResolvedValueOnce({ done: true, account: "Example owner" });
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    const start = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("settings-feishubot.create_start"))!;
    await act(async () => start.click());
    expect(dialog.querySelector("svg[role=img]")).not.toBeNull();
    let failConnectors!: (error: Error) => void;
    connectorList.pending = new Promise((_, reject) => { failConnectors = reject; });
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    const paired = saved({ owner: { name: "Example owner", paired_at: 1 } });
    if (order === "before") await act(async () => failConnectors(new Error("503 Service Unavailable")));
    await act(async () => calls.status[1].resolve(paired));
    if (order === "after") await act(async () => failConnectors(new Error("503 Service Unavailable")));

    expect(dialog.textContent).toContain("Example owner");
    expect(dialog.textContent).not.toContain(t("settings-feishubot.create_start"));
    expect(host.textContent).toContain("cli_saved");
    expect(connectorLogin).not.toHaveBeenCalled();
    await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
    expect(bindChannel).toHaveBeenCalledTimes(2);

    // The bot's existing recheck still works without binding or authorizing again.
    await act(async () => dialog.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-feishubot.recheck")}"]`)!.click());
    await act(async () => calls.status[2].resolve(paired));
    expect(dialog.textContent).toContain("Example owner");
    expect(bindChannel).toHaveBeenCalledTimes(2);
    expect(connectorLogin).not.toHaveBeenCalled();

    // Recovery remains explicit: the existing authorize action can start a single device flow.
    connectorList.pending = null;
    connectorList.value = [larkcli];
    connectorLogin.mockResolvedValueOnce({ step: "device", kind: "device_code", user_code: "ABC123", url: "https://example.test/authorize" });
    const authorize = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("settings-feishubot.read_authorize"))!;
    await act(async () => authorize.click());
    expect(connectorLogin).toHaveBeenCalledTimes(1);
    await act(async () => calls.status[3].resolve(paired));
    expect(dialog.textContent).toContain("ABC123");
    expect(bindChannel).toHaveBeenCalledTimes(2);
  });

  describe("confirmed status ordering", () => {
    const owner = { name: "Current owner", paired_at: 2 };
    const stale = () => saved({ owner: { name: "Earlier owner", paired_at: 1 } });
    const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
    const recheckButton = () => dialog().querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-feishubot.recheck")}"]`);
    const clickText = (label: string) => act(async () => {
      const button = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label);
      expect(button).toBeDefined();
      button!.click();
    });
    async function boundWithPendingStatus(connectorFails = true) {
      vi.useFakeTimers();
      const larkcli: HubConnector = { id: "larkcli", name: "Lark", status: "not_signed_in", capabilities: ["login"] };
      act(() => root.render(<FeishuBot larkcli={larkcli} />));
      await act(async () => calls.status[0].resolve(saved({ app_id: "", has_secret: false, connected: false })));
      await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
      bindChannel.mockResolvedValueOnce({ step: "register", kind: "qr", interval: 5, qr: { size: 2, rows: ["10", "01"] } });
      bindChannel.mockResolvedValueOnce({ done: true, account: "Earlier owner" });
      await clickText(t("settings-feishubot.create_start"));
      let rejectConnectors!: (error: Error) => void;
      let resolveConnectors!: (value: HubConnector[]) => void;
      connectorList.pending = new Promise((resolve, reject) => { resolveConnectors = resolve; rejectConnectors = reject; });
      await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
      if (connectorFails) await act(async () => rejectConnectors(new Error("503 Service Unavailable")));
      expect(calls.status).toHaveLength(2); // A is pending while the connector fails or waits.
      await act(async () => recheckButton()!.click());
      expect(calls.status).toHaveLength(3); // B is the explicit retry.
      expect(recheckButton()).toBeNull();
      return () => resolveConnectors([larkcli]);
    }

    it("keeps the successful recheck when the post-bind status arrives late", async () => {
      await boundWithPendingStatus();
      await act(async () => calls.status[2].resolve(saved({ owner })));
      expect(recheckButton()).not.toBeNull();
      await act(async () => calls.status[1].resolve(stale()));
      expect(dialog().textContent).toContain(owner.name);
      expect(dialog().textContent).not.toContain("Earlier owner");
      expect(dialog().textContent).not.toContain(t("settings-feishubot.create_start"));
      expect(connectorLogin).not.toHaveBeenCalled();
      expect(bindChannel).toHaveBeenCalledTimes(2);
    });

    it.each(["disable", "unpair"])("keeps a confirmed %s after recheck when the post-bind status arrives late", async (action) => {
      await boundWithPendingStatus();
      await act(async () => calls.status[2].resolve(saved({ owner })));
      if (action === "disable") {
        await act(async () => dialog().querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.enable")}"]`)!.click());
        expect(calls.update[0].body).toEqual({ enabled: false });
        await act(async () => calls.update[0].resolve(saved({ owner, enabled: false })));
      } else {
        await clickText(t("settings-feishubot.unpair"));
        await clickText(t("settings-feishubot.unpair"));
        await act(async () => calls.unpair[0].resolve(saved({ owner: null })));
      }
      await act(async () => calls.status[1].resolve(stale()));
      if (action === "disable") {
        expect(dialog().querySelector(`[role="switch"][aria-label="${t("settings-feishubot.enable")}"]`)?.getAttribute("aria-checked")).toBe("false");
        expect(dialog().textContent).toContain(owner.name);
      } else {
        expect(dialog().textContent).not.toContain("Earlier owner");
        expect(dialog().textContent).not.toContain(owner.name);
        expect(dialog().textContent).toContain(t("settings-feishubot.pair"));
      }
      expect(connectorLogin).not.toHaveBeenCalled();
      expect(bindChannel).toHaveBeenCalledTimes(2);
    });

    it("keeps recheck loading until it answers and does not authorize superseded setup", async () => {
      const finishConnectors = await boundWithPendingStatus(false);
      await act(async () => calls.status[1].resolve(stale()));
      expect(dialog().textContent).toContain("Earlier owner");
      expect(recheckButton()).toBeNull(); // A must not clear B's loading feedback.
      await act(async () => calls.status[2].resolve(saved({ owner, enabled: false })));
      expect(recheckButton()).not.toBeNull();
      await act(async () => finishConnectors());
      expect(dialog().textContent).toContain(owner.name);
      expect(connectorLogin).not.toHaveBeenCalled();
      expect(bindChannel).toHaveBeenCalledTimes(2);
    });

    it("retries a failed recheck and keeps its successful answer over the late setup read", async () => {
      await boundWithPendingStatus();
      await act(async () => calls.status[2].reject!(new Error("Synthetic recheck failure")));
      expect(dialog().querySelector('[role="alert"]')?.textContent).toContain("Synthetic recheck failure");
      await act(async () => recheckButton()!.click());
      expect(recheckButton()).toBeNull();
      await act(async () => calls.status[3].resolve(saved({ owner })));
      expect(dialog().querySelector('[role="alert"]')).toBeNull();
      expect(recheckButton()).not.toBeNull();
      await act(async () => calls.status[1].resolve(stale()));
      expect(dialog().textContent).toContain(owner.name);
      expect(connectorLogin).not.toHaveBeenCalled();
    });

    it.each(["success", "failure"])("ignores late mount %s after a newer dialog refresh", async (outcome) => {
      const larkcli: HubConnector = { id: "larkcli", name: "Lark", status: "not_signed_in", capabilities: ["login"] };
      connectorList.value = [larkcli];
      act(() => root.render(<FeishuBot larkcli={larkcli} />));
      await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
      connectorLogin.mockResolvedValueOnce({ done: true });
      await clickText(t("settings-feishubot.read_authorize"));
      expect(calls.status).toHaveLength(2);
      expect(recheckButton()).toBeNull();
      await act(async () => calls.status[1].resolve(saved({ owner })));
      expect(recheckButton()).not.toBeNull();
      if (outcome === "success") await act(async () => calls.status[0].resolve(stale()));
      else await act(async () => calls.status[0].reject!(new Error("Synthetic mount failure")));
      expect(dialog().textContent).toContain(owner.name);
      expect(dialog().textContent).not.toContain("Earlier owner");
      expect(dialog().querySelector('[role="alert"]')).toBeNull();
      expect(connectorLogin).toHaveBeenCalledTimes(1);
    });

    it("fences polling reads issued both before and during a successful write", async () => {
      vi.useFakeTimers();
      const pairing = { code: "123456", expires_at: Math.floor(Date.now() / 1000) + 600 };
      await open(saved({ pairing }));
      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      await act(async () => dialog().querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.enable")}"]`)!.click());
      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      expect(calls.status).toHaveLength(3);
      await act(async () => calls.update[0].resolve(saved({ enabled: false })));
      await act(async () => calls.status[2].resolve(saved({ pairing })));
      await act(async () => calls.status[1].resolve(saved({ pairing })));
      expect(dialog().querySelector(`[role="switch"][aria-label="${t("settings-feishubot.enable")}"]`)?.getAttribute("aria-checked")).toBe("false");
      expect(dialog().textContent).not.toContain(pairing.code);
    });

    it("accepts the useful post-bind response when the newer recheck fails", async () => {
      await boundWithPendingStatus();
      await act(async () => calls.status[2].reject!(new Error("Synthetic recheck failure")));
      expect(dialog().querySelector('[role="alert"]')?.textContent).toContain("Synthetic recheck failure");
      expect(recheckButton()).not.toBeNull();
      await act(async () => calls.status[1].resolve(stale()));
      expect(dialog().textContent).toContain("Earlier owner");
      expect(dialog().textContent).not.toContain(t("settings-feishubot.create_start"));
      expect(dialog().querySelector('[role="alert"]')).toBeNull();
      expect(connectorLogin).not.toHaveBeenCalled();
    });
  });

  it("keeps the create step and does not authorize when the bot status read fails after binding", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark", status: "not_signed_in", capabilities: ["login"] };
    connectorList.value = [larkcli];
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ app_id: "", has_secret: false, connected: false })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
    bindChannel.mockResolvedValueOnce({ done: true, account: "Example owner" });
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    const start = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("settings-feishubot.create_start"))!;
    await act(async () => start.click());
    await act(async () => calls.status[1].reject!(new Error("Synthetic bot status failure")));
    expect(dialog.textContent).toContain(t("settings-feishubot.create_start"));
    expect(dialog.textContent).not.toContain(t("settings-feishubot.paired"));
    expect(connectorLogin).not.toHaveBeenCalled();
    expect(bindChannel).toHaveBeenCalledTimes(1);
  });

  it("opens the permission URL when lark-cli scopes are missing", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const larkcli: HubConnector = {
      id: "larkcli",
      name: "Lark via lark-cli",
      status: "no_permission",
      missing_scopes: ["im:chat:read"],
      guide_url: "https://open.feishu.cn/permissions",
    };
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

    const permission = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.read_open_platform")))!;
    await act(async () => permission.click());
    expect(open).toHaveBeenCalledWith("https://open.feishu.cn/permissions", "_blank", "noopener,noreferrer");
    open.mockRestore();
  });

  it("ignores unsafe guide_url and falls back to safe https permission_url, or hides button when both are unsafe", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    try {
      const larkcli: HubConnector = {
        id: "larkcli",
        name: "Lark via lark-cli",
        status: "no_permission",
        missing_scopes: ["im:chat:read"],
        guide_url: "https:/admin",
        permission_url: "https://open.feishu.cn/app/permissions",
      };
      act(() => root.render(<FeishuBot larkcli={larkcli} />));
      await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));
      await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

      const permission = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes(t("settings-feishubot.read_open_platform")))!;
      await act(async () => permission.click());
      expect(open).toHaveBeenCalledWith("https://open.feishu.cn/app/permissions", "_blank", "noopener,noreferrer");

      act(() => root.render(<FeishuBot larkcli={{ ...larkcli, guide_url: "javascript:alert(1)", permission_url: "https://user:pw@evil.example/p" }} />));
      expect([...document.querySelectorAll<HTMLButtonElement>("button")].some((button) => button.textContent?.includes(t("settings-feishubot.read_open_platform")))).toBe(false);
    } finally {
      open.mockRestore();
    }
  });

  it("lists lark-cli profiles and saves the selected choice", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "ok", account: "me" };
    connectorList.value = [larkcli];
    await open(saved({
      larkcli: {
        profile: "example",
        app_id: "cli_example",
        same_app: true,
        mode: "chosen",
        choice: "example",
        profiles: [
          { name: "example", app_id: "cli_example", user: "Example User", token_status: "valid" },
          { name: "other", app_id: "cli_other", user: "", token_status: "signed_out" },
        ],
      },
    }));

    // The profile in use is shown under the account; the select opens behind "Change".
    expect(document.body.textContent).toContain(t("settings-feishubot.larkcli_profile_using", { name: "example" }));
    expect(document.querySelector(`select[aria-label="${t("settings-feishubot.larkcli_profile_title")}"]`)).toBeNull();
    const change = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("settings-feishubot.larkcli_profile_change"))!;
    await act(async () => change.click());
    const select = document.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-feishubot.larkcli_profile_title")}"]`)!;
    expect([...select.options].map((option) => option.textContent)).toEqual([
      t("settings-feishubot.larkcli_profile_auto"),
      "example · Example User · cli_example",
      "other · " + t("settings-feishubot.larkcli_profile_not_logged_in") + " · cli_other",
    ]);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(select, "other");
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(calls.update[0].body).toEqual({ larkcli_profile: "other" });
    await act(async () => calls.update[0].resolve(saved({ larkcli: { ...saved().larkcli!, mode: "chosen", choice: "other", profiles: [] } })));
    await act(async () => calls.status[1].resolve(saved({ larkcli: { ...saved().larkcli!, mode: "chosen", choice: "other", profiles: [] } })));
  });

  it("shows the profile manager.json fixes, with no way to change it", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "ok", account: "me" };
    connectorList.value = [larkcli];
    await open(saved({
      larkcli: {
        profile: "fixed",
        app_id: "cli_fixed",
        same_app: true,
        mode: "config",
        choice: "",
        profiles: [{ name: "fixed", app_id: "cli_fixed", user: "Example User", token_status: "valid" }],
      },
    }));
    expect(document.body.textContent).toContain(t("settings-feishubot.larkcli_profile_using", { name: t("settings-feishubot.larkcli_profile_fixed", { name: "fixed" }) }));
    expect([...document.querySelectorAll("button")].some((b) => b.textContent === t("settings-feishubot.larkcli_profile_change"))).toBe(false);
    expect(document.querySelector(`select[aria-label="${t("settings-feishubot.larkcli_profile_title")}"]`)).toBeNull();
  });

  it("hides the permission button without a link and re-reads the connector on recheck", async () => {
    const larkcli: HubConnector = { id: "larkcli", name: "Lark via lark-cli", status: "no_permission", missing_scopes: ["im:chat:read"] };
    connectorList.value = [{ ...larkcli, status: "ok" }];
    act(() => root.render(<FeishuBot larkcli={larkcli} />));
    await act(async () => calls.status[0].resolve(saved({ owner: { name: "owner", paired_at: 1 } })));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

    const buttons = () => [...document.querySelectorAll<HTMLButtonElement>("button")];
    expect(buttons().some((button) => button.textContent?.includes(t("settings-feishubot.read_open_platform")))).toBe(false);
    const recheck = buttons().find((button) => button.textContent?.includes(t("settings-feishubot.recheck")) && !button.getAttribute("aria-label"))!;
    await act(async () => recheck.click());
    expect(connectorList.calls).toBe(1);
  });

  it("leaves the bot dialog unchanged when lark-cli is absent", async () => {
    act(() => root.render(<FeishuBot larkcli={null} />));
    await act(async () => calls.status[0].resolve(saved()));
    await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());

    expect(host.textContent).toContain(t("settings-feishubot.footer"));
    expect(host.textContent).not.toContain(t("settings-feishubot.read_section"));
    expect(host.textContent).not.toContain(t("settings-feishubot.larkcli_dialog_footer"));
  });

  it("fills the saved App ID into a dialog opened by URL before the status arrived", async () => {
    act(() => root.render(<FeishuBot larkcli={null} onRouteClose={() => {}} />));
    await advanced();
    expect(input("App ID").value).toBe("");
    await act(async () => calls.status[0].resolve(saved()));
    expect(input("App ID").value).toBe("cli_saved");
  });

  it("keeps a typed App ID when a feature switch is flipped", async () => {
    await open(saved());
    await advanced();
    type("App ID", "cli_typed");
    // The enable switch comes first, then one switch per feature.
    await act(async () => switches()[2].click());
    expect(calls.update).toHaveLength(1);
    expect(calls.update[0].body).toHaveProperty("features");
    await act(async () => calls.update[0].resolve(saved({ features: { ...saved().features, cards: false } })));
    expect(input("App ID").value).toBe("cli_typed");
  });

  it("sends only the switch flipped, not the whole set this page last saw (#74)", async () => {
    await open(saved());
    // Enable, then main, cards, all cards, pushes: the fifth is "Push to Lark".
    await act(async () => switches()[4].click());
    expect(calls.update).toHaveLength(1);
    expect(calls.update[0].body).toEqual({ features: { pushes: true } });
    // The board was turned on elsewhere meanwhile; the hub's answer carries it, and the next switch leaves it alone.
    await act(async () => calls.update[0].resolve(saved({ features: { ...saved().features, pushes: true, board: true } })));
    await act(async () => switches()[5].click());
    expect(calls.update[1].body).toEqual({ features: { sync: true } });
  });

  it("names both of the card switches when turning questions and approvals off", async () => {
    await open(saved());
    await act(async () => switches()[2].click());
    expect(calls.update[0].body).toEqual({ features: { cards: false, all_cards: false } });
  });

  it("greys out only conclusions while task progress is off: the hub then sends neither progress nor conclusions", async () => {
    const named = (key: string) => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t(key)}"]`)!;
    const onlyConclusions = () => named("settings-feishubot.feature_only_conclusions_title");
    await open(saved({ features: { ...saved().features, reports: false, only_conclusions: false } }));
    expect(onlyConclusions().disabled).toBe(true);
    await act(async () => named("settings-feishubot.feature_reports_title").click());
    expect(calls.update[0].body).toEqual({ features: { reports: true } });
    await act(async () => calls.update[0].resolve(saved({ features: { ...saved().features, reports: true, only_conclusions: false } })));
    expect(onlyConclusions().disabled).toBe(false);
    await act(async () => onlyConclusions().click());
    expect(calls.update[1].body).toEqual({ features: { only_conclusions: true } });
  });

  it("hides execution steps for a hub from before it", async () => {
    await open(saved());
    expect(document.body.querySelector(`[role="switch"][aria-label="${t("settings-feishubot.feature_report_steps_title")}"]`)).toBeNull();
  });

  it("execution steps: off by default, and only conclusions waits for it", async () => {
    const named = (key: string) => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t(key)}"]`);
    const steps = () => named("settings-feishubot.feature_report_steps_title");
    const onlyConclusions = () => named("settings-feishubot.feature_only_conclusions_title")!;
    await open(saved({ features: { ...saved().features, report_steps: false, only_conclusions: false } }));
    expect(steps()!.getAttribute("aria-checked")).toBe("false");
    expect(onlyConclusions().disabled).toBe(true);
    await act(async () => steps()!.click());
    expect(calls.update[0].body).toEqual({ features: { report_steps: true } });
    await act(async () => calls.update[0].resolve(saved({ features: { ...saved().features, report_steps: true, only_conclusions: false } })));
    expect(onlyConclusions().disabled).toBe(false);
  });

  it("hides answer as a new card for a hub from before it", async () => {
    await open(saved());
    expect(document.body.querySelector(`[role="switch"][aria-label="${t("settings-feishubot.feature_answer_new_card_title")}"]`)).toBeNull();
  });

  it("switches answer as a new card on its own, off by default", async () => {
    const newCard = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_answer_new_card_title")}"]`)!;
    await open(saved({ features: { ...saved().features, answer_new_card: false } }));
    expect(newCard().getAttribute("aria-checked")).toBe("false");
    await act(async () => newCard().click());
    expect(calls.update[0].body).toEqual({ features: { answer_new_card: true } });
  });

  it("hides reading a group with /read for a hub from before it", async () => {
    await open(saved());
    expect(document.body.querySelector(`[role="switch"][aria-label="${t("settings-feishubot.feature_read_group_title")}"]`)).toBeNull();
  });

  it("switches reading a group with /read on its own, off by default", async () => {
    const read = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_read_group_title")}"]`)!;
    await open(saved({ features: { ...saved().features, read_group: false } }));
    expect(read().getAttribute("aria-checked")).toBe("false");
    await act(async () => read().click());
    expect(calls.update[0].body).toEqual({ features: { read_group: true } });
  });

  it("hides answering /read in the group directly for a hub from before it", async () => {
    await open(saved({ features: { ...saved().features, read_group: true } }));
    expect(document.body.querySelector(`[role="switch"][aria-label="${t("settings-feishubot.feature_read_group_direct_title")}"]`)).toBeNull();
  });

  it("dims answering /read in the group directly while /read is off", async () => {
    const direct = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_read_group_direct_title")}"]`)!;
    await open(saved({ features: { ...saved().features, read_group: false, read_group_direct: false } }));
    expect(direct().getAttribute("aria-checked")).toBe("false");
    expect(direct().disabled).toBe(true);
  });

  it("switches answering /read in the group directly on its own, off by default", async () => {
    const direct = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_read_group_direct_title")}"]`)!;
    await open(saved({ features: { ...saved().features, read_group: true, read_group_direct: false } }));
    expect(direct().getAttribute("aria-checked")).toBe("false");
    expect(direct().disabled).toBe(false);
    await act(async () => direct().click());
    expect(calls.update[0].body).toEqual({ features: { read_group_direct: true } });
  });

  it("switches the workbench on its own, off by default", async () => {
    const workbench = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_workbench_title")}"]`)!;
    await open(saved({ features: { ...saved().features, workbench: false } }));
    expect(workbench().getAttribute("aria-checked")).toBe("false");
    await act(async () => workbench().click());
    expect(calls.update[0].body).toEqual({ features: { workbench: true } });
  });

  it("sends every switch to a hub from before 0.1.12, which replaces the whole set", async () => {
    const { reports: _reports, board: _board, ...old } = saved().features;
    await open(saved({ features: old }));
    await act(async () => switches()[4].click());
    expect(calls.update[0].body).toEqual({ features: { ...old, pushes: true } });
  });

  it("keeps a typed App ID when the bot is switched off", async () => {
    await open(saved());
    await advanced();
    type("App ID", "cli_typed");
    await act(async () => switches()[0].click());
    expect(calls.update[0].body).toEqual({ enabled: false });
    await act(async () => calls.update[0].resolve(saved({ enabled: false })));
    expect(input("App ID").value).toBe("cli_typed");
  });

  it("keeps a typed App Secret while the pairing poll answers", async () => {
    vi.useFakeTimers();
    await open(saved({ pairing: { code: "123456", expires_at: Math.floor(Date.now() / 1000) + 600 } }));
    await advanced();
    type("App Secret", "typed-secret");
    await act(async () => void vi.advanceTimersByTime(3000));
    expect(calls.status).toHaveLength(2);
    await act(async () => calls.status[1].resolve(saved({ pairing: { code: "123456", expires_at: Math.floor(Date.now() / 1000) + 597 } })));
    expect(input("App Secret").value).toBe("typed-secret");
  });

  it("stops looking for the pairing while the tab is hidden and looks once right away on coming back", async () => {
    vi.useFakeTimers();
    await open(saved({ pairing: { code: "123456", expires_at: Math.floor(Date.now() / 1000) + 600 } }));
    await show("hidden");
    await act(async () => void vi.advanceTimersByTime(9000));
    expect(calls.status).toHaveLength(1); // only the row's first read
    await show("visible");
    expect(calls.status).toHaveLength(2);
    await act(async () => calls.status[1].resolve(saved({ owner: { name: "owner-1", paired_at: Math.floor(Date.now() / 1000) } })));
    expect(document.body.textContent).toContain("owner-1");
    await act(async () => void vi.advanceTimersByTime(9000)); // paired: nothing left to look for
    expect(calls.status).toHaveLength(2);
  });

  it("takes the hub's answer after verify and save", async () => {
    await open(saved());
    await advanced();
    type("App ID", " cli_new ");
    type("App Secret", "new-secret");
    const save = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("settings-feishubot.verify_save"));
    await act(async () => save!.click());
    expect(calls.update[0].body).toEqual({ app_id: "cli_new", domain: "feishu", app_secret: "new-secret" });
    await act(async () => calls.update[0].resolve(saved({ app_id: "cli_new" })));
    expect(input("App ID").value).toBe("cli_new");
    expect(input("App Secret").value).toBe("");
  });

  it("sets how long an unanswered card waits before the nudge, an hour when the hub says none", async () => {
    await open(saved());
    const select = () => document.body.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-feishubot.nudge_title")}"]`)!;
    expect(select().value).toBe("60");
    expect(document.body.querySelector(`select[aria-label="${t("settings-feishubot.source_report_delay_title")}"]`)).toBeNull();
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(select(), "0");
      select().dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(calls.update[0].body).toEqual({ nudge_after: 0 });
    await act(async () => calls.update[0].resolve(saved({ nudge_after: 0 })));
    expect(select().value).toBe("0");
  });

  it("turns on the complete final answer and can turn it off again", async () => {
    await open(saved({ source_report_full_reply: false }));
    const toggle = () => switches().find((b) => b.getAttribute("aria-label") === t("settings-feishubot.source_report_full_title"))!;
    expect(toggle().getAttribute("aria-checked")).toBe("false");
    for (const on of [true, false]) {
      await act(async () => toggle().click());
      const pending = calls.update.at(-1)!;
      expect(pending.body).toEqual({ source_report_full_reply: on });
      await act(async () => pending.resolve(saved({ source_report_full_reply: on })));
      expect(toggle().getAttribute("aria-checked")).toBe(String(on));
    }
  });

  it("sets the original-group reply to immediate and restores the ten-minute preview", async () => {
    await open(saved({ source_report_delay_minutes: 10 }));
    const select = document.body.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-feishubot.source_report_delay_title")}"]`)!;
    expect(select.value).toBe("10");
    for (const minutes of [0, 10]) {
      await act(async () => {
        select.value = String(minutes);
        select.dispatchEvent(new Event("change", { bubbles: true }));
      });
      const pending = calls.update.at(-1)!;
      expect(pending.body).toEqual({ source_report_delay_minutes: minutes });
      expect(select.disabled).toBe(true);
      await act(async () => pending.resolve(saved({ source_report_delay_minutes: minutes })));
      expect(select.value).toBe(String(minutes));
    }
  });

  it("with no app, starts with scanning to create one, the credentials folded under Advanced (#5818)", async () => {
    const noApp = saved({ app_id: "", has_secret: false, connected: false });
    await open(noApp);
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    const start = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("settings-feishubot.create_start"));
    expect(start).toBeDefined();
    expect(dialog.textContent).toContain(t("settings-feishubot.create_footer"));
    expect(input("App ID")).toBeNull();
    expect(advancedToggle()?.textContent).toBe(t("settings-feishubot.advanced_have_app"));

    // The press is the channel's bind step: the hub begins the app registration and answers with a QR code.
    bindChannel.mockResolvedValueOnce({ step: "register", kind: "qr", url: "https://accounts.example.com/verify", interval: 5, qr: { size: 2, rows: ["10", "01"] } });
    await act(async () => start!.click());
    expect(bindChannel).toHaveBeenCalledWith("feishu", "", {});
    expect(dialog.querySelector(`svg[role=img][aria-label="${t("settings-channels.qr_label")}"]`)).not.toBeNull();
  });

  it("keeps manager.json's credentials: no create step when the app comes from the config", async () => {
    await open(saved({ app_id: "", has_secret: false, from_config: true }));
    expect(document.body.textContent).not.toContain(t("settings-feishubot.create_start"));
  });

  it("fills the form from a switch's answer when nothing was typed", async () => {
    await open(saved());
    await advanced();
    await act(async () => switches()[0].click());
    await act(async () => calls.update[0].resolve(saved({ enabled: false, app_id: "cli_elsewhere" })));
    expect(input("App ID").value).toBe("cli_elsewhere");
  });

  // AC5: the native topic process control is visible, on by default, and reversible through the bot-settings API.
  it("shows native topic process on by default and turns it off and on", async () => {
    const features = { ...saved().features, topic_cot: true };
    const nativeProcess = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_topic_cot_title")}"]`);
    await open(saved({ features }));
    expect(nativeProcess()).not.toBeNull();
    expect(nativeProcess()!.getAttribute("aria-checked")).toBe("true");

    await act(async () => nativeProcess()!.click());
    expect(calls.update[0].body).toEqual({ features: { topic_cot: false } });
    await act(async () => calls.update[0].resolve(saved({ features: { ...features, topic_cot: false } })));
    expect(nativeProcess()!.getAttribute("aria-checked")).toBe("false");

    await act(async () => nativeProcess()!.click());
    expect(calls.update[1].body).toEqual({ features: { topic_cot: true } });
    await act(async () => calls.update[1].resolve(saved({ features })));
    expect(nativeProcess()!.getAttribute("aria-checked")).toBe("true");
  });

  // The private-chat native process and the unified answer switches show only when the hub sends their keys;
  // "cot_answer" waits disabled while neither a DM nor a topic native agent message is on.
  it("hides the DM native process and the unified answer switches when the hub leaves the keys out", async () => {
    const dmCot = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_dm_cot_title")}"]`);
    const cotAnswer = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_cot_answer_title")}"]`);
    await open(saved());
    expect(dmCot()).toBeNull();
    expect(cotAnswer()).toBeNull();
  });

  it("toggles the DM native process and enables the unified answer switch once a DM or topic native process is on", async () => {
    const dmCot = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_dm_cot_title")}"]`);
    const cotAnswer = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_cot_answer_title")}"]`);
    const features = { ...saved().features, topic_cot: true, dm_cot: false, cot_answer: false };
    await open(saved({ features }));
    expect(dmCot()!.getAttribute("aria-checked")).toBe("false");
    expect(cotAnswer()!.disabled).toBe(false);

    await act(async () => dmCot()!.click());
    expect(calls.update[0].body).toEqual({ features: { dm_cot: true } });
    await act(async () => calls.update[0].resolve(saved({ features: { ...features, dm_cot: true } })));
    expect(dmCot()!.getAttribute("aria-checked")).toBe("true");
  });

  it("dims cot_answer while neither a DM nor a topic native agent message is on", async () => {
    const cotAnswer = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.feature_cot_answer_title")}"]`);
    await open(saved({ features: { ...saved().features, topic_cot: false, dm_cot: false, cot_answer: true } }));
    expect(cotAnswer()!.disabled).toBe(true);
  });
});

describe("topic group policies", () => {
  const owner = { name: "Example owner", paired_at: 1 };
  const clickText = (text: string) => act(async () => {
    const button = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === text);
    expect(button).toBeDefined(); button!.click();
  });
  const strictSwitch = (name: string) => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.topic_mention_for", { name })}"]`)!;
  const homeSelect = () => document.body.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-feishubot.topic_home")}"]`)!;
  const setLabelled = (label: string, value: string) => act(() => {
    const field = [...document.body.querySelectorAll("label")].find((l) => l.textContent === label)!.querySelector("input")!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, value);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
  it("shows a saved strict group and no home again after refresh", async () => {
    const status = saved({ owner, topic_chats: [{ chat_id: "oc_example", name: "Example group", require_owner_mention: true }] });
    await open(status);
    expect(strictSwitch("Example group").getAttribute("aria-checked")).toBe("true");
    expect(homeSelect().value).toBe("");
    await act(async () => document.body.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-feishubot.recheck")}"]`)!.click());
    await act(async () => calls.status.at(-1)!.resolve(status));
    expect(strictSwitch("Example group").getAttribute("aria-checked")).toBe("true");
  });
  it("waits for the receipt, restores failure, then reverses the saved policy", async () => {
    const group = { chat_id: "oc_example", name: "Example group", require_owner_mention: true, home: true };
    await open(saved({ owner, topic_chats: [group] }));
    await act(async () => strictSwitch("Example group").click());
    expect(calls.update[0].body).toEqual({ topic_chats: [{ ...group, require_owner_mention: false }] });
    expect(strictSwitch("Example group").disabled).toBe(true);
    expect(strictSwitch("Example group").getAttribute("aria-checked")).toBe("true");
    await act(async () => calls.update[0].reject!(new Error("Synthetic save failure")));
    expect(document.body.querySelector('[role="alert"]')?.textContent).toContain("Synthetic save failure");
    expect(strictSwitch("Example group").getAttribute("aria-checked")).toBe("true");
    await act(async () => strictSwitch("Example group").click());
    await act(async () => calls.update[1].resolve(saved({ owner, topic_chats: [{ ...group, require_owner_mention: false }] })));
    expect(strictSwitch("Example group").getAttribute("aria-checked")).toBe("false");
  });
  it("adds an isolated strict group and chooses a home only in a separate save", async () => {
    await open(saved({ owner }));
    await clickText(t("settings-feishubot.topic_add"));
    setLabelled(t("settings-feishubot.topic_group_id"), "oc_example");
    setLabelled(t("settings-feishubot.topic_group_name"), "Example group");
    const control = document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.topic_owner_mention")}"]`)!;
    expect(control.getAttribute("aria-checked")).toBe("false");
    await act(async () => control.click());
    await clickText(t("settings-feishubot.topic_add"));
    const group = { chat_id: "oc_example", name: "Example group", require_owner_mention: true };
    expect(calls.update[0].body).toEqual({ topic_chats: [group] });
    await act(async () => calls.update[0].reject!(new Error("Synthetic save failure")));
    expect(document.body.querySelector<HTMLInputElement>("label input")?.value).toBe("oc_example");
    await clickText(t("settings-feishubot.topic_add"));
    await act(async () => calls.update[1].resolve(saved({ owner, topic_chats: [group] })));
    expect(homeSelect().value).toBe("");
    await act(async () => { const home=homeSelect(); home.value = "oc_example"; home.dispatchEvent(new Event("change", { bubbles: true })); });
    expect(calls.update[2].body).toEqual({ topic_chats: [{ ...group, home: true }] });
    await act(async () => calls.update[2].resolve(saved({ owner, topic_chats: [{ ...group, home: true }] })));
    expect(homeSelect().value).toBe("oc_example");
    await act(async () => { const home=homeSelect(); home.value = ""; home.dispatchEvent(new Event("change", { bubbles: true })); });
    expect(calls.update[3].body).toEqual({ topic_chats: [{ ...group, home: false }] });
    await act(async () => calls.update[3].resolve(saved({ owner, topic_chats: [{ ...group, home: false }] })));
    expect(homeSelect().value).toBe("");
  });
  it("saves the to-do mode for one group without changing the home", async () => {
    const group = { chat_id: "oc_example", name: "Example group", home: true, todo_mode: "answer_only" } as const;
    const other = { chat_id: "oc_other", name: "Other group", todo_mode: "always_todo" } as const;
    await open(saved({ owner, topic_chats: [group, other] }));
    const modeSelect = (name: string) =>
      document.body.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-feishubot.topic_todo_for", { name })}"]`)!;
    const choose = async (value: string) => act(async () => {
      const select = modeSelect(group.name);
      select.value = value;
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(modeSelect(group.name).value).toBe("answer_only");
    await choose("main_decides");
    expect(JSON.parse(JSON.stringify(calls.update[0].body))).toEqual({ topic_chats: [
      { chat_id: group.chat_id, name: group.name, todo_mode: "main_decides" },
      { chat_id: other.chat_id, name: other.name },
    ] });
    expect(modeSelect(group.name).disabled).toBe(true);
    await act(async () => calls.update[0].reject!(new Error("Synthetic save failure")));
    expect(modeSelect(group.name).value).toBe("answer_only");
    await choose("main_decides");
    await act(async () => calls.update[1].resolve(saved({ owner, topic_chats: [{ ...group, todo_mode: "main_decides" }, other] })));
    expect(modeSelect(group.name).value).toBe("main_decides");
    expect(modeSelect(other.name).value).toBe("always_todo");
    expect(homeSelect().value).toBe(group.chat_id);
  });
});
