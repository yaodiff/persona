// The stored `context` of a mail item in the shapes the hub writes (server/internal/assistant/inbox_mail.go
// readMailBody and mailContext), and what the page shows of them with mail off (#15995).
import { describe, expect, it } from "vitest";
import { storedMailClipping, storedMailText } from "./mail-stored";

const read = (body: string) =>
  `邮箱 me@example.com 收到的未读邮件（共 2 封，下面是最新一封）：\n发件人：Ana <ana@example.com>\n收件人：me@example.com\n抄送：\n时间：2026-11-20T09:00:00Z\n主题：Weekly sync\n\n${body}`;
const listed = (...excerpts: string[]) =>
  `邮箱 me@example.com 收到的未读邮件：\n${excerpts.map((x) => `---\n发件人：Ana <ana@example.com>\n收件人：me@example.com\n时间：2026-11-20T09:00:00Z\n主题：Weekly sync\n${x}`).join("\n")}`;

describe("storedMailText", () => {
  it("shows the mail's own words, without the heading and headers written for the model", () => {
    expect(storedMailText(read("Hi,\n\nThe agenda is attached.\nAna"), "Hi, The agenda")).toBe("Hi,\n\nThe agenda is attached.\nAna");
  });

  it("keeps a body that itself holds header-like lines and blank lines", () => {
    const body = "Forwarded:\n\n主题：old\n\nSee above.";
    expect(storedMailText(read(body), "")).toBe(body);
  });

  it("shows each excerpt when the body was never read in full", () => {
    expect(storedMailText(listed("First excerpt", "Second excerpt"), "p")).toBe("First excerpt\n\nSecond excerpt");
    expect(storedMailText(listed("Only one").replace("收到的未读邮件", "收到的邮件（来时已读）"), "p")).toBe("Only one");
  });

  it("takes the item's own mail from a merged item's context, which holds its members' first", () => {
    expect(storedMailText(`${listed("A member's excerpt")}\n\n${read("The newest mail")}`, "p")).toBe("The newest mail");
    expect(storedMailText(`${read("A member's mail")}\n\n${listed("The newest excerpt")}`, "p")).toBe("The newest excerpt");
  });

  it("shows a context in another shape as it is, and the preview when there is none", () => {
    expect(storedMailText("Some other text\nthe hub kept", "p")).toBe("Some other text\nthe hub kept");
    expect(storedMailText(undefined, " The preview ")).toBe("The preview");
    expect(storedMailText("  ", undefined)).toBe("");
    // read in full, but the mail had no text: the preview, never an empty page with words in the list
    expect(storedMailText(read(""), "p")).not.toBe("");
  });
});

describe("storedMailClipping", () => {
  it("reads the item page's flag as a closed state; anything but a boolean is unknown, never whole", () => {
    expect(storedMailClipping({ mail_body_truncated: true })).toBe("clipped");
    expect(storedMailClipping({ mail_body_truncated: false })).toBe("whole");
    for (const item of [undefined, {}, { mail_body_truncated: undefined }, { mail_body_truncated: null }, { mail_body_truncated: "false" }, { mail_body_truncated: 0 }])
      expect(storedMailClipping(item), JSON.stringify(item)).toBe("unknown");
  });
});
