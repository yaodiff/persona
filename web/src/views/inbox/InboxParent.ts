// Changing the two ends of a Trail relationship. The hub takes the parent object itself: null removes it.
import { api, ApiError, errorText } from "../../lib/api";
import { itemPath, itemRequest, replaceItem, type InboxItem, type InboxParent } from "../../lib/inbox";
import { t } from "../../i18n";
import { toast } from "../../ui/atoms";

export type InboxItemRef = Pick<InboxItem, "id" | "manager">;

const changedListeners = new Set<() => void>();
/** Every visible Trail rereads after a relationship changes, including one changed from the detail's More menu. */
export function onInboxParentChanged(listener: () => void): () => void {
  changedListeners.add(listener);
  return () => {
    changedListeners.delete(listener);
  };
}

/** The one request-body shape used by both Trail actions (docs/assistant/inbox.md "Relationships: parents and children"). */
export async function setInboxParent(item: InboxItemRef, parent: InboxParent | null): Promise<void> {
  const fresh = await api<InboxItem>(`${itemPath(item.id)}/parent`, { method: "PUT", json: parent, headers: itemRequest(item) });
  if (fresh?.id === item.id) replaceItem(fresh, item.manager);
  changedListeners.forEach((listener) => listener());
}

export async function detachInboxParent(item: InboxItemRef): Promise<boolean> {
  try {
    await setInboxParent(item, null);
    toast(t("inbox-inboxline.not_same_done"), "ok");
    return true;
  } catch (e) {
    toast(errorText(e), "error");
    return false;
  }
}

export async function linkInboxParent(item: InboxItemRef, parent: Pick<InboxItem, "id" | "title">): Promise<boolean> {
  try {
    await setInboxParent(item, { kind: "inbox", id: parent.id });
    toast(t("inbox-inboxline.linked", { title: parent.title }), "ok");
    return true;
  } catch (e) {
    const conflict = e instanceof ApiError && e.status === 409;
    toast(conflict ? t("inbox-inboxline.link_conflict") : errorText(e), "error");
    return false;
  }
}
