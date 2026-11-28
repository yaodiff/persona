import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Interaction } from "./types";

const op = vi.hoisted(() => vi.fn());
vi.mock("./ops", () => ({ apiOp: op }));
import { askApprovalQuestion } from "./approval-question";

describe("approval clarification occurrence", () => {
  beforeEach(() => { op.mockReset(); op.mockResolvedValue({ ok: true }); });

  it("pins the question to the approval occurrence the owner saw", async () => {
    const ix: Interaction = {
      id: "approval", machine_id: "machine", session_id: "conversation", kind: "permission",
      created_at: "2026-01-02T03:04:05.000000001Z",
    };
    await askApprovalQuestion(ix, "Which exact change?", "question-op");
    expect(op).toHaveBeenCalledWith("/interactions/approval/clarify", {
      method: "POST",
      opId: "question-op",
      json: { text: "Which exact change?", client_msg_id: "question-op" },
      headers: { "X-Persona-Interaction-Created-At": ix.created_at },
    });
  });
});
