import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { useKnownDefault } from "../../lib/manager-choice";
import type { SavedAgentSearchesModel, SavedAgentSearchesSnapshot } from "../../lib/saved-agent-searches";

export interface SavedSearchPanel { scope: string; returnQuery: string; initialQuery?: string }
const empty: SavedAgentSearchesSnapshot = { document: null, loading: false, busy: false, error: "", readonly: false, unsupported: false, canUndo: false, conflict: false };
const idle = () => empty;
const noSubscription = () => () => {};

/** The palette is kept mounted by the shell. Its saved-search code and network work start only on an open search. */
export function useSavedSearches(open: boolean, panel: SavedSearchPanel | null) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const manager = useKnownDefault();
  const scope = JSON.stringify([owner, manager]);
  const active = open || panel?.scope === scope;
  const [module, setModule] = useState<typeof import("./SavedSearchesDialog") | null>(null);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!active || module) return;
    let live = true;
    void import("./SavedSearchesDialog").then((loaded) => { if (live) { setModule(loaded); setFailed(false); } }, () => { if (live) setFailed(true); });
    return () => { live = false; };
  }, [active, module, attempt]);
  const model = useMemo<SavedAgentSearchesModel | null>(() => module && manager ? new module.SavedAgentSearchesModel() : null, [module, scope, manager]);
  const state = useSyncExternalStore(model?.subscribe ?? noSubscription, model?.snapshot ?? idle, model?.snapshot ?? idle);
  useEffect(() => {
    if (!active || !model || !module) return;
    return module.followSavedSearches(model);
  }, [active, model, module]);
  return { owner, scope, model, state, module, failed, retry: () => { setFailed(false); setAttempt((value) => value + 1); } };
}
