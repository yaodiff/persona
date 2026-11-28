// Mounted once from the app root. This file stays on the first screen, so it only reads the hub's feature list:
// the listener loads once the hub says `text_lookup`, and the card only once a word is clicked.
import { lazy, Suspense, useEffect, useState } from "react";
import { getState, onHubEvent, useStore } from "../lib/store";
import type { LookupOpen } from "./listen";

const LookupPopover = lazy(() => import("./popover"));

/** client.ts TEXT_LOOKUP_FEATURE, repeated so the first screen doesn't pull in the client. */
const FEATURE = "text_lookup";

export function TextLookupHost() {
  const on = useStore((s) => s.features.includes(FEATURE));
  const [hit, setHit] = useState<LookupOpen | null>(null);
  useEffect(() => {
    if (!on) return;
    let stop = () => {};
    let live = true;
    void Promise.all([import("./listen"), import("./client")]).then(([{ startTextLookup }, { textLookupApi }]) => {
      if (!live) return;
      const open = (next: LookupOpen) => {
        void import("./popover");
        setHit(next);
      };
      stop = startTextLookup({ features: () => getState().features, onHub: onHubEvent, api: textLookupApi(), open });
    });
    return () => {
      live = false;
      stop();
      setHit(null);
    };
  }, [on]);
  if (!hit) return null;
  return (
    <Suspense fallback={null}>
      <LookupPopover hit={hit} onClose={() => setHit(null)} />
    </Suspense>
  );
}
