import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { mergeMessages, type LarkMessage } from "./inbox";
import {
  attachmentRequest, FILE_LIMIT, fileOutcomeUnknown, fitPick, IMAGE_LIMIT, LARK_ATTACHMENT_REPLY_FEATURE, LarkReceiptMissing,
  MAX_PICK, routeAllowed, sendLarkAttachment, sendsAsImage, stageLarkAttachment, type LarkAttachment,
} from "./lark-attachments";

beforeEach(() => {
  URL.createObjectURL = vi.fn(() => "blob:test/1");
  URL.revokeObjectURL = vi.fn();
});

const file = (name: string, type: string, body = "abc") => new File([body], name, { type });
const sized = (f: File, size: number) => (Object.defineProperty(f, "size", { value: size }), f);
const jpeg = (body = "jpeg") => async () => new Blob([body], { type: "image/jpeg" });
const att = (kind: "image" | "file", body = "hi"): LarkAttachment => ({
  id: "a", kind, name: kind === "image" ? "照片.jpg" : "报告.pdf", mime: kind === "image" ? "image/jpeg" : "application/pdf",
  data: new Blob([body]), size: body.length,
});

describe("挑选附件", () => {
  it("静态图片按图片发，动图和矢量图按文件发", () => {
    expect(sendsAsImage("image/jpeg")).toBe(true);
    expect(sendsAsImage("image/png")).toBe(true);
    expect(sendsAsImage("image/webp")).toBe(true);
    expect(sendsAsImage("image/gif")).toBe(false);
    expect(sendsAsImage("image/svg+xml")).toBe(false);
    expect(sendsAsImage("application/pdf")).toBe(false);
  });

  it("图片转成 JPEG、改扩展名，有预览", async () => {
    const a = await stageLarkAttachment(file("截图.png", "image/png"), jpeg());
    expect(a).toMatchObject({ kind: "image", name: "截图.jpg", mime: "image/jpeg", size: 4, url: "blob:test/1" });
  });

  it("浏览器画不出来的图片按文件发", async () => {
    const a = await stageLarkAttachment(file("照片.heic", "image/heic"), async () => null);
    expect(a).toMatchObject({ kind: "file", name: "照片.heic", mime: "image/heic" });
    expect(a.url).toBeUndefined();
  });

  it("空文件、超过 100 MiB 的文件、转完还太大的图片都不收", async () => {
    await expect(stageLarkAttachment(file("空.txt", "text/plain", ""))).rejects.toThrow("空文件");
    await expect(stageLarkAttachment(sized(file("大.zip", "application/zip"), FILE_LIMIT + 1))).rejects.toThrow("100 MiB");
    await expect(stageLarkAttachment(sized(file("刚好.zip", "application/zip"), FILE_LIMIT))).resolves.toMatchObject({ kind: "file" });
    await expect(stageLarkAttachment(file("大图.png", "image/png"), jpeg("x".repeat(IMAGE_LIMIT + 1)))).rejects.toThrow("体积过大");
  });

  it("每次最多 4 个，多出的报个数", () => {
    expect(fitPick(0, [1, 2, 3, 4, 5])).toEqual({ take: [1, 2, 3, 4], dropped: 1 });
    expect(fitPick(3, [1, 2])).toEqual({ take: [1], dropped: 1 });
    expect(fitPick(MAX_PICK, [1])).toEqual({ take: [], dropped: 1 });
  });
});

describe("引用和话题", () => {
  it("引用一条消息发附件要 hub 的扩展支持", () => {
    expect(routeAllowed({}, [])).toBe(true);
    expect(routeAllowed({ replyTo: "m1" }, ["lark_attachments"])).toBe(false);
    expect(routeAllowed({ replyTo: "m1", thread: true }, [LARK_ATTACHMENT_REPLY_FEATURE])).toBe(true);
  });

  it("图片：JSON 带 base64、引用和话题，账号在查询里", async () => {
    const r = await attachmentRequest("oc_1", att("image", "hi"), "c1", { replyTo: "m1", thread: true, threadRoot: "m1" }, 2);
    expect(r.path).toBe("/lark/conversations/oc_1/images?ol_account=2");
    expect(r.init.json).toEqual({ client_id: "c1", name: "照片.jpg", mime: "image/jpeg", data: btoa("hi"), reply_to: "m1", thread: true });
    const plain = await attachmentRequest("oc_1", att("image"), "c2", {});
    expect(plain.init.json).not.toHaveProperty("reply_to");
    expect(plain.init.json).not.toHaveProperty("thread");
  });

  it("文件：原始字节，引用和话题根在查询里", async () => {
    const a = att("file");
    const r = await attachmentRequest("oc_1", a, "c1", { replyTo: "m2", thread: true, threadRoot: "root" });
    const q = new URL(r.path, "http://hub").searchParams;
    expect(Object.fromEntries(q)).toEqual({ client_id: "c1", name: "报告.pdf", mime: "application/pdf", reply_to: "m2", thread_root: "root" });
    expect(r.init.body).toBe(a.data);
    const quote = new URL((await attachmentRequest("oc_1", a, "c1", { replyTo: "m2" })).path, "http://hub").searchParams;
    expect(quote.has("thread_root")).toBe(false);
  });
});

describe("发送结果", () => {
  const answer = (body: unknown, status = 200) => vi.fn(async () => new Response(JSON.stringify(body), { status }));
  beforeEach(() => vi.stubGlobal("localStorage", { getItem: () => "jd_test" }));
  afterEach(() => vi.unstubAllGlobals());

  it("图片要拿到 message_id", async () => {
    vi.stubGlobal("fetch", answer({ message_id: "om_1" }));
    await expect(sendLarkAttachment("oc_1", att("image"), "c1")).resolves.toEqual({ message_id: "om_1" });
    vi.stubGlobal("fetch", answer({ message_id: "" }));
    await expect(sendLarkAttachment("oc_1", att("image"), "c1")).rejects.toBeInstanceOf(LarkReceiptMissing);
  });

  it("文件没有 message_id 也算发出了；回执读不懂算结果未知", async () => {
    vi.stubGlobal("fetch", answer({ message_id: "" }));
    await expect(sendLarkAttachment("oc_1", att("file"), "c1")).resolves.toEqual({ message_id: undefined });
    vi.stubGlobal("fetch", answer({ message_id: "local:1" }));
    await expect(sendLarkAttachment("oc_1", att("file"), "c1")).resolves.toEqual({ message_id: undefined });
    vi.stubGlobal("fetch", answer([]));
    await expect(sendLarkAttachment("oc_1", att("file"), "c1")).rejects.toBeInstanceOf(LarkReceiptMissing);
  });

  it("明确拒绝的文件可以重发，超时、服务端错误、断线的不自动重发", () => {
    expect(fileOutcomeUnknown(new ApiError(429, "too many", {}))).toBe(false);
    expect(fileOutcomeUnknown(new ApiError(400, "bad", {}))).toBe(false);
    expect(fileOutcomeUnknown(new ApiError(502, "upstream", {}))).toBe(true);
    expect(fileOutcomeUnknown(new ApiError(408, "timeout", {}))).toBe(true);
    expect(fileOutcomeUnknown(new TypeError("Failed to fetch"))).toBe(true);
    expect(fileOutcomeUnknown(new LarkReceiptMissing())).toBe(true);
  });
});

describe("回显", () => {
  it("hub 带回同一个 client_id 的消息替换本地那条", () => {
    const local: LarkMessage = { id: "temp-c1", client_id: "c1", position: "11", sending: true };
    const other: LarkMessage = { id: "temp-c2", client_id: "c2", position: "12", sending: true };
    const out = mergeMessages([{ id: "om_0", position: "10" }, local, other], [{ id: "om_1", client_id: "c1", position: "11" }]);
    expect(out.map((m) => m.id)).toEqual(["om_0", "om_1", "temp-c2"]);
  });
});
