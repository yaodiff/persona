import { useEffect, useSyncExternalStore } from "react";
import { isResumed } from "../../../lib/reconnect";
import { onHubEvent } from "../../../lib/store";
import { emptyConfiguration, type ResourceConfiguration } from "./resourceConfiguration";

const idle = () => () => {};
const empty = () => emptyConfiguration;

/** Refresh the same resource on an open, a route becoming reachable, or a reconnect with missed updates. */
export function useResourceConfiguration<Remote extends { id: string }, Draft>(
  resource: ResourceConfiguration<Remote, Draft> | undefined, routing: string, reconnect?: () => void,
) {
  const state = useSyncExternalStore(resource?.subscribe ?? idle, resource?.snapshot ?? empty);
  useEffect(() => {
    if (!resource) return;
    void resource.refresh();
    const stopChanges = resource.client.subscribe(() => {
      if (!resource.snapshot().saving) void resource.refresh();
    });
    const stopHello = onHubEvent("hello", (message) => {
      if (!isResumed(message)) void (reconnect ?? resource.refresh)();
    });
    return () => { stopChanges(); stopHello(); };
  }, [resource, routing, reconnect]);
  return state;
}
