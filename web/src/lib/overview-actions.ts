// Actionable waits that the Overview adds to its existing Needs you collection (#12561). The outbox is device-local
// state, so only its entries for the page's visible, non-Assistant Agent sessions join the current assistant's scope.
// The same result drives both the rows and the section count; resolving the source state removes the item everywhere.
import { isPending, neverConnected } from "./add-computer";
import { disconnectExpected } from "./machine-connection-state";
import { offlineManagerOf, type ManagerList } from "./managers";
import type { OutboxEntry } from "./outbox";
import type { Machine, Session } from "./types";

export interface ActionableOutbox {
  key: string;
  mid: string;
  sid: string;
  session: Session;
  entry: OutboxEntry;
}

export interface ActionableComputer {
  key: string;
  machine: Machine;
  /** The Machines list holds trust confirmation and first-connection setup; other offline computers have a detail page. */
  setup: boolean;
}

export interface OverviewActions {
  outbox: ActionableOutbox[];
  computers: ActionableComputer[];
  count: number;
}

/** A computer the owner can make ready: trust/setup is unfinished, or it is unexpectedly offline. A planned deployment
 * disconnect is work already under way, and a computer offline only because its manager is (the Machines page says so
 * instead of offering Reconnect) has nothing to reconnect: neither is an owner action here. */
export function computerNeedsAction(machine: Machine, roster: ManagerList | null = null): boolean {
  if (isPending(machine) || neverConnected(machine)) return true;
  return !machine.online && !disconnectExpected(machine) && !offlineManagerOf(roster, machine);
}

export function overviewActions(input: {
  outbox: Record<string, OutboxEntry[]>;
  sessions: Session[];
  machines: Machine[];
  roster?: ManagerList | null;
}): OverviewActions {
  const agents = new Map(input.sessions.filter((s) => !s.assistant && !s.hidden_by_directory).map((s) => [`${s.machine_id}/${s.session_id}`, s]));
  const outbox = Object.entries(input.outbox).flatMap(([key, entries]) => {
    const session = agents.get(key);
    if (!session) return [];
    return entries.filter((entry) => entry.state === "failed").map((entry): ActionableOutbox => ({
      key: `outbox:${key}/${entry.id}`, mid: session.machine_id, sid: session.session_id, session, entry,
    }));
  }).sort((a, b) => a.entry.at - b.entry.at || a.entry.id.localeCompare(b.entry.id));
  const computers = input.machines.filter((m) => computerNeedsAction(m, input.roster ?? null)).map((machine): ActionableComputer => ({
    key: `computer:${machine.id}`, machine, setup: isPending(machine) || neverConnected(machine),
  }));
  return { outbox, computers, count: outbox.length + computers.length };
}
