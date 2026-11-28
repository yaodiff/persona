// @vitest-environment jsdom
// Controlled panels supply queue mutations, draft restoration, photo URL resolvers, transport feedback and notifications without touching default singletons; default callers keep every fallback.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SendFeedback } from "../../lib/message-send";
import type { Session } from "../../lib/types";
import type { QueuePanelProps } from "./QueuePanel";
import type { QueueStripProps } from "./QueueStrip";

const mocks = vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  return {
    api: vi.fn(),
    sessionPath: vi.fn((mid: string, sid: string) => `/sessions/${mid}/${sid}`),
    fileThumbURL: vi.fn((_mid: string, path: string, _cwd?: string, size?: number) => `/default-thumb?path=${encodeURIComponent(path)}&size=${size ?? 0}`),
    fileDownloadURL: vi.fn((_mid: string, path: string) => `/default-download?path=${encodeURIComponent(path)}&inline=1`),
    inSendOrder: vi.fn((_key: string, fn: () => Promise<unknown>) => fn()),
    outboxKey: vi.fn((mid: string, sid: string) => `${mid}/${sid}`),
    useMessageFeedback: vi.fn((): SendFeedback | undefined => "sending"),
    useStore: vi.fn(() => false),
    restoreQueued: vi.fn(async () => {}),
    toast: vi.fn(),
  };
});

vi.mock("../../lib/api", async (orig) => {
  const actual = await orig<typeof import("../../lib/api")>();
  return {
    ...actual,
    api: mocks.api,
    sessionPath: mocks.sessionPath,
    fileThumbURL: mocks.fileThumbURL,
    fileDownloadURL: mocks.fileDownloadURL,
  };
});
vi.mock("../../lib/outbox", () => ({
  inSendOrder: mocks.inSendOrder,
  outboxKey: mocks.outboxKey,
}));
vi.mock("../../lib/message-send", () => ({
  useMessageFeedback: mocks.useMessageFeedback,
}));
vi.mock("../../lib/store", () => ({
  useStore: mocks.useStore,
}));
vi.mock("./queue-restore", () => ({
  restoreQueued: mocks.restoreQueued,
}));
vi.mock("../../ui/atoms", async (orig) => {
  const actual = await orig<typeof import("../../ui/atoms")>();
  return { ...actual, toast: mocks.toast };
});

const { QueuePanel } = await import("./QueuePanel");
const { QueueStrip } = await import("./QueueStrip");
const { t } = await import("../../i18n");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const makeSession = (mid: string, sid: string, queued: string[], extra: Partial<Session> = {}): Session =>
  ({
    machine_id: mid,
    session_id: sid,
    agent: "claude",
    state: "working",
    cwd: "/workspace/example",
    queued,
    ...extra,
  }) as unknown as Session;

let host: HTMLDivElement;
let root: Root;

const renderStrip = async (props: QueueStripProps) => {
  await act(async () => {
    root.render(<QueueStrip {...props} />);
  });
};

const renderPanel = async (props: QueuePanelProps) => {
  await act(async () => {
    root.render(<QueuePanel {...props} />);
  });
};

const rows = () => [...host.querySelectorAll<HTMLElement>("[data-queue-row]")];
const within = (row: number, sel: string) => rows()[row].querySelector<HTMLElement>(sel)!;
const click = (el: Element) => act(() => { el.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
const key = (el: Element, k: string) => act(() => { el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })); });
const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};

const expectNoDefaultSingletons = () => {
  expect(mocks.api).not.toHaveBeenCalled();
  expect(mocks.sessionPath).not.toHaveBeenCalled();
  expect(mocks.fileThumbURL).not.toHaveBeenCalled();
  expect(mocks.fileDownloadURL).not.toHaveBeenCalled();
  expect(mocks.inSendOrder).not.toHaveBeenCalled();
  expect(mocks.outboxKey).not.toHaveBeenCalled();
  expect(mocks.useMessageFeedback).not.toHaveBeenCalled();
  expect(mocks.useStore).not.toHaveBeenCalled();
  expect(mocks.restoreQueued).not.toHaveBeenCalled();
  expect(mocks.toast).not.toHaveBeenCalled();
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.inSendOrder.mockImplementation((_key: string, fn: () => Promise<unknown>) => fn());
  mocks.useMessageFeedback.mockReturnValue("sending");
  mocks.useStore.mockReturnValue(false);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("QueueStrip and QueuePanel with controlled props", () => {
  it("supports QueueStrip renderPanel bypass without touching default singletons", async () => {
    const renderPanelProp = vi.fn((props: QueuePanelProps) => (
      <div data-custom-queue-strip={props.draftKey}>{props.session.queued?.length ?? 0}</div>
    ));
    const session = makeSession("host-a", "s-1", ["first", "second"]);

    await renderStrip({
      session,
      draftKey: "draft-custom",
      canEdit: true,
      renderPanel: renderPanelProp,
    });

    expect(renderPanelProp).toHaveBeenCalledTimes(1);
    expect(host.querySelector('[data-custom-queue-strip="draft-custom"]')?.textContent).toBe("2");
    expectNoDefaultSingletons();
  });

  it("handles edit, delete, reorder, send-now, photo URL fallback, and optimistic send status with zero default singleton calls", async () => {
    const photoPath = "/workspace/example/screen.png";
    const withPhoto = `Check layout\n[Image #1]\n<attached_images>\npath: ${photoPath}\n</attached_images>`;
    const session = makeSession("host-a", "s-1", [withPhoto, "Second message"]);
    const onEditQueue = vi.fn(async () => ({ queue: [withPhoto, "Second message"] }));
    const onSendNow = vi.fn(async () => {});
    const onRestoreQueued = vi.fn(async () => {});
    const onNotify = vi.fn();
    const resolveThumbURL = vi.fn((p: string, size: number) => `/shell/thumb?p=${encodeURIComponent(p)}&s=${size}`);
    const resolveDownloadURL = vi.fn((p: string, opts: { inline?: boolean }) => `/shell/raw?p=${encodeURIComponent(p)}&inline=${opts.inline ? 1 : 0}`);
    const getSendFeedback = vi.fn(() => "processing" as const);

    await renderPanel({
      session,
      draftKey: "draft-a",
      canEdit: true,
      optimistic: [{ id: "opt-1", raw: "Pending local send" }],
      onEditQueue,
      onSendNow,
      onRestoreQueued,
      onNotify,
      resolveThumbURL,
      resolveDownloadURL,
      getSendFeedback,
      offline: false,
    });

    // Optimistic row uses controlled feedback and offline state.
    const status = host.querySelector<HTMLElement>('[role="status"]');
    expect(status?.getAttribute("title")).toBe(t("chat-segmentview.processing"));
    expect(getSendFeedback).toHaveBeenCalledWith("opt-1");

    // Photo thumbnail uses controlled resolvers and falls back thumb -> download -> icon.
    const img = rows()[0].querySelector<HTMLImageElement>("[data-queue-photo-thumb]")!;
    expect(img.getAttribute("src")).toBe(`/shell/thumb?p=${encodeURIComponent(photoPath)}&s=96`);
    act(() => { img.dispatchEvent(new Event("error")); });
    const inlineImg = rows()[0].querySelector<HTMLImageElement>("[data-queue-photo-thumb]")!;
    expect(inlineImg.getAttribute("src")).toBe(`/shell/raw?p=${encodeURIComponent(photoPath)}&inline=1`);
    act(() => { inlineImg.dispatchEvent(new Event("error")); });
    expect(rows()[0].querySelector("[data-queue-photo-fallback]")).not.toBeNull();

    // Remove optimistic row so confirmed actions are unlocked.
    await renderPanel({
      session,
      draftKey: "draft-a",
      canEdit: true,
      onEditQueue,
      onSendNow,
      onRestoreQueued,
      onNotify,
      resolveThumbURL,
      resolveDownloadURL,
      getSendFeedback,
      offline: false,
    });

    // Edit pulls back via onEditQueue + onRestoreQueued + onNotify and hides the pulled row optimistically.
    click(within(0, "[data-queue-edit]"));
    await flush();
    expect(onEditQueue).toHaveBeenCalledWith([1]);
    expect(onRestoreQueued).toHaveBeenCalledWith(
      "draft-a",
      session,
      expect.objectContaining({ text: "Check layout", photos: [photoPath] }),
      [],
    );
    expect(onNotify).toHaveBeenCalledWith(t("chat-queuepanel.pulled_back"), "ok");
    expect(rows()).toHaveLength(1);

    // Deliver updated queue before deleting the remaining row.
    await renderPanel({
      session: makeSession("host-a", "s-1", ["Second message"]),
      draftKey: "draft-a",
      canEdit: true,
      onEditQueue,
      onSendNow,
      onRestoreQueued,
      onNotify,
      resolveThumbURL,
      resolveDownloadURL,
      getSendFeedback,
      offline: false,
    });

    // Delete uses onEditQueue + onNotify without restoring and hides at once.
    click(within(0, "[data-queue-delete]"));
    expect(rows()).toHaveLength(0);
    await flush();
    expect(onEditQueue).toHaveBeenLastCalledWith([]);
    expect(onRestoreQueued).toHaveBeenCalledTimes(1);
    expect(onNotify).toHaveBeenLastCalledWith(t("chat-queuepanel.deleted"), "ok");

    // Re-deliver both rows, then test keyboard reorder.
    await renderPanel({
      session,
      draftKey: "draft-a",
      canEdit: true,
      onEditQueue,
      onSendNow,
      onRestoreQueued,
      onNotify,
      resolveThumbURL,
      resolveDownloadURL,
      getSendFeedback,
      offline: false,
    });

    // Keyboard reorder uses onEditQueue and holds order until the reordered queue arrives.
    key(within(1, "[data-queue-handle]"), "ArrowUp");
    await flush();
    expect(onEditQueue).toHaveBeenLastCalledWith([1, 0]);

    // Deliver the reordered queue so holdOrder releases, then test Send now.
    await renderPanel({
      session: makeSession("host-a", "s-1", ["Second message", withPhoto]),
      draftKey: "draft-a",
      canEdit: true,
      onEditQueue,
      onSendNow,
      onRestoreQueued,
      onNotify,
      resolveThumbURL,
      resolveDownloadURL,
      getSendFeedback,
      offline: false,
    });
    click(within(0, "[data-send-now]"));
    await flush();
    expect(onSendNow).toHaveBeenCalledTimes(1);
    expect(onNotify).toHaveBeenLastCalledWith(t("chat-queuepanel.sent_now", { agent: "Claude" }), "ok");

    expectNoDefaultSingletons();
  });

  it("restores text and images via controlled onRestoreQueued when unmounting after the last queued item empties before onEditQueue resolves", async () => {
    let resolvePull!: (res: { queue: string[]; images: { mime: string; data: string }[][] }) => void;
    const onEditQueue = vi.fn(() => new Promise<{ queue: string[]; images: { mime: string; data: string }[][] }>((res) => { resolvePull = res; }));
    const onRestoreQueued = vi.fn(async () => {});
    const onNotify = vi.fn();
    const single = makeSession("host-a", "s-last", ["Final controlled message"]);

    await renderPanel({
      session: single,
      draftKey: "draft-last-controlled",
      canEdit: true,
      onEditQueue,
      onSendNow: vi.fn(async () => {}),
      onRestoreQueued,
      onNotify,
      resolveThumbURL: vi.fn(() => ""),
      resolveDownloadURL: vi.fn(() => ""),
      getSendFeedback: vi.fn(() => "sending" as const),
      offline: false,
    });

    click(within(0, "[data-queue-edit]"));
    expect(onEditQueue).toHaveBeenCalledWith([]);

    // Queue-emptying push unmounts QueuePanel before onEditQueue settles.
    await act(async () => { root.render(null); });
    expect(host.innerHTML).toBe("");

    await act(async () => {
      resolvePull({
        queue: ["Final controlled message"],
        images: [[{ mime: "image/png", data: "CCCC" }]],
      });
      await Promise.resolve();
    });
    await flush();

    expect(onRestoreQueued).toHaveBeenCalledTimes(1);
    expect(onRestoreQueued).toHaveBeenCalledWith(
      "draft-last-controlled",
      single,
      expect.objectContaining({ text: "Final controlled message" }),
      [{ mime: "image/png", data: "CCCC" }],
    );
    expect(onNotify).toHaveBeenCalledTimes(1);
    expect(onNotify).toHaveBeenCalledWith(t("chat-queuepanel.pulled_back"), "ok");
    expectNoDefaultSingletons();
  });

  it("isolates in-flight delete/edit/send-now across session switches and ABA transitions without writing to B or leaking locks/notifications", async () => {
    let resolveA1!: (res: { queue: string[]; images?: { mime: string; data: string }[][] }) => void;
    let resolveA2!: (res: { queue: string[] }) => void;
    const onEditQueue = vi.fn(() => {
      if (onEditQueue.mock.calls.length === 1) {
        return new Promise<{ queue: string[]; images?: { mime: string; data: string }[][] }>((res) => { resolveA1 = res; });
      }
      return new Promise<{ queue: string[] }>((res) => { resolveA2 = res; });
    });
    const onRestoreQueued = vi.fn(async () => {});
    const onNotify = vi.fn();

    const sessionA = makeSession("host-a", "s-a", ["Alpha 1", "Alpha 2"]);
    const sessionB = makeSession("host-b", "s-b", ["Beta 1", "Beta 2"]);
    const common = {
      canEdit: true,
      onEditQueue,
      onSendNow: vi.fn(async () => {}),
      onRestoreQueued,
      onNotify,
      resolveThumbURL: vi.fn(() => ""),
      resolveDownloadURL: vi.fn(() => ""),
      getSendFeedback: vi.fn(() => "sending" as const),
      offline: false,
    };

    // Start pulling back row 0 in session A (request #1 stays in flight).
    await renderPanel({ ...common, session: sessionA, draftKey: "draft-a" });
    click(within(0, "[data-queue-edit]"));
    expect(onEditQueue).toHaveBeenCalledTimes(1);
    expect(within(1, "[data-queue-delete]").hasAttribute("disabled")).toBe(true);

    // Switch to session B while A1 is in flight: B must NOT be locked by A's pending request.
    await renderPanel({ ...common, session: sessionB, draftKey: "draft-b" });
    expect(within(0, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);
    expect(within(1, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);

    // ABA: switch back to session A while A1 is STILL pending.
    // Session A2 must NOT be permanently locked and can start request #2 (delete row 1).
    await renderPanel({ ...common, session: sessionA, draftKey: "draft-a" });
    expect(within(1, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);
    click(within(1, "[data-queue-delete]"));
    expect(onEditQueue).toHaveBeenCalledTimes(2);
    expect(within(0, "[data-queue-edit]").hasAttribute("disabled")).toBe(true);

    // Stale A1 edit resolves late: restores strictly to captured (draft-a, sessionA), NEVER to B,
    // does NOT emit stale notifications on B/A2, and does NOT unlock A2.
    await act(async () => {
      resolveA1({
        queue: ["Alpha 1", "Alpha 2"],
        images: [[{ mime: "image/png", data: "DDDD" }]],
      });
      await Promise.resolve();
    });
    await flush();
    expect(onRestoreQueued).toHaveBeenCalledTimes(1);
    expect(onRestoreQueued).toHaveBeenCalledWith(
      "draft-a",
      sessionA,
      expect.objectContaining({ text: "Alpha 1" }),
      [{ mime: "image/png", data: "DDDD" }],
    );
    expect(onRestoreQueued).not.toHaveBeenCalledWith("draft-b", expect.anything(), expect.anything(), expect.anything());
    expect(onNotify).not.toHaveBeenCalled();
    expect(within(0, "[data-queue-edit]").hasAttribute("disabled")).toBe(true);

    // Active A2 delete resolves: notifies once and unlocks A2.
    await act(async () => {
      resolveA2({ queue: ["Alpha 1", "Alpha 2"] });
      await Promise.resolve();
    });
    await flush();
    expect(onNotify).toHaveBeenCalledTimes(1);
    expect(onNotify).toHaveBeenCalledWith(t("chat-queuepanel.deleted"), "ok");
    expect(onRestoreQueued).toHaveBeenCalledTimes(1);
    expect(rows()).toHaveLength(1);
    // Pushed hub update clears the optimistic hold and unlocks the remaining row.
    await renderPanel({ ...common, session: makeSession("host-a", "s-a", ["Alpha 1"]), draftKey: "draft-a" });
    expect(within(0, "[data-queue-edit]").hasAttribute("disabled")).toBe(false);
    expectNoDefaultSingletons();
  });

  it("drops pending optimistic withdraw state when switching to another session", async () => {
    const onEditQueue = vi.fn(async () => ({ queue: ["Shared item"] }));
    const onNotify = vi.fn();
    const cancelQueued = vi.fn(() => false);
    const sessionA = makeSession("host-a", "s-a", []);
    const sessionB = makeSession("host-b", "s-b", ["Shared item"]);

    await renderPanel({
      session: sessionA,
      draftKey: "draft-a",
      canEdit: true,
      optimistic: [{ id: "opt-shared", raw: "Local pending" }],
      cancelQueued,
      onEditQueue,
      onSendNow: vi.fn(async () => {}),
      onRestoreQueued: vi.fn(async () => {}),
      onNotify,
      getSendFeedback: vi.fn(() => "sending" as const),
      offline: false,
    });

    // Clicking delete when cancelQueued returns false queues a deferred withdraw for session A.
    click(within(0, "[data-queue-delete]"));
    expect(cancelQueued).toHaveBeenCalledWith("opt-shared");

    // Switching to session B with confirmed rows must NOT inherit session A's withdraw lock or delete B's row.
    await renderPanel({
      session: sessionB,
      draftKey: "draft-b",
      canEdit: true,
      optimistic: [],
      pending: [{ id: "opt-shared", raw: "Shared item", before: [] }],
      cancelQueued,
      onEditQueue,
      onSendNow: vi.fn(async () => {}),
      onRestoreQueued: vi.fn(async () => {}),
      onNotify,
      getSendFeedback: vi.fn(() => "sending" as const),
      offline: false,
    });
    await flush();
    expect(onEditQueue).not.toHaveBeenCalled();
    expect(within(0, "[data-queue-delete]").hasAttribute("disabled")).toBe(false);
    expectNoDefaultSingletons();
  });
});

describe("QueueStrip and QueuePanel default compatibility", () => {
  it("keeps lazy QueueStrip loading and default api, outbox, restoreQueued, URL, feedback, store and toast fallbacks", async () => {
    const photoPath = "/workspace/example/default.png";
    const withPhoto = `Fix build\n[Image #1]\n<attached_images>\npath: ${photoPath}\n</attached_images>`;
    const session = makeSession("host-a", "s-1", [withPhoto]);
    mocks.api.mockResolvedValue({ queue: [withPhoto] });
    mocks.useMessageFeedback.mockReturnValue("unknown");
    mocks.useStore.mockReturnValue(true);

    await renderStrip({
      session,
      draftKey: "draft-default",
      canEdit: true,
      optimistic: [{ id: "opt-def", raw: "Unsent row" }],
    });
    await act(async () => { await vi.dynamicImportSettled(); });
    await flush();

    expect(mocks.fileThumbURL).toHaveBeenCalledWith("host-a", photoPath, "/workspace/example", 96);
    expect(mocks.fileDownloadURL).toHaveBeenCalledWith("host-a", photoPath, "/workspace/example", { inline: true });
    expect(mocks.useMessageFeedback).toHaveBeenCalledWith("host-a", "s-1", "opt-def");
    expect(mocks.useStore).toHaveBeenCalled();
    expect(host.querySelector('[role="status"]')?.getAttribute("title")).toBe(t("lib-outbox.delivery_unknown"));

    // Clear optimistic row and trigger default Edit + Send now.
    await renderStrip({
      session,
      draftKey: "draft-default",
      canEdit: true,
    });
    click(within(0, "[data-queue-edit]"));
    await flush();
    expect(mocks.outboxKey).toHaveBeenCalledWith("host-a", "s-1");
    expect(mocks.inSendOrder).toHaveBeenCalled();
    expect(mocks.sessionPath).toHaveBeenCalledWith("host-a", "s-1");
    expect(mocks.api).toHaveBeenCalledWith("/sessions/host-a/s-1/queue", { method: "POST", json: { keep: [] } });
    expect(mocks.restoreQueued).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith(t("chat-queuepanel.pulled_back"), "ok");

    // Hub pushes the remaining queued row so default Send now can be exercised.
    await renderStrip({
      session: makeSession("host-a", "s-1", ["Second item"]),
      draftKey: "draft-default",
      canEdit: true,
    });
    click(within(0, "[data-send-now]"));
    await flush();
    expect(mocks.api).toHaveBeenLastCalledWith("/sessions/host-a/s-1/queue/send-now", { method: "POST", json: {} });
    expect(mocks.toast).toHaveBeenLastCalledWith(t("chat-queuepanel.sent_now", { agent: "Claude" }), "ok");
  });

  it("still calls default restoreQueued with captured draftKey and session when QueueStrip unmounts before the last pull-back HTTP resolves", async () => {
    let resolvePull!: (v: unknown) => void;
    mocks.api.mockReturnValue(new Promise((r) => { resolvePull = r; }));
    const lastSession = makeSession("host-a", "s-last-default", ["Last default item"]);

    await renderStrip({
      session: lastSession,
      draftKey: "draft-last-default",
      canEdit: true,
    });
    await act(async () => { await vi.dynamicImportSettled(); });
    await flush();

    click(within(0, "[data-queue-edit]"));
    expect(mocks.api).toHaveBeenCalledWith("/sessions/host-a/s-last-default/queue", { method: "POST", json: { keep: [] } });

    // Unmount QueueStrip before HTTP resolves (simulating queued becoming empty).
    await act(async () => { root.render(null); });
    expect(host.innerHTML).toBe("");

    await act(async () => {
      resolvePull({
        queue: ["Last default item"],
        images: [[{ mime: "image/png", data: "EEEE" }]],
      });
      await Promise.resolve();
    });
    await flush();

    expect(mocks.restoreQueued).toHaveBeenCalledTimes(1);
    expect(mocks.restoreQueued).toHaveBeenCalledWith(
      "draft-last-default",
      lastSession,
      expect.objectContaining({ text: "Last default item" }),
      [{ mime: "image/png", data: "EEEE" }],
    );
    expect(mocks.toast).toHaveBeenCalledWith(t("chat-queuepanel.pulled_back"), "ok");
  });
});
