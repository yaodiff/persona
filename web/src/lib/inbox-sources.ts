// Whether anything feeds the inbox yet (docs/assistant/onboarding.md 4.5 "Rewriting the empty states"): an empty inbox with no source connected says
// "No message sources yet" and lists the sources a few taps connect, not "All caught up". A source is connected when the hub has a mail
// account, Feishu set up (feature lark), or a connector that brings items in and is signed in.
import { useEffect, useState } from "react";
import { t } from "../i18n";
import { api } from "./api";
import { isEnabled, isSignedIn, listConnectors, otherConnectors, signsInHere, storesData, type HubConnector } from "./connectors";
import { loadMailAccounts, useMailboxEnabled } from "./mail";
import { ownedSub } from "./manager-choice";
import { larkOn, useStore } from "./store";

/** A source to connect from the empty inbox, and the settings page that does it. */
export interface SourceOffer {
  id: string;
  name: string;
  href: string;
}

export interface SourcesInput {
  /** Mail accounts, or undefined when live mail is unavailable. */
  mail?: number;
  /** Feishu: undefined on a hub without it (feature lark), else whether it is set up. */
  lark?: boolean;
  connectors: HubConnector[];
}

const MAX_CONNECTOR_OFFERS = 3;

const connectorsHref = (id?: string, manager?: string) => `/settings?tab=connectors${id ? `&connector=${encodeURIComponent(ownedSub(id, manager))}` : ""}`;

/** A connector that brings items in and is switched on and signed in feeds the inbox (its `inbox` switch, when it has one). */
function feeds(c: HubConnector): boolean {
  return isEnabled(c) && isSignedIn(c) && storesData(c) && c.inbox !== false;
}

/** Whether a source is connected, and the few-tap ones to offer: "Mail" first, Lark where the hub has it, then the
 *  connectors that sign in here (a QR code, a device code) and bring items in. */
export function inboxSources(input: SourcesInput): { connected: boolean; offers: SourceOffer[] } {
  const others = otherConnectors(input.connectors, input.lark !== undefined);
  const connected = (input.mail ?? 0) > 0 || input.lark === true || others.some(feeds);
  const offers: SourceOffer[] = input.mail === undefined ? [] : [{ id: "mail", name: t("lib-inbox-sources.mail"), href: connectorsHref() }];
  if (input.lark === false) offers.push({ id: "lark", name: t("lib-inbox-sources.lark"), href: connectorsHref() });
  const signIns = others.filter((c) => signsInHere(c) && storesData(c) && !isSignedIn(c)).slice(0, MAX_CONNECTOR_OFFERS);
  for (const c of signIns) offers.push({ id: c.id, name: c.name, href: connectorsHref(c.id, c.manager) });
  return { connected, offers };
}

/** The sources, read once `on` (an empty inbox); null while unknown or when the hub could not say (the inbox then
 *  keeps its plain "All caught up"). */
export function useInboxSources(on: boolean): { connected: boolean; offers: SourceOffer[] } | null {
  const lark = useStore(larkOn);
  const mailOn = useMailboxEnabled();
  const hasConnectors = useStore((s) => s.features.includes("connectors"));
  const [sources, setSources] = useState<{ connected: boolean; offers: SourceOffer[] } | null>(null);
  useEffect(() => {
    if (!on) return;
    let live = true;
    Promise.all([
      mailOn ? loadMailAccounts() : Promise.resolve(undefined),
      lark ? api<{ configured?: boolean }>("/lark/status").then((s) => !!s.configured) : Promise.resolve(undefined),
      hasConnectors ? listConnectors() : Promise.resolve([] as HubConnector[]),
    ])
      .then(([mail, larkSet, connectors]) => live && setSources(inboxSources({ mail: mail?.length, lark: larkSet, connectors })))
      .catch(() => live && setSources(null));
    return () => {
      live = false;
    };
  }, [on, lark, mailOn, hasConnectors]);
  return on ? sources : null;
}
