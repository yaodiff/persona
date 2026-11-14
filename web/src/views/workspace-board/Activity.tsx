import { useEffect, useId, useMemo, useState, useSyncExternalStore } from "react";
import { formatList, t } from "../../i18n";
import { credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { fromDefaultManager, useKnownDefault } from "../../lib/manager-choice";
import { relativeTime, exactTime } from "../../lib/relative-time";
import { onHubEvent } from "../../lib/store";
import { BoardActivityModel, type ActivityChange, type ActivitySummary, type DetailState } from "../../lib/workspace-board-activity";
import type { BoardCard } from "../../lib/workspace-board";
import { Button, Dialog, Loading, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ListError } from "../../ui/ListError";

const word = (key: string, values?: Parameters<typeof t>[1]) => t(`workspaceboard-activity.${key}`, values);
const kindKeys: Record<string, string> = {
  card_added: "card_added", card_removed: "card_removed", card_updated: "card_updated",
  column_added: "column_added", column_removed: "column_removed", column_renamed: "column_renamed",
  cards_reordered: "cards_reordered", columns_reordered: "columns_reordered",
};
const kindLabel = (kind: string) => word(Object.hasOwn(kindKeys, kind) ? kindKeys[kind] : "other_change");
const EMPTY_CARDS: readonly BoardCard[] = [];

/** The caller mounts this dialog only while open, so closing aborts pending reads immediately. */
export function ActivityDialog({ onClose, currentCards, onOpenCard }: {
  onClose: () => void;
  currentCards: readonly BoardCard[];
  onOpenCard: (id: string) => void;
}) {
  return <Dialog open title={word("title")} onClose={onClose} width={640}>
    <ActivityScope currentCards={currentCards} onOpenCard={onOpenCard} />
  </Dialog>;
}

/** Already inside the workspace's dialog: only the timeline, never a second modal. */
export function CardActivity({ cardID }: { cardID: string }) {
  return <ActivityScope cardID={cardID} />;
}

function ActivityScope({ cardID, currentCards = EMPTY_CARDS, onOpenCard }: {
  cardID?: string;
  currentCards?: readonly BoardCard[];
  onOpenCard?: (id: string) => void;
}) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const manager = useKnownDefault();
  if (!manager) return <Loading label={word("loading")} />;
  return <ActivityReader key={JSON.stringify([owner, manager, cardID])} cardID={cardID} currentCards={currentCards} onOpenCard={onOpenCard} />;
}

/** One mounted reader owns its requests and subscriptions. A push offers Refresh without moving the reading position. */
function ActivityReader({ cardID, currentCards, onOpenCard }: {
  cardID?: string;
  currentCards: readonly BoardCard[];
  onOpenCard?: (id: string) => void;
}) {
  const [model] = useState(() => new BoardActivityModel(cardID));
  const [started, setStarted] = useState(false);
  const state = useSyncExternalStore(model.subscribe, model.snapshot, model.snapshot);
  const [visible, setVisible] = useState(() => document.visibilityState !== "hidden");
  const now = useNow(60_000, visible);
  const cards = useMemo(() => new Map(currentCards.map((card) => [card.id, card])), [currentCards]);
  useEffect(() => {
    setStarted(true);
    void model.refresh();
    const off = onHubEvent("workspace_board", (event) => {
      if (fromDefaultManager(event) && (typeof event.revision === "number" || typeof event.revision === "string")) model.noteRevision(event.revision);
    });
    const visibility = () => setVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", visibility);
    return () => { off(); document.removeEventListener("visibilitychange", visibility); model.dispose(); };
  }, [model]);
  return <section aria-label={word(cardID ? "card_title" : "title")} className="min-w-0 space-y-3" data-board-activity>
    <div className="flex flex-wrap items-center gap-2">
      <p className="min-w-0 flex-1 text-sm text-ink-2">{word(cardID ? "card_intro" : "intro")}</p>
      <Button icon="retry" disabled={state.loading || state.loadingMore} onClick={() => void model.refresh()}>{word("refresh")}</Button>
    </div>
    {state.newer && <div role="status" className="flex flex-wrap items-center gap-2 rounded-control bg-accent-tint px-3 py-2">
      <p className="min-w-0 flex-1 text-sm text-accent-ink">{word("newer")}</p>
      <Button disabled={state.loading || state.loadingMore} onClick={() => void model.refresh()}>{word("show_latest")}</Button>
    </div>}
    {(!started || state.loading) && <Loading label={word("loading")} />}
    {state.error && <ListError text={state.error} onRetry={() => void model.refresh()} />}
    {started && !state.loading && !state.error && !state.entries.length && <p className="rounded-control bg-inset px-3 py-4 text-sm text-ink-2">{word("empty")}</p>}
    <ol className="min-w-0 divide-y divide-line">
      {state.entries.map((entry) => <ActivityEntry key={entry.revision} entry={entry} detail={state.details.get(entry.revision)} now={now} refreshing={state.loading} allChanges={Boolean(cardID)} cards={cards} onOpenCard={onOpenCard}
        onExpand={() => void model.detail(entry.revision)} onMore={() => void model.moreDetails(entry.revision)} />)}
    </ol>
    {state.moreError && <ListError text={state.moreError} onRetry={() => void model.more()} />}
    {state.nextCursor && <Button disabled={state.loading || state.loadingMore} busy={state.loadingMore} onClick={() => void model.more()}>{word("load_more")}</Button>}
    {state.retentionLimit > 0 && <p className="text-xs text-ink-3" data-activity-retention>{word("retention", { count: state.retentionLimit })}</p>}
  </section>;
}

function ActivityEntry({ entry, detail, now, refreshing, allChanges, cards, onOpenCard, onExpand, onMore }: {
  entry: ActivitySummary;
  detail?: DetailState;
  now: number;
  refreshing: boolean;
  allChanges: boolean;
  cards: ReadonlyMap<string, BoardCard>;
  onOpenCard?: (id: string) => void;
  onExpand: () => void;
  onMore: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const id = useId();
  // Refresh can cancel an in-flight detail read while keeping its group expanded. Resume that read once the new
  // summary page confirms the group still exists; a failure waits for explicit Retry.
  useEffect(() => {
    if (expanded && !refreshing && !detail?.loading && !detail?.loaded && !detail?.error) onExpand();
  }, [expanded, refreshing, detail, onExpand]);
  const kinds = formatList([...new Set(entry.kinds)].map(kindLabel));
  return <li className="min-w-0 py-3" data-activity-revision={entry.revision}>
    <button type="button" aria-expanded={expanded} aria-controls={id} className="flex min-h-11 w-full min-w-0 items-start gap-2 rounded-control py-2 text-left hover:bg-hover focus-visible:outline-accent" onClick={() => {
      if (!expanded) onExpand();
      setExpanded(!expanded);
    }}>
      <Icon name={expanded ? "chevron" : "chevronRight"} size={16} className="mt-0.5 shrink-0 text-ink-3" />
      <span className="min-w-0 flex-1 space-y-1">
        <span className="block break-words text-sm font-medium text-ink">{entry.card_count ? word("workspaces_changed", { count: entry.card_count }) : word("board_changed")}</span>
        <span className="block break-words text-xs text-ink-2">{kinds || word("other_change")}</span>
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-ink-3">
          <time dateTime={entry.at} title={exactTime(entry.at)} className="whitespace-nowrap">{relativeTime(entry.at, now)}</time>
          <span className="whitespace-nowrap">{word("revision", { revision: entry.revision })}</span>
          <span className="whitespace-nowrap">{word("change_count", { count: entry.change_count })}</span>
        </span>
      </span>
    </button>
    {expanded && <div id={id} className="min-w-0 space-y-3 border-l border-line pl-3">
      {allChanges && <p className="text-xs font-medium text-ink-2">{word("all_changes")}</p>}
      {detail?.loading && <Loading label={word("loading_details")} />}
      {detail?.error && <ListError text={detail.error} onRetry={detail.changes.length ? onMore : onExpand} />}
      {detail && <ol className="min-w-0 space-y-3">{detail.changes.map((change, index) => <ChangeRow key={`${change.kind}:${change.id ?? ""}:${index}`} change={change} cards={cards} onOpenCard={onOpenCard} />)}</ol>}
      {detail?.nextOffset !== undefined && <Button disabled={detail.loading} busy={detail.loading} onClick={onMore}>{word("more_details", { count: Math.max(0, detail.total - detail.changes.length) })}</Button>}
    </div>}
  </li>;
}

type Field = "title" | "column" | "pinned" | "archived";
const fields: readonly Field[] = ["title", "column", "pinned", "archived"];
function ChangeRow({ change, cards, onOpenCard }: { change: ActivityChange; cards: ReadonlyMap<string, BoardCard>; onOpenCard?: (id: string) => void }) {
  const card = change.id && change.kind.startsWith("card_") ? cards.get(change.id) : undefined;
  const title = change.title || change.after?.title || change.before?.title;
  const shown = fields.filter((field) => change.fields?.includes(field) || (!change.fields?.length && (field === "title" ? !!(change.before?.title || change.after?.title) : field === "column" ? !!(change.before?.column_title || change.after?.column_title) : change.before?.[field] !== undefined || change.after?.[field] !== undefined)));
  const value = (side: "before" | "after", field: Field): string => {
    const data = change[side];
    if (!data) return word("absent");
    if (field === "column") return data.column_title || word("unknown_column");
    if (field === "pinned") return data.pinned === undefined ? word("absent") : word(data.pinned ? "pinned" : "not_pinned");
    if (field === "archived") return data.archived === undefined ? word("absent") : word(data.archived ? "archived" : "on_board");
    return data.title || word("untitled");
  };
  return <li className="min-w-0 space-y-2 rounded-control bg-inset p-3" data-activity-change={change.kind}>
    <div className="flex min-w-0 flex-wrap items-start gap-2">
      <div className="min-w-0 flex-1">
        <p className="break-words text-sm font-medium text-ink [overflow-wrap:anywhere]">{title || kindLabel(change.kind)}</p>
        {title && <p className="mt-1 text-xs text-ink-3">{kindLabel(change.kind)}</p>}
      </div>
      {card && onOpenCard && <Button size="sm" onClick={() => onOpenCard(card.id)} aria-label={word("open_workspace_named", { title: card.title })}>{word("open_workspace")}</Button>}
    </div>
    {shown.length > 0 && <dl className="min-w-0 space-y-2">{shown.map((field) => <div key={field} className="min-w-0">
      <dt className="mb-1 text-xs font-medium text-ink-2">{word(`field_${field}`)}</dt>
      <dd className="grid min-w-0 gap-2 sm:grid-cols-2">
        {(["before", "after"] as const).map((side) => <div key={side} className="min-w-0 rounded-control bg-surface px-3 py-2">
          <span className="mb-1 block text-xs text-ink-3">{word(side)}</span>
          <span className="block whitespace-pre-wrap break-words text-sm text-ink [overflow-wrap:anywhere]">{value(side, field)}</span>
        </div>)}
      </dd>
    </div>)}</dl>}
    {change.card_ids?.length ? <p className="text-xs text-ink-2">{word("order_count", { count: change.card_ids.length })}</p> : null}
    {!shown.length && !change.card_ids?.length && <p className="text-xs text-ink-2">{word("details_unavailable")}</p>}
  </li>;
}
