import { t } from "../i18n";
import type { MenuItem } from "../ui/menu-model";

// DESIGN_SYSTEM §3.4: both Web entry points lay out the actions they actually offer.
// Keep Web-specific session actions in their original order within their section.
export function groupAgentMenu(items: MenuItem[], paneItems: MenuItem[] = []): MenuItem[] {
  const remaining = new Map(items.map((item) => [item.key, item]));
  const take = (...keys: string[]) => keys.flatMap((key) => {
    const item = remaining.get(key);
    remaining.delete(key);
    return item ? [item] : [];
  });

  const top = take("back-to-chat", "stop", "seen", "rename", "pin");
  const last = take(remaining.has("close") ? "close" : "delete");
  const navigation = take("files", "git", "review", "preview", "sides", "space-agents", "attach", "takeover", "takeover-continue");
  const session = take("mute", "todo-detail", "link-todo", "relink-todo", "unlink-todo", "persona");
  const utilities = take("sid", "cwd", "terminal", "resume", "resume-terminal");
  const deletion = take("delete");

  // Node actions own their keys. Place their copy commands alongside the chat's equivalents.
  const pane = paneItems.map((item) => ({ ...item, key: `pane:${item.key}` }));
  const nodeCopies = pane.filter((item) => item.key.startsWith("pane:node-copy-"));
  const nodeActions = pane.filter((item) => !item.key.startsWith("pane:node-copy-"));
  const groups = [navigation, [...session, ...remaining.values()], [...nodeCopies, ...utilities], nodeActions, deletion];

  const children = groups.filter((group) => group.length).flatMap((group, section) =>
    group.map((item, index) => ({ ...item, divider: index === 0 && section > 0 || item.divider })),
  );
  if (children.length) top.push({ key: "more", label: t("sidebar.more"), icon: "more", children, onSelect: () => {} });
  return [...top, ...last.map((item) => ({ ...item, divider: true }))];
}
