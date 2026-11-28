// An Inbox item's knowledge base chat (#15356, docs/assistant/knowledge.md "A chat's Inbox items"): the item's own read
// (GET /inbox/{id}) names it as kb_chat, and "Knowledge page" opens that chat's page through the shared route table
// (lib/deep-links.ts). Asked once per item a page shows; an item with no chat, or of another assistant than the
// knowledge pages read, shows nothing.
import { useEffect, useState } from "react";
import { api } from "../../lib/api";
import { pagePath } from "../../lib/deep-links";
import { itemKey, itemPath, itemRequest, type InboxItem } from "../../lib/inbox";
import { sameManager, shownManager } from "../../lib/manager-choice";
import { navigate } from "../../lib/router";
import { Button } from "../../ui/atoms";
import { t } from "../../i18n";

/** Each item's chat as its read said, by itemKey: its id, or null for none. A read that failed is asked again. */
const chats = new Map<string, string | null>();

/** Tests: forget what the reads said. */
export function resetKBChatsForTests() {
  chats.clear();
}

/** The id of the knowledge base chat item came from, once its read says; null for none (or not said yet). Only items
 *  of a chat or a mail correspondent can have one. */
export function useKBChat(item: InboxItem): string | null {
  const key = itemKey(item);
  const asks = Boolean(item.lark || item.mail || item.connector);
  const [chat, setChat] = useState(() => chats.get(key) ?? null);
  useEffect(() => {
    if (!asks) return;
    if (chats.has(key)) {
      setChat(chats.get(key) ?? null);
      return;
    }
    let live = true;
    api<InboxItem>(itemPath(item.id), { headers: itemRequest(item) })
      .then((r) => {
        const id = r?.kb_chat?.id || null;
        chats.set(key, id);
        if (live) setChat(id);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the item is its key; its other fields change with every list
  }, [key, asks]);
  return asks ? chat : null;
}

/** "Knowledge page": the item's chat's page in the knowledge base, which is the current assistant's. */
export function KBChatButton({ item }: { item: InboxItem }) {
  const chat = useKBChat(item);
  if (!chat || !sameManager(item.manager, shownManager())) return null;
  const path = pagePath({ page: "knowledge-entry", kind: "chats", id: chat });
  return (
    <Button tone="secondary" icon="book" testId="inbox-kb-chat" onClick={() => navigate(path)}>
      {t("inbox-inboxdetail.open_kb_chat")}
    </Button>
  );
}
