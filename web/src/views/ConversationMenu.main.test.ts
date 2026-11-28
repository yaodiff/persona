import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  const kept = new Map<string, string>();
  const g = globalThis as Record<string, unknown>;
  g.localStorage = {
    getItem: (k: string) => kept.get(k) ?? null,
    setItem: (k: string, v: string) => void kept.set(k, v),
    removeItem: (k: string) => void kept.delete(k),
  };
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

import { t } from "../i18n";
import type { Session } from "../lib/types";
import { conversationMenuItems, type ConversationMenuDeps } from "./ConversationMenu";

const makeMain = (patch: Partial<Session> = {}): Session =>
  ({
    machine_id: "hub",
    session_id: "main-1",
    title: "Main",
    status: "idle",
    assistant: true,
    ...patch,
  }) as Session;

function makeDeps(over: Partial<ConversationMenuDeps> = {}) {
  const calls: string[] = [];
  const deps: ConversationMenuDeps = {
    rename: async (_s, title) => void calls.push(`rename:${title}`),
    archive: async (s) => void calls.push(`archive:${s.session_id}`),
    unarchive: async (s) => void calls.push(`unarchive:${s.session_id}`),
    end: async (s) => void calls.push(`end:${s.session_id}`),
    refresh: async () => void calls.push("refresh"),
    lead: async (s, on) => void calls.push(`lead:${s.session_id}:${on}`),
    compact: async (s) => void calls.push(`compact:${s.session_id}`),
    clearMain: async (s) => void calls.push(`clear:${s.session_id}`),
    copy: (text) => void calls.push(`copy:${text}`),
    notify: (text, tone) => void calls.push(`${tone}:${text}`),
    ...over,
  };
  return { deps, calls };
}

describe("Main chat sidebar menu parity (nwverse/persona#16294)", () => {
  it("shows Compact context, Clear context, New main chat..., Copy ID, and End when all capabilities are enabled", async () => {
    const onNewMain = vi.fn();
    const { deps, calls } = makeDeps();
    const items = conversationMenuItems(
      makeMain(),
      {
        main: true,
        leader: true,
        canDelete: true,
        onDelete: vi.fn(),
        canCompact: true,
        canClearMain: true,
        canNewMain: true,
        onNewMain,
      },
      deps,
    );

    expect(items.map((i) => i.key)).toEqual(["compact", "clear", "newmain", "copy", "end"]);
    // Main chat stays non-archivable, non-renamable, non-deletable, and non-leader-toggled.
    expect(items.some((i) => ["leader", "rename", "archive", "delete"].includes(i.key))).toBe(false);
    expect(items.find((i) => i.key === "compact")).toMatchObject({
      label: t("assistant-assistantchat.compact"),
      icon: "layers",
      disabled: false,
    });
    expect(items.find((i) => i.key === "clear")).toMatchObject({
      label: t("assistant-assistantchat.clear"),
      icon: "retry",
      disabled: false,
    });
    expect(items.find((i) => i.key === "newmain")).toMatchObject({
      label: t("assistant-assistantchat.new_main_ellipsis"),
      icon: "compose",
      keepOpen: true,
    });
    expect(items.find((i) => i.key === "copy")?.divider).toBe(true);

    await items.find((i) => i.key === "compact")!.onSelect();
    await items.find((i) => i.key === "clear")!.onSelect();
    items.find((i) => i.key === "newmain")!.onSelect();
    expect(calls).toEqual(["compact:main-1", "clear:main-1"]);
    expect(onNewMain).toHaveBeenCalledTimes(1);
  });

  it("gates each main-chat action independently on hub capabilities and degraded hubs", () => {
    const onNewMain = vi.fn();
    expect(conversationMenuItems(makeMain(), { main: true }).map((i) => i.key)).toEqual(["copy", "end"]);
    expect(conversationMenuItems(makeMain(), { main: true }).find((i) => i.key === "copy")?.divider).toBeUndefined();

    expect(conversationMenuItems(makeMain(), { main: true, canCompact: true }).map((i) => i.key)).toEqual(["compact", "copy", "end"]);
    expect(conversationMenuItems(makeMain(), { main: true, canClearMain: true }).map((i) => i.key)).toEqual(["clear", "copy", "end"]);
    expect(conversationMenuItems(makeMain(), { main: true, canNewMain: true, onNewMain }).map((i) => i.key)).toEqual(["newmain", "copy", "end"]);
    expect(conversationMenuItems(makeMain({ status: "offline" }), { main: true, canCompact: true, canClearMain: true, canNewMain: true, onNewMain }).map((i) => i.key)).toEqual(["compact", "clear", "newmain", "copy"]);
  });

  it("disables Compact while compacting and Clear while clearing, and surfaces compact errors via notify", async () => {
    const running = makeMain({ compaction: { state: "running", started_at: "2026-11-26T00:00:00Z" } });
    const items = conversationMenuItems(running, { main: true, canCompact: true, canClearMain: true, clearingMain: true });
    expect(items.find((i) => i.key === "compact")).toMatchObject({
      label: t("assistant-assistantchat.compacting"),
      disabled: true,
    });
    expect(items.find((i) => i.key === "clear")).toMatchObject({ disabled: true });

    const { deps, calls } = makeDeps({
      compact: async () => {
        throw new Error("Already compacting.");
      },
    });
    const failed = conversationMenuItems(makeMain(), { main: true, canCompact: true }, deps);
    await failed.find((i) => i.key === "compact")!.onSelect();
    expect(calls).toEqual([`error:${t("assistant-assistantchat.compact_start_failed", { error: "Already compacting." })}`]);
  });

  it("never exposes main actions on non-main or built-in supervisor rows", () => {
    const nonMain = conversationMenuItems(makeMain({ session_id: "side-1" }), {
      main: false,
      canCompact: true,
      canClearMain: true,
      canNewMain: true,
      onNewMain: vi.fn(),
    });
    expect(nonMain.map((i) => i.key)).toEqual(["leader", "rename", "archive", "copy", "end"]);

    const supervisor = conversationMenuItems(makeMain({ session_id: "sup-1" }), {
      main: true,
      builtIn: true,
      canCompact: true,
      canClearMain: true,
      canNewMain: true,
      onNewMain: vi.fn(),
    });
    expect(supervisor.map((i) => i.key)).toEqual(["copy"]);
  });
});
