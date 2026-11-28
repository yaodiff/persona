// @vitest-environment jsdom
// #17399: an unavailable source with no retained items leaves no chip or empty-state mention; archives and old hubs stay visible.
import { registerTestMailbox } from "../../test/mailbox-fixture";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((media: string) => ({
    matches: false, media, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} disconnect() {} takeRecords() { return []; } },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});

type Stored = { lark: boolean; mail: boolean } | null;
const hub = vi.hoisted(() => ({
  features: ["modules"] as string[],
  loaded: true,
  stored: undefined as Stored | undefined,
}));

vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: async (path: string) => {
    if (path === "/mail/accounts") return { accounts: [] };
    if (path.startsWith("/inbox?")) return { items: [], ...(hub.stored !== undefined ? { stored_sources: hub.stored } : {}) };
    return {};
  },
}));
vi.mock("../../lib/store", async (original) => {
  const m = await original<typeof import("../../lib/store")>();
  return {
    ...m,
    useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true, features: hub.features })),
  };
});

vi.mock("../../lib/inbox-sources", async (original) => ({
  ...(await original<typeof import("../../lib/inbox-sources")>()),
  useInboxSources: () => null,
}));

import { connectModuleHub, resetModulesForTest } from "../../core/module-host";
import { forgetModuleSwitches } from "../../core/module-switch";
import { initI18n } from "../../i18n";
import { KIND_FILTERS, resetInboxForTests } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { resetManagerChoice, settleRosterDefault } from "../../lib/manager-choice";
import { reset, getState } from "../../lib/store";
import { InboxList } from "./InboxList";

let host: HTMLDivElement;
let root: Root;
const listeners = new Set<() => void>();
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const larkChip = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "Lark");
// KIND_FILTERS labels are captured when the module loads (the zh-Hans test setup), so use that same label.
const mailKindLabel = KIND_FILTERS.find((k) => k.key === "mail")?.label ?? "Mail";
const mailChip = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === mailKindLabel);
const agentChip = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "Agent");
const hint = () => host.querySelector("[data-inbox-empty-hint]")?.textContent;
const sourceKindDivider = () => host.querySelector("[data-source-kind-divider]");

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  resetModulesForTest();
  registerTestMailbox();
  forgetModuleSwitches();
  reset();
  hub.features = ["modules"];
  hub.loaded = true;
  hub.stored = undefined;
  listeners.clear();
  connectModuleHub(() => hub, (listener) => {
    listeners.add(listener);
    return () => listeners.delete(listener);
  });
  resetInboxForTests();
  resetMailForTests();
  resetManagerChoice();
  settleRosterDefault();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  listeners.clear();
  resetInboxForTests();
  resetModulesForTest();
  forgetModuleSwitches();
  localStorage.clear();
});

async function show() {
  Object.assign(getState(), { features: hub.features, loaded: true });
  await act(async () => root.render(<InboxList />));
  await flush();
  const all = host.querySelector<HTMLButtonElement>('[data-bar-choice="all"]');
  if (all) await act(async () => all.click());
  await flush();
}

describe("Inbox source availability", () => {
  it.each([
    {
      name: "offered without stored items",
      features: ["modules", "lark", "module.lark", "module.mail"], stored: { lark: false, mail: false },
      chip: true, mailChip: true, words: "New Lark messages and mail will show up here.",
    },
    {
      name: "offered and stored",
      features: ["modules", "lark", "module.lark", "module.mail"], stored: { lark: true, mail: true },
      chip: true, mailChip: true, words: "New Lark messages and mail will show up here.",
    },
    {
      name: "stored Lark without a capability",
      features: ["modules"], stored: { lark: true, mail: false },
      chip: true, mailChip: false, words: "New Lark messages will show up here.",
    },
    {
      name: "stored mail without a capability",
      features: ["modules"], stored: { lark: false, mail: true },
      chip: false, mailChip: true, words: "New mail will show up here.",
    },
    {
      name: "neither capability nor stored items",
      features: ["modules"], stored: { lark: false, mail: false },
      chip: false, mailChip: false, words: "New items will show up here.",
    },
    {
      name: "missing stored sources is unknown on an old or mixed hub",
      features: ["modules"], stored: undefined,
      chip: true, mailChip: true, words: "New Lark messages and mail will show up here.",
    },
    {
      name: "an explicit merged null is unknown too",
      features: ["modules"], stored: null,
      chip: true, mailChip: true, words: "New Lark messages and mail will show up here.",
    },
  ])("$name", async ({ features, stored, chip, mailChip: mailChipShown, words }) => {
    hub.features = features;
    hub.stored = stored;
    await show();
    expect(Boolean(larkChip())).toBe(chip);
    // #17814: the Mail kind chip follows the same offered/retained decision.
    expect(Boolean(mailChip())).toBe(mailChipShown);
    // The other kind chips stay: hiding Mail never removes the whole kind row.
    expect(agentChip()).toBeTruthy();
    // No source chip precedes the kind filters in these fixtures, so a divider must not dangle before @Me.
    expect(Boolean(sourceKindDivider())).toBe(chip);
    expect(hint()).toBe(words);
  });

  it("clears a persisted Mail kind selection when its chip becomes unavailable", async () => {
    localStorage.setItem("inbox.kind", "mail");
    hub.stored = { lark: false, mail: false };
    await show();
    expect(mailChip()).toBeUndefined();
    expect(sourceKindDivider()).toBeNull();
    expect(localStorage.getItem("inbox.kind")).toBeNull();
    expect(host.querySelector("[data-inbox-filtered-empty]")).toBeNull();
    expect(hint()).toBe("New items will show up here.");
  });

  it("keeps a persisted Mail kind selection while mail stays unknown", async () => {
    localStorage.setItem("inbox.kind", "mail");
    hub.stored = undefined;
    await show();
    expect(mailChip()).toBeTruthy();
    expect(localStorage.getItem("inbox.kind")).toBe("mail");
  });

  it("clears a persisted Lark selection when its chip becomes unavailable", async () => {
    localStorage.setItem("inbox.source", "feishu");
    hub.stored = { lark: false, mail: false };
    await show();
    expect(larkChip()).toBeUndefined();
    expect(sourceKindDivider()).toBeNull();
    expect(localStorage.getItem("inbox.source")).toBeNull();
    expect(host.querySelector("[data-inbox-filtered-empty]")).toBeNull();
    expect(hint()).toBe("New items will show up here.");
  });
});
