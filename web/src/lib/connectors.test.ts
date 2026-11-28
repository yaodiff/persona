import { afterEach, describe, expect, it, vi } from "vitest";
import {
  canSendStep, chatHeadline, connectorBrand, connectorOpenTitle, connectorState, connectorSummary, connectorTone, deletedLine, feedsFooter, fieldInput, githubInboxFooter, githubMachineLine,
  githubOptions, githubScopeAdvice, deleteIMessageData, imessageAIFooter, setIMessageAI, isPolledStep, isStarting, labelColor, listConnectors, logoutMessage, mediaLine, mergeNewest, mergeOlder, mutableRepo,
  otherConnectors, reachMessage, setGitHubAuth, signInFooter, signsInHere, startsGroup, stepView, storesData, switchFooter, switchLabel, undescribed,
  type HubConnector, type ThreadMessage, type ThreadPage,
} from "./connectors";

const conn = (status: string, extra: Partial<HubConnector> = {}): HubConnector => ({ id: "github", name: "GitHub", status, ...extra });

afterEach(() => vi.unstubAllGlobals());

describe("连接器的状态", () => {
  it("says each state and its dot; an unknown state is not 出错了", () => {
    expect(connectorState(conn("not_signed_in"))).toBe("未登录");
    expect(connectorState(conn("rate_limited"))).toBe("请求过于频繁，已暂停");
    expect(connectorState(conn("weird"))).toBe("状态未知");
    expect(["ok", "rate_limited", "error", "no_permission", "disabled", "weird"].map((s) => connectorTone(conn(s)))).toEqual([
      "green", "orange", "red", "red", "idle", "idle",
    ]);
    expect(connectorTone(null)).toBe("idle");
  });

  it("names the account when connected, else the state", () => {
    expect(connectorSummary(conn("ok", { account: "alice" }))).toBe("alice");
    expect(connectorSummary(conn("ok"))).toBe("已连接");
    expect(connectorSummary(conn("disabled", { account: "alice" }))).toBe("已关闭");
  });

  it("tells a sign-in here from a program that has not started", () => {
    expect(signsInHere(conn("ok", { capabilities: ["status", "login"] }))).toBe(true);
    expect(signsInHere(conn("ok", { capabilities: ["status"] }))).toBe(false);
    expect(undescribed(conn("error", { capabilities: [] }))).toBe(true);
    expect(undescribed(conn("ok"))).toBe(false); // an older hub without capabilities
    expect(isStarting(conn("error", { capabilities: [], enabled: true }))).toBe(true);
    expect(isStarting(conn("disabled", { capabilities: [], enabled: false }))).toBe(false);
  });

  it("leaves mail and Feishu to their own groups", () => {
    const list = [conn("ok", { id: "lark" }), conn("ok", { id: "larkcli" }), conn("ok", { id: "mail" }), conn("ok"), conn("ok", { id: "telegram" })];
    expect(otherConnectors(list).map((c) => c.id)).toEqual(["github", "telegram"]);
    // no Lark gateway group (a hub without lark): the gateway connector is listed with the rest, but lark-cli stays in the bot group
    expect(otherConnectors(list, false).map((c) => c.id)).toEqual(["lark", "github", "telegram"]);
  });

  it("has a mark only for the connectors that have one; the rest draw the generic icon", () => {
    expect(["github", "telegram", "lark", "larkcli"].map(connectorBrand)).toEqual(["github", "telegram", "lark", "lark"]);
    // an extension's connector draws the generic icon; a plugin's id may also be an Object.prototype name
    expect(["examplechat", "example", "constructor"].map(connectorBrand)).toEqual([undefined, undefined, undefined]);
  });

  it("words a listed connector from the table and any other from its own name, never Telegram's", () => {
    const off = (id: string, name: string) => conn("disabled", { id, name, enabled: false });
    expect(switchFooter(off("telegram", "Telegram"))).toContain("私聊和群");
    expect(signInFooter(conn("not_signed_in", { id: "telegram", name: "Telegram" }))).toContain("api_id");
    // an extension's connector says its own storage words in its sign-in hint, so the footer is the generic one
    expect(signInFooter(conn("not_signed_in", { id: "examplechat", name: "示例聊天" }))).toBe("登录状态会加密保存在 hub 上，退出登录时将自动删除。");
    expect(connectorOpenTitle("telegram")).toBe("在 Telegram 中回复");

    // not in the table: the generic wording, with the connector's own name where one is shown
    const plugin = off("example", "示例");
    expect(switchFooter(plugin)).toBe("开启后即可登录。登录后，其内容将同步至知识库和收件箱，助手也可以搜索相关内容。");
    expect(signInFooter(plugin)).toBe("登录状态会加密保存在 hub 上，退出登录时将自动删除。");
    expect(connectorOpenTitle("example")).toBeUndefined();
    expect(switchFooter({ ...plugin, enabled: true })).toContain("关闭后将停止读取 示例");
    for (const text of [switchFooter(plugin), signInFooter(plugin)]) expect(text).not.toMatch(/Telegram|api_id|私聊和群/);
  });

  it("says 启用 for a chat bot that stores nothing (WeChat), 同步 for one that brings data in, as the apps do", () => {
    const wechat = (enabled: boolean) => conn("ok", { id: "wechat", name: "微信", enabled, capabilities: ["status", "login"] });
    expect(storesData(wechat(true))).toBe(false);
    expect(switchLabel(wechat(true))).toBe("启用 微信");
    expect(switchFooter(wechat(true))).toBe("关闭后将停止运行 微信，并保留当前登录状态。");
    expect(switchFooter(wechat(false))).toBe("开启后即可登录。");
    for (const text of [switchFooter(wechat(true)), switchFooter(wechat(false))]) expect(text).not.toMatch(/知识库|收件箱|停止读取/);

    const telegram = conn("ok", { id: "telegram", name: "Telegram", capabilities: ["status", "login", "archive", "inbox"] });
    expect(storesData(telegram)).toBe(true);
    expect(switchLabel(telegram)).toBe("同步 Telegram");
    expect(storesData(conn("ok", { capabilities: ["status", "inbox"] }))).toBe(true);
    // not described yet (a program not started, a hub from before capabilities): as before there were bots
    expect(storesData(conn("error", { capabilities: [] }))).toBe(true);
    expect(storesData(conn("ok"))).toBe(true);
    expect(switchLabel(conn("ok"))).toBe("同步 GitHub");
  });

  it("tells a sign-out that ends the remote session from one that only forgets the hub's copy", () => {
    // Telegram's log-out call ends the session at Telegram: the old wording stays, word for word
    expect(logoutMessage(conn("ok", { id: "telegram", name: "Telegram" }))).toBe(
      "hub 上保存的 API 凭据和登录状态将被删除，同时会终止该设备在 Telegram 中的会话。下次使用需重新登录。",
    );
    // An extension's app key, GitHub's device token, an unknown plugin or an Object.prototype name: nothing is ended or revoked over there
    for (const [id, name] of [["examplechat", "示例聊天"], ["github", "GitHub"], ["example", "示例"], ["constructor", "示例"]]) {
      const text = logoutMessage(conn("ok", { id, name }));
      expect(text).toContain("访问权限不会自动撤销");
      expect(text).not.toMatch(/终止该设备|API 凭据|Telegram/);
    }
  });

  it("words the footers", () => {
    expect(switchFooter(conn("ok"))).toContain("关闭后将停止读取 GitHub");
    expect(feedsFooter(conn("ok", { archive: true, inbox: false }))).toBe(
      "已同步至知识库：GitHub 的内容会存入知识库，供助手和 AI 分身检索。关闭后将不再保存新内容，已保存的数据会继续保留。\n不进入收件箱：不再接收新条目，已有条目仍会保留。",
    );
    expect(feedsFooter(conn("ok"))).toBe("");
    expect(deletedLine({ chats: 3, people: 5, inbox: 2 })).toBe("已删除。知识库：3 个会话、5 人；收件箱：2 条。");
    expect(deletedLine({ chats: 1 }, false)).toBe("已删除。知识库：1 个会话、0 人。");
  });
});

describe("分步登录", () => {
  it("sends a step only when every field is filled in", () => {
    const p = { step: "credentials", fields: [{ name: "api_id", label: "api_id" }, { name: "api_hash", label: "api_hash", secret: true }] };
    expect(canSendStep(p, { api_id: "1" })).toBe(false);
    expect(canSendStep(p, { api_id: "1", api_hash: "  " })).toBe(false);
    expect(canSendStep(p, { api_id: "1", api_hash: "x" })).toBe(true);
    expect(canSendStep(null, {})).toBe(false);
  });

  it("draws only the steps it knows; the rest say they can't be drawn (#1693)", () => {
    const rows = ["101", "010", "101"];
    expect(stepView({ step: "credentials", fields: [{ name: "phone", label: "手机号" }] })).toBe("fields");
    expect(stepView({ kind: "device_code", user_code: "ABCD-1234" })).toBe("device_code");
    expect(stepView({ kind: "qr", url: "https://example.com/q", qr: { size: 3, rows } })).toBe("qr");
    expect(stepView({ kind: "qr", url: "https://example.com/q" })).toBe("qr_link");
    expect(stepView({ kind: "qr", qr: { size: 0, rows: [] } })).toBe("unsupported");
    expect(stepView({ kind: "qr", url: "weixin-only-content" })).toBe("unsupported");
    expect(stepView({ kind: "open_url", url: "https://example.com/auth" })).toBe("unsupported");
    expect(stepView({ kind: "device_code" })).toBe("unsupported");
    expect(stepView({ step: "wait", hint: "等一下" })).toBe("unsupported"); // no fields: "Next" could never light up
  });

  it("asks a code or a QR code again by itself, not a form or a step it can't draw", () => {
    expect(isPolledStep({ kind: "qr", step: "qr", qr: { size: 1, rows: ["1"] } })).toBe(true);
    expect(isPolledStep({ kind: "qr", step: "qr", url: "https://example.com/q" })).toBe(true);
    expect(isPolledStep({ kind: "device_code", user_code: "ABCD-1234" })).toBe(true);
    expect(isPolledStep({ kind: "qr", step: "qr" })).toBe(false);
    expect(isPolledStep({ kind: "mystery" })).toBe(false);
    expect(isPolledStep({ step: "credentials", fields: [{ name: "phone", label: "手机号" }] })).toBe(false);
    expect(isPolledStep({ kind: "qr", done: true, qr: { size: 1, rows: ["1"] } })).toBe(false);
    expect(isPolledStep(null)).toBe(false);
  });

  it("knows each field's input", () => {
    expect(fieldInput({ name: "password", label: "", secret: true }).type).toBe("password");
    expect(fieldInput({ name: "phone", label: "" }).inputMode).toBe("tel");
    expect(fieldInput({ name: "code", label: "" }).inputMode).toBe("numeric");
  });
});

describe("GitHub", () => {
  it("says each computer's gh login", () => {
    expect(githubMachineLine({ id: "a", name: "A", state: "signed_in", account: "alice" })).toBe("gh 已登录 · alice");
    expect(githubMachineLine({ id: "a", name: "A", state: "signed_in", missing: [{ scope: "repo", affects: [] }, { scope: "notifications", affects: [] }] }))
      .toBe("gh 已登录 · 缺少 repo、notifications 权限");
    expect(githubMachineLine({ id: "a", name: "A", state: "offline" })).toBe("离线");
    expect(githubMachineLine({ id: "a", name: "A", state: "error", error: "boom" })).toBe("无法读取 gh 登录状态：boom");
  });

  it("advises on missing scopes by where the token comes from", () => {
    const missing = [{ scope: "repo", affects: [] }, { scope: "notifications", affects: [] }];
    expect(githubScopeAdvice({ source: "machine", machine: "m", machines: [{ id: "m", name: "MacBook" }] }, missing)).toBe(
      "MacBook 上的 gh 缺少 repo、notifications 权限。请在该电脑上运行 gh auth refresh -s repo,notifications，或改用设备码登录。",
    );
    expect(githubScopeAdvice({ source: "device", machines: [] }, missing)).toContain("设备码登录缺少");
  });

  it("reads its inbox options and checks a repository to mute", () => {
    expect(githubOptions({ ci: "mine", muted_repos: ["a/b", 3] })).toEqual({ ci: "mine", muted_repos: ["a/b"], ignore_branches: undefined });
    expect(githubOptions({ muted_repos: [] })).toBeNull();
    expect(githubOptions(null)).toBeNull();
    expect(mutableRepo(" owner/name ", [])).toBe("owner/name");
    expect(mutableRepo("owner/name", ["owner/name"])).toBeNull();
    expect(mutableRepo("owner", [])).toBeNull();
    expect(githubInboxFooter({ ci: "off", muted_repos: [] })).toMatch(/^CI 失败不会进入收件箱。.*自动创建的分支（train\/\*）不会触发提醒/);
  });

  it("asks the hub with the right bodies", async () => {
    const calls: { url: string; init: RequestInit }[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "jd_test" });
    vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      return new Response(JSON.stringify({ connectors: [conn("ok")], source: "device", machines: [] }), { status: 200 });
    }));
    expect((await listConnectors(true)).map((c) => c.id)).toEqual(["github"]);
    await setGitHubAuth(null);
    await setGitHubAuth("mbp");
    expect(calls.map((c) => c.url)).toEqual(["/api/v1/connectors?all=1", "/api/v1/connectors/github/auth", "/api/v1/connectors/github/auth"]);
    expect(calls.slice(1).map((c) => c.init.body)).toEqual(['{"source":"device"}', '{"source":"machine","machine":"mbp"}']);
  });
});

describe("会话页", () => {
  const msg = (id: string, extra: Partial<ThreadMessage> = {}): ThreadMessage => ({ id, at: "2026-11-01T09:00:00Z", sender: { id: "u1", name: "示例" }, ...extra });
  const page = (ids: string[], before?: string): ThreadPage => ({ chat: { id: "c", name: "c", kind: "dm" }, messages: ids.map((i) => msg(i)), before });

  it("puts an older page before, one copy per id", () => {
    const merged = mergeOlder(page(["3", "4"], "3"), page(["1", "2", "3"], "1"));
    expect(merged.messages.map((m) => m.id)).toEqual(["1", "2", "3", "4"]);
    expect(merged.before).toBe("1");
  });

  it("puts the newest page in place of the newest messages, keeping the older ones", () => {
    const have = page(["1", "2", "3", "4"], "1");
    expect(mergeNewest(have, page(["3", "4", "5"], "3")).messages.map((m) => m.id)).toEqual(["1", "2", "3", "4", "5"]);
    expect(mergeNewest(have, page(["3", "4", "5"], "3")).before).toBe("1");
    expect(mergeNewest(have, page(["9"])).messages.map((m) => m.id)).toEqual(["9"]); // nothing in common: the new page
    expect(mergeNewest(null, page(["1"])).messages.length).toBe(1);
  });

  it("groups a sender's messages within five minutes", () => {
    const a = msg("1");
    expect(startsGroup(msg("2", { at: "2026-11-01T09:04:00Z" }), a)).toBe(false);
    expect(startsGroup(msg("2", { at: "2026-11-01T09:06:00Z" }), a)).toBe(true);
    expect(startsGroup(msg("2", { sender: { name: "我" }, mine: true }), a)).toBe(true);
    expect(startsGroup(msg("2", { reply_to: { id: "1" } }), a)).toBe(true);
    expect(startsGroup(a)).toBe(true);
  });

  it("describes media and label colors", () => {
    expect(mediaLine({ kind: "voice", duration: 72 })).toBe("语音 1:12");
    expect(mediaLine({ kind: "file", name: "示例.pdf", size: 284000 })).toBe("示例.pdf · 277 KB");
    expect(mediaLine({ kind: "file", size: 3 << 20 })).toBe("文件 · 3.0 MB");
    expect(mediaLine({ kind: "file", size: 1048000 })).toBe("文件 · 1023 KB");
    expect(mediaLine({ kind: "file", size: 1048500 })).toBe("文件 · 1.0 MB");
    expect(mediaLine({ kind: "file", size: 1048575 })).toBe("文件 · 1.0 MB");
    expect(labelColor("a2eeef")).toBe("#a2eeef");
    expect(labelColor("red")).toBeUndefined();
  });

  it("says a group's or channel's size when the hub gives it, nothing for a direct chat", () => {
    expect(chatHeadline({ id: "c", name: "示例群", kind: "group", members: 1234 })).toBe("群聊 · 1,234 位成员");
    expect(chatHeadline({ id: "c", name: "示例群", kind: "group" })).toBe("群聊");
    expect(chatHeadline({ id: "c", name: "示例频道", kind: "channel", members: 12 })).toBe("频道 · 12 位订阅者");
    expect(chatHeadline({ id: "u", name: "示例用户", kind: "dm", members: 3 })).toBeUndefined();
    expect(chatHeadline(undefined)).toBeUndefined();
  });

  it("reads back a few pages at most for a quote's message", async () => {
    const pages: Record<string, ThreadPage> = { "5": page(["3", "4"], "3"), "3": page(["1", "2"], "1"), "1": page(["0"]) };
    const older = vi.fn((before: string) => Promise.resolve(pages[before]));
    const found = await reachMessage(page(["5", "6"], "5"), "1", older);
    expect(found.found).toBe(true);
    expect(found.page.messages.map((m) => m.id)).toEqual(["1", "2", "3", "4", "5", "6"]);
    expect(older).toHaveBeenCalledTimes(2);
    older.mockClear();
    expect((await reachMessage(page(["5", "6"], "5"), "x", older, 2)).found).toBe(false);
    expect(older).toHaveBeenCalledTimes(2); // gives up after its pages
    expect((await reachMessage(page(["5"]), "x", older)).found).toBe(false); // the thread's start: nothing to read
  });
});

describe("信息 (iMessage)", () => {
  it("says what 交给 Persona does on and off", () => {
    expect(imessageAIFooter(true)).toContain("关闭后立即停止同步");
    expect(imessageAIFooter(false)).toContain("hub 上此前已保存的数据仍会保留");
  });

  it("puts the switch and deletes the data on the hub's iMessage API", async () => {
    const calls: [string, string, string | null][] = [];
    vi.stubGlobal("localStorage", { getItem: () => "jd_test" });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push([String(url), init.method ?? "GET", (init.body as string) ?? null]);
      const body = init.method === "DELETE" ? { chats: 2, people: 1, inbox: 3 } : { machine: "mac", status: "ok", ai: false };
      return new Response(JSON.stringify(body), { status: 200 });
    });
    expect((await setIMessageAI(false)).ai).toBe(false);
    expect(await deleteIMessageData()).toEqual({ chats: 2, people: 1, inbox: 3 });
    expect(calls.map(([u, m, b]) => [u.replace(/^.*\/api\/v1/, ""), m, b])).toEqual([
      ["/imessage/settings", "PUT", '{"ai":false}'],
      ["/imessage/data", "DELETE", null],
    ]);
  });
});
