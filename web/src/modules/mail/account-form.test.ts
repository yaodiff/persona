import { describe, expect, it } from "vitest";
import { mailSummary } from "../../lib/agent-settings";
import { EMPTY_MAIL_FORM, mailAccountLine, mailCodeGuide, mailErrors, mailProviderBadge, clientIDError, MAIL_KINDS, mailRequest } from "./account-form";

describe("mail account form", () => {
  it("needs an address and a password; Gmail is for the phone", () => {
    expect(mailErrors({ ...EMPTY_MAIL_FORM })).toEqual({ email: "请输入邮箱地址", password: "请输入密码或授权码" });
    expect(mailErrors({ ...EMPTY_MAIL_FORM, email: "me@", password: "x" }).email).toBe("邮箱地址格式不正确");
    expect(mailErrors({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "x" })).toEqual({});
    expect(mailErrors({ ...EMPTY_MAIL_FORM, kind: "gmail" }).kind).toContain("手机");
    expect(mailErrors({ ...EMPTY_MAIL_FORM, kind: "outlook" }).kind).toBe("Outlook 需要通过 Microsoft 账号登录，目前仅支持在手机端添加。");
    expect(MAIL_KINDS.map((k) => k.k)).toEqual(["gmail", "outlook", "feishu", "imap"]);
    expect(mailErrors({ ...EMPTY_MAIL_FORM, kind: "feishu", email: "me@example.com" }).password).toContain("飞书");
    expect(mailErrors({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "x", imapHost: "imap.example.com", imapPort: "abc" }).imapPort).toBeTruthy();
  });

  it("tells QQ and NetEase addresses where to make the authorization code (#5123)", () => {
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@QQ.com " })).toEqual({ provider: "qq", webMail: "https://mail.qq.com" });
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@foxmail.com" })?.provider).toBe("qq");
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@126.com" })).toEqual({ provider: "netease", webMail: "https://mail.126.com" });
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@vip.163.com" })?.webMail).toBe("https://vip.163.com");
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@188.com" })?.webMail).toBe("https://www.188.com");
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@example.com" })).toBeNull();
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me" })).toBeNull();
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "feishu", email: "me@qq.com" })).toBeNull();
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, email: "me@qq.com", imapHost: "imap.example.com" })).toBeNull();
  });

  it("chooses SMTP security from the parsed port (#4879)", () => {
    const form = { ...EMPTY_MAIL_FORM, email: "me@example.com", password: "pw", smtpHost: "smtp.example.com" };
    for (const smtpPort of ["0587", "587", " 0587 ", "587.0", "5.87e2"]) {
      expect(mailErrors({ ...form, smtpPort })).toEqual({});
      expect(mailRequest({ ...form, smtpPort })).toMatchObject({ smtp_port: 587, smtp_security: "starttls" });
    }
    for (const smtpPort of ["465", "0465", "2525"]) {
      expect(mailErrors({ ...form, smtpPort })).toEqual({});
      expect(mailRequest({ ...form, smtpPort })).toMatchObject({ smtp_port: Number(smtpPort), smtp_security: "tls" });
    }
    for (const smtpPort of ["abc", "", "0", "65536", "587.5"]) expect(mailErrors({ ...form, smtpPort }).smtpPort).toBeTruthy();
    const omitted = { ...form, smtpHost: " ", smtpPort: "abc" };
    expect(mailErrors(omitted)).toEqual({});
    expect(mailRequest(omitted)).toEqual({ name: "me@example.com", email: "me@example.com", provider: "imap", auth: "password", password: "pw" });
  });

  it("builds the hub's request", () => {
    expect(mailRequest({ ...EMPTY_MAIL_FORM, kind: "gmail" })).toBeNull();
    expect(mailRequest({ ...EMPTY_MAIL_FORM, kind: "outlook", email: "me@example.com", password: "pw" })).toBeNull();
    expect(mailRequest({ ...EMPTY_MAIL_FORM, kind: "feishu", email: " me@example.com ", password: "pw", imapHost: "ignored" })).toEqual({
      name: "me@example.com",
      email: "me@example.com",
      provider: "feishu",
      auth: "password",
      password: "pw",
    });
    // servers left empty: the hub guesses them from the domain
    expect(mailRequest({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "pw" })).toEqual({ name: "me@example.com", email: "me@example.com", provider: "imap", auth: "password", password: "pw" });
    expect(mailRequest({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "pw", imapHost: "imap.example.com", imapPort: "993", smtpHost: "smtp.example.com", smtpPort: "587" })).toMatchObject({
      imap_host: "imap.example.com",
      imap_port: 993,
      smtp_host: "smtp.example.com",
      smtp_port: 587,
      smtp_security: "starttls",
    });
    expect(mailRequest({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "pw", smtpHost: "smtp.example.com", smtpPort: "465" })?.smtp_security).toBe("tls");
  });

  // #2442: a hub without google.client_id cannot sign in with Google, so Gmail is added here with an app password.
  it("adds Gmail with an app password on a hub without Google sign-in", () => {
    const gmail = { ...EMPTY_MAIL_FORM, kind: "gmail" as const };
    expect(mailErrors(gmail, true)).toEqual({ email: "请输入邮箱地址", password: "请输入 Gmail 的应用专用密码" });
    expect(mailErrors({ ...gmail, email: "me@mail.test", password: "abcd efgh ijkl mnop" }, true)).toEqual({});
    expect(mailRequest({ ...gmail, email: " me@mail.test ", password: "abcd efgh ijkl mnop" }, true)).toEqual({
      name: "me@mail.test",
      email: "me@mail.test",
      provider: "gmail",
      auth: "password",
      password: "abcd efgh ijkl mnop",
      imap_host: "imap.gmail.com",
      imap_port: 993,
      smtp_host: "smtp.gmail.com",
      smtp_port: 465,
      smtp_security: "tls",
    });
  });

  it("describe accounts", () => {
    expect(mailAccountLine({ id: "a", email: "me@example.com", status: "error", error: "auth failed" })).toBe("auth failed");
    expect(mailAccountLine({ id: "a", email: "me@example.com", auth: "oauth2", provider: "gmail" })).toBe("Gmail · Google 账号登录");
    expect(mailAccountLine({ id: "a", email: "me@example.com", auth: "oauth2", provider: "outlook" })).toBe("Outlook · Microsoft 账号登录");
    expect(mailAccountLine({ id: "a", email: "me@example.com", provider: "feishu" })).toBe("飞书邮箱");
    expect(mailProviderBadge({ id: "a", email: "x", provider: "outlook" })).toBe("Outlook");
    expect(mailProviderBadge({ id: "a", email: "x", provider: "gmail" })).toBe("Gmail");
    expect(mailProviderBadge({ id: "a", email: "x", provider: "feishu" })).toBe("飞书");
    expect(mailProviderBadge({ id: "a", email: "x", provider: "imap" })).toBeNull();
    expect(mailAccountLine({ id: "a", email: "me@example.com", provider: "imap", imap_host: "imap.example.com" })).toBe("IMAP · imap.example.com");
    expect(mailSummary(null)).toBe("");
    expect(mailSummary([])).toBe("未添加");
    expect(mailSummary([{ id: "a", email: "x", status: "error" }, { id: "b", email: "y" }])).toBe("2 个 · 有错误");
    expect(mailSummary([{ id: "a", email: "x" }])).toBe("1 个账户");
  });
});

describe("Outlook client ID", () => {
  it("checks the shape the hub does", () => {
    expect(clientIDError("")).toContain("应用程序(客户端) ID");
    expect(clientIDError("not-an-id")).toContain("客户端 ID 格式不正确");
    expect(clientIDError(" 00000000-0000-0000-0000-000000000000 ")).toBe("");
  });
});
