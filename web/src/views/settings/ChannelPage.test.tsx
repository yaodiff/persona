// @vitest-environment jsdom
// A channel page's changes go one at a time: while one is with the hub the page's other switches, its target row, unbind
// and the test message wait, and an answer older than a later request (a refresh, another change) is not put back over
// it; the page reads the detail again instead, since that later read may predate the change. The hub is faked: every
// read and change waits until the test answers it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { ApiError, setPairedToken } from "../../lib/api";
import type { ChannelDetail, ChannelTestResult } from "../../lib/channels";

type Asked = {
  kind: "get" | "put" | "unbind" | "test";
  body?: unknown;
  answer: (d: ChannelDetail | ChannelTestResult) => void;
  reject: (err: unknown) => void;
};
const hub = vi.hoisted(() => ({ asked: [] as Asked[] }));

vi.mock("../../lib/channels", async (original) => ({
  ...(await original<typeof import("../../lib/channels")>()),
  channelDetail: () => new Promise((resolve, reject) => hub.asked.push({ kind: "get", answer: resolve, reject })),
  updateChannel: (_id: string, body: unknown) => new Promise((resolve, reject) => hub.asked.push({ kind: "put", body, answer: resolve, reject })),
  unbindChannel: () => new Promise((resolve, reject) => hub.asked.push({ kind: "unbind", answer: resolve, reject })),
  testChannel: () => new Promise((resolve, reject) => hub.asked.push({ kind: "test", answer: resolve, reject })),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { toast } from "../../ui/atoms";
import { ChannelPage } from "./Channels";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

/** The hub's detail with features a and b on or off as given. */
const detail = (a: boolean, b: boolean): ChannelDetail => ({
  id: "demo", name: "Demo", kind: "im", source: "builtin", installed: true, enabled: true, state: "connected",
  account: { name: "owner" }, capabilities: { push: true }, bind: "none", target: { kind: "main" },
  features: [{ id: "a", title: "Feature A", on: a }, { id: "b", title: "Feature B", on: b }],
  waiting: { answers: 0, asks: 0 },
});

let host: HTMLDivElement;
let root: Root;
let onSettings: ReturnType<typeof vi.fn<(url: string) => void>>;
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const answer = async (a: Asked | undefined, d: ChannelDetail | ChannelTestResult) => {
  expect(a).toBeTruthy();
  await act(async () => a!.answer(d));
  await settle();
};
const fail = async (a: Asked | undefined, err: unknown) => {
  expect(a).toBeTruthy();
  await act(async () => a!.reject(err));
  await settle();
};
const sw = (label: string) => host.querySelector<HTMLButtonElement>(`button[role="switch"][aria-label="${label}"]`)!;
const flip = async (label: string) => {
  await act(async () => sw(label).click());
  await settle();
};
const byText = (text: string) =>
  [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(text))!;
const puts = () => hub.asked.filter((a) => a.kind === "put");

beforeEach(async () => {
  setPairedToken("");
  hub.asked = [];
  vi.mocked(toast).mockReset();
  onSettings = vi.fn<(url: string) => void>();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(<ChannelPage id="demo" onBack={() => {}} onSettings={onSettings} />));
  await act(async () => vi.dynamicImportSettled());
  await answer(hub.asked.shift(), detail(false, false));
  hub.asked = [];
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

it("offers a test message only when the connected channel can send without a reply", async () => {
  expect(byText(t("settings-channels.send_test"))).toBeDefined();

  const refresh = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-channels.refresh")}"]`)!;
  await act(async () => refresh.click());
  await answer(hub.asked.shift(), { ...detail(false, false), source: "plugin", capabilities: { format: "plain" } });

  expect(byText(t("settings-channels.send_test"))).toBeUndefined();
  expect(byText(t("settings-channels.check_connection"))).toBeUndefined();
});

it("offers Check connection for reply-only channels and displays the returned note", async () => {
  const refresh = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-channels.refresh")}"]`)!;
  await act(async () => refresh.click());
  await answer(hub.asked.shift(), {
    ...detail(false, false),
    source: "plugin",
    kind: "mention",
    capabilities: { format: "plain", reply_only: true },
  });

  expect(byText(t("settings-channels.send_test"))).toBeUndefined();
  const check = byText(t("settings-channels.check_connection"));
  expect(check).toBeDefined();
  expect(host.textContent).toContain(t("settings-channels.check_connection_footer"));

  await act(async () => check.click());
  await settle();
  const req = hub.asked.shift();
  expect(req?.kind).toBe("test");
  const note = "Demo is connected and ready. It only replies to incoming messages, so no test message was sent.";
  await answer(req, { ok: true, reply_only: true, note });

  expect(toast).toHaveBeenCalledWith(note, "ok");
  expect(host.querySelector('[role="status"]')?.textContent).toBe(note);
});

it("displays non-502 cannot_send error messages cleanly without gateway wording", async () => {
  const sendTest = byText(t("settings-channels.send_test"));
  await act(async () => sendTest.click());
  await settle();
  const req = hub.asked.shift();
  expect(req?.kind).toBe("test");
  const explanation = "This channel only replies to incoming messages.";
  await fail(req, new ApiError(409, explanation, { code: "cannot_send", error: explanation }, true, undefined, "/channels/demo/test"));

  expect(toast).toHaveBeenCalledWith(explanation, "error");
  expect(host.querySelector('[role="alert"]')?.textContent).toBe(explanation);
  expect(host.textContent).not.toContain("502");
});

describe("ChannelPage target override hint", () => {
  const refreshWith = async (next: ChannelDetail) => {
    const refresh = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-channels.refresh")}"]`)!;
    await act(async () => refresh.click());
    await answer(hub.asked.shift(), next);
  };

  it("stays hidden when target_override is absent (inactive or older hub)", () => {
    expect(host.textContent).not.toContain(t("settings-channels.target_override_topics"));
    expect(host.textContent).not.toContain(t("settings-channels.target_override_threads"));
    expect(host.textContent).not.toContain(t("settings-channels.target_override_generic"));
    expect(byText(t("settings-channels.target_override_extension_settings"))).toBeUndefined();
    expect(byText(t("settings-channels.target_override_settings"))).toBeUndefined();
  });

  it("explains where groups, topics and direct messages go in groups_and_topics mode", async () => {
    await refreshWith({ ...detail(false, false), target_override: { mode: "groups_and_topics" } });
    expect(host.textContent).toContain(t("settings-channels.target_override_groups_and_topics"));
    expect(host.textContent).not.toContain(t("settings-channels.target_override_topics"));
  });

  it("shows the group topic routing hint with an extension settings shortcut when active", async () => {
    await refreshWith({
      ...detail(false, false),
      source: "plugin",
      plugin: "topic-ext",
      settings_url: "settings/plugins/topic-ext",
      target_override: { mode: "topics" },
    });

    expect(host.textContent).toContain(t("settings-channels.target_override_topics"));
    const link = byText(t("settings-channels.target_override_extension_settings"));
    expect(link).toBeDefined();
    await act(async () => link.click());
    expect(onSettings).toHaveBeenCalledWith("settings/plugins/topic-ext");
  });

  it("shows the thread routing hint without a settings link when settings_url is absent", async () => {
    await refreshWith({
      ...detail(false, false),
      source: "plugin",
      settings_url: undefined,
      target_override: { mode: "threads" },
    });

    expect(host.textContent).toContain(t("settings-channels.target_override_threads"));
    expect(byText(t("settings-channels.target_override_extension_settings"))).toBeUndefined();
    expect(byText(t("settings-channels.target_override_settings"))).toBeUndefined();
  });

  it("uses the general settings label for built-in channels and falls back gracefully on unknown modes", async () => {
    await refreshWith({
      ...detail(false, false),
      source: "builtin",
      settings_url: "settings/connectors/feishu-bot",
      target_override: { mode: "future_mode" },
    });

    expect(host.textContent).toContain(t("settings-channels.target_override_generic"));
    const link = byText(t("settings-channels.target_override_settings"));
    expect(link).toBeDefined();
    await act(async () => link.click());
    expect(onSettings).toHaveBeenCalledWith("settings/connectors/feishu-bot");
  });
});

describe("ChannelPage saves", () => {
  it("keeps the other switches off while a change is with the hub", async () => {
    await flip("Feature A");
    expect(puts()).toHaveLength(1);
    expect(sw("Feature A").disabled).toBe(true);
    expect(sw("Feature B").disabled).toBe(true);
    // a click on the waiting switch sends nothing more
    await flip("Feature B");
    expect(puts()).toHaveLength(1);

    await answer(hub.asked.shift(), detail(true, false));
    expect(sw("Feature B").disabled).toBe(false);
    expect(sw("Feature A").getAttribute("aria-checked")).toBe("true");
  });

  it("waits for the write before reading a refresh requested during the change", async () => {
    await flip("Feature A");
    const change = hub.asked.shift();
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-channels.refresh")}"]`)!.click());
    expect(hub.asked).toHaveLength(0);
    await answer(change, detail(true, false));
    expect(sw("Feature A").getAttribute("aria-checked")).toBe("true");
    const again = hub.asked.shift();
    expect(again?.kind).toBe("get");
    await answer(again, detail(true, true));
    expect(sw("Feature A").getAttribute("aria-checked")).toBe("true");
    expect(sw("Feature B").getAttribute("aria-checked")).toBe("true");
    expect(sw("Feature A").disabled).toBe(false);
    expect(hub.asked).toHaveLength(0);
  });

  it("does not read again when the change's answer is the newest", async () => {
    await flip("Feature A");
    await answer(hub.asked.shift(), detail(true, false));
    expect(hub.asked).toHaveLength(0);
    expect(sw("Feature A").getAttribute("aria-checked")).toBe("true");
  });

  it("keeps unbind and the test message waiting while a change is with the hub", async () => {
    await flip("Feature A");
    const unbind = byText(t("settings-channels.unbind"));
    const test = byText(t("settings-channels.send_test"));
    expect(unbind.disabled).toBe(true);
    expect(test.disabled).toBe(true);
    await act(async () => test.click());
    await settle();
    expect(hub.asked.map((a) => a.kind)).toEqual(["put"]);
    await answer(hub.asked.shift(), detail(true, false));
    expect(byText(t("settings-channels.unbind")).disabled).toBe(false);
    expect(byText(t("settings-channels.send_test")).disabled).toBe(false);
  });

  it("does not unbind while a change is with the hub, even from an open dialog", async () => {
    await act(async () => byText(t("settings-channels.unbind")).click());
    await settle();
    const confirm = () => [...document.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.includes(t("settings-channels.unbind"))).pop()!;
    await flip("Feature A");
    expect(confirm().disabled).toBe(true);
    await act(async () => confirm().click());
    await settle();
    expect(hub.asked.map((a) => a.kind)).toEqual(["put"]);
    await answer(hub.asked.shift(), detail(true, false));

    // once the change is done the unbind goes out, and the switches wait for it
    await act(async () => confirm().click());
    await settle();
    expect(hub.asked.map((a) => a.kind)).toEqual(["unbind"]);
    expect(sw("Feature A").disabled).toBe(true);
    await flip("Feature B");
    expect(hub.asked.map((a) => a.kind)).toEqual(["unbind"]);
    await answer(hub.asked.shift(), { ...detail(true, false), state: "not_connected", account: undefined });
    expect(sw("Feature A").disabled).toBe(false);
  });

  it("closes open unbind and target picker dialogs when switching to another channel", async () => {
    // Open the unbind dialog on demo, then switch to another channel while mounted.
    await act(async () => byText(t("settings-channels.unbind")).click());
    await settle();
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();

    await act(async () => root.render(<ChannelPage id="other" onBack={() => {}} onSettings={onSettings} />));
    await answer(hub.asked.shift(), { ...detail(false, false), id: "other", name: "Other" });
    expect(document.querySelector('[role="dialog"]')).toBeNull();

    // Open the target picker on other, then switch back to demo while mounted.
    const targetRow = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      b.textContent?.includes(t("settings-channels.target_row")),
    )!;
    await act(async () => targetRow.click());
    await settle();
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();

    await act(async () => root.render(<ChannelPage id="demo" onBack={() => {}} onSettings={onSettings} />));
    await answer(hub.asked.shift(), detail(false, false));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });
});
