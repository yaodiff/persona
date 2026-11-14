// "Trail" (#2128, docs/assistant/inbox.md "Relationships: parents and children"): under an item's summary, what it grew out of (its parent's chain, up to a todo or
// an agent session) and what grew out of it (the items right under it). Each row opens where it lives. Nothing is
// drawn when the item has neither. A pair whose lower end is an Inbox item can be unlinked; another open item can become this item's parent.
import { useEffect, useState } from "react";
import { fetchInboxTree, itemRoute, parentRoute, type InboxItem, type InboxParent, type InboxTree } from "../../lib/inbox";
import { openFromInbox } from "../../lib/inbox-nav";
import type { Route } from "../../lib/router";
import { sessionTitle } from "../../lib/format";
import { sameManager } from "../../lib/manager-choice";
import { useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import type { Todo } from "../../lib/todos";
import { t } from "../../i18n";
import { Button, cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { lazyDialog } from "../LazyOverlay";
import { detachInboxParent, onInboxParentChanged, type InboxItemRef } from "./InboxParent";

const SameMatterDialog = lazyDialog(() => import("./InboxSameMatter").then((m) => m.InboxSameMatter));

export type LineTarget = InboxItemRef;
export type LineRow = { key: string; label: string; title: string; route?: Route; up: boolean; detach?: LineTarget };
export type LineLocal = {
  todos?: readonly Todo[];
  session?: (mid: string, sid: string) => Session | undefined;
};

const kindLabel: Record<InboxParent["kind"], string> = { inbox: t("inbox-inboxline.kind_inbox"), todo: t("inbox-inboxline.kind_todo"), session: t("inbox-inboxline.kind_session") };

/** The rows of a line: the ancestors farthest first (the chain reads top down), then the children oldest first.
 *  `manager`: whose line it is (its items are that manager's: docs/assistant/managers.md "Clients").
 *  Local lists name a non-item ancestor; without a name only its kind is shown, never its raw id. */
export function lineRows(tree: InboxTree, manager?: string, local: LineLocal = {}): LineRow[] {
  // A partial 200 answer is an empty line, as an older hub's refused route is; never throw the item page away.
  const ancestors = tree.ancestors ?? [];
  const children = tree.children ?? [];
  const localTitle = (a: InboxParent): string | undefined => {
    if (a.kind === "todo") {
      const title = local.todos?.find((td) => td.id === a.id && sameManager(td.manager, manager))?.title.trim();
      return title || undefined;
    }
    if (a.kind !== "session") return undefined;
    const i = a.id.indexOf("/");
    if (i <= 0 || i === a.id.length - 1) return undefined;
    const session = local.session?.(a.id.slice(0, i), a.id.slice(i + 1));
    return session ? sessionTitle(session) : undefined;
  };
  const target = (node: InboxParent, item?: InboxItem): LineTarget | undefined =>
    node.kind === "inbox" && item ? { id: node.id, manager: item.manager ?? manager } : undefined;
  const ups = ancestors.map((a, i): LineRow => {
    // ancestors are nearest first: this row's pair ends at the ancestor just below it, or at this item for the nearest one.
    const lower = i === 0 ? tree.node : ancestors[i - 1];
    const lowerItem = i === 0 ? tree.item : ancestors[i - 1].item;
    return {
      key: `up:${a.kind}:${a.id}`,
      label: kindLabel[a.kind],
      title: a.item?.title || (a.kind === "inbox" ? t("inbox-inboxline.gone") : localTitle(a) ?? ""),
      route: a.kind === "inbox" && !a.item ? undefined : parentRoute(a, manager),
      up: true,
      detach: target(lower, lowerItem),
    };
  }).reverse();
  const downs = children.map((c): LineRow => ({
    key: `down:${c.id}`,
    label: t("inbox-inboxline.next"),
    title: c.title,
    route: itemRoute({ id: c.id, manager }),
    up: false,
    detach: { id: c.id, manager: c.manager ?? manager },
  }));
  return [...ups, ...downs];
}

export function LineList({
  rows,
  onNotSameMatter,
  notSamePending,
  onSameMatter,
}: {
  rows: LineRow[];
  onNotSameMatter?: (target: LineTarget) => void;
  notSamePending?: string;
  onSameMatter?: () => void;
}) {
  if (!rows.length) return null;
  return (
    <section className="mt-3" data-inbox-line>
      <p className="mb-1 text-[11.5px] font-medium text-ink-3">{t("inbox-inboxline.title")}</p>
      <ul className="flex flex-col gap-0.5">
        {rows.map((r) => {
          const target = r.detach;
          return (
            <li key={r.key} className="flex min-w-0 items-center gap-1">
              <button
                type="button"
                disabled={!r.route}
                onClick={() => r.route && openFromInbox(r.route)}
                className={cx(
                  "flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1 text-left text-[12.5px] whitespace-nowrap",
                  r.route ? "text-ink hover:bg-field" : "cursor-default text-ink-3",
                )}
              >
                <Icon name={r.up ? "up" : "chevron"} size={12} />
                <span className="shrink-0 text-ink-3">{r.label}</span>
                {r.title && <span className="truncate">{r.title}</span>}
              </button>
              {target && onNotSameMatter && (
                <Button
                  size="sm"
                  tone="quiet"
                  busy={notSamePending === target.id}
                  disabled={notSamePending !== undefined && notSamePending !== target.id}
                  testId={`not-same-${target.id}`}
                  onClick={() => onNotSameMatter(target)}
                >
                  {t("inbox-inboxline.not_same")}
                </Button>
              )}
            </li>
          );
        })}
      </ul>
      {onSameMatter && (
        <div className="mt-1 px-1.5">
          <Button size="sm" tone="quiet" icon="link" onClick={onSameMatter}>{t("inbox-inboxline.same")}</Button>
        </div>
      )}
    </section>
  );
}

export function InboxLine({ item }: { item: InboxItem }) {
  const [tree, setTree] = useState<InboxTree | null>(null);
  const [revision, setRevision] = useState(0);
  const [sameOpen, setSameOpen] = useState(false);
  const [pending, setPending] = useState<string>();
  const todos = useStore((s) => s.todos);
  const sessions = useStore((s) => s.sessions);
  const summaries = useStore((s) => s.sessionSummaries);
  const conversations = useStore((s) => s.assistant?.conversations);
  const parentKey = item.parent ? `${item.parent.kind}:${item.parent.id}` : "";
  useEffect(() => onInboxParentChanged(() => setRevision((n) => n + 1)), []);
  useEffect(() => {
    let live = true;
    setTree(null);
    setSameOpen(false);
    setPending(undefined);
    // the item's manager's line (its tree is in that manager's inbox)
    fetchInboxTree("inbox", item.id, item.manager)
      .then((fresh) => live && setTree(fresh))
      .catch(() => {}); // an older hub without the route: no line
    return () => {
      live = false;
    };
  }, [item.id, item.manager, parentKey, revision]);
  const rows = tree ? lineRows(tree, item.manager, {
    todos,
    session: (mid, sid) => sessions[mid]?.find((s) => s.session_id === sid)
      ?? summaries.find((s) => s.machine_id === mid && s.session_id === sid)
      ?? conversations?.find((s) => s.machine_id === mid && s.session_id === sid),
  }) : [];
  const detach = async (target: LineTarget) => {
    setPending(target.id);
    try {
      await detachInboxParent(target);
    } finally {
      setPending(undefined);
    }
  };
  return (
    <>
      <LineList rows={rows} onNotSameMatter={(target) => void detach(target)} notSamePending={pending} onSameMatter={() => setSameOpen(true)} />
      <SameMatterDialog
        open={sameOpen}
        item={item}
        tree={tree ?? undefined}
        onClose={() => setSameOpen(false)}
      />
    </>
  );
}
