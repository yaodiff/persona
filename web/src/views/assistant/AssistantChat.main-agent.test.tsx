// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { getState, reset } from "../../lib/store";
import type { Session } from "../../lib/types";

const launcherRead = vi.hoisted(() => ({
  info: undefined as import("../../lib/session-models").AssistantLaunchers | undefined,
  calls: [] as { mid: string; again: boolean }[],
  hold: false,
  settleBeforeRead: false,
  loading: false,
  promise: undefined as Promise<void> | undefined,
  release: undefined as (() => void) | undefined,
}));
vi.mock("../../lib/session-models", async (original) => ({
  ...(await original<typeof import("../../lib/session-models")>()),
  assistantLaunchers: () => ({ info: launcherRead.info, loading: launcherRead.loading, read: launcherRead.promise }),
  readAssistantLaunchers: (mid: string, o: { again?: boolean } = {}) => {
    launcherRead.calls.push({ mid, again: Boolean(o.again) });
    if (launcherRead.settleBeforeRead) {
      launcherRead.settleBeforeRead = false;
      launcherRead.release?.();
    }
    if (launcherRead.loading) return launcherRead.promise!;
    if (!launcherRead.hold) return Promise.resolve();
    launcherRead.loading = true;
    launcherRead.promise = new Promise<void>((resolve) => {
      launcherRead.release = () => {
        launcherRead.loading = false;
        launcherRead.promise = undefined;
        resolve();
      };
    });
    return launcherRead.promise;
  },
  useModelLists: () => 0,
}));

import { CompactionStrip } from "./AssistantChat";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const session = (x: Partial<Session> = {}): Session => ({
  machine_id: "hub", session_id: "c1", title: "", cwd: "/", assistant: true, status: "idle", state: "idle",
  agent: "codex", launcher: "codex", model: "gpt-5.5", ...x,
}) as Session;

let host: HTMLDivElement;
let root: Root;
const flush = () => act(async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); });
const notice = () => host.querySelector<HTMLElement>("[data-main-chat-agent-notice]");

beforeEach(() => {
  reset();
  launcherRead.calls = [];
  launcherRead.hold = false;
  launcherRead.settleBeforeRead = false;
  launcherRead.loading = false;
  launcherRead.promise = undefined;
  launcherRead.release = undefined;
  launcherRead.info = {
    launchers: [{ name: "claude", source: "builtin" }, { name: "codex", source: "builtin", agent: "codex" }],
    def: { provider: "claude" }, unavailable: [],
  };
  Object.assign(getState(), {
    features: ["assistant_main_new", "assistant_model_pick"],
    assistant: { machine_id: "hub", main: "c1", launcher: "claude", conversations: [] },
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

async function show(s: Session) {
  await act(async () => root.render(<CompactionStrip session={s} />));
  await act(async () => vi.dynamicImportSettled());
  await flush();
}

describe("the Main chat default's agent-kind mismatch", () => {
  it("names the actual and configured launchers and opens the existing New main chat dialog", async () => {
    await show(session());
    expect(notice()).not.toBeNull();
    expect(notice()!.textContent).toContain("Claude Code");
    expect(notice()!.textContent).toContain("Codex");
    const action = [...notice()!.querySelectorAll("button")].find((b) => b.textContent === t("assistant-assistantchat.new_main_ellipsis"))!;
    expect(action.className).toContain("touch-hit");
    await act(async () => action.click());
    await act(async () => vi.dynamicImportSettled());
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain(t("assistant-assistantchat.new_main_title"));
  });

  it("stays hidden for another launcher of the same agent kind", async () => {
    launcherRead.info = {
      launchers: [{ name: "codex", source: "builtin", agent: "codex" }, { name: "codex-alt", source: "zshrc", agent: "codex" }],
      def: { provider: "codex-alt" }, unavailable: [],
    };
    await show(session());
    expect(notice()).toBeNull();
  });

  it("stays hidden while the default is unknown and while a pick owns the next message", async () => {
    launcherRead.info = { launchers: [{ name: "codex", source: "builtin", agent: "codex" }], def: { provider: "gone" }, unavailable: [] };
    await show(session());
    expect(notice()).toBeNull();
    launcherRead.info = {
      launchers: [{ name: "claude", source: "builtin" }, { name: "codex", source: "builtin", agent: "codex" }],
      def: { provider: "claude" }, unavailable: [],
    };
    await show(session({ model_pick: { launcher: "codex" } }));
    expect(notice()).toBeNull();
  });

  it("refreshes the default on mount and when the conversation revision changes", async () => {
    await show(session({ rev: 1 }));
    expect(launcherRead.calls).toEqual([{ mid: "hub", again: true }]);
    await show(session({ rev: 2 }));
    expect(launcherRead.calls).toEqual([{ mid: "hub", again: true }, { mid: "hub", again: true }]);
  });

  it("holds revision refreshes while the conversation works and refreshes once when its turn ends", async () => {
    await show(session({ rev: 1 }));
    expect(launcherRead.calls).toHaveLength(1);
    for (let rev = 2; rev <= 6; rev++) await show(session({ rev, status: "working", state: "working" }));
    expect(launcherRead.calls).toHaveLength(1);
    await show(session({ rev: 7 }));
    expect(launcherRead.calls).toEqual([{ mid: "hub", again: true }, { mid: "hub", again: true }]);
  });

  it("keeps a trailing refresh when a revision arrives during a pending launcher read", async () => {
    launcherRead.hold = true;
    await show(session({ rev: 1 }));
    expect(launcherRead.calls).toHaveLength(1);
    await show(session({ rev: 2 }));
    expect(launcherRead.calls).toHaveLength(2);
    const release = launcherRead.release!;
    await act(async () => { release(); await flush(); });
    expect(launcherRead.calls).toHaveLength(3);
    expect(launcherRead.calls.every((call) => call.mid === "hub" && call.again)).toBe(true);
  });

  it("keeps that trailing refresh when the conversation starts working before the pending read settles", async () => {
    launcherRead.hold = true;
    await show(session({ rev: 1 }));
    await show(session({ rev: 2 }));
    expect(launcherRead.calls).toHaveLength(2);
    // a new turn marks the conversation working before its first write moves the revision
    await show(session({ rev: 2, status: "working", state: "working" }));
    const release = launcherRead.release!;
    await act(async () => { release(); await flush(); });
    expect(launcherRead.calls).toHaveLength(3);
    expect(launcherRead.calls.every((call) => call.mid === "hub" && call.again)).toBe(true);
  });

  it("does not add a trailing refresh when the old request settles before the effect", async () => {
    launcherRead.hold = true;
    await show(session({ rev: 1 }));
    launcherRead.settleBeforeRead = true;
    await show(session({ rev: 2 }));
    expect(launcherRead.calls).toHaveLength(2);
    const release = launcherRead.release!;
    await act(async () => { release(); await flush(); });
    expect(launcherRead.calls).toHaveLength(2);
  });
});
