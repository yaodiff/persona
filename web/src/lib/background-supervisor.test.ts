import { describe, expect, it } from "vitest";
import { asBackgroundSupervisorCard, backgroundSupervisorTitle, isBackgroundSupervisor, isBackgroundSupervisorInteraction, reviewedInBackground, REVIEW_BY_BACKGROUND } from "./background-supervisor";
import { autoConversations, autoOrigin, matchesOrigin, recentConversations, unlistedConversations } from "./conversation-origins";
import type { FeedItem } from "./feed";
import type { Interaction, Session } from "./types";

const conv = (id: string, extra: Partial<Session> = {}) => ({ machine_id: "hub", session_id: id, title: id, cwd: "", status: "idle", ...extra }) as Session;

describe("the background supervisor (#5082)", () => {
  it("is the conversation the hub names, never the main one, and none on a hub before it", () => {
    expect(isBackgroundSupervisor({ main: "main", background_supervisor: "bg" }, "bg")).toBe(true);
    expect(isBackgroundSupervisor({ main: "main", background_supervisor: "bg" }, "other")).toBe(false);
    expect(isBackgroundSupervisor({ main: "main", background_supervisor: "main" }, "main")).toBe(false);
    expect(isBackgroundSupervisor({ main: "main" }, "bg")).toBe(false);
    expect(isBackgroundSupervisor(undefined, "bg")).toBe(false);
    expect(isBackgroundSupervisor(null, "bg")).toBe(false);
    expect(isBackgroundSupervisor({ main: "main", background_supervisor: "bg" }, undefined)).toBe(false);
  });

  it("is called what the hub called it, else by this page's own name (tests run in zh-Hans)", () => {
    expect(backgroundSupervisorTitle({ title: "Background supervisor" })).toBe("Background supervisor");
    for (const title of ["", "  ", "Claude Code", "assistant"]) expect(backgroundSupervisorTitle({ title })).toBe("后台督办");
    expect(backgroundSupervisorTitle()).toBe("后台督办");
  });

  it("is listed with the scheduled tasks' chats whatever origin its row carries", () => {
    expect(autoOrigin(conv("bg"), "bg")).toBe("task");
    expect(autoOrigin(conv("bg", { origin: "user" }), "bg")).toBe("task");
    expect(autoOrigin(conv("bg", { origin: "task" }), "bg")).toBe("task");
    // without the hub naming it, a row goes by its origin as before
    expect(autoOrigin(conv("bg", { origin: "user" }))).toBeUndefined();
    expect(autoOrigin(conv("other", { origin: "user" }), "bg")).toBeUndefined();
    expect(matchesOrigin(conv("bg"), "task", "bg")).toBe(true);
    expect(matchesOrigin(conv("bg"), "mine", "bg")).toBe(false);
    expect(matchesOrigin(conv("bg"), "all", "bg")).toBe(true);
    const list = [conv("main"), conv("bg", { state: "working" }), conv("morning", { origin: "task" }), conv("mine")];
    expect(autoConversations(list, "main", "bg").groups).toEqual([{ origin: "task", count: 2, working: 1, needsYou: 0 }]);
    expect(autoConversations(list, "main").groups).toEqual([{ origin: "task", count: 1, working: 0, needsYou: 0 }]);
  });

  it("is never among the sidebar's recent chats", () => {
    const at = (h: number) => new Date(Date.UTC(2026, 10, 12, 12) - h * 3_600_000).toISOString();
    const list = [conv("main", { updated_at: at(0) }), conv("bg", { updated_at: at(0.1) }), conv("a", { updated_at: at(1) }), conv("lead", { updated_at: at(2) })];
    expect(recentConversations(list, "main", ["lead"], "bg").map((c) => c.session_id)).toEqual(["a"]);
    // without its id (a hub from before it) a task's chat is still a scheduled task's, so it stays out of Recent too (#6228)
    expect(recentConversations([...list.slice(0, 1), conv("bg", { origin: "task" }), ...list.slice(2)], "main", ["lead"]).map((c) => c.session_id)).toEqual(["a"]);
  });

  it("tells its reviews from the main chat's own and from a reviewer this page doesn't know", () => {
    const review = (body?: Record<string, unknown>, kind = "review") => ({ kind, body }) as Pick<FeedItem, "kind" | "body">;
    expect(reviewedInBackground(review({ verdict: "accepted", by: REVIEW_BY_BACKGROUND }))).toBe(true);
    expect(reviewedInBackground(review({ verdict: "accepted" }))).toBe(false);
    expect(reviewedInBackground(review({ verdict: "accepted", by: "someone_newer" }))).toBe(false);
    expect(reviewedInBackground(review())).toBe(false);
    expect(reviewedInBackground(review({ by: REVIEW_BY_BACKGROUND }, "report"))).toBe(false);
  });

  it("one the held answer does not name yet has the assistant read again, as any conversation it does not list (#10190)", () => {
    const held = { background_supervisor: "bg", conversations: [conv("listed", { assistant: true, origin: "task" })] };
    const pushed = [
      conv("bg", { assistant: true, origin: "task" }), // the one named
      conv("listed", { assistant: true, origin: "task" }), // already in the answer
      conv("agent", { origin: "task" }), // not the assistant's
      conv("new", { assistant: true, origin: "task" }), // made since, or the replacement of one that is gone
    ];
    expect(unlistedConversations(held, pushed)).toEqual(["new"]);
    expect(unlistedConversations({ conversations: [] }, [conv("first", { assistant: true, origin: "task" })])).toEqual(["first"]);
    expect(unlistedConversations({ background_supervisor: "new", conversations: [] }, pushed.slice(3))).toEqual([]);
  });

  it("matches user-facing cards raised by the background supervisor and wraps them with stable identity", () => {
    const assistant = { main: "main", background_supervisor: "bg" };
    const ix = (extra: Partial<Interaction> = {}) => ({
      id: "ix-bg",
      machine_id: "m1",
      session_id: "bg",
      session_title: "assistant",
      kind: "permission",
      tool_name: "Bash",
      created_at: "2026-11-28T00:00:00Z",
      ...extra,
    }) as Interaction;

    expect(isBackgroundSupervisorInteraction(assistant, ix())).toBe(true);
    expect(isBackgroundSupervisorInteraction(assistant, ix({ session_id: "sub", folded_under: "bg" }))).toBe(true);
    expect(isBackgroundSupervisorInteraction(assistant, ix({ session_id: "side", folded_side: "bg" }))).toBe(true);
    expect(isBackgroundSupervisorInteraction(assistant, ix({ kind: "question" }))).toBe(true);
    expect(isBackgroundSupervisorInteraction(assistant, ix({ delegating: true }))).toBe(false);
    expect(isBackgroundSupervisorInteraction(assistant, ix({ kind: "question", delegating: true }))).toBe(true);
    expect(isBackgroundSupervisorInteraction(assistant, ix({ manager: "other" }), "default")).toBe(false);
    expect(isBackgroundSupervisorInteraction(assistant, ix({ manager: "default" }), "default")).toBe(true);
    expect(isBackgroundSupervisorInteraction({ main: "main" }, ix())).toBe(false);
    expect(isBackgroundSupervisorInteraction({ main: "bg", background_supervisor: "bg" }, ix())).toBe(false);

    const card = ix();
    const wrapped = asBackgroundSupervisorCard(card, "main", "hub");
    expect(wrapped).toMatchObject({
      id: "ix-bg",
      machine_id: "m1",
      session_id: "bg",
      folded_under: "main",
      folded_under_machine_id: "hub",
      session_title: "后台督办",
    });
    expect(asBackgroundSupervisorCard(card, "main", "hub")).toBe(wrapped);
    expect(asBackgroundSupervisorCard(card, "main", "m1").folded_under_machine_id).toBeUndefined();

    const child = ix({ session_id: "sub", folded_under: "bg", session_title: "Child" });
    expect(asBackgroundSupervisorCard(child, "main", "hub")).toBe(child);
  });
});
