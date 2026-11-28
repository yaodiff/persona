import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { TriageRuleField } from "./TriageRuleField";

const data = vi.hoisted(() => ({ templates: [] as { extension: string; extension_name: string; id: string; title: string; text: string }[] }));
vi.mock("../lib/extension-declared", () => ({ useDeclaredList: () => data.templates }));

describe("triage rule field", () => {
  it("preserves a multiline draft and has no template button on older hubs", () => {
    data.templates = [];
    const html = renderToStaticMarkup(<TriageRuleField id="rules" value={"一行\n另一行"} invalid={false} onChange={() => { throw Error("render changed a draft"); }} />);
    expect(html).toContain("一行\n另一行");
    expect(html).not.toContain("使用模板");
  });
  it("offers a picker without replacing or saving the draft during rendering", () => {
    data.templates = [{ extension: "example-kit", extension_name: "入门套件", id: "mentions", title: "只看提到我的", text: "新规则" }];
    const html = renderToStaticMarkup(<TriageRuleField id="rules" value="原来的规则" invalid={false} onChange={() => { throw Error("render changed a draft"); }} />);
    expect(html).toContain("使用模板");
    expect(html).toContain("原来的规则");
    expect(html).not.toContain("替换草稿");
  });
});
