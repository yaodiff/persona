import { beforeEach, describe, expect, it, vi } from "vitest";

const sendFile = vi.hoisted(() => vi.fn());
vi.mock("../../lib/attachments", async (real) => ({ ...(await real<typeof import("../../lib/attachments")>()), sendFile }));

import { MAX_FILE } from "../../lib/attachments";
import { uploadReports } from "./reports";

const file = (name: string, size = 3) => {
  const f = new File(["pdf"], name, { type: "application/pdf" });
  Object.defineProperty(f, "size", { value: size });
  return f;
};

describe("uploadReports", () => {
  beforeEach(() => sendFile.mockReset());

  it("keeps every file in the health space and names them in the message", async () => {
    sendFile.mockResolvedValueOnce("/h/health/reports/a-1.pdf").mockResolvedValueOnce("/h/health/reports/b c-2.pdf");
    const a = file("a.pdf"), b = file("b c.pdf");
    const text = await uploadReports("m1", [a, b], "health");
    expect(sendFile.mock.calls).toEqual([["m1", a, { dest: "health" }], ["m1", b, { dest: "health" }]]);
    expect(text).toContain("/h/health/reports/a-1.pdf '/h/health/reports/b c-2.pdf'");
  });

  it("starts nothing for a file over the limit", async () => {
    await expect(uploadReports("m1", [file("a.pdf"), file("big.pdf", MAX_FILE + 1)], "health")).rejects.toThrow("文件过大");
    expect(sendFile).not.toHaveBeenCalled();
  });

  it("passes a failed upload on", async () => {
    sendFile.mockRejectedValueOnce(new Error("no"));
    await expect(uploadReports("m1", [file("a.pdf")], "health")).rejects.toThrow("no");
  });
});
