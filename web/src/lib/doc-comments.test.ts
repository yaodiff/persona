import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { clientIdFor, findDocPeople, ON_DOCUMENT, parseThreads, readComments, replyText, sendComment, sendKey, setResolved, sortThreads, writeErrorText, type DocThread } from "./doc-comments";

const URL1 = "https://example.feishu.cn/docx/AbC123";

const reply = (id: string, minutesAgo: number, text = "好") => ({ id, author: { id: `u-${id}`, name: "示例甲" }, content: [{ kind: "text", text }], createdAt: new Date(Date.UTC(2026, 10, 5, 12) - minutesAgo * 60_000).toISOString() });
const thread = (id: string, resolved: boolean, minutesAgo: number): DocThread => ({ id, whole: false, resolved, quote: "一段", replies: parseThreads({ threads: [{ id, replies: [reply(`${id}r`, minutesAgo)] }] })[0].replies });

describe("parseThreads：插件给的线程", () => {
  it("读出引用、回复和 @ 片段，不认识的跳过", () => {
    const t = parseThreads({
      threads: [
        { id: "c1", whole: false, resolved: false, quote: "这一段", anchorBlockId: "b2", replies: [{ id: "r1", author: { name: "示例乙" }, content: [{ kind: "mention", text: "@示例甲" }, { kind: "text", text: " 看下" }] }, { nope: 1 }] },
        { whole: true },
        "x",
      ],
    });
    expect(t).toHaveLength(1);
    expect(t[0]).toMatchObject({ id: "c1", whole: false, resolved: false, quote: "这一段", anchorBlockId: "b2" });
    expect(t[0].replies).toHaveLength(1);
    expect(replyText(t[0].replies[0])).toBe("@示例甲 看下");
    expect(t[0].replies[0].content[0].kind).toBe("mention");
  });

  it("没有 threads 时是空的", () => {
    expect(parseThreads({})).toEqual([]);
    expect(parseThreads(null)).toEqual([]);
  });
});

describe("sortThreads", () => {
  it("未解决在前，各自按最近的回复排", () => {
    const out = sortThreads([thread("old", false, 300), thread("done", true, 1), thread("new", false, 5)]);
    expect(out.map((t) => t.id)).toEqual(["new", "old", "done"]);
  });
});

describe("client_id：重试不发两次", () => {
  it("同样的字发到同一处沿用同一个 id，换了字、换了地方、换了文档就是新的", () => {
    let n = 0;
    const fresh = () => `id${++n}`;
    const a = clientIdFor(null, sendKey(URL1, ON_DOCUMENT, "好"), fresh);
    expect(clientIdFor(a, sendKey(URL1, ON_DOCUMENT, "好"), fresh).id).toBe(a.id);
    expect(clientIdFor(a, sendKey(URL1, ON_DOCUMENT, "好的"), fresh).id).not.toBe(a.id);
    expect(clientIdFor(a, sendKey(URL1, { kind: "thread", id: "c1", name: "示例乙", text: "" }, "好"), fresh).id).not.toBe(a.id);
    expect(clientIdFor(a, sendKey("https://example.feishu.cn/docx/Other", ON_DOCUMENT, "好"), fresh).id).not.toBe(a.id);
  });
});

describe("writeErrorText", () => {
  it("用 hub 的原话；不支持的文档说清楚", () => {
    expect(writeErrorText(new ApiError(502, "飞书网关未登录飞书", { error: "飞书网关未登录飞书" }))).toBe("飞书网关未登录飞书");
    expect(writeErrorText(new ApiError(400, "x", { error: "x", code: "comment_unsupported" }))).toBe("此类文档暂不支持该评论方式");
    expect(writeErrorText(new TypeError("Failed to fetch"))).toBe("无法连接 hub");
  });
});

describe("请求", () => {
  const calls: { path: string; init: RequestInit }[] = [];
  const answer = (body: unknown, status = 200) => {
    vi.stubGlobal("localStorage", { getItem: () => "jd_test" });
    return vi.fn(async (path: string, init: RequestInit) => (calls.push({ path, init }), new Response(JSON.stringify(body), { status })));
  };
  afterEach(() => {
    calls.length = 0;
    vi.unstubAllGlobals();
  });
  const json = (i: number) => JSON.parse(String(calls[i].init.body));

  it("读评论走只读窗口", async () => {
    vi.stubGlobal("fetch", answer({ threads: [{ id: "c1", replies: [] }] }));
    const t = await readComments(URL1);
    expect(calls[0].path).toBe(`/api/v1/lark/raw/documents/comments?url=${encodeURIComponent(URL1)}`);
    expect(t.map((x) => x.id)).toEqual(["c1"]);
  });

  it("评论全文：内容、@ 的人、client_id，不带 quote", async () => {
    vi.stubGlobal("fetch", answer({ ok: true, threads: [] }));
    const t = await sendComment(URL1, ON_DOCUMENT, "@示例乙 看下", [{ user_id: "ou_b", name: "示例乙", avatar: "k" }], "id1");
    expect(calls[0].path).toBe("/api/v1/lark/documents/comments");
    expect(calls[0].init.method).toBe("POST");
    expect(new Headers(calls[0].init.headers).get("Authorization")).toBe("Bearer jd_test");
    expect(json(0)).toEqual({ url: URL1, content: "@示例乙 看下", mentions: [{ user_id: "ou_b", name: "示例乙" }], client_id: "id1" });
    expect(t).toEqual([]);
  });

  it("评论一段带 quote 和 block_id；回复走线程", async () => {
    vi.stubGlobal("fetch", answer({ ok: true, threads: null }));
    expect(await sendComment(URL1, { kind: "passage", quote: "这一段", blockId: "b2" }, "好", [], "id2")).toBeNull();
    expect(json(0)).toMatchObject({ quote: "这一段", block_id: "b2" });
    await sendComment(URL1, { kind: "passage", quote: "那一段" }, "好", [], "id3");
    expect(json(1)).not.toHaveProperty("block_id");
    await sendComment(URL1, { kind: "thread", id: "c/1", name: "示例乙", text: "" }, "好", [], "id4");
    expect(calls[2].path).toBe("/api/v1/lark/documents/comments/c%2F1/replies");
    expect(json(2)).not.toHaveProperty("quote");
  });

  it("解决 / 重新打开", async () => {
    vi.stubGlobal("fetch", answer({ ok: true, threads: [{ id: "c1", resolved: true, replies: [] }] }));
    const t = await setResolved(URL1, "c1", true);
    expect(calls[0].path).toBe("/api/v1/lark/documents/comments/c1/resolve");
    expect(json(0)).toEqual({ url: URL1, resolved: true });
    expect(t?.[0].resolved).toBe(true);
  });

  it("names the manager the reader reads the document as on every request, none for the default's", async () => {
    vi.stubGlobal("fetch", answer({ ok: true, threads: [] }));
    await readComments(URL1, "m-2");
    await sendComment(URL1, ON_DOCUMENT, "ok", [], "id6", "m-2");
    await sendComment(URL1, { kind: "thread", id: "c1", name: "Sample", text: "" }, "ok", [], "id7", "m-2");
    await setResolved(URL1, "c1", true, "m-2");
    await findDocPeople(URL1, "a", undefined, "m-2");
    await readComments(URL1, "assistant");
    await readComments(URL1);
    expect(calls.map((c) => new Headers(c.init.headers).get("X-Persona-Manager"))).toEqual(["m-2", "m-2", "m-2", "m-2", "m-2", null, null]);
  });

  it("写失败时抛出 hub 的话", async () => {
    vi.stubGlobal("fetch", answer({ error: "飞书网关未登录飞书" }, 502));
    await expect(sendComment(URL1, ON_DOCUMENT, "好", [], "id5")).rejects.toThrow("飞书网关未登录飞书");
  });
});
