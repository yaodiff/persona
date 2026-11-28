// @vitest-environment jsdom
// Controlled panels supply explicit agent menu state, node actions and mark-seen handlers; default fallbacks stay pinned too.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { AGENT_ARCHIVE_CLOSE_FEATURE, AGENT_ARCHIVES_FEATURE } from "../lib/agent-archives";
import type { NodeActionDeps } from "../lib/node-actions";
import type { Machine, Session } from "../lib/types";
import type { MenuItem } from "../ui/menu-model";
import type { AgentRowMenuProps, MarkSeenDialogProps } from "./AgentMenus";

const mocks = vi.hoisted(() => ({
  useStore: vi.fn(),
  getState: vi.fn(),
  markSeen: vi.fn(),
  refresh: vi.fn(),
  refreshTodos: vi.fn(),
  flipPin: vi.fn(),
  flipMute: vi.fn(),
  handToPersona: vi.fn(),
  archiveAgent: vi.fn(),
  closeAgentAction: vi.fn(),
  openCloseAgent: vi.fn(),
  unlinkAgentFromTodo: vi.fn(),
  attachLive: vi.fn(),
  defaultNodeDeps: vi.fn(),
  copyText: vi.fn(),
  toast: vi.fn(),
  preloadSessionDialogs: vi.fn(),
}));

vi.mock("../lib/store", () => ({
  useStore: mocks.useStore,
  getState: mocks.getState,
  markSeen: mocks.markSeen,
  refresh: mocks.refresh,
  refreshTodos: mocks.refreshTodos,
}));
vi.mock("../lib/agent-actions", () => ({
  flipPin: mocks.flipPin,
  flipMute: mocks.flipMute,
  handToPersona: mocks.handToPersona,
}));
vi.mock("../lib/agent-archives", async (importOriginal) => ({
  ...await importOriginal<typeof import("../lib/agent-archives")>(),
  archiveAgent: mocks.archiveAgent,
}));
vi.mock("../lib/close-agent", () => ({
  closeAgentAction: mocks.closeAgentAction,
  openCloseAgent: mocks.openCloseAgent,
}));
vi.mock("../lib/todos", () => ({
  unlinkAgentFromTodo: mocks.unlinkAgentFromTodo,
}));
vi.mock("../lib/attach-live", () => ({
  attachLive: mocks.attachLive,
}));
vi.mock("../lib/node-actions", async (importOriginal) => ({
  ...await importOriginal<typeof import("../lib/node-actions")>(),
  defaultNodeDeps: mocks.defaultNodeDeps,
}));
vi.mock("../ui/atoms", async (importOriginal) => ({
  ...await importOriginal<typeof import("../ui/atoms")>(),
  copyText: mocks.copyText,
  toast: mocks.toast,
}));
vi.mock("./chat/SessionDialogs", async (importOriginal) => ({
  ...await importOriginal<typeof import("./chat/SessionDialogs")>(),
  preloadSessionDialogs: mocks.preloadSessionDialogs,
}));

const { appArchiveDeps } = await import("../lib/agent-archives");
const { agentRowMenuItems, archiveCloseItems, closeItems, AgentRowMenu, MarkSeenDialog } = await import("./AgentMenus");
const { initI18n, t } = await import("../i18n");
const { markAllSeenLabel } = await import("../lib/state-labels");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const archiveFeatures = [AGENT_ARCHIVES_FEATURE, AGENT_ARCHIVE_CLOSE_FEATURE] as const;

const baseSession = (overrides: Partial<Session> = {}): Session => ({
  machine_id: "example-machine",
  session_id: "example-session",
  title: "Controlled agent",
  cwd: "/repo/example",
  status: "idle",
  state: "idle",
  agent: "claude",
  pane_id: "w1:p1",
  todo: { id: "todo-1", title: "Ship slice" },
  capabilities: { read: true, send: true, rename: true, terminal: true },
  ...overrides,
});

const attachableSession = (): Session => baseSession({
  session_id: "attachable-session",
  source_kind: "codex_history",
  attached: false,
  capabilities: { read: true, send: true, attach: true, rename: true, terminal: true },
});

const makeNodeDeps = (): NodeActionDeps => ({
  post: vi.fn().mockResolvedValue({}),
  renameWorkspace: vi.fn().mockResolvedValue({}),
  renameTab: vi.fn().mockResolvedValue({}),
  launchers: vi.fn().mockResolvedValue([]),
  newAgent: vi.fn(),
  openSession: vi.fn(),
  copy: vi.fn(),
  notify: vi.fn(),
  changed: vi.fn(),
});

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
};

let host: HTMLDivElement;
let anchor: HTMLButtonElement;
let root: Root;

const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); }); // NodeMenu comes in a chunk of its own
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};

const expectNoDefaultSingletons = () => {
  expect(mocks.useStore).not.toHaveBeenCalled();
  expect(mocks.getState).not.toHaveBeenCalled();
  expect(mocks.markSeen).not.toHaveBeenCalled();
  expect(mocks.refresh).not.toHaveBeenCalled();
  expect(mocks.refreshTodos).not.toHaveBeenCalled();
  expect(mocks.flipPin).not.toHaveBeenCalled();
  expect(mocks.flipMute).not.toHaveBeenCalled();
  expect(mocks.handToPersona).not.toHaveBeenCalled();
  expect(mocks.archiveAgent).not.toHaveBeenCalled();
  expect(mocks.closeAgentAction).not.toHaveBeenCalled();
  expect(mocks.openCloseAgent).not.toHaveBeenCalled();
  expect(mocks.unlinkAgentFromTodo).not.toHaveBeenCalled();
  expect(mocks.attachLive).not.toHaveBeenCalled();
  expect(mocks.defaultNodeDeps).not.toHaveBeenCalled();
  expect(mocks.copyText).not.toHaveBeenCalled();
  expect(mocks.toast).not.toHaveBeenCalled();
  expect(mocks.preloadSessionDialogs).not.toHaveBeenCalled();
};

const menuButton = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("[role=menuitem]")].find((b) => b.textContent?.includes(label));

const dialogButton = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((b) => b.textContent?.trim() === label);

beforeEach(async () => {
  await initI18n("en");
  vi.clearAllMocks();
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }));
  Element.prototype.scrollIntoView = vi.fn();
  mocks.refresh.mockResolvedValue(undefined);
  mocks.refreshTodos.mockResolvedValue(undefined);
  mocks.unlinkAgentFromTodo.mockResolvedValue(undefined);
  mocks.attachLive.mockResolvedValue(undefined);
  mocks.archiveAgent.mockResolvedValue({ ok: true });
  mocks.closeAgentAction.mockImplementation((s: Session) => ({ onSelect: () => mocks.openCloseAgent(s) }));
  mocks.defaultNodeDeps.mockReturnValue(makeNodeDeps());
  mocks.getState.mockReturnValue({
    features: [...archiveFeatures],
    machines: [{ id: "example-machine", name: "Studio computer", online: true } as Machine],
    pins: { spaces: [], sessions: [] },
    mutes: { spaces: [], sessions: [], allowed: [] },
  });
  mocks.useStore.mockImplementation((selector: (st: unknown) => unknown) => selector(mocks.getState()));
  host = document.createElement("div");
  anchor = document.createElement("button");
  document.body.append(host, anchor);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  anchor.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("AgentMenus controlled mode", () => {
  it("routes all agentRowMenuItems, archiveCloseItems and closeItems callbacks without touching default singletons", async () => {
    const s = baseSession();
    const onRename = vi.fn();
    const onDelete = vi.fn();
    const onLinkTodo = vi.fn();
    const onAttach = vi.fn();
    const onTogglePin = vi.fn();
    const onToggleMute = vi.fn();
    const onHandToPersona = vi.fn();
    const onUnlinkTodo = vi.fn().mockRejectedValueOnce(new Error("unlink refused"));
    const onCopy = vi.fn();
    const onArchive = vi.fn();
    const onCloseAgent = vi.fn();
    const onNotice = vi.fn();

    const items = agentRowMenuItems(s, {
      pinned: false,
      muted: true,
      features: archiveFeatures,
      machineName: "Controlled box",
      onRename,
      onDelete,
      onLinkTodo,
      onAttach,
      onTogglePin,
      onToggleMute,
      onHandToPersona,
      onUnlinkTodo,
      onCopy,
      onArchive,
      onCloseAgent,
      onNotice,
    });

    const pick = (key: string) => items.find((item) => item.key === key)!;
    pick("pin").onSelect();
    pick("rename").onSelect();
    pick("mute").onSelect();
    pick("persona").onSelect();
    pick("link-todo").onSelect();
    pick("unlink-todo").onSelect();
    pick("cwd").onSelect();
    pick("resume").onSelect();
    pick("archive").onSelect();
    pick("close").onSelect();

    const attachable = attachableSession();
    const attachItems = agentRowMenuItems(attachable, {
      pinned: false,
      muted: false,
      features: archiveFeatures,
      onRename,
      onDelete,
      onAttach,
      onCloseAgent,
      onArchive,
    });
    attachItems.find((item) => item.key === "attach")!.onSelect();
    await flush();

    expect(onAttach).toHaveBeenCalledWith(attachable);
    expect(onTogglePin).toHaveBeenCalledWith(s);
    expect(onRename).toHaveBeenCalledWith(s);
    expect(onToggleMute).toHaveBeenCalledWith(s);
    expect(onHandToPersona).toHaveBeenCalledWith(s, "Controlled box");
    expect(onLinkTodo).toHaveBeenCalledWith(s);
    expect(onUnlinkTodo).toHaveBeenCalledWith(s, "todo-1");
    expect(onNotice).toHaveBeenCalledWith("unlink refused", "error");
    expect(onCopy).toHaveBeenNthCalledWith(1, "/repo/example");
    expect(onCopy).toHaveBeenNthCalledWith(2, expect.stringContaining("example-session"));
    expect(onArchive).toHaveBeenCalledWith(s);
    expect(onCloseAgent).toHaveBeenCalledWith(s);

    // Busy archive and busy close (without plain close capability) stay disabled and do not invoke callbacks.
    const busyNoClose = baseSession({ status: "working", state: "working", capabilities: { read: true, send: true, terminal: false } });
    const busyItems = archiveCloseItems(busyNoClose, archiveFeatures, { onArchive, onCloseAgent });
    expect(busyItems.find((i) => i.key === "archive")?.disabled).toBe(true);
    expect(busyItems.find((i) => i.key === "close")?.disabled).toBe(true);
    busyItems.forEach((i) => i.onSelect());
    expect(onArchive).toHaveBeenCalledTimes(1);
    expect(onCloseAgent).toHaveBeenCalledTimes(1);

    // Standalone closeItems respects controlled callback and skips default closeAgentAction/openCloseAgent.
    const standaloneClose = closeItems(s, archiveFeatures, onCloseAgent);
    standaloneClose[0].onSelect();
    expect(onCloseAgent).toHaveBeenCalledTimes(2);
    expectNoDefaultSingletons();
  });

  it("renders AgentRowMenu with controlled state and nodeDeps without touching store, defaultNodeDeps or dialog preload", async () => {
    const s = baseSession();
    const nodeDeps = makeNodeDeps();
    const onClose = vi.fn();
    const onTogglePin = vi.fn();
    const onHandToPersona = vi.fn();
    const onArchive = vi.fn();
    const onCloseAgent = vi.fn();

    const mountRowMenu = async (props: Partial<AgentRowMenuProps> = {}) => {
      await act(async () => root.render(
        <AgentRowMenu
          s={s}
          anchor={anchor}
          onClose={onClose}
          onRename={vi.fn()}
          onDelete={vi.fn()}
          pinned={false}
          muted={false}
          features={archiveFeatures}
          online
          machineName="Custom machine"
          nodeDeps={nodeDeps}
          onTogglePin={onTogglePin}
          onToggleMute={vi.fn()}
          onHandToPersona={onHandToPersona}
          onCopy={vi.fn()}
          onArchive={onArchive}
          onCloseAgent={onCloseAgent}
          {...props}
        />,
      ));
      await flush();
    };

    await mountRowMenu();
    expect(menuButton(t("agentmenus.pin"))).toBeTruthy();
    expect(menuButton(t("lib-node-actions.copy_session_id"))).toBeTruthy();

    await act(async () => { menuButton(t("agentmenus.hand_to_persona"))!.click(); });
    expect(onHandToPersona).toHaveBeenCalledWith(s, "Custom machine");

    await mountRowMenu();
    await act(async () => { menuButton(t("lib-node-actions.copy_session_id"))!.click(); });
    expect(nodeDeps.copy).toHaveBeenCalledWith("example-session");
    expectNoDefaultSingletons();
  });

  it("resets inline destructive confirmation state in AgentRowMenu when switching sessions", async () => {
    const sessionA = baseSession({ session_id: "session-a" });
    const sessionB = baseSession({ session_id: "session-b" });
    const destructive = vi.fn();
    const destructiveItem = (sid: string): MenuItem[] => [{
      key: "danger-custom",
      label: "Purge scratch",
      danger: true,
      confirm: "Confirm purge?",
      onSelect: () => destructive(sid),
    }];

    const renderMenu = async (s: Session) => {
      await act(async () => root.render(
        <AgentRowMenu
          s={s}
          anchor={anchor}
          onClose={vi.fn()}
          onRename={vi.fn()}
          onDelete={vi.fn()}
          pinned={false}
          muted={false}
          features={archiveFeatures}
          online
          onArchive={vi.fn()}
          onCloseAgent={vi.fn()}
          nodeItems={destructiveItem(s.session_id)}
        />,
      ));
      await flush();
    };

    await renderMenu(sessionA);
    // First click arms inline confirmation for session A without executing.
    await act(async () => { menuButton("Purge scratch")!.click(); });
    expect(destructive).not.toHaveBeenCalled();
    expect(menuButton("Confirm purge?")).toBeTruthy();

    // Switching to session B while mounted clears confirmation so a single click cannot confirm session B.
    await renderMenu(sessionB);
    expect(menuButton("Confirm purge?")).toBeUndefined();
    expect(menuButton("Purge scratch")).toBeTruthy();
    await act(async () => { menuButton("Purge scratch")!.click(); });
    expect(destructive).not.toHaveBeenCalled();
    expect(menuButton("Confirm purge?")).toBeTruthy();

    await act(async () => { menuButton("Confirm purge?")!.click(); });
    expect(destructive).toHaveBeenCalledTimes(1);
    expect(destructive).toHaveBeenCalledWith("session-b");
    expectNoDefaultSingletons();
  });

  it("handles sync, async, error recovery, and ABA session-switch races in controlled MarkSeenDialog", async () => {
    const s1 = baseSession({ session_id: "s1" });
    const s2 = baseSession({ session_id: "s2" });
    const s3 = baseSession({ session_id: "s3" });
    const onClose = vi.fn();
    const onDone = vi.fn();
    const onNotice = vi.fn();

    const mountDialog = async (props: Partial<MarkSeenDialogProps>) => {
      await act(async () => root.render(
        <MarkSeenDialog list={[s1]} onClose={onClose} onDone={onDone} onNotice={onNotice} {...props} />,
      ));
      await flush();
    };

    // 1) Synchronous onMarkSeen forwards release ?? list and closes immediately.
    const syncMark = vi.fn();
    await mountDialog({ list: [s1], release: [s1, s2], onMarkSeen: syncMark });
    await act(async () => { dialogButton(markAllSeenLabel(1))!.click(); });
    expect(syncMark).toHaveBeenCalledWith([s1, s2]);
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);

    // 2) Async onMarkSeen rejects -> stays open, clears busy lock, reports error via alert and onNotice.
    const failMark = vi.fn().mockRejectedValueOnce(new Error("hub unavailable"));
    await mountDialog({ list: [s1], onMarkSeen: failMark });
    await act(async () => { dialogButton(markAllSeenLabel(1))!.click(); });
    await flush();
    expect(document.querySelector("[role=dialog] [role=alert]")?.textContent).toBe("hub unavailable");
    expect(onNotice).toHaveBeenCalledWith("hub unavailable", "error");
    expect(dialogButton(markAllSeenLabel(1))!.disabled).toBe(false);
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);

    // 3) ABA race: start async confirm on list A -> switch to list B (error & busy clear) -> switch back to list A
    //    and start a second confirm; settling the stale first promise must not close, call onDone, or unlock the active confirm.
    const pendingA1 = deferred<void>();
    const pendingA2 = deferred<void>();
    const pendingB = deferred<void>();
    const asyncMark: Mock<(sessions: Session[]) => Promise<void>> = vi.fn()
      .mockReturnValueOnce(pendingA1.promise)
      .mockReturnValueOnce(pendingB.promise)
      .mockReturnValueOnce(pendingA2.promise);

    // Close dialog (list=null) then reopen on list A: prior error is cleared on scope transition.
    await mountDialog({ list: null, onMarkSeen: asyncMark });
    await mountDialog({ list: [s1], onMarkSeen: asyncMark });
    expect(document.querySelector("[role=dialog] [role=alert]")).toBeNull();
    await act(async () => { dialogButton(markAllSeenLabel(1))!.click(); });
    expect(dialogButton(markAllSeenLabel(1))!.disabled).toBe(true);

    // Switch to list B ([s3]): pending lock clears immediately.
    await mountDialog({ list: [s3], onMarkSeen: asyncMark });
    expect(dialogButton(markAllSeenLabel(1))!.disabled).toBe(false);
    await act(async () => { dialogButton(markAllSeenLabel(1))!.click(); });
    expect(dialogButton(markAllSeenLabel(1))!.disabled).toBe(true);

    // Switch back to list A ([s1]) and start a fresh confirmation.
    await mountDialog({ list: [s1], onMarkSeen: asyncMark });
    expect(dialogButton(markAllSeenLabel(1))!.disabled).toBe(false);
    await act(async () => { dialogButton(markAllSeenLabel(1))!.click(); });
    expect(dialogButton(markAllSeenLabel(1))!.disabled).toBe(true);

    // Resolve stale A1 and reject stale B while A2 is active -> neither affects active A2.
    await act(async () => {
      pendingA1.resolve(undefined);
      pendingB.reject(new Error("stale B error"));
    });
    await flush();
    expect(document.querySelector("[role=dialog] [role=alert]")).toBeNull();
    expect(dialogButton(markAllSeenLabel(1))!.disabled).toBe(true);
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);

    // Resolving active A2 finishes cleanly.
    await act(async () => { pendingA2.resolve(undefined); });
    await flush();
    expect(onDone).toHaveBeenCalledTimes(2);
    expect(onClose).toHaveBeenCalledTimes(2);
    expectNoDefaultSingletons();
  });
});

describe("AgentMenus default compatibility", () => {
  it("keeps default store, action helpers, clipboard, toast, preload and NodeMenu fallbacks when optional props are omitted", async () => {
    const s = baseSession();
    const onRename = vi.fn();
    const onDelete = vi.fn();
    const onLinkTodo = vi.fn();

    const items = agentRowMenuItems(s, { pinned: false, muted: false, onRename, onDelete, onLinkTodo });
    const pick = (key: string) => items.find((item) => item.key === key)!;

    pick("pin").onSelect();
    pick("mute").onSelect();
    pick("persona").onSelect();
    pick("unlink-todo").onSelect();
    pick("cwd").onSelect();
    pick("resume").onSelect();
    pick("archive").onSelect();
    pick("close").onSelect();
    const attachable = attachableSession();
    agentRowMenuItems(attachable, { pinned: false, muted: false, onRename, onDelete }).find((item) => item.key === "attach")!.onSelect();
    await flush();

    await vi.waitFor(() => expect(mocks.attachLive).toHaveBeenCalledWith(attachable));
    expect(mocks.flipPin).toHaveBeenCalledWith(s);
    expect(mocks.flipMute).toHaveBeenCalledWith(s);
    expect(mocks.handToPersona).toHaveBeenCalledWith(s, "Studio computer");
    expect(mocks.unlinkAgentFromTodo).toHaveBeenCalledWith("todo-1", "example-machine", "example-session");
    expect(mocks.refreshTodos).toHaveBeenCalledOnce();
    expect(mocks.refresh).toHaveBeenCalledOnce();
    expect(mocks.toast).toHaveBeenCalledWith(t("agentmenus.unlinked"), "ok");
    expect(mocks.copyText).toHaveBeenCalledWith("/repo/example");
    expect(mocks.archiveAgent).toHaveBeenCalledWith(s, false, appArchiveDeps);
    expect(mocks.openCloseAgent).toHaveBeenCalledWith(s);

    // Default AgentRowMenu subscribes to useStore, preloads dialogs, and delegates trailing items to NodeMenu/defaultNodeDeps.
    await act(async () => root.render(
      <AgentRowMenu s={s} anchor={anchor} onClose={vi.fn()} onRename={onRename} onDelete={onDelete} />,
    ));
    await flush();
    expect(mocks.useStore).toHaveBeenCalled();
    expect(mocks.preloadSessionDialogs).toHaveBeenCalledTimes(1);
    expect(mocks.defaultNodeDeps).toHaveBeenCalled();

    // Default MarkSeenDialog calls store markSeen(release ?? list), onDone and onClose.
    const onDone = vi.fn();
    const onClose = vi.fn();
    const s2 = baseSession({ session_id: "s2" });
    await act(async () => root.render(<MarkSeenDialog list={[s]} release={[s, s2]} onClose={onClose} onDone={onDone} />));
    await flush();
    await act(async () => { dialogButton(markAllSeenLabel(1))!.click(); });
    expect(mocks.markSeen).toHaveBeenCalledWith([s, s2]);
    expect(onDone).toHaveBeenCalledOnce();
    expect(onClose).toHaveBeenCalledOnce();
  });
});
