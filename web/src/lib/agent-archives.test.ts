// Archiving agents (lib/agent-archives.ts): what the menus offer (mirroring the manager's rules), and what the actions
// tell the user: Undo after an archive, why a busy agent was refused, and how "Archive and close" ended.
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import {
  AGENT_ARCHIVE_CLOSE_FEATURE, AGENT_ARCHIVES_FEATURE, archiveAgent, archiveOffers, closeBusyReason, closeOffer, deleteInsteadOfArchive, restoreAgent,
  type ArchiveDeps, type CloseResult,
} from "./agent-archives";
import { t } from "../i18n";
import type { Session } from "./types";

const both = [AGENT_ARCHIVES_FEATURE, AGENT_ARCHIVE_CLOSE_FEATURE];
const open: Session = { machine_id: "mac", session_id: "s1", title: "Fix the build", cwd: "/w/app", status: "idle", state: "idle", agent: "claude", pane_id: "w1:p1" };

describe("what the menus offer", () => {
  it("archives and closes an idle Claude agent open in a tab", () => {
    expect(archiveOffers(open, both)).toEqual({ archive: "ok", close: "ok" });
    expect(archiveOffers(open, [AGENT_ARCHIVES_FEATURE])).toEqual({ archive: "ok", close: "no" }); // an older manager
    expect(archiveOffers(open, [])).toEqual({ archive: "no", close: "no" }); // an older hub: Delete chat stays
    expect(deleteInsteadOfArchive([])).toBe(true);
    expect(deleteInsteadOfArchive(both)).toBe(false);
  });

  it("never offers the assistant's conversations or one already archived", () => {
    expect(archiveOffers({ ...open, assistant: true }, both)).toEqual({ archive: "no", close: "no" });
    expect(archiveOffers({ ...open, archived: true }, both)).toEqual({ archive: "no", close: "no" });
  });

  it("shows both disabled while the agent is busy, as the manager refuses them", () => {
    for (const busy of [
      { status: "working", state: "working" }, { status: "blocked" }, { pending_interactions: 1 }, { starting: true },
      { background_working: true }, { background_waiting: true }, { background_count: 1 }, { running_subagents: 1 },
      { subagents: [{ id: "a", running: true }] }, { running_workflows: 1 },
    ] as Partial<Session>[]) {
      expect(archiveOffers({ ...open, ...busy }, both), JSON.stringify(busy)).toEqual({ archive: "busy", close: "busy" });
    }
  });

  it("archives but does not close one with a loop, queued messages or a server", () => {
    for (const busy of [{ loop: { prompt: "check" } }, { queued: ["next"] }, { ports: [{ port: 5173 }] }] as Partial<Session>[]) {
      expect(archiveOffers({ ...open, ...busy }, both), JSON.stringify(busy)).toEqual({ archive: "ok", close: "busy" });
    }
  });

  it("archives one awaiting the user's reply: it is idle", () => {
    expect(archiveOffers({ ...open, awaits_reply: true }, both)).toEqual({ archive: "ok", close: "ok" });
  });

  it("closes only what session.retire can bring back", () => {
    expect(archiveOffers({ ...open, agent: "codex" }, both).close).toBe("no"); // launcher unknown
    expect(archiveOffers({ ...open, agent: "codex", started_by_persona: true }, both).close).toBe("ok");
    expect(archiveOffers({ ...open, agent: "pi", source_kind: "runtime" }, both)).toEqual({ archive: "ok", close: "no" });
    expect(archiveOffers({ ...open, source_kind: "codex_app_server", agent: "codex" }, both)).toEqual({ archive: "ok", close: "no" });
    expect(archiveOffers({ ...open, status: "offline", state: "offline" }, both)).toEqual({ archive: "ok", close: "no" });
    expect(archiveOffers({ ...open, status: "offline", start_error: "did not start" }, both)).toEqual({ archive: "ok", close: "no" });
  });
});

describe("what Close… does (#7125)", () => {
  it("archives and closes where the hub can, and only closes where it can't", () => {
    expect(closeOffer(open, both)).toBe("archive");
    expect(closeOffer(open, [AGENT_ARCHIVES_FEATURE])).toBe("close"); // an older manager
    expect(closeOffer(open, [])).toBe("close"); // an older hub
    expect(closeOffer({ ...open, agent: "codex" }, both)).toBe("close"); // a Codex session Persona did not start
    expect(closeOffer({ ...open, archived: true }, both)).toBe("close");
  });
  it("closes a busy agent too, without archiving it (the hub refuses that)", () => {
    for (const busy of [{ status: "working", state: "working" }, { status: "blocked", state: "needs_you" }, { loop: { prompt: "check" } }, { ports: [{ port: 3000 }] }] as Partial<Session>[]) {
      expect(closeOffer({ ...open, ...busy }, both), JSON.stringify(busy)).toBe("close");
    }
  });
  it("is disabled only for a busy agent whose node offers no plain close, and absent where nothing closes it", () => {
    const noPlainClose = { ...open, capabilities: { read: true, send: true, terminal: false } } as Session;
    expect(closeOffer({ ...noPlainClose, status: "working", state: "working" }, both)).toBe("busy");
    expect(closeOffer(noPlainClose, both)).toBe("archive");
    expect(closeOffer({ ...open, status: "offline", state: "offline" }, both)).toBe("no");
    expect(closeOffer({ ...open, assistant: true }, both)).toBe("no");
  });
  it("names what closing a busy agent interrupts: an answer it waits for first, then its work, a loop, a server", () => {
    expect(closeBusyReason(open)).toBeUndefined();
    expect(closeBusyReason({ ...open, awaits_reply: true })).toBeUndefined(); // a finished turn is not busy
    expect(closeBusyReason({ ...open, status: "blocked", state: "needs_you", loop: { prompt: "check" } } as Session)).toBe("waiting");
    expect(closeBusyReason({ ...open, pending_interactions: 1 })).toBe("waiting");
    expect(closeBusyReason({ ...open, status: "working", state: "working", ports: [{ port: 3000 }] } as Session)).toBe("working");
    expect(closeBusyReason({ ...open, queued: ["next"] })).toBe("working");
    expect(closeBusyReason({ ...open, running_subagents: 1 })).toBe("working");
    expect(closeBusyReason({ ...open, loop: { prompt: "check" }, ports: [{ port: 3000 }] } as Session)).toBe("loop");
    expect(closeBusyReason({ ...open, ports: [{ port: 3000 }] } as Session)).toBe("server");
  });
});

function deps(over: Partial<ArchiveDeps> = {}) {
  const notes: { text: string; tone: string; action?: { label: string; run: () => void } }[] = [];
  const d: ArchiveDeps = {
    archive: vi.fn().mockResolvedValue({ ok: true }),
    unarchive: vi.fn().mockResolvedValue({ ok: true }),
    drop: vi.fn(),
    restore: vi.fn(),
    refresh: vi.fn().mockResolvedValue(undefined),
    notify: (text, tone, action) => void notes.push({ text, tone, action }),
    ...over,
  };
  return { d, notes };
}

describe("archiving", () => {
  it("takes the row out at once, says so and offers Undo, which restores it", async () => {
    const { d, notes } = deps();
    expect((await archiveAgent(open, false, d)).ok).toBe(true);
    expect(d.drop).toHaveBeenCalledWith(open);
    expect(d.archive).toHaveBeenCalledWith(open, false);
    expect(notes[0]).toMatchObject({ tone: "ok", text: t("lib-agent-archives.archived", { title: "Fix the build" }) });
    notes[0].action!.run();
    await vi.waitFor(() => expect(d.unarchive).toHaveBeenCalledWith(open));
    await vi.waitFor(() => expect(notes[1]).toMatchObject({ tone: "ok", text: t("lib-agent-archives.restored", { title: "Fix the build" }) }));
  });

  it("says the agent is busy when the hub refuses (409), and restores before refreshing", async () => {
    const { d, notes } = deps({ archive: vi.fn().mockRejectedValue(new ApiError(409, "busy", { code: "agent_busy" })) });
    expect((await archiveAgent(open, false, d)).ok).toBe(false);
    expect(notes).toEqual([{ text: t("lib-agent-archives.busy"), tone: "error", action: undefined }]);
    expect(d.restore).toHaveBeenCalledWith(open);
    expect(vi.mocked(d.restore).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(d.refresh).mock.invocationCallOrder[0]);
    expect(d.refresh).toHaveBeenCalled();
    const close = deps({ archive: vi.fn().mockRejectedValue(new ApiError(409, "busy", { code: "agent_busy" })) });
    // "Close…" then offers to close it anyway (#7125)
    expect(await archiveAgent(open, true, close.d)).toEqual({ ok: false, busy: true });
    expect(close.notes[0].text).toBe(t("lib-agent-archives.close_busy"));
  });

  it("asks for an update when the manager cannot archive and close (501)", async () => {
    const { d, notes } = deps({ archive: vi.fn().mockRejectedValue(new ApiError(501, "old", { code: "agent_archive_close_unsupported" })) });
    expect(await archiveAgent(open, true, d)).toEqual({ ok: false });
    expect(notes[0]).toMatchObject({ tone: "error", text: t("lib-agent-archives.close_upgrade") });
  });

  it("reports how the close ended: closed, nothing to close, or still open with the hub's reason", async () => {
    const cases: [CloseResult | undefined, string, string][] = [
      [{ closed: true, outcome: "closed" }, "ok", t("lib-agent-archives.archived_closed", { title: "Fix the build" })],
      [{ closed: false, outcome: "not_running" }, "ok", t("lib-agent-archives.archived_not_running", { title: "Fix the build" })],
      [{ closed: false, outcome: "refused", reason: "Its engine keeps this session" }, "info",
        t("lib-agent-archives.archived_still_open", { title: "Fix the build", reason: "Its engine keeps this session" })],
      [{ closed: false, outcome: "offline", reason: "Its computer is offline" }, "info",
        t("lib-agent-archives.archived_still_open", { title: "Fix the build", reason: "Its computer is offline" })],
      [undefined, "info", t("lib-agent-archives.archived_close_unknown", { title: "Fix the build" })],
    ];
    for (const [close, tone, text] of cases) {
      const { d, notes } = deps({ archive: vi.fn().mockResolvedValue({ ok: true, close }) });
      const r = await archiveAgent(open, true, d);
      expect(r).toEqual({ ok: true, close });
      expect(d.archive).toHaveBeenCalledWith(open, true);
      expect(notes[0]).toMatchObject({ tone, text });
      expect(notes[0].action?.label).toBe(t("lib-agent-archives.undo"));
    }
  });

  it("reports a failed restore", async () => {
    const { d, notes } = deps({ unarchive: vi.fn().mockRejectedValue(new Error("offline")) });
    expect(await restoreAgent(open, d)).toBe(false);
    expect(notes[0].tone).toBe("error");
    expect(notes[0].text).toContain("offline");
    expect(d.refresh).toHaveBeenCalled();
  });
});
