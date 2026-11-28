import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { AnsweredLine } from "./Tasks";

describe("AnsweredLine (#1231)", () => {
  const item = {
    id: "f1", conversation: "c1", at: new Date().toISOString(), kind: "answered" as const, ref: "todo_x",
    body: { question: "先补哪一块？", answer: "重试；假服务器", items: [{ question: "先补哪一块？", answer: "重试和超时" }, { question: "请求怎么处理？", answer: "假服务器" }] },
  };

  it("starts folded: the line and a disclosure, not the questions", () => {
    const html = renderToStaticMarkup(<AnsweredLine item={item} title="补测试" onOpenTask={() => {}} />);
    expect(html).toContain("补测试");
    expect(html).toContain("· 重试；假服务器");
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain("请求怎么处理？");
  });

  it("has nothing to open when a record is empty", () => {
    const html = renderToStaticMarkup(<AnsweredLine item={{ ...item, body: {} }} title="补测试" />);
    expect(html).toContain("· 已回复");
    expect(html).not.toContain("aria-expanded");
  });
});
