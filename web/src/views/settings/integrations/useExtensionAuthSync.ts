// Unified extension auth-change synchronization (#17803):
// keeps detail.auth reconciled across AuthGroup actions, pushed extension_auth events,
// and non-resumed reconnects while triggering stale-safe refreshes of dependent snapshots
// (such as associated hub connectors) under the same owning assistant scope.
import { useCallback, useEffect, useRef } from "react";
import { getAuth, onAuthChange, type ExtensionAuth } from "../../../lib/extension-auth";
import type { ScopedAssistantClient } from "../../../lib/scoped-assistant-api";
import { isResumed } from "../../../lib/reconnect";
import { onHubEvent } from "../../../lib/store";

function sameAuthSnapshot(a?: ExtensionAuth, b?: ExtensionAuth): boolean {
  if (!a || !b) return false;
  return (
    a.state === b.state &&
    (a.account ?? "") === (b.account ?? "") &&
    (a.message ?? "") === (b.message ?? "") &&
    (a.expires_at ?? "") === (b.expires_at ?? "") &&
    (a.checked_at ?? "") === (b.checked_at ?? "")
  );
}

export interface UseExtensionAuthSyncOptions {
  /** Extension ID owned by this detail view. */
  id: string;
  client: ScopedAssistantClient;
  /** Current auth snapshot from detail (used for reconnect subscription and pushed-change detection). */
  auth?: ExtensionAuth;
  /** Apply an authoritative auth snapshot to the detail state. */
  onAuth: (auth: ExtensionAuth) => void;
  /** Refresh dependent snapshots (e.g., associated hub connectors) after an auth transition. */
  onSync?: () => void;
}

export function useExtensionAuthSync({
  id,
  client,
  auth,
  onAuth,
  onSync,
}: UseExtensionAuthSyncOptions) {
  const sequence = useRef(0);
  const alive = useRef(true);
  const latestAuth = useRef<ExtensionAuth | undefined>(auth);
  const onAuthRef = useRef(onAuth);
  const onSyncRef = useRef(onSync);
  onAuthRef.current = onAuth;
  onSyncRef.current = onSync;

  useEffect(() => {
    if (auth) latestAuth.current = auth;
  }, [auth]);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      sequence.current++;
    };
  }, [id, client]);

  const authChanged = useCallback((next?: ExtensionAuth) => {
    const seq = ++sequence.current;
    if (next) {
      latestAuth.current = next;
      onAuthRef.current(next);
      onSyncRef.current?.();
      return;
    }
    onSyncRef.current?.();
    void getAuth(id, client).then(
      (fresh) => {
        if (!alive.current || sequence.current !== seq) return;
        latestAuth.current = fresh;
        onAuthRef.current(fresh);
      },
      () => {},
    );
  }, [id, client]);

  useEffect(() => {
    return onAuthChange((extId, next, from) => {
      if (extId !== id || from !== client.managerId) return;
      const changed = !sameAuthSnapshot(latestAuth.current, next);
      sequence.current++;
      latestAuth.current = next;
      onAuthRef.current(next);
      if (changed) onSyncRef.current?.();
    });
  }, [id, client]);

  const signsIn = !!auth;
  useEffect(
    () => (signsIn ? onHubEvent("hello", (m) => !isResumed(m) && authChanged()) : undefined),
    [signsIn, authChanged],
  );

  return authChanged;
}
