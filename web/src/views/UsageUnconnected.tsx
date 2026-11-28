// Active model-switcher providers without subscription usage windows yet, loaded after mount by SidebarUsage so the
// cold first screen stays within FIRST_SCREEN_BUDGET (#10082, #10349).
import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { errorText } from "../lib/api";
import { assistantLaunchers, readAssistantLaunchers, useModelLists } from "../lib/session-models";
import { sameValue } from "../lib/stable-lines";
import { useStore } from "../lib/store";
import { canonicalUsageProvider, liveAccounts, useHiddenUsageProviders, useUsageWindowsReady, type UnconnectedProvider, type UsageAccount } from "../lib/usage-windows";

export interface UsageProviderCandidate {
  name: string;
  display_name?: string;
  agent?: string;
  agent_type?: string;
  base_url?: string;
}

export interface UsageUnconnectedViewState {
  providers: readonly UnconnectedProvider[];
  loading: boolean;
  error?: string;
}

export interface UsageUnconnectedViewProps {
  providers: readonly UnconnectedProvider[];
  loading?: boolean;
  error?: string;
  render?: (state: UsageUnconnectedViewState) => ReactNode;
}

export interface UsageUnconnectedDeps {
  /** Optional scope partition (value or getter); switching it resets state, aborts stale reads, and fences callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled target machine id (value or getter; bypasses `useStore` when supplied). */
  machineId?: string | (() => string);
  /** Subscribe to machine id updates when `machineId` is a getter. */
  subscribeMachineId?: (listener: () => void) => () => void;
  /** Controlled `/usage/windows` readiness (value or getter; bypasses `useUsageWindowsReady` when supplied). */
  usageReady?: boolean | (() => boolean);
  /** Subscribe to readiness updates when `usageReady` is a getter. */
  subscribeUsageReady?: (listener: () => void) => () => void;
  /** Controlled hidden provider IDs (value or getter; bypasses `useHiddenUsageProviders` when supplied). */
  hiddenProviders?: readonly string[] | (() => readonly string[]);
  /** Subscribe to hidden provider updates when `hiddenProviders` is a getter. */
  subscribeHiddenProviders?: (listener: () => void) => () => void;
  /** Controlled launcher list or per-machine getter (bypasses `useModelLists` and `assistantLaunchers` when supplied). */
  launchers?: readonly UsageProviderCandidate[] | null | ((mid: string) => readonly UsageProviderCandidate[] | undefined | null);
  /** Subscribe to launcher list updates when `launchers` is a getter. */
  subscribeLaunchers?: (listener: () => void) => () => void;
  /** Trigger reading launchers for `mid` (defaults to `readAssistantLaunchers` only when `launchers` is omitted). */
  readLaunchers?: (mid: string, signal: AbortSignal) => void | Promise<unknown>;
  /** Fixed timestamp or clock reader (ms) for `liveAccounts` (defaults to `Date.now`). */
  now?: number | (() => number);
  /** Format launcher read failures (defaults to `api.errorText`). */
  formatError?: (error: unknown) => string;
  /** Error observer invoked when `readLaunchers` throws or rejects in the active scope. */
  onError?: (message: string) => void;
  /** Optional custom view renderer passed to `UsageUnconnectedView`. */
  render?: (state: UsageUnconnectedViewState) => ReactNode;
}

export interface UsageUnconnectedProps {
  accounts: UsageAccount[];
  onChange: (next: readonly UnconnectedProvider[]) => void;
  deps?: UsageUnconnectedDeps;
}

const BUILTIN_TITLES: Readonly<Record<string, string>> = {
  claude: "Claude Code",
  codex: "Codex",
  antigravity: "Antigravity",
};

const EMPTY_PROVIDERS: readonly UnconnectedProvider[] = [];
const noopSubscribe = () => () => {};

/** Providers offered in the model switcher that do not yet have a live subscription account in `live`, leaving out the
 *  ones the owner hid from the bars. A relay launcher matches by name or source. */
export function unconnectedUsageProviders(launchers: readonly UsageProviderCandidate[], live: readonly UsageAccount[], hidden: readonly string[] = []): UnconnectedProvider[] {
  const out: UnconnectedProvider[] = [];
  const seen = new Set<string>();
  const hiddenProviders = new Set(hidden.map(canonicalUsageProvider));
  const connectedProviders = new Set(live.map((a) => canonicalUsageProvider(a.provider)));
  for (const l of launchers) {
    if (!l.name || seen.has(l.name)) continue;
    seen.add(l.name);
    const launcherProvider = canonicalUsageProvider(l.name);
    const title = l.display_name?.trim() || BUILTIN_TITLES[launcherProvider] || l.name;
    const agent = (l.agent_type || l.agent || "claude").toLowerCase();
    const provider = canonicalUsageProvider(agent);
    if (hiddenProviders.has(launcherProvider) || hiddenProviders.has(canonicalUsageProvider(title)) ||
        (!l.base_url && hiddenProviders.has(provider))) continue;
    const idLower = l.name.toLowerCase();
    const titleLower = title.toLowerCase();
    const matchedByProvider = !l.base_url && Object.hasOwn(BUILTIN_TITLES, provider) && connectedProviders.has(provider);
    const matchedByName = live.some(
      (a) =>
        a.name.toLowerCase() === titleLower ||
        a.name.toLowerCase() === idLower ||
        a.sources?.some((s) => s.toLowerCase() === titleLower || s.toLowerCase() === idLower),
    );
    if (matchedByProvider || matchedByName) continue;
    out.push({ id: l.name, title, agent, ...(l.base_url ? { base_url: l.base_url } : {}) });
  }
  return out;
}

/** Pure presentational view for unconnected usage providers (renders null unless a custom `render` callback is supplied). */
export function UsageUnconnectedView({ providers, loading = false, error, render }: UsageUnconnectedViewProps) {
  return render ? <>{render({ providers, loading, error })}</> : null;
}

function useTrackedScope(subscribe: (listener: () => void) => () => void, read: () => string) {
  const stateRef = useRef<{ value: string; version: number } | null>(null);
  const sub = useCallback((listener: () => void) => subscribe(() => {
    const next = read();
    if (!stateRef.current || next !== stateRef.current.value) {
      stateRef.current = { value: next, version: (stateRef.current?.version ?? 0) + 1 };
    }
    listener();
  }), [subscribe, read]);
  const getSnapshot = useCallback(() => {
    const next = read();
    if (!stateRef.current) {
      stateRef.current = { value: next, version: 0 };
    } else if (next !== stateRef.current.value) {
      stateRef.current = { value: next, version: stateRef.current.version + 1 };
    }
    return stateRef.current;
  }, [read]);
  const snap = useSyncExternalStore(sub, getSnapshot, getSnapshot);
  const readToken = useCallback(() => {
    const cur = read();
    const s = stateRef.current;
    return s && s.value === cur ? `${s.version}:${cur}` : `drift:${cur}`;
  }, [read]);
  return { value: snap.value, token: `${snap.version}:${snap.value}`, readToken };
}

function useCachedExternalValue<T>(
  source: T | (() => T) | undefined,
  subscribe: ((listener: () => void) => () => void) | undefined,
): T | undefined {
  const cache = useRef<{ has: boolean; value: T | undefined }>({ has: false, value: undefined });
  const getSnapshot = useCallback((): T | undefined => {
    const next = typeof source === "function" ? (source as () => T)() : source;
    if (cache.current.has && sameValue(cache.current.value, next)) {
      return cache.current.value;
    }
    cache.current = { has: true, value: next };
    return next;
  }, [source]);
  const sub = subscribe && typeof source === "function" ? subscribe : noopSubscribe;
  return useSyncExternalStore(sub, getSnapshot, getSnapshot);
}

function useCachedSnapshot<T>(
  read: () => T,
  subscribe: ((listener: () => void) => () => void) | undefined,
): T {
  const cache = useRef<{ has: boolean; value: T | undefined }>({ has: false, value: undefined });
  const getSnapshot = useCallback((): T => {
    const next = read();
    if (cache.current.has && sameValue(cache.current.value, next)) {
      return cache.current.value as T;
    }
    cache.current = { has: true, value: next };
    return next;
  }, [read]);
  return useSyncExternalStore(subscribe ?? noopSubscribe, getSnapshot, getSnapshot);
}

function DefaultMachineIdBridge({ render }: { render: (mid: string) => ReactNode }) {
  const mid = useStore((s) => s.assistant?.machine_id || s.machines.find((m) => m.online)?.id || s.machines[0]?.id || "");
  return <>{render(mid)}</>;
}

function DefaultUsageReadyBridge({ render }: { render: (ready: boolean) => ReactNode }) {
  const usageReady = useUsageWindowsReady();
  return <>{render(usageReady)}</>;
}

function DefaultHiddenProvidersBridge({ render }: { render: (hidden: readonly string[]) => ReactNode }) {
  const hidden = useHiddenUsageProviders();
  return <>{render(hidden)}</>;
}

function DefaultLaunchersBridge({
  mid,
  render,
}: {
  mid: string;
  render: (launchers: readonly UsageProviderCandidate[] | undefined, version: number) => ReactNode;
}) {
  const version = useModelLists();
  const launchers = mid ? assistantLaunchers(mid).info?.launchers : undefined;
  return <>{render(launchers, version)}</>;
}

interface UsageUnconnectedBodyProps {
  accounts: UsageAccount[];
  onChange: (next: readonly UnconnectedProvider[]) => void;
  deps?: UsageUnconnectedDeps;
  mid: string;
  usageReady: boolean;
  hidden: readonly string[];
  defaultLaunchers?: readonly UsageProviderCandidate[];
  defaultVersion: number;
  outerScopeToken: string;
  readOuterScopeToken: () => string;
  useControlledLaunchers: boolean;
}

function UsageUnconnectedBody({
  accounts,
  onChange,
  deps,
  mid,
  usageReady,
  hidden,
  defaultLaunchers,
  defaultVersion,
  outerScopeToken,
  readOuterScopeToken,
  useControlledLaunchers,
}: UsageUnconnectedBodyProps) {
  const launchersDep = deps?.launchers;
  const readControlledLaunchers = useCallback((): readonly UsageProviderCandidate[] | undefined => {
    if (!useControlledLaunchers) return undefined;
    if (typeof launchersDep === "function") return (mid ? launchersDep(mid) : undefined) ?? undefined;
    return launchersDep ?? undefined;
  }, [useControlledLaunchers, launchersDep, mid]);
  const launchersSub = useControlledLaunchers && typeof launchersDep === "function" ? deps?.subscribeLaunchers : undefined;
  const controlledLaunchers = useCachedSnapshot(readControlledLaunchers, launchersSub);
  const launchers = useControlledLaunchers ? controlledLaunchers : defaultLaunchers;

  const effectiveScope = JSON.stringify([outerScopeToken, mid]);
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: effectiveScope, epoch: 1 }));
  const currentEpoch = scope.key === effectiveScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== effectiveScope) {
    setScope({ key: effectiveScope, epoch: currentEpoch });
  }

  const [loadingState, setLoadingState] = useState<{ epoch: number; loading: boolean }>({ epoch: 0, loading: false });
  const [errorState, setErrorState] = useState<{ epoch: number; error: string | undefined }>({ epoch: 0, error: undefined });
  const loading = loadingState.epoch === currentEpoch && loadingState.loading;
  const error = errorState.epoch === currentEpoch ? errorState.error : undefined;

  const activeEpochRef = useRef(currentEpoch);
  const readSeqRef = useRef(0);
  const depsRef = useRef(deps);
  const readScopeTokenRef = useRef(readOuterScopeToken);

  useLayoutEffect(() => {
    depsRef.current = deps;
    readScopeTokenRef.current = readOuterScopeToken;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = currentEpoch;
    return () => {
      if (activeEpochRef.current === currentEpoch) activeEpochRef.current = 0;
      readSeqRef.current += 1;
    };
  }, [currentEpoch]);

  const customReadLaunchers = deps?.readLaunchers;
  const shouldReadLaunchers = Boolean(mid) && (customReadLaunchers !== undefined || !useControlledLaunchers);
  useEffect(() => {
    if (!shouldReadLaunchers) return;
    const runEpoch = currentEpoch;
    const runToken = outerScopeToken;
    const seq = ++readSeqRef.current;
    const controller = new AbortController();
    const isRunCurrent = () =>
      !controller.signal.aborted
      && activeEpochRef.current === runEpoch
      && readSeqRef.current === seq
      && readScopeTokenRef.current() === runToken;
    setLoadingState({ epoch: runEpoch, loading: true });
    setErrorState({ epoch: runEpoch, error: undefined });
    void (async () => {
      try {
        if (customReadLaunchers) await customReadLaunchers(mid, controller.signal);
        else await readAssistantLaunchers(mid);
      } catch (e) {
        if (isRunCurrent()) {
          const msg = (depsRef.current?.formatError ?? errorText)(e);
          setErrorState({ epoch: runEpoch, error: msg });
          depsRef.current?.onError?.(msg);
        }
      } finally {
        if (activeEpochRef.current === runEpoch && readSeqRef.current === seq) {
          setLoadingState({ epoch: runEpoch, loading: false });
        }
      }
    })();
    return () => {
      controller.abort();
      readSeqRef.current += 1;
    };
  }, [shouldReadLaunchers, customReadLaunchers, mid, currentEpoch, outerScopeToken]);

  const resolvedNow = typeof deps?.now === "function" ? deps.now() : deps?.now;
  const providers = usageReady && launchers ? unconnectedUsageProviders(launchers, liveAccounts(accounts, resolvedNow), hidden) : EMPTY_PROVIDERS;

  useEffect(() => {
    if (activeEpochRef.current !== currentEpoch || readScopeTokenRef.current() !== outerScopeToken) return;
    onChange(providers);
  }, [usageReady, launchers, accounts, hidden, defaultVersion, resolvedNow, currentEpoch, outerScopeToken, onChange]);

  return <UsageUnconnectedView providers={providers} loading={loading} error={error} render={deps?.render} />;
}

export function UsageUnconnected({ accounts, onChange, deps }: UsageUnconnectedProps) {
  const scopeDep = deps?.scopeKey;
  const readScope = useCallback(() => (typeof scopeDep === "function" ? scopeDep() : (scopeDep ?? "")), [scopeDep]);
  const scopeSubscribe = deps?.subscribeScopeKey && typeof scopeDep === "function" ? deps.subscribeScopeKey : noopSubscribe;
  const trackedScope = useTrackedScope(scopeSubscribe, readScope);

  // Keep controlled inputs and scope tracking in the identity-stable outer component so direct <-> getter+subscribe switches never remount.
  const controlledMid = useCachedExternalValue(deps?.machineId, deps?.subscribeMachineId);
  const controlledUsageReady = useCachedExternalValue(deps?.usageReady, deps?.subscribeUsageReady);
  const controlledHidden = useCachedExternalValue(deps?.hiddenProviders, deps?.subscribeHiddenProviders);

  const needDefaultMid = deps?.machineId === undefined;
  const needDefaultReady = deps?.usageReady === undefined;
  const needDefaultHidden = deps?.hiddenProviders === undefined;
  const useControlledLaunchers = deps?.launchers !== undefined;

  const renderBody = (
    mid: string,
    usageReady: boolean,
    hidden: readonly string[],
    defaultLaunchers: readonly UsageProviderCandidate[] | undefined,
    defaultVersion: number,
  ) => (
    <UsageUnconnectedBody
      accounts={accounts}
      onChange={onChange}
      deps={deps}
      mid={mid}
      usageReady={usageReady}
      hidden={hidden}
      defaultLaunchers={defaultLaunchers}
      defaultVersion={defaultVersion}
      outerScopeToken={trackedScope.token}
      readOuterScopeToken={trackedScope.readToken}
      useControlledLaunchers={useControlledLaunchers}
    />
  );

  const renderWithLaunchers = (mid: string, usageReady: boolean, hidden: readonly string[]) => {
    if (!useControlledLaunchers) {
      return (
        <DefaultLaunchersBridge
          mid={mid}
          render={(launchers, version) => renderBody(mid, usageReady, hidden, launchers, version)}
        />
      );
    }
    return renderBody(mid, usageReady, hidden, undefined, 0);
  };

  const renderWithHidden = (mid: string, usageReady: boolean) => {
    if (needDefaultHidden) {
      return <DefaultHiddenProvidersBridge render={(hidden) => renderWithLaunchers(mid, usageReady, hidden)} />;
    }
    return renderWithLaunchers(mid, usageReady, controlledHidden ?? []);
  };

  const renderWithReady = (mid: string) => {
    if (needDefaultReady) {
      return <DefaultUsageReadyBridge render={(ready) => renderWithHidden(mid, ready)} />;
    }
    return renderWithHidden(mid, Boolean(controlledUsageReady));
  };

  if (needDefaultMid) {
    return <DefaultMachineIdBridge render={renderWithReady} />;
  }
  return renderWithReady(controlledMid ?? "");
}
