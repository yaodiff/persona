import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { TriageBanner } from "./TriageBanner";

const draw = (triage: Parameters<typeof TriageBanner>[0]["triage"], canConfigure?: boolean) => renderToStaticMarkup(<TriageBanner triage={triage} canConfigure={canConfigure} />);

describe("等分拣的横幅", () => {
  it("没配模型：说明加「去配置」", () => {
    const html = draw({ state: "unconfigured", waiting: 3, sources: [{ source: "lark", name: "飞书", state: "unconfigured", waiting: 3 }] });
    expect(html).toContain('role="status"');
    expect(html).toContain("有 3 条飞书消息等待分拣：尚未配置分拣模型");
    expect(html).toContain("去配置");
    expect(html).toContain("bg-orange-tint");
  });
  it("别的状态只说原因；不能改设置的设备也只说原因", () => {
    expect(draw({ state: "behind", waiting: 12 })).not.toContain("去配置");
    const readOnly = draw({ state: "unconfigured", waiting: 3 }, false);
    expect(readOnly).toContain("有 3 条消息等待分拣：尚未配置分拣模型");
    expect(readOnly).not.toContain("去配置");
  });
  it("ok 和老 hub 什么都不画", () => {
    expect(draw({ state: "ok", waiting: 0 })).toBe("");
    expect(draw(null)).toBe("");
  });
});
