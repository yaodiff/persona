import { t } from "../../i18n";

// The words every page answers to in any UI language (the Mac's quick open matches English and Chinese alike);
// the catalog's words_* add the UI language's own synonyms. Search data, never shown: i18n-ignore.
const PLACE_WORDS: Record<string, string> = {
  persona: "persona assistant persona main 助手 主对话", // i18n-ignore
  history: "history chats conversations 对话 历史", // i18n-ignore
  home: "home overview 总览 首页", // i18n-ignore
  inbox: "inbox mail lark feishu 邮件 飞书 收件箱", // i18n-ignore
  health: "health 健康", // i18n-ignore
  messages: "messages lark feishu chats 飞书 消息 聊天", // i18n-ignore
  "workspace-board": "board kanban workflow 看板 工作流", // i18n-ignore
  todos: "todo todos tasks 待办 任务", // i18n-ignore
  routines: "routines schedule cron followup 定时 跟进", // i18n-ignore
  kb: "knowledge kb people projects docs 知识库 项目 文档 会议", // i18n-ignore
  machines: "machines monitor computers cpu memory 电脑 监控 内存", // i18n-ignore
  terminal: "terminal shell 终端", // i18n-ignore
  terminals: "terminals terminal shell 终端", // i18n-ignore
  settings: "settings preferences 设置 偏好", // i18n-ignore
};

export function placeWords(key: string): string {
  const own: Record<string, string> = {
    persona: t("palette.words_persona"),
    history: t("palette.words_history"),
    home: t("palette.words_home"),
    inbox: t("palette.words_inbox"),
    health: t("palette.words_health"),
    messages: t("palette.words_messages"),
    "workspace-board": t("palette.words_board"),
    todos: t("palette.words_todos"),
    routines: t("palette.words_routines"),
    kb: t("palette.words_kb"),
    machines: t("palette.words_machines"),
    terminal: t("palette.words_terminal"),
    terminals: t("palette.words_terminal"),
    settings: t("palette.words_settings"),
  };
  return [PLACE_WORDS[key], own[key]].filter(Boolean).join(" ");
}
