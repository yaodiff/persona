import { expect, it } from "vitest";
import en from "./locales/en/inbox-inboxdetail.json";
import zh from "./locales/zh-Hans/inbox-inboxdetail.json";
import ja from "./locales/ja/inbox-inboxdetail.json";

it("pins the original-source action labels in every locale", () => {
  expect(en.view_source).toBe("View original");
  expect(en.hide_source).toBe("Hide original");
  expect(en.view_event).toBe("Handling");

  expect(zh.view_source).toBe("看原文");
  expect(zh.hide_source).toBe("收起原文");
  expect(zh.view_event).toBe("看处理");

  expect(ja.view_source).toBe("原文");
  expect(ja.hide_source).toBe("原文を閉じる");
  expect(ja.view_event).toBe("処理");
});
