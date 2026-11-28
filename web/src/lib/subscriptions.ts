// Source subscriptions (#2041, docs/extensions/subscriptions.md "Pages"): the manager draws the pages as declarative UI
// (POST /api/v1/subscriptions/-/ui); the web opens them at /subscriptions and shows entry rows, once the manager reports
// that subscriptions deliver to the Inbox and the knowledge base (feature source_subscriptions).
import { useEffect, useState } from "react";
import { api } from "./api";
import { itemHeaders } from "./manager-choice";
import { onHubEvent, useStore } from "./store";

export const SUBSCRIPTIONS_FEATURE = "source_subscriptions";
/** The manager's pages are Inbox Sources (#5278): built-in sources beside the subscriptions; Settings has its row. */
export const INBOX_SOURCES_FEATURE = "inbox_sources";
/** The manager answers GET /api/v1/subscriptions/-/graph (#5741): Inbox Sources has its List / Relationships switch. */
export const INBOX_SOURCES_GRAPH_FEATURE = "inbox_sources_graph";

export { SUBSCRIPTIONS_UI } from "./subscriptions-ui";

/** Reserved connector ID for device push sources (iOS Shortcuts, #3848). */
export const PUSH_SUBSCRIPTION_CONNECTOR = "-shortcuts";

export type SubscriptionState =
  | "off"
  | "ok"
  | "failing"
  | "not_connected"
  | "rate_limited"
  | "needs_recipe"
  | (string & {});

export type SubscriptionAlertState = "failing" | "needs_recipe" | "rate_limited" | "not_connected";

export interface SubscriptionProblem {
  code: string;
  message: string;
}

/** One subscription returned by GET /api/v1/subscriptions. */
export interface HubSubscription {
  id: string;
  connector: string;
  extension: string;
  source: string;
  source_name: string;
  icon?: string;
  service?: string;
  label: string;
  recipe_origin: string;
  to_inbox: boolean;
  to_kb: boolean;
  state: SubscriptionState;
  error?: SubscriptionProblem | null;
  fail_count?: number;
  last_ok_at?: string | null;
  last_run_at?: string | null;
  paused_until?: string | null;
}

/** Whether a subscription is a standalone device push source (such as iOS Shortcuts). */
export function isPushSubscription(sub: Pick<HubSubscription, "connector" | "recipe_origin">): boolean {
  return sub.connector === PUSH_SUBSCRIPTION_CONNECTOR || sub.recipe_origin === "push";
}

/** Whether a subscription is currently in a pull error or warning state that needs user attention. */
export function isSubscriptionAlertState(state: string): state is SubscriptionAlertState {
  return state === "failing" || state === "needs_recipe" || state === "rate_limited" || state === "not_connected";
}

/** Encodes the declarative UI state JSON for opening a single subscription's settings page in SourcesUI. */
export function subscriptionSourceState(id: string): string {
  return JSON.stringify({ p: "source", id });
}

/** Reads all subscriptions from the hub (or a specific manager when provided). */
export async function listSubscriptions(manager?: string): Promise<HubSubscription[]> {
  const res = await api<{ subscriptions?: HubSubscription[] }>("/subscriptions", manager ? { headers: itemHeaders(manager) } : undefined);
  return Array.isArray(res.subscriptions) ? res.subscriptions : [];
}

/** A connector's line of GET /api/v1/subscriptions/summary: subscribable is absent until its catalog was read. */
export interface SubscriptionSummaryLine {
  connector: string;
  extension: string;
  subscribable?: boolean;
  on: number;
  failing: number;
}

/** What an entry row says: how many are on and how many need attention. */
export interface SubscriptionCounts {
  on: number;
  failing: number;
}

/** Whether the manager shows subscriptions (they deliver). */
export function useSubscriptionsShown(): boolean {
  return useStore((s) => s.features.includes(SUBSCRIPTIONS_FEATURE));
}

/** Whether the manager draws Inbox Sources (Settings' row, the Inbox menu's item and the page's title say so). */
export function useInboxSourcesShown(): boolean {
  return useStore((s) => s.features.includes(INBOX_SOURCES_FEATURE));
}

/** Whether Inbox Sources offers its relationship view. */
export function useInboxSourcesGraphShown(): boolean {
  return useStore((s) => s.features.includes(INBOX_SOURCES_GRAPH_FEATURE));
}

/** The summary, read when subscriptions are shown and again whenever one changes (at most once a second); null before. */
export function useSubscriptionSummary(manager?: string): SubscriptionSummaryLine[] | null {
  const shown = useSubscriptionsShown();
  const [summary, setSummary] = useState<{ manager?: string; lines: SubscriptionSummaryLine[] } | null>(null);
  useEffect(() => {
    setSummary(null);
    if (!shown) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let sequence = 0;
    const load = () => {
      const read = ++sequence;
      return api<{ connectors?: SubscriptionSummaryLine[] }>("/subscriptions/summary", { headers: itemHeaders(manager) }).then(
        (r) => alive && read === sequence && setSummary({ manager, lines: Array.isArray(r.connectors) ? r.connectors : [] }),
        () => {}, // an entry row is a convenience: on a failure it keeps what it had (or stays hidden)
      );
    };
    load();
    const off = onHubEvent("subscription", () => {
      clearTimeout(timer);
      timer = setTimeout(load, 1000);
    });
    return () => {
      alive = false;
      clearTimeout(timer);
      off();
    };
  }, [shown, manager]);
  return shown && summary && summary.manager === manager ? summary.lines : null;
}

/** The counts for the lines one entry row stands for; null when none of them can be subscribed (the row is hidden). */
export function countsOf(lines: SubscriptionSummaryLine[] | null, pick: (l: SubscriptionSummaryLine) => boolean): SubscriptionCounts | null {
  const mine = (lines ?? []).filter(pick);
  if (!mine.some((l) => l.subscribable === true || l.on > 0)) return null;
  return mine.reduce((c, l) => ({ on: c.on + l.on, failing: c.failing + l.failing }), { on: 0, failing: 0 });
}

/** The pages' first state: all of them, one extension's or one connector's. */
export type SubscriptionsScope = { ext: string } | { c: string } | undefined;

export function subscriptionsHref(scope?: SubscriptionsScope): string {
  return scope ? `/subscriptions?state=${encodeURIComponent(JSON.stringify(scope))}` : "/subscriptions";
}
