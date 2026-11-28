// Inbox and Settings reach the live mailbox only through its enabled provider.
import { useEffect, useState, useSyncExternalStore } from "react";
import { enabledModules, loadedModule, loadModule, subscribeModules, useModuleStates } from "../core/module-host";
import type { MailAccount } from "./agent-settings";
import { itemHeaders, useItemManager } from "./manager-choice";

export type MailReading = { kind: "full" } | { kind: "stored"; reason: "mail-off" };
const FULL: MailReading = { kind: "full" };
const STORED: MailReading = { kind: "stored", reason: "mail-off" };

const provider = (s?: Parameters<typeof enabledModules>[0]) => enabledModules(s).find((m) => m.mailbox);
export function mailReading(s?: Parameters<typeof enabledModules>[0]): MailReading {
  return provider(s) ? FULL : STORED;
}

export function useMailReading(): MailReading {
  return useSyncExternalStore(subscribeModules, mailReading, mailReading);
}

export function useMailboxEnabled(): boolean {
  return useMailReading().kind === "full";
}

/** A failed module load leaves its archived body available. Remounting retries the import. */
export function useMailbox() {
  useModuleStates();
  const id = provider()?.id;
  const mailbox = id ? loadedModule(id)?.mailbox : undefined;
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    setFailed(false);
    if (!id || mailbox) return;
    let live = true;
    loadModule(id).catch(() => { if (live) setFailed(true); });
    return () => { live = false; };
  }, [id, mailbox]);
  return { mailbox, loading: !!id && !mailbox && !failed };
}

/** Whether `item`'s replies would be mail the hub can no longer send (`reading` is `stored`). */
export function mailItemReadOnly(reading: MailReading, item?: { source: string; mail?: unknown }): boolean {
  return reading.kind === "stored" && Boolean(item && (item.mail || item.source === "mail"));
}

// by manager ("": the default's): each manager keeps its own mail accounts
const known = new Map<string, MailAccount[]>();
const inFlight = new Map<string, Promise<MailAccount[]>>();
const accountsKey = (manager?: string) => (itemHeaders(manager) ? manager! : "");

/** The mail accounts of manager (none: the default's), read again on every call (calls while one is out share it);
 *  the last answer is kept. */
export function loadMailAccounts(manager?: string): Promise<MailAccount[]> {
  const module = provider();
  if (!module) return Promise.resolve([]);
  const key = accountsKey(manager);
  let p = inFlight.get(key);
  if (!p) {
    const mailbox = loadedModule(module.id)?.mailbox;
    const request = mailbox ? mailbox.accounts(manager) : loadModule(module.id)
      .then((code) => provider() === module ? code.mailbox!.accounts(manager) : []);
    p = request
      .then((list) => {
        known.set(key, list);
        return list;
      })
      .finally(() => inFlight.delete(key));
    inFlight.set(key, p);
  }
  return p;
}

/** The accounts of the manager whose item is shown (ItemManager; none: the default's), or of `owner.manager` when given
 *  (a new mail on the Inbox page: the current assistant's), the last known ones at once and the hub's answer when it
 *  comes. */
export function useMailAccounts(owner?: { manager: string | undefined }): MailAccount[] {
  const enabled = useMailboxEnabled();
  const shown = useItemManager();
  const manager = owner ? owner.manager : shown;
  const key = accountsKey(manager);
  const [state, setState] = useState(() => ({ key, list: known.get(key) ?? [] }));
  useEffect(() => {
    let live = true;
    loadMailAccounts(manager)
      .then((list) => live && setState({ key, list }))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [manager, key, enabled]);
  if (!enabled) return [];
  // A same-id account under another manager is not evidence of this item's receiving address.
  return state.key === key ? state.list : known.get(key) ?? [];
}

/** Tests only. */
export function resetMailForTests() {
  known.clear();
  inFlight.clear();
}
