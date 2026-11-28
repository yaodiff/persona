// @vitest-environment jsdom
// A mail item on a hub that turned mail off (#15995, the owner's rule in #13430: switching a module off removes its
// new activity and its entry points; what the user already has stays readable). The hub's mail routes are gone, so
// the body asks none of them: it shows the text the hub kept with the item and says why the rest is not here; the
// controls that would write to the mailbox are left out and the Inbox's own stay. With the module on, with a hub
// that names no modules, and before any hello, the mail is read as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((media: string) => ({ matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});

type ApiCall = { path: string; method?: string };
const world = vi.hoisted(() => ({
  calls: [] as ApiCall[],
  hub: { features: [] as string[], loaded: false },
  context: "" as string | undefined,
  clipped: undefined as boolean | undefined, // the item page's mail_body_truncated; undefined: the hub does not say
  itemFails: false,
  translation: null as null | { subject: string; text: string },
  prefs: { enabled: false, mail: true, target: "zh-Hans", chats: [] as { id: string }[] },
}));

vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: async (path: string, init?: { method?: string }) => {
    world.calls.push({ path, method: init?.method });
    if (path === "/mail/accounts") return { accounts: [{ id: "a1", email: "me@example.com" }] };
    if (path.startsWith("/mail/messages/") && path.includes("/flags")) return {};
    if (path.startsWith("/mail/messages/")) return { message: { uid: 7, subject: "Weekly sync", from: { name: "Ana", email: "ana@example.com" }, to: [{ email: "me@example.com" }], date: "2026-11-20T09:00:00Z", text: "The whole message from the mail server", seen: false, attachments: [{ id: "1", filename: "agenda.pdf", size: 2048 }] } };
    if (path === "/translate/mail") {
      if (!world.translation) throw new Error("mail is not set up");
      return world.translation;
    }
    if (path.startsWith("/inbox/mail:")) {
      if (world.itemFails) throw new Error("HTTP 503");
      return { ...mailItem(), context: world.context, ...(world.clipped === undefined ? {} : { mail_body_truncated: world.clipped }) };
    }
    return {};
  },
}));
vi.mock("../../lib/store", async (original) => {
  const m = await original<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, features: world.hub.features, loaded: world.hub.loaded })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", async (original) => ({ ...(await original<typeof import("../../lib/translate")>()), useTranslatePrefs: () => world.prefs }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { connectModuleHub, loadModule, registerModule, MODULES_FEATURE, resetModulesForTest } from "../../core/module-host";
import { initI18n, t } from "../../i18n";
import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { handOffContext } from "../../lib/inbox-page";
import { loadMailAccounts, mailItemReadOnly, mailReading, resetMailForTests, type MailReading } from "../../lib/mail";
import { resetManagerChoice, settleRosterDefault } from "../../lib/manager-choice";
import { reset } from "../../lib/store";
import { MODULES } from "../../modules";
import { setModuleSwitchedOff } from "../../core/module-switch";
import { MailBody } from "./InboxBodies";
import { InboxDetail } from "./InboxDetail";
import { EventBody } from "./InboxEvent";
import { DraftCard } from "./InboxParts";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function mailItem(patch: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "mail:a1:thread:7", source: "mail", kind: "mail", account: "a1", account_name: "me@example.com", title: "Weekly sync", sender: "Ana",
    preview: "Hi, the agenda for Thursday", count: 1, tier: "action", updated_at: "2026-11-20T09:00:00Z", version: "v1",
    mail: { account_id: "a1", mailbox: "INBOX", uids: [7], from: { name: "Ana", email: "ana@example.com" } },
    ...patch,
  };
}
const STORED = "Hi,\n\nThe agenda for Thursday is below.\nAna";
const storedContext = `邮箱 me@example.com 收到的未读邮件（共 1 封，下面是最新一封）：\n发件人：Ana <ana@example.com>\n收件人：me@example.com\n抄送：\n时间：2026-11-20T09:00:00Z\n主题：Weekly sync\n\n${STORED}`;

/** The hub's hello, as the three cases of the hub flag rule. */
const HUBS: Record<"off" | "on" | "noMarker" | "noHello", { features: string[]; loaded: boolean }> = {
  off: { features: [MODULES_FEATURE, "module.lark"], loaded: true },
  on: { features: [MODULES_FEATURE, "module.mail"], loaded: true },
  noMarker: { features: ["terminal_stream"], loaded: true },
  noHello: { features: [], loaded: false },
};
const setHub = (hub: keyof typeof HUBS) => Object.assign(world.hub, { features: [...HUBS[hub].features], loaded: HUBS[hub].loaded });

const descriptor = MODULES.find((m) => m.mailbox);
const chunk = vi.fn();
const catalog = vi.fn();
let host: HTMLDivElement;
let root: Root;
const flush = async (times = 8) => {
  for (let i = 0; i < times; i++) await act(async () => { await Promise.resolve(); });
};
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label) ?? null;
const mailCalls = () => world.calls.filter((c) => c.path.startsWith("/mail/"));
const notice = () => host.querySelector("[data-mail-off-notice]");

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  resetModulesForTest();
  chunk.mockClear();
  catalog.mockClear();
  if (descriptor) registerModule({
    ...descriptor,
    load: () => { chunk(); return descriptor.load(); },
    locales: { en: () => { catalog(); return descriptor.locales!.en(); } },
  });
  reset();
  setHub("noHello");
  connectModuleHub(() => world.hub, () => () => {});
  resetInboxForTests();
  resetMailForTests();
  resetManagerChoice();
  settleRosterDefault();
  Object.assign(world, { calls: [], context: storedContext, clipped: undefined, itemFails: false, translation: null });
  world.prefs.enabled = false;
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetModulesForTest();
  vi.restoreAllMocks();
});

describe("how a mail is read, decided once", () => {
  it("is stored only when the hello names modules and leaves mail out", () => {
    const kinds = (Object.keys(HUBS) as (keyof typeof HUBS)[]).map((k) => [k, mailReading(HUBS[k]).kind]);
    if (!descriptor) {
      expect(kinds.every(([, kind]) => kind === "stored")).toBe(true);
      return;
    }
    expect(kinds).toEqual([["off", "stored"], ["on", "full"], ["noMarker", "full"], ["noHello", "full"]]);
  });

  it("makes only mail items read-only, and only when stored", () => {
    const stored: MailReading = { kind: "stored", reason: "mail-off" };
    expect(mailItemReadOnly(stored, mailItem())).toBe(true);
    expect(mailItemReadOnly(stored, { source: "mail" })).toBe(true);
    expect(mailItemReadOnly(stored, { source: "github" })).toBe(false);
    expect(mailItemReadOnly(stored, undefined)).toBe(false);
    expect(mailItemReadOnly({ kind: "full" }, mailItem())).toBe(false);
  });
});

describe("a mail item's body with mail off", () => {
  beforeEach(() => setHub("off"));

  it("asks no mail route and shows the text the hub kept, then one line saying why the rest is not here", async () => {
    await act(async () => root.render(<MailBody item={mailItem()} />));
    await flush();

    expect(mailCalls()).toEqual([]);
    expect(world.calls).toEqual([{ path: "/inbox/mail:a1:thread:7", method: undefined }]);
    expect(host.querySelector("[data-mail-stored-text]")?.textContent).toBe(STORED);
    expect(notice()?.textContent).toBe(t("inbox-inboxbodies.mail_off_stored"));
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
    // the notice comes after the text
    expect(host.querySelector("[data-mail-stored-text]")!.compareDocumentPosition(notice()!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // who and when stay, from the item
    expect(host.querySelector("dl")?.textContent).toContain("Ana <ana@example.com>");
    expect(host.querySelector("dl")?.textContent).toContain(t("inbox-inboxbodies.date"));
    // no error text, nothing to retry, none of the mailbox's controls
    expect(host.textContent).not.toMatch(/HTTP|404|not set up/);
    expect(host.querySelector(".text-red")).toBeNull();
    expect(button(t("inbox-inboxbodies.reply"))).toBeNull();
    expect(button(t("inbox-inboxbodies.reply_all"))).toBeNull();
    expect(host.querySelector("a[download]")).toBeNull();
    expect(host.querySelector("iframe")).toBeNull();
    expect(host.querySelectorAll("button")).toHaveLength(0);
  });

  it("says the text is cut short only when the hub says so, nothing about it when it is whole, and 'may be' when the hub does not say", async () => {
    const lines = { clipped: t("inbox-inboxbodies.mail_off_stored_clipped"), whole: t("inbox-inboxbodies.mail_off_stored_whole"), unknown: t("inbox-inboxbodies.mail_off_stored") };
    expect(new Set(Object.values(lines)).size).toBe(3);
    expect(lines.clipped).toContain("is cut short");
    expect(lines.unknown).toContain("may be cut short");
    expect(lines.whole).not.toMatch(/cut short/);
    for (const line of Object.values(lines)) expect(line).toContain("the hub machine");
    for (const [clipped, state] of [[true, "clipped"], [false, "whole"], [undefined, "unknown"]] as const) {
      world.clipped = clipped;
      await act(async () => root.render(<MailBody key={state} item={mailItem()} />));
      await flush();
      expect(notice()?.textContent, state).toBe(lines[state]);
      expect(notice()?.getAttribute("data-mail-stored-clipping"), state).toBe(state);
      expect(host.querySelector("[data-mail-stored-text]")?.textContent, state).toBe(STORED);
    }
    expect(mailCalls()).toEqual([]);
  });

  it("falls back to the list's preview when the item cannot be read again, still without an error", async () => {
    world.itemFails = true;
    world.clipped = false; // the page that would say so cannot be read: the list's copy says nothing
    await act(async () => root.render(<MailBody item={mailItem({ mail_body_truncated: false } as Partial<InboxItem>)} />));
    await flush();
    expect(host.querySelector("[data-mail-stored-text]")?.textContent).toBe("Hi, the agenda for Thursday");
    expect(notice()?.textContent).toBe(t("inbox-inboxbodies.mail_off_stored"));
    expect(host.textContent).not.toMatch(/HTTP|503/);
    expect(mailCalls()).toEqual([]);
  });

  it("asks for no new translation, and shows the one the hub kept when the item lists it", async () => {
    world.prefs.enabled = true;
    await act(async () => root.render(<MailBody item={mailItem({ preview: "Bonjour, voici l'ordre du jour de jeudi pour notre réunion" })} />));
    await flush();
    expect(world.calls.map((c) => c.path)).toEqual(["/inbox/mail:a1:thread:7"]);

    world.calls = [];
    world.translation = { subject: "周会", text: "已存的译文" };
    await act(async () => root.render(<MailBody item={mailItem({ id: "mail:a1:thread:8", translations: { "zh-Hans": { subject: "周会", preview: "已存" } } })} />));
    await flush();
    expect(world.calls.map((c) => c.path).sort()).toEqual(["/inbox/mail:a1:thread:8", "/translate/mail"]);
    expect(mailCalls()).toEqual([]);
    expect(host.querySelector("[data-mail-stored-text]")?.textContent).toBe("已存的译文");
    await act(async () => button(t("inbox-inboxbodies.show_original"))!.click());
    expect(host.querySelector("[data-mail-stored-text]")?.textContent).toBe(STORED);
  });

  it("stays quiet when the kept translation is gone", async () => {
    world.prefs.enabled = true;
    // its own mail: translations are remembered by mail (lib/translate.ts)
    const other = mailItem({ translations: { "zh-Hans": { subject: "周会", preview: "已存" } } });
    await act(async () => root.render(<MailBody item={{ ...other, mail: { ...other.mail!, uids: [9] } }} />));
    await flush();
    expect(world.calls.map((c) => c.path)).toContain("/translate/mail");
    expect(host.querySelector("[data-mail-stored-text]")?.textContent).toBe(STORED);
    expect(host.textContent).not.toContain("not set up");
  });

  it("does not ask the hub for mail accounts or point an agent at mail tools", async () => {
    await expect(loadMailAccounts()).resolves.toEqual([]);
    expect(mailCalls()).toEqual([]);
    expect(handOffContext(mailItem({ context: "kept" })).text).toBe("kept");
  });
});

describe("a mail item's actions with mail off", () => {
  beforeEach(() => setHub("off"));

  it("keeps the Inbox's own (Hand to AI, Done) and leaves out drafting a reply", async () => {
    await act(async () => root.render(<InboxDetail item={mailItem()} loaded />));
    await flush();
    expect(button(t("inbox-inboxdetail.hand_to_ai"))).not.toBeNull();
    expect(button(t("inbox-inboxdetail.done"))).not.toBeNull();
    expect(button(t("inbox-inboxdetail.ai_draft"))).toBeNull();
    expect(notice()).not.toBeNull();
    expect(mailCalls()).toEqual([]);
  });

  it("keeps a drafted reply readable without Send", async () => {
    const drafted = mailItem({ agent: { state: "drafted", draft: "Thanks, Thursday works." } });
    await act(async () => root.render(<DraftCard it={drafted} onEdit={() => {}} onSend={() => {}} />));
    expect(host.querySelector("[data-draft-text]")?.textContent).toBe("Thanks, Thursday works.");
    expect(button(t("inbox-inboxparts.send"))).toBeNull();
    expect(button(t("inbox-inboxparts.edit"))).not.toBeNull();
  });

  it("offers no confirm on a reply suggestion, which would send a mail", async () => {
    const suggested = mailItem({ status: "action", summary: "Ana asks about Thursday", suggestion: { version: 2, kind: "reply", text: "Thursday works." } });
    await act(async () => root.render(<EventBody item={suggested} onSource={() => {}} />));
    await flush();
    expect(host.querySelector("[data-suggestion-text]")?.textContent ?? host.textContent).toContain("Thursday works.");
    expect(button(t("inbox-inboxevent.edit"))).toBeNull();
    expect(world.calls.filter((c) => c.path.endsWith("/suggestion"))).toEqual([]);
  });
});

describe.skipIf(!descriptor).each(["on", "noMarker", "noHello"] as const)("a mail item with the hub %s", (hub) => {
  beforeEach(async () => {
    setHub(hub);
    await loadModule(descriptor!.id);
  });

  it("is read from the mail server as before, with its replies, flags and attachments", async () => {
    await act(async () => root.render(<MailBody item={mailItem()} />));
    await act(async () => { await vi.dynamicImportSettled(); });
    await flush();

    expect(world.calls).toEqual([
      { path: "/mail/accounts", method: undefined },
      { path: "/mail/messages/a1/7?mailbox=INBOX", method: undefined },
      { path: "/mail/messages/a1/7/flags", method: "POST" },
    ]);
    expect(host.textContent).toContain("The whole message from the mail server");
    expect(button(t("inbox-inboxbodies.reply"))).not.toBeNull();
    expect(button(t("inbox-inboxbodies.reply_all"))).not.toBeNull();
    expect(host.querySelector("a[download='agenda.pdf']")).not.toBeNull();
    expect(notice()).toBeNull();
    expect(host.querySelector("[data-mail-body='stored']")).toBeNull();
  });

  it("keeps drafting and sending a reply", async () => {
    await act(async () => root.render(<InboxDetail item={mailItem()} loaded />));
    await flush();
    expect(button(t("inbox-inboxdetail.ai_draft"))).not.toBeNull();
    expect(button(t("inbox-inboxdetail.hand_to_ai"))).not.toBeNull();
    const drafted = mailItem({ agent: { state: "drafted", draft: "Thanks, Thursday works." } });
    await act(async () => root.render(<DraftCard it={drafted} onEdit={() => {}} onSend={() => {}} />));
    expect(button(t("inbox-inboxparts.send"))).not.toBeNull();
    expect(handOffContext(mailItem({ context: "kept" })).text).toContain("mail_read");
    const suggested = mailItem({ status: "action", summary: "Ana asks about Thursday", suggestion: { version: 2, kind: "reply", text: "Thursday works." } });
    await act(async () => root.render(<EventBody item={suggested} onSource={() => {}} />));
    await flush();
    expect(button(t("inbox-inboxevent.edit"))).not.toBeNull();
  });
});

it("keeps historical mail readable on a mail-capable hub with no provider or a local opt-out", async () => {
  setHub("on");
  if (descriptor) setModuleSwitchedOff(descriptor.id, true);
  try {
    await act(async () => root.render(<MailBody item={mailItem()} />));
    await flush();
    expect(host.querySelector("[data-mail-stored-text]")?.textContent).toBe(STORED);
    expect(mailCalls()).toEqual([]);
    expect(handOffContext(mailItem({ context: "kept" })).text).toBe("kept");
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
    expect(notice()?.textContent).toBe(t("inbox-inboxbodies.saved"));
    if (descriptor) {
      await act(async () => { setModuleSwitchedOff(descriptor.id, false); await vi.dynamicImportSettled(); });
      await act(async () => { await vi.dynamicImportSettled(); });
      expect(button(t("inbox-inboxbodies.reply"))).not.toBeNull();
      expect(chunk).toHaveBeenCalledOnce();
      expect(catalog).toHaveBeenCalledOnce();
      await act(async () => { setModuleSwitchedOff(descriptor.id, true); });
      expect(host.querySelector("[data-mail-stored-text]")?.textContent).toBe(STORED);
    }
  } finally {
    act(() => root.render(null));
    if (descriptor) setModuleSwitchedOff(descriptor.id, false);
  }
});
