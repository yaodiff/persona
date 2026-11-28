// Last list views live only in this tab. The hub URL includes its reverse-proxy prefix;
// the hello identifies the hub behind it, and the roster identifies the implicit manager.
import { useSyncExternalStore } from "react";
import { credentialOwner, getToken, onCredentialChange, subscribeCredentialOwner } from "./api";
import { BASE } from "./base";
import { chosenManager, defaultManager, onManagerChoice } from "./manager-choice";
import { getState, useStore, type State } from "./store";

const selfHub = (s: State) => s.mesh.find((hub) => hub.self)?.hub_id ?? "";
const owner = () => JSON.stringify([globalThis.location?.origin, BASE, credentialOwner(), defaultManager(), chosenManager()]);
const key = (hub: string, owner: string) => JSON.stringify([hub, owner]);

function subscribeOwner(changed: () => void) {
  const stopCredential = subscribeCredentialOwner(changed);
  const stopManager = onManagerChoice(changed);
  return () => { stopCredential(); stopManager(); };
}

export const currentListViewKey = () => key(selfHub(getState()), owner());
export const useListViewKey = () => key(useStore(selfHub), useSyncExternalStore(subscribeOwner, owner, owner));

export function listViewCache<T>() {
  const views = new Map<string, T>();
  onCredentialChange(() => views.clear());
  return {
    read: (scope: string) => views.get(scope),
    keep(scope: string, view: T) {
      if (getToken() && scope === currentListViewKey()) views.set(scope, view);
    },
  };
}
