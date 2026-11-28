// @vitest-environment jsdom
// The refusal notice is a lazy chunk: the ordinary Lines path must still draw it once that chunk arrives.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it } from "vitest";
import { chatLines } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { ChatLineView, RefusalNotice as RefusalNoticePage } from "./Lines";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("the lazy refusal notice", () => {
  it("loads through an ordinary transcript notice line", async () => {
    const item = {
      id: "r1", seq: 1, role: "system", kind: "notice", text: "Upstream refused",
      model_refusal: { subtype: "model_refusal_fallback", original_model: "primary-x", fallback_model: "fallback-y", scope: "session" },
    } as Item;
    const line = chatLines([item]).find((l) => l.kind === "notice")!;
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(<ChatLineView line={line} mid="m" sid="s" expandFold={async () => {}} />);
      await RefusalNoticePage.preload();
    });
    expect(host.textContent).toContain("Claude Code 已自动从 primary-x 切换至 fallback-y");
    await act(async () => root.unmount());
    host.remove();
  });
});
