// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { ApiError } from "../lib/api";
import { COMPACT_FEATURE, COMPACT_REQUESTED_EVENT, NEW_MAIN_FEATURE } from "../lib/compaction";
import { CLEAR_MAIN_FEATURE, clearMainContext, forgetMainCleared, isMainClearing, mainCleared } from "../lib/main-clear";
import { getState, refreshAssistant, reset, useStore } from "../lib/store";
import { navigate } from "../lib/router";
import { Toasts } from "../ui/atoms";
import type { Session } from "../lib/types";
import { ConversationRowMenu, type ConversationRowMenuViewProps, type MainCapabilities } from "./ConversationRowMenu";
import { NewMainDialog, type NewMainResult } from "./assistant/NewMainDialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function createExternalBox<T>(initial: T) {
  let current = initial;
  const listeners = new Set<() => void>();
  return {
    get: () => current,
    set: (next: T) => {
      current = next;
      listeners.forEach((l) => l());
    },
    setSilent: (next: T) => {
      current = next;
    },
    notify: () => {
      listeners.forEach((l) => l());
    },
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

function makeMainSession(patch: Partial<Session> = {}): Session {
  return {
    machine_id: "hub",
    session_id: "main-1",
    manager: "mgr-1",
    status: "idle",
    title: "Main Chat",
    cwd: "/workspace",
    assistant: true,
    updated_at: "2026-11-26T00:00:00Z",
    ...patch,
  };
}

let host: HTMLDivElement;
let anchor: HTMLButtonElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  Object.assign(globalThis, {
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
  });
  reset();
  getState().features = [COMPACT_FEATURE, CLEAR_MAIN_FEATURE, NEW_MAIN_FEATURE];
  getState().assistant = { machine_id: "hub", main: "main-1", conversations: [makeMainSession()] } as NonNullable<ReturnType<typeof getState>["assistant"]>;
  forgetMainCleared();
  host = document.createElement("div");
  anchor = document.createElement("button");
  document.body.append(host, anchor);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  anchor.remove();
  forgetMainCleared();
  reset();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("ConversationRowMenu main-chat parity & ABA/scope races", () => {
  it("gates Compact, Clear, and New Main Chat on hub features in default store mode", () => {
    const s = makeMainSession();
    getState().features = [COMPACT_FEATURE, CLEAR_MAIN_FEATURE, NEW_MAIN_FEATURE];
    getState().assistant = { machine_id: "hub", main: "main-1", conversations: [s] } as NonNullable<ReturnType<typeof getState>["assistant"]>;

    act(() => root.render(<ConversationRowMenu s={s} main anchor={anchor} onClose={vi.fn()} />));
    const labelsWithFeatures = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].map((el) => el.textContent?.trim() ?? "");
    expect(labelsWithFeatures).toHaveLength(5);
    expect(labelsWithFeatures.slice(0, 3)).toEqual([
      t("assistant-assistantchat.compact"),
      t("assistant-assistantchat.clear"),
      t("assistant-assistantchat.new_main_ellipsis"),
    ]);
    expect(labelsWithFeatures[3]).toContain(t("conversation-menu.copy_id"));
    expect(labelsWithFeatures[4]).toContain(t("conversation-menu.end"));

    // Degraded hub without main-conversation features hides all three actions cleanly.
    act(() => {
      getState().features = [];
      root.render(<ConversationRowMenu s={s} main anchor={anchor} onClose={vi.fn()} />);
    });
    const labelsDegraded = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].map((el) => el.textContent?.trim() ?? "");
    expect(labelsDegraded).toHaveLength(2);
    expect(labelsDegraded[0]).toContain(t("conversation-menu.copy_id"));
    expect(labelsDegraded[1]).toContain(t("conversation-menu.end"));
  });

  it("enforces fail-closed capability downgrade, source-switch reset, and ABA scope fencing on main actions", async () => {
    const s = makeMainSession();
    const scopeBox = createExternalBox("acct-1");
    const capsBox = createExternalBox<MainCapabilities>({
      canCompact: true,
      canClearMain: true,
      canNewMain: true,
      clearingMain: false,
    });
    const onStartingNewMainChange = vi.fn();
    const compactGate = deferred<void>();
    const compactSpy = vi.fn(() => compactGate.promise);
    const clearSpy = vi.fn(async () => {});
    const notifySpy = vi.fn();
    let captured: ConversationRowMenuViewProps | null = null;

    const renderView = (props: ConversationRowMenuViewProps) => {
      captured = props;
      return <div data-newmain={String(Boolean(props.startingNewMain))} />;
    };

    const renderMenu = (startingNewMain?: boolean) =>
      root.render(
        <ConversationRowMenu
          s={s}
          main
          anchor={anchor}
          onClose={vi.fn()}
          deps={{
            scopeKey: scopeBox.get,
            subscribeScopeKey: scopeBox.subscribe,
            canDelete: false,
            mainCapabilities: () => capsBox.get(),
            subscribeMainCapabilities: capsBox.subscribe,
            startingNewMain,
            onStartingNewMainChange,
            menuDeps: { compact: compactSpy, clearMain: clearSpy, notify: notifySpy },
            renderView,
          }}
        />,
      );

    act(() => renderMenu());
    expect(captured!.items.map((i) => i.key)).toEqual(["compact", "clear", "newmain", "copy", "end"]);

    // Downgrade canNewMain before commit: stale onSelect fails closed.
    const staleNewMain = captured!.items.find((i) => i.key === "newmain")!;
    capsBox.setSilent({ ...capsBox.get(), canNewMain: false });
    act(() => { staleNewMain.onSelect(); });
    expect(captured!.startingNewMain).toBe(false);
    expect(onStartingNewMainChange).not.toHaveBeenCalled();

    // Restore capability and open the new-main dialog.
    capsBox.set({ ...capsBox.get(), canNewMain: true });
    act(() => { captured!.items.find((i) => i.key === "newmain")!.onSelect(); });
    expect(captured!.startingNewMain).toBe(true);
    expect(onStartingNewMainChange).toHaveBeenLastCalledWith(true);

    // Start an in-flight compact request that will reject after an ABA scope burst.
    const compactPromise = captured!.items.find((i) => i.key === "compact")!.onSelect() as Promise<void>;
    expect(compactSpy).toHaveBeenCalledTimes(1);

    // ABA scope burst acct-1 -> acct-2 -> acct-1 resets startingNewMain and bumps newMainDialogKey.
    const keyBeforeAba = captured!.newMainDialogKey;
    act(() => {
      scopeBox.set("acct-2");
      scopeBox.set("acct-1");
    });
    expect(captured!.startingNewMain).toBe(false);
    expect(captured!.newMainDialogKey).not.toBe(keyBeforeAba);
    expect(onStartingNewMainChange).toHaveBeenLastCalledWith(false);

    // Stale compact rejection after ABA must never trigger a toast.
    await act(async () => {
      compactGate.reject(new Error("busy"));
      await compactPromise;
    });
    expect(notifySpy).not.toHaveBeenCalled();

    // Switching controlled startingNewMain=true -> uncontrolled resets fail-closed to false.
    act(() => renderMenu(true));
    expect(captured!.startingNewMain).toBe(true);
    act(() => renderMenu(undefined));
    expect(captured!.startingNewMain).toBe(false);
  });
});

describe("NewMainDialog transitions, 409 main_changed, and unmount/ABA races", () => {
  it("submits carry=true by default, resets carry on reopen, guards double-click, and handles 409 main_changed", async () => {
    const s = makeMainSession();
    const startGate = deferred<NewMainResult>();
    const startNewMain = vi.fn(() => startGate.promise);
    const refresh = vi.fn(async () => {});
    const navigateMain = vi.fn();
    const onNavigate = vi.fn();
    const onClose = vi.fn();
    const notify = vi.fn();
    const forgetCleared = vi.fn();

    act(() => root.render(
      <NewMainDialog
        open
        onClose={onClose}
        onNavigate={onNavigate}
        session={s}
        scopeKey="scope-1"
        deps={{ startNewMain, refresh, navigateMain, notify, forgetCleared }}
      />,
    ));

    // Toggle summary switch off, close, and reopen: default carry=true must be restored.
    const toggle = document.querySelector<HTMLButtonElement>('button[role="switch"]')!;
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    act(() => toggle.click());
    expect(toggle.getAttribute("aria-checked")).toBe("false");

    act(() => root.render(
      <NewMainDialog open={false} onClose={onClose} onNavigate={onNavigate} session={s} scopeKey="scope-1" deps={{ startNewMain, refresh, navigateMain, notify, forgetCleared }} />,
    ));
    act(() => root.render(
      <NewMainDialog open onClose={onClose} onNavigate={onNavigate} session={s} scopeKey="scope-1" deps={{ startNewMain, refresh, navigateMain, notify, forgetCleared }} />,
    ));
    expect(document.querySelector<HTMLButtonElement>('button[role="switch"]')!.getAttribute("aria-checked")).toBe("true");

    // Double-click confirm: single-flight ensures only one request is sent, and Cancel is disabled while busy.
    const confirmBtn = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("assistant-assistantchat.new_main")))!;
    const cancelBtn = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("common.cancel")))!;
    act(() => {
      confirmBtn.click();
      confirmBtn.click();
    });
    expect(startNewMain).toHaveBeenCalledTimes(1);
    expect(startNewMain).toHaveBeenCalledWith(true, "main-1");
    expect(cancelBtn.disabled).toBe(true);

    // Resolve with 409 main_changed: refreshes hub state, closes dialog, navigates to main, and shows info toast.
    await act(async () => {
      startGate.reject(new ApiError(409, "main changed", { code: "main_changed" }));
      await Promise.resolve();
    });
    await vi.waitFor(() => {
      expect(refresh).toHaveBeenCalledTimes(1);
      expect(forgetCleared).toHaveBeenCalledTimes(1);
      expect(onClose).toHaveBeenCalledTimes(1);
      expect(navigateMain).toHaveBeenCalledTimes(1);
      expect(onNavigate).toHaveBeenCalledTimes(1);
      expect(notify).toHaveBeenCalledWith(t("assistant-assistantchat.new_main_elsewhere"), "info");
    });
  });

  it("fences stale NewMainDialog resolution after unmount and after scopeKey ABA bursts", async () => {
    const s = makeMainSession();
    const gateA = deferred<NewMainResult>();
    const startNewMain = vi.fn(() => gateA.promise);
    const refresh = vi.fn(async () => {});
    const navigateMain = vi.fn();
    const onClose = vi.fn();
    const notify = vi.fn();

    const mount = (scopeKey: string) =>
      root.render(
        <NewMainDialog
          open
          onClose={onClose}
          session={s}
          scopeKey={scopeKey}
          deps={{ startNewMain, refresh, navigateMain, notify, forgetCleared: vi.fn() }}
        />,
      );

    act(() => mount("scope-A"));
    const confirmBtn = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("assistant-assistantchat.new_main")))!;
    act(() => confirmBtn.click());
    expect(startNewMain).toHaveBeenCalledTimes(1);

    // ABA scope burst scope-A -> scope-B -> scope-A while request is in flight.
    act(() => mount("scope-B"));
    act(() => mount("scope-A"));

    await act(async () => {
      gateA.resolve({ ok: true, archived: "main-1", carried: true });
      await Promise.resolve();
    });
    await flush();
    expect(refresh).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(navigateMain).not.toHaveBeenCalled();
    expect(notify).not.toHaveBeenCalled();

    // Start a second request and unmount before it settles: late error must not notify or close.
    const gateB = deferred<NewMainResult>();
    startNewMain.mockReturnValueOnce(gateB.promise);
    const confirmBtn2 = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("assistant-assistantchat.new_main")))!;
    act(() => confirmBtn2.click());
    expect(startNewMain).toHaveBeenCalledTimes(2);
    act(() => root.unmount());
    await act(async () => {
      gateB.reject(new Error("hub offline"));
      await Promise.resolve();
    });
    await flush();
    expect(notify).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("completes close, navigation, and toast when refresh() flips the row out of main (409 main_changed)", async () => {
    const s = makeMainSession();
    const onClose = vi.fn();
    const onNavigate = vi.fn();
    const navigateMain = vi.fn();
    const notify = vi.fn();
    const startNewMain = vi.fn(async () => {
      throw new ApiError(409, "main changed", { code: "main_changed" });
    });

    const renderWithMain = (isMain: boolean) =>
      root.render(
        <ConversationRowMenu
          s={s}
          main={isMain}
          anchor={anchor}
          onClose={onClose}
          onNavigate={onNavigate}
          deps={{
            canDelete: false,
            mainCapabilities: { canCompact: true, canClearMain: true, canNewMain: true, clearingMain: false },
            newMainDialogDeps: {
              startNewMain,
              refresh: async () => {
                // Simulate store.assistant.main updating during refreshAssistant(), flipping `main` to false.
                act(() => renderWithMain(false));
              },
              navigateMain,
              notify,
              forgetCleared: vi.fn(),
            },
          }}
        />,
      );

    act(() => renderWithMain(true));
    const newMainItem = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) =>
      (el.textContent ?? "").includes(t("assistant-assistantchat.new_main_ellipsis")),
    )!;
    await act(async () => {
      newMainItem.click();
    });
    let confirmBtn: HTMLButtonElement | undefined;
    await vi.waitFor(async () => {
      await act(async () => {
        await Promise.resolve();
      });
      confirmBtn = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((b) =>
        b.textContent?.includes(t("assistant-assistantchat.new_main")),
      );
      expect(confirmBtn).toBeDefined();
    });
    await act(async () => {
      confirmBtn!.click();
    });
    await vi.waitFor(() => {
      expect(onClose).toHaveBeenCalledTimes(1);
      expect(navigateMain).toHaveBeenCalledTimes(1);
      expect(onNavigate).toHaveBeenCalledTimes(1);
      expect(notify).toHaveBeenCalledWith(t("assistant-assistantchat.new_main_elsewhere"), "info");
    });
  });
});

describe("clearMainContext busy tracking & scope-change fencing", () => {
  it("tracks in-flight clearing state and fences navigation/toasts when scope drifts mid-flight", async () => {
    const postGate = deferred<void>();
    let scopeCurrent = true;
    const refresh = vi.fn(async () => {});
    const home = vi.fn();
    const afterHome = vi.fn();
    const toast = vi.fn();

    const pending = clearMainContext("main-1", {
      post: () => postGate.promise,
      refresh,
      home,
      afterHome,
      toast,
      isScopeCurrent: () => scopeCurrent,
    });
    expect(isMainClearing("main-1")).toBe(true);
    expect(mainCleared()).toBe(true);

    // Scope changes while request is in flight.
    scopeCurrent = false;
    postGate.resolve();
    await pending;

    expect(isMainClearing("main-1")).toBe(false);
    expect(mainCleared()).toBe(false);
    expect(refresh).not.toHaveBeenCalled();
    expect(home).not.toHaveBeenCalled();
    expect(afterHome).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("emits COMPACT_REQUESTED_EVENT constant cleanly for compaction tracking", () => {
    expect(COMPACT_REQUESTED_EVENT).toBe("persona:compact-requested");
  });
});

function dialogButton(key: string) {
  const button = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button'))
    .find((b) => b.textContent?.trim() === t(key));
  if (!button) throw new Error("Dialog action was missing");
  return button;
}

describe("Main chat mutations from competing devices and menu entries", () => {
  it("closes an open sidebar confirmation on capability withdrawal and rechecks before the next render", async () => {
    const caps = createExternalBox({ canNewMain: true });
    const post = vi.fn();
    const close = vi.fn();
    act(() => root.render(<ConversationRowMenu s={makeMainSession()} main anchor={anchor} onClose={close}
      deps={{ mainCapabilities: caps.get, subscribeMainCapabilities: caps.subscribe, newMainDialogDeps: { startNewMain: post } }} />));
    const entry = Array.from(document.querySelectorAll<HTMLElement>('[role="menuitem"]')).find(el => el.textContent?.includes(t("assistant-assistantchat.new_main_ellipsis")))!;
    await act(async () => entry.click());
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeTruthy());
    caps.setSilent({ canNewMain: false });
    act(() => dialogButton("assistant-assistantchat.new_main").click());
    expect(post).not.toHaveBeenCalled();
    act(() => caps.notify());
    expect(close).toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("serializes Clear and New from separate menus so the successful dock marker survives", async () => {
    const gate = deferred<NewMainResult>();
    const post = vi.fn(() => gate.promise);
    act(() => root.render(<NewMainDialog open session={makeMainSession()} onClose={() => {}}
      deps={{ startNewMain: post, refresh: async () => {}, navigateMain: () => {}, notify: () => {} }} />));
    const clearGate = deferred<void>();
    let clearing!: Promise<boolean>;
    act(() => { clearing = clearMainContext("main-1", { post: () => clearGate.promise, refresh: async () => {}, home: () => {} }); });
    expect(dialogButton("assistant-assistantchat.new_main").disabled).toBe(true);
    act(() => dialogButton("assistant-assistantchat.new_main").click());
    expect(post).not.toHaveBeenCalled();
    await act(async () => { clearGate.resolve(); await clearing; });
    expect(mainCleared()).toBe(true);

    act(() => dialogButton("assistant-assistantchat.new_main").click());
    const competingClear = vi.fn();
    await clearMainContext("main-1", { post: competingClear });
    expect(competingClear).not.toHaveBeenCalled();
    await act(async () => gate.resolve({ ok: true }));
    expect(mainCleared()).toBe(true);
  });

  it.each([false, true])("opens the winning main from a side chat unless the user navigates (leave=%s)", async (leave) => {
    navigate({ name: "persona", sid: "side" });
    const home = vi.fn(), closeDrawer = vi.fn();
    const answer = deferred<Response>();
    vi.stubGlobal("fetch", (url: string) => String(url).includes("/main/clear") ? answer.promise
      : Promise.resolve(Response.json({ machine_id: "hub", main: "replacement", conversations: [] })));
    let view!: ConversationRowMenuViewProps;
    act(() => root.render(<ConversationRowMenu s={makeMainSession()} main anchor={anchor} onClose={() => {}}
      onNavigate={closeDrawer} deps={{ navigate: home, renderView: (v) => { view = v; return null; } }} />));
    let pending!: unknown;
    act(() => { pending = view.items.find(i => i.key === "clear")!.onSelect(); });
    if (leave) navigate({ name: "history" });
    await act(async () => { answer.resolve(Response.json({ code: "main_changed", error: "changed" }, { status: 409 })); await pending; });
    expect(getState().assistant?.main).toBe("replacement");
    expect(home).toHaveBeenCalledTimes(leave ? 0 : 1);
    expect(closeDrawer).toHaveBeenCalledTimes(leave ? 0 : 1);
    navigate({ name: "persona" });
  });

  it("retries only the read after another device wins and the refresh fails", async () => {
    const start = vi.fn(async () => { throw new ApiError(409, "changed", { code: "main_changed" }); });
    const refresh = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(true);
    const home = vi.fn(), close = vi.fn(), notify = vi.fn();
    act(() => root.render(<NewMainDialog open session={makeMainSession()} onClose={close}
      deps={{ startNewMain: start, refresh, navigateMain: home, notify }} />));
    await act(async () => dialogButton("assistant-assistantchat.new_main").click());
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("offline");
    expect(close).not.toHaveBeenCalled();
    expect(home).not.toHaveBeenCalled();
    await act(async () => dialogButton("personaassistant.retry").click());
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(start).toHaveBeenCalledTimes(1);
    expect(home).toHaveBeenCalledOnce();
    expect(notify).toHaveBeenCalledWith(t("assistant-assistantchat.new_main_elsewhere"), "info");
  });

  it.each([
    { pushFirst: true, leave: false },
    { pushFirst: true, leave: true },
    { pushFirst: false, leave: false },
  ])("keeps a read-only retry after the sidebar dialog unmounts (push first=$pushFirst, leave and return=$leave)", async ({ pushFirst, leave }) => {
    navigate({ name: "persona", sid: "side" });
    const reply = deferred<NewMainResult>();
    const start = vi.fn(() => reply.promise);
    const refresh = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(true);
    const home = vi.fn();
    function SidebarMenu() {
      const main = useStore((state) => state.assistant?.main);
      return main === "main-1" && <ConversationRowMenu s={makeMainSession()} main anchor={anchor} onClose={() => {}}
        deps={{ newMainDialogDeps: { startNewMain: start, refresh, navigateMain: home } }} />;
    }
    act(() => root.render(<><SidebarMenu /><Toasts /></>));
    const newMain = Array.from(document.querySelectorAll<HTMLElement>('[role="menuitem"]'))
      .find(el => el.textContent?.includes(t("assistant-assistantchat.new_main_ellipsis")))!;
    await act(async () => newMain.click());
    await act(async () => dialogButton("assistant-assistantchat.new_main").click());

    const conflict = () => reply.reject(new ApiError(409, "changed", { code: "main_changed" }));
    if (!pushFirst) {
      await act(async () => conflict());
      expect(dialogButton("personaassistant.retry")).toBeTruthy();
    }
    vi.stubGlobal("fetch", async () => Response.json({ machine_id: "hub", main: "replacement", conversations: [] }));
    await act(async () => { await refreshAssistant(); });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    if (pushFirst) await act(async () => conflict());
    expect(refresh).toHaveBeenCalledOnce();
    expect(home).not.toHaveBeenCalled();
    const notice = Array.from(document.querySelectorAll<HTMLButtonElement>('[aria-live="polite"] button'))
      .find(button => button.textContent?.trim() === t("personaassistant.retry"));
    expect(notice).toBeDefined();
    expect(notice!.parentElement?.textContent).toContain("offline");
    if (leave) {
      navigate({ name: "history" });
      navigate({ name: "persona", sid: "side" });
    }
    await act(async () => notice!.click());
    expect(refresh).toHaveBeenCalledTimes(leave ? 1 : 2);
    expect(home).toHaveBeenCalledTimes(leave ? 0 : 1);
    expect(start).toHaveBeenCalledOnce();
    navigate({ name: "persona" });
  });

  it("finishes when a store update unmounts the dialog before the mutation reply", async () => {
    const gate = deferred<NewMainResult>();
    const home = vi.fn(), refresh = vi.fn(async () => true);
    function CurrentMain() {
      const sid = useStore((state) => state.assistant?.main);
      return sid === "main-1" ? <NewMainDialog open session={makeMainSession()} onClose={() => {}}
        deps={{ startNewMain: () => gate.promise, refresh, navigateMain: home, notify: () => {} }} /> : null;
    }
    act(() => root.render(<CurrentMain />));
    act(() => dialogButton("assistant-assistantchat.new_main").click());
    vi.stubGlobal("fetch", async () => Response.json({ machine_id: "hub", main: "replacement", conversations: [] }));
    await act(async () => { await refreshAssistant(); });
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeNull());
    await act(async () => gate.resolve({ ok: true }));
    expect(refresh).toHaveBeenCalledOnce();
    expect(home).toHaveBeenCalledOnce();
  });
});
