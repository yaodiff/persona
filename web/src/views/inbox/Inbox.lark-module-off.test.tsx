// @vitest-environment jsdom
// Shared Inbox write entrypoints (list DraftCard Send, detail DraftDialog Send, and TextSuggestion reply
// confirm/retry/instruct including Enter) follow the Lark module boundary for Lark and larkcli items while
// preserving readable drafts, Done, external links, non-reply actions, and other connectors.
import { registerTestMailbox } from "../../test/mailbox-fixture";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((media: string) => ({
    matches: false,
    media,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
    IntersectionObserver: globalThis.IntersectionObserver ?? class {
      observe() {}
      unobserve() {}
      disconnect() {}
      takeRecords() { return []; }
    },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});

type ApiCall = { path: string; method?: string; json?: unknown };
const calls = vi.hoisted(() => ({
  list: [] as ApiCall[],
  items: [] as unknown[],
}));

vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: async (path: string, init?: { method?: string; json?: unknown }) => {
    calls.list.push({ path, method: init?.method, json: init?.json });
    if (path === "/mail/accounts") return { accounts: [] };
    if (path.startsWith("/inbox?")) return { items: calls.items };
    if (path.endsWith("/draft")) return { text: "Generated draft" };
    return {};
  },
}));
vi.mock("../../lib/store", async (original) => {
  const m = await original<typeof import("../../lib/store")>();
  return {
    ...m,
    useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })),
  };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({
  useTranslatePrefs: () => ({ enabled: false }),
  hubTranslation: () => undefined,
  translatesChat: () => false,
  translatesMail: () => false,
  languageName: () => "",
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
vi.mock("./ConnectorThread", async (original) => ({
  ...(await original<typeof import("./ConnectorThread")>()),
  ConnectorBody: () => <div data-testid="connector-history">Connector history</div>,
}));

import { connectModuleHub, MODULES_FEATURE, registerModule, resetModulesForTest, type ModuleDescriptor } from "../../core/module-host";
import { forgetModuleSwitches, setModuleSwitchedOff } from "../../core/module-switch";
import { initI18n, t } from "../../i18n";
import { itemPath, reloadInbox, resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { resetManagerChoice, settleRosterDefault } from "../../lib/manager-choice";
import { reset } from "../../lib/store";
import { Inbox } from "./Inbox";
import { InboxDetail } from "./InboxDetail";
import { DraftDialog } from "./InboxDialogs";
import { EventBody } from "./InboxEvent";
import { InboxList } from "./InboxList";
import { LarkWriteGate } from "./LarkThread";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const larkModule: ModuleDescriptor = {
  id: "lark",
  feature: "module.lark",
  title: () => "Lark",
  description: () => "Lark module",
  load: async () => ({}),
};

const larkDraftItem = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "lark:oc_demo:1",
  source: "lark",
  kind: "dm",
  account: "feishu",
  account_name: "Lark",
  title: "Alice",
  preview: "Can you confirm?",
  count: 1,
  tier: "action",
  updated_at: "2026-11-20T09:00:00Z",
  lark: { feed_id: "oc_demo", is_group: false },
  agent: { state: "drafted", draft: "Lark draft text" },
  ...patch,
});

const larkcliDraftItem = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "larkcli:oc_cli:1",
  source: "larkcli",
  kind: "dm",
  account: "larkcli",
  account_name: "Feishu",
  title: "Bob",
  preview: "Check status?",
  count: 1,
  tier: "action",
  updated_at: "2026-11-20T09:01:00Z",
  connector: {
    thread: "larkcli:chat:oc_cli",
    can_reply: true,
    open_url: "https://applink.feishu.cn/client/chat/open?openChatId=oc_cli",
  },
  agent: { state: "drafted", draft: "LarkCLI draft text" },
  ...patch,
});

const mailDraftItem = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "mail:a1:thread:1",
  source: "mail",
  kind: "mail",
  account: "a1",
  account_name: "Mail",
  title: "Weekly sync",
  preview: "Agenda attached",
  count: 1,
  tier: "action",
  updated_at: "2026-11-20T09:02:00Z",
  agent: { state: "drafted", draft: "Mail draft text" },
  ...patch,
});

const githubDraftItem = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "github:org/repo#42",
  source: "github",
  kind: "mention",
  account: "github",
  account_name: "GitHub",
  title: "PR #42",
  preview: "Please review",
  count: 1,
  tier: "action",
  updated_at: "2026-11-20T09:03:00Z",
  connector: { thread: "org/repo#42", can_reply: true },
  agent: { state: "drafted", draft: "GitHub draft text" },
  ...patch,
});

const replySuggestionItem = (
  source: "lark" | "larkcli" | "mail" | "github",
  patch: Partial<InboxItem> = {},
): InboxItem => ({
  id: `${source}:reply:1`,
  source,
  kind: source === "mail" ? "mail" : "dm",
  account: source,
  account_name: source,
  title: `Thread ${source}`,
  preview: "Need a reply",
  count: 1,
  tier: "action",
  status: "action",
  summary: "Summary text",
  updated_at: "2026-11-20T09:05:00Z",
  ...(source === "lark" ? { lark: { feed_id: "oc_s1", is_group: false } } : {}),
  ...(source === "larkcli" ? { connector: { thread: "larkcli:chat:oc_s1", can_reply: true } } : {}),
  ...(source === "github" ? { connector: { thread: "org/repo#9", can_reply: true } } : {}),
  suggestion: { version: 2, kind: "reply", text: `Suggested reply for ${source}` },
  ...patch,
});

let host: HTMLDivElement;
let root: Root;
const hub = { features: [] as string[], loaded: false };
const hubListeners = new Set<() => void>();
const setHub = (patch: Partial<typeof hub>) => {
  Object.assign(hub, patch);
  hubListeners.forEach((l) => l());
};

const flush = async (times = 8) => {
  for (let i = 0; i < times; i++) await act(async () => { await Promise.resolve(); });
};

const replyCalls = () => calls.list.filter((c) => c.path.endsWith("/reply"));
const suggestionCalls = () => calls.list.filter((c) => c.path.endsWith("/suggestion"));
const doneCalls = () => calls.list.filter((c) => c.path.endsWith("/done"));

const rowEl = (id: string) => host.querySelector<HTMLElement>(`[data-item="${id}"]`)!;
const rowButtons = (id: string) => [...rowEl(id).querySelectorAll<HTMLButtonElement>("button")];
const findButton = (container: ParentNode, label: string) =>
  [...container.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label) ?? null;

const setTextarea = async (el: HTMLTextAreaElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const setInput = async (el: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const setupModule = (mode: "on" | "off" | "absent") => {
  if (mode === "on") registerModule(larkModule);
  if (mode === "off") {
    registerModule(larkModule);
    setModuleSwitchedOff("lark", true);
  }
};

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  resetModulesForTest();
  registerTestMailbox();
  forgetModuleSwitches();
  reset();
  hub.features = [];
  hub.loaded = false;
  hubListeners.clear();
  connectModuleHub(() => hub, (l) => {
    hubListeners.add(l);
    return () => hubListeners.delete(l);
  });
  resetInboxForTests();
  resetMailForTests();
  resetManagerChoice();
  settleRosterDefault();
  calls.list = [];
  calls.items = [];
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetInboxForTests();
  resetModulesForTest();
  forgetModuleSwitches();
  vi.restoreAllMocks();
});

describe("InboxList DraftCard write gate", () => {
  it.each(["lark", "larkcli"] as const)("allows sending %s draft when lark module is on", async (source) => {
    setupModule("on");
    const it = source === "lark" ? larkDraftItem() : larkcliDraftItem();
    calls.items = [it];
    await act(async () => root.render(<LarkWriteGate><InboxList /></LarkWriteGate>));
    await flush();

    const sendBtn = rowButtons(it.id).find((b) => b.textContent?.trim() === t("inbox-inboxparts.send"));
    expect(sendBtn).toBeDefined();
    await act(async () => sendBtn!.click());
    await flush();

    expect(replyCalls()).toEqual([{
      path: `/inbox/${it.id}/reply`,
      method: "POST",
      json: { text: it.agent!.draft, changes: undefined },
    }]);
  });

  it.each(["off", "absent"] as const)("blocks Lark/larkcli DraftCard Send when module is %s while keeping draft text, Done, and other connectors writable", async (mode) => {
    setupModule(mode);
    const lark = larkDraftItem();
    const larkcli = larkcliDraftItem();
    const mail = mailDraftItem();
    const github = githubDraftItem();
    calls.items = [lark, larkcli, mail, github];
    await act(async () => root.render(<LarkWriteGate><InboxList /></LarkWriteGate>));
    await flush();

    for (const target of [lark, larkcli]) {
      expect(rowEl(target.id).querySelector("[data-draft-text]")?.textContent).toBe(target.agent!.draft);
      expect(findButton(rowEl(target.id), t("inbox-inboxparts.send"))).toBeNull();
      // Edit opens the draft dialog in read-only mode so the user can inspect/copy the draft without sending.
      const editBtn = findButton(rowEl(target.id), t("inbox-inboxparts.edit"));
      expect(editBtn).not.toBeNull();
      await act(async () => editBtn!.click());
      const dlg = document.querySelector<HTMLElement>("[role=dialog]")!;
      expect(dlg).not.toBeNull();
      expect(dlg.querySelector("textarea")?.value).toBe(target.agent!.draft);
      expect(findButton(dlg, t("inbox-inboxdialogs.send"))).toBeNull();
      await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    }

    // Done remains available on Lark items.
    const doneBtn = rowEl(lark.id).querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-inboxlist.mark_done_title")}"]`);
    expect(doneBtn).not.toBeNull();
    await act(async () => doneBtn!.click());
    await flush();
    expect(doneCalls()).toHaveLength(1);
    expect(replyCalls()).toHaveLength(0);

    // Mail and non-Lark connectors keep their original Send behavior.
    for (const other of [mail, github]) {
      const btn = findButton(rowEl(other.id), t("inbox-inboxparts.send"));
      expect(btn).not.toBeNull();
      await act(async () => btn!.click());
    }
    await flush();
    expect(replyCalls().map((c) => c.path)).toEqual([`${itemPath(mail.id)}/reply`, `${itemPath(github.id)}/reply`]);
  });

  it("syncs DraftCard Send immediately on live toggle (local switch and hub feature flag)", async () => {
    setupModule("on");
    const lark = larkDraftItem();
    const larkcli = larkcliDraftItem();
    calls.items = [lark, larkcli];
    await act(async () => root.render(<LarkWriteGate><InboxList /></LarkWriteGate>));
    await flush();

    expect(findButton(rowEl(lark.id), t("inbox-inboxparts.send"))).not.toBeNull();
    expect(findButton(rowEl(larkcli.id), t("inbox-inboxparts.send"))).not.toBeNull();

    // Browser switch off -> Send disappears immediately, draft stays visible.
    await act(async () => setModuleSwitchedOff("lark", true));
    expect(findButton(rowEl(lark.id), t("inbox-inboxparts.send"))).toBeNull();
    expect(findButton(rowEl(larkcli.id), t("inbox-inboxparts.send"))).toBeNull();
    expect(rowEl(lark.id).querySelector("[data-draft-text]")?.textContent).toBe(lark.agent!.draft);

    // Browser switch back on -> Send reappears immediately.
    await act(async () => setModuleSwitchedOff("lark", false));
    expect(findButton(rowEl(lark.id), t("inbox-inboxparts.send"))).not.toBeNull();

    // Hub removes module.lark -> Send disappears immediately.
    await act(async () => setHub({ features: [MODULES_FEATURE], loaded: true }));
    expect(findButton(rowEl(lark.id), t("inbox-inboxparts.send"))).toBeNull();

    // Hub restores module.lark -> Send works again.
    await act(async () => setHub({ features: [MODULES_FEATURE, "module.lark"], loaded: true }));
    const sendBtn = findButton(rowEl(lark.id), t("inbox-inboxparts.send"));
    expect(sendBtn).not.toBeNull();
    await act(async () => sendBtn!.click());
    await flush();
    expect(replyCalls()).toHaveLength(1);
    expect(replyCalls()[0]?.path).toBe(`/inbox/${lark.id}/reply`);
  });

  it("applies the gate through the full Inbox page wrapper", async () => {
    setupModule("off");
    const lark = larkDraftItem();
    const mail = mailDraftItem();
    calls.items = [lark, mail];
    await act(async () => root.render(<Inbox onToggleSidebar={() => {}} />));
    await flush();

    expect(findButton(rowEl(lark.id), t("inbox-inboxparts.send"))).toBeNull();
    expect(findButton(rowEl(mail.id), t("inbox-inboxparts.send"))).not.toBeNull();

    await act(async () => setModuleSwitchedOff("lark", false));
    expect(findButton(rowEl(lark.id), t("inbox-inboxparts.send"))).not.toBeNull();
  });
});

describe("InboxDetail and DraftDialog write gate", () => {
  it.each(["lark", "larkcli"] as const)("allows opening and sending %s DraftDialog when module is on", async (source) => {
    setupModule("on");
    const it = source === "lark" ? larkDraftItem() : larkcliDraftItem();
    await act(async () => root.render(<LarkWriteGate><InboxDetail item={it} loaded /></LarkWriteGate>));
    await flush();

    const openBtn = findButton(host, t("inbox-inboxdetail.view_draft"));
    expect(openBtn).not.toBeNull();
    await act(async () => openBtn!.click());
    const dlg = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(dlg).not.toBeNull();
    await setTextarea(dlg.querySelector("textarea")!, "Edited reply");
    const sendBtn = findButton(dlg, t("inbox-inboxdialogs.send"));
    expect(sendBtn).not.toBeNull();
    await act(async () => sendBtn!.click());
    await flush();

    expect(replyCalls()).toEqual([{
      path: `/inbox/${it.id}/reply`,
      method: "POST",
      json: { text: "Edited reply", changes: undefined },
    }]);
  });

  it.each(["off", "absent"] as const)("blocks Lark/larkcli DraftDialog Send and draftless AI draft CTA when module is %s while preserving draft text, links, Done, and other connectors", async (mode) => {
    setupModule(mode);
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);

    // 1) Lark item with existing draft: View draft stays readable, Send is absent.
    const lark = larkDraftItem();
    await act(async () => root.render(<LarkWriteGate><InboxDetail item={lark} loaded /></LarkWriteGate>));
    await flush();
    expect(findButton(host, t("inbox-inboxdetail.done"))).not.toBeNull();
    const viewDraft = findButton(host, t("inbox-inboxdetail.view_draft"));
    expect(viewDraft).not.toBeNull();
    await act(async () => viewDraft!.click());
    const dlg = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(dlg.querySelector("textarea")?.value).toBe(lark.agent!.draft);
    expect(findButton(dlg, t("inbox-inboxdialogs.send"))).toBeNull();
    await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));

    // 2) Draftless larkcli item: AI draft CTA is hidden, external Feishu link and history stay available.
    const larkcliNoDraft = larkcliDraftItem({ agent: undefined });
    await act(async () => root.render(<LarkWriteGate><InboxDetail item={larkcliNoDraft} loaded /></LarkWriteGate>));
    await flush();
    expect(findButton(host, t("inbox-inboxdetail.ai_draft"))).toBeNull();
    expect(host.querySelector("[data-testid='connector-history']")).not.toBeNull();
    const extBtn = findButton(host, t("inbox-inboxdetail.open_in_feishu"));
    expect(extBtn).not.toBeNull();
    await act(async () => extBtn!.click());
    expect(openSpy).toHaveBeenCalledExactlyOnceWith(larkcliNoDraft.connector!.open_url, "_blank", "noopener,noreferrer");
    expect(replyCalls()).toHaveLength(0);

    // 3) Non-Lark connector (GitHub) keeps DraftDialog Send working.
    const github = githubDraftItem();
    await act(async () => root.render(<LarkWriteGate><InboxDetail item={github} loaded /></LarkWriteGate>));
    await flush();
    await act(async () => findButton(host, t("inbox-inboxdetail.view_draft"))!.click());
    const ghDlg = document.querySelector<HTMLElement>("[role=dialog]")!;
    const ghSend = findButton(ghDlg, t("inbox-inboxdialogs.send"));
    expect(ghSend).not.toBeNull();
    await act(async () => ghSend!.click());
    await flush();
    expect(replyCalls()).toHaveLength(1);
    expect(replyCalls()[0]?.path).toBe(`${itemPath(github.id)}/reply`);
  });

  it("syncs an open DraftDialog Send button immediately on live toggle without losing edits", async () => {
    setupModule("on");
    const lark = larkDraftItem();
    const onClose = vi.fn();
    await act(async () => root.render(<LarkWriteGate><DraftDialog item={lark} onClose={onClose} /></LarkWriteGate>));
    await flush();

    const dlg = document.querySelector<HTMLElement>("[role=dialog]")!;
    await setTextarea(dlg.querySelector("textarea")!, "Preserved live edit");
    expect(findButton(dlg, t("inbox-inboxdialogs.send"))).not.toBeNull();

    await act(async () => setModuleSwitchedOff("lark", true));
    expect(findButton(dlg, t("inbox-inboxdialogs.send"))).toBeNull();
    expect(dlg.querySelector("textarea")?.value).toBe("Preserved live edit");
    expect(replyCalls()).toHaveLength(0);

    await act(async () => setModuleSwitchedOff("lark", false));
    const sendBtn = findButton(dlg, t("inbox-inboxdialogs.send"));
    expect(sendBtn).not.toBeNull();
    await act(async () => sendBtn!.click());
    await flush();
    expect(replyCalls()).toEqual([{
      path: `/inbox/${lark.id}/reply`,
      method: "POST",
      json: { text: "Preserved live edit", changes: undefined },
    }]);
    expect(onClose).toHaveBeenCalledOnce();
  });
});

describe("InboxEvent TextSuggestion reply write gate", () => {
  it.each(["lark", "larkcli"] as const)("allows %s reply suggestion confirm, instruct (button and Enter), and retry when module is on", async (source) => {
    setupModule("on");
    const it = replySuggestionItem(source);
    calls.items = [it];
    await act(async () => { await reloadInbox(); });

    // Direct confirm
    await act(async () => root.render(<LarkWriteGate><EventBody item={it} onSource={() => {}} /></LarkWriteGate>));
    await flush();
    await act(async () => findButton(host, t("lib-inbox-events.send"))!.click());
    await flush();
    expect(suggestionCalls()).toHaveLength(1);
    expect(suggestionCalls()[0]?.json).toMatchObject({ version: 2, action: "confirm" });

    // Edit -> Enter instruct
    calls.list = [];
    const it2 = replySuggestionItem(source, { id: `${source}:reply:2` });
    calls.items = [it2];
    await act(async () => { await reloadInbox(); });
    await act(async () => root.render(<LarkWriteGate><EventBody item={it2} onSource={() => {}} /></LarkWriteGate>));
    await flush();
    await act(async () => findButton(host, t("inbox-inboxevent.edit"))!.click());
    const sayInput = host.querySelector<HTMLInputElement>("[data-suggestion-say]")!;
    await setInput(sayInput, "Make it shorter");
    await act(async () => sayInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(suggestionCalls()).toHaveLength(1);
    expect(suggestionCalls()[0]?.json).toMatchObject({ version: 2, action: "instruct", text: "Make it shorter" });

    // Retry failed attempt
    calls.list = [];
    const retryItem = replySuggestionItem(source, {
      id: `${source}:reply:3`,
      suggestion_attempts: [{
        version: 2,
        status: "failed",
        retryable: true,
        text: "Frozen reply payload",
        at: "2026-11-20T09:06:00Z",
      }],
    });
    calls.items = [retryItem];
    await act(async () => { await reloadInbox(); });
    await act(async () => root.render(<LarkWriteGate><EventBody item={retryItem} onSource={() => {}} /></LarkWriteGate>));
    await flush();
    const retryBtn = host.querySelector<HTMLButtonElement>("[data-testid='suggestion-retry']");
    expect(retryBtn).not.toBeNull();
    await act(async () => retryBtn!.click());
    await flush();
    expect(suggestionCalls()).toHaveLength(1);
    expect(suggestionCalls()[0]?.json).toMatchObject({ version: 2, action: "confirm" });
  });

  it.each(["off", "absent"] as const)("blocks Lark/larkcli reply confirm, retry, edit/instruct when module is %s while preserving text, Reject, non-reply suggestions, and other connectors", async (mode) => {
    setupModule(mode);

    for (const source of ["lark", "larkcli"] as const) {
      const it = replySuggestionItem(source);
      calls.items = [it];
      await act(async () => { await reloadInbox(); });
      await act(async () => root.render(<LarkWriteGate><EventBody item={it} onSource={() => {}} /></LarkWriteGate>));
      await flush();

      expect(host.querySelector("[data-suggestion-text]")?.textContent).toBe(it.suggestion!.text);
      expect(findButton(host, t("lib-inbox-events.send"))).toBeNull();
      expect(findButton(host, t("inbox-inboxevent.edit"))).toBeNull();
      expect(host.querySelector("[data-suggestion-say]")).toBeNull();
      // Reject remains available for dismissing the suggestion locally.
      expect(findButton(host, t("inbox-inboxevent.reject"))).not.toBeNull();

      // Handback reply suggestions are also read-only when the Lark module is off/absent.
      const handback = replySuggestionItem(source, {
        id: `${source}:reply:hb`,
        suggestion: { version: 2, kind: "reply", handback: true, text: "Handback reply" },
      });
      await act(async () => root.render(<LarkWriteGate><EventBody item={handback} onSource={() => {}} /></LarkWriteGate>));
      await flush();
      expect(host.querySelector("[data-suggestion-text]")?.textContent).toBe("Handback reply");
      expect(findButton(host, t("lib-inbox-events.confirm"))).toBeNull();
      expect(findButton(host, t("inbox-inboxevent.edit"))).toBeNull();

      // Failed retryable reply keeps its attempt text readable without a Retry button.
      const retryItem = replySuggestionItem(source, {
        id: `${source}:reply:retry`,
        suggestion_attempts: [{
          version: 2,
          status: "failed",
          retryable: true,
          text: "Failed reply text",
          at: "2026-11-20T09:06:00Z",
        }],
      });
      await act(async () => root.render(<LarkWriteGate><EventBody item={retryItem} onSource={() => {}} /></LarkWriteGate>));
      await flush();
      expect(host.querySelector("[data-suggestion-text]")?.textContent).toBe("Failed reply text");
      expect(host.querySelector("[data-testid='suggestion-retry']")).toBeNull();
    }
    expect(suggestionCalls()).toHaveLength(0);

    // Non-reply suggestions on Lark (e.g. conclusion) still work when Lark module is off/absent.
    const conclusion: InboxItem = {
      ...replySuggestionItem("lark", { id: "lark:conclusion:1" }),
      suggestion: { version: 1, kind: "conclusion", text: "No action needed" },
    };
    calls.items = [conclusion];
    await act(async () => { await reloadInbox(); });
    await act(async () => root.render(<LarkWriteGate><EventBody item={conclusion} onSource={() => {}} /></LarkWriteGate>));
    await flush();
    await act(async () => findButton(host, t("lib-inbox-events.got_it"))!.click());
    await flush();
    expect(suggestionCalls()).toHaveLength(1);
    expect(suggestionCalls()[0]?.path).toBe("/inbox/lark:conclusion:1/suggestion");

    // Non-Lark reply suggestions (mail, github) keep confirm, instruct (Enter), and retry working.
    calls.list = [];
    for (const other of ["mail", "github"] as const) {
      const it = replySuggestionItem(other);
      calls.items = [it];
      await act(async () => { await reloadInbox(); });
      await act(async () => root.render(<LarkWriteGate><EventBody item={it} onSource={() => {}} /></LarkWriteGate>));
      await flush();
      await act(async () => findButton(host, t("inbox-inboxevent.edit"))!.click());
      const input = host.querySelector<HTMLInputElement>("[data-suggestion-say]")!;
      await setInput(input, "Ask for timeline");
      await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
      await flush();
    }
    expect(suggestionCalls().map((c) => c.path)).toEqual(["/inbox/mail:reply:1/suggestion", "/inbox/github:reply:1/suggestion"]);
  });

  it("syncs TextSuggestion edit/instruct (including Enter) and retry immediately on live toggle", async () => {
    setupModule("on");
    const it = replySuggestionItem("lark");
    calls.items = [it];
    await act(async () => { await reloadInbox(); });
    await act(async () => root.render(<LarkWriteGate><EventBody item={it} onSource={() => {}} /></LarkWriteGate>));
    await flush();

    // Open edit mode and type both an edit and an instruction while module is on.
    await act(async () => findButton(host, t("inbox-inboxevent.edit"))!.click());
    const editor = host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")!;
    await setTextarea(editor, "Edited reply kept across toggle");
    const sayInput = host.querySelector<HTMLInputElement>("[data-suggestion-say]")!;
    await setInput(sayInput, "Keep it brief");

    // Live-toggle off: say input, Say button, and Send button disappear immediately; edited text stays; Enter sends nothing.
    await act(async () => setModuleSwitchedOff("lark", true));
    expect(host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")?.value).toBe("Edited reply kept across toggle");
    expect(host.querySelector("[data-suggestion-say]")).toBeNull();
    expect(findButton(host, t("inbox-inboxevent.say"))).toBeNull();
    expect(findButton(host, t("lib-inbox-events.send"))).toBeNull();
    await act(async () => sayInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(suggestionCalls()).toHaveLength(0);

    // Live-toggle back on: controls reappear with preserved instruction, and Enter submits instruct.
    await act(async () => setModuleSwitchedOff("lark", false));
    const restoredInput = host.querySelector<HTMLInputElement>("[data-suggestion-say]")!;
    expect(restoredInput).not.toBeNull();
    expect(restoredInput.value).toBe("Keep it brief");
    expect(findButton(host, t("lib-inbox-events.send"))).not.toBeNull();
    await act(async () => restoredInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(suggestionCalls()).toHaveLength(1);
    expect(suggestionCalls()[0]?.json).toMatchObject({ version: 2, action: "instruct", text: "Keep it brief" });

    // Live-toggle on failed retryable attempt: Retry hidden while off, appears and submits once toggled on.
    calls.list = [];
    await act(async () => setModuleSwitchedOff("lark", true));
    const retryItem = replySuggestionItem("larkcli", {
      id: "larkcli:reply:live-retry",
      suggestion_attempts: [{
        version: 2,
        status: "failed",
        retryable: true,
        text: "Frozen retry text",
        at: "2026-11-20T09:06:00Z",
      }],
    });
    calls.items = [retryItem];
    await act(async () => { await reloadInbox(); });
    await act(async () => root.render(<LarkWriteGate><EventBody item={retryItem} onSource={() => {}} /></LarkWriteGate>));
    await flush();
    expect(host.querySelector("[data-testid='suggestion-retry']")).toBeNull();
    expect(suggestionCalls()).toHaveLength(0);

    await act(async () => setModuleSwitchedOff("lark", false));
    const retryBtn = host.querySelector<HTMLButtonElement>("[data-testid='suggestion-retry']");
    expect(retryBtn).not.toBeNull();
    await act(async () => retryBtn!.click());
    await flush();
    expect(suggestionCalls()).toHaveLength(1);
    expect(suggestionCalls()[0]?.path).toBe("/inbox/larkcli:reply:live-retry/suggestion");
    expect(suggestionCalls()[0]?.json).toMatchObject({ version: 2, action: "confirm" });
  });
});
