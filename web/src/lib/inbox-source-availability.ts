import type { InboxStoredSources } from "./inbox-sources-wire";

/** The sources the hub offers, independent of this browser's optional-feature switches. */
export interface InboxSourceOffers {
  lark: boolean;
  mail: boolean;
}

/**
 * Whether an existing source entry or empty-state mention stays visible (#17399). Unknown from an old or mixed-version
 * hub is deliberately visible: it must never be read as an empty archive. Clients keep their own existing entry shapes;
 * Android, for example, still creates source options only from loaded records.
 */
export function inboxSourceVisibility(offers: InboxSourceOffers, stored?: InboxStoredSources | null): InboxSourceOffers {
  return {
    lark: offers.lark || (stored?.lark ?? true),
    mail: offers.mail || (stored?.mail ?? true),
  };
}

/**
 * The fixed kind-filter chips that stay in the row (#17814). Only the Mail kind chip is source-shaped: it follows the
 * same offered/retained decision as the Mail source entry. Every other kind (@Me, DM, Group, Bot, Doc, Agent) matches
 * item kinds across sources and is always offered. The input is the one closed visibility result, not a second rule.
 */
export function visibleInboxKinds<T extends { key: string }>(kindFilters: readonly T[], visibility: InboxSourceOffers): T[] {
  return kindFilters.filter((kind) => kind.key !== "mail" || visibility.mail);
}
