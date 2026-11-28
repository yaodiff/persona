// @vitest-environment jsdom
// The "Sign in" group of an extension's detail (#4507): its state, "Sign in" / "Sign in again", and "Sign out" only when signed in.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ExtensionAuth } from "../../lib/extension-auth";
import { AuthGroup } from "./ExtensionAuth";

const draw = (auth: ExtensionAuth, enabled = true) => renderToStaticMarkup(<AuthGroup id="cis" auth={auth} enabled={enabled} onChange={() => {}} />);

describe("AuthGroup", () => {
  it("offers signing in when not signed in", () => {
    const html = draw({ title: "飞书登录", description: "用飞书确认身份后才能查", state: "signed_out" });
    expect(html).toContain("登录");
    expect(html).toContain("飞书登录");
    expect(html).toContain("未登录");
    expect(html).toContain("用飞书确认身份后才能查");
    expect(html).not.toContain("退出登录");
  });

  it("offers signing in again and out when signed in", () => {
    const html = draw({ title: "飞书登录", state: "signed_in", account: "me@example.com" });
    expect(html).toContain("已登录：me@example.com");
    expect(html).toContain("重新登录");
    expect(html).toContain("退出登录");
  });

  it("says a switched-off extension has to be on first", () => {
    const html = draw({ title: "飞书登录", state: "unknown" }, false);
    expect(html).toContain("扩展未运行");
    expect(html).toContain("启用扩展后才能登录。");
    expect(html).toContain("disabled");
  });
});
