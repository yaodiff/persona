import { currentLanguage, t } from "../i18n";
import { api, ApiError, credentialOwner, getToken, HubTimeoutError, invalidateInFlightGets, onCredentialChange, READ_TIMEOUT_MS } from "./api";
import { BASE } from "./base";
import { BUILTIN_MANAGER, chosenManager, MANAGER_HEADER, managerHeaderFor, shownManager } from "./manager-choice";
import { currentRoster, listManagers, multiManager, unsupported, type ManagerList } from "./managers";

export type AssistantScope = Readonly<{ kind: "current" } | { kind: "owned"; managerId: string }>;
export const currentAssistant = { kind: "current" } as const;
export const ownedAssistant = (managerId: string): AssistantScope => ({ kind: "owned", managerId });

export type AssistantResource = Readonly<{ kind: string; id: string }>;
type Mutation = { method: "POST" | "PUT" | "PATCH" | "DELETE"; json?: unknown };

export interface ScopedAssistantClient {
  readonly managerId: string;
  readonly key: string;
  request<T>(path: string, mutation?: Mutation, legacy?: () => Promise<T>): Promise<T>;
  subscribe(listener: () => void): () => void;
}

export class AssistantScopeUnverified extends Error {
  constructor() { super(t("lib-api.hub_no_response")); }
}

interface Read {
  controller: AbortController;
  promise: Promise<unknown>;
}

const reads = new Map<string, Map<string, Read>>();
const subscribers = new Map<string, Set<() => void>>();
let legacyOwner: string | undefined;
let rosterUsed = false;
let retiredRoster: ManagerList | null | undefined;

function detachReads(key: string) {
  const bucket = reads.get(key);
  reads.delete(key);
  bucket?.forEach(({ controller }) => controller.abort());
}

onCredentialChange(() => {
  if (rosterUsed) retiredRoster = currentRoster();
  legacyOwner = undefined;
  reads.forEach((_, key) => detachReads(key));
  subscribers.clear();
});

function rosterForCredential(): ManagerList | null {
  rosterUsed = true;
  const roster = currentRoster();
  return roster === retiredRoster ? null : roster;
}

function assertCredential(owner: string) {
  if (owner !== credentialOwner()) throw new DOMException("aborted", "AbortError");
}

async function verifyRoster(owner: string): Promise<ManagerList | null> {
  if (legacyOwner === owner) return null;
  try {
    const result = await listManagers();
    if (!Array.isArray(result?.managers) || !result.default) throw new AssistantScopeUnverified();
    return result;
  } catch (e) {
    if (!unsupported(e)) throw e;
    assertCredential(owner);
    legacyOwner = owner;
    return null;
  }
}

/** A permission entry point must verify the roster again after a credential change. An older roster request may still finish. */
export function currentScopedAssistantRoster(): ManagerList | null {
  const roster = rosterForCredential();
  return retiredRoster === undefined ? roster : null;
}

export async function verifyScopedAssistantRoster(): Promise<ManagerList | null> {
  const owner = credentialOwner();
  const checked = await verifyRoster(owner);
  assertCredential(owner);
  return checked;
}

/** Resolve current once when opening a resource. A change of sidebar selection opens a new binding. */
export function bindScopedAssistant(scope: AssistantScope, { kind, id }: AssistantResource): ScopedAssistantClient {
  const owner = credentialOwner();
  const managerId = scope.kind === "owned" ? scope.managerId : shownManager();
  // An implicit default captured before the roster is a provisional identity. Confirm it before using a named route.
  let verified = scope.kind === "owned" || !!chosenManager() || !!rosterForCredential();
  const key = JSON.stringify([globalThis.location?.origin ?? "", BASE, owner, managerId, kind, id]);

  return {
    managerId,
    key,
    async request<T>(path: string, mutation?: Mutation, legacy?: () => Promise<T>): Promise<T> {
      assertCredential(owner);
      const query = path.indexOf("?");
      if (!managerId || managerId !== managerId.trim() || (query >= 0 && new URLSearchParams(path.slice(query + 1)).has("manager"))) {
        throw new AssistantScopeUnverified();
      }
      const known = rosterForCredential();
      const checked = known ?? await verifyRoster(owner);
      assertCredential(owner);
      const roster = rosterForCredential() ?? checked;
      if (!verified && roster) {
        if (roster.default !== managerId) throw new AssistantScopeUnverified();
        verified = true;
      }
      const named = Boolean(multiManager(roster));
      if (!named) {
        if ((roster?.default ?? BUILTIN_MANAGER) !== managerId) {
          throw new ApiError(404, t("settings-managers.err_gone"), { code: "no_manager" });
        }
        // The headerless compatibility path must have no implicit choice or pin for this route.
        if (managerHeaderFor(path)) throw new AssistantScopeUnverified();
      }
      const headers = named ? { [MANAGER_HEADER]: managerId } : undefined;
      const transport = { token: getToken(), tokenOwner: owner, headers };
      // The pre-roster hub keeps the original operation through the same scope and response fences.
      const legacyRequest = roster === null ? legacy : undefined;
      const verifyResponse = () => {
        assertCredential(owner);
        const latest = rosterForCredential();
        if (!named && latest && (latest.default !== managerId || multiManager(latest))) throw new AssistantScopeUnverified();
      };

      if (mutation) {
        detachReads(key);
        let result: T;
        try {
          result = await (legacyRequest?.() ?? api<T>(path, { ...transport, ...mutation }));
          verifyResponse();
        } catch (error) {
          assertCredential(owner);
          throw error;
        } finally {
          detachReads(key);
        }
        subscribers.get(key)?.forEach((listener) => listener());
        return result;
      }

      const requestKey = JSON.stringify([path, named, currentLanguage()]);
      const bucket = reads.get(key) ?? new Map<string, Read>();
      const existing = bucket.get(requestKey);
      if (existing) {
        const value = await existing.promise;
        assertCredential(owner);
        return structuredClone(value) as T;
      }
      const controller = new AbortController();
      // A retry of a headerless request could follow a new default. Give that read its usual deadline with one attempt.
      const deadline = named ? undefined : AbortSignal.timeout(READ_TIMEOUT_MS);
      const signal = deadline ? AbortSignal.any([controller.signal, deadline]) : controller.signal;
      if (legacyRequest) invalidateInFlightGets(path); // Legacy track helpers also use api's route-level deduplication.
      const entry: Read = {
        controller,
        promise: (legacyRequest?.() ?? api<T>(path, { ...transport, signal, dedupe: false, timeout: named ? undefined : 0 }))
          .then((value) => {
            controller.signal.throwIfAborted();
            verifyResponse();
            return value;
          })
          .catch((error: unknown) => {
            if (deadline?.aborted && error === deadline.reason) throw new HubTimeoutError();
            throw error;
          })
          .finally(() => {
            bucket.delete(requestKey);
            if (!bucket.size && reads.get(key) === bucket) reads.delete(key);
          }),
      };
      bucket.set(requestKey, entry);
      reads.set(key, bucket);
      const value = await entry.promise;
      assertCredential(owner);
      return structuredClone(value) as T;
    },
    subscribe(listener) {
      if (owner !== credentialOwner()) return () => {};
      const listeners = subscribers.get(key) ?? new Set<() => void>();
      listeners.add(listener);
      subscribers.set(key, listeners);
      return () => {
        listeners.delete(listener);
        if (!listeners.size && subscribers.get(key) === listeners) subscribers.delete(key);
      };
    },
  };
}
