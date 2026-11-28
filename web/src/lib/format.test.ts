import { describe, expect, it } from "vitest";
import { ago, attentionReason, baseName, bytes, externalLink, failureReason, failureText, sessionPreview, sessionTitle } from "./format";
import { formatRelative } from "../i18n";
import type { Session } from "./types";

const session = (o: Partial<Session>): Session => ({ machine_id: "m", session_id: "s", cwd: "/x", status: "idle", ...o }) as Session;

describe("remote path names", () => {
  it.each([
    ["C:\\Work\\App", "App"],
    ["c:/Work/App/", "App"],
    ["C:\\Work\\App\\", "App"],
    ["\\\\server\\share\\Project", "Project"],
    ["//server/share/Project/", "Project"],
    ["/work/App/", "App"],
    ["/work/a\\b", "a\\b"],
    ["/", "/"],
    ["", ""],
  ])("shows the last component of %s", (path, name) => {
    expect(baseName(path)).toBe(name);
  });
});

describe("sessionTitle", () => {
  const tab = { slot_id: "w1:t1", slot_name: "Export work", space_kind: "herdr" };
  it("prefers a named herdr tab when enabled", () => {
    const s = session({ ...tab, title: "CLI session name" });
    expect(sessionTitle(s)).toBe(s.title);
    expect(sessionTitle(s, true)).toBe(tab.slot_name);
  });
  it("uses a tab in place of an empty or shell prompt title with the setting off", () => {
    for (const title of ["", "  ", "user@host: /workspace", "user@host:~/workspace", "user@host: path"]) {
      expect(sessionTitle(session({ ...tab, title }))).toBe(tab.slot_name);
    }
    expect(sessionTitle(session({ ...tab, title: "Contact user@host: about exports" }))).toBe("Contact user@host: about exports");
  });
  it("keeps the CLI name in other engines and for sessions without a tab", () => {
    for (const s of [session({ ...tab, assistant: true }), session({ ...tab, space_kind: "orca" }), session({ slot_name: "Workspace" }), session({})]) {
      expect(sessionTitle({ ...s, title: "CLI session name" }, true)).toBe("CLI session name");
    }
  });
});

// A botmux session's reason for waiting and its Feishu topic (attention_reason, external_url; docs/integrations/engines.md "botmux").
describe("sessionPreview", () => {
  it("shows why it waits while it needs you", () => {
    expect(sessionPreview(session({ status: "blocked", state: "needs_you", last_text: "正在跑全量", attention_reason: "本轮执行失败，再发一条消息继续" }))).toBe("本轮执行失败，再发一条消息继续");
  });
  it("keeps the last words otherwise", () => {
    expect(sessionPreview(session({ status: "working", state: "working", last_text: "在补测试", attention_reason: "旧的原因" }))).toBe("在补测试");
    expect(sessionPreview(session({ status: "blocked", state: "needs_you", last_text: "在等你", attention_reason: "  " }))).toBe("在等你");
    expect(sessionPreview(session({}))).toBe("");
  });
});

// The host stopped its turn on an error (failure_reason, iOS listPreview / FailureNote).
describe("failureReason", () => {
  it("is the notice without its warning sign while the session is not working", () => {
    expect(failureReason(session({ state: "idle", failure_reason: "⚠️ 模型接口出错，已停下" }))).toBe("模型接口出错，已停下");
    expect(failureReason(session({ state: "offline", status: "offline", failure_reason: "出错了" }))).toBe("出错了");
  });
  it("is gone while it works again, or when there is none", () => {
    expect(failureReason(session({ state: "working", status: "working", failure_reason: "出错了" }))).toBeUndefined();
    expect(failureReason(session({ failure_reason: " ⚠ " }))).toBeUndefined();
    expect(failureReason(session({}))).toBeUndefined();
    expect(failureReason(undefined)).toBeUndefined();
  });
  it("strips only the leading sign", () => {
    expect(failureText("⚠ 第 ⚠ 步失败")).toBe("第 ⚠ 步失败");
  });
  it("shows in the list in place of the last words", () => {
    expect(sessionPreview(session({ state: "idle", last_text: "改好了", failure_reason: "⚠️ 本轮出错" }))).toBe("本轮出错");
    expect(sessionPreview(session({ state: "needs_you", status: "blocked", attention_reason: "等你确认", failure_reason: "本轮出错" }))).toBe("等你确认");
  });
});

describe("attentionReason", () => {
  it("is there only while it needs you", () => {
    expect(attentionReason(session({ status: "blocked", state: "needs_you", attention_reason: " 等你确认 " }))).toBe("等你确认");
    expect(attentionReason(session({ status: "offline", state: "offline", attention_reason: "等你确认" }))).toBeUndefined();
    expect(attentionReason(session({ status: "blocked", state: "needs_you" }))).toBeUndefined();
  });
});

describe("externalLink", () => {
  it("takes an https link", () => {
    expect(externalLink(session({ external_url: "https://applink.example.com/thread?x=1" }))).toBe("https://applink.example.com/thread?x=1");
  });
  it("drops anything else, including single-slash relative URLs, userinfo, backslashes, and control chars", () => {
    for (const url of [
      "http://applink.example.com/t",
      "javascript:alert(1)",
      "lark://open",
      "https:/admin",
      "https:admin",
      "https://user:pw@evil.example/t",
      "https://@evil.example/t",
      "https://good.example\\@evil.example/t",
      "https://applink.example.com/t\t1",
      "https://applink.example.com/t\n1",
      "https://",
      "not a url",
      "",
    ]) {
      expect(externalLink(session({ external_url: url })), url).toBeUndefined();
    }
    expect(externalLink(undefined)).toBeUndefined();
  });
});

describe("ago", () => {
  const at = (d: number, h: number) => new Date(2026, 10, d, h, 0).getTime();
  const iso = (ms: number) => new Date(ms).toISOString();

  it("counts days by the calendar, not by 24 hour spans (#4281)", () => {
    // yesterday morning seen late tonight: 40 h, still yesterday
    expect(ago(iso(at(4, 7)), at(5, 23))).toBe(formatRelative(-1, "day"));
    // the evening before yesterday seen just past midnight: 30 h, two days ago
    expect(ago(iso(at(3, 19)), at(5, 1))).toBe(formatRelative(-2, "day"));
    // six calendar days back still reads as days; seven is a date
    expect(ago(iso(at(1, 23)), at(7, 1))).toBe(formatRelative(-6, "day"));
    expect(ago(iso(at(1, 1)), at(8, 23))).not.toBe(formatRelative(-7, "day"));
  });

  it("keeps minutes and hours as they were", () => {
    expect(ago(iso(at(5, 1)), at(5, 3))).toBe(formatRelative(-2, "hour"));
    expect(ago(iso(at(4, 23)), at(5, 1))).toBe(formatRelative(-2, "hour"));
  });

  it("steps up a unit once rounding reaches it, not 60 minutes or 24 hours ago", () => {
    const now = at(5, 23);
    expect(ago(iso(now - 3540_000), now)).toBe(formatRelative(-59, "minute"));
    expect(ago(iso(now - 3570_000), now)).toBe(formatRelative(-1, "hour"));
    expect(ago(iso(now - 3599_000), now)).toBe(formatRelative(-1, "hour"));
    expect(ago(iso(now - 84_600_000), now)).toBe(formatRelative(-1, "day"));
    // 23.5 hours back within one calendar day still reads as a day ago, not "today"
    const late = new Date(2026, 10, 5, 23, 50).getTime();
    expect(ago(iso(late - 84_600_000), late)).toBe(formatRelative(-1, "day"));
  });
});

describe("baseName", () => {
  it("names the last folder of a path, without a trailing separator", () => {
    expect(baseName("/home/me/code/app")).toBe("app");
    expect(baseName("/home/me/code/app/")).toBe("app");
    expect(baseName("app")).toBe("app");
    expect(baseName("")).toBe("");
    expect(baseName(undefined)).toBe("");
  });

  it("reads a Windows computer's path by its own separator", () => {
    expect(baseName("C:\\Users\\me\\code\\app")).toBe("app");
    expect(baseName("C:\\Users\\me\\code\\app\\")).toBe("app");
    expect(baseName("d:/code/app")).toBe("app");
    expect(baseName("C:\\code/mixed\\app")).toBe("app");
    expect(baseName("\\\\server\\share\\app")).toBe("app");
  });

  it("keeps a backslash that is part of a name anywhere else", () => {
    expect(baseName("/home/me/odd\\name")).toBe("odd\\name");
  });
});

// File sizes (the Files panel, the file viewer, chat file cards…): the unit and the decimals follow the figure as it is
// shown, after rounding, as the machine pages' bytesBin does (#9969; Android #9900, #9917).
describe("bytes", () => {
  it("moves up a unit once the rounded figure reaches 1024", () => {
    expect(bytes(1_047_551)).toBe("1023 KB");
    expect(bytes(1_048_064)).toBe("1.0 MB");
    expect(bytes(1_048_575)).toBe("1.0 MB");
    expect(bytes(1023)).toBe("1023 B");
    expect(bytes(1023.5)).toBe("1.0 KB");
    expect(bytes(1023.5 * 1024 ** 2)).toBe("1.0 GB");
  });

  it("drops the decimal once the figure rounds to 100", () => {
    expect(bytes(102_348)).toBe("99.9 KB");
    expect(bytes(102_350)).toBe("100 KB");
    expect(bytes(99.96 * 1024 ** 2)).toBe("100 MB");
  });

  it("reads every other size as before", () => {
    expect(bytes(undefined)).toBe("—");
    expect(bytes(Number.NaN)).toBe("—");
    expect(bytes(0)).toBe("0 B");
    expect(bytes(512)).toBe("512 B");
    expect(bytes(1024)).toBe("1.0 KB");
    expect(bytes(1536)).toBe("1.5 KB");
    expect(bytes(10 * 1024 ** 2)).toBe("10.0 MB");
    expect(bytes(250 * 1024 ** 2)).toBe("250 MB");
    expect(bytes(3 * 1024 ** 5)).toBe("3072 TB");
  });
});
