// Cards a task's supervisor holds. When a task has a supervisor, the permission, plan and terminal-dialog ("screen")
// cards of the Agents it runs go to that supervisor first, not to the user: the hub marks such a card `delegating: true`
// when it routes it to the supervising conversation (server/internal/assistant/ask_route.go routeAsk) and sends it again
// without the mark once the supervisor passes it up (ask_escalate); only then is it the user's. The other `delegating`
// is on questions alone, the assistant deciding whether it may answer one for the user (fleet_delegate.go,
// lib/delegate.ts isDelegating), and is not this.
//
// So the web leaves a held card out of every "Needs you" list and count, out of the tab's badge, its sounds and system
// notifications, and offers no button that answers it: it says the supervisor is handling it.
import { homeKey } from "./folded-rows";
import { stateOf } from "./format";
import type { Interaction, Session } from "./types";

/** A card the task's supervisor holds (a permission, plan, terminal dialog… marked `delegating`): not the user's until
 *  the supervisor passes it up. A delegating question is the assistant's own look (isDelegating), not this. */
export function supervisorHeld(ix: Pick<Interaction, "kind" | "delegating">): boolean {
  return ix.delegating === true && ix.kind !== "question";
}

/** The cards that are the user's: without the ones a supervisor holds (the same array when there are none). */
export function userCards<T extends Pick<Interaction, "kind" | "delegating">>(interactions: readonly T[]): T[] {
  const out = interactions.filter((ix) => !supervisorHeld(ix));
  return out.length === interactions.length ? (interactions as T[]) : out;
}

/** The sessions (by agentKey, a child's card under its parent: homeKey) whose every pending card, at least one, is
 *  held by a supervisor: they wait on the supervisor, not on the user. A session with one card of the user's is not. */
export function supervisorHeldOnly(interactions: readonly Interaction[]): Set<string> {
  const held = new Set<string>();
  const other = new Set<string>();
  for (const ix of interactions) if (!ix.mirror_of) (supervisorHeld(ix) ? held : other).add(homeKey(ix));
  for (const key of other) held.delete(key);
  return held;
}

/** The cards for one sidebar row `mid/sid` (counting folded children, homeKey): the first user-owned card (own before
 *  child), how many user-owned child cards wait, how many own cards a supervisor holds, and whether it waits only on
 *  supervisor-held cards. One pass, returning plain fields for useStore's shallowEqual. */
export function rowCards(interactions: readonly Interaction[], mid: string, sid: string) {
  let own: Interaction | undefined, kid: Interaction | undefined, kidCount = 0, ownHeld = 0, hasHeld = false;
  const key = `${mid}/${sid}`;
  for (const ix of interactions) {
    if (homeKey(ix) !== key) continue;
    const mine = ix.machine_id === mid && ix.session_id === sid;
    if (supervisorHeld(ix)) { hasHeld = true; if (mine) ownHeld++; }
    else if (mine) own ??= ix;
    else { kid ??= ix; kidCount++; }
  }
  const ix = own ?? kid;
  return { ix, kidCount, ownHeld, heldOnly: hasHeld && !ix };
}

/** The sessions as the overview lists them: one "Needs you" only for cards its supervisor holds is at work (the
 *  supervisor is), as Working, not waiting on the user. The same array when none is. */
export function heldAsWorking(sessions: Session[], interactions: readonly Interaction[]): Session[] {
  const held = supervisorHeldOnly(interactions);
  if (!held.size) return sessions;
  let changed = false;
  const out = sessions.map((s) => {
    if (s.view || stateOf(s) !== "needs_you" || !held.has(`${s.machine_id}/${s.session_id}`)) return s;
    changed = true;
    return { ...s, state: "working" as const, pending_interactions: 0 };
  });
  return changed ? out : sessions;
}
