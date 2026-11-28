import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Interaction } from "./types";

const apiOp = vi.fn();
vi.mock("./ops", () => ({ apiOp: (...a: unknown[]) => apiOp(...a) }));
const { ownerApprovalAttempt, replyOwnerApproval } = await import("./owner-approval");

const proposal: Interaction = {
  id: "oa_batch_item",
  machine_id: "m",
  session_id: "s",
  kind: "permission",
  approval_batch_id: "batch",
  owner_proposal: true,
  owner_approval_phrases: ["发", "Agree"],
  created_at: "2026-11-21T12:00:00.123456789Z",
};

beforeEach(() => apiOp.mockReset());

describe("replyOwnerApproval", () => {
  it("reuses one event id for an uncertain retry, including a remount, and never across proposal targets", () => {
    const first = ownerApprovalAttempt(proposal, null);
    expect(ownerApprovalAttempt(proposal, first)).toBe(first);
    expect(ownerApprovalAttempt(proposal, null)).toBe(first);
    const another = ownerApprovalAttempt({ ...proposal, id: "oa_batch_other" }, first);
    expect(another.target).toBe("oa_batch_other");
    expect(another.id).not.toBe(first.id);
    const replacement = ownerApprovalAttempt({ ...proposal, created_at: "2026-11-21T12:00:01.123456789Z" }, first);
    expect(replacement.target).toBe(proposal.id);
    expect(replacement.id).not.toBe(first.id);
  });

  it("sends only the authenticated exact-reply envelope, with one stable owner event id", async () => {
    apiOp.mockResolvedValue({ ok: true, approval_reply: true, interaction_id: proposal.id });
    expect(await replyOwnerApproval(proposal, "发", "client-event")).toBe("");
    expect(apiOp).toHaveBeenCalledWith("/sessions/m/s/messages", {
      method: "POST",
      opId: "client-event",
      json: { text: "发", client_msg_id: "client-event", approval_reply_to: proposal.id, approval_reply_created_at: proposal.created_at },
    });
    const body = apiOp.mock.calls[0][1].json;
    expect(body).not.toHaveProperty("quoteReply");
    expect(body).not.toHaveProperty("via");
    expect(body).not.toHaveProperty("images");
  });

  it("never infers a target for an ordinary card or words outside the proposal's exact phrases", async () => {
    for (const [ix, phrase] of [
      [{ ...proposal, owner_proposal: false }, "发"],
      [{ ...proposal, approval_batch_id: undefined }, "发"],
      [proposal, "please send it"],
      [proposal, ""],
    ] as const) {
      expect(await replyOwnerApproval(ix, phrase, "client-event")).not.toBe("");
    }
    expect(apiOp).not.toHaveBeenCalled();
  });

  it("does not treat an ordinary model-delivery receipt as an approval", async () => {
    apiOp.mockResolvedValue({ ok: true, delivered: "node" });
    expect(await replyOwnerApproval(proposal, "发", "client-event")).not.toBe("");
    apiOp.mockResolvedValue({ ok: true, approval_reply: true, interaction_id: "another-card" });
    expect(await replyOwnerApproval(proposal, "发", "client-event")).not.toBe("");
  });
});
