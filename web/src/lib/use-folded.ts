// A fold kept per browser (`fold:<key>` is "1" or "0"): the sidebar's sections and the "Terminal" page's tree share it.
import { createContext, useContext, useEffect, useState } from "react";
import { foldedNow } from "./fold";
import { foldDecided, FoldMissed, missedFoldAll } from "./fold-all";
import { onFoldAll } from "./sidebar-api";

// follow: until the user folds or opens it, the section takes its default as it is now, not as it was on the first
// render ("Unassigned" is folded only once the todos above it have loaded, #2249); other sections keep their first default.
// holds: the section has the open conversation's row (#2283); it opens when it comes to hold it (opening the
// conversation from anywhere shows its row), without storing that, and can still be folded by hand after.
// all: "Collapse / expand all" moves it too; off for the blocks that hold the groups (Pages, Agents, #2524), so folding every
// group still leaves their titles to read.
// scope: the block the group is in, so the block's own "Collapse all" / "Expand all" moves only its groups (#3027); taken from the
// FoldScope around it unless given (a block's own groups, above its provider).
// A group mounting where it was not drawn a moment ago (under a folded group, in a long group's part coming into view)
// takes the "Collapse / expand all" it missed meanwhile (lib/fold-all.ts, #7440).
export const FoldScope = createContext<string | undefined>(undefined);

export function useFolded(key: string, initial = false, { follow = false, holds = false, all = true, scope }: { follow?: boolean; holds?: boolean; all?: boolean; scope?: string } = {}): [boolean, () => void] {
  const around = useContext(FoldScope);
  const within = scope ?? around;
  const region = useContext(FoldMissed);
  // what is kept, or the fold-all this group missed while it was not drawn, kept from now on as if it had been here
  const read = (k: string) => {
    const missed = all ? missedFoldAll(k, within, region.since) : undefined;
    if (missed === undefined) return localStorage.getItem(`fold:${k}`);
    const next = missed ? "1" : "0";
    localStorage.setItem(`fold:${k}`, next);
    return next;
  };
  const [stored, setStored] = useState(() => read(key));
  const [first, setFirst] = useState(initial);
  const [opened, setOpened] = useState(holds);
  const [held, setHeld] = useState(holds);
  const [seen, setSeen] = useState(key);
  if (seen !== key) {
    // a mounted row given another key (a re-grouping, a renamed space) starts over from that key's fold, as on mount
    setSeen(key);
    setStored(read(key));
    setFirst(initial);
    setOpened(holds);
    setHeld(holds);
  } else if (held !== holds) {
    // adjusted while rendering, so the row is laid out (and scrolled to) in the same frame
    setHeld(holds);
    if (holds) setOpened(true);
  }
  const folded = !opened && foldedNow(stored, follow ? initial : first);
  // "Collapse / expand all" (the keyboard shortcut, lib/sidebar-api.ts)
  useEffect(() => all ? onFoldAll((folded) => {
    const next = folded ? "1" : "0";
    localStorage.setItem(`fold:${key}`, next);
    foldDecided(key);
    setStored(next);
    setOpened(false);
  }, within) : undefined, [key, all, within]);
  return [
    folded,
    () => {
      const next = folded ? "0" : "1";
      localStorage.setItem(`fold:${key}`, next);
      foldDecided(key);
      setStored(next);
      setOpened(false);
    },
  ];
}
