import { useMemo, useSyncExternalStore } from "react";
import { credentialOwner, subscribeCredentialOwner } from "../../../lib/api";
import { useShownManager } from "../../../lib/manager-choice";
import { multiManager, useRoster } from "../../../lib/managers";
import { bindScopedAssistant, currentAssistant, ownedAssistant, type AssistantScope } from "../../../lib/scoped-assistant-api";

export function useScopedAssistant(scope: AssistantScope, kind: string, id: string) {
  const credential = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const shown = useShownManager();
  const explicit = scope.kind === "owned" ? scope.managerId : undefined;
  const managerId = explicit ?? shown;
  const roster = useRoster();
  const client = useMemo(() => bindScopedAssistant(explicit === undefined ? currentAssistant : ownedAssistant(explicit), { kind, id }),
    [credential, explicit, managerId, kind, id]);
  const record = roster?.managers.find((m) => m.id === managerId);
  const named = Boolean(multiManager(roster));
  const routing = JSON.stringify([roster !== null, named, named ? undefined : shown, record?.online, record?.installing]);
  return { client, routing, roster };
}
