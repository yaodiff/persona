import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ConnectorPrompt, HubConnector } from "../../lib/connectors";
import { ExtensionConnectorGroup, IMessageGroups, SignInStep } from "./HubConnector";

// Settings › Connectors' login card (#1693): a QR code step is drawn and a step the page can't draw says so, never an empty
// form whose "Next" stays grey forever.
const draw = (prompt: ConnectorPrompt, isChannel = false, integrationsV2 = false) =>
  renderToStaticMarkup(
    <SignInStep
      name="微信"
      prompt={prompt}
      values={{}}
      busy={false}
      isChannel={isChannel}
      integrationsV2={integrationsV2}
      onChange={() => {}}
      onSubmit={() => {}}
      onRestart={() => {}}
      onCancel={() => {}}
    />,
  );

describe("a sign-in step", () => {
  it("draws the hub's QR code with 保存二维码, and no 下一步", () => {
    const html = draw({ step: "qr", kind: "qr", url: "https://example.com/q", interval: 2, hint: "用微信扫一扫", qr: { size: 3, rows: ["101", "010", "101"] } });
    expect(html).toContain('role="img"');
    expect(html).toContain('aria-label="登录二维码"');
    expect(html).toContain("M2 2h1v1h-1z"); // the top-left module, past the quiet zone
    expect(html).toContain("保存二维码");
    expect(html).toContain("正在等待你在 微信 中扫码确认");
    expect(html).toContain("用微信扫一扫");
    expect(html).not.toContain("下一步");
  });

  it("says a QR step without its modules or a link can't be drawn, and what to do instead", () => {
    const html = draw({ step: "qr", kind: "qr", hint: "用微信扫一扫" }, true);
    expect(html).not.toContain('role="img"'); // no QR code
    expect(html).toContain("当前页面无法显示此步骤");
    expect(html).toContain("设置 › 渠道 › 微信");
    expect(html).toContain("重新开始");
    expect(html).not.toContain("下一步");
  });

  it("points to Integrations & Extensions when the unified list is enabled", () => {
    const html = draw({ step: "qr", kind: "qr" }, true, true);
    expect(html).toContain("设置 › 集成与扩展 › 微信");
    expect(html).not.toContain("设置 › 渠道");
  });

  it("offers the link a QR step encodes when only its modules are missing", () => {
    const html = draw({ step: "qr", kind: "qr", url: "https://example.com/q" });
    expect(html).toContain("https://example.com/q");
    expect(html).toContain("复制链接");
    expect(html).not.toContain("下一步");
  });

  it("says a kind it doesn't know can't be drawn, without pointing at 渠道 for a connector that isn't one", () => {
    const html = draw({ step: "auth", kind: "open_url", url: "https://example.com/auth" });
    expect(html).toContain("当前页面无法显示此步骤");
    expect(html).not.toContain("渠道");
    expect(html).not.toContain("下一步");
  });

  it("still draws a form with its fields and 下一步", () => {
    const html = draw({ step: "credentials", fields: [{ name: "phone", label: "手机号" }] });
    expect(html).toContain("手机号");
    expect(html).toContain("下一步");
    expect(html).not.toContain("无法显示此步骤");
  });
});

// Settings › Connectors › Messages (#2898): "Hand to Persona" and deleting what the hub kept, as on iOS.
const imessage = (ai: boolean | undefined, extra: Partial<Parameters<typeof IMessageGroups>[0]> = {}) =>
  renderToStaticMarkup(
    <IMessageGroups ai={ai} busy="" deleted="" error="" confirm={false} onAI={() => {}} onDelete={() => {}} onCancel={() => {}} onConfirm={() => {}} {...extra} />,
  );

describe("信息's page", () => {
  it("draws 交给 Persona on, with delete held back until it is off", () => {
    const html = imessage(true);
    expect(html).toContain('role="switch" aria-checked="true" aria-label="交给 Persona"');
    expect(html).toMatch(/<button type="button" disabled="".*删除 hub 上已保存的信息/);
    expect(html).toContain("关闭后立即停止同步");
    expect(html).not.toContain("在手机 App");
  });

  it("lets the user delete once 交给 Persona is off, and says what went", () => {
    const html = imessage(false, { deleted: "已删除：知识库里 2 个会话、1 个人，收件箱里 3 条。" });
    expect(html).toContain('aria-checked="false"');
    expect(html).not.toMatch(/disabled="".{0,200}删除 hub 上已保存的信息/);
    expect(html).toContain("当前不交由 Persona 处理");
    expect(html).toContain("收件箱里 3 条");
  });

  it("shows only the read-only line on a hub without the switch", () => {
    const html = imessage(undefined);
    expect(html).not.toContain('role="switch"');
    expect(html).not.toContain("删除 hub 上已保存的信息");
    expect(html).toContain("Persona 仅拥有读取权限");
  });

  it("holds both while a change is saving, and shows its error", () => {
    const html = imessage(false, { busy: "ai", error: "没改成：no database" });
    expect(html).toMatch(/role="switch" aria-checked="false" aria-label="交给 Persona" disabled=""|disabled="".{0,80}role="switch"/);
    expect(html).toContain("没改成：no database");
  });
});

// Settings › Extensions › GitHub (persona#3178): the connector's own "Add to Inbox" switch, and the way to its page.
describe("an extension's connector group", () => {
  const github = (inbox: boolean) => ({ id: "github", name: "GitHub", inbox }) as HubConnector;
  const draw = (c: HubConnector, onOpen?: (id: string) => void) =>
    renderToStaticMarkup(<ExtensionConnectorGroup c={c} reload={async () => {}} onOpen={onOpen} />);

  it("draws the switch on, and the link to the connector's page", () => {
    const html = draw(github(true), () => {});
    expect(html).toContain("连接器 GitHub");
    expect(html).toContain("加入收件箱");
    expect(html).toContain('aria-checked="true"');
    expect(html).toContain("将 GitHub 的新条目收入收件箱");
    expect(html).toContain("连接器设置");
  });

  it("says what off means, and has no link without a way to open the page", () => {
    const html = draw(github(false));
    expect(html).toContain('aria-checked="false"');
    expect(html).toContain("不再接收新条目，已有条目仍会保留");
    expect(html).not.toContain("连接器设置");
  });
});
