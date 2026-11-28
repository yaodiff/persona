import { describe, expect, it } from "vitest";
import { resolveSessionLink, sessionLinkRoute, sessionLoadError } from "./session-link";
import type { Session } from "./types";

const FULL = "cafe0001-0000-4000-8000-000000000001";
const TWIN = "cafe0001-1000-4000-8000-000000000001";

function s(o: Partial<Session> & { session_id: string }): Session {
  return { machine_id: "box", title: "", cwd: "/code", status: "idle", ...o };
}

describe("resolveSessionLink", () => {
  const all = [s({ session_id: FULL }), s({ session_id: "dead0001-0000-4000-8000-000000000001" })];

  it("takes a whole id as it is, listed or not", () => {
    expect(resolveSessionLink(all, "box", FULL, true)).toEqual({ kind: "exact" });
    // not on the list but shaped like an id: the node may still have it (an older conversation)
    expect(resolveSessionLink([], "box", FULL, true)).toEqual({ kind: "exact" });
    // before the list is in, a whole id opens right away
    expect(resolveSessionLink([], "box", FULL, false)).toEqual({ kind: "exact" });
  });

  it("settles a unique prefix of 8 or more characters on that session", () => {
    expect(resolveSessionLink(all, "box", "cafe0001", true)).toEqual({ kind: "prefix", session: all[0] });
    expect(resolveSessionLink(all, "box", "cafe0001-0000", true)).toEqual({ kind: "prefix", session: all[0] });
  });

  it("lists every session a prefix fits, newest first, instead of picking one", () => {
    const older = s({ session_id: FULL, activity_at: "2026-11-01T00:00:00Z" });
    const newer = s({ session_id: TWIN, activity_at: "2026-11-04T00:00:00Z" });
    expect(resolveSessionLink([older, newer], "box", "cafe0001", true)).toEqual({ kind: "ambiguous", candidates: [newer, older] });
  });

  it("asks the node as it is when nothing on the list fits", () => {
    expect(resolveSessionLink(all, "box", "deadbeef", true)).toEqual({ kind: "unlisted" });
    // the same id on another machine does not count
    expect(resolveSessionLink(all, "mac", "cafe0001", true)).toEqual({ kind: "unlisted" });
    // shorter than 8 is never looked up as a prefix
    expect(resolveSessionLink(all, "box", "cafe000", true)).toEqual({ kind: "unlisted" });
  });

  it("waits for the list before settling a prefix", () => {
    expect(resolveSessionLink([], "box", "cafe0001", false)).toEqual({ kind: "pending" });
  });
});

describe("sessionLinkRoute", () => {
  it("is the whole id's /s/ link, or its tab's /t/ link when it has one", () => {
    const plain = s({ session_id: FULL });
    expect(sessionLinkRoute([plain], plain, "term")).toEqual({ name: "session", mid: "box", sid: FULL, view: "term" });
    const tabbed = s({ session_id: FULL, space_id: "w1", slot_id: "w1:t1" });
    expect(sessionLinkRoute([tabbed], tabbed)).toEqual({ name: "tab", mid: "box", space: "w1", slot: "w1:t1", sid: FULL });
  });
});

describe("sessionLoadError", () => {
  it("says a missing conversation in words", () => {
    expect(sessionLoadError("transcript for session cafe0001 not found")).toBe("找不到该会话，可能已被删除或链接中的 ID 不完整。");
    expect(sessionLoadError("node box: subagent a1b2 not found")).toBe("找不到该会话，可能已被删除或链接中的 ID 不完整。");
  });

  it("leaves other errors as they are", () => {
    expect(sessionLoadError("连不上 hub")).toBe("连不上 hub");
  });
});
