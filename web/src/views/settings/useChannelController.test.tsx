// @vitest-environment jsdom
// Deterministic reads and saves through the shared channel resource.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { HubTimeoutError, setPairedToken } from "../../lib/api";
import type { ChannelDetail, ChannelTestResult } from "../../lib/channels";
import type { ScopedAssistantClient } from "../../lib/scoped-assistant-api";
import { useChannelController, type ChannelController } from "./integrations/ChannelSection";

type Asked = {
  kind: "get" | "put" | "unbind" | "test";
  id: string;
  manager?: string;
  body?: unknown;
  answer: (d: ChannelDetail | ChannelTestResult) => void;
  reject: (err: unknown) => void;
};

const hub = vi.hoisted(() => ({
  asked: [] as Asked[],
  forgotten: 0,
}));

vi.mock("../../lib/channels", async (original) => ({
  ...(await original<typeof import("../../lib/channels")>()),
  channelDetail: (id: string, client: ScopedAssistantClient) => new Promise((resolve, reject) => hub.asked.push({ kind: "get", id, manager: client.managerId, answer: resolve, reject })),
  updateChannel: (id: string, body: unknown, client: ScopedAssistantClient) => new Promise((resolve, reject) => hub.asked.push({ kind: "put", id, manager: client.managerId, body, answer: resolve, reject })),
  unbindChannel: (id: string, client: ScopedAssistantClient) => new Promise((resolve, reject) => hub.asked.push({ kind: "unbind", id, manager: client.managerId, answer: resolve, reject })),
  testChannel: (id: string, client: ScopedAssistantClient) => new Promise((resolve, reject) => hub.asked.push({ kind: "test", id, manager: client.managerId, answer: resolve, reject })),
  forgetChannels: () => {
    hub.forgotten++;
  },
}));
vi.mock("../../lib/managers", async (original) => ({ ...(await original<typeof import("../../lib/managers")>()), useRoster: () => null }));
vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: vi.fn(),
}));

import { toast } from "../../ui/atoms";

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const makeDetail = (id: string, patch: Partial<ChannelDetail> = {}): ChannelDetail => ({
  id,
  name: id.toUpperCase(),
  kind: "im",
  source: "builtin",
  installed: true,
  enabled: true,
  state: "connected",
  account: { name: `${id}@example.com` },
  capabilities: { push: true },
  bind: "none",
  target: { kind: "main" },
  waiting: { answers: 0, asks: 0 },
  ...patch,
});

let host: HTMLDivElement;
let root: Root;
let latest: ChannelController;

function Harness({ channelId, manager }: { channelId: string | null; manager?: string }) {
  latest = useChannelController(channelId, manager);
  return null;
}

const settle = async () => {
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

beforeEach(() => {
  setPairedToken("");
  hub.asked = [];
  hub.forgotten = 0;
  vi.mocked(toast).mockReset();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("useChannelController", () => {
  it("clears state when channelId becomes null and ignores stale reads from the previous channel", async () => {
    await act(async () => root.render(<Harness channelId="alpha" />));
    const req = hub.asked.shift()!;
    expect(req.kind).toBe("get");
    expect(req.id).toBe("alpha");
    expect(latest.loading).toBe(true);

    await act(async () => root.render(<Harness channelId={null} />));
    expect(latest.detail).toBeNull();
    expect(latest.loading).toBe(false);

    await act(async () => req.answer(makeDetail("alpha")));
    await settle();
    expect(latest.detail).toBeNull();
  });

  it("keeps a write on its own channel when switching away and back", async () => {
    await act(async () => root.render(<Harness channelId="alpha" />));
    await act(async () => hub.asked.shift()!.answer(makeDetail("alpha")));
    await settle();
    expect(latest.detail?.id).toBe("alpha");

    // Start a write on alpha, then switch to beta before it finishes.
    let alphaWritePromise!: Promise<void>;
    await act(async () => {
      alphaWritePromise = latest.change("enabled", { enabled: false });
    });
    const alphaPut = hub.asked.shift()!;
    expect(alphaPut.kind).toBe("put");
    expect(alphaPut.id).toBe("alpha");
    expect(latest.busy).toBe("enabled");

    await act(async () => root.render(<Harness channelId="beta" />));
    const betaGet = hub.asked.shift()!;
    expect(betaGet.kind).toBe("get");
    expect(betaGet.id).toBe("beta");
    expect(latest.busy).toBe("");

    await act(async () => betaGet.answer(makeDetail("beta")));
    await settle();
    expect(latest.detail?.id).toBe("beta");

    // Beta is immediately writable even while alpha's old write is still pending.
    let betaWritePromise!: Promise<void>;
    await act(async () => {
      betaWritePromise = latest.change("enabled", { enabled: false });
    });
    const betaPut = hub.asked.shift()!;
    expect(betaPut.kind).toBe("put");
    expect(betaPut.id).toBe("beta");
    await act(async () => {
      betaPut.answer(makeDetail("beta", { enabled: false }));
      await betaWritePromise;
    });
    await settle();
    expect(latest.detail?.enabled).toBe(false);

    // Reopening alpha joins its write and waits for a fresh read after it.
    await act(async () => root.render(<Harness channelId="alpha" />));
    expect(latest.detail?.enabled).toBe(true);
    expect(latest.busy).toBe("enabled");
    expect(hub.asked).toHaveLength(0);

    await act(async () => {
      alphaPut.answer(makeDetail("alpha", { enabled: false }));
      await alphaWritePromise;
    });
    expect(latest.detail?.enabled).toBe(false);
    expect(latest.busy).toBe("");
    const refresh = hub.asked.shift()!;
    expect(refresh).toMatchObject({ kind: "get", id: "alpha" });
    await act(async () => refresh.answer(makeDetail("alpha", { enabled: false })));
  });

  it("ignores stale test errors and stale unbind results after switching channels", async () => {
    await act(async () => root.render(<Harness channelId="alpha" />));
    await act(async () => hub.asked.shift()!.answer(makeDetail("alpha")));
    await settle();

    let unbindResult: boolean | undefined;
    let unbindPromise!: Promise<void>;
    await act(async () => {
      unbindPromise = latest.unbind().then((ok) => {
        unbindResult = ok;
      });
    });
    const alphaUnbind = hub.asked.shift()!;
    expect(alphaUnbind.kind).toBe("unbind");

    await act(async () => root.render(<Harness channelId="beta" />));
    await act(async () => hub.asked.shift()!.answer(makeDetail("beta")));
    await settle();

    await act(async () => {
      alphaUnbind.answer(makeDetail("alpha", { state: "not_connected", account: undefined }));
      await unbindPromise;
    });
    await settle();

    expect(unbindResult).toBe(false);
    expect(latest.detail?.id).toBe("beta");
    expect(toast).not.toHaveBeenCalled();

    // Now start a test on beta and switch away before it rejects.
    let testPromise!: Promise<void>;
    await act(async () => {
      testPromise = latest.runTest();
    });
    const betaTest = hub.asked.shift()!;
    expect(betaTest.kind).toBe("test");

    await act(async () => root.render(<Harness channelId="alpha" />));
    await act(async () => hub.asked.shift()!.answer(makeDetail("alpha")));
    await settle();

    await act(async () => {
      betaTest.reject(new Error("boom"));
      await testPromise;
    });
    await settle();

    expect(latest.testFeedback).toBeNull();
    expect(toast).not.toHaveBeenCalled();
  });

  it("binds reads and reply-only tests to the manager", async () => {
    await act(async () => root.render(<Harness channelId="alpha" manager="m-1" />));
    const read = hub.asked.shift()!;
    expect(read).toMatchObject({ kind: "get", id: "alpha", manager: "m-1" });
    await act(async () => read.answer(makeDetail("alpha", { capabilities: { reply_only: true }, name: "Alpha" })));

    let run!: Promise<void>;
    await act(async () => {
      run = latest.runTest();
    });
    const testReq = hub.asked.shift()!;
    expect(testReq).toMatchObject({ kind: "test", id: "alpha", manager: "m-1" });
    await act(async () => {
      testReq.answer({ ok: true, reply_only: true });
      await run;
    });
    await settle();

    expect(latest.testFeedback).toEqual({
      tone: "ok",
      text: t("settings-channels.connection_ok", { name: "Alpha" }),
    });
    expect(toast).toHaveBeenCalledWith(t("settings-channels.connection_ok", { name: "Alpha" }), "ok");
  });

  it("re-fences, clears state, and reloads when manager changes for the same channelId", async () => {
    await act(async () => root.render(<Harness channelId="wechat" manager="m-1" />));
    const m1Get = hub.asked.shift()!;
    expect(m1Get.manager).toBe("m-1");
    await act(async () => m1Get.answer(makeDetail("wechat", { name: "M1 WeChat", enabled: true })));
    await settle();
    expect(latest.detail?.name).toBe("M1 WeChat");

    // Start an in-flight write on m-1, then switch manager to m-2 with the same channelId.
    let m1PutPromise!: Promise<void>;
    await act(async () => {
      m1PutPromise = latest.change("enabled", { enabled: false });
    });
    const m1Put = hub.asked.shift()!;
    expect(m1Put).toMatchObject({ kind: "put", manager: "m-1" });

    await act(async () => root.render(<Harness channelId="wechat" manager="m-2" />));
    expect(latest.detail).toBeNull();
    expect(latest.busy).toBe("");

    const m2Get = hub.asked.shift()!;
    expect(m2Get).toMatchObject({ kind: "get", id: "wechat", manager: "m-2" });
    await act(async () => m2Get.answer(makeDetail("wechat", { name: "M2 WeChat", enabled: false })));
    await settle();
    expect(latest.detail?.name).toBe("M2 WeChat");

    // Resolving the stale m-1 write must neither overwrite m-2 nor trigger a reload.
    const countBefore = hub.asked.length;
    await act(async () => {
      m1Put.answer(makeDetail("wechat", { name: "M1 Stale", enabled: true }));
      await m1PutPromise;
    });
    await settle();
    expect(latest.detail?.name).toBe("M2 WeChat");
    expect(hub.asked.length).toBe(countBefore);
  });

  it("clears loading when an in-flight load finishes after an interleaved write", async () => {
    await act(async () => root.render(<Harness channelId="alpha" />));
    await act(async () => hub.asked.shift()!.answer(makeDetail("alpha", { enabled: true })));
    await settle();
    expect(latest.loading).toBe(false);

    // Start a manual refresh, then interleave a write before the refresh returns.
    let refreshPromise!: Promise<void>;
    await act(async () => {
      refreshPromise = latest.load();
    });
    const slowRefresh = hub.asked.shift()!;
    expect(slowRefresh.kind).toBe("get");
    expect(latest.loading).toBe(true);

    let writePromise!: Promise<void>;
    await act(async () => {
      writePromise = latest.change("enabled", { enabled: false });
    });
    const put = hub.asked.shift()!;
    expect(put.kind).toBe("put");
    await act(async () => {
      put.answer(makeDetail("alpha", { enabled: false }));
      await writePromise;
    });
    await settle();
    expect(latest.detail?.enabled).toBe(false);

    // The older GET settles while the authoritative read is waiting.
    const reconcile = hub.asked.shift()!;
    await act(async () => {
      slowRefresh.answer(makeDetail("alpha", { enabled: true }));
      await refreshPromise;
    });
    expect(latest.detail?.enabled).toBe(false);
    expect(latest.loading).toBe(true);
    await act(async () => reconcile.answer(makeDetail("alpha", { enabled: false })));
    expect(latest.loading).toBe(false);
  });

  it("queues channel fields behind routing and keeps both writes partial", async () => {
    await act(async () => root.render(<Harness channelId="alpha" />));
    const plugin = makeDetail("alpha", { source: "plugin", thread_mode: "channel_default" });
    await act(async () => hub.asked.shift()!.answer(plugin));
    const { form, start } = latest.routing!;
    await act(async () => start());
    act(() => form.choose("thread_mode", "per_topic"));
    const put = hub.asked.shift()!;
    expect(put).toMatchObject({ kind: "put", id: "alpha", body: { thread_mode: "per_topic" } });
    expect(latest.busy).toBe("routing");
    act(() => {
      form.choose("thread_mode", "per_topic");
      void latest.change("enabled", { enabled: false });
      void latest.change("enabled", { enabled: false });
      void latest.load();
    });
    expect(hub.asked).toHaveLength(0);

    const committed = { ...plugin, thread_mode: "per_topic" } satisfies ChannelDetail;
    await act(async () => put.answer(committed));
    expect(form.snapshot().doc?.values.thread_mode).toBe("per_topic");
    const toggle = hub.asked.shift()!;
    expect(toggle).toMatchObject({ kind: "put", id: "alpha", body: { enabled: false } });
    expect(hub.asked).toHaveLength(0);
    const fresh = { ...committed, enabled: false, thread_idle_hours: 72 };
    await act(async () => toggle.answer({ ...committed, enabled: false }));
    const reconcile = hub.asked.shift()!;
    expect(reconcile.kind).toBe("get");
    await act(async () => reconcile.answer(fresh));
    expect(latest.detail).toEqual(fresh);
    expect(form.snapshot().doc?.values.thread_idle_hours).toBe(72);
  });

  it("keeps drafts bound to the assistant and retires them when credentials change", async () => {
    const plugin = makeDetail("alpha", { source: "plugin", thread_mode: "channel_default" });
    await act(async () => root.render(<Harness channelId="alpha" manager="m-1" />));
    await act(async () => hub.asked.shift()!.answer(plugin));
    await act(async () => latest.routing!.start());
    act(() => latest.routing!.form.setDraft("thread_idle_hours", "48"));
    for (const [manager, draft] of [["m-2", undefined], ["m-1", "48"]] as const) {
      await act(async () => root.render(<Harness channelId="alpha" manager={manager} />));
      await act(async () => hub.asked.shift()!.answer(plugin));
      await act(async () => latest.routing!.start());
      expect(latest.routing!.form.snapshot().fields.thread_idle_hours?.draft).toBe(draft);
    }
    const old = latest.routing;
    await act(async () => setPairedToken(""));
    expect(latest.detail).toBeNull();
    expect(latest.routing).not.toBe(old);
    await act(async () => hub.asked.shift()!.answer(plugin));
    await act(async () => latest.routing!.start());
    expect(latest.routing!.form.snapshot().fields).toEqual({});
  });

  it("keeps the typed routing draft when a lost response cannot be confirmed", async () => {
    const plugin = makeDetail("alpha", { source: "plugin", thread_mode: "channel_default", thread_idle_hours: 24 });
    await act(async () => root.render(<Harness channelId="alpha" manager="m-1" />));
    await act(async () => hub.asked.shift()!.answer(plugin));
    const routing = latest.routing!;
    await act(async () => routing.start());
    act(() => {
      routing.form.setDraft("thread_idle_hours", "48");
      routing.form.saveDraft("thread_idle_hours");
    });
    const lost = hub.asked.shift()!;
    expect(lost).toMatchObject({ kind: "put", manager: "m-1", id: "alpha", body: { thread_idle_hours: 48 } });
    await act(async () => lost.reject(new HubTimeoutError()));
    const comparison = hub.asked.shift()!;
    expect(comparison).toMatchObject({ kind: "get", manager: "m-1", id: "alpha" });
    await act(async () => comparison.answer(plugin));
    expect(routing.form.snapshot()).toMatchObject({ blocked: { code: "unconfirmed" }, fields: { thread_idle_hours: { draft: "48" } } });
    expect(hub.asked).toHaveLength(0);
    act(() => { void routing.reload(); });
    await act(async () => hub.asked.shift()!.answer(plugin));
    act(() => routing.form.saveDraft("thread_idle_hours"));
    const retry = hub.asked.shift()!;
    expect(retry.body).toEqual({ thread_idle_hours: 48 });
    await act(async () => retry.answer({ ...plugin, thread_idle_hours: 48 }));
    expect(routing.form.snapshot().doc?.values.thread_idle_hours).toBe(48);
    expect(routing.form.snapshot().fields.thread_idle_hours?.draft).toBeUndefined();
  });
});
