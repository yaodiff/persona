// The two small dialogs the palette's node commands ask with (lib/node-palette.ts): a name for a rename, a yes for a
// close. Mounted once, with the sidebar, so it is on every first screen: only this watch of lib/node-ask.ts is; the
// dialogs (views/NodeAskDialog.tsx) load when a command first asks (#17142).
import { useSyncExternalStore } from "react";
import { answerAsk, getAsk, subscribeAsk, type NodeAsk } from "../lib/node-ask";
import { lazyOverlay } from "./LazyOverlay";

const NodeAskDialog = lazyOverlay(() => import("./NodeAskDialog").then((m) => m.NodeAskDialog));

/** A question whose dialog could not load (offline, a redeploy) is answered as cancelled, so its command goes on. */
function cancel(ask: NodeAsk) {
  answerAsk();
  if (ask.kind === "text") ask.resolve(null);
  else ask.resolve(false);
}

export function NodePrompt() {
  const ask = useSyncExternalStore(subscribeAsk, getAsk, getAsk);
  if (!ask) return null;
  return <NodeAskDialog ask={ask} onClose={() => cancel(ask)} />;
}
