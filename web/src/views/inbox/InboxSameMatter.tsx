// "Same matter as…": pick another open item as this item's parent. Loaded on first open (lazyDialog in InboxLine).
import { useEffect, useMemo, useState } from "react";
import { fetchInboxTree, isDone, useInbox, type InboxItem, type InboxTree } from "../../lib/inbox";
import { rowMeta } from "../../lib/inbox-now";
import { sameManager } from "../../lib/manager-choice";
import { byTime } from "../../lib/times";
import { t } from "../../i18n";
import { Button, cx, Dialog, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { linkInboxParent } from "./InboxParent";

const NO_CHILDREN: InboxItem[] = [];
const MAX_PARENT_DEPTH = 50;

/** Whether following inbox parents from candidate reaches ancestor, with corrupt cycles bounded. */
function hasInboxAncestor(candidate: InboxItem, ancestorID: string, itemsByID: ReadonlyMap<string, InboxItem>): boolean {
  const seen = new Set<string>();
  let parent = candidate.parent;
  for (let depth = 0; depth < MAX_PARENT_DEPTH && parent?.kind === "inbox"; depth++) {
    if (parent.id === ancestorID) return true;
    if (seen.has(parent.id)) return false;
    seen.add(parent.id);
    parent = itemsByID.get(parent.id)?.parent;
  }
  return false;
}

/** Open items this one can join: not itself or anything under it, newest first, matching title or where it happened. */
export function sameMatterCandidates(items: readonly InboxItem[], item: InboxItem, children: readonly InboxItem[], query = ""): InboxItem[] {
  const excluded = new Set([item.id, ...children.map((c) => c.id)]);
  // A parent id is local to its item's manager; another manager may reuse the same id.
  const itemsByID = new Map<string, InboxItem>();
  for (const candidate of items) {
    if (sameManager(candidate.manager, item.manager)) itemsByID.set(candidate.id, candidate);
  }
  const q = query.trim().toLowerCase();
  return items
    .filter((candidate) => !isDone(candidate)
      && sameManager(candidate.manager, item.manager)
      && !excluded.has(candidate.id)
      && !hasInboxAncestor(candidate, item.id, itemsByID)
      && (!q || [candidate.title, (candidate as InboxItem & { where?: string }).where]
        .some((text) => text?.toLowerCase().includes(q))))
    .sort((a, b) => byTime(b.updated_at, a.updated_at) || a.id.localeCompare(b.id));
}

export function SameMatterList({ items, busy, onPick }: { items: readonly InboxItem[]; busy: boolean; onPick: (item: InboxItem) => void }) {
  return (
    <div className="mt-2 max-h-[320px] divide-y divide-line overflow-y-auto rounded-card bg-surface shadow-card">
      {items.map((candidate) => {
        const meta = rowMeta(candidate);
        return (
          <button
            key={candidate.id}
            type="button"
            disabled={busy}
            data-same-matter={candidate.id}
            onClick={() => onPick(candidate)}
            className={cx("flex w-full items-center gap-2.5 px-3 py-2 text-left transition-colors", busy ? "opacity-60" : "hover:bg-hover")}
          >
            <span className="min-w-0 flex-1">
              <span className="block truncate text-meta text-ink">{candidate.title}</span>
              {meta && <span className="block truncate text-label text-ink-3">{meta}</span>}
            </span>
            <Icon name="chevronRight" size={13} className="shrink-0 text-ink-3" />
          </button>
        );
      })}
    </div>
  );
}

export function InboxSameMatter({
  open,
  item,
  tree,
  onClose,
  onLinked,
}: {
  open: boolean;
  item: InboxItem;
  /** The line already on the page. Without it (the detail's More menu), the dialog reads it before offering choices. */
  tree?: InboxTree;
  onClose: () => void;
  onLinked?: () => void;
}) {
  const items = useInbox((s) => s.items);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [readTree, setReadTree] = useState<InboxTree | null>(null);
  const [reading, setReading] = useState(false);

  useEffect(() => {
    if (!open) return;
    setQuery("");
    setBusy(false);
  }, [open, item.id, item.manager]);

  useEffect(() => {
    if (!open) return;
    if (tree) {
      setReadTree(null);
      setReading(false);
      return;
    }
    let live = true;
    setReadTree(null);
    setReading(true);
    fetchInboxTree("inbox", item.id, item.manager)
      .then((fresh) => live && setReadTree(fresh))
      .catch(() => {
        // The write itself reports an older hub's error. Until then only the exclusions already known can apply.
        if (live) setReadTree({ node: { kind: "inbox", id: item.id }, item, ancestors: [], children: [] });
      })
      .finally(() => live && setReading(false));
    return () => {
      live = false;
    };
  }, [open, item.id, item.manager, tree]);

  const effectiveTree = tree ?? readTree;
  const children = effectiveTree?.children ?? NO_CHILDREN;
  const candidates = useMemo(() => sameMatterCandidates(items, item, children, query), [items, item, children, query]);
  const pick = async (choice: InboxItem) => {
    if (busy) return;
    setBusy(true);
    try {
      if (await linkInboxParent(item, choice)) {
        onClose();
        onLinked?.();
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("inbox-inboxline.same")}
      width={480}
      footer={<Button tone="quiet" onClick={onClose}>{t("common.cancel")}</Button>}
    >
      <div className="flex h-8 items-center gap-2 rounded-control bg-field px-3 shadow-inset-field focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
        <Icon name="search" size={14} className="shrink-0 text-ink-3" />
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("inbox-inboxline.search_placeholder")}
          aria-label={t("inbox-inboxline.search_placeholder")}
          autoComplete="off"
          className="min-w-0 flex-1 bg-transparent text-meta text-ink outline-none placeholder:text-ink-3"
        />
      </div>
      {reading || !effectiveTree ? (
        <div className="py-6"><Loading /></div>
      ) : candidates.length ? (
        <SameMatterList items={candidates} busy={busy} onPick={(choice) => void pick(choice)} />
      ) : (
        <p className="py-6 text-center text-meta text-ink-3">{t("inbox-inboxline.empty")}</p>
      )}
    </Dialog>
  );
}
