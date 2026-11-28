import { expect, it } from "vitest";
import { initI18n, t } from "./index";

it.each([
  { lang: "en", chip: "In a tab", tab: "Tab", shell: "Shell", tabs: "Tabs", history: "History", archive: "Archived sessions", terminal: "Terminal" },
  { lang: "zh-Hans", chip: "在标签页中", tab: "标签页", shell: "Shell", tabs: "标签页", history: "历史记录", archive: "已归档会话", terminal: "终端" },
  { lang: "ja", chip: "タブで実行中", tab: "タブ", shell: "シェル", tabs: "タブ", history: "履歴", archive: "アーカイブ済みセッション", terminal: "ターミナル" },
])("distinguishes the tab, shell, session and terminal labels in $lang", async ({ lang, chip, tab, shell, tabs, history, archive, terminal }) => {
  await initI18n(lang);
  expect(t("lib-agent-filter.terminal")).toBe(chip);
  expect(t("sidebar-row-hover.tab")).toBe(tab);
  expect(t("lib-terminals.terminal")).toBe(shell);
  expect(t("terminal-terminalpage.terminals")).toBe(tabs);
  expect(t("session-history.title")).toBe(history);
  expect(t("agents-archives.title")).toBe(archive);
  expect(t("session-terminalpanel.title")).toBe(terminal);
});
