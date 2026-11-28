// A chat's Inbox items on its knowledge page (#15356, docs/assistant/knowledge.md "A chat's Inbox items"): "In Inbox",
// the open ones with their tier, the done ones folded under "N done", each opening its item; and the link a row of the
// chat's recent messages gets when an item holds it. Links go through the shared route table (lib/deep-links.ts).
import { useState } from "react";
import { BASE } from "../../lib/base";
import { pagePath } from "../../lib/deep-links";
import { shortTime } from "../../lib/format";
import type { KBInboxItem } from "../../lib/kb";
import { otherManager } from "../../lib/manager-choice";
import { navigate } from "../../lib/router";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import { Card, MoreRow, onPlainClick, SectionTitle, TonePill } from "./KBParts";

/** The page of an item of manager's Inbox (the knowledge base's assistant), as the route table writes it. */
export function inboxItemPath(id: string, manager?: string): string {
  return pagePath({ page: "inbox-item", id, manager: otherManager(manager) });
}

/** Where an item stands, as its row says it: its section while open (as the Inbox sorts it), else its tier; "" for an
 *  item not sorted yet. */
export function inboxTier(item: Pick<KBInboxItem, "tier" | "status" | "done_at">): "" | "action" | "working" | "fyi" | "noise" {
  const key = !item.done_at && item.status ? item.status : item.tier;
  if (key === "working" || key === "fyi" || key === "noise") return key;
  return key ? "action" : "";
}

const TIER_LABEL = { action: "lib-inbox.tier_action", working: "lib-inbox.tier_working", fyi: "lib-inbox.tier_fyi", noise: "lib-inbox.tier_noise" } as const;

/** "In Inbox": the chat's open items, then its done ones behind "N done". Nothing for a chat without items. */
export function ChatInbox({ items, manager }: { items: readonly KBInboxItem[]; manager?: string }) {
  const [doneShown, setDoneShown] = useState(false);
  if (!items.length) return null;
  const open = items.filter((x) => !x.done_at);
  const done = items.filter((x) => x.done_at);
  return (
    <section data-kb-inbox>
      <SectionTitle title={t("knowledge-kbinbox.title")} count={open.length || undefined} />
      <Card>
        {open.map((x) => <InboxRow key={x.id} item={x} manager={manager} />)}
        {doneShown && done.map((x) => <InboxRow key={x.id} item={x} manager={manager} />)}
        {done.length > 0 && <MoreRow open={doneShown} label={t("knowledge-kbinbox.done", { count: done.length })} onClick={() => setDoneShown((v) => !v)} />}
      </Card>
    </section>
  );
}

function InboxRow({ item, manager }: { item: KBInboxItem; manager?: string }) {
  const path = inboxItemPath(item.id, manager);
  const tier = inboxTier(item);
  const line = item.summary || item.preview;
  const time = shortTime(item.done_at || item.updated_at);
  return (
    <a href={BASE + path} onClick={(ev) => onPlainClick(ev, () => navigate(path))} data-inbox-item={item.id} data-done={item.done_at ? "" : undefined} className="flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-hover">
      <Icon name={item.done_at ? "check" : "inbox"} size={14} className="shrink-0 text-ink-3" />
      <span className="min-w-0 flex-1">
        <span className={item.done_at ? "block truncate text-meta text-ink-2" : "block truncate text-meta font-medium text-ink"}>{item.title}</span>
        {line && <span className="block truncate text-submeta text-ink-3">{line}</span>}
      </span>
      {tier && <TonePill tone={tier === "action" && !item.done_at ? "orange" : "neutral"}>{t(TIER_LABEL[tier])}</TonePill>}
      {time && <span className="shrink-0 whitespace-nowrap text-submeta text-ink-3 tabular-nums">{time}</span>}
      <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
    </a>
  );
}

/** A row of the chat's recent messages that an Inbox item holds: the row, opening that item. */
export function InboxMessageLink({ item, manager, children }: { item: KBInboxItem; manager?: string; children: React.ReactNode }) {
  const path = inboxItemPath(item.id, manager);
  return (
    <a
      href={BASE + path}
      onClick={(ev) => onPlainClick(ev, () => navigate(path))}
      data-inbox-item={item.id}
      title={t("knowledge-kbinbox.open_item", { title: item.title })}
      className="-mx-2 flex items-start gap-2 rounded-control px-2 py-1 transition-colors hover:bg-hover"
    >
      <div className="min-w-0 flex-1">{children}</div>
      <Icon name="inbox" size={13} className="mt-0.5 shrink-0 text-accent-ink" />
      <span className="sr-only">{t("knowledge-kbinbox.open_item", { title: item.title })}</span>
    </a>
  );
}
